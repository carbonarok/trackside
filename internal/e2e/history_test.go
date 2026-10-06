package e2e

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/carbonarok/trackside/internal/api"
	"github.com/carbonarok/trackside/internal/history"
	"github.com/carbonarok/trackside/internal/timetable"
	"github.com/carbonarok/trackside/internal/trust"
	"github.com/carbonarok/trackside/internal/ukrail"
)

// TestHistory runs trains through TRUST, checks Delay Repay against the live
// tables, archives the day and checks the same answers come from history.
func TestHistory(t *testing.T) {
	nextMorning := func() time.Time { return time.Date(2026, 10, 7, 5, 0, 0, 0, ukrail.London) }
	var store *timetable.Store
	pool, srv := setup(t, func(st *timetable.Store) { store = st })
	ctx := context.Background()

	// Rewire the API with history enabled.
	q := &history.Querier{Pool: pool, Store: store, Now: nextMorning}
	mux := http.NewServeMux()
	(&api.Server{Store: store, History: q}).Register(mux)
	srv.Config.Handler = mux

	applier := &trust.Applier{Pool: pool, Now: func() time.Time { return time.Date(2026, 10, 6, 9, 30, 0, 0, ukrail.London) }}
	frame := trustFrame(t,
		// W10001 leaves Waterloo late and reaches Woking 17 minutes late.
		map[string]any{"msg_type": "0003", "train_id": "721A01MX06", "event_type": "DEPARTURE",
			"loc_stanox": "87701", "actual_timestamp": localMillis(8, 2), "planned_timestamp": localMillis(8, 0), "offroute_ind": "false"},
		map[string]any{"msg_type": "0003", "train_id": "721A01MX06", "event_type": "ARRIVAL",
			"loc_stanox": "87705", "actual_timestamp": localMillis(8, 52), "planned_timestamp": localMillis(8, 35), "offroute_ind": "false"},
		// W10004, the next train after the cancelled W10003, reaches Woking at 09:10.
		map[string]any{"msg_type": "0003", "train_id": "721A04MX06", "event_type": "DEPARTURE",
			"loc_stanox": "87701", "actual_timestamp": localMillis(8, 31), "planned_timestamp": localMillis(8, 30), "offroute_ind": "false"},
		map[string]any{"msg_type": "0003", "train_id": "721A04MX06", "event_type": "ARRIVAL",
			"loc_stanox": "87705", "actual_timestamp": localMillis(9, 10), "planned_timestamp": localMillis(9, 0), "offroute_ind": "false"},
	)
	if err := applier.ApplyFrame(ctx, frame); err != nil {
		t.Fatal(err)
	}

	check := func(label string) {
		var late api.DelayRepay
		get(t, srv, "/v1/delay-repay?from=WAT&to=WOK&date=2026-10-06&departure=08:00", &late)
		if late.Train.UID != "W10001" || late.DelayMinutes == nil || *late.DelayMinutes != 17 || late.Band != "15-29" ||
			!late.Eligible || late.Compensation == nil || late.Compensation.SinglePercent != 25 || late.ArrivalSource != "TRUST" {
			t.Errorf("%s: late train = %+v", label, late)
		}
		// W10003 (08:15, booked into Woking 08:50) was cancelled; W10004 got
		// there at 09:10, 20 minutes after the booked arrival.
		var cancelled api.DelayRepay
		get(t, srv, "/v1/delay-repay?from=WAT&to=WOK&date=2026-10-06&departure=08:15", &cancelled)
		if !cancelled.Cancelled || cancelled.UsedTrain == nil || cancelled.UsedTrain.UID != "W10004" ||
			cancelled.DelayMinutes == nil || *cancelled.DelayMinutes != 20 || cancelled.Band != "15-29" {
			t.Errorf("%s: cancelled train = %+v (used %+v)", label, cancelled, cancelled.UsedTrain)
		}
	}
	check("live")

	resp, err := http.Get(srv.URL + "/v1/delay-repay?from=WAT&to=WOK&date=2026-10-06&departure=07:55")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("no train at 07:55: status %d", resp.StatusCode)
	}

	arch := &history.Archiver{Pool: pool, Store: store, Now: nextMorning}
	if err := arch.Run(ctx); err != nil {
		t.Fatal(err)
	}
	var n int
	pool.QueryRow(ctx, `SELECT count(*) FROM history_stops WHERE run_date = '2026-10-06'`).Scan(&n)
	if n == 0 {
		t.Fatal("nothing archived for 6 October")
	}
	// Archiving again is a no-op; re-archiving a date replaces it.
	if err := arch.Run(ctx); err != nil {
		t.Fatal(err)
	}
	// Remove the live data so answers must come from history.
	if _, err := pool.Exec(ctx, `DELETE FROM services WHERE run_date = '2026-10-06'`); err != nil {
		t.Fatal(err)
	}
	check("history")

	var stats api.StationStats
	get(t, srv, "/v1/stats/locations/WOK?days=2", &stats)
	if stats.Arrivals.Reported != 2 || stats.Arrivals.OnTime != 0 || stats.Arrivals.Cancelled < 1 ||
		stats.Arrivals.AverageDelayMinutes < 13 || stats.Arrivals.AverageDelayMinutes > 14 {
		t.Errorf("WOK stats = %+v", stats.Arrivals)
	}

	var sh api.ServiceHistory
	get(t, srv, "/v1/history/services/W10001?days=5", &sh)
	if len(sh.Runs) == 0 || sh.Runs[0].Train.RunDate != "2026-10-06" || sh.Runs[0].Destination.DelayMinutes == nil ||
		*sh.Runs[0].Destination.DelayMinutes != 17 {
		t.Errorf("W10001 history = %+v", sh.Runs)
	}
}
