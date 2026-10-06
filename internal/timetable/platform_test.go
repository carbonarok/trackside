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
