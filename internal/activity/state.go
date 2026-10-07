// Package activity keeps iOS Live Activities for a journey leg up to date
// through APNs while the app is in the background.
//
// The app registers each activity's push token with the train and the leg's
// origin and destination. When the live hub reports that the train changed,
// the leg's state is rebuilt from the service detail the API serves and,
// if anything the activity shows has changed, pushed to the device.
package activity

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/carbonarok/trackside/internal/api"
	"github.com/carbonarok/trackside/internal/ukrail"
)

// Phases, in the order a leg goes through them.
const (
	PhaseUpcoming = "upcoming"
	PhaseBoarding = "boarding"
	PhaseOnTrain  = "onTrain"
	PhaseArrived  = "arrived"
)

var phaseRank = map[string]int{PhaseUpcoming: 0, PhaseBoarding: 1, PhaseOnTrain: 2, PhaseArrived: 3}

// boardingWindow is how long before departure a leg counts as boarding when
// the app hasn't said so.
const boardingWindow = 30 * time.Minute

// ContentState is LegActivityAttributes.ContentState in TrackSideIOS. Field
// names and types must match the Swift struct exactly, or the device drops
// the update.
type ContentState struct {
	Phase                    string     `json:"phase"`
	DeparturePlatform        *string    `json:"departurePlatform"`
	DeparturePlatformChanged bool       `json:"departurePlatformChanged"`
	ArrivalPlatform          *string    `json:"arrivalPlatform"`
	ArrivalPlatformChanged   bool       `json:"arrivalPlatformChanged"`
	ScheduledDeparture       *string    `json:"scheduledDeparture"`
	ExpectedDeparture        *string    `json:"expectedDeparture"`
	ScheduledArrival         *string    `json:"scheduledArrival"`
	ExpectedArrival          *string    `json:"expectedArrival"`
	DepartureDate            *AppleDate `json:"departureDate"`
	ArrivalDate              *AppleDate `json:"arrivalDate"`
	DepartureDelayMinutes    int        `json:"departureDelayMinutes"`
	ArrivalDelayMinutes      int        `json:"arrivalDelayMinutes"`
	IsCancelled              bool       `json:"isCancelled"`
	CancelReason             *string    `json:"cancelReason"`
	ConnectionMinutes        *int       `json:"connectionMinutes"`
	LastUpdated              AppleDate  `json:"lastUpdated"`
}

// sameAs reports whether two states show the same thing, ignoring when they
// were made.
func (s ContentState) sameAs(o ContentState) bool {
	s.LastUpdated, o.LastUpdated = AppleDate{}, AppleDate{}
	a, _ := json.Marshal(s)
	b, _ := json.Marshal(o)
	return string(a) == string(b)
}

// appleEpoch is 2001-01-01T00:00:00Z, the reference date of Foundation's Date.
const appleEpoch = 978307200

// AppleDate is a time encoded the way Swift's JSONDecoder decodes a Date by
// default (and ActivityKit decodes content-state): seconds since 1 January
// 2001. An ISO 8601 string here would fail to decode on the device.
type AppleDate struct{ time.Time }

func (d AppleDate) MarshalJSON() ([]byte, error) {
	return []byte(strconv.FormatInt(d.Unix()-appleEpoch, 10)), nil
}

func (d *AppleDate) UnmarshalJSON(b []byte) error {
	f, err := strconv.ParseFloat(string(b), 64)
	if err != nil {
		return fmt.Errorf("apple date: %w", err)
	}
	sec, frac := math.Modf(f)
	d.Time = time.Unix(int64(sec)+appleEpoch, int64(frac*1e9)).UTC()
	return nil
}

// Leg is what a registration says about the activity's journey leg.
type Leg struct {
	OriginCRS         string
	DestinationCRS    string
	ConnectionMinutes *int
	// Phase is the phase last sent (or the app's, at registration). Phases
	// never go backwards.
	Phase string
}

// Endpoints finds the leg's boarding and alighting stops in the service:
// the first call at the origin with a departure, and the last call after it
// at the destination with an arrival.
func Endpoints(d api.ServiceDetail, origin, destination string) (dep, arr *api.Stop, ok bool) {
	from := -1
	for i := range d.Stops {
		s := &d.Stops[i]
		if s.Departure != nil && s.Kind != "pass" && (s.CRS == origin || s.TIPLOC == origin) {
			from = i
			break
		}
	}
	if from < 0 {
		return nil, nil, false
	}
	for i := len(d.Stops) - 1; i > from; i-- {
		s := &d.Stops[i]
		if s.Arrival != nil && s.Kind != "pass" && (s.CRS == destination || s.TIPLOC == destination) {
			return &d.Stops[from], s, true
		}
	}
	return nil, nil, false
}

// State builds the leg's content-state at now.
func State(d api.ServiceDetail, leg Leg, now time.Time) (ContentState, bool) {
	from, to, ok := Endpoints(d, leg.OriginCRS, leg.DestinationCRS)
	if !ok {
		return ContentState{}, false
	}
	dep, arr := from.Departure, to.Arrival
	s := ContentState{
		DeparturePlatform:     platform(from),
		ArrivalPlatform:       platform(to),
		ScheduledDeparture:    clock(booked(dep)),
		ExpectedDeparture:     expected(dep),
		ScheduledArrival:      clock(booked(arr)),
		ExpectedArrival:       expected(arr),
		DepartureDate:         appleDate(best(dep)),
		ArrivalDate:           appleDate(best(arr)),
		DepartureDelayMinutes: lateness(dep),
		ArrivalDelayMinutes:   lateness(arr),
		IsCancelled: d.Status == "cancelled" || from.Cancelled || to.Cancelled ||
			dep.Cancelled || arr.Cancelled,
		ConnectionMinutes: leg.ConnectionMinutes,
		LastUpdated:       AppleDate{now.Truncate(time.Second)},
	}
	if from.Platform != nil {
		s.DeparturePlatformChanged = from.Platform.Changed
	}
	if to.Platform != nil {
		s.ArrivalPlatformChanged = to.Platform.Changed
	}
	if s.IsCancelled {
		switch {
		case d.CancelReasonText != "":
			s.CancelReason = &d.CancelReasonText
		case d.CancelReasonCodeDescription != "":
			s.CancelReason = &d.CancelReasonCodeDescription
		}
	}

	// Arrived and on the train are facts from the feeds. Before departure the
	// leg is boarding once the train is at or approaching the platform, or
	// departure is near; until then upcoming. Nothing ever goes backwards.
	switch {
	case arr.Actual != nil:
		s.Phase = PhaseArrived
	case dep.Actual != nil:
		s.Phase = PhaseOnTrain
	case from.AtPlatform || from.Approaching:
		s.Phase = PhaseBoarding
	case best(dep) != nil && best(dep).Sub(now) <= boardingWindow:
		s.Phase = PhaseBoarding
	default:
		s.Phase = PhaseUpcoming
	}
	if phaseRank[leg.Phase] > phaseRank[s.Phase] {
		s.Phase = leg.Phase
	}
	return s, true
}

func platform(s *api.Stop) *string {
	if s.Platform == nil {
		return nil
	}
	p := s.Platform.Actual
	if p == "" {
		p = s.Platform.Planned
	}
	if p == "" {
		return nil
	}
	return &p
}

func booked(t *api.Times) *time.Time {
	if t.Public != nil {
		return t.Public
	}
	return t.Working
}

func liveTime(t *api.Times) *time.Time {
	if t.Actual != nil {
		return t.Actual
	}
	return t.Estimated
}

// best is the time to count down to: what happened, else the forecast,
// else the timetable.
func best(t *api.Times) *time.Time {
	if l := liveTime(t); l != nil {
		return l
	}
	return booked(t)
}

func clock(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := t.In(ukrail.London).Format("15:04")
	return &s
}

// expected is the live time when it differs from the timetable's, as the
// app shows it only then.
func expected(t *api.Times) *string {
	l, b := clock(liveTime(t)), clock(booked(t))
	if l == nil || (b != nil && *l == *b) {
		return nil
	}
	return l
}

func lateness(t *api.Times) int {
	if t.DelayMinutes == nil || *t.DelayMinutes < 0 {
		return 0
	}
	return *t.DelayMinutes
}

func appleDate(t *time.Time) *AppleDate {
	if t == nil {
		return nil
	}
	return &AppleDate{*t}
}

// Alert is the aps alert shown on the lock screen for a change worth
// interrupting someone for.
type Alert struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}

// delayAlertRise and delayAlertFloor gate delay alerts: the delay must have
// grown by at least the rise and reached at least the floor.
const (
	delayAlertRise  = 3
	delayAlertFloor = 5
)

// AlertFor decides whether moving from prev to next deserves an alert:
// a cancellation, a platform change, or a delay that grew by 3 or more
// minutes to 5 or more. The first state sent never alerts.
func AlertFor(prev *ContentState, next ContentState, headcode string) *Alert {
	if prev == nil {
		return nil
	}
	name := headcode
	if name == "" {
		name = "Your train"
	}
	switch {
	case next.IsCancelled && !prev.IsCancelled:
		body := name + " has been cancelled."
		if next.CancelReason != nil {
			body += " " + *next.CancelReason
		}
		return &Alert{Title: "Train cancelled", Body: body}
	case next.Phase != PhaseOnTrain && next.Phase != PhaseArrived && changed(prev.DeparturePlatform, next.DeparturePlatform):
		return &Alert{Title: "Platform changed", Body: fmt.Sprintf("%s: Platform changed to %s", name, *next.DeparturePlatform)}
	case next.Phase != PhaseArrived && changed(prev.ArrivalPlatform, next.ArrivalPlatform):
		return &Alert{Title: "Arrival platform changed", Body: fmt.Sprintf("%s: Now arriving at platform %s", name, *next.ArrivalPlatform)}
	}
	// Before departure the departure delay matters; after it, the arrival's.
	was, now, at := prev.DepartureDelayMinutes, next.DepartureDelayMinutes, next.ExpectedDeparture
	verb := "departs"
	if next.Phase == PhaseOnTrain {
		was, now, at, verb = prev.ArrivalDelayMinutes, next.ArrivalDelayMinutes, next.ExpectedArrival, "arrives"
	}
	if next.Phase != PhaseArrived && now-was >= delayAlertRise && now >= delayAlertFloor {
		body := fmt.Sprintf("%s is now %d min late", name, now)
		if at != nil {
			body += fmt.Sprintf(" and %s at %s", verb, *at)
		}
		return &Alert{Title: "Running late", Body: body + "."}
	}
	return nil
}

// changed reports a platform moving from one known value to another.
func changed(was, now *string) bool {
	return was != nil && now != nil && *was != *now
}
