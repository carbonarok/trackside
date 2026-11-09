package e2e

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/carbonarok/trackside/internal/db"
	"github.com/carbonarok/trackside/internal/history"
	"github.com/carbonarok/trackside/internal/timetable"
	"github.com/carbonarok/trackside/internal/trust"
	"github.com/carbonarok/trackside/internal/ukrail"
)

const day = 24 * time.Hour

func count(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestPruneLive checks that the live tables are pruned past LIVE_DAYS, that
// a date is archived before its live rows go, and that nothing in the window
// is touched.
func TestPruneLive(t *testing.T) {
	var store *timetable.Store
	pool, _ := setup(t, func(st *timetable.Store) { store = st })
	ctx := context.Background()

	// Live running for 6 October, so pruning has TRUST events to cascade to.
	applier := &trust.Applier{Pool: pool, Now: func() time.Time { return time.Date(2026, 10, 6, 9, 30, 0, 0, ukrail.London) }}
	if err := applier.ApplyFrame(ctx, trustFrame(t,
		map[string]any{"msg_type": "0003", "train_id": "721A01MX06", "event_type": "DEPARTURE",
			"loc_stanox": "87701", "actual_timestamp": localMillis(8, 2), "planned_timestamp": localMillis(8, 0), "offroute_ind": "false"},
	)); err != nil {
		t.Fatal(err)
	}
	if count(t, pool, `SELECT count(*) FROM service_events`) == 0 {
		t.Fatal("no TRUST events to prune")
	}
	if count(t, pool, `SELECT count(*) FROM services WHERE run_date = '2026-10-07'`) == 0 {
		t.Fatal("no services on 7 October to keep")
	}

	// 6 October is marked archived but history has nothing for it, as when an
	// archive ran before the day's data was there: it must be archived again
	// rather than lost. 5 October was never archived at all.
	if err := db.SetState(ctx, pool, "history_archived:2026-10-06", "2026-10-07T04:00:00+01:00"); err != nil {
		t.Fatal(err)
	}

	// VSTP schedules that finished before the window go; current ones and
	// CIF ones (which timetable loads replace) stay.
	if _, err := pool.Exec(ctx, `INSERT INTO schedules (train_uid, start_date, end_date, days_runs, stp, source) VALUES
		('V00001', '2026-09-01', '2026-09-30', '1111111', 'N', 'V'),
		('V00002', '2026-10-01', '2026-12-31', '1111111', 'N', 'V'),
		('C00001', '2026-09-01', '2026-09-30', '1111111', 'P', 'C')`); err != nil {
		t.Fatal(err)
	}
	// Darwin associations left alone for longer than the window go.
	if _, err := pool.Exec(ctx, `INSERT INTO darwin_associations (main_rid, assoc_rid, tiploc, category, updated_at) VALUES
		('202609017000001', '202609017000002', 'WATRLMN', 'NP', '2026-09-01T12:00:00Z'),
		('202610137000001', '202610137000002', 'WATRLMN', 'NP', '2026-10-13T12:00:00Z')`); err != nil {
		t.Fatal(err)
	}

	// A week on from 7 October: everything before the 7th is past the window.
	weekOn := func() time.Time { return time.Date(2026, 10, 14, 5, 0, 0, 0, ukrail.London) }
	arch := &history.Archiver{Pool: pool, Store: store, LiveKeep: 7 * day, Now: weekOn}
	for i := range 2 { // the second run finds nothing left to do
		if err := arch.Run(ctx); err != nil {
			t.Fatalf("run %d: %v", i+1, err)
		}
	}

	if n := count(t, pool, `SELECT count(*) FROM services WHERE run_date < '2026-10-07'`); n != 0 {
		t.Errorf("%d services before 7 October survived pruning", n)
	}
	if n := count(t, pool, `SELECT count(*) FROM services WHERE run_date = '2026-10-07'`); n == 0 {
		t.Error("services on 7 October, inside the window, were pruned")
	}
	if n := count(t, pool, `SELECT count(*) FROM service_events`); n != 0 {
		t.Errorf("%d TRUST events survived their services", n)
	}
	if n := count(t, pool, `SELECT count(*) FROM history_stops WHERE run_date = '2026-10-06'`); n == 0 {
		t.Error("6 October was pruned without being archived again")
	}
	if n := count(t, pool, `SELECT count(*) FROM history_stops
		WHERE run_date = '2026-10-06' AND train_uid = 'W10001' AND actual_dep IS NOT NULL`); n == 0 {
		t.Error("6 October's TRUST departure didn't reach history before pruning")
	}
	if v, _ := db.GetState(ctx, pool, "history_archived:2026-10-05"); v == "" {
		t.Error("5 October was pruned without being archived")
	}
	var uids []string
	rows, _ := pool.Query(ctx, `SELECT train_uid FROM schedules WHERE train_uid IN ('V00001', 'V00002', 'C00001') ORDER BY 1`)
	for rows.Next() {
		var u string
		rows.Scan(&u)
		uids = append(uids, u)
	}
	if len(uids) != 2 || uids[0] != "C00001" || uids[1] != "V00002" {
		t.Errorf("schedules left = %v, want [C00001 V00002]", uids)
	}
	if n := count(t, pool, `SELECT count(*) FROM darwin_associations`); n != 1 {
		t.Errorf("%d Darwin associations left, want 1", n)
	}
}

// TestPruneLiveWindow checks that LIVE_DAYS=0 keeps everything and that a
// window shorter than the archive's look-back is widened to it.
func TestPruneLiveWindow(t *testing.T) {
	var store *timetable.Store
	pool, _ := setup(t, func(st *timetable.Store) { store = st })
	ctx := context.Background()
	before := count(t, pool, `SELECT count(*) FROM services`)
	if count(t, pool, `SELECT count(DISTINCT run_date) FROM services`) != 3 {
		t.Fatal("expected services on 5, 6 and 7 October")
	}

	// 9 October: with LIVE_DAYS=0 nothing is pruned.
	nineth := func() time.Time { return time.Date(2026, 10, 9, 5, 0, 0, 0, ukrail.London) }
	if err := (&history.Archiver{Pool: pool, Store: store, Now: nineth}).Run(ctx); err != nil {
		t.Fatal(err)
	}
	if n := count(t, pool, `SELECT count(*) FROM services`); n != before {
		t.Fatalf("LIVE_DAYS=0 pruned: %d services, had %d", n, before)
	}

	// LIVE_DAYS=1 is widened to three days, so only the 5th goes.
	if err := (&history.Archiver{Pool: pool, Store: store, LiveKeep: day, Now: nineth}).Run(ctx); err != nil {
		t.Fatal(err)
	}
	if n := count(t, pool, `SELECT count(*) FROM services WHERE run_date < '2026-10-06'`); n != 0 {
		t.Errorf("%d services on 5 October survived", n)
	}
	if n := count(t, pool, `SELECT count(DISTINCT run_date) FROM services WHERE run_date >= '2026-10-06'`); n != 2 {
		t.Errorf("%d of 6 and 7 October left, want both", n)
	}
}
