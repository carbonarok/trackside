// Package api serves trackside's native JSON API.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/carbonarok/trackside/internal/timetable"
	"github.com/carbonarok/trackside/internal/trust"
	"github.com/carbonarok/trackside/internal/ukrail"
)

// Server holds the API's dependencies.
type Server struct {
	Store *timetable.Store
	// Now is the clock used for default time windows; tests override it.
	Now func() time.Time
}

// Register adds the native API routes to mux.
func (s *Server) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/locations", s.searchLocations)
	mux.HandleFunc("GET /v1/locations/{code}", s.location)
	mux.HandleFunc("GET /v1/locations/{code}/departures", s.board(false))
	mux.HandleFunc("GET /v1/locations/{code}/arrivals", s.board(true))
	mux.HandleFunc("GET /v1/locations/{code}/messages", s.messages)
	mux.HandleFunc("GET /v1/services/{uid}/{date}", s.service)
}

func (s *Server) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Location is a timetable location in API responses.
type Location struct {
	TIPLOC string `json:"tiploc"`
	CRS    string `json:"crs,omitempty"`
	Name   string `json:"name"`
}

func toLocation(l timetable.Location) Location {
	return Location{TIPLOC: l.TIPLOC, CRS: l.CRS, Name: l.Name}
}

func (s *Server) searchLocations(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if len(q) < 2 {
		writeError(w, http.StatusBadRequest, "q must be at least 2 characters")
		return
	}
	locs, err := s.Store.Search(r.Context(), q, 20)
	if err != nil {
		serverError(w, err)
		return
	}
	out := make([]Location, 0, len(locs))
	for _, l := range locs {
		out = append(out, toLocation(l))
	}
	writeJSON(w, map[string]any{"locations": out})
}

func (s *Server) location(w http.ResponseWriter, r *http.Request) {
	locs, ok := s.lookup(w, r.Context(), r.PathValue("code"))
	if !ok {
		return
	}
	tiplocs := make([]string, 0, len(locs))
	for _, l := range locs {
		tiplocs = append(tiplocs, l.TIPLOC)
	}
	writeJSON(w, map[string]any{"location": toLocation(locs[0]), "tiplocs": tiplocs})
}

func (s *Server) lookup(w http.ResponseWriter, ctx context.Context, code string) ([]timetable.Location, bool) {
	locs, err := s.Store.Lookup(ctx, code)
	if errors.Is(err, timetable.ErrNotFound) {
		writeError(w, http.StatusNotFound, "unknown location "+code)
		return nil, false
	}
	if err != nil {
		serverError(w, err)
		return nil, false
	}
	return locs, true
}

// Board is a departures or arrivals response.
type Board struct {
	Location Location       `json:"location"`
	From     time.Time      `json:"from"`
	To       time.Time      `json:"to"`
	Messages []Message      `json:"messages"`
	Services []BoardService `json:"services"`
}

// Message is a Darwin station message, as shown on station screens.
type Message struct {
	ID       int    `json:"id"`
	Category string `json:"category"`
	// Severity runs from 0 (information) to 3 (severe).
	Severity  int       `json:"severity"`
	Text      string    `json:"text"`
	HTML      string    `json:"html"`
	UpdatedAt time.Time `json:"updatedAt"`
}

func (s *Server) stationMessages(ctx context.Context, crs string) ([]Message, error) {
	msgs, err := s.Store.Messages(ctx, crs)
	if err != nil {
		return nil, err
	}
	out := make([]Message, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, Message{ID: m.ID, Category: m.Category, Severity: m.Severity,
			Text: m.Text, HTML: m.HTML, UpdatedAt: m.UpdatedAt.In(ukrail.London)})
	}
	return out, nil
}

func (s *Server) messages(w http.ResponseWriter, r *http.Request) {
	locs, ok := s.lookup(w, r.Context(), r.PathValue("code"))
	if !ok {
		return
	}
	msgs, err := s.stationMessages(r.Context(), locs[0].CRS)
	if err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, map[string]any{"location": toLocation(locs[0]), "messages": msgs})
}

// BoardService is one train on a board.
type BoardService struct {
	ServiceSummary
	Stop Stop `json:"stop"`
}

// ServiceSummary identifies a service.
type ServiceSummary struct {
	UID          string     `json:"uid"`
	RunDate      string     `json:"runDate"`
	Headcode     string     `json:"headcode,omitempty"`
	Operator     *Operator  `json:"operator,omitempty"`
	IsPassenger  bool       `json:"isPassenger"`
	Status       string     `json:"status"`
	Origin       []Endpoint `json:"origin"`
	Destination  []Endpoint `json:"destination"`
	CancelReason string     `json:"cancelReasonCode,omitempty"`
	// CancelReasonCodeDescription explains cancelReasonCode, a TRUST delay
	// attribution code, in industry terms.
	CancelReasonCodeDescription string `json:"cancelReasonCodeDescription,omitempty"`
	// Darwin's passenger-facing explanations.
	CancelReasonText string `json:"cancelReason,omitempty"`
	LateReasonText   string `json:"lateReason,omitempty"`
	PlannedCancel    bool   `json:"plannedCancel"`
}

// Operator is a train operating company.
type Operator struct {
	Code string `json:"code"`
	Name string `json:"name"`
}

// Endpoint is a service's origin or destination.
type Endpoint struct {
	Location
	Time *time.Time `json:"time,omitempty"`
}

// Stop is a location on a service's route.
type Stop struct {
	Location
	Kind           string    `json:"kind"` // origin, call, pass, destination, or the non-public "stop"
	Arrival        *Times    `json:"arrival,omitempty"`
	Departure      *Times    `json:"departure,omitempty"`
	Pass           *Times    `json:"pass,omitempty"`
	Platform       *Platform `json:"platform,omitempty"`
	Cancelled      bool      `json:"cancelled"`
	StartsHere     bool      `json:"startsHere,omitempty"`
	AtPlatform     bool      `json:"atPlatform,omitempty"`
	TerminatesHere bool      `json:"terminatesHere,omitempty"`
	Line           string    `json:"line,omitempty"`
	Path           string    `json:"path,omitempty"`
}

// Times are the planned and live times for one event at a stop.
type Times struct {
	Public    *time.Time `json:"public,omitempty"`
	Working   *time.Time `json:"working,omitempty"`
	Actual    *time.Time `json:"actual,omitempty"`
	Estimated *time.Time `json:"estimated,omitempty"`
	// DelayMinutes compares the actual or estimated time with the public time
	// (or the working time where there is no public time).
	DelayMinutes *int `json:"delayMinutes,omitempty"`
	// Delayed means late by an amount not yet known.
	Delayed   bool `json:"delayed,omitempty"`
	Cancelled bool `json:"cancelled,omitempty"`
}

// Platform is the booked and live platform. It is omitted entirely while
// Darwin has the platform suppressed.
type Platform struct {
	Planned   string `json:"planned,omitempty"`
	Actual    string `json:"actual,omitempty"`
	Changed   bool   `json:"changed"`
	Confirmed bool   `json:"confirmed"`
}

func (s *Server) board(arrivals bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		locs, ok := s.lookup(w, r.Context(), r.PathValue("code"))
		if !ok {
			return
		}
		q := r.URL.Query()
		from := s.now()
		if v := q.Get("at"); v != "" {
			t, err := parseLocal(v)
			if err != nil {
				writeError(w, http.StatusBadRequest, "at must be RFC 3339 or YYYY-MM-DDTHH:MM")
				return
			}
			from = t
		}
		window := 120
		if v := q.Get("window"); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 1 || n > 1440 {
				writeError(w, http.StatusBadRequest, "window must be 1-1440 minutes")
				return
			}
			window = n
		}
		bq := timetable.BoardQuery{
			From:     from,
			To:       from.Add(time.Duration(window) * time.Minute),
			Arrivals: arrivals,
			Passes:   q.Get("passes") == "true",
		}
		for _, l := range locs {
			bq.TIPLOCs = append(bq.TIPLOCs, l.TIPLOC)
		}
		filterKey := "to"
		if arrivals {
			filterKey = "from"
		}
		if code := q.Get(filterKey); code != "" {
			other, ok := s.lookup(w, r.Context(), code)
			if !ok {
				return
			}
			for _, l := range other {
				bq.Calling = append(bq.Calling, l.TIPLOC)
			}
		}
		entries, err := s.Store.Board(r.Context(), bq)
		if err != nil {
			serverError(w, err)
			return
		}
		msgs, err := s.stationMessages(r.Context(), locs[0].CRS)
		if err != nil {
			serverError(w, err)
			return
		}
		out := Board{
			Messages: msgs,
			Location: toLocation(locs[0]),
			From:     bq.From.In(ukrail.London),
			To:       bq.To.In(ukrail.London),
			Services: make([]BoardService, 0, len(entries)),
		}
		for _, e := range entries {
			out.Services = append(out.Services, BoardService{
				ServiceSummary: summary(e.Service),
				Stop:           stop(e.Service, e.Index),
			})
		}
		writeJSON(w, out)
	}
}

// ServiceDetail is a service with its full route.
type ServiceDetail struct {
	ServiceSummary
	TrainClass string    `json:"trainClass,omitempty"`
	PowerType  string    `json:"powerType,omitempty"`
	Category   string    `json:"category,omitempty"`
	Source     string    `json:"source"`
	Position   *Position `json:"position,omitempty"`
	// Associations are only included in service detail.
	Associations []Association `json:"associations,omitempty"`
	Stops        []Stop        `json:"stops"`
}

// Position is the last Train Describer berth the train occupied.
type Position struct {
	Area  string    `json:"tdArea"`
	Berth string    `json:"berth"`
	At    time.Time `json:"at"`
}

func (s *Server) service(w http.ResponseWriter, r *http.Request) {
	date, err := time.Parse(time.DateOnly, r.PathValue("date"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "date must be YYYY-MM-DD")
		return
	}
	svc, err := s.Store.Service(r.Context(), r.PathValue("uid"), date)
	if errors.Is(err, timetable.ErrNotFound) {
		writeError(w, http.StatusNotFound, "service not found")
		return
	}
	if err != nil {
		serverError(w, err)
		return
	}
	assocs, err := s.Store.Associations(r.Context(), svc)
	if err != nil {
		serverError(w, err)
		return
	}
	d := detail(svc)
	for _, a := range assocs {
		out := Association{
			Type:      a.Type,
			Category:  a.Category,
			Location:  toLocation(a.Location),
			Cancelled: a.Cancelled,
		}
		if a.Other != nil {
			sum := summary(a.Other)
			out.Service = &sum
		} else {
			out.Service = &ServiceSummary{UID: a.OtherUID, RunDate: a.OtherRunDate.Format(time.DateOnly),
				Origin: []Endpoint{}, Destination: []Endpoint{}}
		}
		d.Associations = append(d.Associations, out)
	}
	writeJSON(w, d)
}

// Association is a join, divide or next working, from this service's point
// of view.
type Association struct {
	// Type is divides, divided_from, joined_by, joins, forms, formed_from or
	// linked.
	Type      string          `json:"type"`
	Category  string          `json:"category"`
	Location  Location        `json:"location"`
	Cancelled bool            `json:"cancelled"`
	Service   *ServiceSummary `json:"service"`
}

func detail(svc *timetable.Service) ServiceDetail {
	source := "timetable"
	if svc.Source == "V" {
		source = "vstp"
	}
	d := ServiceDetail{
		ServiceSummary: summary(svc),
		TrainClass:     svc.TrainClass,
		PowerType:      svc.PowerType,
		Category:       svc.Category,
		Source:         source,
		Stops:          make([]Stop, 0, len(svc.Stops)),
	}
	for i := range svc.Stops {
		d.Stops = append(d.Stops, stop(svc, i))
	}
	if svc.TDBerthAt != nil {
		d.Position = &Position{Area: svc.TDArea, Berth: svc.TDBerth, At: svc.TDBerthAt.In(ukrail.London)}
	}
	return d
}

func summary(svc *timetable.Service) ServiceSummary {
	sum := ServiceSummary{
		UID:              svc.UID,
		RunDate:          svc.RunDate.Format(time.DateOnly),
		Headcode:         svc.Headcode,
		IsPassenger:      ukrail.IsPassenger(svc.TrainStatus, svc.Category),
		Status:           string(svc.Status()),
		CancelReason:     svc.CancelReason,
		CancelReasonText: svc.CancelReasonText,
		LateReasonText:   svc.LateReasonText,
		PlannedCancel:    svc.PlannedCancel,
		Origin:           []Endpoint{},
		Destination:      []Endpoint{},
	}
	if c, ok := trust.LookupDelayCode(svc.CancelReason); ok {
		sum.CancelReasonCodeDescription = c.Cause
	}
	if svc.ATOCCode != "" && svc.ATOCCode != "ZZ" {
		name := svc.OperatorName
		if name == "" {
			name = ukrail.OperatorName(svc.ATOCCode)
		}
		sum.Operator = &Operator{Code: svc.ATOCCode, Name: name}
	}
	if n := len(svc.Stops); n > 0 {
		first, last := svc.Stops[0], svc.Stops[n-1]
		sum.Origin = append(sum.Origin, Endpoint{toLocation(first.Location), svc.At(first.WTTDep)})
		sum.Destination = append(sum.Destination, Endpoint{toLocation(last.Location), svc.At(last.WTTArr)})
		if first.GBTTDep != nil {
			sum.Origin[0].Time = svc.At(first.GBTTDep)
		}
		if last.GBTTArr != nil {
			sum.Destination[0].Time = svc.At(last.GBTTArr)
		}
	}
	return sum
}

func stop(svc *timetable.Service, i int) Stop {
	p := &svc.Stops[i]
	out := Stop{
		Location:       toLocation(p.Location),
		Kind:           kind(svc, i),
		Cancelled:      p.Cancelled(),
		StartsHere:     p.StartsHere,
		AtPlatform:     p.AtPlatform,
		TerminatesHere: p.TerminatesHere,
		Line:           p.Line,
		Path:           p.Path,
	}
	if p.IsPass() {
		out.Pass = times(svc, nil, p.WTTPass, p.ActualPass, p.EstPass, p.Cancelled())
	} else {
		out.Arrival = times(svc, p.GBTTArr, p.WTTArr, p.ActualArr, p.EstArr, p.ArrCancelled)
		out.Departure = times(svc, p.GBTTDep, p.WTTDep, p.ActualDep, p.EstDep, p.DepCancelled)
		if out.Arrival != nil {
			out.Arrival.Delayed = p.ArrDelayed
		}
		if out.Departure != nil {
			out.Departure.Delayed = p.DepDelayed
		}
	}
	if (p.Platform != "" || p.ActualPlatform != "") && !p.PlatformSuppressed {
		out.Platform = &Platform{
			Planned:   p.Platform,
			Actual:    p.ActualPlatform,
			Changed:   p.ActualPlatform != "" && p.Platform != "" && p.ActualPlatform != p.Platform,
			Confirmed: p.PlatformConfirmed,
		}
	}
	return out
}

func kind(svc *timetable.Service, i int) string {
	p := &svc.Stops[i]
	switch {
	case p.IsPass():
		return "pass"
	case i == 0:
		return "origin"
	case i == len(svc.Stops)-1:
		return "destination"
	case p.IsPublicCall():
		return "call"
	}
	return "stop"
}

func times(svc *timetable.Service, gbtt, wtt *int, actual, est *time.Time, cancelled bool) *Times {
	if gbtt == nil && wtt == nil {
		return nil
	}
	t := &Times{
		Public:    svc.At(gbtt),
		Working:   svc.At(wtt),
		Actual:    actual,
		Estimated: est,
		Cancelled: cancelled,
	}
	ref := t.Public
	if ref == nil {
		ref = t.Working
	}
	live := actual
	if live == nil {
		live = est
	}
	if live != nil && ref != nil {
		m := int(live.Sub(*ref).Round(time.Minute).Minutes())
		t.DelayMinutes = &m
	}
	return t
}

// parseLocal accepts RFC 3339 or a bare local "YYYY-MM-DDTHH:MM".
func parseLocal(v string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t, nil
	}
	return time.ParseInLocation("2006-01-02T15:04", v, ukrail.London)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	if err := enc.Encode(v); err != nil {
		slog.Warn("write response", "err", err)
	}
}

func writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func serverError(w http.ResponseWriter, err error) {
	slog.Error("request failed", "err", err)
	writeError(w, http.StatusInternalServerError, "internal error")
}
