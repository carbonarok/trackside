// Package td follows trains through Network Rail's Train Describer (TD)
// feed. Signalling berth steps are mapped to arrivals and departures with
// the SMART reference data, giving live times and positions ahead of TRUST.
package td

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Berth is one SMART record: a berth step that marks an event at a location.
type Berth struct {
	TD        string
	FromBerth string
	ToBerth   string
	FromLine  string
	ToLine    string
	Offset    int // seconds to add to the step time
	Platform  string
	Event     string // A, B, C or D
	Route     string
	STANOX    string
	StepType  string
}

// IsArrival reports whether the step marks an arrival rather than a
// departure.
func (b Berth) IsArrival() bool { return b.Event == "A" || b.Event == "C" }

type rawBerth struct {
	TD          string `json:"TD"`
	FromBerth   string `json:"FROMBERTH"`
	ToBerth     string `json:"TOBERTH"`
	FromLine    string `json:"FROMLINE"`
	ToLine      string `json:"TOLINE"`
	BerthOffset string `json:"BERTHOFFSET"`
	Platform    string `json:"PLATFORM"`
	Event       string `json:"EVENT"`
	Route       string `json:"ROUTE"`
	STANOX      string `json:"STANOX"`
	StepType    string `json:"STEPTYPE"`
}

// ParseSMART decodes a SMART JSON document.
func ParseSMART(r io.Reader) ([]Berth, error) {
	var doc struct {
		Data []rawBerth `json:"BERTHDATA"`
	}
	if err := json.NewDecoder(r).Decode(&doc); err != nil {
		return nil, fmt.Errorf("decode SMART: %w", err)
	}
	out := make([]Berth, 0, len(doc.Data))
	for _, b := range doc.Data {
		t := strings.TrimSpace
		if t(b.TD) == "" || t(b.STANOX) == "" || t(b.Event) == "" {
			continue
		}
		offset, _ := strconv.Atoi(strings.TrimPrefix(t(b.BerthOffset), "+"))
		out = append(out, Berth{
			TD: t(b.TD), FromBerth: t(b.FromBerth), ToBerth: t(b.ToBerth),
			FromLine: t(b.FromLine), ToLine: t(b.ToLine), Offset: offset,
			Platform: t(b.Platform), Event: t(b.Event), Route: t(b.Route),
			STANOX: t(b.STANOX), StepType: t(b.StepType),
		})
	}
	return out, nil
}

// LoadSMART replaces the stored SMART data.
func LoadSMART(ctx context.Context, pool *pgxpool.Pool, berths []Berth) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `DELETE FROM smart_berths`); err != nil {
		return err
	}
	rows := make([][]any, len(berths))
	for i, b := range berths {
		rows[i] = []any{b.TD, b.FromBerth, b.ToBerth, b.FromLine, b.ToLine, b.Offset,
			b.Platform, b.Event, b.Route, b.STANOX, b.StepType}
	}
	if _, err := tx.CopyFrom(ctx, pgx.Identifier{"smart_berths"},
		[]string{"td", "from_berth", "to_berth", "from_line", "to_line", "offset_secs",
			"platform", "event", "route", "stanox", "step_type"},
		pgx.CopyFromRows(rows)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Map indexes SMART data for berth step lookups.
type Map struct {
	between map[[3]string][]Berth // td, from, to: step types B and D
	from    map[[2]string][]Berth // td, from: F (any step out) and C (clearout)
	to      map[[2]string][]Berth // td, to: T (any step in) and I (interpose)
	// approach maps a berth to the STANOXes a train in it is approaching:
	// the berths that arrival steps start from.
	approach map[[2]string][]string
}

// NewMap builds a lookup index.
func NewMap(berths []Berth) *Map {
	m := &Map{
		between:  map[[3]string][]Berth{},
		from:     map[[2]string][]Berth{},
		to:       map[[2]string][]Berth{},
		approach: map[[2]string][]string{},
	}
	for _, b := range berths {
		switch b.StepType {
		case "F", "C":
			m.from[[2]string{b.TD, b.FromBerth}] = append(m.from[[2]string{b.TD, b.FromBerth}], b)
		case "T", "I":
			m.to[[2]string{b.TD, b.ToBerth}] = append(m.to[[2]string{b.TD, b.ToBerth}], b)
		default:
			k := [3]string{b.TD, b.FromBerth, b.ToBerth}
			m.between[k] = append(m.between[k], b)
		}
		if b.IsArrival() && (b.StepType == "B" || b.StepType == "F") && b.FromBerth != "" {
			k := [2]string{b.TD, b.FromBerth}
			if !contains(m.approach[k], b.STANOX) {
				m.approach[k] = append(m.approach[k], b.STANOX)
			}
		}
	}
	return m
}

// Step returns the SMART records triggered by a berth step (CA).
func (m *Map) Step(td, from, to string) []Berth {
	var out []Berth
	out = append(out, m.between[[3]string{td, from, to}]...)
	for _, b := range m.from[[2]string{td, from}] {
		if b.StepType == "F" {
			out = append(out, b)
		}
	}
	for _, b := range m.to[[2]string{td, to}] {
		if b.StepType == "T" {
			out = append(out, b)
		}
	}
	return out
}

// Approaching returns the STANOXes a train entering berth is approaching.
func (m *Map) Approaching(td, berth string) []string {
	return m.approach[[2]string{td, berth}]
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// Cancel returns records triggered by a berth cancel (CB).
func (m *Map) Cancel(td, from string) []Berth {
	var out []Berth
	for _, b := range m.from[[2]string{td, from}] {
		if b.StepType == "C" {
			out = append(out, b)
		}
	}
	return out
}

// Interpose returns records triggered by a berth interpose (CC).
func (m *Map) Interpose(td, to string) []Berth {
	var out []Berth
	for _, b := range m.to[[2]string{td, to}] {
		if b.StepType == "I" {
			out = append(out, b)
		}
	}
	return out
}

// LoadMap reads stored SMART data into a lookup index.
func LoadMap(ctx context.Context, pool *pgxpool.Pool) (*Map, int, error) {
	rows, err := pool.Query(ctx, `SELECT td, from_berth, to_berth, COALESCE(from_line, ''),
		COALESCE(to_line, ''), offset_secs, COALESCE(platform, ''), event, COALESCE(route, ''),
		stanox, step_type FROM smart_berths`)
	if err != nil {
		return nil, 0, err
	}
	berths, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Berth, error) {
		var b Berth
		err := r.Scan(&b.TD, &b.FromBerth, &b.ToBerth, &b.FromLine, &b.ToLine, &b.Offset,
			&b.Platform, &b.Event, &b.Route, &b.STANOX, &b.StepType)
		return b, err
	})
	if err != nil {
		return nil, 0, err
	}
	return NewMap(berths), len(berths), nil
}
