// Package live tells connected browsers what has changed, so they refetch a
// board or a train only when there is something new to show.
//
// Feed handlers report each service they write (by database ID, train UID
// or Darwin RID) and each station whose messages change. Every flush the hub
// turns those into topics and sends each WebSocket client the ones it
// subscribed to:
//
//	station:<CRS or TIPLOC>  a board at that station may have changed
//	train:<UID>|<YYYY-MM-DD> that train's page may have changed
//	map                      train positions may have moved
//
// Messages carry topics, not data: clients refetch through the REST API, so
// there is one source of truth for what a board shows.
package live

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"sync"
	"time"

	"github.com/coder/websocket"
)

const (
	// maxTopics bounds one client's subscriptions.
	maxTopics = 64
	// sendBuffer is how many messages may wait for a slow client before it
	// is disconnected; it reconnects and refetches everything.
	sendBuffer = 16
	pingEvery  = 25 * time.Second
	writeLimit = 10 * time.Second
)

var topicPattern = regexp.MustCompile(`^(map|station:[A-Z0-9]{3,7}|train:[A-Z0-9]{6}\|\d{4}-\d{2}-\d{2})$`)

// Changes is what feed handlers reported between two flushes.
type Changes struct {
	Services []int64
	UIDs     []string
	RIDs     []string
}

func (c Changes) empty() bool { return len(c.Services)+len(c.UIDs)+len(c.RIDs) == 0 }

// Resolver turns changed services into the station and train topics they
// touch. NewResolver reads them from the database.
type Resolver func(ctx context.Context, c Changes) ([]string, error)

// Hub collects changes and fans them out. The zero value is not usable; use
// New. All reporting methods are safe on a nil *Hub, so handlers can be run
// without one.
type Hub struct {
	Resolve Resolver
	// Interval is how often changes are flushed to clients.
	Interval time.Duration
	// Fallback is how often, in seconds, clients should still poll while
	// connected, to catch what pushes can't (on-demand Darwin Lite boards).
	Fallback int
	// OnChange, if set, receives every flush's changes whether or not any
	// browser is connected; Live Activity pushes start here. It must not
	// block.
	OnChange func(Changes)
	// OnTopics, if set, receives the topics of each flush that reaches
	// browsers, before they are told: cached responses for those topics
	// must go first, or a browser refetching on the news would be served
	// the old copy. It must not block.
	OnTopics func([]string)

	mu       sync.Mutex
	services map[int64]struct{}
	uids     map[string]struct{}
	rids     map[string]struct{}
	direct   map[string]struct{}
	clients  map[*client]struct{}
}

// New returns a hub that resolves changes with r.
func New(r Resolver) *Hub {
	return &Hub{
		Resolve:  r,
		Interval: 2 * time.Second,
		Fallback: 120,
		services: map[int64]struct{}{},
		uids:     map[string]struct{}{},
		rids:     map[string]struct{}{},
		direct:   map[string]struct{}{},
		clients:  map[*client]struct{}{},
	}
}

// Service reports that a service's times, platforms or status changed.
func (h *Hub) Service(id int64) {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.services[id] = struct{}{}
	h.mu.Unlock()
}

// UID reports that the schedules for a train UID changed (VSTP).
func (h *Hub) UID(uid string) {
	if h == nil || uid == "" {
		return
	}
	h.mu.Lock()
	h.uids[uid] = struct{}{}
	h.mu.Unlock()
}

// RID reports a change keyed by Darwin RID, such as an association.
func (h *Hub) RID(rid string) {
	if h == nil || rid == "" {
		return
	}
	h.mu.Lock()
	h.rids[rid] = struct{}{}
	h.mu.Unlock()
}

// Stations reports that the boards at these stations changed directly, as
// when a station message is added or withdrawn.
func (h *Hub) Stations(codes ...string) {
	if h == nil {
		return
	}
	h.mu.Lock()
	for _, c := range codes {
		if c != "" {
			h.direct["station:"+c] = struct{}{}
		}
	}
	h.mu.Unlock()
}

// Run flushes changes every Interval until ctx ends, then disconnects every
// client so the server can shut down.
func (h *Hub) Run(ctx context.Context) {
	t := time.NewTicker(h.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			h.mu.Lock()
			for c := range h.clients {
				go c.conn.Close(websocket.StatusGoingAway, "server shutting down")
			}
			h.mu.Unlock()
			return
		case <-t.C:
			h.Flush(ctx)
		}
	}
}

// Flush sends the changes gathered since the last flush.
func (h *Hub) Flush(ctx context.Context) {
	h.mu.Lock()
	ch := Changes{Services: keys(h.services), UIDs: keys(h.uids), RIDs: keys(h.rids)}
	topics := h.direct
	clients := make([]*client, 0, len(h.clients))
	for c := range h.clients {
		clients = append(clients, c)
	}
	h.services, h.uids, h.rids, h.direct = map[int64]struct{}{}, map[string]struct{}{}, map[string]struct{}{}, map[string]struct{}{}
	h.mu.Unlock()

	if !ch.empty() && h.OnChange != nil {
		h.OnChange(ch)
	}
	if len(clients) == 0 || (ch.empty() && len(topics) == 0) {
		return
	}
	if !ch.empty() {
		topics["map"] = struct{}{}
		// Only ask the database when someone is watching a board or a train.
		if wantsMoreThanMap(clients) && h.Resolve != nil {
			resolved, err := h.Resolve(ctx, ch)
			if err != nil && ctx.Err() == nil {
				slog.Warn("live: resolving changes failed", "err", err)
			}
			for _, t := range resolved {
				topics[t] = struct{}{}
			}
		}
	}
	if h.OnTopics != nil {
		h.OnTopics(keys(topics))
	}
	for _, c := range clients {
		if hit := c.matching(topics); len(hit) > 0 {
			c.offer(message{Type: "changed", Topics: hit})
		}
	}
}

// Clients reports how many browsers are connected.
func (h *Hub) Clients() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.clients)
}

func wantsMoreThanMap(clients []*client) bool {
	for _, c := range clients {
		c.mu.Lock()
		n := len(c.topics)
		_, onlyMap := c.topics["map"]
		c.mu.Unlock()
		if n > 1 || (n == 1 && !onlyMap) {
			return true
		}
	}
	return false
}

func keys[K comparable](m map[K]struct{}) []K {
	out := make([]K, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// message is the wire format in both directions.
type message struct {
	Type     string   `json:"type"`
	Topics   []string `json:"topics,omitempty"`
	Fallback int      `json:"fallback,omitempty"`
	Message  string   `json:"message,omitempty"`
}

type client struct {
	conn *websocket.Conn
	send chan message

	mu     sync.Mutex
	topics map[string]struct{}
}

func (c *client) matching(topics map[string]struct{}) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var hit []string
	for t := range c.topics {
		if _, ok := topics[t]; ok {
			hit = append(hit, t)
		}
	}
	return hit
}

// offer queues a message without waiting. A client that has fallen this
// far behind is disconnected; it reconnects and refetches everything.
func (c *client) offer(m message) {
	select {
	case c.send <- m:
	default:
		// Close waits for the handshake, so it must not hold up the flush.
		go c.conn.Close(websocket.StatusTryAgainLater, "too far behind")
	}
}

// ServeHTTP upgrades the request to a WebSocket and serves one client until
// it disconnects. Only same-origin pages may connect.
func (h *Hub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return // Accept has already written the error response
	}
	conn.SetReadLimit(8 << 10)
	c := &client{conn: conn, send: make(chan message, sendBuffer), topics: map[string]struct{}{}}
	c.send <- message{Type: "hello", Fallback: h.Fallback}

	h.mu.Lock()
	h.clients[c] = struct{}{}
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		delete(h.clients, c)
		h.mu.Unlock()
	}()

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	go c.write(ctx, cancel)
	err = c.read(ctx)
	if s := websocket.CloseStatus(err); s == -1 && !errors.Is(err, context.Canceled) {
		slog.Debug("live: client read ended", "err", err)
	}
	conn.CloseNow()
}

func (c *client) read(ctx context.Context) error {
	for {
		_, data, err := c.conn.Read(ctx)
		if err != nil {
			return err
		}
		var m message
		if json.Unmarshal(data, &m) != nil {
			c.offer(message{Type: "error", Message: "messages must be JSON"})
			continue
		}
		switch m.Type {
		case "subscribe":
			c.subscribe(m.Topics)
		case "unsubscribe":
			c.mu.Lock()
			for _, t := range m.Topics {
				delete(c.topics, t)
			}
			c.mu.Unlock()
		default:
			c.offer(message{Type: "error", Message: "unknown message type " + m.Type})
		}
	}
}

func (c *client) subscribe(topics []string) {
	c.mu.Lock()
	var bad []string
	full := false
	for _, t := range topics {
		switch {
		case !topicPattern.MatchString(t):
			bad = append(bad, t)
		case len(c.topics) >= maxTopics:
			full = true
		default:
			c.topics[t] = struct{}{}
		}
	}
	c.mu.Unlock()
	if len(bad) > 0 {
		c.offer(message{Type: "error", Message: "unknown topics", Topics: bad})
	}
	if full {
		c.offer(message{Type: "error", Message: "too many topics; unsubscribe from some first"})
	}
}

func (c *client) write(ctx context.Context, cancel context.CancelFunc) {
	defer cancel()
	ping := time.NewTicker(pingEvery)
	defer ping.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case m := <-c.send:
			b, _ := json.Marshal(m)
			wctx, done := context.WithTimeout(ctx, writeLimit)
			err := c.conn.Write(wctx, websocket.MessageText, b)
			done()
			if err != nil {
				return
			}
		case <-ping.C:
			pctx, done := context.WithTimeout(ctx, writeLimit)
			err := c.conn.Ping(pctx)
			done()
			if err != nil {
				return
			}
		}
	}
}
