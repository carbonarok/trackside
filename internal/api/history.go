package api

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/carbonarok/trackside/internal/history"
	"github.com/carbonarok/trackside/internal/timetable"
	"github.com/carbonarok/trackside/internal/ukrail"
)

func (s *Server) registerHistory(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/delay-repay", s.delayRepay)
	mux.HandleFunc("GET /v1/history/services/{uid}", s.serviceHistory)
	mux.HandleFunc("GET /v1/stats/locations/{code}", s.stationStats)
}

// TrainRef identifies a train on a day.
type TrainRef struct {
	UID      string    `json:"uid"`
	RunDate  string    `json:"runDate"`
	Headcode string    `json:"headcode,omitempty"`
	Operator *Operator `json:"operator,omitempty"`
}

func trainRef(c history.Call) TrainRef {
	t := TrainRef{UID: c.UID, RunDate: c.RunDate.Format(time.DateOnly), Headcode: c.Headcode}
	if c.ATOC != "" && c.ATOC != "ZZ" {
		t.Operator = &Operator{Code: c.ATOC, Name: ukrail.OperatorName(c.ATOC)}
	}
	return t
}

// DelayRepay is the result of checking a journey for Delay Repay.
type DelayRepay struct {
	From            Location  `json:"from"`
	To              Location  `json:"to"`
	BookedDeparture time.Time `json:"bookedDeparture"`
	BookedArrival   time.Time `json:"bookedArrival"`
	Train           TrainRef  `json:"train"`
	Cancelled       bool      `json:"cancelled"`
	CancelReason    string    `json:"cancelReason,omitempty"`
	LateReason      string    `json:"lateReason,omitempty"`
	// UsedTrain is the train that got the passenger there instead, when the
	// booked one was cancelled.
	UsedTrain       *TrainRef  `json:"usedTrain,omitempty"`
	ActualDeparture *time.Time `json:"actualDeparture,omitempty"`
	ArrivedAt       *time.Time `json:"arrivedAt,omitempty"`
	// ArrivalSource says which feed reported the arrival: TRUST, TD or
	// Darwin.
	ArrivalSource string `json:"arrivalSource,omitempty"`
	DelayMinutes  *int   `json:"delayMinutes,omitempty"`
	Eligible      bool   `json:"eligible"`
	// Band is the Delay Repay 15 band: 15-29, 30-59, 60-119 or 120+.
	Band         string        `json:"band,omitempty"`
	Compensation *Compensation `json:"compensation,omitempty"`
	Note         string        `json:"note"`
}

// Compensation is the standard Delay Repay 15 refund as a percentage of the
// ticket price.
type Compensation struct {
	SinglePercent int `json:"singlePercent"`
	ReturnPercent int `json:"returnPercent"`
}

func (s *Server) delayRepay(w http.ResponseWriter, r *http.Request) {
	if s.History == nil {
		writeError(w, http.StatusNotImplemented, "history is not enabled")
		return
	}
	q := r.URL.Query()
	from, ok := s.lookup(w, r.Context(), q.Get("from"))
	if !ok {
		return
	}
	to, ok := s.lookup(w, r.Context(), q.Get("to"))
	if !ok {
		return
	}
	dep, err := time.ParseInLocation("2006-01-02 15:04", q.Get("date")+" "+q.Get("departure"), ukrail.London)
	if err != nil {
		writeError(w, http.StatusBadRequest, "date (YYYY-MM-DD) and departure (HH:MM, the booked time) are required")
		return
	}
	claim, err := s.History.Repay(r.Context(), tiplocs(from), tiplocs(to), dep)
	if errors.Is(err, history.ErrNoTrain) {
		writeError(w, http.StatusNotFound, "no train booked to leave "+from[0].Name+" at "+q.Get("departure")+
			" calling at "+to[0].Name+" that day")
		return
	}
	if err != nil {
		serverError(w, err)
		return
	}
	b := claim.Booked
	out := DelayRepay{
		From:            toLocation(from[0]),
		To:              toLocation(to[0]),
		BookedDeparture: b.From.BookedDep().In(ukrail.London),
		BookedArrival:   b.To.BookedArr().In(ukrail.London),
		Train:           trainRef(b.From),
		Cancelled:       claim.Cancelled,
		CancelReason:    firstNonEmpty(b.From.CancelText, b.To.CancelText),
		LateReason:      b.To.LateText,
		DelayMinutes:    claim.DelayMinutes,
		Band:            claim.Band,
		Eligible:        claim.Band != "",
	}
	if claim.Used != nil {
		out.ActualDeparture = localPtr(claim.Used.From.ActualDep)
		out.ArrivedAt = localPtr(claim.ArrivedAt)
		out.ArrivalSource = claim.Used.To.ArrSource
		if claim.Cancelled {
			t := trainRef(claim.Used.From)
			out.UsedTrain = &t
		}
	}
	if out.Eligible {
		single, ret := history.Compensation(claim.Band)
		out.Compensation = &Compensation{SinglePercent: single, ReturnPercent: ret}
		out.Note = "Eligible under Delay Repay 15. Most operators use these percentages, but check your " +
			"operator's scheme and claim within 28 days."
	}
	switch {
	case claim.Used == nil:
		out.Note = "The booked train was cancelled and no later train has a reported arrival yet."
	case claim.ArrivedAt == nil:
		out.Note = "No arrival has been reported for this train yet."
	case !out.Eligible:
		out.Note = "Less than 15 minutes late, so not eligible for Delay Repay."
	}
	writeJSON(w, out)
}

// HistoricCall is a station call as it actually happened.
type HistoricCall struct {
	TIPLOC          string     `json:"tiploc"`
	CRS             string     `json:"crs,omitempty"`
	BookedArrival   *time.Time `json:"bookedArrival,omitempty"`
	BookedDeparture *time.Time `json:"bookedDeparture,omitempty"`
	ActualArrival   *time.Time `json:"actualArrival,omitempty"`
	ActualDeparture *time.Time `json:"actualDeparture,omitempty"`
	DelayMinutes    *int       `json:"delayMinutes,omitempty"`
	Cancelled       bool       `json:"cancelled"`
}

func historicCall(c history.Call, arrival bool) HistoricCall {
	h := HistoricCall{TIPLOC: c.TIPLOC, CRS: c.CRS,
		BookedArrival: localPtr(c.BookedArr()), BookedDeparture: localPtr(c.BookedDep()),
		ActualArrival: localPtr(c.ActualArr), ActualDeparture: localPtr(c.ActualDep),
		Cancelled: c.ArrCancelled || c.DepCancelled}
	booked, actual := c.BookedDep(), c.ActualDep
	if arrival {
		booked, actual = c.BookedArr(), c.ActualArr
	}
	if booked != nil && actual != nil {
		m := int(actual.Sub(*booked).Round(time.Minute).Minutes())
		h.DelayMinutes = &m
	}
	return h
}

// ServiceRun is one day of a train's running.
type ServiceRun struct {
	Train       TrainRef     `json:"train"`
	Origin      HistoricCall `json:"origin"`
	Destination HistoricCall `json:"destination"`
	Cancelled   bool         `json:"cancelled"`
}

// ServiceHistory is a train's recent record.
type ServiceHistory struct {
	UID  string       `json:"uid"`
	Days int          `json:"days"`
	Runs []ServiceRun `json:"runs"`
}

func (s *Server) serviceHistory(w http.ResponseWriter, r *http.Request) {
	if s.History == nil {
		writeError(w, http.StatusNotImplemented, "history is not enabled")
		return
	}
	days, ok := daysParam(w, r, 30, 400)
	if !ok {
		return
	}
	runs, err := s.History.Train(r.Context(), r.PathValue("uid"), days)
	if err != nil {
		serverError(w, err)
		return
	}
	out := ServiceHistory{UID: r.PathValue("uid"), Days: days, Runs: []ServiceRun{}}
	for _, run := range runs {
		out.Runs = append(out.Runs, ServiceRun{
			Train:       trainRef(run.Origin),
			Origin:      historicCall(run.Origin, false),
			Destination: historicCall(run.Destination, true),
			Cancelled:   run.Cancelled,
		})
	}
	writeJSON(w, out)
}

// Punctuality summarises arrivals or departures at a station.
type Punctuality struct {
	Booked    int `json:"booked"`
	Reported  int `json:"reported"`
	Cancelled int `json:"cancelled"`
	// OnTime is within 59 seconds of the booked time, the industry measure.
	OnTime              int     `json:"onTime"`
	Within5Minutes      int     `json:"within5Minutes"`
	Within15Minutes     int     `json:"within15Minutes"`
	AverageDelayMinutes float64 `json:"averageDelayMinutes"`
}

// LateTrain is a train that was often late arriving.
type LateTrain struct {
	UID                 string    `json:"uid"`
	Headcode            string    `json:"headcode,omitempty"`
	Operator            *Operator `json:"operator,omitempty"`
	BookedArrival       string    `json:"bookedArrival"`
	Runs                int       `json:"runs"`
	AverageDelayMinutes float64   `json:"averageDelayMinutes"`
}

// StationStats is a station's punctuality over a period.
type StationStats struct {
	Location   Location    `json:"location"`
	From       string      `json:"from"`
	To         string      `json:"to"`
	Arrivals   Punctuality `json:"arrivals"`
	Departures Punctuality `json:"departures"`
	MostLate   []LateTrain `json:"mostLate"`
}

func (s *Server) stationStats(w http.ResponseWriter, r *http.Request) {
	if s.History == nil {
		writeError(w, http.StatusNotImplemented, "history is not enabled")
		return
	}
	locs, ok := s.lookup(w, r.Context(), r.PathValue("code"))
	if !ok {
		return
	}
	days, ok := daysParam(w, r, 7, 400)
	if !ok {
		return
	}
	st, err := s.History.Station(r.Context(), tiplocs(locs), days)
	if err != nil {
		serverError(w, err)
		return
	}
	out := StationStats{
		Location:   toLocation(locs[0]),
		From:       st.From.Format(time.DateOnly),
		To:         st.To.Format(time.DateOnly),
		Arrivals:   punctuality(st.Arrivals),
		Departures: punctuality(st.Departures),
		MostLate:   []LateTrain{},
	}
	for _, t := range st.MostLate {
		lt := LateTrain{UID: t.UID, Headcode: t.Headcode, BookedArrival: t.BookedTime, Runs: t.Runs,
			AverageDelayMinutes: t.AverageDelay}
		if t.ATOC != "" && t.ATOC != "ZZ" {
			lt.Operator = &Operator{Code: t.ATOC, Name: ukrail.OperatorName(t.ATOC)}
		}
		out.MostLate = append(out.MostLate, lt)
	}
	writeJSON(w, out)
}

func punctuality(p history.Punctuality) Punctuality {
	return Punctuality{Booked: p.Booked, Reported: p.Reported, Cancelled: p.Cancelled, OnTime: p.OnTime,
		Within5Minutes: p.Within5, Within15Minutes: p.Within15, AverageDelayMinutes: p.AverageDelay}
}

func daysParam(w http.ResponseWriter, r *http.Request, def, max int) (int, bool) {
	v := r.URL.Query().Get("days")
	if v == "" {
		return def, true
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 || n > max {
		writeError(w, http.StatusBadRequest, "days must be 1-"+strconv.Itoa(max))
		return 0, false
	}
	return n, true
}

func localPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	l := t.In(ukrail.London)
	return &l
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

func tiplocs(locs []timetable.Location) []string {
	out := make([]string, 0, len(locs))
	for _, l := range locs {
		out = append(out, l.TIPLOC)
	}
	return out
}
