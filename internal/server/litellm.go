package server

import (
	"context"
	"database/sql"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/greyhavenhq/llmproxy/internal/apierr"
	"github.com/greyhavenhq/llmproxy/internal/secrets"
	"github.com/greyhavenhq/llmproxy/internal/store"
)

// LiteLLM management-API compatibility, so existing registration tooling
// works unchanged: POST /model/new, GET /model/info, POST /model/delete.
//
// The mapping: a LiteLLM "deployment" (model_name + litellm_params) becomes a
// provider (api_base + api_key, reused across deployments with the same
// api_base) plus a model binding (model_name as the alias, litellm_params
// .model as the upstream name). Re-registering the same mapping is an
// idempotent success. A second deployment under one model_name becomes a
// hidden "<model_name>@<provider>" model, and model_name an alias over all
// of them with the weighted strategy.

var providerNameStrip = regexp.MustCompile(`[^a-z0-9._-]+`)
var providerNameLead = regexp.MustCompile(`^[^a-z0-9]+`)

// providerNameFromBase derives a provider name from the upstream host, e.g.
// "https://api.tensorx.ai/v1" -> "api.tensorx.ai".
func providerNameFromBase(apiBase string) string {
	host := "upstream"
	if u, err := url.Parse(apiBase); err == nil && u.Hostname() != "" {
		host = strings.ToLower(u.Hostname())
		if u.Port() != "" {
			host += "-" + u.Port()
		}
	}
	name := providerNameLead.ReplaceAllString(providerNameStrip.ReplaceAllString(host, "-"), "")
	if name == "" || !nameRe.MatchString(name) {
		return "upstream"
	}
	return name
}

// ensureProviderFor returns a provider pointing at apiBase, creating one when
// none exists. A supplied apiKey is stored (replacing any previous credential:
// the key given at registration is the key used, as in LiteLLM).
func (s *Server) ensureProviderFor(w http.ResponseWriter, r *http.Request, auth *Auth, apiBase, apiKey string) *store.Provider {
	providers, err := s.store.ListProviders(r.Context(), 500, 0)
	if err != nil {
		internalErr(w, "failed to list providers")
		return nil
	}
	base := strings.TrimRight(apiBase, "/")

	for i := range providers {
		p := &providers[i]
		if strings.TrimRight(p.BaseURL, "/") != base {
			continue
		}
		if apiKey != "" {
			encrypted, err := secrets.EncryptCredential(s.secret, apiKey)
			if err != nil {
				internalErr(w, "failed to encrypt credential")
				return nil
			}
			p.CredentialCiphertext = sql.NullString{String: encrypted, Valid: true}
			audit := &store.Audit{Actor: auth.PrincipalID, Action: "provider.update", TargetKind: "provider", TargetRef: p.Name}
			if err := s.store.UpdateProvider(r.Context(), p, audit); err != nil {
				internalErr(w, "failed to update provider")
				return nil
			}
			s.invalidate()
		}
		return p
	}

	taken := make(map[string]bool, len(providers))
	for _, p := range providers {
		taken[p.Name] = true
	}
	name := providerNameFromBase(apiBase)
	candidate := name
	for i := 2; taken[candidate]; i++ {
		candidate = name + "-" + strconv.Itoa(i)
	}

	p := &store.Provider{
		Name: candidate, WireFormat: "openai", BaseURL: base,
		VerifyTLS: true, TimeoutConnect: 10, TimeoutRead: 300, Enabled: true,
	}
	if apiKey != "" {
		encrypted, err := secrets.EncryptCredential(s.secret, apiKey)
		if err != nil {
			internalErr(w, "failed to encrypt credential")
			return nil
		}
		p.CredentialCiphertext = sql.NullString{String: encrypted, Valid: true}
	}
	audit := &store.Audit{Actor: auth.PrincipalID, Action: "provider.create", TargetKind: "provider", TargetRef: candidate}
	if err := s.store.CreateProvider(r.Context(), p, nil, audit); err != nil {
		internalErr(w, "failed to create provider")
		return nil
	}
	s.invalidate()
	return p
}

func litellmDeploymentView(modelName string, b *store.ModelBinding, apiBase string) map[string]any {
	return map[string]any{
		"model_name": modelName,
		"litellm_params": map[string]any{
			"model":               b.UpstreamName,
			"api_base":            apiBase,
			"custom_llm_provider": "openai",
		},
		"model_info": map[string]any{"id": b.ID},
	}
}

// isDeployment reports whether target was created for a deployment of alias.
func isDeployment(alias string, target *store.ModelBinding) bool {
	return target.Hidden && strings.HasPrefix(target.Alias, alias+"@")
}

// deploymentName picks a free "<model_name>@<provider>" name.
func (s *Server) deploymentName(ctx context.Context, modelName, providerName string) (string, error) {
	base := modelName + "@" + providerName
	name := base
	for i := 2; ; i++ {
		taken, err := s.store.GetBindingByAlias(ctx, name)
		if err != nil || taken == nil {
			return name, err
		}
		name = base + "-" + strconv.Itoa(i)
	}
}

// capabilitiesForMode maps LiteLLM's model_info.mode to a capability set.
func capabilitiesForMode(mode string) string {
	switch mode {
	case "embedding", "embeddings":
		return "embeddings"
	case "completion", "text-completion":
		return "completions"
	default:
		return "chat,chat_stream"
	}
}

func (s *Server) handleLiteLLMModelNew(w http.ResponseWriter, r *http.Request, auth *Auth) {
	var body struct {
		ModelName     string `json:"model_name"`
		LiteLLMParams struct {
			Model   string  `json:"model"`
			APIBase string  `json:"api_base"`
			APIKey  string  `json:"api_key"`
			Weight  float64 `json:"weight"`
		} `json:"litellm_params"`
		ModelInfo map[string]any `json:"model_info"`
	}
	if perr := readJSONBody(r, 1<<20, &body); perr != nil {
		writeProxyError(w, perr)
		return
	}
	if !aliasRe.MatchString(body.ModelName) || len(body.ModelName) > 200 {
		writeProxyError(w, apierr.New(400, "invalid_alias",
			"model_name must match ^[A-Za-z0-9][A-Za-z0-9._:/-]*$"))
		return
	}
	if body.LiteLLMParams.Model == "" || len(body.LiteLLMParams.Model) > 200 {
		writeProxyError(w, apierr.New(400, "invalid_upstream_name",
			"litellm_params.model is required"))
		return
	}
	if !isHTTPURL(body.LiteLLMParams.APIBase) {
		writeProxyError(w, apierr.New(400, "invalid_base_url",
			"litellm_params.api_base must be a full http(s) URL"))
		return
	}

	provider := s.ensureProviderFor(w, r, auth, body.LiteLLMParams.APIBase, body.LiteLLMParams.APIKey)
	if provider == nil {
		return
	}

	existing, err := s.store.GetBindingByAlias(r.Context(), body.ModelName)
	if err != nil {
		internalErr(w, "failed to check alias")
		return
	}
	matches := func(b *store.ModelBinding) bool {
		return b.ProviderID == provider.ID && b.UpstreamName == body.LiteLLMParams.Model
	}
	if existing != nil && len(existing.Targets) == 0 && matches(existing) {
		writeJSON(w, 200, litellmDeploymentView(existing.Alias, existing, provider.BaseURL))
		return
	}
	for _, t := range targetsOf(existing) {
		target, err := s.store.GetBindingByID(r.Context(), t.ID)
		if err != nil {
			internalErr(w, "failed to load target")
			return
		}
		if target != nil && matches(target) {
			writeJSON(w, 200, litellmDeploymentView(existing.Alias, target, provider.BaseURL))
			return
		}
	}

	mode, _ := body.ModelInfo["mode"].(string)
	binding := &store.ModelBinding{
		Alias:         body.ModelName,
		ProviderID:    provider.ID,
		UpstreamName:  body.LiteLLMParams.Model,
		CapabilitySet: capabilitiesForMode(mode),
		Origin:        "declared",
		ProviderName:  provider.Name,
	}
	if existing != nil {
		name, err := s.deploymentName(r.Context(), body.ModelName, provider.Name)
		if err != nil {
			internalErr(w, "failed to check alias")
			return
		}
		binding.Alias = name
		binding.Hidden = true
	}
	audit := &store.Audit{Actor: auth.PrincipalID, Action: "model.create", TargetKind: "model", TargetRef: binding.Alias}
	if err := s.store.CreateBinding(r.Context(), binding, audit); err != nil {
		internalErr(w, "failed to create binding")
		return
	}
	if existing != nil {
		weight := max(int(math.Round(body.LiteLLMParams.Weight)), 1)
		if perr := s.addDeployment(r.Context(), auth, existing, binding, weight); perr != nil {
			s.invalidate()
			writeProxyError(w, perr)
			return
		}
	}
	s.invalidate()
	writeJSON(w, 200, litellmDeploymentView(body.ModelName, binding, provider.BaseURL))
}

func targetsOf(b *store.ModelBinding) []store.BindingTarget {
	if b == nil {
		return nil
	}
	return b.Targets
}

// addDeployment makes deployment one more target of existing. A direct model
// keeps its id: it moves to a hidden deployment name, and a new alias takes
// its place.
func (s *Server) addDeployment(ctx context.Context, auth *Auth, existing, deployment *store.ModelBinding,
	weight int) *apierr.ProxyError {
	alias := existing
	if len(existing.Targets) == 0 {
		name, err := s.deploymentName(ctx, existing.Alias, existing.ProviderName)
		if err != nil {
			return apierr.New(500, "internal_error", "failed to check alias")
		}
		moved := *existing
		moved.Alias = name
		moved.Hidden = true
		audit := &store.Audit{Actor: auth.PrincipalID, Action: "model.update", TargetKind: "model", TargetRef: name}
		if err := s.store.UpdateBinding(ctx, &moved, audit); err != nil {
			return apierr.New(500, "internal_error", "failed to update binding")
		}
		alias = &store.ModelBinding{
			Alias:   existing.Alias,
			Origin:  existing.Origin,
			Hidden:  existing.Hidden,
			Targets: []store.BindingTarget{{ID: moved.ID, Alias: name, Weight: 1}},
		}
	}
	alias.Targets = append(alias.Targets, store.BindingTarget{ID: deployment.ID, Alias: deployment.Alias, Weight: weight})
	if alias.Strategy == "" {
		alias.Strategy = "weighted"
	}
	audit := &store.Audit{Actor: auth.PrincipalID, Action: "model.update", TargetKind: "model", TargetRef: alias.Alias}
	if alias.ID == "" {
		audit.Action = "model.create"
		if err := s.store.CreateBinding(ctx, alias, audit); err != nil {
			return apierr.New(500, "internal_error", "failed to create binding")
		}
		return nil
	}
	if err := s.store.UpdateBinding(ctx, alias, audit); err != nil {
		return apierr.New(500, "internal_error", "failed to update binding")
	}
	return nil
}

func (s *Server) handleLiteLLMModelInfo(w http.ResponseWriter, r *http.Request, auth *Auth) {
	bindings, err := s.store.ListBindings(r.Context(), "", 500, 0)
	if err != nil {
		internalErr(w, "failed to list models")
		return
	}
	providers, err := s.store.ListProviders(r.Context(), 500, 0)
	if err != nil {
		internalErr(w, "failed to list providers")
		return
	}
	baseByID := make(map[string]string, len(providers))
	for _, p := range providers {
		baseByID[p.ID] = p.BaseURL
	}
	byID := make(map[string]*store.ModelBinding, len(bindings))
	for i := range bindings {
		byID[bindings[i].ID] = &bindings[i]
	}
	// A deployment target is listed under its alias, not on its own.
	listed := make(map[string]bool)
	for _, b := range bindings {
		if b.Strategy == "" {
			continue
		}
		for _, t := range b.Targets {
			if target := byID[t.ID]; target != nil && isDeployment(b.Alias, target) {
				listed[t.ID] = true
			}
		}
	}
	data := make([]map[string]any, 0, len(bindings))
	for i := range bindings {
		b := &bindings[i]
		if listed[b.ID] {
			continue
		}
		if b.Strategy == "" {
			data = append(data, litellmDeploymentView(b.Alias, b, baseByID[b.ProviderID]))
			continue
		}
		for _, t := range b.Targets {
			if target := byID[t.ID]; target != nil {
				data = append(data, litellmDeploymentView(b.Alias, target, baseByID[target.ProviderID]))
			}
		}
	}
	writeJSON(w, 200, map[string]any{"data": data})
}

func (s *Server) handleLiteLLMModelDelete(w http.ResponseWriter, r *http.Request, auth *Auth) {
	var body struct {
		ID string `json:"id"`
	}
	if perr := readJSONBody(r, 1<<20, &body); perr != nil {
		writeProxyError(w, perr)
		return
	}
	binding, err := s.store.GetBindingByID(r.Context(), body.ID)
	if err != nil {
		internalErr(w, "failed to load binding")
		return
	}
	if binding == nil {
		writeProxyError(w, apierr.Newf(404, "model_not_found", "no deployment with id '%s'", body.ID))
		return
	}
	modelName, perr := s.deleteDeployment(r.Context(), auth, binding)
	s.invalidate()
	if perr != nil {
		writeProxyError(w, perr)
		return
	}
	writeJSON(w, 200, map[string]any{"deleted": body.ID, "model_name": modelName})
}

// deleteDeployment removes a model and returns the model_name it served
// under. A target of several-target aliases leaves them first. An alias
// with one deployment left becomes that model again.
func (s *Server) deleteDeployment(ctx context.Context, auth *Auth, binding *store.ModelBinding) (string, *apierr.ProxyError) {
	internal := func(msg string) (string, *apierr.ProxyError) {
		return "", apierr.New(500, "internal_error", msg)
	}
	names, err := s.store.ListBindingsTargeting(ctx, binding.ID)
	if err != nil {
		return internal("failed to check aliases")
	}
	var aliases []*store.ModelBinding
	for _, name := range names {
		alias, err := s.store.GetBindingByAlias(ctx, name)
		if err != nil || alias == nil {
			return internal("failed to load alias")
		}
		if alias.Strategy == "" {
			return "", apierr.Newf(409, "model_in_use",
				"'%s' is the target of '%s'; delete or repoint that first", binding.Alias, name)
		}
		aliases = append(aliases, alias)
	}

	modelName := binding.Alias
	for _, alias := range aliases {
		if isDeployment(alias.Alias, binding) {
			modelName = alias.Alias
		}
		kept := alias.Targets[:0]
		for _, t := range alias.Targets {
			if t.ID != binding.ID {
				kept = append(kept, t)
			}
		}
		alias.Targets = kept
		audit := &store.Audit{Actor: auth.PrincipalID, Action: "model.update", TargetKind: "model", TargetRef: alias.Alias}
		if err := s.store.UpdateBinding(ctx, alias, audit); err != nil {
			return internal("failed to update alias")
		}
	}

	// Deleting a several-target alias takes its deployments with it.
	var deployments []*store.ModelBinding
	for _, t := range binding.Targets {
		target, err := s.store.GetBindingByID(ctx, t.ID)
		if err != nil {
			return internal("failed to load target")
		}
		if target != nil && binding.Strategy != "" && isDeployment(binding.Alias, target) {
			deployments = append(deployments, target)
		}
	}
	audit := &store.Audit{Actor: auth.PrincipalID, Action: "model.delete", TargetKind: "model", TargetRef: binding.Alias}
	if err := s.store.DeleteBinding(ctx, binding.ID, audit); err != nil {
		return internal("failed to delete binding")
	}
	for _, d := range deployments {
		if _, perr := s.deleteDeployment(ctx, auth, d); perr != nil {
			return "", perr
		}
	}

	for _, alias := range aliases {
		if perr := s.collapseAlias(ctx, auth, alias); perr != nil {
			return "", perr
		}
	}
	return modelName, nil
}

// collapseAlias turns an alias with one deployment left back into that model.
func (s *Server) collapseAlias(ctx context.Context, auth *Auth, alias *store.ModelBinding) *apierr.ProxyError {
	if len(alias.Targets) != 1 {
		return nil
	}
	last, err := s.store.GetBindingByID(ctx, alias.Targets[0].ID)
	if err != nil {
		return apierr.New(500, "internal_error", "failed to load target")
	}
	if last == nil || !isDeployment(alias.Alias, last) {
		return nil
	}
	names, err := s.store.ListBindingsTargeting(ctx, last.ID)
	if err != nil || len(names) != 1 {
		return nil
	}
	audit := &store.Audit{Actor: auth.PrincipalID, Action: "model.delete", TargetKind: "model", TargetRef: alias.Alias}
	if err := s.store.DeleteBinding(ctx, alias.ID, audit); err != nil {
		return apierr.New(500, "internal_error", "failed to delete binding")
	}
	last.Alias = alias.Alias
	last.Hidden = alias.Hidden
	audit = &store.Audit{Actor: auth.PrincipalID, Action: "model.update", TargetKind: "model", TargetRef: last.Alias}
	if err := s.store.UpdateBinding(ctx, last, audit); err != nil {
		return apierr.New(500, "internal_error", "failed to update binding")
	}
	return nil
}
