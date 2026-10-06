package timetable

import (
	"math"
	"testing"
	"time"

	"github.com/carbonarok/trackside/internal/ukrail"
)

func f(v float64) *float64 { return &v }

// A train from A (0,0) to C (0,2) via B (0,1), heading north.
func positionService() *Service {
	return &Service{
		RunDate: time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC),
		Stops: []Stop{
			{Location: Location{TIPLOC: "A", Name: "A", Lat: f(50), Lon: f(0)}, WTTDep: secs(10, 0)},
			{Location: Location{TIPLOC: "J", Name: "Junction"}, WTTPass: secs(10, 5)},
			{Location: Location{TIPLOC: "B", Name: "B", Lat: f(51), Lon: f(0)}, WTTArr: secs(10, 10), WTTDep: secs(10, 12)},
			{Location: Location{TIPLOC: "C", Name: "C", Lat: f(52), Lon: f(0)}, WTTArr: secs(10, 30)},
		},
	}
}

func at(h, m int) time.Time { return time.Date(2026, 10, 6, h, m, 0, 0, ukrail.London) }

func TestPosition(t *testing.T) {
	s := positionService()
	for _, c := range []struct {
		name      string
		now       time.Time
		ok        bool
		lat       float64
		atStation bool
		next      string
	}{
		{"long before departure", at(9, 0), false, 0, false, ""},
		{"about to leave", at(9, 58), true, 50, true, "B"},
		{"halfway to B, ignoring the junction without coordinates", at(10, 5), true, 50.5, false, "B"},
		{"standing at B", at(10, 11), true, 51, true, "C"},
		{"halfway to C (leaves B at 10:12, due 10:30)", at(10, 21), true, 51.5, false, "C"},
		{"just arrived", at(10, 31), true, 52, true, ""},
		{"long gone", at(11, 0), false, 0, false, ""},
	} {
		p, ok := s.position(c.now)
		if ok != c.ok {
			t.Errorf("%s: ok = %v", c.name, ok)
			continue
		}
		if !ok {
			continue
		}
		if math.Abs(p.Lat-c.lat) > 1e-9 || p.AtStation != c.atStation {
			t.Errorf("%s: lat %v atStation %v", c.name, p.Lat, p.AtStation)
		}
		next := ""
		if p.Next != nil {
			next = p.Next.Location.TIPLOC
		}
		if next != c.next {
			t.Errorf("%s: next %q", c.name, next)
		}
		if !p.AtStation && math.Abs(p.Bearing) > 1e-6 && math.Abs(p.Bearing-360) > 1e-6 {
			t.Errorf("%s: bearing %v, want north", c.name, p.Bearing)
		}
	}
}

func TestPositionUsesLiveTimes(t *testing.T) {
	s := positionService()
	// Left A five minutes late: at 10:05 it has only just set off.
	late := at(10, 5)
	s.Stops[0].ActualDep = &late
	s.Stops[2].EstArr = ptrTime(at(10, 15))
	p, ok := s.position(at(10, 5))
	if !ok || math.Abs(p.Lat-50) > 1e-9 {
		t.Fatalf("position = %+v ok=%v", p, ok)
	}
	if p.DelayMinutes == nil || *p.DelayMinutes != 5 {
		t.Errorf("delay = %v", p.DelayMinutes)
	}
}

func ptrTime(t time.Time) *time.Time { return &t }
