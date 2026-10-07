package api

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestCache(t *testing.T) {
	ctx := context.Background()
	clock := time.Date(2026, 10, 7, 18, 0, 0, 0, time.UTC)
	c := NewCache()
	c.now = func() time.Time { return clock }
	var calls atomic.Int32
	compute := func(context.Context) (any, error) { return calls.Add(1), nil }
	spec := cacheSpec{TTL: 3 * time.Second, Topics: []string{"station:CLJ", "station:CLPHMJC"}}
	get := func(key string) string {
		t.Helper()
		e, err := c.get(ctx, key, spec, compute)
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(string(e.body))
	}

	first, second := get("/CLJ"), get("/CLJ")
	if first != "1" || second != "1" {
		t.Fatal("second request recomputed")
	}
	if get("/WAT") != "2" {
		t.Fatal("different page shared an entry")
	}

	// Expires after its TTL.
	clock = clock.Add(3 * time.Second)
	if get("/CLJ") != "3" {
		t.Fatal("served after TTL")
	}

	// A change to any of its topics drops it at once.
	clock = clock.Add(time.Second)
	c.Invalidate([]string{"map", "train:W12345|2026-10-07", "station:CLPHMJC"})
	clock = clock.Add(time.Millisecond)
	if get("/CLJ") != "4" {
		t.Fatal("served after its station changed")
	}
	if get("/CLJ") != "4" {
		t.Fatal("not cached again after the change")
	}
	// Changes elsewhere don't.
	c.Invalidate([]string{"station:PAD"})
	if get("/CLJ") != "4" {
		t.Fatal("dropped by another station's change")
	}

	// A page computed before a change but stored after it is stale.
	clock = clock.Add(time.Second)
	rdg := cacheSpec{TTL: time.Minute, Topics: []string{"station:RDG"}}
	c.get(ctx, "/RDG", rdg, func(context.Context) (any, error) {
		c.Invalidate([]string{"station:RDG"})
		return "old", nil
	})
	if e, _ := c.get(ctx, "/RDG", rdg, func(context.Context) (any, error) { return "new", nil }); !strings.Contains(string(e.body), "new") {
		t.Fatalf("served %s, computed before the change", e.body)
	}

	// Errors aren't cached.
	boom := errors.New("boom")
	if _, err := c.get(ctx, "/EUS", spec, func(context.Context) (any, error) { return nil, boom }); err != boom {
		t.Fatalf("err = %v", err)
	}
	if _, err := c.get(ctx, "/EUS", spec, compute); err != nil {
		t.Fatal("error was cached")
	}
}

// Viewers asking at once share one computation, and one leaving doesn't
// cancel it for the rest.
func TestCacheShared(t *testing.T) {
	c := NewCache()
	var calls atomic.Int32
	release := make(chan struct{})
	compute := func(ctx context.Context) (any, error) {
		calls.Add(1)
		<-release
		return "board", ctx.Err()
	}
	spec := cacheSpec{TTL: 3 * time.Second}
	gone, leave := context.WithCancel(context.Background())
	first := make(chan error, 1)
	go func() {
		_, err := c.get(gone, "/CLJ", spec, compute)
		first <- err
	}()
	for calls.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	var wg sync.WaitGroup
	results := make(chan string, 20)
	for range 20 {
		wg.Go(func() {
			e, err := c.get(context.Background(), "/CLJ", spec, compute)
			if err != nil {
				t.Error(err)
				return
			}
			results <- strings.TrimSpace(string(e.body))
		})
	}
	leave()
	if err := <-first; !errors.Is(err, context.Canceled) {
		t.Errorf("leaver got %v", err)
	}
	time.Sleep(10 * time.Millisecond)
	close(release)
	wg.Wait()
	close(results)
	for b := range results {
		if b != `"board"` {
			t.Errorf("got %s", b)
		}
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("computed %d times", n)
	}
}

// Responses go out gzipped to clients that accept it, compressed once.
func TestCacheServe(t *testing.T) {
	c := NewCache()
	big := strings.Repeat("Clapham Junction ", 200)
	spec := cacheSpec{TTL: time.Minute, Header: http.Header{
		"Content-Type":  {"application/geo+json"},
		"Cache-Control": {"public, max-age=15"},
	}}
	var calls atomic.Int32
	serve := func(path, accept string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", path, nil)
		if accept != "" {
			r.Header.Set("Accept-Encoding", accept)
		}
		w := httptest.NewRecorder()
		c.serve(w, r, spec, func(context.Context) (any, error) {
			calls.Add(1)
			if strings.HasPrefix(path, "/missing") {
				return nil, &statusError{http.StatusNotFound, "service not found"}
			}
			return big, nil
		})
		return w
	}

	w := serve("/big", "br, gzip;q=0.8")
	if w.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("not gzipped: %v", w.Header())
	}
	zr, err := gzip.NewReader(w.Body)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(zr)
	if !bytes.Contains(got, []byte("Clapham Junction")) {
		t.Errorf("gzip body = %.40q", got)
	}
	if w.Header().Get("Content-Type") != "application/geo+json" || w.Header().Get("Cache-Control") != "public, max-age=15" ||
		w.Header().Get("Vary") != "Accept-Encoding" {
		t.Errorf("headers %v", w.Header())
	}

	for _, accept := range []string{"", "identity", "gzip;q=0"} {
		w = serve("/big", accept)
		if w.Header().Get("Content-Encoding") != "" || !strings.Contains(w.Body.String(), "Clapham Junction") {
			t.Errorf("Accept-Encoding %q: got %v", accept, w.Header())
		}
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("computed %d times, want 1", n)
	}

	// Error statuses pass through and aren't cached.
	for range 2 {
		if w := serve("/missing", "gzip"); w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), "service not found") {
			t.Errorf("got %d %s", w.Code, w.Body)
		}
	}
	if n := calls.Load(); n != 3 {
		t.Errorf("computed %d times, want 3", n)
	}

	// A nil cache still serves.
	var none *Cache
	r := httptest.NewRequest("GET", "/x", nil)
	rec := httptest.NewRecorder()
	none.serve(rec, r, cacheSpec{}, func(context.Context) (any, error) { return "ok", nil })
	if rec.Body.String() != "\"ok\"\n" || rec.Header().Get("Content-Type") != "application/json" {
		t.Errorf("nil cache: %q %v", rec.Body, rec.Header())
	}
}
