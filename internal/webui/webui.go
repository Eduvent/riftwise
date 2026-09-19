// Package webui serves the dependency-free dashboard embedded in the Go binary.
package webui

import (
	"bytes"
	"embed"
	"io/fs"
	"net/http"
	"time"
)

//go:embed assets/*
var embeddedAssets embed.FS

type asset struct {
	fileName    string
	contentType string
	cache       string
}

var routes = map[string]asset{
	"/":                        {fileName: "index.html", contentType: "text/html; charset=utf-8", cache: "no-store"},
	"/index.html":              {fileName: "index.html", contentType: "text/html; charset=utf-8", cache: "no-store"},
	"/assets/app.css":          {fileName: "app.css", contentType: "text/css; charset=utf-8", cache: "no-cache"},
	"/assets/state.js":         {fileName: "state.js", contentType: "text/javascript; charset=utf-8", cache: "no-cache"},
	"/assets/app.js":           {fileName: "app.js", contentType: "text/javascript; charset=utf-8", cache: "no-cache"},
	"/assets/history-state.js": {fileName: "history-state.js", contentType: "text/javascript; charset=utf-8", cache: "no-cache"},
	"/assets/history.js":       {fileName: "history.js", contentType: "text/javascript; charset=utf-8", cache: "no-cache"},
	"/favicon.svg":             {fileName: "favicon.svg", contentType: "image/svg+xml", cache: "no-cache"},
}

// New returns a handler for the known UI routes. Unknown paths remain 404s;
// this intentionally is not a catch-all single-page-app router.
func New() http.Handler {
	assets, err := fs.Sub(embeddedAssets, "assets")
	if err != nil {
		panic("open embedded web UI: " + err.Error())
	}
	return &handler{assets: assets}
}

type handler struct {
	assets fs.FS
}

func (h *handler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	setSecurityHeaders(response.Header())
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		response.Header().Set("Allow", "GET, HEAD")
		http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	entry, ok := routes[request.URL.Path]
	if !ok {
		http.NotFound(response, request)
		return
	}
	data, err := fs.ReadFile(h.assets, entry.fileName)
	if err != nil {
		http.Error(response, "embedded asset unavailable", http.StatusInternalServerError)
		return
	}

	response.Header().Set("Content-Type", entry.contentType)
	response.Header().Set("Cache-Control", entry.cache)
	http.ServeContent(response, request, entry.fileName, time.Time{}, bytes.NewReader(data))
}

func setSecurityHeaders(header http.Header) {
	header.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' https://ddragon.leagueoflegends.com data:; connect-src 'self'; script-src 'self'; style-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
	header.Set("Referrer-Policy", "no-referrer")
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("X-Frame-Options", "DENY")
}
