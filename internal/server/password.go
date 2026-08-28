package server

import (
	"crypto/subtle"
	"net/http"
	"time"

	"github.com/greyhavenhq/llmproxy/internal/apierr"
)

// Password login for the local admin principal. The password comes from
// LLMPROXY_ADMIN_PASSWORD or a file generated at first boot; it is never
// stored in the database. This path works with or without SSO configured, so
// an IdP outage cannot lock the operator out of their own proxy.

func (s *Server) handleAuthPassword(w http.ResponseWriter, r *http.Request) {
	if s.adminPassword == "" || s.localAdminID == "" {
		writeProxyError(w, apierr.New(404, "password_login_disabled", "password login is not enabled"))
		return
	}
	// Same-origin check: this endpoint sets a cookie, so treat it like any
	// other session mutation.
	if perr := s.checkSameOrigin(r); perr != nil {
		writeProxyError(w, perr)
		return
	}
	var body struct {
		Password string `json:"password"`
	}
	if perr := readJSONBody(r, 1<<20, &body); perr != nil {
		writeProxyError(w, perr)
		return
	}
	if subtle.ConstantTimeCompare([]byte(body.Password), []byte(s.adminPassword)) != 1 {
		// Blunt brute-force damper; real rate limiting is a reverse-proxy job.
		time.Sleep(400 * time.Millisecond)
		writeProxyError(w, apierr.New(401, "wrong_password", "wrong password"))
		return
	}
	if err := s.issueSessionCookie(w, r, s.localAdminID); err != nil {
		internalErr(w, "failed to create your session")
		return
	}
	writeJSON(w, 200, map[string]any{
		"name": s.cfg.LocalAdminName,
		"role": "admin",
	})
}
