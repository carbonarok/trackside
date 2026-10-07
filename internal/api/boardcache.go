package api

import (
	"context"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// BoardCache shares station board responses between viewers. An open board
// refetches whenever its station changes, which at a busy station is every
// couple of seconds, so without sharing each viewer costs a full board
// query; with it a board costs the same however many people watch it.
//
// Requests for the same board at the same moment wait for one computation.
// The result is kept for TTL, but dropped as soon as the live hub reports a
// change at any of the board's stations (Invalidate), since browsers
// refetch on that news and must not be handed the copy from before it.
type BoardCache struct {
	TTL time.Duration

	group   singleflight.Group
	mu      sync.Mutex
	entries map[string]cachedBoard
	// changed is when each station topic last changed. An entry computed
	// before that is stale, even if it was stored after.
	changed map[string]time.Time
	now     func() time.Time
}

type cachedBoard struct {
	body    []byte
	started time.Time
	topics  []string
}

// NewBoardCache returns a cache that keeps boards for ttl.
func NewBoardCache(ttl time.Duration) *BoardCache {
	return &BoardCache{TTL: ttl, entries: map[string]cachedBoard{}, changed: map[string]time.Time{}, now: time.Now}
}

// Invalidate drops boards at these topics ("station:CLJ", "station:CLPHMJC").
// Other topics are ignored. It is meant for live.Hub.OnTopics.
func (c *BoardCache) Invalidate(topics []string) {
	now := c.now()
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, t := range topics {
		if len(t) > 8 && t[:8] == "station:" {
			c.changed[t] = now
		}
	}
}

// get returns the cached board for key, or computes it once for everyone
// asking. topics are the station topics the board shows.
func (c *BoardCache) get(ctx context.Context, key string, topics []string, compute func(context.Context) ([]byte, error)) ([]byte, error) {
	if c == nil {
		return compute(ctx)
	}
	if body, ok := c.lookup(key); ok {
		return body, nil
	}
	ch := c.group.DoChan(key, func() (any, error) {
		// Shared by every waiter, so not tied to the first one's request.
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		started := c.now()
		body, err := compute(ctx)
		if err == nil {
			c.store(key, cachedBoard{body: body, started: started, topics: topics})
		}
		return body, err
	})
	select {
	case r := <-ch:
		if r.Err != nil {
			return nil, r.Err
		}
		return r.Val.([]byte), nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (c *BoardCache) lookup(key string) ([]byte, bool) {
	now := c.now()
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok {
		return nil, false
	}
	fresh := now.Sub(e.started) < c.TTL
	for _, t := range e.topics {
		if !c.changed[t].Before(e.started) {
			fresh = false
		}
	}
	if !fresh {
		delete(c.entries, key)
		return nil, false
	}
	return e.body, true
}

func (c *BoardCache) store(key string, e cachedBoard) {
	now := c.now()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[key] = e
	// Boards are few (one per station and filter being watched), but sweep
	// now and then so abandoned ones and old change times don't pile up.
	if len(c.entries) > 2000 || len(c.changed) > 20000 {
		for k, e := range c.entries {
			if now.Sub(e.started) >= c.TTL {
				delete(c.entries, k)
			}
		}
		for t, at := range c.changed {
			if now.Sub(at) >= c.TTL {
				delete(c.changed, t)
			}
		}
	}
}
