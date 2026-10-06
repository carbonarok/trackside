// Package e2e exercises the full pipeline against a real Postgres: load the
// fixture timetable, replay TRUST messages and query both HTTP APIs.
//
// It runs only when TEST_DATABASE_URL is set. The database is wiped.
package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/carbonarok/trackside/internal/api"
	"github.com/carbonarok/trackside/internal/compat"
	"github.com/carbonarok/trackside/internal/corpus"
	"github.com/carbonarok/trackside/internal/darwin"
	"github.com/carbonarok/trackside/internal/db"
	"github.com/carbonarok/trackside/internal/schedule"
	"github.com/carbonarok/trackside/internal/td"
	"github.com/carbonarok/trackside/internal/timetable"
	"github.com/carbonarok/trackside/internal/trust"
	"github.com/carbonarok/trackside/internal/ukrail"
)

var runDate = time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC) // a Tuesday

func setup(t *testing.T, opts ...func(*timetable.Store)) (*pgxpool.Pool, *httptest.Server) {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := db.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public`); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}

	f, err := os.Open("../../testdata/corpus.json")
	if err != nil {
		t.Fatal(err)
	}
	entries, err := corpus.Parse(f)
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := corpus.Load(ctx, pool, entries); err != nil {
		t.Fatal(err)
	}
	loadSchedule(t, pool, "../../testdata/schedule_full.json")

	now := func() time.Time { return time.Date(2026, 10, 6, 8, 0, 0, 0, ukrail.London) }
	store := &timetable.Store{Pool: pool, Now: now}
	for _, o := range opts {
		o(store)
	}
	mux := http.NewServeMux()
	(&api.Server{Store: store, Now: now}).Register(mux)
	(&compat.Server{Store: store, Now: now}).Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return pool, srv
}

func loadSchedule(t *testing.T, pool *pgxpool.Pool, path string) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := schedule.Load(context.Background(), pool, f); err != nil {
		t.Fatal(err)
	}
	if err := schedule.RefreshRange(context.Background(), pool, runDate.AddDate(0, 0, -1), runDate.AddDate(0, 0, 1)); err != nil {
		t.Fatal(err)
	}
}

func get(t *testing.T, srv *httptest.Server, path string, out any) {
	t.Helper()
	resp, err := http.Get(srv.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: %s", path, resp.Status)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		t.Fatal(err)
	}
}

// localMillis encodes a UK local time the way TRUST does: as if it were UTC.
func localMillis(h, m int) string {
	return fmt.Sprint(time.Date(2026, 10, 6, h, m, 0, 0, time.UTC).UnixMilli())
}

func trustFrame(t *testing.T, msgs ...map[string]any) []byte {
	t.Helper()
	var frame []map[string]any
	for _, m := range msgs {
		typ := m["msg_type"]
		delete(m, "msg_type")
		frame = append(frame, map[string]any{"header": map[string]any{"msg_type": typ}, "body": m})
	}
	b, err := json.Marshal(frame)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

type board struct {
	Location api.Location       `json:"location"`
	Services []api.BoardService `json:"services"`
}

func uids(b board) string {
	var out []string
	for _, s := range b.Services {
		out = append(out, s.UID)
	}
	return strings.Join(out, ",")
}

func TestPipeline(t *testing.T) {
	pool, srv := setup(t)
	ctx := context.Background()

	t.Run("departures resolve STP overlays and cancellations", func(t *testing.T) {
		var b board
		get(t, srv, "/v1/locations/CLJ/departures?at=2026-10-06T08:00&window=60", &b)
		if got := uids(b); got != "W10001,W10003,W10004" {
			t.Fatalf("services = %s", got)
		}
		if b.Location.Name != "Clapham Junction" {
			t.Errorf("location name = %q", b.Location.Name)
		}
		w3 := b.Services[1]
		if !w3.PlannedCancel || w3.Status != "cancelled" || !w3.Stop.Cancelled {
			t.Errorf("W10003 should be planned-cancelled: %+v", w3)
		}
		w4 := b.Services[2]
		if w4.Stop.Platform.Planned != "10" {
			t.Errorf("W10004 overlay platform = %+v", w4.Stop.Platform)
		}
		if w4.Destination[0].Time.Format("15:04") != "09:00" {
			t.Errorf("W10004 overlay destination time = %v", w4.Destination[0].Time)
		}
	})

	t.Run("destination filter", func(t *testing.T) {
		var b board
		get(t, srv, "/v1/locations/CLJ/departures?at=2026-10-06T08:00&window=60&to=WIM", &b)
		if got := uids(b); got != "W10001" {
			t.Fatalf("services to WIM = %s (the W10004 overlay skips Wimbledon)", got)
		}
	})

	t.Run("passes only when asked", func(t *testing.T) {
		var b board
		get(t, srv, "/v1/locations/VXH/departures?at=2026-10-06T08:00&window=30", &b)
		if len(b.Services) != 0 {
			t.Fatalf("passing trains shown without passes=true: %s", uids(b))
		}
		get(t, srv, "/v1/locations/VXH/departures?at=2026-10-06T08:00&window=30&passes=true", &b)
		if got := uids(b); got != "W10001" {
			t.Fatalf("passes = %s", got)
		}
	})

	t.Run("services after midnight belong to the previous run date", func(t *testing.T) {
		var b board
		get(t, srv, "/v1/locations/WIM/departures?at=2026-10-07T00:00&window=10", &b)
		if len(b.Services) != 1 || b.Services[0].UID != "W10002" || b.Services[0].RunDate != "2026-10-06" {
			t.Fatalf("got %+v", b.Services)
		}
		if got := b.Services[0].Stop.Departure.Public.Format(time.RFC3339); got != "2026-10-07T00:03:00+01:00" {
			t.Errorf("departure = %s", got)
		}
	})

	t.Run("TRUST movements produce actuals and estimates", func(t *testing.T) {
		applier := &trust.Applier{Pool: pool, Now: func() time.Time {
			return time.Date(2026, 10, 6, 8, 10, 0, 0, ukrail.London)
		}}
		frame := trustFrame(t,
			map[string]any{"msg_type": "0001", "train_id": "721A01MX06", "train_uid": "W10001",
				"tp_origin_timestamp":  "2026-10-06",
				"origin_dep_timestamp": fmt.Sprint(time.Date(2026, 10, 6, 7, 0, 0, 0, time.UTC).UnixMilli())},
			map[string]any{"msg_type": "0003", "train_id": "721A01MX06", "event_type": "DEPARTURE",
				"loc_stanox": "87701", "actual_timestamp": localMillis(8, 2), "planned_timestamp": localMillis(8, 0),
				"platform": " 5", "offroute_ind": "false"},
			map[string]any{"msg_type": "0003", "train_id": "721A01MX06", "event_type": "ARRIVAL",
				"loc_stanox": "87703", "actual_timestamp": localMillis(8, 9), "planned_timestamp": localMillis(8, 6),
				"platform": " 8", "offroute_ind": "false"},
			map[string]any{"msg_type": "0003", "train_id": "UNKNOWN123", "event_type": "ARRIVAL",
				"loc_stanox": "87703", "actual_timestamp": localMillis(8, 9), "planned_timestamp": localMillis(8, 6)},
		)
		if err := applier.ApplyFrame(ctx, frame); err != nil {
			t.Fatal(err)
		}

		var d api.ServiceDetail
		get(t, srv, "/v1/services/W10001/2026-10-06", &d)
		if d.Status != "running" {
			t.Errorf("status = %s", d.Status)
		}
		wat, clj, wim, wok := d.Stops[0], d.Stops[2], d.Stops[3], d.Stops[4]
		if wat.Departure.Actual == nil || wat.Departure.Actual.Format("15:04") != "08:02" || *wat.Departure.DelayMinutes != 2 {
			t.Errorf("WAT departure = %+v", wat.Departure)
		}
		if clj.Arrival.Actual.Format("15:04") != "08:09" || !clj.Platform.Changed || clj.Platform.Actual != "8" {
			t.Errorf("CLJ = %+v %+v", clj.Arrival, clj.Platform)
		}
		if clj.Departure.Estimated == nil || clj.Departure.Estimated.Format("15:04") != "08:10" {
			t.Errorf("CLJ estimated departure = %v", clj.Departure.Estimated)
		}
		if wim.Arrival.Estimated.Format("15:04") != "08:15" || wok.Arrival.Estimated.Format("15:04") != "08:38" {
			t.Errorf("estimates WIM=%v WOK=%v", wim.Arrival.Estimated, wok.Arrival.Estimated)
		}
	})

	t.Run("TRUST adopts trains activated before startup", func(t *testing.T) {
		applier := &trust.Applier{Pool: pool, Now: func() time.Time {
			return time.Date(2026, 10, 6, 8, 41, 0, 0, ukrail.London)
		}}
		// W10005 (1A05) was never activated, so its train ID is unknown.
		frame := trustFrame(t,
			map[string]any{"msg_type": "0003", "train_id": "721A05MX06", "event_type": "DEPARTURE",
				"loc_stanox": "87704", "actual_timestamp": localMillis(8, 15), "planned_timestamp": localMillis(8, 14),
				"offroute_ind": "false"},
			// A headcode nothing runs under is ignored.
			map[string]any{"msg_type": "0003", "train_id": "729Z99MX06", "event_type": "DEPARTURE",
				"loc_stanox": "87703", "actual_timestamp": localMillis(8, 40), "planned_timestamp": localMillis(8, 37),
				"offroute_ind": "false"},
		)
		if err := applier.ApplyFrame(ctx, frame); err != nil {
			t.Fatal(err)
		}
		var d api.ServiceDetail
		get(t, srv, "/v1/services/W10005/2026-10-06", &d)
		if got := d.Stops[0].Departure.Actual; got == nil || got.Format("15:04") != "08:15" {
			t.Errorf("adopted movement not recorded: %+v", d.Stops[0].Departure)
		}
		var trustID string
		if err := pool.QueryRow(ctx, `SELECT trust_id FROM services WHERE train_uid = 'W10005' AND run_date = '2026-10-06'`).Scan(&trustID); err != nil || trustID != "721A05MX06" {
			t.Errorf("trust_id = %q, %v", trustID, err)
		}
	})

	t.Run("compat search", func(t *testing.T) {
		var res struct {
			Location map[string]any `json:"location"`
			Services []struct {
				ServiceUID     string                `json:"serviceUid"`
				RunDate        string                `json:"runDate"`
				ATOCName       string                `json:"atocName"`
				LocationDetail compat.LocationDetail `json:"locationDetail"`
			} `json:"services"`
		}
		get(t, srv, "/api/v1/json/search/CLJ/2026/10/06/0800", &res)
		if len(res.Services) != 3 {
			t.Fatalf("services = %d", len(res.Services))
		}
		w1 := res.Services[0]
		if w1.ServiceUID != "W10001" || w1.ATOCName != "South Western Railway" {
			t.Errorf("W10001 header = %+v", w1)
		}
		ld := w1.LocationDetail
		if ld.GBTTBookedDeparture != "0807" || ld.RealtimeArrival != "0809" || !ld.RealtimeArrivalActual ||
			ld.RealtimeDeparture != "0810" || ld.RealtimeDepartureActual || ld.Platform != "8" || !ld.PlatformChanged {
			t.Errorf("W10001 locationDetail = %+v", ld)
		}
		if ld.Origin[0].Description != "London Waterloo" || ld.Destination[0].PublicTime != "0835" {
			t.Errorf("origin/destination = %+v %+v", ld.Origin, ld.Destination)
		}
		if got := res.Services[1].LocationDetail.DisplayAs; got != "CANCELLED_CALL" {
			t.Errorf("W10003 displayAs = %s", got)
		}
	})

	t.Run("compat live search and service", func(t *testing.T) {
		var res struct {
			Services []struct {
				ServiceUID string `json:"serviceUid"`
			} `json:"services"`
		}
		get(t, srv, "/api/v1/json/search/WAT/to/WOK", &res)
		if len(res.Services) != 3 {
			t.Errorf("live WAT to WOK = %+v", res.Services)
		}
		var svc struct {
			ServiceUID        string                  `json:"serviceUid"`
			RealtimeActivated bool                    `json:"realtimeActivated"`
			Locations         []compat.LocationDetail `json:"locations"`
		}
		get(t, srv, "/api/v1/json/service/W10001/2026/10/06", &svc)
		if !svc.RealtimeActivated || len(svc.Locations) != 5 {
			t.Fatalf("service = %+v", svc)
		}
		if svc.Locations[0].DisplayAs != "ORIGIN" || svc.Locations[1].DisplayAs != "PASS" ||
			svc.Locations[4].DisplayAs != "DESTINATION" {
			t.Errorf("displayAs = %s %s %s", svc.Locations[0].DisplayAs, svc.Locations[1].DisplayAs, svc.Locations[4].DisplayAs)
		}
		if svc.Locations[3].RealtimeArrival != "0815" || svc.Locations[3].RealtimeArrivalActual {
			t.Errorf("WIM estimate = %+v", svc.Locations[3])
		}
	})

	t.Run("en-route cancellation", func(t *testing.T) {
		applier := &trust.Applier{Pool: pool, Now: func() time.Time {
			return time.Date(2026, 10, 6, 8, 11, 0, 0, ukrail.London)
		}}
		frame := trustFrame(t, map[string]any{"msg_type": "0002", "train_id": "721A01MX06",
			"loc_stanox": "87704", "canx_type": "EN ROUTE", "canx_reason_code": "YI"})
		if err := applier.ApplyFrame(ctx, frame); err != nil {
			t.Fatal(err)
		}
		var d api.ServiceDetail
		get(t, srv, "/v1/services/W10001/2026-10-06", &d)
		wim, wok := d.Stops[3], d.Stops[4]
		if d.Status != "partially_cancelled" || !wim.TerminatesHere || wim.Cancelled || !wok.Cancelled {
			t.Errorf("status=%s WIM=%+v WOK=%+v", d.Status, wim, wok)
		}
		if d.CancelReason != "YI" || !strings.HasPrefix(d.CancelReasonCodeDescription, "Late arrival of booked inward stock") {
			t.Errorf("reason = %q (%q)", d.CancelReason, d.CancelReasonCodeDescription)
		}
		var svc struct {
			Locations []compat.LocationDetail `json:"locations"`
		}
		get(t, srv, "/api/v1/json/service/W10001/2026/10/06", &svc)
		if got := svc.Locations[4].CancelReasonLongText; !strings.HasPrefix(got, "Late arrival of booked inward stock") {
			t.Errorf("compat fallback reason = %q", got)
		}
	})

	t.Run("Darwin reference data and forecasts", func(t *testing.T) {
		f, err := os.Open("../../testdata/darwin_ref.xml")
		if err != nil {
			t.Fatal(err)
		}
		ref, err := darwin.ParseReference(f)
		f.Close()
		if err != nil {
			t.Fatal(err)
		}
		if err := darwin.LoadReference(ctx, pool, ref); err != nil {
			t.Fatal(err)
		}
		applier := &darwin.Applier{Pool: pool}
		for _, name := range []string{"darwin_ts.xml", "darwin_schedule.xml"} {
			body, err := os.ReadFile("../../testdata/" + name)
			if err != nil {
				t.Fatal(err)
			}
			if err := applier.ApplyMessage(ctx, body); err != nil {
				t.Fatal(err)
			}
		}

		var d api.ServiceDetail
		get(t, srv, "/v1/services/W10004/2026-10-06", &d)
		wat, clj, wok := d.Stops[0], d.Stops[1], d.Stops[2]
		if wat.Platform != nil {
			t.Errorf("suppressed platform shown at WAT: %+v", wat.Platform)
		}
		if wat.Departure.Actual == nil || wat.Departure.Actual.Format("15:04") != "08:33" {
			t.Errorf("Darwin actual not used at WAT: %+v", wat.Departure)
		}
		if clj.Departure.Estimated.Format("15:04") != "08:41" || clj.Platform.Actual != "11" || !clj.Platform.Confirmed {
			t.Errorf("CLJ = %+v %+v", clj.Departure, clj.Platform)
		}
		if !wok.Arrival.Delayed {
			t.Errorf("WOK should be delayed: %+v", wok.Arrival)
		}
		if d.LateReasonText != "This train has been delayed by a fault with the signalling system" {
			t.Errorf("late reason = %q", d.LateReasonText)
		}
		if clj.Name != "Clapham Junction" || d.Operator.Name != "South Western Railway" {
			t.Errorf("names = %q %q", clj.Name, d.Operator.Name)
		}

		get(t, srv, "/v1/services/W10002/2026-10-06", &d)
		if d.Status != "cancelled" || d.CancelReasonText != "This train has been cancelled because of a broken down train" {
			t.Errorf("W10002 = %s %q", d.Status, d.CancelReasonText)
		}
		var svc struct {
			Locations []compat.LocationDetail `json:"locations"`
		}
		get(t, srv, "/api/v1/json/service/W10002/2026/10/06", &svc)
		if got := svc.Locations[1].CancelReasonShortText; got != "a broken down train" {
			t.Errorf("compat short reason = %q", got)
		}
	})

	t.Run("associations from the timetable and Darwin", func(t *testing.T) {
		var d api.ServiceDetail
		get(t, srv, "/v1/services/W10001/2026-10-06", &d)
		if len(d.Associations) != 1 {
			t.Fatalf("W10001 associations = %+v", d.Associations)
		}
		a := d.Associations[0]
		if a.Type != "divides" || a.Location.Name != "Wimbledon" || a.Service.UID != "W10005" ||
			a.Service.Destination[0].Name != "Woking" {
			t.Errorf("divide = %+v", a)
		}
		get(t, srv, "/v1/services/W10005/2026-10-06", &d)
		if len(d.Associations) != 1 || d.Associations[0].Type != "divided_from" || d.Associations[0].Service.UID != "W10001" {
			t.Errorf("W10005 associations = %+v", d.Associations)
		}
		var monday api.ServiceDetail
		get(t, srv, "/v1/services/W10001/2026-10-05", &monday)
		if len(monday.Associations) != 0 {
			t.Errorf("STP-cancelled divide still shown on 5 Oct: %+v", monday.Associations)
		}

		// Next working across midnight: W10002 (6 Oct) forms W10006 (7 Oct).
		get(t, srv, "/v1/services/W10006/2026-10-07", &d)
		if len(d.Associations) != 1 || d.Associations[0].Type != "formed_from" ||
			d.Associations[0].Service.RunDate != "2026-10-06" {
			t.Errorf("W10006 associations = %+v", d.Associations)
		}

		var svc struct {
			Locations []compat.LocationDetail `json:"locations"`
		}
		get(t, srv, "/api/v1/json/service/W10001/2026/10/06", &svc)
		if as := svc.Locations[3].Associations; len(as) != 1 || as[0].Type != "divide" || as[0].AssociatedUID != "W10005" {
			t.Errorf("compat associations at WIM = %+v", as)
		}

		// Darwin cancels the timetabled next working and adds a link.
		body, err := os.ReadFile("../../testdata/darwin_association.xml")
		if err != nil {
			t.Fatal(err)
		}
		if err := (&darwin.Applier{Pool: pool}).ApplyMessage(ctx, body); err != nil {
			t.Fatal(err)
		}
		get(t, srv, "/v1/services/W10004/2026-10-06", &d)
		got := map[string]api.Association{}
		for _, a := range d.Associations {
			got[a.Type] = a
		}
		if f, ok := got["forms"]; !ok || !f.Cancelled || f.Service.UID != "W10002" {
			t.Errorf("Darwin cancellation of next working = %+v", d.Associations)
		}
		if l, ok := got["linked"]; !ok || l.Location.TIPLOC != "CLPHMJN" {
			t.Errorf("Darwin-only association = %+v", d.Associations)
		}
	})

	t.Run("station messages", func(t *testing.T) {
		applier := &darwin.Applier{Pool: pool}
		body, err := os.ReadFile("../../testdata/darwin_messages.xml")
		if err != nil {
			t.Fatal(err)
		}
		if err := applier.ApplyMessage(ctx, body); err != nil {
			t.Fatal(err)
		}
		var b api.Board
		get(t, srv, "/v1/locations/CLJ/departures?at=2026-10-06T08:00&window=5", &b)
		if len(b.Messages) != 2 || b.Messages[0].Severity != 3 || b.Messages[1].Text != "Lifts at platforms 3 & 4 are out of order. More details" {
			t.Fatalf("CLJ messages (suppressed one must be hidden) = %+v", b.Messages)
		}
		var m struct {
			Messages []api.Message `json:"messages"`
		}
		get(t, srv, "/v1/locations/WAT/messages", &m)
		if len(m.Messages) != 1 || m.Messages[0].ID != 91001 {
			t.Errorf("WAT messages = %+v", m.Messages)
		}

		body, err = os.ReadFile("../../testdata/darwin_messages_clear.xml")
		if err != nil {
			t.Fatal(err)
		}
		if err := applier.ApplyMessage(ctx, body); err != nil {
			t.Fatal(err)
		}
		var after api.Board
		get(t, srv, "/v1/locations/CLJ/departures?at=2026-10-06T08:00&window=5", &after)
		if len(after.Messages) != 1 || after.Messages[0].ID != 91001 {
			t.Errorf("message with no stations should be removed: %+v", after.Messages)
		}
	})

	t.Run("CORPUS reload keeps Darwin names", func(t *testing.T) {
		f, _ := os.Open("../../testdata/corpus.json")
		entries, err := corpus.Parse(f)
		f.Close()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := corpus.Load(ctx, pool, entries); err != nil {
			t.Fatal(err)
		}
		var b board
		get(t, srv, "/v1/locations/WAT/departures?at=2026-10-06T08:00&window=1", &b)
		if b.Location.Name != "London Waterloo" {
			t.Errorf("name = %q", b.Location.Name)
		}
	})

	t.Run("daily update deletes the overlay and adds a train", func(t *testing.T) {
		loadSchedule(t, pool, "../../testdata/schedule_update.json")
		var b board
		get(t, srv, "/v1/locations/CLJ/departures?at=2026-10-06T08:30&window=60&to=WIM", &b)
		if got := uids(b); got != "W10004,W19999" {
			t.Fatalf("after update = %s", got)
		}
		f, _ := os.Open("../../testdata/schedule_update.json")
		defer f.Close()
		if _, err := schedule.Load(ctx, pool, f); err == nil || !strings.Contains(err.Error(), "already applied") {
			t.Errorf("re-applying an update should fail, got %v", err)
		}
	})

	t.Run("live data follows stops when the schedule changes", func(t *testing.T) {
		// The update removed W10004's overlay, so Wimbledon is back as stop 2.
		// Darwin's forecast for Woking (stop 2 in the overlay) must stay at
		// Woking, now stop 3.
		var d api.ServiceDetail
		get(t, srv, "/v1/services/W10004/2026-10-06", &d)
		if len(d.Stops) != 4 || d.Stops[2].TIPLOC != "WIMBLDN" {
			t.Fatalf("stops = %+v", d.Stops)
		}
		if d.Stops[2].Arrival.Delayed {
			t.Errorf("Woking's forecast leaked onto Wimbledon")
		}
		if !d.Stops[3].Arrival.Delayed {
			t.Errorf("Woking lost its forecast")
		}
	})

	t.Run("Darwin Kafka record and single TRUST record", func(t *testing.T) {
		body, err := os.ReadFile("../../testdata/darwin_kafka_json.json")
		if err != nil {
			t.Fatal(err)
		}
		if err := (&darwin.Applier{Pool: pool}).ApplyMessage(ctx, body); err != nil {
			t.Fatal(err)
		}
		applier := &trust.Applier{Pool: pool, Now: func() time.Time {
			return time.Date(2026, 10, 6, 8, 59, 0, 0, ukrail.London)
		}}
		activation, _ := json.Marshal(map[string]any{
			"header": map[string]any{"msg_type": "0001"},
			"body": map[string]any{"train_id": "722V50MW06", "train_uid": "W19999",
				"tp_origin_timestamp":  "2026-10-06",
				"origin_dep_timestamp": fmt.Sprint(time.Date(2026, 10, 6, 8, 0, 0, 0, time.UTC).UnixMilli())},
		})
		if err := applier.ApplyFrame(ctx, activation); err != nil {
			t.Fatal(err)
		}
		var d api.ServiceDetail
		get(t, srv, "/v1/services/W19999/2026-10-06", &d)
		if d.Status != "activated" {
			t.Errorf("single-record activation not applied: %s", d.Status)
		}
		if got := d.Stops[0].Departure.Estimated; got == nil || got.Format("15:04") != "09:03" {
			t.Errorf("CLJ estimate = %v", got)
		}
		if d.Stops[0].Platform.Actual != "12" {
			t.Errorf("CLJ platform = %+v", d.Stops[0].Platform)
		}
	})

	t.Run("Train Describer steps", func(t *testing.T) {
		f, err := os.Open("../../testdata/smart.json")
		if err != nil {
			t.Fatal(err)
		}
		berths, err := td.ParseSMART(f)
		f.Close()
		if err != nil {
			t.Fatal(err)
		}
		if err := td.LoadSMART(ctx, pool, berths); err != nil {
			t.Fatal(err)
		}
		m, _, err := td.LoadMap(ctx, pool)
		if err != nil {
			t.Fatal(err)
		}
		proc := &td.Processor{Pool: pool, Map: m, Now: func() time.Time {
			return time.Date(2026, 10, 6, 9, 10, 0, 0, ukrail.London)
		}}
		utc := func(h, m, s int) string {
			return fmt.Sprint(time.Date(2026, 10, 6, h, m, s, 0, time.UTC).UnixMilli())
		}
		// STOMP frame: W19999 (2V50) leaves Clapham, with heartbeats and an
		// S-class message mixed in.
		frame := `[{"CT_MSG":{"msg_type":"CT","area_id":"WI","time":"` + utc(8, 1, 0) + `","report_time":"0901"}},
			{"SF_MSG":{"msg_type":"SF","area_id":"WI","time":"` + utc(8, 1, 0) + `","address":"0A","data":"FF"}},
			{"CA_MSG":{"msg_type":"CA","area_id":"WI","time":"` + utc(8, 2, 0) + `","from":"0101","to":"0103","descr":"2V50"}}]`
		if err := proc.ApplyFrame(ctx, []byte(frame)); err != nil {
			t.Fatal(err)
		}
		// W10004 (1A04) steps into 0201, the berth before Wimbledon.
		approach := `{"CA_MSG":{"msg_type":"CA","area_id":"WI","time":"` + utc(7, 45, 0) + `","from":"0199","to":"0201","descr":"1A04"}}`
		if err := proc.ApplyFrame(ctx, []byte(approach)); err != nil {
			t.Fatal(err)
		}
		var appr api.ServiceDetail
		get(t, srv, "/v1/services/W10004/2026-10-06", &appr)
		if !appr.Stops[2].Approaching || appr.Stops[2].AtPlatform {
			t.Errorf("WIM should be approaching: %+v", appr.Stops[2])
		}
		var apprCompat struct {
			Locations []compat.LocationDetail `json:"locations"`
		}
		get(t, srv, "/api/v1/json/service/W10004/2026/10/06", &apprCompat)
		if got := apprCompat.Locations[2].ServiceLocation; got != "APPR_STAT" {
			t.Errorf("compat approach = %q", got)
		}

		// Kafka record: W10004 (1A04) arrives at Wimbledon.
		record := `{"CA_MSG":{"msg_type":"CA","area_id":"WI","time":"` + utc(7, 47, 15) + `","from":"0201","to":"0203","descr":"1A04"}}`
		if err := proc.ApplyFrame(ctx, []byte(record)); err != nil {
			t.Fatal(err)
		}

		var d api.ServiceDetail
		get(t, srv, "/v1/services/W19999/2026-10-06", &d)
		if got := d.Stops[0].Departure.Actual; got == nil || got.Format("15:04:05") != "09:02:30" {
			t.Errorf("TD departure with +30s offset = %v", got)
		}
		if d.Position == nil || d.Position.Berth != "0103" || d.Position.Area != "WI" {
			t.Errorf("position = %+v", d.Position)
		}

		get(t, srv, "/v1/services/W10004/2026-10-06", &d)
		wim := d.Stops[2]
		if wim.Arrival.Actual == nil || wim.Arrival.Actual.Format("15:04:05") != "08:47:00" || !wim.AtPlatform || wim.Approaching {
			t.Errorf("WIM = %+v atPlatform=%v", wim.Arrival, wim.AtPlatform)
		}
		var svc struct {
			Locations []compat.LocationDetail `json:"locations"`
		}
		get(t, srv, "/api/v1/json/service/W10004/2026/10/06", &svc)
		if svc.Locations[2].ServiceLocation != "AT_PLAT" {
			t.Errorf("compat serviceLocation = %q", svc.Locations[2].ServiceLocation)
		}
	})

	t.Run("VSTP adds a short-notice train", func(t *testing.T) {
		rec, err := schedule.ParseVSTP([]byte(strings.ReplaceAll(vstp, "\n", "")))
		if err != nil {
			t.Fatal(err)
		}
		if err := schedule.ApplyVSTP(ctx, pool, rec, time.Date(2026, 10, 6, 12, 0, 0, 0, ukrail.London)); err != nil {
			t.Fatal(err)
		}
		var d api.ServiceDetail
		get(t, srv, "/v1/services/V12345/2026-10-06", &d)
		if d.Source != "vstp" || len(d.Stops) != 2 {
			t.Errorf("VSTP service = %+v", d)
		}
	})
}

const vstp = `{"VSTPCIFMsgV1":{"schedule":{"transaction_type":"Create","schedule_start_date":"2026-10-06",
"schedule_end_date":"2026-10-06","schedule_days_runs":"0100000","CIF_bank_holiday_running":" ","train_status":"1",
"CIF_train_uid":"V12345","CIF_stp_indicator":"N","schedule_segment":[{"signalling_id":"1Z99","atoc_code":"SW",
"CIF_train_category":"OO","CIF_power_type":"EMU","CIF_speed":"100","schedule_location":[
{"scheduled_departure_time":"120000","public_departure_time":"120000","CIF_platform":"1","location":{"tiploc":{"tiploc_id":"WATRLMN"}}},
{"scheduled_arrival_time":"121000","public_arrival_time":"121000","location":{"tiploc":{"tiploc_id":"CLPHMJN"}}}]}]}}}`
