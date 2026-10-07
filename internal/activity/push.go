package activity

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/carbonarok/trackside/internal/api"
	"github.com/carbonarok/trackside/internal/apns"
	"github.com/carbonarok/trackside/internal/live"
	"github.com/carbonarok/trackside/internal/timetable"
	"github.com/carbonarok/trackside/internal/ukrail"
)

const (
	// idleLimit removes activities nothing has happened to for this long.
	idleLimit = 6 * time.Hour
	// dismissAfter keeps an ended activity on the lock screen this long.
	dismissAfter = 15 * time.Minute
	// sendConcurrency bounds pushes in flight at once.
	sendConcurrency = 8
)

// Sender delivers one APNs notification; *apns.Client is one.
type Sender interface {
	Send(ctx context.Context, host string, n apns.Notification) (apns.Response, error)
}

// Pusher pushes Live Activity updates when trains change.
type Pusher struct {
	Pool  *pgxpool.Pool
	Store *timetable.Store
	APNs  Sender
	// Host is the APNs environment tried first (apns.Production or
	// apns.Sandbox). A token the other environment issued is retried there
	// once and remembered, so Xcode and TestFlight builds both work.
	Host string
	Now  func() time.Time

	mu      sync.Mutex
	pending live.Changes
	wake    chan struct{}
}

// NewPusher returns a pusher that sends through s, trying host first.
func NewPusher(pool *pgxpool.Pool, store *timetable.Store, s Sender, host string) *Pusher {
	return &Pusher{Pool: pool, Store: store, APNs: s, Host: host, wake: make(chan struct{}, 1)}
}

func (p *Pusher) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

// Notify queues changes from the live hub. It never blocks: changes that
// arrive while a batch is being pushed are merged into the next one.
func (p *Pusher) Notify(c live.Changes) {
	p.mu.Lock()
	p.pending.Services = append(p.pending.Services, c.Services...)
	p.pending.UIDs = append(p.pending.UIDs, c.UIDs...)
	p.pending.RIDs = append(p.pending.RIDs, c.RIDs...)
	p.mu.Unlock()
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

// Run pushes queued changes and removes stale activities until ctx ends.
func (p *Pusher) Run(ctx context.Context) {
	cleanup := time.NewTicker(10 * time.Minute)
	defer cleanup.Stop()
	p.Cleanup(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-cleanup.C:
			p.Cleanup(ctx)
		case <-p.wake:
			p.mu.Lock()
			c := p.pending
			p.pending = live.Changes{}
			p.mu.Unlock()
			if err := p.Push(ctx, c); err != nil && ctx.Err() == nil {
				slog.Warn("live activities: push failed", "err", err)
			}
		}
	}
}

// registration is one row of live_activities.
type registration struct {
	ID, Token, Bundle, UID string
	RunDate                time.Time
	Origin, Destination    string
	ConnectionMinutes      *int
	Host                   *string
	Last                   *ContentState
	LastTimestamp          int64
	// The previous leg's train, if the app named it.
	PrevUID     *string
	PrevRunDate *time.Time
	PrevCRS     *string
	// The device's own token, for normal notifications.
	DeviceToken *string
}

// Push sends updates to every activity whose train, or previous leg's
// train, changed.
func (p *Pusher) Push(ctx context.Context, c live.Changes) error {
	if len(c.Services)+len(c.UIDs)+len(c.RIDs) == 0 {
		return nil
	}
	rows, err := p.Pool.Query(ctx, `
		SELECT a.activity_id, a.push_token, a.bundle_id, a.train_uid, a.run_date, a.origin_crs,
		       a.destination_crs, a.connection_minutes, a.apns_host, a.last_state, a.last_timestamp,
		       a.previous_train_uid, a.previous_run_date, a.previous_arrival_crs, a.device_token
		FROM live_activities a
		WHERE a.train_uid = ANY($2) OR a.previous_train_uid = ANY($2)
		   OR EXISTS (SELECT 1 FROM services sv
		              WHERE ((sv.train_uid = a.train_uid AND sv.run_date = a.run_date)
		                  OR (sv.train_uid = a.previous_train_uid AND sv.run_date = a.previous_run_date))
		                AND (sv.id = ANY($1) OR sv.darwin_rid = ANY($3)))`,
		c.Services, c.UIDs, c.RIDs)
	if err != nil {
		return err
	}
	regs, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (registration, error) {
		var g registration
		var last []byte
		err := r.Scan(&g.ID, &g.Token, &g.Bundle, &g.UID, &g.RunDate, &g.Origin, &g.Destination,
			&g.ConnectionMinutes, &g.Host, &last, &g.LastTimestamp, &g.PrevUID, &g.PrevRunDate, &g.PrevCRS,
			&g.DeviceToken)
		if err == nil && last != nil {
			g.Last = &ContentState{}
			if json.Unmarshal(last, g.Last) != nil {
				g.Last = nil
			}
		}
		return g, err
	})
	if err != nil || len(regs) == 0 {
		return err
	}

	// One service lookup per train, however many activities follow it.
	type key struct {
		uid  string
		date time.Time
	}
	details := map[key]*api.ServiceDetail{}
	lookup := func(uid string, date time.Time) *api.ServiceDetail {
		k := key{uid, date}
		if d, seen := details[k]; seen {
			return d
		}
		svc, err := p.Store.Service(ctx, uid, date)
		if err != nil {
			if !errors.Is(err, timetable.ErrNotFound) {
				slog.Warn("live activities: service lookup failed", "uid", uid, "err", err)
			}
			details[k] = nil
			return nil
		}
		d := api.Detail(svc)
		details[k] = &d
		return &d
	}
	sem := make(chan struct{}, sendConcurrency)
	var wg sync.WaitGroup
	for _, g := range regs {
		d := lookup(g.UID, g.RunDate)
		if d == nil {
			continue
		}
		// With the previous leg's train known, the connection follows both
		// trains' live times; otherwise it stays as the app registered it.
		if g.PrevUID != nil && g.PrevRunDate != nil && g.PrevCRS != nil {
			if prev := lookup(*g.PrevUID, *g.PrevRunDate); prev != nil {
				if conn := Connection(*prev, *g.PrevCRS, *d, g.Origin); conn != nil {
					g.ConnectionMinutes = conn
				}
			}
		}
		sem <- struct{}{}
		wg.Add(1)
		go func(g registration, d *api.ServiceDetail) {
			defer func() { <-sem; wg.Done() }()
			p.update(ctx, g, d)
		}(g, d)
	}
	wg.Wait()
	return nil
}

// aps is the Live Activity notification payload.
type aps struct {
	Timestamp     int64        `json:"timestamp"`
	Event         string       `json:"event"`
	ContentState  ContentState `json:"content-state"`
	Alert         *Alert       `json:"alert,omitempty"`
	DismissalDate int64        `json:"dismissal-date,omitempty"`
}

// update rebuilds one activity's state and pushes it if it changed.
func (p *Pusher) update(ctx context.Context, g registration, d *api.ServiceDetail) {
	now := p.now()
	leg := Leg{OriginCRS: g.Origin, DestinationCRS: g.Destination, ConnectionMinutes: g.ConnectionMinutes}
	if g.Last != nil {
		leg.Phase = g.Last.Phase
		leg.Last = g.Last
	}
	next, ok := State(*d, leg, now)
	if !ok {
		slog.Warn("live activities: leg no longer on its service", "activity", g.ID, "uid", g.UID)
		return
	}
	if g.Last != nil && next.sameAs(*g.Last) {
		return // the change was something the activity doesn't show
	}
	t := Train{Headcode: d.Headcode}
	if from, to, ok := Endpoints(*d, g.Origin, g.Destination); ok {
		t.From, t.To = from.Name, to.Name
	}
	alert := AlertFor(g.Last, next, t)
	// The timestamp orders updates on the device, so it must always rise.
	ts := max(now.Unix(), g.LastTimestamp+1)
	body := aps{Timestamp: ts, Event: "update", ContentState: next, Alert: alert}
	// With the device's token the alert goes as a normal notification, whose
	// text shows; the activity then updates without one, so it buzzes once.
	notify := alert != nil && g.DeviceToken != nil
	if notify {
		body.Alert = nil
	}
	ended := next.Phase == PhaseArrived
	if ended {
		body.Event = "end"
		body.DismissalDate = now.Add(dismissAfter).Unix()
	}
	payload, err := json.Marshal(map[string]aps{"aps": body})
	if err != nil {
		slog.Error("live activities: encode payload", "err", err)
		return
	}
	n := apns.Notification{
		DeviceToken: g.Token,
		Topic:       g.Bundle + ".push-type.liveactivity",
		PushType:    "liveactivity",
		Priority:    5,
		Payload:     payload,
	}
	if alert != nil || ended {
		n.Priority = 10
	}

	host := p.Host
	if g.Host != nil {
		host = *g.Host
	}
	resp, err := p.APNs.Send(ctx, host, n)
	if err == nil && resp.BadToken() && g.Host == nil {
		// Probably the other environment's token: try it there once.
		host = otherHost(host)
		resp, err = p.APNs.Send(ctx, host, n)
	}
	switch {
	case err != nil:
		slog.Warn("live activities: apns unreachable", "activity", g.ID, "err", err)
	case resp.OK() && ended:
		// Journey complete: nothing more to send.
		if notify {
			p.notify(ctx, g, host, *alert)
		}
		p.remove(ctx, g.ID, "arrived")
	case resp.OK():
		state, _ := json.Marshal(next)
		if _, err := p.Pool.Exec(ctx, `UPDATE live_activities SET last_state = $2, last_timestamp = $3,
			apns_host = $4, updated_at = now() WHERE activity_id = $1 AND push_token = $5`,
			g.ID, state, ts, host, g.Token); err != nil {
			slog.Warn("live activities: save state", "activity", g.ID, "err", err)
		}
		if notify {
			p.notify(ctx, g, host, *alert)
		}
	case resp.Gone():
		p.remove(ctx, g.ID, "ended on the device")
	case resp.BadToken():
		p.remove(ctx, g.ID, "token rejected in both environments")
	default:
		slog.Warn("live activities: apns refused", "activity", g.ID, "status", resp.Status, "reason", resp.Reason)
	}
}

// notify sends the alert as a normal notification to the device, grouped
// per train. A token APNs rejects is forgotten; the activity's own alerts
// then take over.
func (p *Pusher) notify(ctx context.Context, g registration, host string, a Alert) {
	payload, _ := json.Marshal(map[string]any{"aps": map[string]any{
		"alert":     map[string]string{"title": a.Title, "body": a.Body},
		"sound":     alertSound,
		"thread-id": "trackside-" + g.UID + "-" + g.RunDate.Format(time.DateOnly),
	}})
	n := apns.Notification{
		DeviceToken: *g.DeviceToken,
		Topic:       g.Bundle,
		PushType:    "alert",
		Priority:    10,
		Payload:     payload,
	}
	resp, err := p.APNs.Send(ctx, host, n)
	if err == nil && resp.BadToken() {
		resp, err = p.APNs.Send(ctx, otherHost(host), n)
	}
	switch {
	case err != nil:
		slog.Warn("live activities: notification unreachable", "activity", g.ID, "err", err)
	case resp.OK():
	case resp.Gone() || resp.BadToken():
		if _, err := p.Pool.Exec(ctx, `UPDATE live_activities SET device_token = NULL WHERE device_token = $1`, *g.DeviceToken); err != nil {
			slog.Warn("live activities: forget device token", "err", err)
		}
	default:
		slog.Warn("live activities: notification refused", "activity", g.ID, "status", resp.Status, "reason", resp.Reason)
	}
}

func otherHost(h string) string {
	if h == apns.Sandbox {
		return apns.Production
	}
	return apns.Sandbox
}

func (p *Pusher) remove(ctx context.Context, id, why string) {
	if _, err := p.Pool.Exec(ctx, `DELETE FROM live_activities WHERE activity_id = $1`, id); err != nil {
		slog.Warn("live activities: remove", "activity", id, "err", err)
		return
	}
	slog.Debug("live activity removed", "activity", id, "why", why)
}

// Cleanup removes activities idle for six hours and any for trains that ran
// before yesterday.
func (p *Pusher) Cleanup(ctx context.Context) {
	now := p.now()
	tag, err := p.Pool.Exec(ctx, `DELETE FROM live_activities
		WHERE updated_at < $1 OR run_date < $2`, now.Add(-idleLimit), ukrail.DateOf(now).AddDate(0, 0, -1))
	if err != nil {
		if ctx.Err() == nil {
			slog.Warn("live activities: cleanup", "err", err)
		}
		return
	}
	if n := tag.RowsAffected(); n > 0 {
		slog.Info("live activities: removed idle", "count", n)
	}
}
