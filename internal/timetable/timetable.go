// Package timetable answers queries over resolved services: location boards
// and service detail, with live data from TRUST merged in.
package timetable

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/carbonarok/trackside/internal/ukrail"
)

// ErrNotFound is returned when a location or service does not exist.
var ErrNotFound = errors.New("not found")

// Store runs timetable queries.
type Store struct {
	Pool *pgxpool.Pool
	// Now is the clock used to expire stale live state; tests override it.
	Now func() time.Time
}

func (st *Store) now() time.Time {
	if st.Now != nil {
		return st.Now()
	}
	return time.Now()
}

// Location is a timetable location.
type Location struct {
	TIPLOC string
	CRS    string
	STANOX string
	Name   string
}

// Service is a train on a run date with its full route.
type Service struct {
	ID            int64
	UID           string
	RunDate       time.Time
	Headcode      string
	ATOCCode      string
	TrainStatus   string
	Category      string
	PowerType     string
	TrainClass    string
	Source        string
	STP           string
	PlannedCancel bool
	TrustID       string
	ActivatedAt   *time.Time
	CancelSTANOX  string
	CancelType    string
	CancelReason  string
	OriginSTANOX  string
	OperatorName  string // from Darwin reference data, if loaded
	DarwinRID     string
	// Darwin reason texts, e.g. "This train has been delayed by a broken
	// down train".
	LateReasonText   string
	CancelReasonText string
	// Last Train Describer berth the train was seen in.
	TDArea, TDBerth string
	TDBerthAt       *time.Time
	// TDApproachTIPLOC is the location TD last saw the train approaching.
	TDApproachTIPLOC string
	TDApproachAt     *time.Time
	Stops            []Stop
}

// Stop is one location on a service's route.
type Stop struct {
	Seq      int
	Location Location
	Type     string // LO, LI or LT
	WTTArr   *int
	WTTDep   *int
	WTTPass  *int
	GBTTArr  *int
	GBTTDep  *int
	Platform string
	Line     string
	Path     string

	ActualArr      *time.Time
	ActualDep      *time.Time
	ActualPass     *time.Time
	ActualPlatform string
	Darwin         *DarwinForecast

	// Derived by annotate.
	EstArr         *time.Time
	EstDep         *time.Time
	EstPass        *time.Time
	ArrCancelled   bool
	DepCancelled   bool
	StartsHere     bool // first served stop after a change of origin
	TerminatesHere bool // last served stop after a part cancellation
	ArrDelayed     bool // late by an unknown amount
	DepDelayed     bool
	// Platform display state. A suppressed platform must not be shown.
	PlatformConfirmed  bool
	PlatformSuppressed bool
	// AtPlatform means the train has arrived here and not yet departed.
	AtPlatform bool
	// Approaching means TD has the train in the berth before this location.
	Approaching bool
}

// DarwinForecast is Darwin's live view of one stop.
type DarwinForecast struct {
	ArrET, ArrAT, DepET, DepAT, PassET, PassAT *time.Time
	ArrDelayed, DepDelayed                     bool
	Platform                                   string
	PlatformSuppressed, PlatformConfirmed      bool
	ArrCancelled, DepCancelled                 bool
}

// IsPass reports whether the train passes rather than calls.
func (s *Stop) IsPass() bool { return s.WTTPass != nil }

// IsPublicCall reports whether the stop is in the public timetable.
func (s *Stop) IsPublicCall() bool { return s.GBTTArr != nil || s.GBTTDep != nil }

// Cancelled reports whether the train no longer serves this stop at all.
func (s *Stop) Cancelled() bool {
	switch {
	case s.WTTArr == nil && s.WTTPass == nil:
		return s.DepCancelled
	case s.WTTDep == nil && s.WTTPass == nil:
		return s.ArrCancelled
	}
	return s.ArrCancelled && s.DepCancelled
}

// Lookup finds a location by CRS or TIPLOC. A CRS returns every TIPLOC that
// shares it (large stations have several), first one as the representative.
func (st *Store) Lookup(ctx context.Context, code string) ([]Location, error) {
	code = strings.ToUpper(strings.TrimSpace(code))
	col := "tiploc"
	if len(code) == 3 {
		col = "crs"
	}
	rows, err := st.Pool.Query(ctx, `SELECT tiploc, COALESCE(crs, ''), COALESCE(stanox, ''),
		COALESCE(name, initcap(tps_description), tiploc)
		FROM locations WHERE `+col+` = $1 ORDER BY tiploc`, code)
	if err != nil {
		return nil, err
	}
	locs, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Location, error) {
		var l Location
		err := r.Scan(&l.TIPLOC, &l.CRS, &l.STANOX, &l.Name)
		return l, err
	})
	if err != nil {
		return nil, err
	}
	if len(locs) == 0 {
		return nil, ErrNotFound
	}
	return locs, nil
}

// Search finds locations whose name starts with or contains q, stations first.
func (st *Store) Search(ctx context.Context, q string, limit int) ([]Location, error) {
	rows, err := st.Pool.Query(ctx, `SELECT tiploc, COALESCE(crs, ''), COALESCE(stanox, ''),
		COALESCE(name, initcap(tps_description), tiploc)
		FROM locations
		WHERE name ILIKE '%' || $1 || '%' OR crs = upper($1) OR tiploc = upper($1)
		ORDER BY (crs = upper($1)) DESC NULLS LAST, (crs IS NULL), (name ILIKE $1 || '%') DESC, name
		LIMIT $2`, q, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Location, error) {
		var l Location
		err := r.Scan(&l.TIPLOC, &l.CRS, &l.STANOX, &l.Name)
		return l, err
	})
}

// BoardQuery describes a location board.
type BoardQuery struct {
	TIPLOCs  []string
	From, To time.Time // UK local times
	Arrivals bool
	Passes   bool     // include trains passing without stopping
	Calling  []string // when set, only trains that later (or, for arrivals, earlier) call at one of these TIPLOCs
}

// BoardEntry is a service and the index of the queried location in its stops.
type BoardEntry struct {
	Service *Service
	Index   int
}

// Board returns services at a location in a time window, in time order.
func (st *Store) Board(ctx context.Context, q BoardQuery) ([]BoardEntry, error) {
	timeCol := "sl.gbtt_dep"
	if q.Arrivals {
		timeCol = "sl.gbtt_arr"
	}
	if q.Passes {
		if q.Arrivals {
			timeCol = "COALESCE(sl.gbtt_arr, sl.wtt_arr, sl.wtt_pass)"
		} else {
			timeCol = "COALESCE(sl.gbtt_dep, sl.wtt_dep, sl.wtt_pass)"
		}
	}
	const layout = "2006-01-02 15:04:05"
	from, to := q.From.In(ukrail.London), q.To.In(ukrail.London)
	rows, err := st.Pool.Query(ctx, `
		SELECT sv.id, sl.seq, `+timeCol+` AS t
		FROM services sv
		JOIN schedule_locations sl ON sl.schedule_id = sv.schedule_id
		WHERE sv.run_date BETWEEN $1::date - 1 AND $2::date
		  AND sl.tiploc = ANY($3)
		  AND `+timeCol+` IS NOT NULL
		  AND sv.run_date + make_interval(secs => `+timeCol+`) >= $4::timestamp
		  AND sv.run_date + make_interval(secs => `+timeCol+`) < $5::timestamp`,
		ukrail.DateOf(from), ukrail.DateOf(to), q.TIPLOCs, from.Format(layout), to.Format(layout))
	if err != nil {
		return nil, err
	}
	type hit struct {
		id  int64
		seq int
	}
	var hits []hit
	for rows.Next() {
		var h hit
		var t int
		if err := rows.Scan(&h.id, &h.seq, &t); err != nil {
			return nil, err
		}
		hits = append(hits, h)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	ids := make([]int64, 0, len(hits))
	for _, h := range hits {
		ids = append(ids, h.id)
	}
	services, err := st.load(ctx, ids)
	if err != nil {
		return nil, err
	}

	calling := make(map[string]bool, len(q.Calling))
	for _, t := range q.Calling {
		calling[t] = true
	}
	var out []BoardEntry
	for _, h := range hits {
		svc := services[h.id]
		if svc == nil {
			continue
		}
		idx := svc.indexOf(h.seq)
		if idx < 0 {
			continue
		}
		if len(calling) > 0 && !svc.callsAt(calling, idx, q.Arrivals) {
			continue
		}
		out = append(out, BoardEntry{Service: svc, Index: idx})
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].sortTime(q.Arrivals).Before(out[j].sortTime(q.Arrivals))
	})
	return out, nil
}

func (e BoardEntry) sortTime(arrivals bool) time.Time {
	s := e.Service.Stops[e.Index]
	t := s.GBTTDep
	if arrivals {
		t = s.GBTTArr
	}
	for _, alt := range []*int{s.WTTDep, s.WTTArr, s.WTTPass} {
		if t == nil {
			t = alt
		}
	}
	if t == nil {
		return time.Time{}
	}
	return ukrail.AtRunDate(e.Service.RunDate, *t)
}

// place finds the stop a live row belongs to. exact is true when the row's
// seq still points at its TIPLOC; otherwise the nearest stop at that TIPLOC
// is used. Rows without a TIPLOC are placed by seq alone.
func (s *Service) place(seq int, tiploc string) (int, bool) {
	if i := s.indexOf(seq); i >= 0 && (tiploc == "" || s.Stops[i].Location.TIPLOC == tiploc) {
		return i, true
	}
	if tiploc == "" {
		return -1, false
	}
	best, bestDist := -1, 0
	for i := range s.Stops {
		if s.Stops[i].Location.TIPLOC != tiploc {
			continue
		}
		dist := s.Stops[i].Seq - seq
		if dist < 0 {
			dist = -dist
		}
		if best < 0 || dist < bestDist {
			best, bestDist = i, dist
		}
	}
	return best, false
}

func (s *Service) indexOf(seq int) int {
	for i := range s.Stops {
		if s.Stops[i].Seq == seq {
			return i
		}
	}
	return -1
}

// callsAt reports whether the service calls publicly at one of the TIPLOCs
// after (or, when before is set, before) stop idx.
func (s *Service) callsAt(tiplocs map[string]bool, idx int, before bool) bool {
	lo, hi := idx+1, len(s.Stops)
	if before {
		lo, hi = 0, idx
	}
	for i := lo; i < hi; i++ {
		st := s.Stops[i]
		if !tiplocs[st.Location.TIPLOC] {
			continue
		}
		if (!before && st.GBTTArr != nil) || (before && st.GBTTDep != nil) {
			return true
		}
	}
	return false
}

// Service returns one service by train UID and run date.
func (st *Store) Service(ctx context.Context, uid string, runDate time.Time) (*Service, error) {
	var id int64
	err := st.Pool.QueryRow(ctx, `SELECT id FROM services WHERE train_uid = $1 AND run_date = $2`,
		strings.ToUpper(uid), runDate).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	m, err := st.load(ctx, []int64{id})
	if err != nil {
		return nil, err
	}
	if m[id] == nil {
		return nil, ErrNotFound
	}
	return m[id], nil
}

// load fetches services with their stops and live events, then derives
// cancellations and estimates.
func (st *Store) load(ctx context.Context, ids []int64) (map[int64]*Service, error) {
	out := make(map[int64]*Service, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := st.Pool.Query(ctx, `
		SELECT sv.id, sv.train_uid, sv.run_date, COALESCE(s.signalling_id, ''), COALESCE(s.atoc_code, ''),
		       COALESCE(s.train_status, ''), COALESCE(s.category, ''), COALESCE(s.power_type, ''),
		       COALESCE(s.train_class, ''), s.source, s.stp, sv.planned_cancel, COALESCE(sv.trust_id, ''),
		       sv.activated_at, COALESCE(sv.cancel_stanox, ''), COALESCE(sv.cancel_type, ''),
		       COALESCE(sv.cancel_reason, ''), COALESCE(sv.origin_stanox, ''),
		       COALESCE(o.name, ''), COALESCE(sv.darwin_rid, ''), COALESCE(lr.text, ''), COALESCE(cr.text, ''),
		       COALESCE(sv.td_area, ''), COALESCE(sv.td_berth, ''), sv.td_berth_at,
		       COALESCE(sv.td_approach_tiploc, ''), sv.td_approach_at
		FROM services sv JOIN schedules s ON s.id = sv.schedule_id
		LEFT JOIN operators o ON o.code = s.atoc_code
		LEFT JOIN darwin_reasons lr ON lr.kind = 'late' AND lr.code = sv.darwin_late_code
		LEFT JOIN darwin_reasons cr ON cr.kind = 'cancel' AND cr.code = sv.darwin_cancel_code
		WHERE sv.id = ANY($1)`, ids)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		s := &Service{}
		if err := rows.Scan(&s.ID, &s.UID, &s.RunDate, &s.Headcode, &s.ATOCCode, &s.TrainStatus,
			&s.Category, &s.PowerType, &s.TrainClass, &s.Source, &s.STP, &s.PlannedCancel, &s.TrustID,
			&s.ActivatedAt, &s.CancelSTANOX, &s.CancelType, &s.CancelReason, &s.OriginSTANOX,
			&s.OperatorName, &s.DarwinRID, &s.LateReasonText, &s.CancelReasonText,
			&s.TDArea, &s.TDBerth, &s.TDBerthAt, &s.TDApproachTIPLOC, &s.TDApproachAt); err != nil {
			return nil, err
		}
		out[s.ID] = s
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	rows, err = st.Pool.Query(ctx, `
		SELECT sv.id, sl.seq, sl.tiploc, COALESCE(l.crs, ''), COALESCE(l.stanox, ''),
		       COALESCE(l.name, initcap(l.tps_description), sl.tiploc), sl.loc_type,
		       sl.wtt_arr, sl.wtt_dep, sl.wtt_pass, sl.gbtt_arr, sl.gbtt_dep,
		       COALESCE(sl.platform, ''), COALESCE(sl.line, ''), COALESCE(sl.path, '')
		FROM services sv
		JOIN schedule_locations sl ON sl.schedule_id = sv.schedule_id
		LEFT JOIN locations l ON l.tiploc = sl.tiploc
		WHERE sv.id = ANY($1)
		ORDER BY sv.id, sl.seq`, ids)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id int64
		var p Stop
		if err := rows.Scan(&id, &p.Seq, &p.Location.TIPLOC, &p.Location.CRS, &p.Location.STANOX,
			&p.Location.Name, &p.Type, &p.WTTArr, &p.WTTDep, &p.WTTPass, &p.GBTTArr, &p.GBTTDep,
			&p.Platform, &p.Line, &p.Path); err != nil {
			return nil, err
		}
		if s := out[id]; s != nil {
			s.Stops = append(s.Stops, p)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Live rows are placed by seq, re-anchored by TIPLOC when the schedule
	// has changed under them. Rows that still match their seq go first so a
	// re-anchored row never overwrites fresher data.
	type eventRow struct {
		id            int64
		seq           int
		tiploc, event string
		platform      string
		actual        time.Time
	}
	rows, err = st.Pool.Query(ctx, `SELECT service_id, seq, COALESCE(tiploc, ''), event, actual,
		COALESCE(platform, '') FROM service_events WHERE service_id = ANY($1)`, ids)
	if err != nil {
		return nil, err
	}
	events, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (eventRow, error) {
		var e eventRow
		err := r.Scan(&e.id, &e.seq, &e.tiploc, &e.event, &e.actual, &e.platform)
		return e, err
	})
	if err != nil {
		return nil, err
	}
	for _, pass := range []bool{true, false} {
		for _, e := range events {
			s := out[e.id]
			if s == nil {
				continue
			}
			i, exact := s.place(e.seq, e.tiploc)
			if i < 0 || exact != pass {
				continue
			}
			p := &s.Stops[i]
			a := e.actual.In(ukrail.London)
			var slot **time.Time
			switch e.event {
			case "arr":
				slot = &p.ActualArr
			case "dep":
				slot = &p.ActualDep
			case "pass":
				slot = &p.ActualPass
			default:
				continue
			}
			if !exact && *slot != nil {
				continue
			}
			*slot = &a
			if e.platform != "" && (exact || p.ActualPlatform == "") {
				p.ActualPlatform = e.platform
			}
		}
	}

	type forecastRow struct {
		id     int64
		seq    int
		tiploc string
		d      *DarwinForecast
	}
	rows, err = st.Pool.Query(ctx, `SELECT service_id, seq, COALESCE(tiploc, ''), arr_et, arr_at, dep_et,
		       dep_at, pass_et, pass_at, arr_delayed, dep_delayed, COALESCE(platform, ''),
		       platform_suppressed, platform_confirmed, arr_cancelled, dep_cancelled
		FROM darwin_forecasts WHERE service_id = ANY($1)`, ids)
	if err != nil {
		return nil, err
	}
	forecasts, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (forecastRow, error) {
		f := forecastRow{d: &DarwinForecast{}}
		d := f.d
		err := r.Scan(&f.id, &f.seq, &f.tiploc, &d.ArrET, &d.ArrAT, &d.DepET, &d.DepAT, &d.PassET,
			&d.PassAT, &d.ArrDelayed, &d.DepDelayed, &d.Platform, &d.PlatformSuppressed,
			&d.PlatformConfirmed, &d.ArrCancelled, &d.DepCancelled)
		for _, t := range []**time.Time{&d.ArrET, &d.ArrAT, &d.DepET, &d.DepAT, &d.PassET, &d.PassAT} {
			if *t != nil {
				local := (*t).In(ukrail.London)
				*t = &local
			}
		}
		return f, err
	})
	if err != nil {
		return nil, err
	}
	for _, pass := range []bool{true, false} {
		for _, f := range forecasts {
			s := out[f.id]
			if s == nil {
				continue
			}
			i, exact := s.place(f.seq, f.tiploc)
			if i < 0 || exact != pass || (!exact && s.Stops[i].Darwin != nil) {
				continue
			}
			s.Stops[i].Darwin = f.d
		}
	}
	now := st.now()
	for _, s := range out {
		s.annotate()
		s.applyApproach(now)
	}
	return out, nil
}
