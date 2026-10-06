package history

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/carbonarok/trackside/internal/timetable"
	"github.com/carbonarok/trackside/internal/ukrail"
)

// Call is one public stop of one train, from history or the live tables.
type Call struct {
	RunDate                time.Time
	UID, Headcode, ATOC    string
	Seq                    int
	TIPLOC, CRS            string
	GBTTArr, GBTTDep       *int
	ActualArr, ActualDep   *time.Time
	ArrSource, DepSource   string
	ArrCancelled           bool
	DepCancelled           bool
	CancelCode, CancelText string
	LateText               string
}

// BookedArr returns the booked arrival as a time.
func (c *Call) BookedArr() *time.Time { return at(c.RunDate, c.GBTTArr) }

// BookedDep returns the booked departure as a time.
func (c *Call) BookedDep() *time.Time { return at(c.RunDate, c.GBTTDep) }

func at(runDate time.Time, secs *int) *time.Time {
	if secs == nil {
		return nil
	}
	t := ukrail.AtRunDate(runDate, *secs)
	return &t
}

// Journey is a train's calls at a pair of stations.
type Journey struct {
	From, To Call
}

// Cancelled reports whether the train failed to take a passenger from one
// to the other.
func (j *Journey) Cancelled() bool { return j.From.DepCancelled || j.To.ArrCancelled }

// Querier answers history questions.
type Querier struct {
	Pool  *pgxpool.Pool
	Store *timetable.Store
	// Now is the clock; tests override it.
	Now func() time.Time
}

func (q *Querier) now() time.Time {
	if q.Now != nil {
		return q.Now()
	}
	return time.Now()
}

// ErrNoTrain means no train matches the journey asked about.
var ErrNoTrain = errors.New("no train found")

// journeys finds trains booked to depart one of the "from" TIPLOCs between
// two times and later call publicly at one of the "to" TIPLOCs. Archived
// days come from history_stops; anything not archived yet from the live
// tables.
func (q *Querier) journeys(ctx context.Context, from, to []string, start, end time.Time) ([]Journey, error) {
	byKey := map[string]Journey{}
	hist, err := q.historyJourneys(ctx, from, to, start, end)
	if err != nil {
		return nil, err
	}
	for _, j := range hist {
		byKey[j.From.UID+j.From.RunDate.Format(time.DateOnly)] = j
	}
	live, err := q.liveJourneys(ctx, from, to, start, end)
	if err != nil {
		return nil, err
	}
	for _, j := range live {
		k := j.From.UID + j.From.RunDate.Format(time.DateOnly)
		if _, ok := byKey[k]; !ok {
			byKey[k] = j
		}
	}
	out := make([]Journey, 0, len(byKey))
	for _, j := range byKey {
		out = append(out, j)
	}
	sort.Slice(out, func(i, k int) bool { return out[i].From.BookedDep().Before(*out[k].From.BookedDep()) })
	return out, nil
}

const layout = "2006-01-02 15:04:05"

func (q *Querier) historyJourneys(ctx context.Context, from, to []string, start, end time.Time) ([]Journey, error) {
	s, e := start.In(ukrail.London), end.In(ukrail.London)
	rows, err := q.Pool.Query(ctx, `
		SELECT `+callCols("f")+`, `+callCols("t")+`
		FROM history_stops f
		JOIN history_stops t ON t.run_date = f.run_date AND t.train_uid = f.train_uid AND t.seq > f.seq
		WHERE f.tiploc = ANY($1) AND t.tiploc = ANY($2)
		  AND f.gbtt_dep IS NOT NULL AND t.gbtt_arr IS NOT NULL
		  AND f.run_date BETWEEN $3::date - 1 AND $4::date
		  AND f.run_date + make_interval(secs => f.gbtt_dep) >= $5::timestamp
		  AND f.run_date + make_interval(secs => f.gbtt_dep) <= $6::timestamp`,
		from, to, ukrail.DateOf(s), ukrail.DateOf(e), s.Format(layout), e.Format(layout))
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Journey, error) {
		var j Journey
		err := r.Scan(append(scanCall(&j.From), scanCall(&j.To)...)...)
		return j, err
	})
}

func callCols(t string) string {
	return t + ".run_date, " + t + ".train_uid, COALESCE(" + t + ".headcode, ''), COALESCE(" + t + ".atoc_code, ''), " +
		t + ".seq, " + t + ".tiploc, COALESCE(" + t + ".crs, ''), " + t + ".gbtt_arr, " + t + ".gbtt_dep, " +
		t + ".actual_arr, " + t + ".actual_dep, COALESCE(" + t + ".arr_source, ''), COALESCE(" + t + ".dep_source, ''), " +
		t + ".arr_cancelled, " + t + ".dep_cancelled, COALESCE(" + t + ".cancel_code, ''), COALESCE(" + t + ".cancel_reason, ''), " +
		"COALESCE(" + t + ".late_reason, '')"
}

func scanCall(c *Call) []any {
	return []any{&c.RunDate, &c.UID, &c.Headcode, &c.ATOC, &c.Seq, &c.TIPLOC, &c.CRS, &c.GBTTArr, &c.GBTTDep,
		&c.ActualArr, &c.ActualDep, &c.ArrSource, &c.DepSource, &c.ArrCancelled, &c.DepCancelled,
		&c.CancelCode, &c.CancelText, &c.LateText}
}

func (q *Querier) liveJourneys(ctx context.Context, from, to []string, start, end time.Time) ([]Journey, error) {
	s, e := start.In(ukrail.London), end.In(ukrail.London)
	rows, err := q.Pool.Query(ctx, `
		SELECT DISTINCT sv.id
		FROM services sv
		JOIN schedule_locations sl ON sl.schedule_id = sv.schedule_id
		WHERE sl.tiploc = ANY($1) AND sl.gbtt_dep IS NOT NULL
		  AND sv.run_date BETWEEN $2::date - 1 AND $3::date
		  AND sv.run_date + make_interval(secs => sl.gbtt_dep) >= $4::timestamp
		  AND sv.run_date + make_interval(secs => sl.gbtt_dep) <= $5::timestamp`,
		from, ukrail.DateOf(s), ukrail.DateOf(e), s.Format(layout), e.Format(layout))
	if err != nil {
		return nil, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil || len(ids) == 0 {
		return nil, err
	}
	services, err := q.Store.Load(ctx, ids)
	if err != nil {
		return nil, err
	}
	fromSet, toSet := set(from), set(to)
	var out []Journey
	for _, svc := range services {
		fi := -1
		for i := range svc.Stops {
			p := &svc.Stops[i]
			if fi < 0 && fromSet[p.Location.TIPLOC] && p.GBTTDep != nil {
				dep := ukrail.AtRunDate(svc.RunDate, *p.GBTTDep)
				if !dep.Before(s) && !dep.After(e) {
					fi = i
				}
				continue
			}
			if fi >= 0 && toSet[p.Location.TIPLOC] && p.GBTTArr != nil {
				out = append(out, Journey{From: liveCall(svc, fi), To: liveCall(svc, i)})
				break
			}
		}
	}
	return out, nil
}

func liveCall(s *timetable.Service, i int) Call {
	p := &s.Stops[i]
	c := Call{RunDate: s.RunDate, UID: s.UID, Headcode: s.Headcode, ATOC: s.ATOCCode, Seq: p.Seq,
		TIPLOC: p.Location.TIPLOC, CRS: p.Location.CRS, GBTTArr: p.GBTTArr, GBTTDep: p.GBTTDep,
		ActualArr: p.ActualArr, ActualDep: p.ActualDep, ArrSource: p.ActualArrSource, DepSource: p.ActualDepSource,
		ArrCancelled: p.ArrCancelled, DepCancelled: p.DepCancelled, LateText: s.LateReasonText}
	if p.ArrCancelled || p.DepCancelled {
		c.CancelCode, c.CancelText = s.CancelReason, s.CancelReasonText
	}
	return c
}

func set(xs []string) map[string]bool {
	m := make(map[string]bool, len(xs))
	for _, x := range xs {
		m[x] = true
	}
	return m
}
