package td

import (
	"os"
	"testing"
)

func TestSMARTLookups(t *testing.T) {
	f, err := os.Open("../../testdata/smart.json")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	berths, err := ParseSMART(f)
	if err != nil {
		t.Fatal(err)
	}
	if len(berths) != 5 {
		t.Fatalf("berths = %d, want 5 (the record without a STANOX is dropped)", len(berths))
	}
	m := NewMap(berths)

	if got := m.Step("WI", "0101", "0103"); len(got) != 1 || got[0].Offset != 30 || got[0].IsArrival() {
		t.Errorf("between step = %+v", got)
	}
	if got := m.Step("WI", "0201", "0203"); len(got) != 1 || got[0].Offset != -15 || !got[0].IsArrival() {
		t.Errorf("negative offset = %+v", got)
	}
	if got := m.Step("WI", "0205", "9999"); len(got) != 1 || got[0].Platform != "5" {
		t.Errorf("'from' step to any berth = %+v", got)
	}
	if got := m.Step("WI", "0101", "0999"); len(got) != 0 {
		t.Errorf("unmapped step matched %+v", got)
	}
	if got := m.Cancel("WI", "0207"); len(got) != 1 {
		t.Errorf("clearout = %+v", got)
	}
	if got := m.Step("WI", "0207", "0300"); len(got) != 0 {
		t.Errorf("clearout record matched a step: %+v", got)
	}
	if got := m.Interpose("WI", "0209"); len(got) != 1 {
		t.Errorf("interpose = %+v", got)
	}
}
