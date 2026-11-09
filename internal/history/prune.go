package history

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/carbonarok/trackside/internal/db"
)

// pruneBatch is how many services one delete removes. Each takes its TRUST
// events and Darwin forecasts with it (ON DELETE CASCADE), so batches keep
// every transaction, and the locks it holds, short.
const pruneBatch = 2000

// liveDays is the live window in whole days, never shorter than the days Run
// archives, so a date always has its chance to be archived on time.
func (a *Archiver) liveDays() int {
	return max(int(a.LiveKeep/(24*time.Hour)), archiveDays)
}

// pruneLive deletes live rows for run dates before the live window. A date
// is archived first if it never was, or if it was marked archived but
// history holds nothing for it while the live tables still do, so pruning
// never loses running that history hasn't got.
func (a *Archiver) pruneLive(ctx context.Context, today, now time.Time) error {
	cutoff := today.AddDate(0, 0, -a.liveDays())
	rows, err := a.Pool.Query(ctx,
		`SELECT DISTINCT run_date FROM services WHERE run_date < $1 ORDER BY run_date`, cutoff)
	if err != nil {
		return err
	}
	dates, err := pgx.CollectRows(rows, pgx.RowTo[time.Time])
	if err != nil {
		return err
	}
	for _, d := range dates {
		if err := a.archiveBeforePrune(ctx, d, today, now); err != nil {
			return fmt.Errorf("archive %s: %w", d.Format(time.DateOnly), err)
		}
		n, err := a.deleteServices(ctx, d)
		if err != nil {
			return fmt.Errorf("delete %s: %w", d.Format(time.DateOnly), err)
		}
		slog.Info("pruned live tables", "date", d.Format(time.DateOnly), "services", n)
	}

	// Darwin associations are keyed by RID, which belongs to one run date,
	// and are rewritten while the trains run; ones left alone this long are
	// for trains that have finished.
	if _, err := a.Pool.Exec(ctx, `DELETE FROM darwin_associations WHERE updated_at < $1`,
		now.AddDate(0, 0, -a.liveDays())); err != nil {
		return err
	}
	// VSTP schedules are not in the CIF extract, so a full timetable load
	// never replaces them; drop the ones that stopped running before the
	// window. CIF schedules are left to the timetable loads.
	_, err = a.Pool.Exec(ctx, `DELETE FROM schedules WHERE source = 'V' AND end_date < $1`, cutoff)
	return err
}

// archiveBeforePrune archives d unless it is already archived with history to
// show for it. Dates older than history's own Keep aren't worth archiving:
// that history would be deleted straight away.
func (a *Archiver) archiveBeforePrune(ctx context.Context, d, today, now time.Time) error {
	if a.Keep > 0 && d.Before(today.Add(-a.Keep)) {
		return nil
	}
	done, err := db.GetState(ctx, a.Pool, archivedKey(d))
	if err != nil {
		return err
	}
	if done != "" {
		var has bool
		if err := a.Pool.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM history_stops WHERE run_date = $1)`, d).Scan(&has); err != nil {
			return err
		}
		if has {
			return nil
		}
	}
	n, err := a.Archive(ctx, d)
	if err != nil {
		return err
	}
	slog.Info("archived history before pruning", "date", d.Format(time.DateOnly), "stops", n)
	return db.SetState(ctx, a.Pool, archivedKey(d), now.Format(time.RFC3339))
}

// deleteServices deletes every service on run date d, in batches.
func (a *Archiver) deleteServices(ctx context.Context, d time.Time) (int64, error) {
	var total int64
	for {
		tag, err := a.Pool.Exec(ctx, `DELETE FROM services WHERE id IN (
			SELECT id FROM services WHERE run_date = $1 LIMIT $2)`, d, pruneBatch)
		if err != nil {
			return total, err
		}
		total += tag.RowsAffected()
		if tag.RowsAffected() < pruneBatch {
			return total, nil
		}
	}
}
