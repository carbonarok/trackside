package darwin

import (
	"os"
	"testing"
)

func decodeFile(t *testing.T, name string) *Pport {
	t.Helper()
	b, err := os.ReadFile("../../testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	p, err := DecodeRecord(b)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestDecodeJSONRecord(t *testing.T) {
	p := decodeFile(t, "darwin_kafka_json.json")
	if p.Version != "18.0" || p.UR == nil || len(p.UR.TrainStatus) != 1 {
		t.Fatalf("pport = %+v", p)
	}
	ts := p.UR.TrainStatus[0]
	if ts.UID != "W19999" || ts.LateReason.Int() != 104 || ts.LateReason.TIPLOC != "CLPHMJN" || !ts.LateReason.Near {
		t.Errorf("TS = %+v reason=%+v", ts, ts.LateReason)
	}
	if len(ts.Locations) != 2 {
		t.Fatalf("locations = %d", len(ts.Locations))
	}
	clj := ts.Locations[0]
	if clj.Dep == nil || clj.Dep.ET != "09:03" || clj.Plat == nil || clj.Plat.Number != "12" || !clj.Plat.Conf {
		t.Errorf("CLJ = %+v plat=%+v", clj, clj.Plat)
	}
	if ts.Locations[1].Arr.Delayed {
		t.Errorf(`"false" decoded as true`)
	}
}

func TestDecodeBase64AndXMLRecords(t *testing.T) {
	p := decodeFile(t, "darwin_kafka_base64.json")
	if len(p.UR.TrainStatus) != 1 || len(p.UR.TrainStatus[0].Locations) != 4 {
		t.Fatalf("base64 record = %+v", p.UR)
	}
	wat := p.UR.TrainStatus[0].Locations[0]
	if !wat.Plat.Suppressed() || wat.Dep.AT != "08:33" {
		t.Errorf("WAT = %+v %+v", wat.Plat, wat.Dep)
	}
	if bare := decodeFile(t, "darwin_ts.xml"); bare.UR == nil || len(bare.UR.TrainStatus) != 1 {
		t.Errorf("bare XML = %+v", bare)
	}
}

func TestDecodeSchedule(t *testing.T) {
	p := decodeFile(t, "darwin_schedule.xml")
	s := p.UR.Schedules[0]
	if s.CancelReason.Int() != 100 || len(s.Locations) != 4 || !s.Locations[0].Can || s.Locations[0].XMLName.Local != "OR" {
		t.Errorf("schedule = %+v", s)
	}
}

func TestHeartbeatHasNoUpdates(t *testing.T) {
	p := decodeFile(t, "darwin_kafka_heartbeat.json")
	if p.UR != nil || p.SR != nil {
		t.Errorf("heartbeat = %+v", p)
	}
}

func TestResolveAcrossMidnight(t *testing.T) {
	cases := []struct{ tod, ref, want int }{
		{8 * 3600, 8*3600 + 60, 8 * 3600},          // same day
		{60, 86400 - 120, 86400 + 60},              // forecast just after midnight
		{86400 - 60, 86400 + 120, 86400 - 60},      // actual just before midnight, scheduled after
		{2 * 3600, 86400 + 2*3600, 86400 + 2*3600}, // whole stop is on the next day
	}
	for _, c := range cases {
		if got := resolve(c.tod, c.ref); got != c.want {
			t.Errorf("resolve(%d, %d) = %d, want %d", c.tod, c.ref, got, c.want)
		}
	}
}
