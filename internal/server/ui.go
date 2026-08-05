package server

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

// Built-in UI: a React app (ui/ in the repo, Greyhaven design system) compiled
// by Vite into uidist/ and embedded into the binary. The app is a plain client
// of the JSON APIs (/auth, /my, /admin/v1); there is no server-side rendering
// and no separate UI backend. Rebuild with `just ui`.

//go:embed all:uidist
var uiDist embed.FS

// uiHandler serves the embedded SPA: exact files (hashed assets, fonts) when
// they exist, index.html for any other GET so a browser reload never 404s.
// Non-GET requests get a JSON error instead of a bare 405. All API routes
// (including the root-level OpenAI aliases) are registered explicitly on the
// mux and take precedence.
func (s *Server) uiHandler() http.Handler {
	dist, err := fs.Sub(uiDist, "uidist")
	if err != nil {
		panic(err) // embed is broken at build time, not a runtime condition
	}
	fileServer := http.FileServerFS(dist)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			s.handleUnknownEndpoint(w, r)
			return
		}
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path != "" {
			if f, err := dist.Open(path); err == nil {
				_ = f.Close()
				if strings.HasPrefix(path, "assets/") || strings.HasPrefix(path, "fonts/") {
					// Vite content-hashes assets; fonts change only with a rebuild.
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				}
				fileServer.ServeHTTP(w, r)
				return
			}
		}
		w.Header().Set("Cache-Control", "no-cache")
		http.ServeFileFS(w, r, dist, "index.html")
	})
}

// uiOrAPI routes a request to the SPA when it is a browser navigation
// (Accept lists text/html; API clients and SDKs never ask for that) and to
// the API handler otherwise. Used where a UI page path collides with a
// root-level API alias, e.g. GET /models.
func uiOrAPI(ui, api http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.Header.Get("Accept"), "text/html") {
			ui.ServeHTTP(w, r)
			return
		}
		api.ServeHTTP(w, r)
	})
}

// handleAuthMe tells the UI who is calling and which login methods exist.
// Unauthenticated is a normal 200 so the login screen can render itself.
func (s *Server) handleAuthMe(w http.ResponseWriter, r *http.Request) {
	view := map[string]any{
		"authenticated":    false,
		"sso_enabled":      s.sso != nil,
		"password_enabled": s.adminPassword != "",
	}
	if auth, perr := s.authenticate(r); perr == nil && auth != nil {
		view["authenticated"] = true
		view["name"] = auth.PrincipalName
		view["role"] = auth.Role
	}
	writeJSON(w, 200, view)
}
