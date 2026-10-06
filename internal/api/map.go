package api

import (
	"net/http"
	"time"

	"github.com/carbonarok/trackside/internal/ukrail"
)

func (s *Server) registerMap(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/map/trains", s.mapTrains)
	mux.HandleFunc("GET /v1/map/stations", s.mapStations)
}

// PointGeometry is a GeoJSON point: [longitude, latitude].
type PointGeometry struct {
	Type        string     `json:"type"`
	Coordinates [2]float64 `json:"coordinates"`
}

func point(lat, lon float64) PointGeometry {
	return PointGeometry{Type: "Point", Coordinates: [2]float64{round6(lon), round6(lat)}}
}

func round6(f float64) float64 { return float64(int64(f*1e6+0.5*sign(f))) / 1e6 }

func sign(f float64) float64 {
	if f < 0 {
		return -1
	}
	return 1
}

// TrainPosition describes a train on the map.
type TrainPosition struct {
	UID         string    `json:"uid"`
	RunDate     string    `json:"runDate"`
	Headcode    string    `json:"headcode,omitempty"`
	Operator    *Operator `json:"operator,omitempty"`
	Origin      string    `json:"origin"`
	Destination string    `json:"destination"`
	// Last is the station the train is at or has just left; Next the one
	// it is heading for.
	Last      *Location `json:"last,omitempty"`
	Next      *Location `json:"next,omitempty"`
	AtStation bool      `json:"atStation"`
	// Bearing is the direction of travel in degrees from north.
	Bearing      float64 `json:"bearing"`
	DelayMinutes *int    `json:"delayMinutes,omitempty"`
	// Live is false when nothing has been reported and the position comes
	// from the timetable alone.
	Live        bool `json:"live"`
	IsPassenger bool `json:"isPassenger"`
}

// TrainFeature is a GeoJSON feature for one train.
type TrainFeature struct {
	Type       string        `json:"type"`
	Geometry   PointGeometry `json:"geometry"`
	Properties TrainPosition `json:"properties"`
}

// TrainMap is a GeoJSON FeatureCollection of train positions.
type TrainMap struct {
	Type        string         `json:"type"`
	GeneratedAt time.Time      `json:"generatedAt"`
	Features    []TrainFeature `json:"features"`
}

func (s *Server) mapTrains(w http.ResponseWriter, r *http.Request) {
	all := r.URL.Query().Get("all") == "true"
	positions, err := s.Store.Positions(r.Context())
	if err != nil {
		serverError(w, err)
		return
	}
	out := TrainMap{Type: "FeatureCollection", GeneratedAt: s.now().In(ukrail.London), Features: []TrainFeature{}}
	for _, p := range positions {
		svc := p.Service
		passenger := ukrail.IsPassenger(svc.TrainStatus, svc.Category)
		if !passenger && !all {
			continue
		}
		props := TrainPosition{
			UID:          svc.UID,
			RunDate:      svc.RunDate.Format(time.DateOnly),
			Headcode:     svc.Headcode,
			Origin:       svc.Stops[0].Location.Name,
			Destination:  svc.Stops[len(svc.Stops)-1].Location.Name,
			AtStation:    p.AtStation,
			Bearing:      float64(int(p.Bearing + 0.5)),
			DelayMinutes: p.DelayMinutes,
			Live:         p.DelayMinutes != nil,
			IsPassenger:  passenger,
		}
		if svc.ATOCCode != "" && svc.ATOCCode != "ZZ" {
			name := svc.OperatorName
			if name == "" {
				name = ukrail.OperatorName(svc.ATOCCode)
			}
			props.Operator = &Operator{Code: svc.ATOCCode, Name: name}
		}
		if p.Last != nil {
			l := toLocation(p.Last.Location)
			props.Last = &l
		}
		if p.Next != nil {
			l := toLocation(p.Next.Location)
			props.Next = &l
		}
		out.Features = append(out.Features, TrainFeature{Type: "Feature", Geometry: point(p.Lat, p.Lon), Properties: props})
	}
	w.Header().Set("Cache-Control", "public, max-age=15")
	w.Header().Set("Content-Type", "application/geo+json")
	writeJSON(w, out)
}

// StationFeature is a GeoJSON feature for one station.
type StationFeature struct {
	Type       string        `json:"type"`
	Geometry   PointGeometry `json:"geometry"`
	Properties Location      `json:"properties"`
}

// StationMap is a GeoJSON FeatureCollection of stations.
type StationMap struct {
	Type     string           `json:"type"`
	Features []StationFeature `json:"features"`
}

func (s *Server) mapStations(w http.ResponseWriter, r *http.Request) {
	stations, err := s.Store.Stations(r.Context())
	if err != nil {
		serverError(w, err)
		return
	}
	out := StationMap{Type: "FeatureCollection", Features: make([]StationFeature, 0, len(stations))}
	for _, l := range stations {
		out.Features = append(out.Features, StationFeature{Type: "Feature",
			Geometry: point(*l.Lat, *l.Lon), Properties: toLocation(l)})
	}
	w.Header().Set("Cache-Control", "public, max-age=3600")
	w.Header().Set("Content-Type", "application/geo+json")
	writeJSON(w, out)
}
