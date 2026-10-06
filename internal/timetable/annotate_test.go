package timetable

import (
	"testing"
	"time"
)

func secs(h, m int) *int { v := h*3600 + m*60; return &v }

func approachService(approachAt time.Time) *Service {
	return &Service{
		RunDate:          time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC),
		TDApproachTIPLOC: "WIMBLDN",
		TDApproachAt:     &approachAt,
		Stops: []Stop{
			{Location: Location{TIPLOC: "WATRLMN"}, WTTDep: secs(8, 0)},
			{Location: Location{TIPLOC: "WIMBLDN"}, WTTArr: secs(8, 12), WTTDep: secs(8, 13)},
			{Location: Location{TIPLOC: "WOKING"}, WTTArr: secs(8, 35)},
		},
	}
}

func TestApproachExpires(t *testing.T) {
	at := time.Date(2026, 10, 6, 8, 10, 0, 0, time.UTC)

	s := approachService(at)
	s.applyApproach(at.Add(5 * time.Minute))
	if !s.Stops[1].Approaching {
		t.Error("fresh approach not shown")
	}

	s = approachService(at)
	s.applyApproach(at.Add(approachExpiry + time.Minute))
	if s.Stops[1].Approaching {
		t.Error("stale approach still shown")
	}

	s = approachService(at)
	arrived := at.Add(2 * time.Minute)
	s.Stops[1].ActualArr = &arrived
	s.applyApproach(at.Add(3 * time.Minute))
	if s.Stops[1].Approaching {
		t.Error("approach shown after arrival")
	}
}
