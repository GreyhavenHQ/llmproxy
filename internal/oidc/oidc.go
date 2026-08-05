// Package oidc implements a minimal OpenID Connect relying party:
// endpoint discovery, the authorization-code redirect, the code exchange, and
// a userinfo lookup.
//
// Identity comes from the userinfo endpoint, fetched server-to-server with
// the access token obtained in the code exchange. That keeps the trust chain
// (TLS to the IdP plus client authentication) without any local JWT
// verification machinery.
package oidc

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Config struct {
	Issuer       string
	ClientID     string
	ClientSecret string
	RedirectURL  string
	Scopes       string
	GroupsClaim  string
}

type Client struct {
	cfg          Config
	authorizeURL string
	tokenURL     string
	userinfoURL  string
	http         *http.Client
}

type Identity struct {
	Sub           string
	Email         string
	PreferredName string
	Groups        []string
}

func Discover(ctx context.Context, cfg Config) (*Client, error) {
	httpClient := &http.Client{Timeout: 15 * time.Second}
	wellKnown := strings.TrimRight(cfg.Issuer, "/") + "/.well-known/openid-configuration"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, wellKnown, nil)
	if err != nil {
		return nil, err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("OIDC discovery failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("OIDC discovery returned HTTP %d", resp.StatusCode)
	}
	var doc struct {
		AuthorizationEndpoint string `json:"authorization_endpoint"`
		TokenEndpoint         string `json:"token_endpoint"`
		UserinfoEndpoint      string `json:"userinfo_endpoint"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		return nil, fmt.Errorf("OIDC discovery document invalid: %w", err)
	}
	if doc.AuthorizationEndpoint == "" || doc.TokenEndpoint == "" || doc.UserinfoEndpoint == "" {
		return nil, fmt.Errorf("OIDC discovery document is missing required endpoints")
	}
	return &Client{
		cfg:          cfg,
		authorizeURL: doc.AuthorizationEndpoint,
		tokenURL:     doc.TokenEndpoint,
		userinfoURL:  doc.UserinfoEndpoint,
		http:         httpClient,
	}, nil
}

func (c *Client) AuthCodeURL(state string) string {
	q := url.Values{
		"response_type": {"code"},
		"client_id":     {c.cfg.ClientID},
		"redirect_uri":  {c.cfg.RedirectURL},
		"scope":         {c.cfg.Scopes},
		"state":         {state},
	}
	sep := "?"
	if strings.Contains(c.authorizeURL, "?") {
		sep = "&"
	}
	return c.authorizeURL + sep + q.Encode()
}

// Exchange trades an authorization code for an access token.
func (c *Client) Exchange(ctx context.Context, code string) (string, error) {
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {c.cfg.RedirectURL},
		"client_id":     {c.cfg.ClientID},
		"client_secret": {c.cfg.ClientSecret},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.tokenURL,
		strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("token exchange failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("token exchange returned HTTP %d", resp.StatusCode)
	}
	var doc struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil || doc.AccessToken == "" {
		return "", fmt.Errorf("token exchange response invalid")
	}
	return doc.AccessToken, nil
}

func (c *Client) Userinfo(ctx context.Context, accessToken string) (*Identity, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.userinfoURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("userinfo request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("userinfo returned HTTP %d", resp.StatusCode)
	}
	var claims map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&claims); err != nil {
		return nil, fmt.Errorf("userinfo response invalid: %w", err)
	}
	sub, _ := claims["sub"].(string)
	if sub == "" {
		return nil, fmt.Errorf("userinfo response has no 'sub'")
	}
	identity := &Identity{Sub: sub}
	identity.Email, _ = claims["email"].(string)
	for _, key := range []string{"preferred_username", "nickname", "name"} {
		if v, ok := claims[key].(string); ok && v != "" {
			identity.PreferredName = v
			break
		}
	}
	if identity.PreferredName == "" {
		identity.PreferredName = identity.Email
	}
	if raw, ok := claims[c.cfg.GroupsClaim].([]any); ok {
		for _, g := range raw {
			if group, ok := g.(string); ok {
				identity.Groups = append(identity.Groups, group)
			}
		}
	}
	return identity, nil
}
