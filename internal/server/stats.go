package server

import (
	"net/http"

	"github.com/monadical/llmproxy/internal/store"
)

// Shared statistics endpoints, open to every authenticated user: the proxy's
// usage is team-visible by design. Same aggregates as the admin endpoints,
// with the full filter set (principal, provider, client). Everything here is
// metadata only; no request or response content exists to expose.

// statsFilter reads the common query filters. principal is resolved from name
// to id (writing the error response on failure); provider matches the
// resolved provider name; client is a prefix match on the stored User-Agent.
func (s *Server) statsFilter(w http.ResponseWriter, r *http.Request) (store.UsageFilter, bool) {
	principalID, ok := s.principalFilter(w, r)
	if !ok {
		return store.UsageFilter{}, false
	}
	return store.UsageFilter{
		PrincipalID: principalID,
		Provider:    r.URL.Query().Get("provider"),
		Model:       r.URL.Query().Get("model"),
		Client:      r.URL.Query().Get("client"),
	}, true
}

func (s *Server) handleStatsSeries(w http.ResponseWriter, r *http.Request, auth *Auth) {
	filter, ok := s.statsFilter(w, r)
	if !ok {
		return
	}
	s.handleUsageSeries(w, r, filter)
}

// handleStatsSummary returns the window aggregated over every recorded
// dimension: one row per (principal, provider, model, endpoint, client). The
// UI rolls these up per dimension and derives its filter options from the
// distinct values.
func (s *Server) handleStatsSummary(w http.ResponseWriter, r *http.Request, auth *Auth) {
	filter, ok := s.statsFilter(w, r)
	if !ok {
		return
	}
	since, perr := parseTimeParam(r.URL.Query().Get("since"), "since")
	if perr != nil {
		writeProxyError(w, perr)
		return
	}
	until, perr := parseTimeParam(r.URL.Query().Get("until"), "until")
	if perr != nil {
		writeProxyError(w, perr)
		return
	}
	filter.Since, filter.Until = since, until
	rows, err := s.store.UsageBreakdown(r.Context(), filter)
	if err != nil {
		internalErr(w, "failed to summarise usage")
		return
	}
	principals, err := s.store.ListPrincipals(r.Context(), 500, 0)
	if err != nil {
		internalErr(w, "failed to list principals")
		return
	}
	names := make(map[string]string, len(principals))
	for _, p := range principals {
		names[p.ID] = p.Name
	}
	views := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		name, ok := names[row.PrincipalID]
		if !ok {
			name = "unknown"
		}
		view := map[string]any{
			"principal": name,
			"provider":  row.Provider,
			"model":     row.Alias,
			"endpoint":  row.Endpoint,
			"client":    row.Client,
			"requests":  row.Requests,
			"cancelled": row.Cancelled,
			"cost":      nil,
			"units":     row.Units,
		}
		if row.Cost.Valid {
			view["cost"] = row.Cost.Float64
		}
		views = append(views, view)
	}
	writeJSON(w, 200, map[string]any{"usage": views})
}

func (s *Server) handleStatsRequests(w http.ResponseWriter, r *http.Request, auth *Auth) {
	s.serveRequestLog(w, r)
}
