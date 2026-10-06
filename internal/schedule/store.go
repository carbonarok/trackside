package schedule

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/carbonarok/trackside/internal/db"
	"github.com/carbonarok/trackside/internal/ukrail"
)

const batchSize = 2000

// LoadResult summarises an import.
type LoadResult struct {
	Type      string
	Sequence  int
	Schedules int
	Deletes   int
	TIPLOCs   int
}

// Load imports a SCHEDULE feed file. A "full" file replaces every CIF
// schedule; an "update" file is applied on top of what is already loaded and
// is refused if its sequence number has already been applied.
func Load(ctx context.Context, pool *pgxpool.Pool, r io.Reader) (*LoadResult, error) {
	rd := NewReader(r)
	first, err := rd.Next()
	if err != nil {
		return nil, fmt.Errorf("read header: %w", err)
	}
	if first.Header == nil {
		return nil, errors.New("file does not start with a JsonTimetableV1 header")
	}
	res := &LoadResult{Type: first.Header.Type, Sequence: first.Header.Sequence}

	last, err := db.GetState(ctx, pool, "schedule_sequence")
	if err != nil {
		return nil, err
	}
	if res.Type == "update" {
		if last == "" {
			return nil, errors.New("no full timetable loaded yet; import a full file first")
		}
		prev, _ := strconv.Atoi(last)
		if res.Sequence <= prev {
			return nil, fmt.Errorf("update sequence %d already applied (last %d)", res.Sequence, prev)
		}
		if res.Sequence != prev+1 {
			slog.Warn("schedule update sequence gap; a full reload is recommended",
				"expected", prev+1, "got", res.Sequence)
		}
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if res.Type == "full" {
		// VSTP schedules are short-lived and not part of the extract, so keep them.
		if _, err := tx.Exec(ctx, `DELETE FROM schedules WHERE source = 'C'`); err != nil {
			return nil, err
		}
	}

	var batch []*Schedule
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		if err := insertSchedules(ctx, tx, batch, res.Type == "update"); err != nil {
			return err
		}
		res.Schedules += len(batch)
		batch = batch[:0]
		return nil
	}
	for {
		rec, err := rd.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		switch {
		case rec.TIPLOC != nil:
			if err := upsertTIPLOC(ctx, tx, rec.TIPLOC); err != nil {
				return nil, err
			}
			res.TIPLOCs++
		case rec.Schedule != nil:
			batch = append(batch, rec.Schedule)
			if len(batch) >= batchSize {
				if err := flush(); err != nil {
					return nil, err
				}
				if res.Schedules%100000 == 0 {
					slog.Info("loading schedules", "count", res.Schedules)
				}
			}
		case rec.Delete != nil:
			if err := flush(); err != nil {
				return nil, err
			}
			if err := deleteSchedule(ctx, tx, *rec.Delete); err != nil {
				return nil, err
			}
			res.Deletes++
		}
	}
	if err := flush(); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO feed_state (feed, value) VALUES ('schedule_sequence', $1)
		ON CONFLICT (feed) DO UPDATE SET value = EXCLUDED.value, updated_at = now()`,
		strconv.Itoa(res.Sequence)); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return res, nil
}

// insertSchedules writes a batch with COPY. When replace is set, any existing
// schedule with the same key is removed first (CIF update "Create" records
// may restate a schedule that already exists).
func insertSchedules(ctx context.Context, tx pgx.Tx, batch []*Schedule, replace bool) error {
	if replace {
		for _, s := range batch {
			if err := deleteSchedule(ctx, tx, Key{s.TrainUID, s.StartDate, s.STP, s.Source}); err != nil {
				return err
			}
		}
	}
	ids := make([]int64, 0, len(batch))
	rows, err := tx.Query(ctx, `SELECT nextval('schedules_id_seq') FROM generate_series(1, $1)`, len(batch))
	if err != nil {
		return err
	}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	schedRows := make([][]any, len(batch))
	var locRows [][]any
	for i, s := range batch {
		schedRows[i] = []any{ids[i], s.TrainUID, s.StartDate, s.EndDate, s.DaysRuns, s.STP, s.Source,
			nullStr(s.BankHolidayRunning), nullStr(s.TrainStatus), nullStr(s.SignallingID),
			nullStr(s.Category), nullStr(s.PowerType), nullStr(s.TrainClass), s.Speed,
			nullStr(s.ATOCCode), nullStr(s.ServiceCode)}
		for _, l := range s.Locations {
			locRows = append(locRows, []any{ids[i], l.Seq, l.TIPLOC, l.Type, l.WTTArr, l.WTTDep,
				l.WTTPass, l.GBTTArr, l.GBTTDep, nullStr(l.Platform), nullStr(l.Line), nullStr(l.Path)})
		}
	}
	if _, err := tx.CopyFrom(ctx, pgx.Identifier{"schedules"},
		[]string{"id", "train_uid", "start_date", "end_date", "days_runs", "stp", "source",
			"bank_holiday_running", "train_status", "signalling_id", "category", "power_type",
			"train_class", "speed", "atoc_code", "service_code"},
		pgx.CopyFromRows(schedRows)); err != nil {
		return fmt.Errorf("copy schedules: %w", err)
	}
	if _, err := tx.CopyFrom(ctx, pgx.Identifier{"schedule_locations"},
		[]string{"schedule_id", "seq", "tiploc", "loc_type", "wtt_arr", "wtt_dep", "wtt_pass",
			"gbtt_arr", "gbtt_dep", "platform", "line", "path"},
		pgx.CopyFromRows(locRows)); err != nil {
		return fmt.Errorf("copy schedule locations: %w", err)
	}
	return nil
}

func deleteSchedule(ctx context.Context, tx pgx.Tx, k Key) error {
	_, err := tx.Exec(ctx, `DELETE FROM schedules
		WHERE train_uid = $1 AND start_date = $2 AND stp = $3 AND source = $4`,
		k.TrainUID, k.StartDate, k.STP, k.Source)
	return err
}

func upsertTIPLOC(ctx context.Context, tx pgx.Tx, t *TIPLOC) error {
	_, err := tx.Exec(ctx, `INSERT INTO locations (tiploc, stanox, crs, nlc, tps_description)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (tiploc) DO UPDATE SET
			stanox = COALESCE(EXCLUDED.stanox, locations.stanox),
			crs = COALESCE(EXCLUDED.crs, locations.crs),
			nlc = COALESCE(EXCLUDED.nlc, locations.nlc),
			tps_description = COALESCE(EXCLUDED.tps_description, locations.tps_description)`,
		t.Code, nullStr(t.STANOX), nullStr(t.CRS), nullStr(t.NLC), nullStr(t.Description))
	return err
}

// ApplyVSTP stores or removes one VSTP schedule and re-resolves the train's
// services for the dates it covers within the service window around now.
func ApplyVSTP(ctx context.Context, pool *pgxpool.Pool, rec *Record, now time.Time) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var uid string
	var from, to time.Time
	switch {
	case rec.Schedule != nil:
		s := rec.Schedule
		if err := insertSchedules(ctx, tx, []*Schedule{s}, true); err != nil {
			return err
		}
		uid, from, to = s.TrainUID, s.StartDate, s.EndDate
	case rec.Delete != nil:
		if err := deleteSchedule(ctx, tx, *rec.Delete); err != nil {
			return err
		}
		uid, from, to = rec.Delete.TrainUID, rec.Delete.StartDate, rec.Delete.StartDate
	default:
		return nil
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	// Only dates inside the materialised window matter; RefreshServices
	// handles later dates when the window reaches them.
	today := ukrail.DateOf(now)
	if from.Before(today.AddDate(0, 0, -1)) {
		from = today.AddDate(0, 0, -1)
	}
	if limit := today.AddDate(0, 0, ServiceWindowDays); to.After(limit) {
		to = limit
	}
	if from.After(to) {
		return nil
	}
	return refresh(ctx, pool, from, to, &uid)
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}
