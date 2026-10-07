package api

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// Cache shares responses between viewers. An open board refetches whenever
// its station changes, every couple of seconds at a busy one, and an open
// map or train page refetches as trains move; without sharing, every
// viewer costs a full computation. With it, a page costs the same however
// many people watch it.
//
// Requests for the same URL at the same moment wait for one computation.
// The result is kept for the entry's TTL, but dropped as soon as the live
// hub reports a change to one of its topics (Invalidate), since browsers
// refetch on that news and must not be handed the copy from before it.
//
// Each response is gzipped once when stored, so compression is shared too:
// everything leaves the cluster compressed, through the home connection
// Cloudflare's tunnel runs over.
type Cache struct {
	group   singleflight.Group
	mu      sync.Mutex
	entries map[string]*cached
	// changed is when each topic last changed. An entry computed before
	// that is stale, even if it was stored after.
	changed map[string]time.Time
	now     func() time.Time
}

type cached struct {
	body, gz []byte
	header   http.Header
	started  time.Time
	ttl      time.Duration
	topics   []string
}

// cacheSpec says how long a response may be shared and what makes it stale.
type cacheSpec struct {
	TTL time.Duration
	// Topics are live hub topics ("station:CLJ", "train:W12345|2026-10-07").
	Topics []string
	// Header is sent with the response (Content-Type, Cache-Control).
	// Content-Type defaults to JSON.
	Header http.Header
}

// statusError is an error response that isn't cached, such as a 404.
type statusError struct {
	status int
	msg    string
}

func (e *statusError) Error() string { return e.msg }

// NewCache returns an empty cache.
func NewCache() *Cache {
	return &Cache{entries: map[string]*cached{}, changed: map[string]time.Time{}, now: time.Now}
}

// Invalidate drops responses for these topics. It is meant for
// live.Hub.OnTopics.
func (c *Cache) Invalidate(topics []string) {
	now := c.now()
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, t := range topics {
		c.changed[t] = now
	}
}

// serve writes the response for r, from the cache when it can. compute
// returns the value to encode as JSON; a *statusError is written as is.
// A nil cache computes every time.
func (c *Cache) serve(w http.ResponseWriter, r *http.Request, spec cacheSpec, compute func(context.Context) (any, error)) {
	e, err := c.get(r.Context(), r.URL.Path+"?"+r.URL.Query().Encode(), spec, compute)
	if err != nil {
		var se *statusError
		switch {
		case errors.As(err, &se):
			writeError(w, se.status, se.msg)
		case r.Context().Err() == nil:
			serverError(w, err)
		}
		return
	}
	h := w.Header()
	maps.Copy(h, e.header)
	h.Add("Vary", "Accept-Encoding")
	if e.gz != nil && acceptsGzip(r) {
		h.Set("Content-Encoding", "gzip")
		w.Write(e.gz)
		return
	}
	w.Write(e.body)
}

func (c *Cache) get(ctx context.Context, key string, spec cacheSpec, compute func(context.Context) (any, error)) (*cached, error) {
	build := func(ctx context.Context) (*cached, error) {
		started := time.Now()
		if c != nil {
			started = c.now()
		}
		v, err := compute(ctx)
		if err != nil {
			return nil, err
		}
		return encode(v, spec, started)
	}
	if c == nil {
		return build(ctx)
	}
	if e, ok := c.lookup(key); ok {
		return e, nil
	}
	ch := c.group.DoChan(key, func() (any, error) {
		// Shared by every waiter, so not tied to the first one's request.
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		e, err := build(ctx)
		if err == nil {
			c.store(key, e)
		}
		return e, err
	})
	select {
	case r := <-ch:
		if r.Err != nil {
			return nil, r.Err
		}
		return r.Val.(*cached), nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func encode(v any, spec cacheSpec, started time.Time) (*cached, error) {
	body, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	body = append(body, '\n')
	e := &cached{body: body, header: http.Header{}, started: started, ttl: spec.TTL, topics: spec.Topics}
	maps.Copy(e.header, spec.Header)
	if e.header.Get("Content-Type") == "" {
		e.header.Set("Content-Type", "application/json")
	}
	// Small bodies aren't worth it.
	if len(body) >= 1024 {
		var buf bytes.Buffer
		zw, _ := gzip.NewWriterLevel(&buf, gzip.DefaultCompression)
		zw.Write(body)
		if err := zw.Close(); err != nil {
			return nil, err
		}
		e.gz = buf.Bytes()
	}
	return e, nil
}

func acceptsGzip(r *http.Request) bool {
	for part := range strings.SplitSeq(r.Header.Get("Accept-Encoding"), ",") {
		coding, params, _ := strings.Cut(part, ";")
		if coding = strings.TrimSpace(coding); !strings.EqualFold(coding, "gzip") && coding != "*" {
			continue
		}
		if q, ok := strings.CutPrefix(strings.TrimSpace(params), "q="); ok {
			if f, err := strconv.ParseFloat(q, 64); err == nil && f == 0 {
				return false
			}
		}
		return true
	}
	return false
}

func (c *Cache) lookup(key string) (*cached, bool) {
	now := c.now()
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok {
		return nil, false
	}
	fresh := now.Sub(e.started) < e.ttl
	for _, t := range e.topics {
		if !c.changed[t].Before(e.started) {
			fresh = false
		}
	}
	if !fresh {
		delete(c.entries, key)
		return nil, false
	}
	return e, true
}

// maxTTL bounds how long a change time can matter: no entry outlives it.
const maxTTL = time.Hour

func (c *Cache) store(key string, e *cached) {
	now := c.now()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[key] = e
	// Pages are few (one per board, train and filter being watched), but
	// sweep now and then so abandoned ones and old change times don't pile
	// up.
	if len(c.entries) > 5000 || len(c.changed) > 50000 {
		for k, e := range c.entries {
			if now.Sub(e.started) >= e.ttl {
				delete(c.entries, k)
			}
		}
		for t, at := range c.changed {
			if now.Sub(at) >= maxTTL {
				delete(c.changed, t)
			}
		}
	}
}
