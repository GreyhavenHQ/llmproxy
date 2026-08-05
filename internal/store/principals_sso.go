package store

import (
	"context"
	"fmt"
	"strings"

	"github.com/monadical/llmproxy/internal/secrets"
)

// sanitizePrincipalName reduces an IdP display name to the local naming rules.
func sanitizePrincipalName(name string) string {
	name = strings.ToLower(name)
	if at := strings.IndexByte(name, '@'); at > 0 {
		name = name[:at]
	}
	var b strings.Builder
	for _, ch := range name {
		switch {
		case ch >= 'a' && ch <= 'z', ch >= '0' && ch <= '9', ch == '.', ch == '_', ch == '-':
			b.WriteRune(ch)
		default:
			b.WriteByte('-')
		}
	}
	cleaned := strings.Trim(b.String(), "-.")
	if cleaned == "" {
		return "user"
	}
	if len(cleaned) > 100 {
		cleaned = cleaned[:100]
	}
	return cleaned
}

// UpsertSSOPrincipal creates or refreshes a principal on SSO login. The stable
// IdP subject is the join key, never the email or name; role and email are
// reconciled on every login.
func (s *Store) UpsertSSOPrincipal(ctx context.Context, sub, email, preferredName, role string) (*Principal, error) {
	existing, err := s.GetPrincipalByExternalSub(ctx, sub)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		if existing.Role != role || existing.Email.String != email {
			if _, err := s.db.ExecContext(ctx,
				s.q(`UPDATE principal SET role = ?, email = ? WHERE id = ?`),
				role, email, existing.ID); err != nil {
				return nil, err
			}
			existing.Role = role
		}
		return existing, nil
	}
	base := sanitizePrincipalName(preferredName)
	for i := 0; i < 50; i++ {
		candidate := base
		if i > 0 {
			candidate = fmt.Sprintf("%s-%d", base, i+1)
		}
		taken, err := s.GetPrincipalByName(ctx, candidate)
		if err != nil {
			return nil, err
		}
		if taken != nil {
			continue
		}
		p := &Principal{ID: secrets.NewID(), Name: candidate, Kind: "user", Role: role, CreatedAt: Now()}
		if _, err := s.db.ExecContext(ctx, s.q(`
			INSERT INTO principal (id, name, kind, role, external_sub, email, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`),
			p.ID, p.Name, p.Kind, p.Role, sub, email, p.CreatedAt); err != nil {
			return nil, err
		}
		return p, nil
	}
	return nil, fmt.Errorf("could not find a free principal name for '%s'", base)
}
