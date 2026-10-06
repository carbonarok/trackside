// Package naptan loads railway station coordinates from NaPTAN, the
// Department for Transport's open dataset of public transport stops.
// Rail station access points have ATCO codes of "9100" followed by the
// station's TIPLOC.
package naptan

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// DownloadURL returns NaPTAN's rail stops (ATCO area 910) as CSV. No
// account is needed. Contains public sector information licensed under the
// Open Government Licence v3.0.
const DownloadURL = "https://naptan.api.dft.gov.uk/v1/access-nodes?dataFormat=csv&atcoAreaCodes=910"

// Station is a rail station's position.
type Station struct {
	TIPLOC string
	Name   string
	Lat    float64
	Lon    float64
}

// Parse reads NaPTAN CSV, keeping active rail station entries.
func Parse(r io.Reader) ([]Station, error) {
	cr := csv.NewReader(r)
	cr.FieldsPerRecord = -1
	header, err := cr.Read()
	if err != nil {
		return nil, fmt.Errorf("naptan header: %w", err)
	}
	col := map[string]int{}
	for i, h := range header {
		col[strings.TrimPrefix(h, "\ufeff")] = i
	}
	for _, need := range []string{"ATCOCode", "CommonName", "Latitude", "Longitude", "StopType", "Status"} {
		if _, ok := col[need]; !ok {
			return nil, fmt.Errorf("naptan: missing column %s", need)
		}
	}
	var out []Station
	for {
		rec, err := cr.Read()
		if err == io.EOF {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		get := func(name string) string {
			if i := col[name]; i < len(rec) {
				return strings.TrimSpace(rec[i])
			}
			return ""
		}
		atco := get("ATCOCode")
		if !strings.HasPrefix(atco, "9100") || get("StopType") != "RLY" || get("Status") == "inactive" {
			continue
		}
		lat, err1 := strconv.ParseFloat(get("Latitude"), 64)
		lon, err2 := strconv.ParseFloat(get("Longitude"), 64)
		if err1 != nil || err2 != nil || lat == 0 || lon == 0 {
			continue
		}
		out = append(out, Station{
			TIPLOC: strings.TrimPrefix(atco, "9100"),
			Name:   strings.TrimSuffix(get("CommonName"), " Rail Station"),
			Lat:    lat,
			Lon:    lon,
		})
	}
}

// Download fetches the NaPTAN rail stops.
func Download(ctx context.Context) (io.ReadCloser, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, DownloadURL, nil)
	if err != nil {
		cancel()
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		cancel()
		return nil, fmt.Errorf("naptan download: %s", resp.Status)
	}
	return struct {
		io.Reader
		io.Closer
	}{resp.Body, closeFunc(func() error { cancel(); return resp.Body.Close() })}, nil
}

type closeFunc func() error

func (f closeFunc) Close() error { return f() }

// Load stores coordinates against TIPLOCs, then copies them to other
// TIPLOCs at the same station that NaPTAN doesn't list, such as the
// platform-group TIPLOCs of large stations. Returns how many locations
// have coordinates afterwards.
func Load(ctx context.Context, pool *pgxpool.Pool, stations []Station) (int, error) {
	b := &pgx.Batch{}
	for _, s := range stations {
		b.Queue(`UPDATE locations SET lat = $2, lon = $3 WHERE tiploc = $1`, s.TIPLOC, s.Lat, s.Lon)
	}
	if err := pool.SendBatch(ctx, b).Close(); err != nil {
		return 0, err
	}
	// Spread coordinates to TIPLOCs at the same place: same CRS or same
	// STANOX. Twice, so a TIPLOC linked by STANOX to one linked by CRS is
	// reached too. Names are not used: they repeat (two Newports).
	for i := 0; i < 2; i++ {
		for _, col := range []string{"crs", "stanox"} {
			if _, err := pool.Exec(ctx, `
				UPDATE locations l SET lat = s.lat, lon = s.lon
				FROM (SELECT DISTINCT ON (`+col+`) `+col+` AS k, lat, lon FROM locations
				      WHERE `+col+` IS NOT NULL AND lat IS NOT NULL ORDER BY `+col+`, tiploc) s
				WHERE l.`+col+` = s.k AND l.lat IS NULL`); err != nil {
				return 0, err
			}
		}
	}
	var n int
	err := pool.QueryRow(ctx, `SELECT count(*) FROM locations WHERE lat IS NOT NULL`).Scan(&n)
	return n, err
}
