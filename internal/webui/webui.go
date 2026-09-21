package webui

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed static/*
var staticFiles embed.FS

// Handler serves the static operator UI. The shell itself is intentionally
// public; all data-changing and data-reading API calls remain protected by the
// Bearer middleware in the application entrypoint.
func Handler() http.Handler {
	content, err := fs.Sub(staticFiles, "static")
	if err != nil {
		panic("webui: embedded static files are unavailable")
	}
	files := http.FileServer(http.FS(content))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		// FileServer resolves the root directory to the embedded index.html.
		// Do not rewrite this to /index.html because net/http intentionally
		// redirects explicit index file paths back to the directory.
		files.ServeHTTP(w, r)
	})
}
