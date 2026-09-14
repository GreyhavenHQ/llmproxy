package server

import (
	"net/http"
	"strings"
	"time"

	"github.com/greyhavenhq/llmproxy/internal/apierr"
	"github.com/greyhavenhq/llmproxy/internal/store"
)

// Shared statistics endpoints, open to every authenticated user: the proxy's
// usage is team-visible by design. Same aggregates as the admin endpoints,
// with the full filter set (principal, provider, client). Everything here is
// metadata only; no request or response content exists to expose.

// maxTagFilters caps the repeatable tag parameter: more dimensions than any
// caller narrows by at once, and it bounds the LIKE clauses per query.
const maxTagFilters = 4

// statsFilter reads the common query filters. principal is resolved from name
// to id (writing the error response on failure); provider matches the
// resolved provider name; model matches the alias the caller used; endpoint
// matches the route ("chat", "embeddings", ...); client is a prefix match on
// the stored User-Agent; key is an API key id, which relay traffic never
// carries. tag is repeatable
// and takes an exact "key:value" pair, lowercased like the stored value,
// several of which narrow together; a value that matches nothing simply
// returns nothing. app_tagged=1 drops events without an app tag, so per-app
// views ignore untagged traffic. since and until bound the window on the
// stored UTC timestamp.
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
	// Lowercased to match what capture stores. Without this the two backends
	// disagree: SQLite's LIKE is ASCII-case-insensitive, Postgres' is not, so
	// an uppercase filter would match on one and not the other.
	tags := r.URL.Query()["tag"]
	if len(tags) > maxTagFilters {
		tags = tags[:maxTagFilters]
	}
	for i, tag := range tags {
		tags[i] = strings.ToLower(tag)
	}
	// Validated rather than passed through: a typo silently matching nothing
	// would read as "no failures".
	outcome := r.URL.Query().Get("outcome")
	if outcome != "" && !outcomeFilters[outcome] {
		writeProxyError(w, apierr.New(400, "invalid_outcome",
			"'outcome' must be ok, upstream_error, unreachable, cancelled or failed"))
		return store.UsageFilter{}, false
	}
	return store.UsageFilter{
		PrincipalID: principalID,
		APIKeyID:    r.URL.Query().Get("key"),
		Provider:    r.URL.Query().Get("provider"),
		Model:       r.URL.Query().Get("model"),
		Endpoint:    r.URL.Query().Get("endpoint"),
		Client:      r.URL.Query().Get("client"),
		Tags:        tags,
		AppTagged:   r.URL.Query().Get("app_tagged") == "1",
		Outcome:     outcome,
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
// dimension: one row per (principal, provider, model, endpoint, client, tags). The
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
			"tags":      row.Tags,
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

// handleStatsRateLimits returns the current rate-limit snapshot for every
// configured provider: the latest in-memory reading plus a stale flag.
func (s *Server) handleStatsRateLimits(w http.ResponseWriter, r *http.Request, auth *Auth) {
	snaps := s.rateLimits.Snapshots()
	staleAfter := 5 * time.Minute

	views := make([]map[string]any, 0, len(snaps))
	for _, snap := range snaps {
		age := time.Since(snap.ObservedAt)
		view := map[string]any{
			"provider":    snap.ProviderName,
			"provider_id": snap.ProviderID,
			"observed_at": snap.ObservedAt.UTC().Format(time.RFC3339),
			"stale":       age > staleAfter,
		}
		if snap.LimitRequests != nil {
			view["limit_requests"] = *snap.LimitRequests
		}
		if snap.RemainingRequests != nil {
			view["remaining_requests"] = *snap.RemainingRequests
		}
		if snap.MinRemRequests != nil {
			view["min_remaining_requests"] = *snap.MinRemRequests
		}
		if snap.LimitTokens != nil {
			view["limit_tokens"] = *snap.LimitTokens
		}
		if snap.RemainingTokens != nil {
			view["remaining_tokens"] = *snap.RemainingTokens
		}
		if snap.MinRemTokens != nil {
			view["min_remaining_tokens"] = *snap.MinRemTokens
		}
		if snap.ResetRequestsAt != "" {
			view["reset_requests_at"] = snap.ResetRequestsAt
		}
		if snap.ResetTokensAt != "" {
			view["reset_tokens_at"] = snap.ResetTokensAt
		}
		views = append(views, view)
	}
	writeJSON(w, 200, map[string]any{"rate_limits": views})
}

// handleStatsRateLimitSeries returns stored minute-bucket samples for one
// provider over a time range.
func (s *Server) handleStatsRateLimitSeries(w http.ResponseWriter, r *http.Request, auth *Auth) {
	providerName := r.URL.Query().Get("provider")
	if providerName == "" {
		writeProxyError(w, apierr.New(400, "missing_provider", "'provider' query parameter is required"))
		return
	}
	provider, err := s.store.GetProviderByName(r.Context(), providerName)
	if err != nil {
		internalErr(w, "failed to load provider")
		return
	}
	if provider == nil {
		writeProxyError(w, apierr.Newf(404, "provider_not_found", "no provider named '%s'", providerName))
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
	rows, err := s.store.RateLimitSeries(r.Context(), provider.ID, since, until)
	if err != nil {
		internalErr(w, "failed to load rate-limit series")
		return
	}
	views := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		view := map[string]any{
			"bucket":       row.Bucket,
			"observed_at":  row.ObservedAt,
			"observations": row.Observations,
		}
		if row.LimitRequests.Valid {
			view["limit_requests"] = row.LimitRequests.Int64
		}
		if row.RemainingRequests.Valid {
			view["remaining_requests"] = row.RemainingRequests.Int64
		}
		if row.MinRemainingRequests.Valid {
			view["min_remaining_requests"] = row.MinRemainingRequests.Int64
		}
		if row.LimitTokens.Valid {
			view["limit_tokens"] = row.LimitTokens.Int64
		}
		if row.RemainingTokens.Valid {
			view["remaining_tokens"] = row.RemainingTokens.Int64
		}
		if row.MinRemainingTokens.Valid {
			view["min_remaining_tokens"] = row.MinRemainingTokens.Int64
		}
		if row.ResetRequestsAt.Valid {
			view["reset_requests_at"] = row.ResetRequestsAt.String
		}
		if row.ResetTokensAt.Valid {
			view["reset_tokens_at"] = row.ResetTokensAt.String
		}
		views = append(views, view)
	}
	writeJSON(w, 200, map[string]any{"provider": providerName, "series": views})
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
		"tags":       strsOrEmpty(facets.Tags),
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
