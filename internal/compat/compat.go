// Package compat serves endpoints shaped like the legacy Realtime Trains v1
// JSON API (api.rtt.io/api/v1/json), so existing clients can switch by
// changing their base URL. Field names follow that API; data comes from
// trackside's own timetable.
package compat

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/carbonarok/trackside/internal/timetable"
	"github.com/carbonarok/trackside/internal/trust"
	"github.com/carbonarok/trackside/internal/ukrail"
)

// Prefix is where the compatibility API is mounted.
const Prefix = "/api/v1/json"

// Search windows. Without a time the board covers the next hour or so from
// now; with an explicit date but no time it covers the whole day.
const (
	liveLookBack = 15 * time.Minute
	liveWindow   = 2 * time.Hour
	timedWindow  = 2 * time.Hour
)

// Server holds the compat API's dependencies.
type Server struct {
	Store *timetable.Store
	Now   func() time.Time
}

// Register adds the compat routes to mux.
func (s *Server) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET "+Prefix+"/search/", s.search)
	mux.HandleFunc("GET "+Prefix+"/service/{uid}/{y}/{m}/{d}", s.service)
}

func (s *Server) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

type searchReq struct {
	station, to string
	date        *time.Time // UK calendar date
	hhmm        string
	arrivals    bool
}

// parseSearch reads /search/{station}[/to/{to}][/{y}/{m}/{d}[/{hhmm}]][/arrivals].
func parseSearch(path string) (*searchReq, error) {
	parts := strings.Split(strings.Trim(strings.TrimPrefix(path, Prefix+"/search/"), "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		return nil, errors.New("missing station")
	}
	req := &searchReq{station: parts[0]}
	parts = parts[1:]
	if len(parts) > 0 && parts[len(parts)-1] == "arrivals" {
		req.arrivals = true
		parts = parts[:len(parts)-1]
	}
	if len(parts) >= 2 && parts[0] == "to" {
		req.to = parts[1]
		parts = parts[2:]
	}
	switch len(parts) {
	case 0:
	case 3, 4:
		d, err := time.Parse("2006/01/02", strings.Join(parts[:3], "/"))
		if err != nil {
			return nil, errors.New("invalid date")
		}
		req.date = &d
		if len(parts) == 4 {
			if _, ok := ukrail.ParseWTT(parts[3]); !ok {
				return nil, errors.New("invalid time")
			}
			req.hhmm = parts[3]
		}
	default:
		return nil, errors.New("unrecognised search path")
	}
	return req, nil
}

func (s *Server) search(w http.ResponseWriter, r *http.Request) {
	req, err := parseSearch(r.URL.Path)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	locs, err := s.Store.Lookup(r.Context(), req.station)
	if err != nil {
		lookupError(w, err)
		return
	}
	q := timetable.BoardQuery{Arrivals: req.arrivals}
	switch {
	case req.date == nil:
		now := s.now()
		q.From, q.To = now.Add(-liveLookBack), now.Add(liveWindow)
	case req.hhmm == "":
		y, m, d := req.date.Date()
		q.From = time.Date(y, m, d, 0, 0, 0, 0, ukrail.London)
		q.To = q.From.AddDate(0, 0, 1)
	default:
		secs, _ := ukrail.ParseWTT(req.hhmm)
		q.From = ukrail.AtRunDate(*req.date, secs)
		q.To = q.From.Add(timedWindow)
	}
	for _, l := range locs {
		q.TIPLOCs = append(q.TIPLOCs, l.TIPLOC)
	}
	var filter any
	if req.to != "" {
		other, err := s.Store.Lookup(r.Context(), req.to)
		if err != nil {
			lookupError(w, err)
			return
		}
		for _, l := range other {
			q.Calling = append(q.Calling, l.TIPLOC)
		}
		key := "destination"
		if req.arrivals {
			key = "origin"
		}
		filter = map[string]any{key: locationHeader(other)}
	}
	entries, err := s.Store.Board(r.Context(), q)
	if err != nil {
		serverError(w, err)
		return
	}
	var services []searchService
	for _, e := range entries {
		services = append(services, searchService{
			LocationDetail: locationDetail(e.Service, e.Index),
			serviceHeader:  header(e.Service),
		})
	}
	writeJSON(w, map[string]any{
		"location": locationHeader(locs),
		"filter":   filter,
		"services": services,
	})
}

func (s *Server) service(w http.ResponseWriter, r *http.Request) {
	date, err := time.Parse("2006/01/02",
		r.PathValue("y")+"/"+r.PathValue("m")+"/"+r.PathValue("d"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid date")
		return
	}
	svc, err := s.Store.Service(r.Context(), r.PathValue("uid"), date)
	if err != nil {
		lookupError(w, err)
		return
	}
	assocs, err := s.Store.Associations(r.Context(), svc)
	if err != nil {
		serverError(w, err)
		return
	}
	locations := make([]LocationDetail, 0, len(svc.Stops))
	for i := range svc.Stops {
		d := locationDetail(svc, i)
		for _, a := range assocs {
			if a.Location.TIPLOC != d.TIPLOC || a.Cancelled {
				continue
			}
			d.Associations = append(d.Associations, compatAssociation{
				Type:              compatAssociationTypes[a.Type],
				AssociatedUID:     a.OtherUID,
				AssociatedRunDate: a.OtherRunDate.Format(time.DateOnly),
			})
		}
		locations = append(locations, d)
	}
	var origin, destination []Pair
	if n := len(svc.Stops); n > 0 {
		origin = []Pair{pair(svc, &svc.Stops[0], true)}
		destination = []Pair{pair(svc, &svc.Stops[n-1], false)}
	}
	writeJSON(w, serviceResponse{
		serviceHeader:        header(svc),
		PowerType:            svc.PowerType,
		TrainClass:           svc.TrainClass,
		PerformanceMonitored: svc.TrustID != "",
		Origin:               origin,
		Destination:          destination,
		Locations:            locations,
		RealtimeActivated:    svc.TrustID != "",
	})
}

type serviceHeader struct {
	ServiceUID      string `json:"serviceUid"`
	RunDate         string `json:"runDate"`
	TrainIdentity   string `json:"trainIdentity"`
	RunningIdentity string `json:"runningIdentity"`
	ATOCCode        string `json:"atocCode"`
	ATOCName        string `json:"atocName"`
	ServiceType     string `json:"serviceType"`
	IsPassenger     bool   `json:"isPassenger"`
	PlannedCancel   bool   `json:"plannedCancel,omitempty"`
}

type searchService struct {
	LocationDetail LocationDetail `json:"locationDetail"`
	serviceHeader
}

type serviceResponse struct {
	serviceHeader
	PowerType            string           `json:"powerType,omitempty"`
	TrainClass           string           `json:"trainClass,omitempty"`
	PerformanceMonitored bool             `json:"performanceMonitored"`
	Origin               []Pair           `json:"origin"`
	Destination          []Pair           `json:"destination"`
	Locations            []LocationDetail `json:"locations"`
	RealtimeActivated    bool             `json:"realtimeActivated"`
}

// Pair is an origin or destination entry.
type Pair struct {
	TIPLOC      string `json:"tiploc"`
	Description string `json:"description"`
	WorkingTime string `json:"workingTime,omitempty"`
	PublicTime  string `json:"publicTime,omitempty"`
}

// LocationDetail mirrors the legacy per-location object.
type LocationDetail struct {
	RealtimeActivated bool   `json:"realtimeActivated"`
	TIPLOC            string `json:"tiploc"`
	CRS               string `json:"crs,omitempty"`
	Description       string `json:"description"`

	WTTBookedArrival   string `json:"wttBookedArrival,omitempty"`
	WTTBookedDeparture string `json:"wttBookedDeparture,omitempty"`
	WTTBookedPass      string `json:"wttBookedPass,omitempty"`

	GBTTBookedArrival          string `json:"gbttBookedArrival,omitempty"`
	GBTTBookedArrivalNextDay   bool   `json:"gbttBookedArrivalNextDay,omitempty"`
	GBTTBookedDeparture        string `json:"gbttBookedDeparture,omitempty"`
	GBTTBookedDepartureNextDay bool   `json:"gbttBookedDepartureNextDay,omitempty"`

	Origin      []Pair `json:"origin"`
	Destination []Pair `json:"destination"`

	IsCall       bool `json:"isCall"`
	IsPublicCall bool `json:"isPublicCall"`

	RealtimeArrival               string `json:"realtimeArrival,omitempty"`
	RealtimeArrivalActual         bool   `json:"realtimeArrivalActual"`
	RealtimeArrivalNextDay        bool   `json:"realtimeArrivalNextDay,omitempty"`
	RealtimeDeparture             string `json:"realtimeDeparture,omitempty"`
	RealtimeDepartureActual       bool   `json:"realtimeDepartureActual"`
	RealtimeDepartureNextDay      bool   `json:"realtimeDepartureNextDay,omitempty"`
	RealtimePass                  string `json:"realtimePass,omitempty"`
	RealtimePassActual            bool   `json:"realtimePassActual,omitempty"`
	RealtimeGBTTArrivalLateness   *int   `json:"realtimeGbttArrivalLateness,omitempty"`
	RealtimeGBTTDepartureLateness *int   `json:"realtimeGbttDepartureLateness,omitempty"`

	Platform          string `json:"platform,omitempty"`
	PlatformConfirmed bool   `json:"platformConfirmed"`
	PlatformChanged   bool   `json:"platformChanged"`
	Line              string `json:"line,omitempty"`
	Path              string `json:"path,omitempty"`

	DisplayAs             string              `json:"displayAs"`
	Associations          []compatAssociation `json:"associations,omitempty"`
	ServiceLocation       string              `json:"serviceLocation,omitempty"`
	CancelReasonCode      string              `json:"cancelReasonCode,omitempty"`
	CancelReasonShortText string              `json:"cancelReasonShortText,omitempty"`
	CancelReasonLongText  string              `json:"cancelReasonLongText,omitempty"`
}

type compatAssociation struct {
	Type              string `json:"type"`
	AssociatedUID     string `json:"associatedUid"`
	AssociatedRunDate string `json:"associatedRunDate"`
}

// compatAssociationTypes maps trackside's association types onto the legacy
// API's vocabulary.
var compatAssociationTypes = map[string]string{
	"divides":      "divide",
	"divided_from": "divide",
	"joined_by":    "join",
	"joins":        "join",
	"forms":        "next",
	"formed_from":  "prev",
	"linked":       "linked",
	"associated":   "linked",
}

func header(svc *timetable.Service) serviceHeader {
	serviceType := "train"
	if svc.TrainStatus == "B" || svc.TrainStatus == "5" {
		serviceType = "bus"
	} else if svc.TrainStatus == "S" || svc.TrainStatus == "4" {
		serviceType = "ship"
	}
	return serviceHeader{
		ServiceUID:      svc.UID,
		RunDate:         svc.RunDate.Format(time.DateOnly),
		TrainIdentity:   svc.Headcode,
		RunningIdentity: svc.Headcode,
		ATOCCode:        svc.ATOCCode,
		ATOCName:        operatorName(svc),
		ServiceType:     serviceType,
		IsPassenger:     ukrail.IsPassenger(svc.TrainStatus, svc.Category),
		PlannedCancel:   svc.PlannedCancel,
	}
}

func operatorName(svc *timetable.Service) string {
	if svc.OperatorName != "" {
		return svc.OperatorName
	}
	return ukrail.OperatorName(svc.ATOCCode)
}

func locationHeader(locs []timetable.Location) map[string]any {
	tiplocs := make([]string, 0, len(locs))
	for _, l := range locs {
		tiplocs = append(tiplocs, l.TIPLOC)
	}
	return map[string]any{
		"name":    locs[0].Name,
		"crs":     locs[0].CRS,
		"tiploc":  locs[0].TIPLOC,
		"tiplocs": tiplocs,
	}
}

func pair(svc *timetable.Service, p *timetable.Stop, origin bool) Pair {
	wtt, gbtt := p.WTTArr, p.GBTTArr
	if origin {
		wtt, gbtt = p.WTTDep, p.GBTTDep
	}
	out := Pair{TIPLOC: p.Location.TIPLOC, Description: p.Location.Name}
	if wtt != nil {
		out.WorkingTime = ukrail.HHMMSS(*wtt)
	}
	if gbtt != nil {
		out.PublicTime = ukrail.HHMM(*gbtt)
	}
	return out
}

func locationDetail(svc *timetable.Service, i int) LocationDetail {
	p := &svc.Stops[i]
	n := len(svc.Stops)
	d := LocationDetail{
		RealtimeActivated: svc.TrustID != "",
		TIPLOC:            p.Location.TIPLOC,
		CRS:               p.Location.CRS,
		Description:       p.Location.Name,
		Origin:            []Pair{pair(svc, &svc.Stops[0], true)},
		Destination:       []Pair{pair(svc, &svc.Stops[n-1], false)},
		IsCall:            !p.IsPass(),
		IsPublicCall:      p.IsPublicCall(),
		Line:              p.Line,
		Path:              p.Path,
		DisplayAs:         displayAs(svc, i),
	}
	if p.AtPlatform {
		d.ServiceLocation = "AT_PLAT"
	}
	if p.WTTArr != nil {
		d.WTTBookedArrival = ukrail.HHMMSS(*p.WTTArr)
	}
	if p.WTTDep != nil {
		d.WTTBookedDeparture = ukrail.HHMMSS(*p.WTTDep)
	}
	if p.WTTPass != nil {
		d.WTTBookedPass = ukrail.HHMMSS(*p.WTTPass)
	}
	if p.GBTTArr != nil {
		d.GBTTBookedArrival = ukrail.HHMM(*p.GBTTArr)
		d.GBTTBookedArrivalNextDay = *p.GBTTArr >= 86400
	}
	if p.GBTTDep != nil {
		d.GBTTBookedDeparture = ukrail.HHMM(*p.GBTTDep)
		d.GBTTBookedDepartureNextDay = *p.GBTTDep >= 86400
	}

	if t, actual := live(p.ActualArr, p.EstArr); t != nil {
		d.RealtimeArrival, d.RealtimeArrivalActual = t.Format("1504"), actual
		d.RealtimeArrivalNextDay = nextDay(svc, *t)
		d.RealtimeGBTTArrivalLateness = lateness(svc, p.GBTTArr, *t)
	}
	if t, actual := live(p.ActualDep, p.EstDep); t != nil {
		d.RealtimeDeparture, d.RealtimeDepartureActual = t.Format("1504"), actual
		d.RealtimeDepartureNextDay = nextDay(svc, *t)
		d.RealtimeGBTTDepartureLateness = lateness(svc, p.GBTTDep, *t)
	}
	if t, actual := live(p.ActualPass, p.EstPass); t != nil {
		d.RealtimePass, d.RealtimePassActual = t.Format("1504"), actual
	}

	if !p.PlatformSuppressed {
		d.Platform = p.Platform
		if p.ActualPlatform != "" {
			d.PlatformChanged = p.Platform != "" && p.ActualPlatform != p.Platform
			d.Platform = p.ActualPlatform
		}
		d.PlatformConfirmed = p.PlatformConfirmed
	}
	if p.Cancelled() || p.ArrCancelled || p.DepCancelled {
		d.CancelReasonCode = svc.CancelReason
		d.CancelReasonLongText = svc.CancelReasonText
		d.CancelReasonShortText = shortReason(svc.CancelReasonText)
		// Without Darwin, fall back to the delay attribution code's cause.
		if c, ok := trust.LookupDelayCode(svc.CancelReason); ok && d.CancelReasonLongText == "" {
			d.CancelReasonLongText = c.Cause
			d.CancelReasonShortText = c.Cause
		}
	}
	return d
}

// shortReason trims Darwin's sentence ("This train has been cancelled
// because of a broken down train") to its cause ("a broken down train").
func shortReason(text string) string {
	for _, sep := range []string{" because of ", " due to ", " by "} {
		if i := strings.Index(text, sep); i >= 0 {
			return text[i+len(sep):]
		}
	}
	return text
}

func live(actual, est *time.Time) (*time.Time, bool) {
	if actual != nil {
		t := actual.In(ukrail.London)
		return &t, true
	}
	if est != nil {
		t := est.In(ukrail.London)
		return &t, false
	}
	return nil, false
}

func nextDay(svc *timetable.Service, t time.Time) bool {
	return ukrail.DateOf(t).After(svc.RunDate)
}

func lateness(svc *timetable.Service, gbtt *int, t time.Time) *int {
	if gbtt == nil {
		return nil
	}
	m := int(t.Sub(ukrail.AtRunDate(svc.RunDate, *gbtt)).Round(time.Minute).Minutes())
	return &m
}

// displayAs reproduces the legacy displayAs values.
func displayAs(svc *timetable.Service, i int) string {
	p := &svc.Stops[i]
	switch {
	case p.Cancelled() && p.IsPass():
		return "CANCELLED_PASS"
	case p.Cancelled():
		return "CANCELLED_CALL"
	case p.IsPass():
		return "PASS"
	case p.StartsHere:
		return "STARTS"
	case p.TerminatesHere:
		return "TERMINATES"
	case i == 0:
		return "ORIGIN"
	case i == len(svc.Stops)-1:
		return "DESTINATION"
	}
	return "CALL"
}

func lookupError(w http.ResponseWriter, err error) {
	if errors.Is(err, timetable.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	serverError(w, err)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
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
