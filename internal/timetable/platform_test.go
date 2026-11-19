package timetable

import "testing"

func TestPlatformChanged(t *testing.T) {
	for _, c := range []struct {
		booked, live string
		want         bool
	}{
		{"12", "12", false},
		{"12", "12B", false}, // a section of the same platform
		{"12A", "12", false},
		{"12", "13", true},
		{"1", "12", true},
		{"12A", "12B", true},
		{"", "3", false},
		{"3", "", false},
	} {
		if got := PlatformChanged(c.booked, c.live); got != c.want {
			t.Errorf("PlatformChanged(%q, %q) = %v, want %v", c.booked, c.live, got, c.want)
		}
	}
}

// At Paddington, Darwin had the train's platform hours ahead but kept it
// off the station's screens, showed it once announced, then hid it again
// when a screen lost the train. The plan never changed, so the platform
// shouldn't either: it was showing the booked one whenever it was hidden.
func TestUnannouncedPlatformStands(t *testing.T) {
	stop := func(suppressed bool) Stop {
		s := Service{Stops: []Stop{{
			Location: Location{TIPLOC: "PADTON"},
			Platform: "9",
			Darwin:   &DarwinForecast{Platform: "1", PlatformSuppressed: suppressed, PlatformConfirmed: true},
		}}}
		s.applyDarwinActuals()
		return s.Stops[0]
	}

	for _, suppressed := range []bool{true, false, true} {
		p := stop(suppressed)
		if p.ActualPlatform != "1" {
			t.Errorf("suppressed=%v: platform %q, want Darwin's 1 rather than the booked 9", suppressed, p.ActualPlatform)
		}
		if p.PlatformSuppressed != suppressed {
			t.Errorf("suppressed=%v: flagged %v", suppressed, p.PlatformSuppressed)
		}
		// Confirmed means on the screens, which a hidden platform isn't.
		if p.PlatformConfirmed == suppressed {
			t.Errorf("suppressed=%v: confirmed %v", suppressed, p.PlatformConfirmed)
		}
	}
}
