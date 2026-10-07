package activity

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/carbonarok/trackside/internal/api"
	"github.com/carbonarok/trackside/internal/timetable"
)

// Server handles registration. With Enabled false (no APNs key configured)
// it answers 503, so the app knows pushes won't come.
type Server struct {
	Pool    *pgxpool.Pool
	Store   *timetable.Store
	Enabled bool
	// Bundles, if not empty, are the only bundle IDs accepted.
	Bundles map[string]bool
	Now     func() time.Time
}

// Register adds the activity routes to mux.
func (s *Server) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/activities/register", s.register)
	mux.HandleFunc("DELETE /v1/activities/{activity_id}", s.deregister)
}

func (s *Server) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Registration is the body of POST /v1/activities/register.
type Registration struct {
	PushToken      string `json:"push_token"`
	ActivityID     string `json:"activity_id"`
	ServiceUID     string `json:"service_uid"`
	RunDate        string `json:"run_date"`
	OriginCRS      string `json:"origin_crs"`
	DestinationCRS string `json:"destination_crs"`
	BundleID       string `json:"bundle_id"`
	// Optional: minutes to make this train from the previous leg, which only
	// the app knows; pushes would otherwise clear it.
	ConnectionMinutes *int `json:"connection_minutes,omitempty"`
	// Optional: the phase the app is showing, so pushes don't move it back.
	Phase string `json:"phase,omitempty"`
}

var (
	hexToken   = regexp.MustCompile(`^[0-9a-f]{16,512}$`)
	activityID = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)
	serviceUID = regexp.MustCompile(`^[A-Z0-9]{1,8}$`)
	crs        = regexp.MustCompile(`^[A-Z]{3}$`)
	bundleID   = regexp.MustCompile(`^[A-Za-z0-9-]+(\.[A-Za-z0-9-]+)+$`)
)

// normalise tidies a registration and reports the first thing wrong with it.
func (r *Registration) normalise() (time.Time, error) {
	r.PushToken = strings.ToLower(strings.TrimSpace(r.PushToken))
	r.ServiceUID = strings.ToUpper(strings.TrimSpace(r.ServiceUID))
	r.OriginCRS = strings.ToUpper(strings.TrimSpace(r.OriginCRS))
	r.DestinationCRS = strings.ToUpper(strings.TrimSpace(r.DestinationCRS))
	r.BundleID = strings.TrimSpace(r.BundleID)
	switch {
	case !hexToken.MatchString(r.PushToken) || len(r.PushToken)%2 != 0:
		return time.Time{}, errors.New("push_token must be the hex-encoded ActivityKit push token")
	case !activityID.MatchString(r.ActivityID):
		return time.Time{}, errors.New("activity_id must be 1 to 128 letters, digits, '.', '_', ':' or '-'")
	case !serviceUID.MatchString(r.ServiceUID):
		return time.Time{}, errors.New("service_uid must be a train UID such as W12345")
	case !crs.MatchString(r.OriginCRS) || !crs.MatchString(r.DestinationCRS):
		return time.Time{}, errors.New("origin_crs and destination_crs must be three-letter station codes")
	case len(r.BundleID) > 155 || !bundleID.MatchString(r.BundleID):
		return time.Time{}, errors.New("bundle_id must be the app's bundle identifier")
	case r.ConnectionMinutes != nil && (*r.ConnectionMinutes < 0 || *r.ConnectionMinutes > 1440):
		return time.Time{}, errors.New("connection_minutes must be between 0 and 1440")
	case r.Phase != "" && phaseRank[r.Phase] == 0 && r.Phase != PhaseUpcoming:
		return time.Time{}, errors.New("phase must be upcoming, boarding, onTrain or arrived")
	}
	date, err := time.Parse(time.DateOnly, r.RunDate)
	if err != nil {
		return time.Time{}, errors.New("run_date must be YYYY-MM-DD")
	}
	return date, nil
}

func (s *Server) register(w http.ResponseWriter, r *http.Request) {
	if !s.Enabled {
		writeError(w, http.StatusServiceUnavailable, "push notifications are not configured on this server")
		return
	}
	var reg Registration
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&reg); err != nil {
		writeError(w, http.StatusBadRequest, "body must be a JSON registration")
		return
	}
	date, err := reg.normalise()
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(s.Bundles) > 0 && !s.Bundles[reg.BundleID] {
		writeError(w, http.StatusForbidden, "bundle_id is not one this server pushes to")
		return
	}

	// The leg must exist, so pushes have something to show. Its current
	// state is what the app just put on screen, so it becomes the last state
	// sent: the first push is then a real change, and can alert.
	svc, err := s.Store.Service(r.Context(), reg.ServiceUID, date)
	if errors.Is(err, timetable.ErrNotFound) {
		writeError(w, http.StatusNotFound, "service not found")
		return
	}
	if err != nil {
		serverError(w, err)
		return
	}
	state, ok := State(api.Detail(svc), Leg{
		OriginCRS: reg.OriginCRS, DestinationCRS: reg.DestinationCRS,
		ConnectionMinutes: reg.ConnectionMinutes, Phase: reg.Phase,
	}, s.now())
	if !ok {
		writeError(w, http.StatusUnprocessableEntity,
			"origin_crs and destination_crs must be stations this service calls at, in that order")
		return
	}
	initial, _ := json.Marshal(state)

	// A new token for a known activity replaces the old one; the last state
	// and timestamp carry over, since the device keeps ordering by them.
	var inserted bool
	err = s.Pool.QueryRow(r.Context(), `
		INSERT INTO live_activities (activity_id, push_token, bundle_id, train_uid, run_date,
			origin_crs, destination_crs, connection_minutes, last_state)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (activity_id) DO UPDATE SET
			push_token = EXCLUDED.push_token, bundle_id = EXCLUDED.bundle_id,
			train_uid = EXCLUDED.train_uid, run_date = EXCLUDED.run_date,
			origin_crs = EXCLUDED.origin_crs, destination_crs = EXCLUDED.destination_crs,
			connection_minutes = EXCLUDED.connection_minutes,
			apns_host = CASE WHEN live_activities.push_token = EXCLUDED.push_token
			                 THEN live_activities.apns_host END,
			last_state = COALESCE(live_activities.last_state, EXCLUDED.last_state),
			updated_at = now()
		RETURNING (xmax = 0)`,
		reg.ActivityID, reg.PushToken, reg.BundleID, reg.ServiceUID, date,
		reg.OriginCRS, reg.DestinationCRS, reg.ConnectionMinutes, initial).Scan(&inserted)
	if err != nil {
		serverError(w, err)
		return
	}
	status := http.StatusOK
	if inserted {
		status = http.StatusCreated
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"activity_id": reg.ActivityID, "status": "registered"})
}

func (s *Server) deregister(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("activity_id")
	if !activityID.MatchString(id) {
		writeError(w, http.StatusBadRequest, "activity_id is not valid")
		return
	}
	// Deleting one that's already gone is fine: the app's goal is met.
	if _, err := s.Pool.Exec(r.Context(), `DELETE FROM live_activities WHERE activity_id = $1`, id); err != nil {
		serverError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
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
