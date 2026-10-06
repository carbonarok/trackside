package schedule

import (
	"errors"
	"io"
	"os"
	"testing"
)

func readAll(t *testing.T, path string) []*Record {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	rd := NewReader(f)
	var out []*Record
	for {
		rec, err := rd.Next()
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, rec)
	}
}

func findSchedule(recs []*Record, uid, stp string) *Schedule {
	for _, r := range recs {
		if r.Schedule != nil && r.Schedule.TrainUID == uid && r.Schedule.STP == stp {
			return r.Schedule
		}
	}
	return nil
}

func TestReaderFullFile(t *testing.T) {
	recs := readAll(t, "../../testdata/schedule_full.json")
	if recs[0].Header == nil || recs[0].Header.Type != "full" || recs[0].Header.Sequence != 100 {
		t.Fatalf("header = %+v", recs[0].Header)
	}
	var tiplocs, schedules int
	for _, r := range recs {
		if r.TIPLOC != nil {
			tiplocs++
		}
		if r.Schedule != nil {
			schedules++
		}
	}
	if tiplocs != 5 || schedules != 6 {
		t.Fatalf("tiplocs=%d schedules=%d, want 5 and 6", tiplocs, schedules)
	}

	s := findSchedule(recs, "W10001", "P")
	if s == nil {
		t.Fatal("W10001 missing")
	}
	if s.SignallingID != "1A01" || s.ATOCCode != "SW" || s.DaysRuns != "1111100" || *s.Speed != 100 {
		t.Errorf("header fields = %+v", s)
	}
	if len(s.Locations) != 5 {
		t.Fatalf("locations = %d", len(s.Locations))
	}
	pass := s.Locations[1]
	if pass.WTTPass == nil || *pass.WTTPass != 8*3600+3*60+30 {
		t.Errorf("half-minute pass time = %v", pass.WTTPass)
	}
	if pass.GBTTArr != nil || pass.GBTTDep != nil {
		t.Errorf("public time 0000 should mean no public time, got %v %v", pass.GBTTArr, pass.GBTTDep)
	}
	if clj := s.Locations[2]; *clj.GBTTArr != 8*3600+6*60 || clj.Platform != "7" {
		t.Errorf("CLJ = %+v", clj)
	}
}

func TestMidnightRollover(t *testing.T) {
	recs := readAll(t, "../../testdata/schedule_full.json")
	s := findSchedule(recs, "W10002", "P")
	wim, wok := s.Locations[2], s.Locations[3]
	if *wim.WTTArr != 86400+2*60 || *wim.GBTTDep != 86400+3*60 {
		t.Errorf("WIM times not rolled past midnight: arr=%d dep=%d", *wim.WTTArr, *wim.GBTTDep)
	}
	if *wok.GBTTArr != 86400+25*60 {
		t.Errorf("WOK = %d", *wok.GBTTArr)
	}
}

func TestCancellationAndDelete(t *testing.T) {
	recs := readAll(t, "../../testdata/schedule_full.json")
	if c := findSchedule(recs, "W10003", "C"); c == nil || len(c.Locations) != 0 {
		t.Fatalf("STP cancellation = %+v", c)
	}
	upd := readAll(t, "../../testdata/schedule_update.json")
	if upd[1].Delete == nil || upd[1].Delete.TrainUID != "W10004" || upd[1].Delete.STP != "O" {
		t.Fatalf("delete = %+v", upd[1])
	}
}

const vstpMessage = `{"VSTPCIFMsgV1":{"schemaLocation":"x","classification":"industry","timestamp":"1790000000000",
"owner":"Network Rail","originMsgId":"x","Sender":{},"schedule":{"schedule_id":"","transaction_type":"Create",
"schedule_start_date":"2026-10-06","schedule_end_date":"2026-10-06","schedule_days_runs":"0100000",
"applicable_timetable":"N","CIF_bank_holiday_running":" ","train_status":"1","CIF_train_uid":" 12345",
"CIF_stp_indicator":"N","schedule_segment":[{"signalling_id":"5A01","uic_code":"","atoc_code":"SW",
"CIF_train_category":"OO","CIF_headcode":"","CIF_course_indicator":"","CIF_train_service_code":"24671004",
"CIF_business_sector":"","CIF_power_type":"EMU","CIF_timing_load":"","CIF_speed":"075","CIF_operating_characteristics":"",
"CIF_train_class":"","CIF_sleepers":"","CIF_reservations":"","CIF_connection_indicator":"","CIF_catering_code":"",
"CIF_service_branding":"","CIF_traction_class":"","schedule_location":[
{"scheduled_arrival_time":" ","scheduled_departure_time":"233000","scheduled_pass_time":" ","public_arrival_time":"","public_departure_time":"233000","CIF_platform":"3","CIF_line":"","CIF_path":"","CIF_activity":"TB","location":{"tiploc":{"tiploc_id":"WATRLMN"}}},
{"scheduled_arrival_time":" ","scheduled_departure_time":" ","scheduled_pass_time":"235930","public_arrival_time":"00","public_departure_time":"00","CIF_platform":"","CIF_line":"","CIF_path":"","CIF_activity":"","location":{"tiploc":{"tiploc_id":"VAUXHLM"}}},
{"scheduled_arrival_time":"000500","scheduled_departure_time":" ","scheduled_pass_time":" ","public_arrival_time":"000500","public_departure_time":"","CIF_platform":"","CIF_line":"","CIF_path":"","CIF_activity":"TF","location":{"tiploc":{"tiploc_id":"CLPHMJN"}}}
]}]}}}`

func TestParseVSTP(t *testing.T) {
	rec, err := ParseVSTP([]byte(vstpMessage))
	if err != nil {
		t.Fatal(err)
	}
	s := rec.Schedule
	if s == nil || s.TrainUID != "12345" || s.Source != "V" || s.STP != "N" || *s.Speed != 75 {
		t.Fatalf("schedule = %+v", s)
	}
	if len(s.Locations) != 3 || s.Locations[0].Type != "LO" || s.Locations[2].Type != "LT" {
		t.Fatalf("locations = %+v", s.Locations)
	}
	if s.Locations[1].WTTPass == nil || *s.Locations[1].WTTPass != 23*3600+59*60+30 {
		t.Errorf("pass = %v", s.Locations[1].WTTPass)
	}
	if s.Locations[1].GBTTArr != nil {
		t.Errorf("public time \"00\" should be absent")
	}
	if got := *s.Locations[2].GBTTArr; got != 86400+5*60 {
		t.Errorf("arrival after midnight = %d", got)
	}
}
