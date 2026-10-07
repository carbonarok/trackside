package live

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// connect starts h behind a test server and dials it, returning the
// connection after the hello message.
func connect(t *testing.T, h *Hub) *websocket.Conn {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.CloseNow() })
	if m := receive(t, conn); m.Type != "hello" || m.Fallback != h.Fallback {
		t.Fatalf("first message = %+v, want hello with fallback %d", m, h.Fallback)
	}
	return conn
}

func send(t *testing.T, conn *websocket.Conn, m message) {
	t.Helper()
	b, _ := json.Marshal(m)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := conn.Write(ctx, websocket.MessageText, b); err != nil {
		t.Fatal(err)
	}
}

func receive(t *testing.T, conn *websocket.Conn) message {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, b, err := conn.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var m message
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// subscribe sends a subscription and waits until the hub has applied it, so
// a following Flush is certain to see it.
func subscribe(t *testing.T, h *Hub, conn *websocket.Conn, topics ...string) {
	t.Helper()
	send(t, conn, message{Type: "subscribe", Topics: topics})
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		h.mu.Lock()
		for c := range h.clients {
			c.mu.Lock()
			ok := true
			for _, tp := range topics {
				if _, has := c.topics[tp]; !has {
					ok = false
				}
			}
			c.mu.Unlock()
			if ok {
				h.mu.Unlock()
				return
			}
		}
		h.mu.Unlock()
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("subscription to %v never applied", topics)
}

func TestServiceChangeReachesStationAndTrainSubscribers(t *testing.T) {
	var calls atomic.Int32
	h := New(func(ctx context.Context, c Changes) ([]string, error) {
		calls.Add(1)
		if !slices.Equal(c.Services, []int64{7}) {
			t.Errorf("resolved services %v, want [7]", c.Services)
		}
		return []string{"station:WAT", "station:CLJ", "train:W12345|2026-10-06"}, nil
	})
	board := connect(t, h)
	subscribe(t, h, board, "station:WAT", "station:EUS")
	train := connect(t, h)
	subscribe(t, h, train, "train:W12345|2026-10-06")

	h.Service(7)
	h.Service(7) // reported twice, flushed once
	h.Flush(context.Background())

	if m := receive(t, board); m.Type != "changed" || !slices.Equal(m.Topics, []string{"station:WAT"}) {
		t.Errorf("board got %+v, want changed [station:WAT]", m)
	}
	if m := receive(t, train); !slices.Equal(m.Topics, []string{"train:W12345|2026-10-06"}) {
		t.Errorf("train got %+v", m)
	}
	if calls.Load() != 1 {
		t.Errorf("resolver called %d times, want 1", calls.Load())
	}
}

func TestMapOnlySubscribersSkipTheDatabase(t *testing.T) {
	var calls atomic.Int32
	h := New(func(context.Context, Changes) ([]string, error) {
		calls.Add(1)
		return nil, nil
	})
	conn := connect(t, h)
	subscribe(t, h, conn, "map")
	h.Service(1)
	h.Flush(context.Background())
	if m := receive(t, conn); !slices.Equal(m.Topics, []string{"map"}) {
		t.Errorf("got %+v, want changed [map]", m)
	}
	if calls.Load() != 0 {
		t.Errorf("resolver called %d times with only map subscribers", calls.Load())
	}
}

func TestStationMessagesNeedNoResolving(t *testing.T) {
	h := New(nil)
	conn := connect(t, h)
	subscribe(t, h, conn, "station:PAD", "map")
	h.Stations("PAD", "RDG")
	h.Flush(context.Background())
	// No service changed, so the map is not told.
	if m := receive(t, conn); !slices.Equal(m.Topics, []string{"station:PAD"}) {
		t.Errorf("got %+v, want changed [station:PAD]", m)
	}
}

func TestUnsubscribedClientsHearNothing(t *testing.T) {
	h := New(func(context.Context, Changes) ([]string, error) { return []string{"station:WAT"}, nil })
	conn := connect(t, h)
	subscribe(t, h, conn, "station:WAT")
	send(t, conn, message{Type: "unsubscribe", Topics: []string{"station:WAT"}})
	subscribe(t, h, conn, "station:EUS") // also waits for the unsubscribe, sent first
	h.Service(1)
	h.Flush(context.Background())
	h.Stations("EUS")
	h.Flush(context.Background())
	if m := receive(t, conn); !slices.Equal(m.Topics, []string{"station:EUS"}) {
		t.Errorf("got %+v, want only the EUS change", m)
	}
}

func TestBadInputGetsAnErrorNotADisconnect(t *testing.T) {
	h := New(nil)
	conn := connect(t, h)
	send(t, conn, message{Type: "subscribe", Topics: []string{"station:WAT", "everything", "train:x"}})
	if m := receive(t, conn); m.Type != "error" || !slices.Equal(m.Topics, []string{"everything", "train:x"}) {
		t.Errorf("got %+v, want an error naming the two bad topics", m)
	}
	send(t, conn, message{Type: "shout"})
	if m := receive(t, conn); m.Type != "error" {
		t.Errorf("got %+v, want an error for an unknown type", m)
	}
	// Still connected and the good topic still applies.
	h.Stations("WAT")
	h.Flush(context.Background())
	if m := receive(t, conn); !slices.Equal(m.Topics, []string{"station:WAT"}) {
		t.Errorf("got %+v after errors", m)
	}
}

func TestTopicLimit(t *testing.T) {
	h := New(nil)
	conn := connect(t, h)
	topics := make([]string, maxTopics+1)
	for i := range topics {
		topics[i] = "station:" + string(rune('A'+i/26%26)) + string(rune('A'+i%26)) + "X"
	}
	send(t, conn, message{Type: "subscribe", Topics: topics})
	if m := receive(t, conn); m.Type != "error" || !strings.Contains(m.Message, "too many") {
		t.Errorf("got %+v, want a too-many-topics error", m)
	}
}

func TestNilHubIsSafe(t *testing.T) {
	var h *Hub
	h.Service(1)
	h.UID("W12345")
	h.RID("202610067654321")
	h.Stations("WAT")
}

func TestShutdownDisconnectsClients(t *testing.T) {
	h := New(nil)
	h.Interval = time.Hour
	conn := connect(t, h)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { h.Run(ctx); close(done) }()
	cancel()
	<-done
	rctx, rcancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer rcancel()
	if _, _, err := conn.Read(rctx); websocket.CloseStatus(err) != websocket.StatusGoingAway {
		t.Errorf("read after shutdown: %v, want going away", err)
	}
}
