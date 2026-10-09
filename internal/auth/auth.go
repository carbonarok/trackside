// Package auth puts a shared API key in front of the whole server.
package auth

import (
	"crypto/subtle"
	"net/http"
	"strings"
)

// Keys parses a comma-separated list of API keys. Several keys let one be
// replaced without locking every client out at once.
func Keys(s string) []string {
	var out []string
	for _, k := range strings.Split(s, ",") {
		if k = strings.TrimSpace(k); k != "" {
			out = append(out, k)
		}
	}
	return out
}

// Require answers 401 to any request that doesn't carry one of keys, except
// the health check. With no keys it returns h unchanged and the server is
// open.
//
// A key is accepted as "Authorization: Bearer KEY", or as the password of
// HTTP Basic auth with any user name. Basic auth lets the built-in website
// work unchanged: the browser asks for the key once and then sends it with
// every request, the live WebSocket included. Clients of the Realtime
// Trains-compatible API already send Basic auth.
func Require(keys []string, h http.Handler) http.Handler {
	if len(keys) == 0 {
		return h
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" || allowed(keys, presented(r)) {
			h.ServeHTTP(w, r)
			return
		}
		w.Header().Set("WWW-Authenticate", `Basic realm="trackside", charset="UTF-8"`)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":"this server needs an API key"}` + "\n"))
	})
}

// presented returns the key a request carries, or "".
func presented(r *http.Request) string {
	if _, pass, ok := r.BasicAuth(); ok {
		return pass
	}
	scheme, token, ok := strings.Cut(r.Header.Get("Authorization"), " ")
	if ok && strings.EqualFold(scheme, "Bearer") {
		return strings.TrimSpace(token)
	}
	return ""
}

// allowed compares key with every configured key in constant time.
func allowed(keys []string, key string) bool {
	if key == "" {
		return false
	}
	ok := 0
	for _, k := range keys {
		ok |= subtle.ConstantTimeCompare([]byte(k), []byte(key))
	}
	return ok == 1
}
