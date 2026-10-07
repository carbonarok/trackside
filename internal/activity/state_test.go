package activity

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/carbonarok/trackside/internal/api"
	"github.com/carbonarok/trackside/internal/ukrail"
)

func at(hhmm string) *time.Time {
	t, err := time.ParseInLocation("2006-01-02 15:04", "2026-10-07 "+hhmm, ukrail.London)
	if err != nil {
		panic(err)
	}
	return &t
}

func intp(n int) *int { return &n }

// service is a Woking (WOK) to Reading (RDG) train calling at Guildford (GLD)
// with every time on schedule and nothing reported yet.
func service() api.ServiceDetail {
	return api.ServiceDetail{
		ServiceSummary: api.ServiceSummary{UID: "W12345", RunDate: "2026-10-07", Headcode: "2C27", Status: "activated"},
		Stops: []api.Stop{
			{Location: api.Location{TIPLOC: "WOKING", CRS: "WOK"}, Kind: "origin",
				Departure: &api.Times{Public: at("11:58"), Working: at("11:58")},
				Platform:  &api.Platform{Planned: "2"}},
			{Location: api.Location{TIPLOC: "WOKINGJ", CRS: ""}, Kind: "pass",
				Pass: &api.Times{Working: at("12:00")}},
			{Location: api.Location{TIPLOC: "GUILDFD", CRS: "GLD"}, Kind: "call",
				Arrival:   &api.Times{Public: at("12:05")},
				Departure: &api.Times{Public: at("12:06")}},
			{Location: api.Location{TIPLOC: "RDNGSTN", CRS: "RDG"}, Kind: "destination",
				Arrival:  &api.Times{Public: at("12:14"), Working: at("12:14")},
				Platform: &api.Platform{Planned: "4"}},
		},
	}
}

var leg = Leg{OriginCRS: "WOK", DestinationCRS: "RDG"}

func TestStateOnTime(t *testing.T) {
	s, ok := State(service(), leg, *at("11:00"))
	if !ok {
		t.Fatal("leg not found")
	}
	if s.Phase != PhaseUpcoming {
		t.Errorf("phase %s, want upcoming an hour out", s.Phase)
	}
	if *s.ScheduledDeparture != "11:58" || *s.ScheduledArrival != "12:14" {
		t.Errorf("scheduled %s / %s", *s.ScheduledDeparture, *s.ScheduledArrival)
	}
	if s.ExpectedDeparture != nil || s.ExpectedArrival != nil {
		t.Error("expected times should be nil when on time")
	}
	if *s.DeparturePlatform != "2" || *s.ArrivalPlatform != "4" || s.DeparturePlatformChanged {
		t.Errorf("platforms %s / %s", *s.DeparturePlatform, *s.ArrivalPlatform)
	}
	if !s.DepartureDate.Equal(*at("11:58")) || s.IsCancelled || s.CancelReason != nil {
		t.Errorf("state %+v", s)
	}
}

func TestDatesEncodeAsSecondsSince2001(t *testing.T) {
	s, _ := State(service(), leg, *at("11:00"))
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	json.Unmarshal(b, &raw)
	// 2026-10-07 11:58 BST is 10:58 UTC.
	want := float64(time.Date(2026, 10, 7, 10, 58, 0, 0, time.UTC).Unix() - 978307200)
	if raw["departureDate"] != want {
		t.Errorf("departureDate = %v, want %v (a number, as Swift's JSONDecoder expects)", raw["departureDate"], want)
	}
	if _, ok := raw["lastUpdated"].(float64); !ok {
		t.Errorf("lastUpdated = %v, want a number", raw["lastUpdated"])
	}
	// Every Swift field is present, nulls included, with no extras.
	fields := []string{"phase", "departurePlatform", "departurePlatformChanged", "arrivalPlatform",
		"arrivalPlatformChanged", "scheduledDeparture", "expectedDeparture", "scheduledArrival",
		"expectedArrival", "departureDate", "arrivalDate", "departureDelayMinutes", "arrivalDelayMinutes",
		"isCancelled", "cancelReason", "connectionMinutes", "lastUpdated"}
	for _, f := range fields {
		if _, ok := raw[f]; !ok {
			t.Errorf("content-state is missing %s", f)
		}
	}
	if len(raw) != len(fields) {
		t.Errorf("content-state has %d fields, want %d", len(raw), len(fields))
	}
	// And it round-trips for the stored last state.
	var back ContentState
	if err := json.Unmarshal(b, &back); err != nil || !back.sameAs(s) {
		t.Errorf("round trip: %v", err)
	}
}

func TestStateDelayedWithPlatformChange(t *testing.T) {
	d := service()
	d.Stops[0].Departure.Estimated = at("12:06")
	d.Stops[0].Departure.DelayMinutes = intp(8)
	d.Stops[0].Platform = &api.Platform{Planned: "2", Actual: "5", Changed: true, Confirmed: true}
	d.Stops[3].Arrival.Estimated = at("12:20")
	d.Stops[3].Arrival.DelayMinutes = intp(6)
	s, _ := State(d, leg, *at("11:50"))
	if s.Phase != PhaseBoarding {
		t.Errorf("phase %s, want boarding within 30 minutes of departure", s.Phase)
	}
	if *s.ExpectedDeparture != "12:06" || *s.ExpectedArrival != "12:20" {
		t.Errorf("expected %v / %v", s.ExpectedDeparture, s.ExpectedArrival)
	}
	if s.DepartureDelayMinutes != 8 || s.ArrivalDelayMinutes != 6 {
		t.Errorf("delays %d / %d", s.DepartureDelayMinutes, s.ArrivalDelayMinutes)
	}
	if *s.DeparturePlatform != "5" || !s.DeparturePlatformChanged {
		t.Errorf("departure platform %s changed=%v", *s.DeparturePlatform, s.DeparturePlatformChanged)
	}
	if !s.DepartureDate.Equal(*at("12:06")) {
		t.Errorf("departure date %v, want the estimate to count down to", s.DepartureDate)
	}
}

func TestPhasesFollowTheFeedsAndNeverGoBack(t *testing.T) {
	d := service()
	d.Stops[0].AtPlatform = true
	if s, _ := State(d, leg, *at("10:00")); s.Phase != PhaseBoarding {
		t.Errorf("at platform: %s", s.Phase)
	}
	d.Stops[0].Departure.Actual = at("11:59")
	if s, _ := State(d, leg, *at("12:00")); s.Phase != PhaseOnTrain {
		t.Errorf("departed: %s", s.Phase)
	}
	d.Stops[3].Arrival.Actual = at("12:15")
	if s, _ := State(d, leg, *at("12:16")); s.Phase != PhaseArrived {
		t.Errorf("arrived: %s", s.Phase)
	}
	// The app said boarding; an hour out the server would say upcoming.
	if s, _ := State(service(), Leg{OriginCRS: "WOK", DestinationCRS: "RDG", Phase: PhaseBoarding}, *at("10:00")); s.Phase != PhaseBoarding {
		t.Errorf("phase went back to %s", s.Phase)
	}
}

func TestLegInTheMiddleOfTheRoute(t *testing.T) {
	s, ok := State(service(), Leg{OriginCRS: "GLD", DestinationCRS: "RDG"}, *at("11:00"))
	if !ok || *s.ScheduledDeparture != "12:06" || s.DeparturePlatform != nil {
		t.Errorf("Guildford leg: ok=%v %+v", ok, s)
	}
	if _, ok := State(service(), Leg{OriginCRS: "RDG", DestinationCRS: "WOK"}, *at("11:00")); ok {
		t.Error("found a leg running backwards")
	}
	if _, ok := State(service(), Leg{OriginCRS: "WOK", DestinationCRS: "PAD"}, *at("11:00")); ok {
		t.Error("found a leg to a station the train doesn't call at")
	}
}

func TestCancellation(t *testing.T) {
	d := service()
	d.Status = "cancelled"
	d.CancelReasonText = "This train has been cancelled because of a fault on this train"
	d.Stops[0].Cancelled = true
	s, _ := State(d, leg, *at("11:00"))
	if !s.IsCancelled || s.CancelReason == nil || !strings.Contains(*s.CancelReason, "fault") {
		t.Errorf("cancelled state %+v", s)
	}
}

func TestAlerts(t *testing.T) {
	base, _ := State(service(), leg, *at("11:40"))
	with := func(f func(*ContentState)) ContentState {
		s := base
		f(&s)
		return s
	}
	str := func(s string) *string { return &s }

	cases := []struct {
		name  string
		prev  *ContentState
		next  ContentState
		title string // empty: no alert
	}{
		{"first state", nil, base, ""},
		{"nothing changed", &base, base, ""},
		{"departure platform", &base, with(func(s *ContentState) { s.DeparturePlatform = str("5") }), "Platform changed"},
		{"arrival platform", &base, with(func(s *ContentState) { s.ArrivalPlatform = str("7") }), "Arrival platform changed"},
		{"platform first announced", ptr(with(func(s *ContentState) { s.DeparturePlatform = nil })), base, ""},
		{"cancelled", &base, with(func(s *ContentState) { s.IsCancelled = true }), "Train cancelled"},
		{"delay grew 0 to 6", &base, with(func(s *ContentState) { s.DepartureDelayMinutes = 6; s.ExpectedDeparture = str("12:04") }), "Running late"},
		{"delay grew 0 to 4: under the floor", &base, with(func(s *ContentState) { s.DepartureDelayMinutes = 4 }), ""},
		{"delay grew 5 to 7: under the rise", ptr(with(func(s *ContentState) { s.DepartureDelayMinutes = 5 })), with(func(s *ContentState) { s.DepartureDelayMinutes = 7 }), ""},
		{"delay fell", ptr(with(func(s *ContentState) { s.DepartureDelayMinutes = 10 })), with(func(s *ContentState) { s.DepartureDelayMinutes = 2 }), ""},
		{"on the train, arrival delay grew", ptr(with(func(s *ContentState) { s.Phase = PhaseOnTrain; s.DepartureDelayMinutes = 9 })),
			with(func(s *ContentState) { s.Phase = PhaseOnTrain; s.DepartureDelayMinutes = 9; s.ArrivalDelayMinutes = 9 }), "Running late"},
		{"on the train, departure platform no longer matters", ptr(with(func(s *ContentState) { s.Phase = PhaseOnTrain })),
			with(func(s *ContentState) { s.Phase = PhaseOnTrain; s.DeparturePlatform = str("9") }), ""},
	}
	for _, c := range cases {
		a := AlertFor(c.prev, c.next, "2C27")
		switch {
		case c.title == "" && a != nil:
			t.Errorf("%s: unexpected alert %+v", c.name, a)
		case c.title != "" && (a == nil || a.Title != c.title):
			t.Errorf("%s: alert %+v, want %q", c.name, a, c.title)
		}
	}
	if a := AlertFor(&base, with(func(s *ContentState) { s.DeparturePlatform = str("5") }), "2C27"); a.Body != "2C27: Platform changed to 5" {
		t.Errorf("platform body %q", a.Body)
	}
}

func ptr(s ContentState) *ContentState { return &s }

func TestConnection(t *testing.T) {
	// An earlier train into Woking at 11:50 makes an 8-minute connection
	// onto the 11:58 to Reading.
	prev := api.ServiceDetail{Stops: []api.Stop{
		{Location: api.Location{CRS: "WAT"}, Kind: "origin", Departure: &api.Times{Public: at("11:15")}},
		{Location: api.Location{CRS: "WOK"}, Kind: "destination", Arrival: &api.Times{Public: at("11:50")}},
	}}
	if c := Connection(prev, "WOK", service(), "WOK"); c == nil || *c != 8 {
		t.Fatalf("connection %v, want 8", c)
	}
	// The earlier train runs 5 late: 3 minutes left.
	prev.Stops[1].Arrival.Estimated = at("11:55")
	if c := Connection(prev, "WOK", service(), "WOK"); *c != 3 {
		t.Errorf("late arrival: %d, want 3", *c)
	}
	// And the onward train 4 late: 7 again. Live times on both sides count.
	d := service()
	d.Stops[0].Departure.Estimated = at("12:02")
	if c := Connection(prev, "WOK", d, "WOK"); *c != 7 {
		t.Errorf("both late: %d, want 7", *c)
	}
	// Badly late: negative means the connection will be missed.
	prev.Stops[1].Arrival.Estimated = at("12:10")
	if c := Connection(prev, "WOK", d, "WOK"); *c != -8 {
		t.Errorf("missed: %d, want -8", *c)
	}
	if Connection(prev, "GLD", service(), "WOK") != nil || Connection(prev, "WOK", service(), "PAD") != nil {
		t.Error("found a connection at a station one of the trains doesn't call at")
	}
}
