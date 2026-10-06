package ldb

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/carbonarok/trackside/internal/ukrail"
)

// fakeServer serves the fixture responses and counts requests.
func fakeServer(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	board, err := os.ReadFile("../../testdata/ldb_board.xml")
	if err != nil {
		t.Fatal(err)
	}
	details, err := os.ReadFile("../../testdata/ldb_service.xml")
	if err != nil {
		t.Fatal(err)
	}
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), "<typ:TokenValue>test-token</typ:TokenValue>") {
			w.WriteHeader(http.StatusInternalServerError)
			io.WriteString(w, `<soap:Envelope xmlns:soap="http://schemas.xmlsoap.org/soap/envelope/"><soap:Body><soap:Fault><faultstring>Unauthorized</faultstring></soap:Fault></soap:Body></soap:Envelope>`)
			return
		}
		switch r.Header.Get("SOAPAction") {
		case `"http://thalesgroup.com/RTTI/2012-01-13/ldb/GetArrivalDepartureBoard"`:
			w.Write(board)
		case `"http://thalesgroup.com/RTTI/2012-01-13/ldb/GetServiceDetails"`:
			w.Write(details)
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &n
}

var eight = time.Date(2026, 10, 6, 8, 0, 0, 0, ukrail.London)

func client(srv *httptest.Server) *Client {
	c := New("test-token")
	c.URL = srv.URL
	c.Now = func() time.Time { return eight }
	return c
}

func TestBoardParsesAndCaches(t *testing.T) {
	srv, n := fakeServer(t)
	c := client(srv)
	ctx := context.Background()
	b, err := c.Board(ctx, "clj", eight, eight.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Services) != 4 {
		t.Fatalf("services = %d", len(b.Services))
	}
	s := b.Services[1]
	if s.ServiceID != "LITE10001CLJ_" || s.STD != "08:07" || s.ETD != "08:10" || s.STA != "08:06" ||
		s.Platform != "8" || s.OperatorCode != "SW" || s.OriginCRS[0] != "WAT" || s.DestinationCRS[0] != "WOK" ||
		s.DelayReason == "" {
		t.Errorf("service = %+v", s)
	}
	if !b.Services[2].Cancelled || b.Services[2].CancelReason == "" {
		t.Errorf("cancelled service = %+v", b.Services[2])
	}
	msgs := c.Messages("CLJ")
	if len(msgs) != 1 || msgs[0].Text != "Lifts are out of order. More in the Disruptions area." ||
		!strings.Contains(msgs[0].HTML, `<a href="https://www.nationalrail.co.uk/">`) {
		t.Errorf("messages = %+v", msgs)
	}

	// A request a minute later for nearly the same window comes from cache.
	if _, err := c.Board(ctx, "CLJ", eight.Add(2*time.Minute), eight.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got := n.Load(); got != 1 {
		t.Errorf("requests = %d, want 1 (cached)", got)
	}
}

func TestBoardOutsideCoverageMakesNoRequest(t *testing.T) {
	srv, n := fakeServer(t)
	c := client(srv)
	for _, from := range []time.Time{eight.Add(-5 * time.Hour), eight.Add(5 * time.Hour)} {
		b, err := c.Board(context.Background(), "CLJ", from, from.Add(time.Hour))
		if err != nil || b != nil {
			t.Errorf("board at %v = %v, %v", from, b, err)
		}
	}
	if b, _ := c.Board(context.Background(), "notacrs", eight, eight.Add(time.Hour)); b != nil {
		t.Error("invalid CRS requested")
	}
	if n.Load() != 0 {
		t.Errorf("requests = %d, want 0", n.Load())
	}
}

func TestServiceNeedsBinding(t *testing.T) {
	srv, _ := fakeServer(t)
	c := client(srv)
	ctx := context.Background()
	run := time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC)
	if s, err := c.Service(ctx, "W10001", run); s != nil || err != nil {
		t.Fatalf("unbound service = %v, %v", s, err)
	}
	c.Bind("LITE10001CLJ_", "W10001", run)
	s, err := c.Service(ctx, "W10001", run)
	if err != nil {
		t.Fatal(err)
	}
	// WAT, CLJ arrival, CLJ departure, WIM, WOK.
	if len(s.Calls) != 5 || s.Calls[0].AT != "08:03" || s.Calls[2].ET != "08:10" || s.Calls[4].ET != "08:39" {
		t.Errorf("calls = %+v", s.Calls)
	}
}

func TestBudgetAndErrors(t *testing.T) {
	srv, _ := fakeServer(t)
	c := client(srv)
	c.HourlyBudget = 1
	ctx := context.Background()
	if _, err := c.Board(ctx, "CLJ", eight, eight.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Board(ctx, "WAT", eight, eight.Add(time.Hour)); !errors.Is(err, ErrBudget) {
		t.Errorf("second request = %v, want ErrBudget", err)
	}

	bad := client(srv)
	bad.Token = "wrong"
	if _, err := bad.Board(ctx, "CLJ", eight, eight.Add(time.Hour)); err == nil || !strings.Contains(err.Error(), "Unauthorized") {
		t.Errorf("bad token error = %v", err)
	}
}
