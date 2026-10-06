// Package web serves the trackside frontend, a React app built into dist
// by `npm run build` and embedded in the binary.
package web

import (
	"bytes"
	"embed"
	"encoding/json"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed all:dist
var dist embed.FS

// Config is handed to the frontend in index.html.
type Config struct {
	TileURL         string `json:"tileUrl,omitempty"`
	TileAttribution string `json:"tileAttribution,omitempty"`
}

// Handler serves the embedded frontend.
func Handler(cfg Config) http.Handler {
	sub, _ := fs.Sub(dist, "dist")
	return New(sub, cfg)
}

// configMarker is replaced by the config script in index.html.
const configMarker = "<!--trackside-config-->"

// New serves the frontend in fsys. Files are served as they are; any other
// path gets index.html so the app's own router can handle it. API paths
// never fall back, so a wrong API URL is a 404 and not a page of HTML.
func New(fsys fs.FS, cfg Config) http.Handler {
	files := http.FileServerFS(fsys)
	index, err := fs.ReadFile(fsys, "index.html")
	if err == nil {
		js, _ := json.Marshal(cfg) // json escapes <, > and &, so this is safe inside a script
		index = bytes.Replace(index, []byte(configMarker), []byte("<script>window.trackside="+string(js)+"</script>"), 1)
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := path.Clean(r.URL.Path)
		if p == "/v1" || p == "/api" || strings.HasPrefix(p, "/v1/") || strings.HasPrefix(p, "/api/") {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"error":"not found"}` + "\n"))
			return
		}
		if name := strings.TrimPrefix(p, "/"); name != "" && name != "index.html" {
			if st, err := fs.Stat(fsys, name); err == nil && !st.IsDir() {
				if strings.HasPrefix(name, "assets/") {
					// Vite puts a content hash in every asset's name.
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				}
				files.ServeHTTP(w, r)
				return
			}
		}
		if index == nil {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte("This trackside was built without its web frontend. Run `make web` (or `npm ci && npm run build` in web/) and rebuild.\nThe API is at /v1 and its documentation at /docs.\n"))
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.Write(index)
	})
}
