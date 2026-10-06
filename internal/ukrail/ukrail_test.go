package ukrail

import (
	"testing"
	"time"
)

func TestParseWTT(t *testing.T) {
	cases := []struct {
		in   string
		want int
		ok   bool
	}{
		{"0000", 0, true},
		{"0805", 8*3600 + 5*60, true},
		{"0805H", 8*3600 + 5*60 + 30, true},
		{"2359H", 23*3600 + 59*60 + 30, true},
		{"", 0, false},
		{"2460", 0, false},
		{"12", 0, false},
	}
	for _, c := range cases {
		got, ok := ParseWTT(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("ParseWTT(%q) = %d, %v; want %d, %v", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestAtRunDateAcrossDST(t *testing.T) {
	// BST ends at 02:00 on 25 October 2026; wall-clock times stay as written.
	run := time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC)
	got := AtRunDate(run, 86400+3*3600)
	if got.Format("2006-01-02 15:04 MST") != "2026-10-25 03:00 GMT" {
		t.Errorf("got %s", got.Format("2006-01-02 15:04 MST"))
	}
}

func TestDisplayName(t *testing.T) {
	for in, want := range map[string]string{
		"LONDON WATERLOO":        "London Waterloo",
		"LONDON WATERLOO (EAST)": "London Waterloo (East)",
		"STOKE-ON-TRENT":         "Stoke-On-Trent",
		"":                       "",
	} {
		if got := DisplayName(in); got != want {
			t.Errorf("DisplayName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestHHMMWrapsPastMidnight(t *testing.T) {
	if got := HHMM(86400 + 5*60); got != "0005" {
		t.Errorf("HHMM = %s", got)
	}
	if got := HHMMSS(8*3600 + 3*60 + 30); got != "080330" {
		t.Errorf("HHMMSS = %s", got)
	}
}
