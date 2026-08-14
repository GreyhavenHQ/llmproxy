// Package server wires the HTTP surface: OpenAI-compatible ingress under /v1,
// self-service under /my, admin under /admin/v1, plus /healthz and /metrics.
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"strings"

	"github.com/monadical/llmproxy/internal/apierr"
	"github.com/monadical/llmproxy/internal/catalog"
	"github.com/monadical/llmproxy/internal/config"
	"github.com/monadical/llmproxy/internal/metrics"
	"github.com/monadical/llmproxy/internal/oidc"
	"github.com/monadical/llmproxy/internal/pricing"
	"github.com/monadical/llmproxy/internal/secrets"
	"github.com/monadical/llmproxy/internal/store"
	"github.com/monadical/llmproxy/internal/upstream"
)

type Server struct {
	cfg     config.Config
	store   *store.Store
	secret  []byte
	catalog *catalog.Catalog
	pool    *upstream.Pool
	metrics *metrics.Metrics
	pricing atomic.Pointer[pricing.Index]

	// transparent is the HTTP client for the transparent Anthropic relay;
	// separate from the provider pool because there is no provider row.
	transparent *http.Client

	// webfetch backs the playground's web_fetch tool; its dialer refuses
	// non-public addresses (see webfetch.go).
	webfetch *http.Client

	sso          *oidc.Client
	sessionKey   []byte
	cookieSecure bool
	// publicHost is the browser-facing host (from the OIDC redirect URL),
	// accepted by the Origin check when a reverse proxy rewrites Host.
	publicHost string

	// adminPassword ("" = disabled) and localAdminID back the browser
	// password login for the local admin principal; both are set once in
	// Bootstrap and read-only afterwards.
	adminPassword string
	localAdminID  string

	// wg tracks detached usage-recording goroutines so shutdown can drain them.
	wg sync.WaitGroup
}

func New(cfg config.Config, st *store.Store, secret []byte) *Server {
	s := &Server{
		cfg:          cfg,
		store:        st,
		secret:       secret,
		catalog:      catalog.New(st, secret, cfg.CatalogTTL),
		pool:         upstream.New(),
		metrics:      metrics.New(),
		sessionKey:   deriveSessionKey(secret),
		cookieSecure: strings.HasPrefix(cfg.OIDCRedirectURL, "https://"),
		publicHost:   hostOf(cfg.OIDCRedirectURL),
		transparent:  newTransparentClient(),
		webfetch:     newWebFetchClient(false),
	}
	s.pricing.Store(pricing.Empty())
	return s
}

func (s *Server) Store() *store.Store { return s.store }

func hostOf(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return parsed.Host
}

// Bootstrap initialises the schema, the local admin principal (in no-SSO
// mode) and the pricing index.
func (s *Server) Bootstrap(ctx context.Context) error {
	if err := s.store.Init(ctx); err != nil {
		return fmt.Errorf("init schema: %w", err)
	}
	if !s.cfg.AdminPasswordDisabled {
		password, generated, err := secrets.LoadOrCreatePassword(s.cfg.AdminPassword, s.cfg.AdminPasswordFile)
		if err != nil {
			return fmt.Errorf("admin password: %w", err)
		}
		s.adminPassword = password
		if generated {
			slog.Info("generated the admin UI password", "file", s.cfg.AdminPasswordFile)
		}
	}
	// The local admin principal backs both local mode and the password login
	// (which stays available as a break-glass path in SSO mode).
	if s.cfg.LocalMode() || s.adminPassword != "" {
		admin, err := s.store.GetOrCreatePrincipal(ctx, s.cfg.LocalAdminName, "user", "admin", nil)
		if err != nil {
			return fmt.Errorf("bootstrap local admin: %w", err)
		}
		s.localAdminID = admin.ID
	}
	version, entries, err := s.store.LoadActivePricing(ctx)
	if err != nil {
		return fmt.Errorf("load pricing: %w", err)
	}
	s.pricing.Store(pricing.NewIndex(version, entries))
	if s.cfg.PricingFile != "" {
		raw, err := os.ReadFile(s.cfg.PricingFile)
		if err != nil {
			return fmt.Errorf("read pricing file: %w", err)
		}
		idx, err := pricing.ParseFeed(raw)
		if err != nil {
			return err
		}
		if idx.Version != version {
			if err := s.store.StorePricingFeed(ctx, idx.Version, s.cfg.PricingFile, idx.Entries(), nil); err != nil {
				return err
			}
			s.pricing.Store(idx)
		}
	}
	if s.cfg.OIDCIssuer != "" {
		if s.cfg.OIDCClientID == "" || s.cfg.OIDCClientSecret == "" || s.cfg.OIDCRedirectURL == "" {
			return fmt.Errorf("SSO mode needs LLMPROXY_OIDC_CLIENT_ID, LLMPROXY_OIDC_CLIENT_SECRET and LLMPROXY_OIDC_REDIRECT_URL")
		}
		sso, err := oidc.Discover(ctx, oidc.Config{
			Issuer:       s.cfg.OIDCIssuer,
			ClientID:     s.cfg.OIDCClientID,
			ClientSecret: s.cfg.OIDCClientSecret,
			RedirectURL:  s.cfg.OIDCRedirectURL,
			Scopes:       s.cfg.OIDCScopes,
			GroupsClaim:  s.cfg.OIDCGroupsClaim,
		})
		if err != nil {
			return err
		}
		s.sso = sso
	}
	return nil
}

// Drain waits for detached accounting goroutines (used by shutdown and tests).
func (s *Server) Drain() { s.wg.Wait() }

// statusWriter records the response status for the access log while passing
// Flush through so SSE relays keep working.
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = 200
	}
	return w.ResponseWriter.Write(b)
}

func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// logRequests writes one line per request. Errors log at warn/error so a
// failing client is visible even at higher log levels; health checks and
// static assets only appear at debug.
func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w}
		next.ServeHTTP(sw, r)
		if sw.status == 0 {
			sw.status = 200
		}
		level := slog.LevelInfo
		switch {
		case sw.status >= 500:
			level = slog.LevelError
		case sw.status >= 400:
			level = slog.LevelWarn
		case r.URL.Path == "/healthz" || r.URL.Path == "/metrics" ||
			strings.HasPrefix(r.URL.Path, "/assets/") || strings.HasPrefix(r.URL.Path, "/fonts/"):
			level = slog.LevelDebug
		}
		slog.Log(r.Context(), level, "http",
			"method", r.Method,
			// Relay tokens ride in the path; keep them out of the log.
			"path", maskTransparentPath(r.URL.Path),
			"status", sw.status,
			"duration_ms", time.Since(start).Milliseconds())
	})
}

// handleUnknownEndpoint gives unmatched API paths (and wrong methods on known
// paths) a JSON error instead of a bare 405 or the SPA's HTML.
func (s *Server) handleUnknownEndpoint(w http.ResponseWriter, r *http.Request) {
	msg := fmt.Sprintf("no endpoint for %s %s", r.Method, r.URL.Path)
	switch {
	case strings.HasPrefix(r.URL.Path, "/v1/responses"), r.URL.Path == "/responses":
		msg = "the OpenAI Responses API is not supported; use POST /v1/chat/completions"
	case strings.HasPrefix(r.URL.Path, "/v1/messages"):
		msg = "the Anthropic Messages API is not supported yet; use POST /v1/chat/completions"
	case strings.HasPrefix(r.URL.Path, "/v1/"):
		msg += "; supported: POST /v1/chat/completions, POST /v1/completions, POST /v1/embeddings, GET /v1/models"
	case strings.HasPrefix(r.URL.Path, "/transparent/"):
		msg = "the transparent relay is served at /transparent/anthropic/<relay-token>/<anthropic-path>"
	}
	writeProxyError(w, apierr.New(404, "unknown_endpoint", msg))
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// JSON fallbacks for the API subtrees; exact method+path patterns below
	// take precedence, so these only see typos and wrong methods.
	for _, prefix := range []string{"/v1/", "/my/", "/admin/", "/auth/", "/transparent/", "/stats/"} {
		mux.HandleFunc(prefix, s.handleUnknownEndpoint)
	}

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		_, _ = w.Write([]byte(s.metrics.Render()))
	})

	ui := s.uiHandler()
	mux.Handle("/", ui)
	mux.HandleFunc("GET /auth/me", s.handleAuthMe)
	mux.HandleFunc("POST /auth/password", s.handleAuthPassword)
	mux.HandleFunc("GET /auth/login", s.handleAuthLogin)
	mux.HandleFunc("GET /auth/callback", s.handleAuthCallback)
	mux.HandleFunc("GET /auth/logout", s.handleAuthLogout)

	mux.Handle("GET /v1/models", s.withAuth(s.handleListModels))
	mux.Handle("POST /v1/chat/completions", s.withAuth(s.handleChatCompletions))
	mux.Handle("POST /v1/completions", s.withAuth(s.handleCompletions))
	mux.Handle("POST /v1/embeddings", s.withAuth(s.handleEmbeddings))

	// LiteLLM serves the OpenAI routes at the root as well; keep that working
	// so clients configured with a bare base URL do not break. /models is also
	// a page of the SPA, so a browser reload there (Accept: text/html) gets
	// the app back; only /v1/models is unconditionally JSON.
	mux.Handle("GET /models", uiOrAPI(ui, s.withAuth(s.handleListModels)))
	mux.Handle("POST /chat/completions", s.withAuth(s.handleChatCompletions))
	mux.Handle("POST /completions", s.withAuth(s.handleCompletions))
	mux.Handle("POST /embeddings", s.withAuth(s.handleEmbeddings))

	// LiteLLM management-API compatibility (admin role, see litellm.go).
	mux.Handle("POST /model/new", s.withAdmin(s.handleLiteLLMModelNew))
	mux.Handle("GET /model/info", s.withAdmin(s.handleLiteLLMModelInfo))
	mux.Handle("POST /model/delete", s.withAdmin(s.handleLiteLLMModelDelete))

	// The transparent relay accepts any method on any sub-path; the token in
	// the path attributes usage, the caller's own credentials go through.
	mux.HandleFunc("/transparent/anthropic/{token}/{path...}", s.handleTransparentAnthropic)

	mux.Handle("POST /my/keys", s.withAuth(s.handleMyKeyCreate))
	mux.Handle("GET /my/keys", s.withAuth(s.handleMyKeysList))
	mux.Handle("DELETE /my/keys/{id}", s.withAuth(s.handleMyKeyRevoke))
	mux.Handle("POST /my/relay-tokens", s.withAuth(s.handleMyRelayTokenCreate))
	mux.Handle("GET /my/relay-tokens", s.withAuth(s.handleMyRelayTokensList))
	mux.Handle("DELETE /my/relay-tokens/{id}", s.withAuth(s.handleMyRelayTokenDelete))
	mux.Handle("POST /my/webfetch", s.withAuth(s.handleMyWebFetch))
	mux.Handle("GET /my/usage", s.withAuth(s.handleMyUsage))
	mux.Handle("GET /my/usage/series", s.withAuth(s.handleMyUsageSeries))

	// Team-visible statistics: every authenticated user sees the whole
	// proxy's usage, filterable by principal, provider and client.
	mux.Handle("GET /stats/series", s.withAuth(s.handleStatsSeries))
	mux.Handle("GET /stats/summary", s.withAuth(s.handleStatsSummary))
	mux.Handle("GET /stats/requests", s.withAuth(s.handleStatsRequests))
	mux.Handle("GET /stats/requests/facets", s.withAuth(s.handleStatsFacets))
	mux.Handle("GET /stats/errors", s.withAuth(s.handleStatsErrors))

	mux.Handle("POST /admin/v1/providers", s.withAdmin(s.handleProviderCreate))
	mux.Handle("GET /admin/v1/providers", s.withAdmin(s.handleProviderList))
	mux.Handle("GET /admin/v1/providers/{name}", s.withAdmin(s.handleProviderGet))
	mux.Handle("PATCH /admin/v1/providers/{name}", s.withAdmin(s.handleProviderPatch))
	mux.Handle("DELETE /admin/v1/providers/{name}", s.withAdmin(s.handleProviderDelete))
	mux.Handle("GET /admin/v1/providers/{name}/discover", s.withAdmin(s.handleProviderDiscover))
	mux.Handle("POST /admin/v1/models", s.withAdmin(s.handleModelCreate))
	mux.Handle("GET /admin/v1/models", s.withAdmin(s.handleModelList))
	mux.Handle("PATCH /admin/v1/models/{alias}", s.withAdmin(s.handleModelPatch))
	mux.Handle("DELETE /admin/v1/models/{alias}", s.withAdmin(s.handleModelDelete))
	mux.Handle("GET /admin/v1/resolve", s.withAdmin(s.handleResolve))
	mux.Handle("POST /admin/v1/principals", s.withAdmin(s.handlePrincipalCreate))
	mux.Handle("GET /admin/v1/principals", s.withAdmin(s.handlePrincipalList))
	mux.Handle("POST /admin/v1/principals/{id}/revoke-sessions", s.withAdmin(s.handlePrincipalRevokeSessions))
	mux.Handle("POST /admin/v1/keys", s.withAdmin(s.handleAdminKeyCreate))
	mux.Handle("GET /admin/v1/keys", s.withAdmin(s.handleAdminKeyList))
	mux.Handle("DELETE /admin/v1/keys/{id}", s.withAdmin(s.handleAdminKeyRevoke))
	mux.Handle("POST /admin/v1/pricing", s.withAdmin(s.handlePricingLoad))
	mux.Handle("GET /admin/v1/pricing", s.withAdmin(s.handlePricingStatus))
	mux.Handle("GET /admin/v1/usage/summary", s.withAdmin(s.handleUsageSummary))
	mux.Handle("GET /admin/v1/usage/series", s.withAdmin(s.handleAdminUsageSeries))
	mux.Handle("GET /admin/v1/requests", s.withAdmin(s.handleAdminRequests))
	mux.Handle("GET /admin/v1/events", s.withAdmin(s.handleAdminEvents))

	return logRequests(mux)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeProxyError emits the OpenAI-shaped envelope for proxy-generated errors.
func writeProxyError(w http.ResponseWriter, e *apierr.ProxyError) {
	var param any
	if e.Param != "" {
		param = e.Param
	}
	w.Header().Set("x-llmproxy-error-source", "proxy")
	writeJSON(w, e.Status, map[string]any{
		"error": map[string]any{
			"message": e.Message,
			"type":    apierr.Type,
			"code":    e.Code,
			"param":   param,
		},
		"llmproxy": map[string]any{"source": "proxy"},
	})
}

func readJSONBody(r *http.Request, maxBytes int64, v any) *apierr.ProxyError {
	raw, perr := readLimited(r, maxBytes)
	if perr != nil {
		return perr
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return apierr.New(400, "invalid_json", "request body is not valid JSON")
	}
	return nil
}
