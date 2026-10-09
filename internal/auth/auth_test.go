package auth

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestKeys(t *testing.T) {
	if got := Keys(""); got != nil {
		t.Errorf("Keys(\"\") = %q, want none", got)
	}
	if got, want := Keys(" a, b ,,c "), []string{"a", "b", "c"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Keys = %q, want %q", got, want)
	}
}

func TestRequire(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusTeapot) })

	t.Run("no keys leaves the server open", func(t *testing.T) {
		w := httptest.NewRecorder()
		Require(nil, ok).ServeHTTP(w, httptest.NewRequest("GET", "/v1/locations", nil))
		if w.Code != http.StatusTeapot {
			t.Errorf("status %d, want the handler's", w.Code)
		}
	})

	h := Require([]string{"old-key", "new-key"}, ok)
	for _, tc := range []struct {
		name, path string
		auth       func(*http.Request)
		want       int
	}{
		{"no key", "/v1/locations", func(*http.Request) {}, http.StatusUnauthorized},
		{"website needs it too", "/", func(*http.Request) {}, http.StatusUnauthorized},
		{"health check is open", "/healthz", func(*http.Request) {}, http.StatusTeapot},
		{"bearer", "/v1/locations", func(r *http.Request) { r.Header.Set("Authorization", "Bearer new-key") }, http.StatusTeapot},
		{"bearer, any case", "/v1/locations", func(r *http.Request) { r.Header.Set("Authorization", "bearer old-key") }, http.StatusTeapot},
		{"basic, any user", "/api/v1/json/search/CLJ", func(r *http.Request) { r.SetBasicAuth("rttuser", "old-key") }, http.StatusTeapot},
		{"basic, empty user", "/v1/live", func(r *http.Request) { r.SetBasicAuth("", "new-key") }, http.StatusTeapot},
		{"wrong bearer", "/v1/locations", func(r *http.Request) { r.Header.Set("Authorization", "Bearer nope") }, http.StatusUnauthorized},
		{"key as basic user is not enough", "/v1/locations", func(r *http.Request) { r.SetBasicAuth("new-key", "") }, http.StatusUnauthorized},
		{"prefix of a key", "/v1/locations", func(r *http.Request) { r.Header.Set("Authorization", "Bearer new") }, http.StatusUnauthorized},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", tc.path, nil)
			tc.auth(r)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Fatalf("status %d, want %d", w.Code, tc.want)
			}
			if w.Code == http.StatusUnauthorized && w.Header().Get("WWW-Authenticate") == "" {
				t.Error("401 without WWW-Authenticate: browsers won't ask for the key")
			}
		})
	}
}
