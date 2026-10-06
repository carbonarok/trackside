package schedule

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/carbonarok/trackside/internal/ukrail"
)

// VSTP messages carry short-notice schedules. They use the same model as the
// SCHEDULE feed but a different layout: times are "HHMMSS", blank fields are
// spaces, and the segment is an array.

type rawVSTP struct {
	Msg struct {
		Schedule struct {
			TransactionType string `json:"transaction_type"`
			StartDate       string `json:"schedule_start_date"`
			EndDate         string `json:"schedule_end_date"`
			DaysRuns        string `json:"schedule_days_runs"`
			BankHoliday     string `json:"CIF_bank_holiday_running"`
			TrainStatus     string `json:"train_status"`
			TrainUID        string `json:"CIF_train_uid"`
			STP             string `json:"CIF_stp_indicator"`
			Segments        []struct {
				SignallingID string `json:"signalling_id"`
				ATOCCode     string `json:"atoc_code"`
				Category     string `json:"CIF_train_category"`
				ServiceCode  string `json:"CIF_train_service_code"`
				PowerType    string `json:"CIF_power_type"`
				Speed        string `json:"CIF_speed"`
				TrainClass   string `json:"CIF_train_class"`
				Locations    []struct {
					Arrival         string `json:"scheduled_arrival_time"`
					Departure       string `json:"scheduled_departure_time"`
					Pass            string `json:"scheduled_pass_time"`
					PublicArrival   string `json:"public_arrival_time"`
					PublicDeparture string `json:"public_departure_time"`
					Platform        string `json:"CIF_platform"`
					Line            string `json:"CIF_line"`
					Path            string `json:"CIF_path"`
					Location        struct {
						TIPLOC struct {
							ID string `json:"tiploc_id"`
						} `json:"tiploc"`
					} `json:"location"`
				} `json:"schedule_location"`
			} `json:"schedule_segment"`
		} `json:"schedule"`
	} `json:"VSTPCIFMsgV1"`
}

// ParseVSTP decodes one VSTP message into a schedule (for a Create) or a key
// (for a Delete).
func ParseVSTP(body []byte) (*Record, error) {
	var raw rawVSTP
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, err
	}
	r := raw.Msg.Schedule
	uid := strings.TrimSpace(r.TrainUID)
	stp := strings.TrimSpace(r.STP)
	start, err := time.Parse(time.DateOnly, r.StartDate)
	if err != nil {
		return nil, fmt.Errorf("vstp %s: start date: %w", uid, err)
	}
	if r.TransactionType == "Delete" {
		return &Record{Delete: &Key{TrainUID: uid, StartDate: start, STP: stp, Source: "V"}}, nil
	}
	end, err := time.Parse(time.DateOnly, r.EndDate)
	if err != nil {
		return nil, fmt.Errorf("vstp %s: end date: %w", uid, err)
	}
	s := &Schedule{
		TrainUID:           uid,
		StartDate:          start,
		EndDate:            end,
		DaysRuns:           r.DaysRuns,
		STP:                stp,
		Source:             "V",
		BankHolidayRunning: strings.TrimSpace(r.BankHoliday),
		TrainStatus:        strings.TrimSpace(r.TrainStatus),
	}
	if len(r.Segments) > 0 {
		seg := r.Segments[0]
		s.SignallingID = strings.TrimSpace(seg.SignallingID)
		s.ATOCCode = strings.TrimSpace(seg.ATOCCode)
		s.Category = strings.TrimSpace(seg.Category)
		s.ServiceCode = strings.TrimSpace(seg.ServiceCode)
		s.PowerType = strings.TrimSpace(seg.PowerType)
		s.TrainClass = strings.TrimSpace(seg.TrainClass)
		s.Speed = atoiPtr(strings.TrimSpace(seg.Speed))
		locs := make([]rawLoc, 0, len(seg.Locations))
		for i, l := range seg.Locations {
			typ := "LI"
			switch i {
			case 0:
				typ = "LO"
			case len(seg.Locations) - 1:
				typ = "LT"
			}
			locs = append(locs, rawLoc{
				Type:     typ,
				TIPLOC:   l.Location.TIPLOC.ID,
				Arr:      vstpTime(l.Arrival),
				Dep:      vstpTime(l.Departure),
				Pass:     vstpTime(l.Pass),
				PubArr:   vstpPublic(l.PublicArrival),
				PubDep:   vstpPublic(l.PublicDeparture),
				Platform: strings.TrimSpace(l.Platform),
				Line:     strings.TrimSpace(l.Line),
				Path:     strings.TrimSpace(l.Path),
			})
		}
		s.Locations = buildLocations(locs)
	}
	return &Record{Schedule: s}, nil
}

func vstpTime(s string) *int {
	if v, ok := ukrail.ParseVSTPTime(s); ok {
		return &v
	}
	return nil
}

func vstpPublic(s string) *int {
	s = strings.TrimSpace(s)
	if s == "" || strings.Trim(s, "0") == "" {
		return nil
	}
	return vstpTime(s)
}
