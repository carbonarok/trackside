package darwin

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/carbonarok/trackside/internal/ukrail"
)

// Applier writes Darwin messages to the database.
type Applier struct {
	Pool *pgxpool.Pool
}

// ApplyMessage decodes and applies one Push Port message: a Kafka record
// value from the Rail Data Marketplace, or bare XML. Problems with an
// individual train are logged and skipped.
func (a *Applier) ApplyMessage(ctx context.Context, body []byte) error {
	p, err := DecodeRecord(body)
	if err != nil {
		return fmt.Errorf("parse push port: %w", err)
	}
	for _, u := range []*Update{p.UR, p.SR} {
		if u == nil {
			continue
		}
		for i := range u.TrainStatus {
			if err := a.applyTS(ctx, &u.TrainStatus[i]); err != nil && !errors.Is(err, errNoService) {
				slog.Warn("darwin TS failed", "rid", u.TrainStatus[i].RID, "err", err)
			}
		}
		for i := range u.Schedules {
			if err := a.applySchedule(ctx, &u.Schedules[i]); err != nil && !errors.Is(err, errNoService) {
				slog.Warn("darwin schedule failed", "rid", u.Schedules[i].RID, "err", err)
			}
		}
	}
	return nil
}

var errNoService = errors.New("no matching service")

// stop is the part of a schedule location needed to match Darwin locations.
type stop struct {
	seq            int
	tiploc         string
	arr, dep, pass *int
}

type service struct {
	id      int64
	runDate time.Time
	stops   []stop
}

func (a *Applier) service(ctx context.Context, uid, ssd string) (*service, error) {
	runDate, err := time.Parse(time.DateOnly, ssd)
	if err != nil {
		return nil, fmt.Errorf("ssd %q: %w", ssd, err)
	}
	svc := &service{runDate: runDate}
	err = a.Pool.QueryRow(ctx, `SELECT id FROM services WHERE train_uid = $1 AND run_date = $2`,
		strings.TrimSpace(uid), runDate).Scan(&svc.id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errNoService
	}
	if err != nil {
		return nil, err
	}
	rows, err := a.Pool.Query(ctx, `SELECT sl.seq, sl.tiploc, sl.wtt_arr, sl.wtt_dep, sl.wtt_pass
		FROM services sv JOIN schedule_locations sl ON sl.schedule_id = sv.schedule_id
		WHERE sv.id = $1 ORDER BY sl.seq`, svc.id)
	if err != nil {
		return nil, err
	}
	svc.stops, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (stop, error) {
		var s stop
		err := r.Scan(&s.seq, &s.tiploc, &s.arr, &s.dep, &s.pass)
		return s, err
	})
	return svc, err
}

// match finds the stop a Darwin location refers to: same TIPLOC and the same
// working time. Trains can visit a TIPLOC twice, so the time decides.
func (s *service) match(tpl, wta, wtd, wtp string) *stop {
	type key struct {
		darwin string
		ours   func(*stop) *int
	}
	keys := []key{
		{wtd, func(s *stop) *int { return s.dep }},
		{wta, func(s *stop) *int { return s.arr }},
		{wtp, func(s *stop) *int { return s.pass }},
	}
	var best *stop
	bestDiff := 5*60 + 1
	for i := range s.stops {
		st := &s.stops[i]
		if st.tiploc != tpl {
			continue
		}
		for _, k := range keys {
			tod, ok := parseTime(k.darwin)
			ours := k.ours(st)
			if !ok || ours == nil {
				continue
			}
			diff := resolve(tod, *ours) - *ours
			if diff < 0 {
				diff = -diff
			}
			if diff < bestDiff {
				best, bestDiff = st, diff
			}
		}
	}
	return best
}

// at turns a Darwin time-of-day into an absolute time near the reference.
func (s *service) at(tod string, ref *int) (*time.Time, bool) {
	secs, ok := parseTime(tod)
	if !ok || ref == nil {
		return nil, false
	}
	t := ukrail.AtRunDate(s.runDate, resolve(secs, *ref))
	return &t, true
}

func (a *Applier) applyTS(ctx context.Context, ts *TrainStatus) error {
	svc, err := a.service(ctx, ts.UID, ts.SSD)
	if err != nil {
		return err
	}
	batch := &pgx.Batch{}
	batch.Queue(`UPDATE services SET darwin_rid = $2,
		darwin_late_code = COALESCE($3, darwin_late_code) WHERE id = $1`,
		svc.id, ts.RID, nullZero(ts.LateReason.Int()))
	for i := range ts.Locations {
		loc := &ts.Locations[i]
		st := svc.match(loc.TPL, loc.WTA, loc.WTD, loc.WTP)
		if st == nil {
			continue
		}
		f := forecastRow{}
		f.arr = svc.event(loc.Arr, firstNonNil(st.arr, st.dep, st.pass))
		f.dep = svc.event(loc.Dep, firstNonNil(st.dep, st.arr, st.pass))
		f.pass = svc.event(loc.Pass, firstNonNil(st.pass, st.arr, st.dep))
		if loc.Plat != nil {
			f.platSet = true
			f.platform = strings.TrimSpace(loc.Plat.Number)
			f.platSuppressed = loc.Plat.Suppressed()
			f.platConfirmed = loc.Plat.Conf
		}
		f.queue(batch, svc.id, st.seq, st.tiploc)
	}
	return a.Pool.SendBatch(ctx, batch).Close()
}

// event is one forecast element converted to absolute times. set is false
// when the element was absent, in which case stored values are kept.
type event struct {
	set, delayed, clearActual bool
	et, at                    *time.Time
}

func (s *service) event(f *Forecast, ref *int) event {
	if f == nil {
		return event{}
	}
	e := event{set: true, delayed: f.Delayed || f.ETUnknown, clearActual: f.ATRemoved}
	e.et, _ = s.at(f.ET, ref)
	if e.et == nil {
		e.et, _ = s.at(f.WET, ref)
	}
	e.at, _ = s.at(f.AT, ref)
	return e
}

type forecastRow struct {
	arr, dep, pass                         event
	platSet, platSuppressed, platConfirmed bool
	platform                               string
}

// queue upserts a forecast row, overwriting only the fields present in this
// update. An actual time is kept unless Darwin explicitly removes it.
func (f forecastRow) queue(b *pgx.Batch, serviceID int64, seq int, tiploc string) {
	b.Queue(`INSERT INTO darwin_forecasts AS d (service_id, seq,
			arr_et, arr_at, arr_delayed, dep_et, dep_at, dep_delayed, pass_et, pass_at,
			platform, platform_suppressed, platform_confirmed, tiploc)
		VALUES ($1, $2, $4, $5, $6, $8, $9, $10, $12, $13, $15, $16, $17, $21)
		ON CONFLICT (service_id, seq) DO UPDATE SET
			tiploc      = EXCLUDED.tiploc,
			arr_et      = CASE WHEN $3  THEN EXCLUDED.arr_et ELSE d.arr_et END,
			arr_at      = CASE WHEN $3  THEN COALESCE(EXCLUDED.arr_at, CASE WHEN $18 THEN NULL ELSE d.arr_at END) ELSE d.arr_at END,
			arr_delayed = CASE WHEN $3  THEN EXCLUDED.arr_delayed ELSE d.arr_delayed END,
			dep_et      = CASE WHEN $7  THEN EXCLUDED.dep_et ELSE d.dep_et END,
			dep_at      = CASE WHEN $7  THEN COALESCE(EXCLUDED.dep_at, CASE WHEN $19 THEN NULL ELSE d.dep_at END) ELSE d.dep_at END,
			dep_delayed = CASE WHEN $7  THEN EXCLUDED.dep_delayed ELSE d.dep_delayed END,
			pass_et     = CASE WHEN $11 THEN EXCLUDED.pass_et ELSE d.pass_et END,
			pass_at     = CASE WHEN $11 THEN COALESCE(EXCLUDED.pass_at, CASE WHEN $20 THEN NULL ELSE d.pass_at END) ELSE d.pass_at END,
			platform            = CASE WHEN $14 THEN EXCLUDED.platform ELSE d.platform END,
			platform_suppressed = CASE WHEN $14 THEN EXCLUDED.platform_suppressed ELSE d.platform_suppressed END,
			platform_confirmed  = CASE WHEN $14 THEN EXCLUDED.platform_confirmed ELSE d.platform_confirmed END,
			updated_at = now()`,
		serviceID, seq,
		f.arr.set, f.arr.et, f.arr.at, f.arr.delayed,
		f.dep.set, f.dep.et, f.dep.at, f.dep.delayed,
		f.pass.set, f.pass.et, f.pass.at,
		f.platSet, nullEmpty(f.platform), f.platSuppressed, f.platConfirmed,
		f.arr.clearActual, f.dep.clearActual, f.pass.clearActual, tiploc)
}

// applySchedule takes per-location cancellations and the cancellation reason
// from Darwin's copy of the schedule.
func (a *Applier) applySchedule(ctx context.Context, s *Schedule) error {
	svc, err := a.service(ctx, s.UID, s.SSD)
	if err != nil {
		return err
	}
	batch := &pgx.Batch{}
	batch.Queue(`UPDATE services SET darwin_rid = $2, darwin_cancel_code = $3 WHERE id = $1`,
		svc.id, s.RID, nullZero(s.CancelReason.Int()))
	for _, loc := range s.Locations {
		switch loc.XMLName.Local {
		case "OR", "OPOR", "IP", "OPIP", "PP", "DT", "OPDT":
		default:
			continue
		}
		st := svc.match(loc.TPL, loc.WTA, loc.WTD, loc.WTP)
		if st == nil {
			continue
		}
		batch.Queue(`INSERT INTO darwin_forecasts (service_id, seq, tiploc, arr_cancelled, dep_cancelled)
			VALUES ($1, $2, $3, $4, $4)
			ON CONFLICT (service_id, seq) DO UPDATE SET
				tiploc = EXCLUDED.tiploc,
				arr_cancelled = EXCLUDED.arr_cancelled, dep_cancelled = EXCLUDED.dep_cancelled,
				updated_at = now()`, svc.id, st.seq, st.tiploc, loc.Can)
	}
	return a.Pool.SendBatch(ctx, batch).Close()
}

func firstNonNil(vs ...*int) *int {
	for _, v := range vs {
		if v != nil {
			return v
		}
	}
	return nil
}

func nullZero(n int) any {
	if n == 0 {
		return nil
	}
	return n
}

func nullEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
