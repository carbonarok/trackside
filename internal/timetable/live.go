package timetable

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/carbonarok/trackside/internal/ukrail"
)

// LiveSource supplies live data on demand, one station or service at a time.
// Darwin Lite (National Rail's OpenLDBWS) is the implementation; it gives
// Darwin forecasts to instances without the Darwin Push Port stream.
type LiveSource interface {
	// Board returns live data for trains at a station. from and to bound the
	// times wanted; a source may return less, or nil when out of range.
	Board(ctx context.Context, crs string, from, to time.Time) (*LiveBoard, error)
	// Service returns live data along a service's route, if the source has
	// seen it on a board; nil when it hasn't.
	Service(ctx context.Context, uid string, runDate time.Time) (*LiveService, error)
	// Bind records that a board entry is this timetable service, so its
	// route can be fetched later.
	Bind(serviceID, uid string, runDate time.Time)
	// Messages returns station messages seen on the station's latest board.
	Messages(crs string) []Message
}

// LiveBoard is a station's live departures and arrivals.
type LiveBoard struct {
	Services []LiveBoardService
}

// LiveBoardService is one train on a live board. Times are "HH:MM";
// estimates may instead be "On time", "Delayed", "Cancelled" or "No report".
type LiveBoardService struct {
	ServiceID      string
	STA, ETA       string
	STD, ETD       string
	Platform       string
	OperatorCode   string
	OriginCRS      []string
	DestinationCRS []string
	Cancelled      bool
	CancelReason   string
	DelayReason    string
}

// LiveService is live data along a service's route.
type LiveService struct {
	Calls        []LiveCall
	CancelReason string
	DelayReason  string
}

// LiveCall is a public calling point. ST is the scheduled time; AT (actual)
// or ET (estimate) is set depending on whether the train has been there.
type LiveCall struct {
	CRS       string
	ST        string
	ET, AT    string
	Cancelled bool
}

// enrichBoard merges a live board into board entries that the Darwin stream
// hasn't already covered.
func (st *Store) enrichBoard(ctx context.Context, q BoardQuery, entries []BoardEntry) {
	lb, err := st.Live.Board(ctx, q.CRS, q.From, q.To)
	if err != nil {
		slog.Warn("live board unavailable", "crs", q.CRS, "err", err)
		return
	}
	if lb == nil {
		return
	}
	used := make([]bool, len(lb.Services))
	for _, e := range entries {
		p := &e.Service.Stops[e.Index]
		if p.Darwin != nil {
			continue
		}
		i := matchLive(e.Service, e.Index, lb.Services, used)
		if i < 0 {
			continue
		}
		used[i] = true
		ls := &lb.Services[i]
		p.Darwin = liveForecast(e.Service, p, ls)
		if e.Service.LateReasonText == "" {
			e.Service.LateReasonText = ls.DelayReason
		}
		if e.Service.CancelReasonText == "" {
			e.Service.CancelReasonText = ls.CancelReason
		}
		if ls.ServiceID != "" {
			st.Live.Bind(ls.ServiceID, e.Service.UID, e.Service.RunDate)
		}
	}
}

// matchLive finds the live board entry for a timetable service. Live boards
// carry no train UID, so the booked public time must agree, and destination,
// origin and operator break ties. Returns -1 when nothing fits well enough.
func matchLive(svc *Service, idx int, live []LiveBoardService, used []bool) int {
	p := &svc.Stops[idx]
	std, sta := hhmm(p.GBTTDep), hhmm(p.GBTTArr)
	origin := svc.Stops[0].Location.CRS
	dest := svc.Stops[len(svc.Stops)-1].Location.CRS
	best, bestScore := -1, 0
	for i := range live {
		if used[i] {
			continue
		}
		l := &live[i]
		if !(std != "" && l.STD == std) && !(sta != "" && l.STA == sta) {
			continue
		}
		score := 1
		if dest != "" && contains(l.DestinationCRS, dest) {
			score += 2
		}
		if origin != "" && contains(l.OriginCRS, origin) {
			score++
		}
		if svc.ATOCCode != "" && l.OperatorCode == svc.ATOCCode {
			score++
		}
		if score > bestScore {
			best, bestScore = i, score
		}
	}
	// The time alone is too weak: several trains often share a minute.
	if bestScore < 2 {
		return -1
	}
	return best
}

// liveForecast converts a live board entry into the same form as Darwin
// stream forecasts, so the usual precedence rules apply.
func liveForecast(svc *Service, p *Stop, ls *LiveBoardService) *DarwinForecast {
	d := &DarwinForecast{Platform: ls.Platform}
	if ls.Cancelled {
		d.ArrCancelled, d.DepCancelled = true, true
		return d
	}
	if p.GBTTArr != nil || p.WTTArr != nil {
		d.ArrET, d.ArrDelayed, d.ArrCancelled = liveTime(svc, firstNonNil(p.GBTTArr, p.WTTArr), ls.ETA)
	}
	if p.GBTTDep != nil || p.WTTDep != nil {
		d.DepET, d.DepDelayed, d.DepCancelled = liveTime(svc, firstNonNil(p.GBTTDep, p.WTTDep), ls.ETD)
	}
	return d
}

// liveTime interprets a live estimate against the scheduled time.
func liveTime(svc *Service, scheduled *int, v string) (t *time.Time, delayed, cancelled bool) {
	switch strings.TrimSpace(v) {
	case "", "No report":
		return nil, false, false
	case "On time":
		at := ukrail.AtRunDate(svc.RunDate, *scheduled)
		return &at, false, false
	case "Delayed":
		return nil, true, false
	case "Cancelled":
		return nil, false, true
	}
	secs, ok := ukrail.ParseWTT(strings.ReplaceAll(v, ":", ""))
	if !ok {
		return nil, false, false
	}
	at := ukrail.AtRunDate(svc.RunDate, nearest(secs, *scheduled))
	return &at, false, false
}

// enrichService merges live data along the route into a service.
func (st *Store) enrichService(ctx context.Context, svc *Service) {
	ls, err := st.Live.Service(ctx, svc.UID, svc.RunDate)
	if err != nil {
		slog.Warn("live service unavailable", "uid", svc.UID, "err", err)
		return
	}
	if ls == nil {
		return
	}
	if svc.LateReasonText == "" {
		svc.LateReasonText = ls.DelayReason
	}
	if svc.CancelReasonText == "" {
		svc.CancelReasonText = ls.CancelReason
	}
	for _, c := range ls.Calls {
		for i := range svc.Stops {
			p := &svc.Stops[i]
			if p.Location.CRS != c.CRS || p.Darwin != nil {
				continue
			}
			var sched *int
			dep := true
			switch {
			case p.GBTTDep != nil && hhmm(p.GBTTDep) == c.ST:
				sched = p.GBTTDep
			case p.GBTTArr != nil && hhmm(p.GBTTArr) == c.ST:
				sched, dep = p.GBTTArr, false
			default:
				continue
			}
			d := &DarwinForecast{}
			if c.Cancelled {
				d.ArrCancelled, d.DepCancelled = true, true
			} else if c.AT != "" {
				at, _, _ := liveTime(svc, sched, c.AT)
				if dep {
					d.DepAT = at
				} else {
					d.ArrAT = at
				}
			} else {
				et, delayed, cancelled := liveTime(svc, sched, c.ET)
				if dep {
					d.DepET, d.DepDelayed, d.DepCancelled = et, delayed, cancelled
				} else {
					d.ArrET, d.ArrDelayed, d.ArrCancelled = et, delayed, cancelled
				}
			}
			p.Darwin = d
			break
		}
	}
}

func hhmm(secs *int) string {
	if secs == nil {
		return ""
	}
	s := *secs % 86400
	return time.Date(0, 1, 1, s/3600, s%3600/60, 0, 0, time.UTC).Format("15:04")
}

// nearest places a time of day within 12 hours of a reference measured in
// seconds after midnight of the run date (which may exceed 86400).
func nearest(tod, ref int) int {
	v := ref - ref%86400 + tod
	switch {
	case v-ref > 12*3600:
		v -= 86400
	case ref-v > 12*3600:
		v += 86400
	}
	return v
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func firstNonNil(vs ...*int) *int {
	for _, v := range vs {
		if v != nil {
			return v
		}
	}
	return nil
}
