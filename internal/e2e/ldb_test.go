package e2e

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/carbonarok/trackside/internal/api"
	"github.com/carbonarok/trackside/internal/compat"
	"github.com/carbonarok/trackside/internal/darwin"
	"github.com/carbonarok/trackside/internal/ldb"
	"github.com/carbonarok/trackside/internal/timetable"
	"github.com/carbonarok/trackside/internal/ukrail"
)

// TestDarwinLite checks that on-demand Darwin Lite data is matched to the
// right trains and merged into boards and service detail.
func TestDarwinLite(t *testing.T) {
	board, _ := os.ReadFile("../../testdata/ldb_board.xml")
	details, _ := os.ReadFile("../../testdata/ldb_service.xml")
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		if strings.HasSuffix(r.Header.Get("SOAPAction"), `GetServiceDetails"`) {
			w.Write(details)
			return
		}
		w.Write(board)
	}))
	defer fake.Close()
	lite := ldb.New("test-token")
	lite.URL = fake.URL
	lite.Now = func() time.Time { return time.Date(2026, 10, 6, 8, 0, 0, 0, ukrail.London) }

	pool, srv := setup(t, func(st *timetable.Store) { st.Live = lite })
	ctx := context.Background()

	// The Darwin stream already covers W10004; it must win over Darwin Lite.
	body, err := os.ReadFile("../../testdata/darwin_ts.xml")
	if err != nil {
		t.Fatal(err)
	}
	if err := (&darwin.Applier{Pool: pool}).ApplyMessage(ctx, body); err != nil {
		t.Fatal(err)
	}

	var b api.Board
	get(t, srv, "/v1/locations/CLJ/departures?at=2026-10-06T08:00&window=60", &b)
	if got := uids(board2(b)); got != "W10001,W10003,W10004" {
		t.Fatalf("services = %s", got)
	}
	w1, w3, w4 := b.Services[0], b.Services[1], b.Services[2]
	// W10001 matched to the SW train, not the Southern decoy at the same minute.
	if w1.Stop.Departure.Estimated == nil || w1.Stop.Departure.Estimated.Format("15:04") != "08:10" ||
		w1.Stop.Platform == nil || w1.Stop.Platform.Actual != "8" {
		t.Errorf("W10001 stop = %+v %+v", w1.Stop.Departure, w1.Stop.Platform)
	}
	if w1.LateReasonText != "This service has been delayed by a signalling fault" {
		t.Errorf("W10001 late reason = %q", w1.LateReasonText)
	}
	if !w3.Stop.Cancelled {
		t.Errorf("W10003 should be cancelled")
	}
	if w4.Stop.Departure.Estimated == nil || w4.Stop.Departure.Estimated.Format("15:04") != "08:41" || w4.Stop.Departure.Delayed {
		t.Errorf("Darwin stream forecast for W10004 should win over Lite's 'Delayed': %+v", w4.Stop.Departure)
	}
	if len(b.Messages) != 1 || b.Messages[0].Text != "Lifts are out of order. More in the Disruptions area." {
		t.Errorf("messages = %+v", b.Messages)
	}

	// The board bound W10001 to its Darwin Lite service, so detail is live
	// along the route.
	var d api.ServiceDetail
	get(t, srv, "/v1/services/W10001/2026-10-06", &d)
	wat, wim, wok := d.Stops[0], d.Stops[3], d.Stops[4]
	if wat.Departure.Actual == nil || wat.Departure.Actual.Format("15:04") != "08:03" {
		t.Errorf("WAT actual = %+v", wat.Departure)
	}
	if wim.Departure.Estimated == nil || wim.Departure.Estimated.Format("15:04") != "08:16" {
		t.Errorf("WIM estimate = %+v", wim.Departure)
	}
	if wok.Arrival.Estimated == nil || wok.Arrival.Estimated.Format("15:04") != "08:39" {
		t.Errorf("WOK estimate = %+v", wok.Arrival)
	}

	// The compat API gets the same data.
	var res struct {
		Services []struct {
			ServiceUID     string                `json:"serviceUid"`
			LocationDetail compat.LocationDetail `json:"locationDetail"`
		} `json:"services"`
	}
	get(t, srv, "/api/v1/json/search/CLJ/2026/10/06/0800", &res)
	if ld := res.Services[0].LocationDetail; res.Services[0].ServiceUID != "W10001" || ld.RealtimeDeparture != "0810" || ld.Platform != "8" {
		t.Errorf("compat W10001 = %+v", ld)
	}
}

// board2 adapts api.Board to the helper used by the main test.
func board2(b api.Board) board {
	return board{Location: b.Location, Services: b.Services}
}
