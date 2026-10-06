// Package history archives how every train ran, and answers questions
// about the past from it: Delay Repay claims, a train's punctuality, and
// station statistics.
package history

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/carbonarok/trackside/internal/db"
	"github.com/carbonarok/trackside/internal/timetable"
	"github.com/carbonarok/trackside/internal/ukrail"
)

// Archiver copies finished days from the live tables into history_stops.
type Archiver struct {
	Pool  *pgxpool.Pool
	Store *timetable.Store
	// Keep is how long history is kept; 0 keeps it forever.
	Keep time.Duration
	// Now is the clock; tests override it.
	Now func() time.Time
}

func (a *Archiver) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

// archiveAfter is when a run date is treated as finished: trains starting
// late in the evening run into the small hours of the next day.
const archiveAfter = 4 * time.Hour

// Run archives any recent run dates that have finished and not been
// archived yet, then drops history older than Keep. It is safe to run
// repeatedly.
func (a *Archiver) Run(ctx context.Context) error {
	now := a.now().In(ukrail.London)
	today := ukrail.DateOf(now)
	for back := 3; back >= 1; back-- {
		d := today.AddDate(0, 0, -back)
		if back == 1 && now.Sub(ukrail.AtRunDate(today, 0)) < archiveAfter {
			continue // yesterday's late trains may still be running
		}
		key := "history_archived:" + d.Format(time.DateOnly)
		done, err := db.GetState(ctx, a.Pool, key)
		if err != nil {
			return err
		}
		if done != "" {
			continue
		}
		n, err := a.Archive(ctx, d)
		if err != nil {
			return fmt.Errorf("archive %s: %w", d.Format(time.DateOnly), err)
		}
		slog.Info("archived history", "date", d.Format(time.DateOnly), "stops", n)
		if err := db.SetState(ctx, a.Pool, key, now.Format(time.RFC3339)); err != nil {
			return err
		}
	}
	if a.Keep > 0 {
		cutoff := today.Add(-a.Keep)
		if _, err := a.Pool.Exec(ctx, `DELETE FROM history_stops WHERE run_date < $1`, cutoff); err != nil {
			return err
		}
	}
	return nil
}

// Archive stores every public call of every train on a run date, with live
// data from TRUST, Darwin and Train Describer merged in. Re-archiving a date
// replaces it.
func (a *Archiver) Archive(ctx context.Context, runDate time.Time) (int, error) {
	ids, err := a.Store.ServiceIDs(ctx, runDate)
	if err != nil {
		return 0, err
	}
	total := 0
	const chunk = 500
	for start := 0; start < len(ids); start += chunk {
		services, err := a.Store.Load(ctx, ids[start:min(start+chunk, len(ids))])
		if err != nil {
			return total, err
		}
		var rows [][]any
		for _, s := range services {
			rows = append(rows, stopRows(s)...)
		}
		if err := a.write(ctx, runDate, rows, start == 0); err != nil {
			return total, err
		}
		total += len(rows)
	}
	return total, nil
}

func stopRows(s *timetable.Service) [][]any {
	var out [][]any
	for i := range s.Stops {
		p := &s.Stops[i]
		if !p.IsPublicCall() {
			continue
		}
		var cancelCode any
		if s.CancelReason != "" && (p.ArrCancelled || p.DepCancelled) {
			cancelCode = s.CancelReason
		}
		out = append(out, []any{
			s.RunDate, s.UID, p.Seq, nullStr(s.Headcode), nullStr(s.ATOCCode),
			p.Location.TIPLOC, nullStr(p.Location.CRS), p.GBTTArr, p.GBTTDep,
			p.ActualArr, p.ActualDep, nullStr(p.ActualArrSource), nullStr(p.ActualDepSource),
			p.ArrCancelled, p.DepCancelled, cancelCode, nullStr(s.CancelReasonText), nullStr(s.LateReasonText),
		})
	}
	return out
}

var columns = []string{"run_date", "train_uid", "seq", "headcode", "atoc_code", "tiploc", "crs",
	"gbtt_arr", "gbtt_dep", "actual_arr", "actual_dep", "arr_source", "dep_source",
	"arr_cancelled", "dep_cancelled", "cancel_code", "cancel_reason", "late_reason"}

func (a *Archiver) write(ctx context.Context, runDate time.Time, rows [][]any, first bool) error {
	tx, err := a.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if first {
		if _, err := tx.Exec(ctx, `DELETE FROM history_stops WHERE run_date = $1`, runDate); err != nil {
			return err
		}
	}
	if len(rows) > 0 {
		if _, err := tx.CopyFrom(ctx, pgx.Identifier{"history_stops"}, columns, pgx.CopyFromRows(rows)); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}
