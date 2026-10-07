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
		if p.APS.Alert == nil || p.APS.Alert.Title != "Running late" || p.APS.Alert.Sound != "default" || n.Priority != 10 {
			t.Errorf("alert %+v priority %d", p.APS.Alert, n.Priority)
		}
		// The alert names the station the train is left at.
		if p.APS.Alert != nil && !strings.Contains(p.APS.Alert.Body, "arrives at Wimbledon") {
			t.Errorf("alert body %q", p.APS.Alert.Body)
		}
		if !strings.Contains(string(n.Payload), `"sound":"default"`) {
			t.Errorf("payload has no sound: %s", n.Payload)
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

func TestLiveActivityConnections(t *testing.T) {
	pool, _ := setup(t)
	ctx := context.Background()
	clock := time.Date(2026, 10, 6, 7, 50, 0, 0, ukrail.London)
	now := func() time.Time { return clock }
	store := &timetable.Store{Pool: pool, Now: now}
	srv := &activity.Server{Pool: pool, Store: store, Enabled: true, Now: now}
	mux := http.NewServeMux()
	srv.Register(mux)
	api := httptest.NewServer(mux)
	defer api.Close()

	// Leg 2 of a journey: W10004 from Clapham Junction (08:37) to Woking,
	// after leg 1 on W10001 into Clapham Junction (08:06): 31 minutes.
	body, _ := json.Marshal(map[string]any{
		"push_token": strings.Repeat("e5", 32), "activity_id": "leg-2", "service_uid": "W10004",
		"run_date": "2026-10-06", "origin_crs": "CLJ", "destination_crs": "WOK",
		"bundle_id": "com.example.TrackSideIOS", "connection_minutes": 99,
		"previous_service_uid": "W10001",
	})
	resp, err := http.Post(api.URL+"/v1/activities/register", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("register: %d", resp.StatusCode)
	}
	var registered int
	pool.QueryRow(ctx, `SELECT (last_state->>'connectionMinutes')::int FROM live_activities`).Scan(&registered)
	if registered != 31 {
		t.Errorf("connection at registration %d, want 31 from the timetable (not the app's 99)", registered)
	}

	// A previous leg that doesn't reach the connecting station is refused.
	bad, _ := json.Marshal(map[string]any{
		"push_token": strings.Repeat("e5", 32), "activity_id": "leg-x", "service_uid": "W10004",
		"run_date": "2026-10-06", "origin_crs": "CLJ", "destination_crs": "WOK",
		"bundle_id": "com.example.TrackSideIOS", "previous_service_uid": "W10001", "previous_arrival_crs": "PAD",
	})
	if resp, _ := http.Post(api.URL+"/v1/activities/register", "application/json", bytes.NewReader(bad)); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("previous leg not via the station: %d, want 422", resp.StatusCode)
	}

	// Leg 1's train reaches Clapham Junction 9 minutes late. Only W10001
	// changed, but leg 2's activity is updated: 22 minutes now.
	clock = time.Date(2026, 10, 6, 8, 16, 0, 0, ukrail.London)
	applier := &trust.Applier{Pool: pool, Now: now}
	frame := trustFrame(t,
		map[string]any{"msg_type": "0001", "train_id": "721A01MX06", "train_uid": "W10001",
			"tp_origin_timestamp":  "2026-10-06",
			"origin_dep_timestamp": fmt.Sprint(time.Date(2026, 10, 6, 7, 0, 0, 0, time.UTC).UnixMilli())},
		map[string]any{"msg_type": "0003", "train_id": "721A01MX06", "event_type": "ARRIVAL",
			"loc_stanox": "87703", "actual_timestamp": localMillis(8, 15), "planned_timestamp": localMillis(8, 6),
			"offroute_ind": "false"},
	)
	if err := applier.ApplyFrame(ctx, frame); err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPNs{}
	pusher := activity.NewPusher(pool, store, fake, apns.Production)
	pusher.Now = now
	if err := pusher.Push(ctx, live.Changes{UIDs: []string{"W10001"}}); err != nil {
		t.Fatal(err)
	}
	got := fake.take()
	if len(got) != 1 {
		t.Fatalf("%d notifications after leg 1 changed, want 1", len(got))
	}
	_, cs := decode(t, got[0])
	if cs.ConnectionMinutes == nil || *cs.ConnectionMinutes != 22 {
		t.Errorf("pushed connection %v, want 22", cs.ConnectionMinutes)
	}
}

func TestLiveActivityNotifications(t *testing.T) {
	pool, _ := setup(t)
	ctx := context.Background()
	clock := time.Date(2026, 10, 6, 7, 50, 0, 0, ukrail.London)
	now := func() time.Time { return clock }
	store := &timetable.Store{Pool: pool, Now: now}
	srv := &activity.Server{Pool: pool, Store: store, Enabled: true, Now: now}
	mux := http.NewServeMux()
	srv.Register(mux)
	api := httptest.NewServer(mux)
	defer api.Close()

	device := strings.Repeat("f6", 32)
	body, _ := json.Marshal(map[string]any{
		"push_token": strings.Repeat("a7", 32), "activity_id": "act-n", "service_uid": "W10001",
		"run_date": "2026-10-06", "origin_crs": "WAT", "destination_crs": "WIM",
		"bundle_id": "com.example.TrackSideIOS", "device_token": device,
	})
	resp, err := http.Post(api.URL+"/v1/activities/register", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("register: %d", resp.StatusCode)
	}

	fake := &fakeAPNs{}
	pusher := activity.NewPusher(pool, store, fake, apns.Production)
	pusher.Now = now
	// The platform last sent was 9; it's 5 now.
	plant := func() {
		if _, err := pool.Exec(ctx, `UPDATE live_activities SET last_state = jsonb_set(last_state, '{departurePlatform}', '"9"')`); err != nil {
			t.Fatal(err)
		}
	}
	plant()
	clock = clock.Add(5 * time.Minute)
	if err := pusher.Push(ctx, live.Changes{UIDs: []string{"W10001"}}); err != nil {
		t.Fatal(err)
	}
	got := fake.take()
	if len(got) != 2 {
		t.Fatalf("%d notifications, want 2 (the activity and the device)", len(got))
	}
	activityPush, devicePush := got[0], got[1]
	p, cs := decode(t, activityPush)
	if activityPush.n.PushType != "liveactivity" || p.APS.Alert != nil || activityPush.n.Priority != 10 {
		t.Errorf("activity push: type %s alert %+v priority %d; want a silent, immediate update",
			activityPush.n.PushType, p.APS.Alert, activityPush.n.Priority)
	}
	if !cs.DeparturePlatformChanged || cs.DeparturePlatform == nil || *cs.DeparturePlatform != "5" {
		t.Errorf("activity shows platform %v changed=%v; want 5, changed", cs.DeparturePlatform, cs.DeparturePlatformChanged)
	}
	if devicePush.n.PushType != "alert" || devicePush.n.Topic != "com.example.TrackSideIOS" || devicePush.n.DeviceToken != device {
		t.Errorf("device push %+v", devicePush.n)
	}
	var note struct {
		APS struct {
			Alert    activity.Alert `json:"alert"`
			Sound    string         `json:"sound"`
			ThreadID string         `json:"thread-id"`
		} `json:"aps"`
	}
	json.Unmarshal(devicePush.n.Payload, &note)
	if note.APS.Alert.Title != "Platform changed: now 5" || !strings.Contains(note.APS.Alert.Body, "from platform 5, not 9") ||
		note.APS.Sound != "default" || note.APS.ThreadID != "trackside-W10001-2026-10-06" {
		t.Errorf("notification %+v", note.APS)
	}

	// A device token APNs rejects is forgotten; the activity carries on and
	// falls back to its own alerts.
	fake.answer = func(_, token string) apns.Response {
		if token == device {
			return apns.Response{Status: http.StatusGone, Reason: "Unregistered"}
		}
		return apns.Response{Status: http.StatusOK}
	}
	plant()
	clock = clock.Add(time.Minute)
	if err := pusher.Push(ctx, live.Changes{UIDs: []string{"W10001"}}); err != nil {
		t.Fatal(err)
	}
	fake.take()
	var token *string
	var n int
	pool.QueryRow(ctx, `SELECT device_token, count(*) OVER () FROM live_activities`).Scan(&token, &n)
	if n != 1 || token != nil {
		t.Errorf("after 410: %d activities, device token %v; want the activity kept and the token forgotten", n, token)
	}
	plant()
	clock = clock.Add(time.Minute)
	pusher.Push(ctx, live.Changes{UIDs: []string{"W10001"}})
	if got := fake.take(); len(got) != 1 || !strings.Contains(string(got[0].n.Payload), `"alert"`) {
		t.Errorf("without a device token the activity should carry the alert itself: %d pushes", len(got))
	}
}
