// Package schedule parses Network Rail timetable data (the SCHEDULE feed's
// newline-delimited JSON and VSTP messages) and loads it into Postgres.
package schedule

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/carbonarok/trackside/internal/ukrail"
)

// Schedule is one timetable schedule for a train UID over a date range.
type Schedule struct {
	TrainUID           string
	StartDate          time.Time
	EndDate            time.Time
	DaysRuns           string
	STP                string // C, N, O or P
	Source             string // C = CIF, V = VSTP
	BankHolidayRunning string
	TrainStatus        string
	SignallingID       string
	Category           string
	PowerType          string
	TrainClass         string
	Speed              *int
	ATOCCode           string
	ServiceCode        string
	Locations          []Location
}

// Location is one calling or passing point. Times are seconds after midnight
// of the run date; a later location may exceed 86400 after midnight.
type Location struct {
	Seq      int
	TIPLOC   string
	Type     string // LO, LI or LT
	WTTArr   *int
	WTTDep   *int
	WTTPass  *int
	GBTTArr  *int
	GBTTDep  *int
	Platform string
	Line     string
	Path     string
}

// TIPLOC is a location record carried in the SCHEDULE feed.
type TIPLOC struct {
	Code        string
	STANOX      string
	CRS         string
	NLC         string
	Description string
}

// Record is one decoded line of the SCHEDULE feed. Exactly one field is set.
type Record struct {
	Header   *Header
	TIPLOC   *TIPLOC
	Schedule *Schedule
	// Delete identifies a schedule removed by an update file.
	Delete *Key
}

// Header is the JsonTimetableV1 record at the top of every file.
type Header struct {
	Type     string // "full" or "update"
	Sequence int
}

// Key is the natural key of a schedule.
type Key struct {
	TrainUID  string
	StartDate time.Time
	STP       string
	Source    string // C = CIF, V = VSTP
}

// Reader decodes a SCHEDULE feed file line by line, so the full multi-GB
// extract never has to sit in memory.
type Reader struct {
	sc   *bufio.Scanner
	line int
}

// NewReader returns a Reader over newline-delimited SCHEDULE JSON.
func NewReader(r io.Reader) *Reader {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	return &Reader{sc: sc}
}

// Next returns the next useful record, or io.EOF at the end of the file.
// Associations and other record types are skipped.
func (r *Reader) Next() (*Record, error) {
	for r.sc.Scan() {
		r.line++
		line := r.sc.Bytes()
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		rec, err := decodeLine(line)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", r.line, err)
		}
		if rec != nil {
			return rec, nil
		}
	}
	if err := r.sc.Err(); err != nil {
		return nil, err
	}
	return nil, io.EOF
}

type rawLine struct {
	Header   *rawHeader   `json:"JsonTimetableV1"`
	TIPLOC   *rawTIPLOC   `json:"TiplocV1"`
	Schedule *rawSchedule `json:"JsonScheduleV1"`
}

type rawHeader struct {
	Metadata struct {
		Type     string `json:"type"`
		Sequence int    `json:"sequence"`
	} `json:"Metadata"`
}

type rawTIPLOC struct {
	TransactionType string `json:"transaction_type"`
	TIPLOC          string `json:"tiploc_code"`
	NALCO           string `json:"nalco"`
	STANOX          string `json:"stanox"`
	CRS             string `json:"crs_code"`
	Description     string `json:"description"`
	TPSDescription  string `json:"tps_description"`
}

type rawSchedule struct {
	BankHoliday     *string `json:"CIF_bank_holiday_running"`
	STP             string  `json:"CIF_stp_indicator"`
	TrainUID        string  `json:"CIF_train_uid"`
	ATOCCode        *string `json:"atoc_code"`
	DaysRuns        string  `json:"schedule_days_runs"`
	EndDate         string  `json:"schedule_end_date"`
	StartDate       string  `json:"schedule_start_date"`
	TrainStatus     string  `json:"train_status"`
	TransactionType string  `json:"transaction_type"`
	Segment         struct {
		SignallingID string           `json:"signalling_id"`
		Category     *string          `json:"CIF_train_category"`
		ServiceCode  *string          `json:"CIF_train_service_code"`
		PowerType    *string          `json:"CIF_power_type"`
		Speed        *string          `json:"CIF_speed"`
		TrainClass   *string          `json:"CIF_train_class"`
		Locations    []rawScheduleLoc `json:"schedule_location"`
	} `json:"schedule_segment"`
}

type rawScheduleLoc struct {
	LocationType    string  `json:"location_type"`
	TIPLOC          string  `json:"tiploc_code"`
	Arrival         *string `json:"arrival"`
	Departure       *string `json:"departure"`
	Pass            *string `json:"pass"`
	PublicArrival   *string `json:"public_arrival"`
	PublicDeparture *string `json:"public_departure"`
	Platform        *string `json:"platform"`
	Line            *string `json:"line"`
	Path            *string `json:"path"`
}

func decodeLine(line []byte) (*Record, error) {
	var raw rawLine
	if err := json.Unmarshal(line, &raw); err != nil {
		return nil, err
	}
	switch {
	case raw.Header != nil:
		return &Record{Header: &Header{
			Type:     raw.Header.Metadata.Type,
			Sequence: raw.Header.Metadata.Sequence,
		}}, nil
	case raw.TIPLOC != nil:
		t := raw.TIPLOC
		if t.TransactionType == "Delete" {
			return nil, nil
		}
		desc := t.Description
		if desc == "" {
			desc = t.TPSDescription
		}
		return &Record{TIPLOC: &TIPLOC{
			Code:        strings.TrimSpace(t.TIPLOC),
			STANOX:      strings.TrimSpace(t.STANOX),
			CRS:         strings.TrimSpace(t.CRS),
			NLC:         strings.TrimSpace(t.NALCO),
			Description: strings.TrimSpace(desc),
		}}, nil
	case raw.Schedule != nil:
		return convertSchedule(raw.Schedule)
	}
	return nil, nil
}

func convertSchedule(r *rawSchedule) (*Record, error) {
	start, err := time.Parse(time.DateOnly, r.StartDate)
	if err != nil {
		return nil, fmt.Errorf("schedule %s: start date: %w", r.TrainUID, err)
	}
	uid := strings.TrimSpace(r.TrainUID)
	if r.TransactionType == "Delete" {
		return &Record{Delete: &Key{TrainUID: uid, StartDate: start, STP: r.STP, Source: "C"}}, nil
	}
	end, err := time.Parse(time.DateOnly, r.EndDate)
	if err != nil {
		return nil, fmt.Errorf("schedule %s: end date: %w", uid, err)
	}
	s := &Schedule{
		TrainUID:           uid,
		StartDate:          start,
		EndDate:            end,
		DaysRuns:           r.DaysRuns,
		STP:                r.STP,
		Source:             "C",
		BankHolidayRunning: str(r.BankHoliday),
		TrainStatus:        strings.TrimSpace(r.TrainStatus),
		SignallingID:       strings.TrimSpace(r.Segment.SignallingID),
		Category:           str(r.Segment.Category),
		PowerType:          str(r.Segment.PowerType),
		TrainClass:         str(r.Segment.TrainClass),
		Speed:              atoiPtr(str(r.Segment.Speed)),
		ATOCCode:           str(r.ATOCCode),
		ServiceCode:        str(r.Segment.ServiceCode),
	}
	locs := make([]rawLoc, 0, len(r.Segment.Locations))
	for _, l := range r.Segment.Locations {
		locs = append(locs, rawLoc{
			Type:     l.LocationType,
			TIPLOC:   l.TIPLOC,
			Arr:      parseWTT(l.Arrival),
			Dep:      parseWTT(l.Departure),
			Pass:     parseWTT(l.Pass),
			PubArr:   parsePublic(l.PublicArrival),
			PubDep:   parsePublic(l.PublicDeparture),
			Platform: str(l.Platform),
			Line:     str(l.Line),
			Path:     str(l.Path),
		})
	}
	s.Locations = buildLocations(locs)
	return &Record{Schedule: s}, nil
}

// rawLoc is a location with times still relative to their own day.
type rawLoc struct {
	Type, TIPLOC         string
	Arr, Dep, Pass       *int
	PubArr, PubDep       *int
	Platform, Line, Path string
}

// buildLocations sequences locations and rolls times past midnight: whenever
// a time is earlier than the previous one, a day has elapsed.
func buildLocations(raw []rawLoc) []Location {
	out := make([]Location, 0, len(raw))
	dayOffset, last := 0, -1
	roll := func(t *int) *int {
		if t == nil {
			return nil
		}
		v := *t + dayOffset
		if last >= 0 && v < last-6*3600 {
			dayOffset += 86400
			v += 86400
		}
		last = v
		return &v
	}
	// Public times are rolled independently of working times but use the
	// working-time day offset of the same location, since they differ by at
	// most a few minutes.
	public := func(pub *int, wtt *int) *int {
		if pub == nil {
			return nil
		}
		v := *pub
		if wtt != nil {
			base := *wtt - *wtt%86400
			v += base
			if v-*wtt > 12*3600 {
				v -= 86400
			} else if *wtt-v > 12*3600 {
				v += 86400
			}
		}
		return &v
	}
	for i, l := range raw {
		loc := Location{
			Seq:      i,
			TIPLOC:   strings.TrimSpace(l.TIPLOC),
			Type:     l.Type,
			Platform: l.Platform,
			Line:     l.Line,
			Path:     l.Path,
		}
		loc.WTTArr = roll(l.Arr)
		loc.WTTPass = roll(l.Pass)
		loc.WTTDep = roll(l.Dep)
		loc.GBTTArr = public(l.PubArr, loc.WTTArr)
		loc.GBTTDep = public(l.PubDep, loc.WTTDep)
		out = append(out, loc)
	}
	return out
}

func parseWTT(s *string) *int {
	if s == nil {
		return nil
	}
	if v, ok := ukrail.ParseWTT(*s); ok {
		return &v
	}
	return nil
}

// parsePublic parses a public (GBTT) time. CIF uses "0000" to mean "no public
// time"; a genuine midnight call is indistinguishable and is dropped, which
// matches how other consumers of the feed treat it.
func parsePublic(s *string) *int {
	if s == nil || strings.TrimSpace(*s) == "0000" {
		return nil
	}
	return parseWTT(s)
}

func str(s *string) string {
	if s == nil {
		return ""
	}
	return strings.TrimSpace(*s)
}

func atoiPtr(s string) *int {
	if s == "" {
		return nil
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return nil
	}
	return &v
}
