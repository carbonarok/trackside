package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/carbonarok/trackside/internal/activity"
	"github.com/carbonarok/trackside/internal/apns"
	"github.com/carbonarok/trackside/internal/live"
	"github.com/carbonarok/trackside/internal/timetable"
	"github.com/carbonarok/trackside/internal/trust"
	"github.com/carbonarok/trackside/internal/ukrail"
)

// fakeAPNs records notifications and answers per device token and host.
type fakeAPNs struct {
	mu     sync.Mutex
	sent   []sent
	answer func(host, token string) apns.Response
}

type sent struct {
	host string
	n    apns.Notification
}

func (f *fakeAPNs) Send(_ context.Context, host string, n apns.Notification) (apns.Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, sent{host, n})
	if f.answer != nil {
		return f.answer(host, n.DeviceToken), nil
	}
	return apns.Response{Status: http.StatusOK}, nil
}

func (f *fakeAPNs) take() []sent {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.sent
	f.sent = nil
	return out
}

type payload struct {
	APS struct {
		Timestamp     int64           `json:"timestamp"`
		Event         string          `json:"event"`
		ContentState  json.RawMessage `json:"content-state"`
		Alert         *activity.Alert `json:"alert"`
		DismissalDate int64           `json:"dismissal-date"`
	} `json:"aps"`
}

func decode(t *testing.T, s sent) (payload, activity.ContentState) {
	t.Helper()
	var p payload
	if err := json.Unmarshal(s.n.Payload, &p); err != nil {
		t.Fatal(err)
	}
	var cs activity.ContentState
	if err := json.Unmarshal(p.APS.ContentState, &cs); err != nil {
		t.Fatal(err)
	}
	return p, cs
}

func TestLiveActivityPushes(t *testing.T) {
	pool, _ := setup(t)
	ctx := context.Background()
	clock := time.Date(2026, 10, 6, 7, 50, 0, 0, ukrail.London)
	now := func() time.Time { return clock }
	store := &timetable.Store{Pool: pool, Now: now}

	srv := &activity.Server{Pool: pool, Store: store, Enabled: true, Now: now,
		Bundles: map[string]bool{"com.example.TrackSideIOS": true}}
	mux := http.NewServeMux()
	srv.Register(mux)
	api := httptest.NewServer(mux)
	defer api.Close()

	post := func(body map[string]any) (int, string) {
		b, _ := json.Marshal(body)
		resp, err := http.Post(api.URL+"/v1/activities/register", "application/json", bytes.NewReader(b))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]string
		json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out["error"]
	}
	// Waterloo to Wimbledon on W10001 (08:00 from Waterloo).
	reg := func(id, token string) map[string]any {
		return map[string]any{
			"push_token": token, "activity_id": id, "service_uid": "W10001", "run_date": "2026-10-06",
			"origin_crs": "WAT", "destination_crs": "WIM", "bundle_id": "com.example.TrackSideIOS",
			"connection_minutes": 7,
		}
	}
	tokenA := strings.Repeat("a1", 32)

	t.Run("registration", func(t *testing.T) {
		if code, msg := post(reg("act-1", tokenA)); code != http.StatusCreated {
			t.Fatalf("register: %d %s", code, msg)
		}
		// The same activity with a new token updates in place.
		if code, _ := post(reg("act-1", strings.Repeat("b2", 32))); code != http.StatusOK {
			t.Errorf("re-register: %d, want 200", code)
		}
		if code, _ := post(reg("act-1", tokenA)); code != http.StatusOK {
			t.Errorf("re-register back: %d", code)
		}
		bad := []struct {
			name string
			edit func(map[string]any)
			code int
		}{
			{"token not hex", func(m map[string]any) { m["push_token"] = "not-hex" }, 400},
			{"bad date", func(m map[string]any) { m["run_date"] = "06/10/2026" }, 400},
			{"unknown field", func(m map[string]any) { m["surprise"] = true }, 400},
			{"other app", func(m map[string]any) { m["bundle_id"] = "com.other.App" }, 403},
			{"no such train", func(m map[string]any) { m["service_uid"] = "W99999" }, 404},
			{"leg backwards", func(m map[string]any) { m["origin_crs"], m["destination_crs"] = "WIM", "WAT" }, 422},
		}
		for _, b := range bad {
			m := reg("act-x", tokenA)
			b.edit(m)
			if code, _ := post(m); code != b.code {
				t.Errorf("%s: %d, want %d", b.name, code, b.code)
			}
		}
		off := &activity.Server{Pool: pool, Store: store}
		mux := http.NewServeMux()
		off.Register(mux)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/activities/register", strings.NewReader("{}")))
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("without APNs: %d, want 503", rec.Code)
		}
	})

	fake := &fakeAPNs{}
	pusher := activity.NewPusher(pool, store, fake, apns.Production)
	pusher.Now = now
	applier := &trust.Applier{Pool: pool, Now: now}
	changes := live.Changes{UIDs: []string{"W10001"}}

	t.Run("late departure pushes an update with an alert", func(t *testing.T) {
		clock = time.Date(2026, 10, 6, 8, 10, 0, 0, ukrail.London)
		frame := trustFrame(t,
			map[string]any{"msg_type": "0001", "train_id": "721A01MX06", "train_uid": "W10001",
				"tp_origin_timestamp":  "2026-10-06",
				"origin_dep_timestamp": fmt.Sprint(time.Date(2026, 10, 6, 7, 0, 0, 0, time.UTC).UnixMilli())},
			map[string]any{"msg_type": "0003", "train_id": "721A01MX06", "event_type": "DEPARTURE",
				"loc_stanox": "87701", "actual_timestamp": localMillis(8, 9), "planned_timestamp": localMillis(8, 0),
				"platform": " 5", "offroute_ind": "false"},
		)
		if err := applier.ApplyFrame(ctx, frame); err != nil {
			t.Fatal(err)
		}
		if err := pusher.Push(ctx, changes); err != nil {
			t.Fatal(err)
		}
		got := fake.take()
		if len(got) != 1 {
			t.Fatalf("%d notifications, want 1", len(got))
		}
		n := got[0].n
		if n.Topic != "com.example.TrackSideIOS.push-type.liveactivity" || n.PushType != "liveactivity" ||
			n.DeviceToken != tokenA || got[0].host != apns.Production {
			t.Errorf("notification %+v to %s", n, got[0].host)
		}
		p, cs := decode(t, got[0])
		if p.APS.Event != "update" || p.APS.Timestamp != clock.Unix() {
			t.Errorf("aps event=%s timestamp=%d", p.APS.Event, p.APS.Timestamp)
		}
		if cs.Phase != activity.PhaseOnTrain || cs.ArrivalDelayMinutes != 9 || *cs.ConnectionMinutes != 7 {
			t.Errorf("state %+v", cs)
		}
		// A 9-minute arrival delay, up from on time, is worth an alert.
		if p.APS.Alert == nil || p.APS.Alert.Title != "Running late" || n.Priority != 10 {
			t.Errorf("alert %+v priority %d", p.APS.Alert, n.Priority)
		}
		if !strings.Contains(string(p.APS.ContentState), `"departureDate":`+fmt.Sprint(time.Date(2026, 10, 6, 7, 9, 0, 0, time.UTC).Unix()-978307200)) {
			t.Errorf("departureDate not seconds since 2001: %s", p.APS.ContentState)
		}
	})

	t.Run("nothing new, nothing sent", func(t *testing.T) {
		clock = clock.Add(time.Minute)
		if err := pusher.Push(ctx, changes); err != nil {
			t.Fatal(err)
		}
		if got := fake.take(); len(got) != 0 {
			t.Errorf("%d notifications for an unchanged state", len(got))
		}
	})

	t.Run("arrival ends the activity and removes it", func(t *testing.T) {
		clock = time.Date(2026, 10, 6, 8, 22, 0, 0, ukrail.London)
		var wim string
		if err := pool.QueryRow(ctx, `SELECT stanox FROM locations WHERE crs = 'WIM' AND stanox IS NOT NULL LIMIT 1`).Scan(&wim); err != nil {
			t.Fatal(err)
		}
		frame := trustFrame(t, map[string]any{"msg_type": "0003", "train_id": "721A01MX06", "event_type": "ARRIVAL",
			"loc_stanox": wim, "actual_timestamp": localMillis(8, 21), "planned_timestamp": localMillis(8, 12),
			"offroute_ind": "false"})
		if err := applier.ApplyFrame(ctx, frame); err != nil {
			t.Fatal(err)
		}
		if err := pusher.Push(ctx, changes); err != nil {
			t.Fatal(err)
		}
		got := fake.take()
		if len(got) != 1 {
			t.Fatalf("%d notifications, want 1", len(got))
		}
		p, cs := decode(t, got[0])
		if p.APS.Event != "end" || cs.Phase != activity.PhaseArrived || p.APS.DismissalDate == 0 || got[0].n.Priority != 10 {
			t.Errorf("end payload %+v phase %s priority %d", p.APS, cs.Phase, got[0].n.Priority)
		}
		var n int
		pool.QueryRow(ctx, `SELECT count(*) FROM live_activities WHERE activity_id = 'act-1'`).Scan(&n)
		if n != 0 {
			t.Error("arrived activity was not removed")
		}
	})

	t.Run("APNs answers decide what's kept", func(t *testing.T) {
		gone, sandbox := strings.Repeat("c3", 32), strings.Repeat("d4", 32)
		// Legs on to Woking, which the train hasn't reached.
		for id, tok := range map[string]string{"act-gone": gone, "act-sandbox": sandbox} {
			m := reg(id, tok)
			m["destination_crs"] = "WOK"
			if code, msg := post(m); code != http.StatusCreated {
				t.Fatalf("register %s: %d %s", id, code, msg)
			}
		}
		fake.answer = func(host, token string) apns.Response {
			switch {
			case token == gone:
				return apns.Response{Status: http.StatusGone, Reason: "Unregistered"}
			case token == sandbox && host == apns.Production:
				return apns.Response{Status: http.StatusBadRequest, Reason: "BadDeviceToken"}
			}
			return apns.Response{Status: http.StatusOK}
		}
		// Make the last state sent differ, so there is something to push.
		if _, err := pool.Exec(ctx, `UPDATE live_activities SET last_state = jsonb_set(last_state, '{arrivalDelayMinutes}', '0')
			WHERE activity_id IN ('act-gone', 'act-sandbox')`); err != nil {
			t.Fatal(err)
		}
		if err := pusher.Push(ctx, changes); err != nil {
			t.Fatal(err)
		}
		fake.take()
		var ids []string
		rows, _ := pool.Query(ctx, `SELECT activity_id || ':' || COALESCE(apns_host, '') FROM live_activities ORDER BY 1`)
		for rows.Next() {
			var s string
			rows.Scan(&s)
			ids = append(ids, s)
		}
		if strings.Join(ids, ",") != "act-sandbox:"+apns.Sandbox {
			t.Errorf("kept %v; want only act-sandbox, remembered on the sandbox host", ids)
		}
	})

	t.Run("idle activities are cleaned up and deregistering is idempotent", func(t *testing.T) {
		pool.Exec(ctx, `UPDATE live_activities SET updated_at = $1`, clock.Add(-7*time.Hour))
		pusher.Cleanup(ctx)
		var n int
		pool.QueryRow(ctx, `SELECT count(*) FROM live_activities`).Scan(&n)
		if n != 0 {
			t.Errorf("%d idle activities left", n)
		}
		for range 2 {
			req, _ := http.NewRequest(http.MethodDelete, api.URL+"/v1/activities/act-sandbox", nil)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusNoContent {
				t.Errorf("delete: %d, want 204", resp.StatusCode)
			}
		}
	})
}
