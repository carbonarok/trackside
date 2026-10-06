package e2e

import (
	"context"
	"net/http"
	"testing"

	"github.com/carbonarok/trackside/internal/api"
)

func TestTrainMap(t *testing.T) {
	pool, srv := setup(t)
	ctx := context.Background()
	// The fixtures have no coordinates; place the stations on a line.
	for tiploc, latlon := range map[string][2]float64{
		"WATRLMN": {51.503, -0.113}, "CLPHMJN": {51.464, -0.170}, "WIMBLDN": {51.421, -0.206}, "WOKING": {51.318, -0.557},
	} {
		if _, err := pool.Exec(ctx, `UPDATE locations SET lat = $2, lon = $3 WHERE tiploc = $1`, tiploc, latlon[0], latlon[1]); err != nil {
			t.Fatal(err)
		}
	}
	// The clock is 08:00, W10001's booked departure: it is just leaving
	// Waterloo, heading south-west for Clapham Junction.
	var m api.TrainMap
	get(t, srv, "/v1/map/trains", &m)
	var w1 *api.TrainFeature
	for i := range m.Features {
		if m.Features[i].Properties.UID == "W10001" {
			w1 = &m.Features[i]
		}
	}
	if m.Type != "FeatureCollection" || w1 == nil {
		t.Fatalf("map = %+v", m)
	}
	p := w1.Properties
	if p.Last == nil || p.Last.TIPLOC != "WATRLMN" || p.Next == nil || p.Next.TIPLOC != "CLPHMJN" ||
		w1.Geometry.Coordinates != [2]float64{-0.113, 51.503} || p.Bearing < 180 || p.Bearing > 270 {
		t.Errorf("W10001 = %+v at %v", p, w1.Geometry.Coordinates)
	}

	resp, err := http.Get(srv.URL + "/v1/map/stations")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "application/geo+json" {
		t.Errorf("content type = %q", ct)
	}
	page, err := http.Get(srv.URL + "/map")
	if err != nil {
		t.Fatal(err)
	}
	page.Body.Close()
	if page.StatusCode != 200 {
		t.Errorf("/map status %d", page.StatusCode)
	}
}
