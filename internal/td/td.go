package td

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/carbonarok/trackside/internal/ukrail"
)

// message is a C-class TD message. S-class (signalling state) messages are
// ignored.
type message struct {
	MsgType string `json:"msg_type"`
	AreaID  string `json:"area_id"`
	Time    string `json:"time"`
	From    string `json:"from"`
	To      string `json:"to"`
	Descr   string `json:"descr"`
}

// headcode matches train descriptions that can identify a service. TD also
// carries placeholders such as "****" and non-train descriptions.
var headcode = regexp.MustCompile(`^[0-9][A-Z][0-9]{2}$`)

// Processor applies TD messages.
type Processor struct {
	Pool *pgxpool.Pool
	// Map is the SMART data in use. It can be replaced with SetMap while
	// messages are being processed; until it is set, berth steps are ignored.
	Map *Map

	mapMu   sync.RWMutex
	current *Map
	// Now bounds which run dates are searched; tests override it.
	Now func() time.Time
}

// ApplyFrame decodes a feed body (a STOMP frame holding an array, or a single
// Kafka record) and applies its berth steps.
// SetMap replaces the SMART data in use.
func (p *Processor) SetMap(m *Map) {
	p.mapMu.Lock()
	p.current = m
	p.mapMu.Unlock()
}

func (p *Processor) smart() *Map {
	p.mapMu.RLock()
	defer p.mapMu.RUnlock()
	if p.current != nil {
		return p.current
	}
	return p.Map
}

func (p *Processor) ApplyFrame(ctx context.Context, body []byte) error {
	smart := p.smart()
	if smart == nil {
		return nil
	}
	var wrapped []map[string]message
	body = bytes.TrimSpace(body)
	if len(body) > 0 && body[0] == '{' {
		var one map[string]message
		if err := json.Unmarshal(body, &one); err != nil {
			return fmt.Errorf("decode td message: %w", err)
		}
		wrapped = append(wrapped, one)
	} else if err := json.Unmarshal(body, &wrapped); err != nil {
		return fmt.Errorf("decode td frame: %w", err)
	}
	for _, w := range wrapped {
		for kind, m := range w {
			var berths []Berth
			switch kind {
			case "CA_MSG":
				berths = smart.Step(m.AreaID, m.From, m.To)
			case "CB_MSG":
				berths = smart.Cancel(m.AreaID, m.From)
			case "CC_MSG":
				berths = smart.Interpose(m.AreaID, m.To)
			default:
				continue
			}
			var approaching []string
			if kind == "CA_MSG" || kind == "CC_MSG" {
				approaching = smart.Approaching(m.AreaID, m.To)
			}
			if (len(berths) == 0 && len(approaching) == 0) || !headcode.MatchString(m.Descr) {
				continue
			}
			at, err := msgTime(m.Time)
			if err != nil {
				continue
			}
			for _, stanox := range approaching {
				err := p.approach(ctx, m.Descr, m.AreaID, m.To, at, stanox)
				if err != nil && !errors.Is(err, errNoMatch) {
					slog.Warn("td approach failed", "area", m.AreaID, "descr", m.Descr, "err", err)
				}
			}
			berth := m.To
			if kind == "CB_MSG" {
				berth = m.From
			}
			for _, b := range berths {
				err := p.apply(ctx, m.Descr, m.AreaID, berth, at, b)
				if err != nil && !errors.Is(err, errNoMatch) {
					slog.Warn("td step failed", "area", m.AreaID, "descr", m.Descr, "err", err)
				}
			}
		}
	}
	return nil
}

// msgTime decodes a TD timestamp: milliseconds since the epoch, UTC.
func msgTime(ms string) (time.Time, error) {
	v, err := strconv.ParseInt(ms, 10, 64)
	if err != nil {
		return time.Time{}, err
	}
	return time.UnixMilli(v), nil
}

var errNoMatch = errors.New("no matching service")

// matchWindow is how far from its working time a train may be and still be
// matched to a berth step. Headcodes repeat across the day, so this has to
// be tight enough to tell trains apart.
const matchWindow = 60 * 60

func (p *Processor) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

// match finds the service carrying the headcode at the STANOX whose working
// time there is closest to at, along with the event (arr, dep or pass) the
// time corresponds to.
func (p *Processor) match(ctx context.Context, descr, stanox string, at time.Time, arrival bool) (*cand, string, error) {
	today := ukrail.DateOf(p.now())
	rows, err := p.Pool.Query(ctx, `
		SELECT sv.id, sv.run_date, sv.trust_id IS NOT NULL, sl.seq, sl.tiploc, sl.wtt_arr, sl.wtt_dep, sl.wtt_pass
		FROM schedules s
		JOIN services sv ON sv.schedule_id = s.id
		JOIN schedule_locations sl ON sl.schedule_id = s.id
		JOIN locations l ON l.tiploc = sl.tiploc
		WHERE s.signalling_id = $1 AND l.stanox = $2
		  AND sv.run_date BETWEEN $3::date - 1 AND $3::date
		  AND NOT sv.planned_cancel`, descr, stanox, today)
	if err != nil {
		return nil, "", err
	}
	cands, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (cand, error) {
		var c cand
		err := r.Scan(&c.id, &c.runDate, &c.activated, &c.seq, &c.tiploc, &c.arr, &c.dep, &c.pass)
		return c, err
	})
	if err != nil {
		return nil, "", err
	}

	var best *cand
	var bestEvent string
	bestScore := matchWindow + 1
	for i := range cands {
		c := &cands[i]
		event, wtt := "pass", c.pass
		if wtt == nil {
			event, wtt = "dep", c.dep
			if arrival || wtt == nil {
				event, wtt = "arr", c.arr
			}
		}
		if wtt == nil {
			continue
		}
		diff := int(at.Sub(ukrail.AtRunDate(c.runDate, *wtt)).Abs().Seconds())
		if diff > matchWindow {
			continue
		}
		// Prefer trains TRUST has activated: they are definitely running.
		score := diff
		if !c.activated {
			score += 30 * 60
		}
		if score < bestScore {
			best, bestScore, bestEvent = c, score, event
		}
	}
	if best == nil {
		return nil, "", errNoMatch
	}
	return best, bestEvent, nil
}

type cand struct {
	id             int64
	runDate        time.Time
	activated      bool
	seq            int
	tiploc         string
	arr, dep, pass *int
}

// apply attaches an event to the service carrying the headcode at the SMART
// location whose working time is closest to the event.
func (p *Processor) apply(ctx context.Context, descr, area, berth string, stepAt time.Time, b Berth) error {
	at := stepAt.Add(time.Duration(b.Offset) * time.Second)
	best, bestEvent, err := p.match(ctx, descr, b.STANOX, at, b.IsArrival())
	if err != nil {
		return err
	}
	batch := &pgx.Batch{}
	// TRUST and Darwin remain authoritative: a TD time never overwrites one.
	batch.Queue(`INSERT INTO service_events (service_id, seq, tiploc, event, actual, platform, source)
		VALUES ($1, $2, $3, $4, $5, $6, 'TD') ON CONFLICT (service_id, seq, event) DO NOTHING`,
		best.id, best.seq, best.tiploc, bestEvent, at, nullEmpty(b.Platform))
	batch.Queue(`UPDATE services SET td_area = $2, td_berth = $3, td_berth_at = $4 WHERE id = $1`,
		best.id, area, berth, stepAt)
	if bestEvent != "dep" {
		// The train has reached the location, so it is no longer approaching.
		batch.Queue(`UPDATE services SET td_approach_tiploc = NULL WHERE id = $1 AND td_approach_tiploc = $2`,
			best.id, best.tiploc)
	}
	return p.Pool.SendBatch(ctx, batch).Close()
}

// approach records that a train has entered the berth before a station.
func (p *Processor) approach(ctx context.Context, descr, area, berth string, stepAt time.Time, stanox string) error {
	best, event, err := p.match(ctx, descr, stanox, stepAt, true)
	if err != nil {
		return err
	}
	if event == "pass" {
		return nil // only calls are announced as approaching
	}
	_, err = p.Pool.Exec(ctx, `UPDATE services SET td_area = $2, td_berth = $3, td_berth_at = $4,
		td_approach_tiploc = $5, td_approach_at = $4 WHERE id = $1`,
		best.id, area, berth, stepAt, best.tiploc)
	return err
}

func nullEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
