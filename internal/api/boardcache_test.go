package api

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestBoardCache(t *testing.T) {
	ctx := context.Background()
	clock := time.Date(2026, 10, 7, 18, 0, 0, 0, time.UTC)
	c := NewBoardCache(3 * time.Second)
	c.now = func() time.Time { return clock }
	var calls atomic.Int32
	compute := func(context.Context) ([]byte, error) {
		n := calls.Add(1)
		return []byte{byte('0' + n)}, nil
	}
	get := func(key string) string {
		t.Helper()
		b, err := c.get(ctx, key, []string{"station:CLJ", "station:CLPHMJC"}, compute)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}

	first, second := get("/CLJ"), get("/CLJ")
	if first != "1" || second != "1" {
		t.Fatal("second request recomputed")
	}
	if get("/WAT") != "2" {
		t.Fatal("different board shared an entry")
	}

	// Expires after TTL.
	clock = clock.Add(3 * time.Second)
	if get("/CLJ") != "3" {
		t.Fatal("served after TTL")
	}

	// A change at any of the board's stations drops it at once.
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

	// A board computed before a change but stored after it is stale.
	clock = clock.Add(time.Second)
	c.get(ctx, "/RDG", []string{"station:RDG"}, func(context.Context) ([]byte, error) {
		c.Invalidate([]string{"station:RDG"})
		return []byte("old"), nil
	})
	if b, _ := c.get(ctx, "/RDG", []string{"station:RDG"}, func(context.Context) ([]byte, error) {
		return []byte("new"), nil
	}); string(b) != "new" {
		t.Fatalf("served %q, computed before the change", b)
	}

	// Errors aren't cached.
	boom := errors.New("boom")
	if _, err := c.get(ctx, "/EUS", nil, func(context.Context) ([]byte, error) { return nil, boom }); err != boom {
		t.Fatalf("err = %v", err)
	}
	if b, _ := c.get(ctx, "/EUS", nil, compute); len(b) == 0 {
		t.Fatal("error was cached")
	}
}

// Viewers asking at once share one computation, and one leaving doesn't
// cancel it for the rest.
func TestBoardCacheShared(t *testing.T) {
	c := NewBoardCache(3 * time.Second)
	var calls atomic.Int32
	release := make(chan struct{})
	compute := func(ctx context.Context) ([]byte, error) {
		calls.Add(1)
		<-release
		return []byte("board"), ctx.Err()
	}
	gone, leave := context.WithCancel(context.Background())
	first := make(chan error, 1)
	go func() {
		_, err := c.get(gone, "/CLJ", nil, compute)
		first <- err
	}()
	for calls.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	var wg sync.WaitGroup
	results := make(chan string, 20)
	for range 20 {
		wg.Go(func() {
			b, err := c.get(context.Background(), "/CLJ", nil, compute)
			if err != nil {
				t.Error(err)
			}
			results <- string(b)
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
		if b != "board" {
			t.Errorf("got %q", b)
		}
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("computed %d times", n)
	}
}
