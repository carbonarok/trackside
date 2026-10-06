// Package trust applies Network Rail TRUST train movement messages
// (activations, movements, cancellations and so on) to running services.
package trust

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/carbonarok/trackside/internal/schedule"
	"github.com/carbonarok/trackside/internal/ukrail"
)

// Message is one TRUST message. A STOMP frame carries a JSON array of these.
type Message struct {
	Header struct {
		MsgType string `json:"msg_type"`
	} `json:"header"`
	Body json.RawMessage `json:"body"`
}

type activation struct {
	TrainID         string `json:"train_id"`
	TrainUID        string `json:"train_uid"`
	OriginDate      string `json:"tp_origin_timestamp"`
	OriginDeparture string `json:"origin_dep_timestamp"`
}

type cancellation struct {
	TrainID    string `json:"train_id"`
	LocSTANOX  string `json:"loc_stanox"`
	ReasonCode string `json:"canx_reason_code"`
	Type       string `json:"canx_type"`
}

type movement struct {
	TrainID          string `json:"train_id"`
	EventType        string `json:"event_type"`
	PlannedEventType string `json:"planned_event_type"`
	LocSTANOX        string `json:"loc_stanox"`
	ActualTimestamp  string `json:"actual_timestamp"`
	PlannedTimestamp string `json:"planned_timestamp"`
	Platform         string `json:"platform"`
	OffRoute         string `json:"offroute_ind"`
}

type simple struct {
	TrainID        string `json:"train_id"`
	LocSTANOX      string `json:"loc_stanox"`
	RevisedTrainID string `json:"revised_train_id"`
}

// Applier writes TRUST messages to the database.
type Applier struct {
	Pool *pgxpool.Pool
	// Now is the clock used to bound train ID lookups; tests override it.
	Now func() time.Time
}

// ErrUnknownTrain means a message referred to a train ID with no activated
// service, which is normal for trains activated before we started listening.
var ErrUnknownTrain = errors.New("unknown train id")

// ApplyFrame decodes and applies a feed message body: a JSON array of
// messages (a STOMP frame) or a single message (a Rail Data Marketplace
// Kafka record). Errors on individual messages are logged, not returned, so
// one bad message does not block the stream.
func (a *Applier) ApplyFrame(ctx context.Context, body []byte) error {
	var msgs []Message
	body = bytes.TrimSpace(body)
	if len(body) > 0 && body[0] == '{' {
		var m Message
		if err := json.Unmarshal(body, &m); err != nil {
			return fmt.Errorf("decode message: %w", err)
		}
		msgs = []Message{m}
	} else if err := json.Unmarshal(body, &msgs); err != nil {
		return fmt.Errorf("decode frame: %w", err)
	}
	for _, m := range msgs {
		if err := a.Apply(ctx, m); err != nil && !errors.Is(err, ErrUnknownTrain) {
			slog.Warn("trust message failed", "type", m.Header.MsgType, "err", err)
		}
	}
	return nil
}

// Apply applies a single TRUST message.
func (a *Applier) Apply(ctx context.Context, m Message) error {
	switch m.Header.MsgType {
	case "0001":
		var b activation
		if err := json.Unmarshal(m.Body, &b); err != nil {
			return err
		}
		return a.activate(ctx, b)
	case "0002":
		var b cancellation
		if err := json.Unmarshal(m.Body, &b); err != nil {
			return err
		}
		return a.exec(ctx, b.TrainID, `UPDATE services SET cancel_stanox = $2, cancel_type = $3,
			cancel_reason = $4 WHERE id = $1`, b.LocSTANOX, b.Type, b.ReasonCode)
	case "0003":
		var b movement
		if err := json.Unmarshal(m.Body, &b); err != nil {
			return err
		}
		return a.move(ctx, b)
	case "0005":
		var b simple
		if err := json.Unmarshal(m.Body, &b); err != nil {
			return err
		}
		return a.exec(ctx, b.TrainID, `UPDATE services SET cancel_stanox = NULL, cancel_type = NULL,
			cancel_reason = NULL WHERE id = $1`)
	case "0006":
		var b simple
		if err := json.Unmarshal(m.Body, &b); err != nil {
			return err
		}
		return a.exec(ctx, b.TrainID, `UPDATE services SET origin_stanox = $2 WHERE id = $1`, b.LocSTANOX)
	case "0007":
		var b simple
		if err := json.Unmarshal(m.Body, &b); err != nil {
			return err
		}
		return a.exec(ctx, b.TrainID, `UPDATE services SET trust_id = $2 WHERE id = $1`, b.RevisedTrainID)
	}
	return nil
}

func (a *Applier) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

func (a *Applier) activate(ctx context.Context, b activation) error {
	runDate, err := activationRunDate(b)
	if err != nil {
		return fmt.Errorf("activation %s: %w", b.TrainID, err)
	}
	uid := strings.TrimSpace(b.TrainUID)
	update := func() (int64, error) {
		tag, err := a.Pool.Exec(ctx, `UPDATE services SET trust_id = $3, activated_at = $4
			WHERE train_uid = $1 AND run_date = $2`, uid, runDate, b.TrainID, a.now())
		return tag.RowsAffected(), err
	}
	n, err := update()
	if err != nil || n > 0 {
		return err
	}
	// The service may not be resolved yet, for example a VSTP schedule that
	// arrived moments ago or a date outside the window. Resolve and retry.
	if err := schedule.RefreshTrain(ctx, a.Pool, uid, runDate); err != nil {
		return err
	}
	if n, err = update(); err == nil && n == 0 {
		slog.Debug("activation for train with no schedule", "uid", uid, "date", b.OriginDate)
	}
	return err
}

// activationRunDate works out the date a train runs. tp_origin_timestamp is
// that date, but it is wrong during BST for trains starting between 00:01
// and 02:00, so origin_dep_timestamp (genuine UTC, unlike most TRUST
// timestamps) is preferred.
func activationRunDate(b activation) (time.Time, error) {
	if ms, err := strconv.ParseInt(strings.TrimSpace(b.OriginDeparture), 10, 64); err == nil && ms > 0 {
		return ukrail.DateOf(time.UnixMilli(ms)), nil
	}
	d, err := time.Parse(time.DateOnly, b.OriginDate)
	if err != nil {
		return time.Time{}, fmt.Errorf("origin date %q: %w", b.OriginDate, err)
	}
	return d, nil
}

// serviceID finds the most recent service carrying a TRUST train ID. Train
// IDs embed the day of month, so they are only unique over a few days.
func (a *Applier) serviceID(ctx context.Context, trainID string) (int64, error) {
	var id int64
	since := ukrail.DateOf(a.now()).AddDate(0, 0, -2)
	err := a.Pool.QueryRow(ctx, `SELECT id FROM services WHERE trust_id = $1 AND run_date >= $2
		ORDER BY run_date DESC LIMIT 1`, trainID, since).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrUnknownTrain
	}
	return id, err
}

func (a *Applier) exec(ctx context.Context, trainID, sql string, args ...any) error {
	id, err := a.serviceID(ctx, trainID)
	if err != nil {
		return err
	}
	_, err = a.Pool.Exec(ctx, sql, append([]any{id}, args...)...)
	return err
}

// adopt finds the service for a movement whose train ID we never saw
// activated. A TRUST train ID embeds the headcode (characters 3-6) and the
// day of the month the train started (characters 9-10); together with the
// location and planned time of the movement that identifies the service.
// The train ID is then recorded so later messages are matched directly.
func (a *Applier) adopt(ctx context.Context, b movement) (int64, error) {
	if len(b.TrainID) != 10 {
		return 0, ErrUnknownTrain
	}
	headcode := b.TrainID[2:6]
	day, err := strconv.Atoi(b.TrainID[8:10])
	if err != nil {
		return 0, ErrUnknownTrain
	}
	planned, err := Timestamp(b.PlannedTimestamp)
	if err != nil {
		return 0, ErrUnknownTrain
	}
	today := ukrail.DateOf(a.now())
	var runDate time.Time
	for _, d := range []time.Time{today, today.AddDate(0, 0, -1)} {
		if d.Day() == day {
			runDate = d
		}
	}
	if runDate.IsZero() {
		return 0, ErrUnknownTrain
	}
	plannedSecs := int(planned.Sub(ukrail.AtRunDate(runDate, 0)).Seconds())
	rows, err := a.Pool.Query(ctx, `
		SELECT DISTINCT sv.id
		FROM services sv
		JOIN schedules s ON s.id = sv.schedule_id
		JOIN schedule_locations sl ON sl.schedule_id = sv.schedule_id
		JOIN locations l ON l.tiploc = sl.tiploc
		WHERE sv.run_date = $1 AND s.signalling_id = $2 AND l.stanox = $3
		  AND sv.trust_id IS NULL AND NOT sv.planned_cancel
		  AND abs(COALESCE(sl.wtt_pass, CASE WHEN $5 THEN sl.wtt_arr ELSE sl.wtt_dep END,
		                   sl.wtt_arr, sl.wtt_dep) - $4) <= $6`,
		runDate, headcode, b.LocSTANOX, plannedSecs, b.EventType == "ARRIVAL", matchTolerance)
	if err != nil {
		return 0, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return 0, err
	}
	// Ambiguous matches are left alone rather than guessed.
	if len(ids) != 1 {
		return 0, ErrUnknownTrain
	}
	if _, err := a.Pool.Exec(ctx, `UPDATE services SET trust_id = $2 WHERE id = $1`, ids[0], b.TrainID); err != nil {
		return 0, err
	}
	return ids[0], nil
}

// matchTolerance is how far a TRUST planned time may be from the timetable
// before we refuse to attach the event to that location.
const matchTolerance = 5 * 60

func (a *Applier) move(ctx context.Context, b movement) error {
	if b.OffRoute == "true" {
		return nil
	}
	id, err := a.serviceID(ctx, b.TrainID)
	if errors.Is(err, ErrUnknownTrain) {
		// Trains activated before we started listening are adopted from
		// their first movement.
		id, err = a.adopt(ctx, b)
	}
	if err != nil {
		return err
	}
	actual, err := Timestamp(b.ActualTimestamp)
	if err != nil {
		return fmt.Errorf("movement %s: actual: %w", b.TrainID, err)
	}
	planned, plannedErr := Timestamp(b.PlannedTimestamp)

	rows, err := a.Pool.Query(ctx, `
		SELECT sl.seq, sl.tiploc, sl.wtt_arr, sl.wtt_dep, sl.wtt_pass, sv.run_date
		FROM services sv
		JOIN schedule_locations sl ON sl.schedule_id = sv.schedule_id
		JOIN locations l ON l.tiploc = sl.tiploc
		WHERE sv.id = $1 AND l.stanox = $2
		ORDER BY sl.seq`, id, b.LocSTANOX)
	if err != nil {
		return err
	}
	type cand struct {
		seq            int
		tiploc         string
		arr, dep, pass *int
		runDate        time.Time
	}
	var cands []cand
	for rows.Next() {
		var c cand
		if err := rows.Scan(&c.seq, &c.tiploc, &c.arr, &c.dep, &c.pass, &c.runDate); err != nil {
			return err
		}
		cands = append(cands, c)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(cands) == 0 {
		return nil
	}

	best, bestDiff := -1, 0
	var bestEvent string
	for i, c := range cands {
		event, wtt := "pass", c.pass
		if wtt == nil {
			if b.EventType == "ARRIVAL" {
				event, wtt = "arr", c.arr
			} else {
				event, wtt = "dep", c.dep
			}
		}
		if wtt == nil {
			continue
		}
		diff := 0
		if plannedErr == nil {
			diff = int(planned.Sub(ukrail.AtRunDate(c.runDate, *wtt)).Abs().Seconds())
			if diff > matchTolerance && len(cands) > 1 {
				continue
			}
		}
		if best < 0 || diff < bestDiff {
			best, bestDiff, bestEvent = i, diff, event
		}
	}
	if best < 0 {
		return nil
	}
	// TRUST is authoritative: it overwrites a time first seen in TD.
	_, err = a.Pool.Exec(ctx, `INSERT INTO service_events (service_id, seq, tiploc, event, actual, platform, source)
		VALUES ($1, $2, $3, $4, $5, $6, 'TRUST')
		ON CONFLICT (service_id, seq, event) DO UPDATE
			SET actual = EXCLUDED.actual, tiploc = EXCLUDED.tiploc, source = EXCLUDED.source,
			    platform = COALESCE(EXCLUDED.platform, service_events.platform)`,
		id, cands[best].seq, cands[best].tiploc, bestEvent, actual, nullIfBlank(b.Platform))
	return err
}

// Timestamp decodes a TRUST millisecond timestamp. Most TRUST timestamps
// (actual, planned, gbtt, canx, dep) encode UK local wall-clock time as if it
// were UTC, so the digits are reinterpreted in the London zone.
func Timestamp(ms string) (time.Time, error) {
	v, err := strconv.ParseInt(strings.TrimSpace(ms), 10, 64)
	if err != nil {
		return time.Time{}, err
	}
	t := time.UnixMilli(v).UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), 0, ukrail.London), nil
}

func nullIfBlank(s string) any {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	return s
}
