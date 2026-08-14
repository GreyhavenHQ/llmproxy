package server

import (
	"database/sql"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/monadical/llmproxy/internal/apierr"
	"github.com/monadical/llmproxy/internal/secrets"
	"github.com/monadical/llmproxy/internal/store"
)

// LiteLLM management-API compatibility, so existing registration tooling
// works unchanged: POST /model/new, GET /model/info, POST /model/delete.
//
// The mapping: a LiteLLM "deployment" (model_name + litellm_params) becomes a
// provider (api_base + api_key, reused across deployments with the same
// api_base) plus a model binding (model_name as the alias, litellm_params
// .model as the upstream name). One deliberate divergence: aliases are
// globally unique, so re-registering the same mapping is an idempotent
// success but a second different deployment under one model_name (LiteLLM's
// load balancing) is rejected with 409.

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

func litellmDeploymentView(b *store.ModelBinding, apiBase string) map[string]any {
	return map[string]any{
		"model_name": b.Alias,
		"litellm_params": map[string]any{
			"model":               b.UpstreamName,
			"api_base":            apiBase,
			"custom_llm_provider": "openai",
		},
		"model_info": map[string]any{"id": b.ID},
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
			Model   string `json:"model"`
			APIBase string `json:"api_base"`
			APIKey  string `json:"api_key"`
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
	if existing != nil {
		if existing.ProviderID == provider.ID && existing.UpstreamName == body.LiteLLMParams.Model {
			writeJSON(w, 200, litellmDeploymentView(existing, provider.BaseURL))
			return
		}
		writeProxyError(w, apierr.Newf(409, "alias_exists",
			"model_name '%s' is already bound to a different deployment; aliases are globally unique",
			body.ModelName))
		return
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
	audit := &store.Audit{Actor: auth.PrincipalID, Action: "model.create", TargetKind: "model", TargetRef: binding.Alias}
	if err := s.store.CreateBinding(r.Context(), binding, audit); err != nil {
		internalErr(w, "failed to create binding")
		return
	}
	s.invalidate()
	writeJSON(w, 200, litellmDeploymentView(binding, provider.BaseURL))
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
	data := make([]map[string]any, 0, len(bindings))
	for i := range bindings {
		data = append(data, litellmDeploymentView(&bindings[i], baseByID[bindings[i].ProviderID]))
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
	audit := &store.Audit{Actor: auth.PrincipalID, Action: "model.delete", TargetKind: "model", TargetRef: binding.Alias}
	if err := s.store.DeleteBinding(r.Context(), binding.ID, audit); err != nil {
		internalErr(w, "failed to delete binding")
		return
	}
	s.invalidate()
	writeJSON(w, 200, map[string]any{"deleted": body.ID, "model_name": binding.Alias})
}
