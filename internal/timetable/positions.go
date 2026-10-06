package timetable

import (
	"context"
	"math"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/carbonarok/trackside/internal/ukrail"
)

// Position is where a train is estimated to be.
type Position struct {
	Service *Service
	Lat     float64
	Lon     float64
	// Bearing is the direction of travel in degrees from north.
	Bearing float64
	// AtStation is set when the train is standing at Last.
	AtStation bool
	// Last and Next are the stations either side of the train (Last only
	// when it is standing at a station).
	Last, Next *Stop
	// DelayMinutes is how late the train was at its last reported event,
	// nil if nothing has been reported.
	DelayMinutes *int
}

// positionCache holds the latest computed positions; working them out
// loads every running train, so they are shared between requests.
type positionCache struct {
	mu      sync.Mutex
	at      time.Time
	results []Position
}

// positionTTL is how long computed positions are reused.
const positionTTL = 15 * time.Second

// Positions estimates where every running train is now. Trains are placed
// at a station while they stand there, and otherwise along the straight
// line between the stations either side, in proportion to the time between
// leaving one and reaching the next (actual, forecast or booked times, in
// that order).
func (st *Store) Positions(ctx context.Context) ([]Position, error) {
	now := st.now()
	c := &st.positions
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.results != nil && now.Sub(c.at) < positionTTL && !now.Before(c.at) {
		return c.results, nil
	}
	today := ukrail.DateOf(now)
	local := now.In(ukrail.London).Format("2006-01-02 15:04:05")
	rows, err := st.Pool.Query(ctx, `
		SELECT sv.id FROM services sv JOIN schedules s ON s.id = sv.schedule_id
		WHERE sv.run_date BETWEEN $1::date - 1 AND $1::date AND NOT sv.planned_cancel
		  AND s.first_time IS NOT NULL
		  AND sv.run_date + make_interval(secs => s.first_time) <= $2::timestamp + interval '5 minutes'
		  AND sv.run_date + make_interval(secs => s.last_time) >= $2::timestamp - interval '30 minutes'`,
		today, local)
	if err != nil {
		return nil, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return nil, err
	}
	out := []Position{}
	const chunk = 1000
	for start := 0; start < len(ids); start += chunk {
		services, err := st.load(ctx, ids[start:min(start+chunk, len(ids))])
		if err != nil {
			return nil, err
		}
		for _, s := range services {
			if p, ok := s.position(now); ok {
				out = append(out, p)
			}
		}
	}
	c.at, c.results = now, out
	return out, nil
}

// eventTime is the best known time for an event: actual, then forecast,
// then booked (working) time.
func (s *Service) eventTime(actual, est *time.Time, wtt *int) *time.Time {
	switch {
	case actual != nil:
		return actual
	case est != nil:
		return est
	case wtt != nil:
		t := s.at(*wtt)
		return &t
	}
	return nil
}

func (s *Service) leaves(p *Stop) *time.Time {
	if p.IsPass() {
		return s.eventTime(p.ActualPass, p.EstPass, p.WTTPass)
	}
	return s.eventTime(p.ActualDep, p.EstDep, p.WTTDep)
}

func (s *Service) reaches(p *Stop) *time.Time {
	if p.IsPass() {
		return s.eventTime(p.ActualPass, p.EstPass, p.WTTPass)
	}
	return s.eventTime(p.ActualArr, p.EstArr, p.WTTArr)
}

func (s *Service) position(now time.Time) (Position, bool) {
	if s.Status() == StatusCancelled {
		return Position{}, false
	}
	// Only stations have coordinates.
	var located []int
	for i := range s.Stops {
		if s.Stops[i].Location.Lat != nil && !s.Stops[i].Cancelled() {
			located = append(located, i)
		}
	}
	if len(located) < 2 {
		return Position{}, false
	}
	pos := Position{Service: s, DelayMinutes: s.lastDelay()}
	for k, i := range located {
		p := &s.Stops[i]
		arr, dep := s.reaches(p), s.leaves(p)
		if k == 0 && dep != nil && now.Before(*dep) {
			// Not started yet: show it at its origin shortly before departure.
			if dep.Sub(now) > 5*time.Minute {
				return Position{}, false
			}
			pos.Lat, pos.Lon, pos.AtStation, pos.Last = *p.Location.Lat, *p.Location.Lon, true, p
			pos.Next = &s.Stops[located[1]]
			pos.Bearing = bearing(p, pos.Next)
			return pos, true
		}
		// Standing at this station.
		if arr != nil && dep != nil && !now.Before(*arr) && now.Before(*dep) {
			pos.Lat, pos.Lon, pos.AtStation, pos.Last = *p.Location.Lat, *p.Location.Lon, true, p
			if k+1 < len(located) {
				pos.Next = &s.Stops[located[k+1]]
				pos.Bearing = bearing(p, pos.Next)
			}
			return pos, true
		}
		if k+1 == len(located) {
			// Arrived at the last station: shown briefly, then gone.
			if arr != nil && now.Sub(*arr) < 2*time.Minute && !now.Before(*arr) {
				pos.Lat, pos.Lon, pos.AtStation, pos.Last = *p.Location.Lat, *p.Location.Lon, true, p
				return pos, true
			}
			return Position{}, false
		}
		next := &s.Stops[located[k+1]]
		nextArr := s.reaches(next)
		if dep == nil || nextArr == nil || now.Before(*dep) || !now.Before(*nextArr) {
			continue
		}
		// Between this station and the next.
		frac := float64(now.Sub(*dep)) / float64(nextArr.Sub(*dep))
		frac = math.Max(0, math.Min(1, frac))
		pos.Lat = *p.Location.Lat + (*next.Location.Lat-*p.Location.Lat)*frac
		pos.Lon = *p.Location.Lon + (*next.Location.Lon-*p.Location.Lon)*frac
		pos.Last, pos.Next = p, next
		pos.Bearing = bearing(p, next)
		return pos, true
	}
	return Position{}, false
}

// lastDelay is how late the train was at its most recent reported event.
func (s *Service) lastDelay() *int {
	var d *int
	for i := range s.Stops {
		p := &s.Stops[i]
		for _, ev := range []struct {
			actual *time.Time
			wtt    *int
		}{{p.ActualArr, p.WTTArr}, {p.ActualPass, p.WTTPass}, {p.ActualDep, p.WTTDep}} {
			if ev.actual != nil && ev.wtt != nil {
				m := int(ev.actual.Sub(s.at(*ev.wtt)).Round(time.Minute).Minutes())
				d = &m
			}
		}
	}
	return d
}

// bearing is the initial compass bearing from one station to another.
func bearing(a, b *Stop) float64 {
	if a == nil || b == nil || a.Location.Lat == nil || b.Location.Lat == nil {
		return 0
	}
	φ1, φ2 := *a.Location.Lat*math.Pi/180, *b.Location.Lat*math.Pi/180
	Δλ := (*b.Location.Lon - *a.Location.Lon) * math.Pi / 180
	y := math.Sin(Δλ) * math.Cos(φ2)
	x := math.Cos(φ1)*math.Sin(φ2) - math.Sin(φ1)*math.Cos(φ2)*math.Cos(Δλ)
	return math.Mod(math.Atan2(y, x)*180/math.Pi+360, 360)
}

// Stations returns every station with coordinates.
func (st *Store) Stations(ctx context.Context) ([]Location, error) {
	rows, err := st.Pool.Query(ctx, `
		SELECT DISTINCT ON (crs) tiploc, crs, COALESCE(stanox, ''), COALESCE(name, initcap(tps_description), tiploc), lat, lon
		FROM locations WHERE crs IS NOT NULL AND lat IS NOT NULL
		ORDER BY crs, (name_source = 'darwin') DESC NULLS LAST, tiploc`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Location, error) {
		var l Location
		err := r.Scan(&l.TIPLOC, &l.CRS, &l.STANOX, &l.Name, &l.Lat, &l.Lon)
		return l, err
	})
}
