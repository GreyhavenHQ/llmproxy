package server

import (
	"net/http"

	"github.com/monadical/llmproxy/internal/apierr"
	"github.com/monadical/llmproxy/internal/secrets"
	"github.com/monadical/llmproxy/internal/store"
)

// Self-service relay tokens: same shape as /my/keys, but the minted secret is
// a relay token for /transparent/anthropic/, not an API key.

func relayTokenView(rt *store.RelayToken) map[string]any {
	view := map[string]any{
		"id":           rt.ID,
		"token_suffix": rt.Suffix,
		"label":        rt.Label,
		"created_at":   rt.CreatedAt,
		"last_used_at": nil,
	}
	if rt.LastUsedAt.Valid {
		view["last_used_at"] = rt.LastUsedAt.String
	}
	return view
}

func (s *Server) handleMyRelayTokenCreate(w http.ResponseWriter, r *http.Request, auth *Auth) {
	var body struct {
		Label string `json:"label"`
	}
	if perr := readJSONBody(r, 1<<20, &body); perr != nil {
		writeProxyError(w, perr)
		return
	}
	if len(body.Label) > 120 {
		writeProxyError(w, apierr.New(400, "invalid_label", "label must be at most 120 characters"))
		return
	}
	plaintext := secrets.GenerateRelayToken()
	token, err := s.store.CreateRelayToken(r.Context(), auth.PrincipalID,
		secrets.HashAPIKey(s.secret, plaintext), secrets.KeySuffix(plaintext), body.Label, nil)
	if err != nil {
		writeProxyError(w, apierr.New(500, "internal_error", "failed to create relay token"))
		return
	}
	view := relayTokenView(token)
	view["token"] = plaintext // shown exactly once; only the keyed hash is stored
	writeJSON(w, 201, view)
}

func (s *Server) handleMyRelayTokensList(w http.ResponseWriter, r *http.Request, auth *Auth) {
	tokens, err := s.store.ListRelayTokens(r.Context(), auth.PrincipalID, 500, 0)
	if err != nil {
		writeProxyError(w, apierr.New(500, "internal_error", "failed to list relay tokens"))
		return
	}
	views := make([]map[string]any, 0, len(tokens))
	for i := range tokens {
		views = append(views, relayTokenView(&tokens[i]))
	}
	writeJSON(w, 200, map[string]any{"relay_tokens": views})
}

func (s *Server) handleMyRelayTokenDelete(w http.ResponseWriter, r *http.Request, auth *Auth) {
	token, err := s.store.GetRelayToken(r.Context(), r.PathValue("id"), auth.PrincipalID)
	if err != nil {
		writeProxyError(w, apierr.New(500, "internal_error", "failed to load relay token"))
		return
	}
	if token == nil {
		writeProxyError(w, apierr.New(404, "relay_token_not_found", "no such relay token on your principal"))
		return
	}
	if err := s.store.DeleteRelayToken(r.Context(), token.ID, nil); err != nil {
		writeProxyError(w, apierr.New(500, "internal_error", "failed to delete relay token"))
		return
	}
	writeJSON(w, 200, map[string]any{"deleted": token.ID})
}
