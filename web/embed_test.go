package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func TestHandler(t *testing.T) {
	fsys := fstest.MapFS{
		"index.html":         {Data: []byte("<head><!--trackside-config--></head><div id=root></div>")},
		"assets/app-abc.js":  {Data: []byte("console.log(1)")},
		"favicon.svg":        {Data: []byte("<svg/>")},
		"assets/nested/x.js": {Data: []byte("x")},
	}
	h := New(fsys, Config{TileURL: "https://tiles.example/{z}/{x}/{y}.png", TileAttribution: "</script><b>x</b>"})

	get := func(path string) (*http.Response, string) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		body, _ := io.ReadAll(rec.Result().Body)
		return rec.Result(), string(body)
	}

	for _, p := range []string{"/", "/map", "/station/RDG", "/train/W10001/2026-10-06", "/index.html"} {
		resp, body := get(p)
		if resp.StatusCode != 200 || !strings.Contains(body, "<div id=root>") {
			t.Errorf("%s: status %d, body %q", p, resp.StatusCode, body)
		}
		if !strings.Contains(body, `window.trackside={"tileUrl":"https://tiles.example/{z}/{x}/{y}.png"`) {
			t.Errorf("%s: config not injected: %q", p, body)
		}
		if strings.Contains(body, "</script><b>") {
			t.Errorf("%s: attribution not escaped: %q", p, body)
		}
	}

	resp, body := get("/assets/app-abc.js")
	if body != "console.log(1)" || !strings.Contains(resp.Header.Get("Cache-Control"), "immutable") {
		t.Errorf("asset: %q, cache %q", body, resp.Header.Get("Cache-Control"))
	}
	if resp, _ := get("/favicon.svg"); resp.Header.Get("Cache-Control") != "" {
		t.Errorf("favicon should not be cached forever")
	}
	for _, p := range []string{"/v1/nope", "/api/v1/json/nope", "/v1"} {
		if resp, _ := get(p); resp.StatusCode != 404 {
			t.Errorf("%s: status %d, want 404", p, resp.StatusCode)
		}
	}
	// A directory is not a file: fall back to the app.
	if _, body := get("/assets/nested"); !strings.Contains(body, "<div id=root>") {
		t.Errorf("directory served %q", body)
	}
}

func TestHandlerWithoutBuild(t *testing.T) {
	rec := httptest.NewRecorder()
	New(fstest.MapFS{".gitkeep": {}}, Config{}).ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != 404 || !strings.Contains(rec.Body.String(), "make web") {
		t.Errorf("status %d, body %q", rec.Code, rec.Body.String())
	}
}
