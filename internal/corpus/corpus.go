// Package corpus loads Network Rail's CORPUS reference data, which links
// TIPLOC, STANOX, CRS and NLC codes and gives each location a name.
package corpus

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/carbonarok/trackside/internal/ukrail"
)

// Entry is one CORPUS record. Blank fields are a single space in the source.
type Entry struct {
	TIPLOC    string          `json:"TIPLOC"`
	STANOX    string          `json:"STANOX"`
	CRS       string          `json:"3ALPHA"`
	NLC       json.RawMessage `json:"NLC"` // a number or a string
	NLCDesc   string          `json:"NLCDESC"`
	NLCDesc16 string          `json:"NLCDESC16"`
}

// Parse decodes a CORPUS JSON document.
func Parse(r io.Reader) ([]Entry, error) {
	var doc struct {
		Data []Entry `json:"TIPLOCDATA"`
	}
	dec := json.NewDecoder(r)
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("decode corpus: %w", err)
	}
	return doc.Data, nil
}

// Load upserts CORPUS entries into locations and returns how many were
// stored. Entries without a TIPLOC (pure NLC records) are skipped.
func Load(ctx context.Context, pool *pgxpool.Pool, entries []Entry) (int, error) {
	batch := &pgx.Batch{}
	n := 0
	for _, e := range entries {
		tiploc := strings.TrimSpace(e.TIPLOC)
		if tiploc == "" {
			continue
		}
		name := ukrail.DisplayName(e.NLCDesc)
		batch.Queue(`INSERT INTO locations (tiploc, stanox, crs, nlc, name)
			VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (tiploc) DO UPDATE SET
				stanox = COALESCE(EXCLUDED.stanox, locations.stanox),
				crs = COALESCE(EXCLUDED.crs, locations.crs),
				nlc = COALESCE(EXCLUDED.nlc, locations.nlc),
				name = CASE WHEN locations.name_source = 'darwin' THEN locations.name
					ELSE COALESCE(EXCLUDED.name, locations.name) END`,
			tiploc, blank(e.STANOX), blank(e.CRS), blank(strings.Trim(string(e.NLC), `"`)), blank(name))
		n++
	}
	if err := pool.SendBatch(ctx, batch).Close(); err != nil {
		return 0, err
	}
	return n, nil
}

func blank(s string) any {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	return s
}
