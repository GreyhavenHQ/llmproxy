// Package config holds runtime configuration, environment-driven with an
// LLMPROXY_ prefix.
package config

import (
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	// DatabaseURL is a SQLite file path (default) or a postgres:// URL.
	DatabaseURL string
	// KeySecret is the HMAC / encryption secret. If empty, a random secret is
	// generated and stored in SecretFile.
	KeySecret  string
	SecretFile string

	Host          string
	Port          int
	AllowNonlocal bool

	// OIDCIssuer unset means local single-admin mode (no SSO).
	OIDCIssuer        string
	OIDCClientID      string
	OIDCClientSecret  string
	OIDCRedirectURL   string
	OIDCScopes        string
	OIDCGroupsClaim   string
	OIDCAdminGroup    string
	OIDCRequiredGroup string
	SessionTTL        time.Duration

	LocalAdminName string
	// AdminPassword enables browser password login for the local admin
	// principal. Empty means: generate one at first boot and store it in
	// AdminPasswordFile. Password login stays available alongside SSO as a
	// break-glass path unless AdminPasswordDisabled is set.
	AdminPassword         string
	AdminPasswordFile     string
	AdminPasswordDisabled bool

	CatalogTTL        time.Duration
	MaxBodyBytes      int64
	MaxEmbeddingBatch int
	PricingFile       string

	// TransparentAnthropicBaseURL is where /transparent/anthropic/ relays to.
	// Overridable for tests; empty disables the relay.
	TransparentAnthropicBaseURL string

	// LogLevel: debug | info | warn | error. Info logs one line per request.
	LogLevel string
}

func (c Config) LocalMode() bool { return c.OIDCIssuer == "" }

func FromEnv() Config {
	return Config{
		DatabaseURL:           envStr("LLMPROXY_DATABASE_URL", "llmproxy.db"),
		KeySecret:             envStr("LLMPROXY_KEY_SECRET", ""),
		SecretFile:            envStr("LLMPROXY_SECRET_FILE", ".llmproxy/secret"),
		Host:                  envStr("LLMPROXY_HOST", "127.0.0.1"),
		Port:                  envInt("LLMPROXY_PORT", 4000),
		AllowNonlocal:         envBool("LLMPROXY_ALLOW_NONLOCAL", false),
		OIDCIssuer:            envStr("LLMPROXY_OIDC_ISSUER", ""),
		OIDCClientID:          envStr("LLMPROXY_OIDC_CLIENT_ID", ""),
		OIDCClientSecret:      envStr("LLMPROXY_OIDC_CLIENT_SECRET", ""),
		OIDCRedirectURL:       envStr("LLMPROXY_OIDC_REDIRECT_URL", ""),
		OIDCScopes:            envStr("LLMPROXY_OIDC_SCOPES", "openid profile email"),
		OIDCGroupsClaim:       envStr("LLMPROXY_OIDC_GROUPS_CLAIM", "groups"),
		OIDCAdminGroup:        envStr("LLMPROXY_OIDC_ADMIN_GROUP", ""),
		OIDCRequiredGroup:     envStr("LLMPROXY_OIDC_REQUIRED_GROUP", ""),
		SessionTTL:            envDuration("LLMPROXY_SESSION_TTL", 12*time.Hour),
		LocalAdminName:        envStr("LLMPROXY_LOCAL_ADMIN_NAME", "local-admin"),
		AdminPassword:         envStr("LLMPROXY_ADMIN_PASSWORD", ""),
		AdminPasswordFile:     envStr("LLMPROXY_ADMIN_PASSWORD_FILE", ".llmproxy/admin-password"),
		AdminPasswordDisabled: envBool("LLMPROXY_ADMIN_PASSWORD_DISABLED", false),
		CatalogTTL:            envDuration("LLMPROXY_CATALOG_TTL", 5*time.Second),
		MaxBodyBytes:          envInt64("LLMPROXY_MAX_BODY_BYTES", 10<<20),
		MaxEmbeddingBatch:     envInt("LLMPROXY_MAX_EMBEDDING_BATCH", 2048),
		PricingFile:           envStr("LLMPROXY_PRICING_FILE", ""),
		LogLevel:              envStr("LLMPROXY_LOG_LEVEL", "info"),

		TransparentAnthropicBaseURL: envStr("LLMPROXY_TRANSPARENT_ANTHROPIC_BASE_URL", "https://api.anthropic.com"),
	}
}

// SlogLevel maps LogLevel to slog, defaulting to Info on unknown values.
func (c Config) SlogLevel() slog.Level {
	switch strings.ToLower(c.LogLevel) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

func envStr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}

func envInt64(key string, fallback int64) int64 {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			return n
		}
	}
	return fallback
}

func envBool(key string, fallback bool) bool {
	if v := os.Getenv(key); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return fallback
}

func envDuration(key string, fallback time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return fallback
}
