package history

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/carbonarok/trackside/internal/ukrail"
)

// Punctuality summarises arrivals or departures.
type Punctuality struct {
	Booked    int // calls with a booked time
	Reported  int // calls with an actual time
	Cancelled int
	// Within each threshold of the booked time, among reported calls. Rail
	// industry "on time" is within 59 seconds.
	OnTime, Within5, Within15 int
	AverageDelay              float64 // minutes, reported calls, early counts as 0
}

// LateTrain is a train that was often late at a station.
type LateTrain struct {
	UID, Headcode, ATOC string
	BookedTime          string // HH:MM
	Runs                int
	AverageDelay        float64
}

// StationStats is a station's performance over a period.
type StationStats struct {
	From, To   time.Time
	Arrivals   Punctuality
	Departures Punctuality
	MostLate   []LateTrain
}

// Station reports punctuality at a station's TIPLOCs over the given days,
// from archived history.
func (q *Querier) Station(ctx context.Context, tiplocs []string, days int) (*StationStats, error) {
	today := ukrail.DateOf(q.now())
	from, to := today.AddDate(0, 0, -days), today.AddDate(0, 0, -1)
	out := &StationStats{From: from, To: to}
	for _, side := range []struct {
		booked, actual, cancelled string
		into                      *Punctuality
	}{
		{"gbtt_arr", "actual_arr", "arr_cancelled", &out.Arrivals},
		{"gbtt_dep", "actual_dep", "dep_cancelled", &out.Departures},
	} {
		delay := "extract(epoch FROM " + side.actual + " - (run_date + make_interval(secs => " + side.booked + "))::timestamp AT TIME ZONE 'Europe/London')"
		p := side.into
		err := q.Pool.QueryRow(ctx, `
			SELECT count(*),
			       count(`+side.actual+`),
			       count(*) FILTER (WHERE `+side.cancelled+`),
			       count(*) FILTER (WHERE `+delay+` < 60),
			       count(*) FILTER (WHERE `+delay+` < 360),
			       count(*) FILTER (WHERE `+delay+` < 960),
			       -- greatest() ignores NULLs, so filter to reported calls first
			       COALESCE(avg(greatest(`+delay+`, 0)) FILTER (WHERE `+side.actual+` IS NOT NULL) / 60, 0)
			FROM history_stops
			WHERE tiploc = ANY($1) AND run_date BETWEEN $2 AND $3 AND `+side.booked+` IS NOT NULL`,
			tiplocs, from, to).Scan(&p.Booked, &p.Reported, &p.Cancelled, &p.OnTime, &p.Within5, &p.Within15, &p.AverageDelay)
		if err != nil {
			return nil, err
		}
	}
	rows, err := q.Pool.Query(ctx, `
		SELECT train_uid, max(COALESCE(headcode, '')), max(COALESCE(atoc_code, '')),
		       to_char(make_interval(secs => mod(min(gbtt_arr), 86400)), 'HH24:MI'), count(*),
		       avg(greatest(extract(epoch FROM actual_arr - (run_date + make_interval(secs => gbtt_arr))::timestamp AT TIME ZONE 'Europe/London'), 0)) / 60 AS late
		FROM history_stops
		WHERE tiploc = ANY($1) AND run_date BETWEEN $2 AND $3 AND gbtt_arr IS NOT NULL AND actual_arr IS NOT NULL
		GROUP BY train_uid HAVING count(*) >= 2
		ORDER BY late DESC LIMIT 10`, tiplocs, from, to)
	if err != nil {
		return nil, err
	}
	out.MostLate, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (LateTrain, error) {
		var t LateTrain
		err := r.Scan(&t.UID, &t.Headcode, &t.ATOC, &t.BookedTime, &t.Runs, &t.AverageDelay)
		return t, err
	})
	return out, err
}

// Run is one day of a train's running.
type Run struct {
	RunDate     time.Time
	Origin      Call
	Destination Call
	Cancelled   bool
}

// Train returns a train's archived runs over the last few days, newest
// first.
func (q *Querier) Train(ctx context.Context, uid string, days int) ([]Run, error) {
	since := ukrail.DateOf(q.now()).AddDate(0, 0, -days)
	rows, err := q.Pool.Query(ctx, `
		WITH ends AS (
		    SELECT run_date, min(seq) AS first, max(seq) AS last,
		           bool_or(arr_cancelled OR dep_cancelled) AS cancelled
		    FROM history_stops WHERE train_uid = $1 AND run_date >= $2 GROUP BY run_date
		)
		SELECT `+callCols("o")+`, `+callCols("d")+`, e.cancelled
		FROM ends e
		JOIN history_stops o ON o.train_uid = $1 AND o.run_date = e.run_date AND o.seq = e.first
		JOIN history_stops d ON d.train_uid = $1 AND d.run_date = e.run_date AND d.seq = e.last
		ORDER BY e.run_date DESC`, uid, since)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Run, error) {
		var run Run
		err := r.Scan(append(append(scanCall(&run.Origin), scanCall(&run.Destination)...), &run.Cancelled)...)
		run.RunDate = run.Origin.RunDate
		return run, err
	})
}
