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
// resolved provider name; client is a prefix match on the stored User-Agent;
// key is an API key id, which relay traffic never carries. since and until
// bound the window on the stored UTC timestamp.
func (s *Server) statsFilter(w http.ResponseWriter, r *http.Request) (store.UsageFilter, bool) {
	principalID, ok := s.principalFilter(w, r)
	if !ok {
		return store.UsageFilter{}, false
	}
	since, perr := parseTimeParam(r.URL.Query().Get("since"), "since")
	if perr != nil {
		writeProxyError(w, perr)
		return store.UsageFilter{}, false
	}
	until, perr := parseTimeParam(r.URL.Query().Get("until"), "until")
	if perr != nil {
		writeProxyError(w, perr)
		return store.UsageFilter{}, false
	}
	return store.UsageFilter{
		PrincipalID: principalID,
		APIKeyID:    r.URL.Query().Get("key"),
		Provider:    r.URL.Query().Get("provider"),
		Model:       r.URL.Query().Get("model"),
		Client:      r.URL.Query().Get("client"),
		Since:       since,
		Until:       until,
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

// handleStatsFacets returns the distinct filter values present in a window,
// feeding the request explorer's dropdowns. It reads every event, including
// the failures and model-less calls the usage breakdown drops: a request
// explorer whose job is triage must be able to filter by a user or key that
// has only ever failed. Only the window narrows it, so the option list stays
// put as the other filters change.
func (s *Server) handleStatsFacets(w http.ResponseWriter, r *http.Request, auth *Auth) {
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
	facets, err := s.store.RequestFacets(r.Context(), since, until)
	if err != nil {
		internalErr(w, "failed to load filter options")
		return
	}
	keys := make([]map[string]any, 0, len(facets.Keys))
	for _, k := range facets.Keys {
		keys = append(keys, map[string]any{
			"id": k.ID, "label": k.Label, "key_suffix": k.Suffix, "principal": k.Principal,
		})
	}
	writeJSON(w, 200, map[string]any{
		"principals": strsOrEmpty(facets.Principals),
		"providers":  strsOrEmpty(facets.Providers),
		"models":     strsOrEmpty(facets.Models),
		"clients":    strsOrEmpty(facets.Clients),
		"keys":       keys,
	})
}

// strsOrEmpty renders an absent facet as [] rather than null.
func strsOrEmpty(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}
