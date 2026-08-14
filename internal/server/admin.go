package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/monadical/llmproxy/internal/apierr"
	"github.com/monadical/llmproxy/internal/catalog"
	"github.com/monadical/llmproxy/internal/pricing"
	"github.com/monadical/llmproxy/internal/secrets"
	"github.com/monadical/llmproxy/internal/store"
)

// Admin API: providers, model bindings, discovery, principals, keys, pricing,
// usage. Every mutation writes a metadata-only admin event in the same
// transaction. No endpoint ever returns a stored credential or key hash.

var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)
var aliasRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]*$`)

func limitOffset(r *http.Request) (int, int) {
	limit, offset := 100, 0
	if v, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && v >= 1 && v <= 500 {
		limit = v
	}
	if v, err := strconv.Atoi(r.URL.Query().Get("offset")); err == nil && v >= 0 {
		offset = v
	}
	return limit, offset
}

func internalErr(w http.ResponseWriter, what string) {
	writeProxyError(w, apierr.New(500, "internal_error", what))
}

// invalidate flushes the alias cache and upstream client pool after any
// catalog mutation.
func (s *Server) invalidate() {
	s.catalog.Invalidate()
	s.pool.Reset()
}

func isHTTPURL(u string) bool {
	return strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://")
}

// ---------- providers ----------

func providerView(p *store.Provider, overrides map[string]string) map[string]any {
	view := map[string]any{
		"name":            p.Name,
		"wire_format":     p.WireFormat,
		"base_url":        p.BaseURL,
		"has_credential":  p.CredentialCiphertext.Valid && p.CredentialCiphertext.String != "",
		"verify_tls":      p.VerifyTLS,
		"has_custom_ca":   p.CAPEM.Valid && p.CAPEM.String != "",
		"timeout_connect": p.TimeoutConnect,
		"timeout_read":    p.TimeoutRead,
		"max_concurrency": nil,
		"enabled":         p.Enabled,
		"created_at":      p.CreatedAt,
	}
	if p.MaxConcurrency.Valid {
		view["max_concurrency"] = p.MaxConcurrency.Int64
	}
	if overrides != nil {
		view["endpoints"] = overrides
	}
	return view
}

func (s *Server) handleProviderCreate(w http.ResponseWriter, r *http.Request, auth *Auth) {
	body := struct {
		Name           string            `json:"name"`
		WireFormat     string            `json:"wire_format"`
		BaseURL        string            `json:"base_url"`
		APIKey         string            `json:"api_key"`
		VerifyTLS      *bool             `json:"verify_tls"`
		CAPEM          string            `json:"ca_pem"`
		TimeoutConnect float64           `json:"timeout_connect"`
		TimeoutRead    float64           `json:"timeout_read"`
		MaxConcurrency *int64            `json:"max_concurrency"`
		Endpoints      map[string]string `json:"endpoints"`
	}{WireFormat: "openai", TimeoutConnect: 10, TimeoutRead: 300}
	if perr := readJSONBody(r, 1<<20, &body); perr != nil {
		writeProxyError(w, perr)
		return
	}
	if !nameRe.MatchString(body.Name) || len(body.Name) > 120 {
		writeProxyError(w, apierr.New(400, "invalid_name",
			"provider name must match ^[a-z0-9][a-z0-9._-]*$"))
		return
	}
	if body.WireFormat != "openai" {
		writeProxyError(w, apierr.New(400, "invalid_wire_format",
			"only the 'openai' wire format is supported for now"))
		return
	}
	if !isHTTPURL(body.BaseURL) {
		writeProxyError(w, apierr.New(400, "invalid_base_url", "base_url must be a full http(s) URL"))
		return
	}
	for endpoint, url := range body.Endpoints {
		if !catalog.IsEndpoint(endpoint) {
			writeProxyError(w, apierr.Newf(400, "invalid_endpoint",
				"unknown endpoint '%s'; valid: %v", endpoint, catalog.Endpoints))
			return
		}
		if !isHTTPURL(url) {
			writeProxyError(w, apierr.Newf(400, "invalid_override",
				"override for '%s' must be a full http(s) URL", endpoint))
			return
		}
	}
	existing, err := s.store.GetProviderByName(r.Context(), body.Name)
	if err != nil {
		internalErr(w, "failed to check provider")
		return
	}
	if existing != nil {
		writeProxyError(w, apierr.Newf(409, "provider_exists", "provider '%s' already exists", body.Name))
		return
	}
	p := &store.Provider{
		Name:           body.Name,
		WireFormat:     body.WireFormat,
		BaseURL:        strings.TrimRight(body.BaseURL, "/"),
		VerifyTLS:      body.VerifyTLS == nil || *body.VerifyTLS,
		TimeoutConnect: body.TimeoutConnect,
		TimeoutRead:    body.TimeoutRead,
		Enabled:        true,
	}
	if body.CAPEM != "" {
		p.CAPEM = sql.NullString{String: body.CAPEM, Valid: true}
	}
	if body.MaxConcurrency != nil {
		p.MaxConcurrency = sql.NullInt64{Int64: *body.MaxConcurrency, Valid: true}
	}
	if body.APIKey != "" {
		encrypted, err := secrets.EncryptCredential(s.secret, body.APIKey)
		if err != nil {
			internalErr(w, "failed to encrypt credential")
			return
		}
		p.CredentialCiphertext = sql.NullString{String: encrypted, Valid: true}
	}
	audit := &store.Audit{Actor: auth.PrincipalID, Action: "provider.create", TargetKind: "provider", TargetRef: p.Name}
	if err := s.store.CreateProvider(r.Context(), p, body.Endpoints, audit); err != nil {
		internalErr(w, "failed to create provider")
		return
	}
	s.invalidate()
	writeJSON(w, 201, providerView(p, body.Endpoints))
}

func (s *Server) handleProviderList(w http.ResponseWriter, r *http.Request, auth *Auth) {
	limit, offset := limitOffset(r)
	providers, err := s.store.ListProviders(r.Context(), limit, offset)
	if err != nil {
		internalErr(w, "failed to list providers")
		return
	}
	views := make([]map[string]any, 0, len(providers))
	for i := range providers {
		views = append(views, providerView(&providers[i], nil))
	}
	writeJSON(w, 200, map[string]any{"providers": views, "limit": limit, "offset": offset})
}

func (s *Server) getProviderOr404(w http.ResponseWriter, r *http.Request) *store.Provider {
	name := r.PathValue("name")
	provider, err := s.store.GetProviderByName(r.Context(), name)
	if err != nil {
		internalErr(w, "failed to load provider")
		return nil
	}
	if provider == nil {
		writeProxyError(w, apierr.Newf(404, "provider_not_found", "no provider named '%s'", name))
		return nil
	}
	return provider
}

func (s *Server) handleProviderGet(w http.ResponseWriter, r *http.Request, auth *Auth) {
	provider := s.getProviderOr404(w, r)
	if provider == nil {
		return
	}
	overrides, err := s.store.ListEndpointOverrides(r.Context(), provider.ID)
	if err != nil {
		internalErr(w, "failed to load endpoint overrides")
		return
	}
	writeJSON(w, 200, providerView(provider, overrides))
}

func (s *Server) handleProviderPatch(w http.ResponseWriter, r *http.Request, auth *Auth) {
	var body struct {
		Enabled          *bool    `json:"enabled"`
		BaseURL          *string  `json:"base_url"`
		APIKey           *string  `json:"api_key"`
		RemoveCredential bool     `json:"remove_credential"`
		VerifyTLS        *bool    `json:"verify_tls"`
		TimeoutConnect   *float64 `json:"timeout_connect"`
		TimeoutRead      *float64 `json:"timeout_read"`
		// MaxConcurrency zero or negative clears the cap back to unlimited.
		MaxConcurrency *int64 `json:"max_concurrency"`
	}
	if perr := readJSONBody(r, 1<<20, &body); perr != nil {
		writeProxyError(w, perr)
		return
	}
	for _, timeout := range []*float64{body.TimeoutConnect, body.TimeoutRead} {
		if timeout != nil && *timeout <= 0 {
			writeProxyError(w, apierr.New(400, "invalid_timeout", "timeouts must be positive seconds"))
			return
		}
	}
	provider := s.getProviderOr404(w, r)
	if provider == nil {
		return
	}
	if body.Enabled != nil {
		provider.Enabled = *body.Enabled
	}
	if body.BaseURL != nil {
		if !isHTTPURL(*body.BaseURL) {
			writeProxyError(w, apierr.New(400, "invalid_base_url", "base_url must be a full http(s) URL"))
			return
		}
		provider.BaseURL = strings.TrimRight(*body.BaseURL, "/")
	}
	if body.VerifyTLS != nil {
		provider.VerifyTLS = *body.VerifyTLS
	}
	if body.TimeoutConnect != nil {
		provider.TimeoutConnect = *body.TimeoutConnect
	}
	if body.TimeoutRead != nil {
		provider.TimeoutRead = *body.TimeoutRead
	}
	if body.MaxConcurrency != nil {
		provider.MaxConcurrency = sql.NullInt64{Int64: *body.MaxConcurrency, Valid: *body.MaxConcurrency > 0}
	}
	if body.RemoveCredential || (body.APIKey != nil && *body.APIKey == "") {
		provider.CredentialCiphertext = sql.NullString{}
	} else if body.APIKey != nil {
		encrypted, err := secrets.EncryptCredential(s.secret, *body.APIKey)
		if err != nil {
			internalErr(w, "failed to encrypt credential")
			return
		}
		provider.CredentialCiphertext = sql.NullString{String: encrypted, Valid: true}
	}
	audit := &store.Audit{Actor: auth.PrincipalID, Action: "provider.update", TargetKind: "provider", TargetRef: provider.Name}
	if err := s.store.UpdateProvider(r.Context(), provider, audit); err != nil {
		internalErr(w, "failed to update provider")
		return
	}
	s.invalidate()
	writeJSON(w, 200, providerView(provider, nil))
}

func (s *Server) handleProviderDelete(w http.ResponseWriter, r *http.Request, auth *Auth) {
	provider := s.getProviderOr404(w, r)
	if provider == nil {
		return
	}
	audit := &store.Audit{Actor: auth.PrincipalID, Action: "provider.delete", TargetKind: "provider", TargetRef: provider.Name}
	if err := s.store.DeleteProvider(r.Context(), provider.ID, audit); err != nil {
		internalErr(w, "failed to delete provider")
		return
	}
	s.invalidate()
	writeJSON(w, 200, map[string]any{"deleted": provider.Name})
}

func (s *Server) handleProviderDiscover(w http.ResponseWriter, r *http.Request, auth *Auth) {
	provider := s.getProviderOr404(w, r)
	if provider == nil {
		return
	}
	route, err := catalog.RouteForProvider(provider, s.secret)
	if err != nil {
		internalErr(w, "failed to decrypt provider credential")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, route.EndpointURL("models"), nil)
	if err != nil {
		internalErr(w, "failed to build discovery request")
		return
	}
	if route.Credential != "" {
		req.Header.Set("Authorization", "Bearer "+route.Credential)
	}
	resp, err := s.pool.ClientFor(route).Do(req)
	if err != nil {
		writeProxyError(w, apierr.Newf(502, "provider_unreachable",
			"discovery request failed: %s", errClass(err)))
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		writeProxyError(w, apierr.Newf(502, "discovery_failed",
			"upstream model listing returned HTTP %d", resp.StatusCode))
		return
	}
	var doc struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&doc); err != nil {
		writeProxyError(w, apierr.New(502, "discovery_failed", "upstream model listing was not valid JSON"))
		return
	}
	bindings, err := s.store.ListBindings(r.Context(), provider.Name, 500, 0)
	if err != nil {
		internalErr(w, "failed to list bindings")
		return
	}
	bound := make(map[string]string, len(bindings))
	for _, b := range bindings {
		bound[b.UpstreamName] = b.Alias
	}
	names := make([]string, 0, len(doc.Data))
	for _, m := range doc.Data {
		if m.ID != "" {
			names = append(names, m.ID)
		}
	}
	sort.Strings(names)
	models := make([]map[string]any, 0, len(names))
	for _, name := range names {
		var alias any
		if a, ok := bound[name]; ok {
			alias = a
		}
		models = append(models, map[string]any{"upstream_name": name, "bound_alias": alias})
	}
	writeJSON(w, 200, map[string]any{
		"provider":      provider.Name,
		"discovered_at": store.Now(),
		"models":        models,
	})
}

// ---------- model bindings ----------

// perMillion scales a per-unit price back to the per-million figure prices are
// quoted in, rounding off the binary-float noise the round trip introduces.
func perMillion(price float64) float64 {
	return math.Round(price*1_000_000*1e6) / 1e6
}

// modelView renders a binding with the prices that apply to it. Prices live in
// the versioned feed keyed on (model, unit); the model can be priced under its
// alias or, inherited, under the upstream name it points at.
func modelView(b *store.ModelBinding, idx *pricing.Index) map[string]any {
	caps := strings.Split(b.CapabilitySet, ",")
	sort.Strings(caps)
	prices := make(map[string]float64)
	inherited := false
	for unit := range pricing.ValidUnits {
		price, ok := idx.Lookup(unit, b.Alias, b.TargetAlias, b.UpstreamName)
		if !ok {
			continue
		}
		prices[unit] = perMillion(price)
		if _, direct := idx.LookupModel(b.Alias, unit); !direct {
			inherited = true
		}
	}
	view := map[string]any{
		"alias":             b.Alias,
		"provider":          b.ProviderName,
		"upstream_name":     b.UpstreamName,
		"capabilities":      caps,
		"origin":            b.Origin,
		"created_at":        b.CreatedAt,
		"target":            nil,
		"pricing":           prices,
		"pricing_inherited": inherited,
	}
	// provider, upstream_name and capabilities are the resolved ones either
	// way; target says where they came from.
	if b.TargetAlias != "" {
		view["target"] = b.TargetAlias
	}
	return view
}

// parsePricing validates a per-million price map from a request body. The map
// is the model's complete price set: units left out are unpriced afterwards.
func parsePricing(prices map[string]*float64) (map[string]float64, *apierr.ProxyError) {
	out := make(map[string]float64, len(prices))
	for unit, price := range prices {
		if !pricing.ValidUnits[unit] {
			return nil, apierr.Newf(400, "invalid_pricing", "unknown price unit '%s'", unit)
		}
		if price == nil {
			continue // an explicit null clears the unit
		}
		if *price < 0 {
			return nil, apierr.Newf(400, "invalid_pricing", "price for '%s' must not be negative", unit)
		}
		out[unit] = *price / 1_000_000
	}
	return out, nil
}

// setModelPricing replaces every price entry keyed on each named alias with
// that alias's price set, as a new feed version. Entries for other models
// carry over untouched. A rename passes the old alias with an empty set and
// the new one with the prices, so both land in the same version.
func (s *Server) setModelPricing(ctx context.Context, alias string,
	sets map[string]map[string]float64, actor string) error {
	audit := &store.Audit{Actor: actor, Action: "pricing.model", TargetKind: "model", TargetRef: alias}
	version := "model-edit-" + store.Now()
	entries, err := s.store.ReplacePricingForModels(ctx, version, "admin-ui", sets, audit)
	if err != nil {
		return err
	}
	s.pricing.Store(pricing.NewIndex(version, entries))
	return nil
}

// resolveTarget loads the binding an alias will point at. One hop only: the
// target must route to a provider itself, so resolution stays a single lookup
// and a chain can never loop.
func (s *Server) resolveTarget(ctx context.Context, alias, target string) (*store.ModelBinding, *apierr.ProxyError) {
	if target == alias {
		return nil, apierr.New(400, "invalid_target", "a model cannot point at itself")
	}
	binding, err := s.store.GetBindingByAlias(ctx, target)
	if err != nil {
		return nil, apierr.New(500, "internal_error", "failed to load the target model")
	}
	if binding == nil {
		return nil, apierr.Newf(404, "model_not_found", "no model named '%s' to point at", target)
	}
	if binding.TargetID.Valid {
		return nil, apierr.Newf(400, "invalid_target",
			"'%s' is itself an alias for '%s'; point at that instead (aliases are one hop)",
			target, binding.TargetAlias)
	}
	return binding, nil
}

func normalizeCapabilities(caps []string) (string, *apierr.ProxyError) {
	if len(caps) == 0 {
		return "", apierr.Newf(400, "invalid_capabilities",
			"capabilities must be a non-empty subset of %v", catalog.Capabilities)
	}
	seen := make(map[string]bool)
	for _, c := range caps {
		if !catalog.IsCapability(c) {
			return "", apierr.Newf(400, "invalid_capabilities",
				"capabilities must be a non-empty subset of %v", catalog.Capabilities)
		}
		seen[c] = true
	}
	unique := make([]string, 0, len(seen))
	for c := range seen {
		unique = append(unique, c)
	}
	sort.Strings(unique)
	return strings.Join(unique, ","), nil
}

func (s *Server) handleModelCreate(w http.ResponseWriter, r *http.Request, auth *Auth) {
	body := struct {
		Alias        string              `json:"alias"`
		Provider     string              `json:"provider"`
		UpstreamName string              `json:"upstream_name"`
		Target       string              `json:"target"`
		Capabilities []string            `json:"capabilities"`
		Origin       string              `json:"origin"`
		Pricing      map[string]*float64 `json:"pricing"`
	}{Capabilities: []string{"chat", "chat_stream"}, Origin: "declared"}
	if perr := readJSONBody(r, 1<<20, &body); perr != nil {
		writeProxyError(w, perr)
		return
	}
	// A model points either at a provider's model or at another model.
	alias := body.Alias
	if body.Target != "" {
		if body.Provider != "" || body.UpstreamName != "" {
			writeProxyError(w, apierr.New(400, "invalid_target",
				"give either 'target' or 'provider' and 'upstream_name', not both"))
			return
		}
		if alias == "" {
			writeProxyError(w, apierr.New(400, "invalid_alias",
				"an alias for another model needs its own name"))
			return
		}
	} else if body.UpstreamName == "" || len(body.UpstreamName) > 200 {
		writeProxyError(w, apierr.New(400, "invalid_upstream_name",
			"upstream_name is required (max 200 chars)"))
		return
	} else if alias == "" {
		// No alias given means "serve it under the upstream's own name",
		// which is what pasting a model id from the provider expects.
		alias = body.UpstreamName
	}
	if !aliasRe.MatchString(alias) || len(alias) > 200 {
		writeProxyError(w, apierr.New(400, "invalid_alias",
			"alias must match ^[A-Za-z0-9][A-Za-z0-9._:/-]*$"))
		return
	}
	if body.Origin != "declared" && body.Origin != "discovered" {
		writeProxyError(w, apierr.New(400, "invalid_origin", "origin must be 'declared' or 'discovered'"))
		return
	}
	prices, perr := parsePricing(body.Pricing)
	if perr != nil {
		writeProxyError(w, perr)
		return
	}
	existing, err := s.store.GetBindingByAlias(r.Context(), alias)
	if err != nil {
		internalErr(w, "failed to check alias")
		return
	}
	if existing != nil {
		writeProxyError(w, apierr.Newf(409, "alias_exists",
			"alias '%s' is already bound; aliases are globally unique", alias))
		return
	}

	binding := &store.ModelBinding{Alias: alias, Origin: body.Origin}
	if body.Target != "" {
		target, perr := s.resolveTarget(r.Context(), alias, body.Target)
		if perr != nil {
			writeProxyError(w, perr)
			return
		}
		// Provider, upstream name and capabilities all come from the target.
		binding.TargetID = sql.NullString{String: target.ID, Valid: true}
		binding.TargetAlias = target.Alias
		binding.ProviderID = target.ProviderID
		binding.ProviderName = target.ProviderName
		binding.UpstreamName = target.UpstreamName
		binding.CapabilitySet = target.CapabilitySet
	} else {
		capabilitySet, perr := normalizeCapabilities(body.Capabilities)
		if perr != nil {
			writeProxyError(w, perr)
			return
		}
		provider, err := s.store.GetProviderByName(r.Context(), body.Provider)
		if err != nil {
			internalErr(w, "failed to load provider")
			return
		}
		if provider == nil {
			writeProxyError(w, apierr.Newf(404, "provider_not_found", "no provider named '%s'", body.Provider))
			return
		}
		binding.ProviderID = provider.ID
		binding.ProviderName = provider.Name
		binding.UpstreamName = body.UpstreamName
		binding.CapabilitySet = capabilitySet
	}
	if body.Origin == "discovered" {
		binding.DiscoveredAt = sql.NullString{String: store.Now(), Valid: true}
	}
	audit := &store.Audit{Actor: auth.PrincipalID, Action: "model.create", TargetKind: "model", TargetRef: alias}
	if err := s.store.CreateBinding(r.Context(), binding, audit); err != nil {
		internalErr(w, "failed to create binding")
		return
	}
	if body.Pricing != nil {
		sets := map[string]map[string]float64{binding.Alias: prices}
		if err := s.setModelPricing(r.Context(), binding.Alias, sets, auth.PrincipalID); err != nil {
			internalErr(w, "failed to store model pricing")
			return
		}
	}
	s.invalidate()
	writeJSON(w, 201, modelView(binding, s.pricing.Load()))
}

func (s *Server) handleModelList(w http.ResponseWriter, r *http.Request, auth *Auth) {
	limit, offset := limitOffset(r)
	bindings, err := s.store.ListBindings(r.Context(), r.URL.Query().Get("provider"), limit, offset)
	if err != nil {
		internalErr(w, "failed to list models")
		return
	}
	idx := s.pricing.Load()
	views := make([]map[string]any, 0, len(bindings))
	for i := range bindings {
		views = append(views, modelView(&bindings[i], idx))
	}
	writeJSON(w, 200, map[string]any{"models": views, "limit": limit, "offset": offset})
}

func (s *Server) getBindingOr404(w http.ResponseWriter, r *http.Request) *store.ModelBinding {
	alias := r.PathValue("alias")
	binding, err := s.store.GetBindingByAlias(r.Context(), alias)
	if err != nil {
		internalErr(w, "failed to load binding")
		return nil
	}
	if binding == nil {
		writeProxyError(w, apierr.Newf(404, "model_not_found", "no binding for alias '%s'", alias))
		return nil
	}
	return binding
}

func (s *Server) handleModelPatch(w http.ResponseWriter, r *http.Request, auth *Auth) {
	var body struct {
		Alias        *string             `json:"alias"`
		Provider     *string             `json:"provider"`
		Capabilities *[]string           `json:"capabilities"`
		UpstreamName *string             `json:"upstream_name"`
		Target       *string             `json:"target"`
		Pricing      map[string]*float64 `json:"pricing"`
	}
	if perr := readJSONBody(r, 1<<20, &body); perr != nil {
		writeProxyError(w, perr)
		return
	}
	binding := s.getBindingOr404(w, r)
	if binding == nil {
		return
	}
	prices, perr := parsePricing(body.Pricing)
	if perr != nil {
		writeProxyError(w, perr)
		return
	}
	// Switching between the two kinds is an edit like any other: point the
	// model at another model, or give it a provider of its own.
	if body.Target != nil {
		if *body.Target == "" {
			binding.TargetID = sql.NullString{}
			binding.TargetAlias = ""
		} else {
			target, perr := s.resolveTarget(r.Context(), binding.Alias, *body.Target)
			if perr != nil {
				writeProxyError(w, perr)
				return
			}
			binding.TargetID = sql.NullString{String: target.ID, Valid: true}
			binding.TargetAlias = target.Alias
			binding.ProviderID = target.ProviderID
			binding.ProviderName = target.ProviderName
			binding.UpstreamName = target.UpstreamName
			binding.CapabilitySet = target.CapabilitySet
		}
	}
	if binding.TargetID.Valid {
		// An alias owns nothing but its name and its price; the rest is the
		// target's and would silently do nothing.
		if body.Capabilities != nil || body.UpstreamName != nil || body.Provider != nil {
			writeProxyError(w, apierr.Newf(400, "invalid_target",
				"'%s' is an alias for '%s' and inherits its provider, model and capabilities; "+
					"edit that one, or clear the target first", binding.Alias, binding.TargetAlias))
			return
		}
	} else {
		if body.Capabilities != nil {
			capabilitySet, perr := normalizeCapabilities(*body.Capabilities)
			if perr != nil {
				writeProxyError(w, perr)
				return
			}
			binding.CapabilitySet = capabilitySet
		}
		if body.UpstreamName != nil {
			if *body.UpstreamName == "" || len(*body.UpstreamName) > 200 {
				writeProxyError(w, apierr.New(400, "invalid_upstream_name",
					"upstream_name is required (max 200 chars)"))
				return
			}
			binding.UpstreamName = *body.UpstreamName
		}
		if body.Provider != nil && *body.Provider != binding.ProviderName {
			provider, err := s.store.GetProviderByName(r.Context(), *body.Provider)
			if err != nil {
				internalErr(w, "failed to load provider")
				return
			}
			if provider == nil {
				writeProxyError(w, apierr.Newf(404, "provider_not_found", "no provider named '%s'", *body.Provider))
				return
			}
			binding.ProviderID = provider.ID
			binding.ProviderName = provider.Name
		}
		// Clearing the target leaves a model with no route unless this call
		// gave it one.
		if body.Target != nil && *body.Target == "" && binding.UpstreamName == "" {
			writeProxyError(w, apierr.New(400, "invalid_upstream_name",
				"clearing the target needs 'provider' and 'upstream_name' in the same call"))
			return
		}
	}
	// Renaming keeps the row, so the model stays one thing across the change:
	// its prices follow it and callers only see the name move.
	oldAlias := binding.Alias
	if body.Alias != nil && *body.Alias != oldAlias {
		if !aliasRe.MatchString(*body.Alias) || len(*body.Alias) > 200 {
			writeProxyError(w, apierr.New(400, "invalid_alias",
				"alias must match ^[A-Za-z0-9][A-Za-z0-9._:/-]*$"))
			return
		}
		taken, err := s.store.GetBindingByAlias(r.Context(), *body.Alias)
		if err != nil {
			internalErr(w, "failed to check alias")
			return
		}
		if taken != nil {
			writeProxyError(w, apierr.Newf(409, "alias_exists",
				"alias '%s' is already bound; aliases are globally unique", *body.Alias))
			return
		}
		binding.Alias = *body.Alias
	}
	audit := &store.Audit{Actor: auth.PrincipalID, Action: "model.update", TargetKind: "model", TargetRef: binding.Alias}
	if err := s.store.UpdateBinding(r.Context(), binding, audit); err != nil {
		internalErr(w, "failed to update binding")
		return
	}
	renamed := binding.Alias != oldAlias
	if body.Pricing != nil || renamed {
		if body.Pricing == nil {
			// A rename with no new prices carries the old ones across.
			prices = make(map[string]float64)
			idx := s.pricing.Load()
			for unit := range pricing.ValidUnits {
				if price, ok := idx.LookupModel(oldAlias, unit); ok {
					prices[unit] = price
				}
			}
		}
		sets := map[string]map[string]float64{binding.Alias: prices}
		if renamed {
			sets[oldAlias] = nil // drop the entries keyed on the old name
		}
		if err := s.setModelPricing(r.Context(), binding.Alias, sets, auth.PrincipalID); err != nil {
			internalErr(w, "failed to store model pricing")
			return
		}
	}
	s.invalidate()
	writeJSON(w, 200, modelView(binding, s.pricing.Load()))
}

func (s *Server) handleModelDelete(w http.ResponseWriter, r *http.Request, auth *Auth) {
	binding := s.getBindingOr404(w, r)
	if binding == nil {
		return
	}
	dependents, err := s.store.ListBindingsTargeting(r.Context(), binding.ID)
	if err != nil {
		internalErr(w, "failed to check aliases")
		return
	}
	if len(dependents) > 0 {
		writeProxyError(w, apierr.Newf(409, "model_in_use",
			"'%s' is the target of %v; delete or repoint those first",
			binding.Alias, dependents))
		return
	}
	audit := &store.Audit{Actor: auth.PrincipalID, Action: "model.delete", TargetKind: "model", TargetRef: binding.Alias}
	if err := s.store.DeleteBinding(r.Context(), binding.ID, audit); err != nil {
		internalErr(w, "failed to delete binding")
		return
	}
	s.invalidate()
	writeJSON(w, 200, map[string]any{"deleted": binding.Alias})
}

func (s *Server) handleResolve(w http.ResponseWriter, r *http.Request, auth *Auth) {
	model := r.URL.Query().Get("model")
	endpoint := r.URL.Query().Get("endpoint")
	if endpoint == "" {
		endpoint = "chat"
	}
	stream := r.URL.Query().Get("stream") == "true"
	route, perr := s.catalog.Resolve(r.Context(), model, endpoint, stream)
	if perr != nil {
		writeProxyError(w, perr)
		return
	}
	caps := make([]string, 0, len(route.Capabilities))
	for c := range route.Capabilities {
		caps = append(caps, c)
	}
	sort.Strings(caps)
	writeJSON(w, 200, map[string]any{
		"alias":         route.Alias,
		"provider":      route.ProviderName,
		"upstream_name": route.UpstreamName,
		"url":           route.EndpointURL(endpoint),
		"capabilities":  caps,
	})
}

// ---------- principals and keys ----------

func (s *Server) handlePrincipalCreate(w http.ResponseWriter, r *http.Request, auth *Auth) {
	body := struct {
		Name string `json:"name"`
		Kind string `json:"kind"`
		Role string `json:"role"`
	}{Kind: "user", Role: "member"}
	if perr := readJSONBody(r, 1<<20, &body); perr != nil {
		writeProxyError(w, perr)
		return
	}
	if !nameRe.MatchString(body.Name) || len(body.Name) > 120 {
		writeProxyError(w, apierr.New(400, "invalid_name",
			"principal name must match ^[a-z0-9][a-z0-9._-]*$"))
		return
	}
	if (body.Kind != "user" && body.Kind != "service") || (body.Role != "member" && body.Role != "admin") {
		writeProxyError(w, apierr.New(400, "invalid_principal",
			"kind must be user|service, role must be member|admin"))
		return
	}
	existing, err := s.store.GetPrincipalByName(r.Context(), body.Name)
	if err != nil {
		internalErr(w, "failed to check principal")
		return
	}
	if existing != nil {
		writeProxyError(w, apierr.Newf(409, "principal_exists", "principal '%s' already exists", body.Name))
		return
	}
	audit := &store.Audit{Actor: auth.PrincipalID, Action: "principal.create", TargetKind: "principal", TargetRef: body.Name}
	principal, err := s.store.GetOrCreatePrincipal(r.Context(), body.Name, body.Kind, body.Role, audit)
	if err != nil {
		internalErr(w, "failed to create principal")
		return
	}
	writeJSON(w, 201, map[string]any{
		"id": principal.ID, "name": principal.Name, "kind": principal.Kind, "role": principal.Role,
	})
}

func (s *Server) handlePrincipalList(w http.ResponseWriter, r *http.Request, auth *Auth) {
	limit, offset := limitOffset(r)
	principals, err := s.store.ListPrincipals(r.Context(), limit, offset)
	if err != nil {
		internalErr(w, "failed to list principals")
		return
	}
	views := make([]map[string]any, 0, len(principals))
	for _, p := range principals {
		views = append(views, map[string]any{"id": p.ID, "name": p.Name, "kind": p.Kind, "role": p.Role})
	}
	writeJSON(w, 200, map[string]any{"principals": views, "limit": limit, "offset": offset})
}

// handlePrincipalRevokeSessions deletes the principal's browser sessions
// before their natural TTL; API keys are unaffected. After removing someone
// at the IdP this forces the next UI access through a fresh login, which
// re-reconciles group membership.
func (s *Server) handlePrincipalRevokeSessions(w http.ResponseWriter, r *http.Request, auth *Auth) {
	principal, err := s.store.GetPrincipalByID(r.Context(), r.PathValue("id"))
	if err != nil {
		internalErr(w, "failed to load principal")
		return
	}
	if principal == nil {
		writeProxyError(w, apierr.Newf(404, "principal_not_found",
			"no principal with id '%s'", r.PathValue("id")))
		return
	}
	audit := &store.Audit{Actor: auth.PrincipalID, Action: "principal.revoke_sessions",
		TargetKind: "principal", TargetRef: principal.ID}
	deleted, err := s.store.RevokePrincipalSessions(r.Context(), principal.ID, audit)
	if err != nil {
		internalErr(w, "failed to revoke sessions")
		return
	}
	writeJSON(w, 200, map[string]any{"revoked": principal.ID, "deleted_sessions": deleted})
}

func (s *Server) handleAdminKeyCreate(w http.ResponseWriter, r *http.Request, auth *Auth) {
	var body struct {
		Principal string `json:"principal"`
		Label     string `json:"label"`
	}
	if perr := readJSONBody(r, 1<<20, &body); perr != nil {
		writeProxyError(w, perr)
		return
	}
	principal, err := s.store.GetPrincipalByName(r.Context(), body.Principal)
	if err != nil {
		internalErr(w, "failed to load principal")
		return
	}
	if principal == nil {
		writeProxyError(w, apierr.Newf(404, "principal_not_found", "no principal named '%s'", body.Principal))
		return
	}
	plaintext := secrets.GenerateAPIKey()
	audit := &store.Audit{Actor: auth.PrincipalID, Action: "key.create", TargetKind: "api_key", TargetRef: principal.Name}
	key, err := s.store.CreateAPIKey(r.Context(), principal.ID,
		secrets.HashAPIKey(s.secret, plaintext), secrets.KeySuffix(plaintext), body.Label, audit)
	if err != nil {
		internalErr(w, "failed to create key")
		return
	}
	view := keyView(key)
	view["key"] = plaintext
	view["principal"] = principal.Name
	writeJSON(w, 201, view)
}

func (s *Server) handleAdminKeyList(w http.ResponseWriter, r *http.Request, auth *Auth) {
	limit, offset := limitOffset(r)
	keys, err := s.store.ListAPIKeys(r.Context(), "", r.URL.Query().Get("principal"), limit, offset)
	if err != nil {
		internalErr(w, "failed to list keys")
		return
	}
	views := make([]map[string]any, 0, len(keys))
	for i := range keys {
		view := keyView(&keys[i])
		view["principal"] = keys[i].PrincipalName
		views = append(views, view)
	}
	writeJSON(w, 200, map[string]any{"keys": views, "limit": limit, "offset": offset})
}

func (s *Server) handleAdminKeyRevoke(w http.ResponseWriter, r *http.Request, auth *Auth) {
	key, err := s.store.GetAPIKey(r.Context(), r.PathValue("id"), "")
	if err != nil {
		internalErr(w, "failed to load key")
		return
	}
	if key == nil {
		writeProxyError(w, apierr.Newf(404, "key_not_found", "no key with id '%s'", r.PathValue("id")))
		return
	}
	audit := &store.Audit{Actor: auth.PrincipalID, Action: "key.delete", TargetKind: "api_key", TargetRef: key.ID}
	if err := s.store.DeleteAPIKey(r.Context(), key.ID, audit); err != nil {
		internalErr(w, "failed to delete key")
		return
	}
	writeJSON(w, 200, map[string]any{"deleted": key.ID})
}

// ---------- pricing, usage, audit ----------

func (s *Server) handlePricingLoad(w http.ResponseWriter, r *http.Request, auth *Auth) {
	raw, perr := readLimited(r, 8<<20)
	if perr != nil {
		writeProxyError(w, perr)
		return
	}
	idx, err := pricing.ParseFeed(raw)
	if err != nil {
		writeProxyError(w, apierr.New(400, "invalid_pricing_feed", err.Error()))
		return
	}
	audit := &store.Audit{Actor: auth.PrincipalID, Action: "pricing.load", TargetKind: "pricing_feed", TargetRef: idx.Version}
	if err := s.store.StorePricingFeed(r.Context(), idx.Version, "admin-api", idx.Entries(), audit); err != nil {
		internalErr(w, "failed to store pricing feed")
		return
	}
	s.pricing.Store(idx)
	writeJSON(w, 200, map[string]any{"version": idx.Version, "count": idx.Len()})
}

func (s *Server) handlePricingStatus(w http.ResponseWriter, r *http.Request, auth *Auth) {
	idx := s.pricing.Load()
	version := any(nil)
	if idx.Version != "" {
		version = idx.Version
	}
	entries := make([]map[string]any, 0, idx.Len())
	for key, price := range idx.Entries() {
		entries = append(entries, map[string]any{
			"model":             key[0],
			"unit":              key[1],
			"price_per_million": perMillion(price),
		})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i]["model"] != entries[j]["model"] {
			return entries[i]["model"].(string) < entries[j]["model"].(string)
		}
		return entries[i]["unit"].(string) < entries[j]["unit"].(string)
	})
	writeJSON(w, 200, map[string]any{"version": version, "count": idx.Len(), "entries": entries})
}

// principalFilter resolves the optional ?principal= name to an id; it returns
// ok=false after writing the error response.
func (s *Server) principalFilter(w http.ResponseWriter, r *http.Request) (string, bool) {
	name := r.URL.Query().Get("principal")
	if name == "" {
		return "", true
	}
	principal, err := s.store.GetPrincipalByName(r.Context(), name)
	if err != nil {
		internalErr(w, "failed to load principal")
		return "", false
	}
	if principal == nil {
		writeProxyError(w, apierr.Newf(404, "principal_not_found", "no principal named '%s'", name))
		return "", false
	}
	return principal.ID, true
}

func (s *Server) handleAdminUsageSeries(w http.ResponseWriter, r *http.Request, auth *Auth) {
	principalID, ok := s.principalFilter(w, r)
	if !ok {
		return
	}
	s.handleUsageSeries(w, r, store.UsageFilter{PrincipalID: principalID})
}

func (s *Server) handleUsageSummary(w http.ResponseWriter, r *http.Request, auth *Auth) {
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
	principalID, ok := s.principalFilter(w, r)
	if !ok {
		return
	}
	rows, err := s.store.UsageSummary(r.Context(), principalID, since, until)
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
	writeJSON(w, 200, map[string]any{"usage": summaryViews(rows, names)})
}

// serveRequestLog returns one filtered, paged slice of the usage events with
// per-unit quantities: the request metadata log (who, key, model, endpoint,
// client, outcome, tokens). No content, ever; there is none to return.
// Served to every authenticated user (/stats/requests) and kept on the admin
// path for compatibility. total is the size of the whole filtered set, so the
// pager can show it without walking the pages.
func (s *Server) serveRequestLog(w http.ResponseWriter, r *http.Request) {
	filter, ok := s.statsFilter(w, r)
	if !ok {
		return
	}
	limit := 50
	if v, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && v >= 1 && v <= 500 {
		limit = v
	}
	offset := 0
	if v, err := strconv.Atoi(r.URL.Query().Get("offset")); err == nil && v >= 0 {
		offset = v
	}
	events, err := s.store.ListRequests(r.Context(), filter, limit, offset)
	if err != nil {
		internalErr(w, "failed to load recent usage")
		return
	}
	total, err := s.store.CountRequests(r.Context(), filter)
	if err != nil {
		internalErr(w, "failed to count usage")
		return
	}
	views := make([]map[string]any, 0, len(events))
	for _, ev := range events {
		view := map[string]any{
			"id":          ev.ID,
			"ts":          ev.TS,
			"principal":   ev.PrincipalName,
			"provider":    ev.Provider,
			"model":       ev.Alias,
			"endpoint":    ev.Endpoint,
			"client":      ev.Client,
			"tags":        ev.Tags,
			"key_id":      ev.APIKeyID,
			"key_label":   ev.KeyLabel,
			"key_suffix":  ev.KeySuffix,
			"outcome":     ev.Outcome,
			"error_kind":  ev.ErrorKind,
			"status_code": nil,
			"streamed":    ev.Streamed,
			"cancelled":   ev.Cancelled,
			"cost":        nil,
			"unpriced":    ev.Unpriced,
			"duration_ms": ev.DurationMs,
			"units":       ev.Units,
		}
		if ev.StatusCode.Valid {
			view["status_code"] = ev.StatusCode.Int64
		}
		if ev.Cost.Valid {
			view["cost"] = ev.Cost.Float64
		}
		views = append(views, view)
	}
	writeJSON(w, 200, map[string]any{
		"requests": views, "limit": limit, "offset": offset, "total": total,
	})
}

func (s *Server) handleAdminRequests(w http.ResponseWriter, r *http.Request, auth *Auth) {
	s.serveRequestLog(w, r)
}

func (s *Server) handleAdminEvents(w http.ResponseWriter, r *http.Request, auth *Auth) {
	limit, offset := limitOffset(r)
	events, err := s.store.ListAdminEvents(r.Context(), limit, offset)
	if err != nil {
		internalErr(w, "failed to list admin events")
		return
	}
	views := make([]map[string]any, 0, len(events))
	for _, e := range events {
		views = append(views, map[string]any{
			"ts":                 e.TS,
			"actor_principal_id": e.ActorPrincipalID,
			"action":             e.Action,
			"target_kind":        e.TargetKind,
			"target_ref":         e.TargetRef,
		})
	}
	writeJSON(w, 200, map[string]any{"events": views, "limit": limit, "offset": offset})
}
