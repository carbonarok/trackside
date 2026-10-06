package trust

import "testing"

func TestDelayCodes(t *testing.T) {
	if n := len(delayCodes); n < 250 {
		t.Fatalf("only %d delay codes loaded", n)
	}
	for code, want := range map[string]string{
		"IA": "Signal failure (including no fault found)",
		"TG": "Driver",
		"OD": "Delays due to National/Regional/Route Operations directives or Route Control decision or directive",
	} {
		got, ok := LookupDelayCode(code)
		if !ok || got.Cause != want {
			t.Errorf("%s = %+v, want cause %q", code, got, want)
		}
	}
	for code, c := range delayCodes {
		if len(code) != 2 || c.Cause == "" || c.Abbreviation == "" || len(c.Abbreviation) > 10 {
			t.Errorf("malformed code %q: %+v", code, c)
		}
	}
}
