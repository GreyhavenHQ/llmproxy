// Package catalog: alias resolution.
//
// The one rule: a caller-facing model name is a globally unique alias mapping
// to exactly one (provider, upstream model) pair, so resolution can never be
// ambiguous; ambiguity is rejected at write time by the alias unique
// constraint. A name may reach that pair through one hop, by pointing at
// another binding, which the store resolves in the same query — so this stays
// a single lookup and a chain can never loop. Resolution takes the endpoint
// into account: the binding's provider must be enabled and the endpoint in
// the capability set.
package catalog

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/monadical/llmproxy/internal/apierr"
	"github.com/monadical/llmproxy/internal/secrets"
	"github.com/monadical/llmproxy/internal/store"
)

// Capabilities is what a binding may declare. Every entry that is also an
// Endpoint gates routing; "vision" is declarative only, describing what the
// model accepts, and is never checked on the request path.
var Capabilities = []string{"chat", "chat_stream", "completions", "embeddings", "transcription", "vision"}
var Endpoints = []string{"chat", "completions", "embeddings", "transcription"}

var DefaultPaths = map[string]string{
	"chat":          "/chat/completions",
	"completions":   "/completions",
	"embeddings":    "/embeddings",
	"transcription": "/audio/transcriptions",
	"models":        "/models",
}

func IsCapability(name string) bool {
	for _, c := range Capabilities {
		if c == name {
			return true
		}
	}
	return false
}

func IsEndpoint(name string) bool {
	for _, e := range Endpoints {
		if e == name {
			return true
		}
	}
	return false
}

type Route struct {
	ProviderID     string
	ProviderName   string
	WireFormat     string
	BaseURL        string
	Credential     string // decrypted; empty means no upstream auth
	VerifyTLS      bool
	CAPEM          string
	TimeoutConnect time.Duration
	TimeoutRead    time.Duration
	MaxConcurrency int
	Alias          string
	// TargetAlias is the alias this one points at, empty when the model
	// routes to a provider directly. Pricing falls through it.
	TargetAlias  string
	UpstreamName string
	Capabilities map[string]bool
	URLOverrides map[string]string
}

func (r *Route) EndpointURL(endpoint string) string {
	if override, ok := r.URLOverrides[endpoint]; ok && override != "" {
		return override
	}
	return strings.TrimRight(r.BaseURL, "/") + DefaultPaths[endpoint]
}

// ClientKey identifies the upstream client configuration; a config change
// yields a new key and therefore a fresh HTTP client.
func (r *Route) ClientKey() string {
	return strings.Join([]string{
		r.ProviderID, r.BaseURL, boolStr(r.VerifyTLS), r.CAPEM,
		r.TimeoutConnect.String(), r.TimeoutRead.String(),
		time.Duration(r.MaxConcurrency).String(),
	}, "\x00")
}

func boolStr(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

type cacheEntry struct {
	expires time.Time
	route   *Route // nil means "known absent"
}

type Catalog struct {
	store  *store.Store
	secret []byte
	ttl    time.Duration

	mu    sync.Mutex
	cache map[string]cacheEntry
}

func New(st *store.Store, secret []byte, ttl time.Duration) *Catalog {
	return &Catalog{store: st, secret: secret, ttl: ttl, cache: make(map[string]cacheEntry)}
}

func (c *Catalog) Invalidate() {
	c.mu.Lock()
	c.cache = make(map[string]cacheEntry)
	c.mu.Unlock()
}

func (c *Catalog) Resolve(ctx context.Context, alias, endpoint string, streaming bool) (*Route, *apierr.ProxyError) {
	route, err := c.lookup(ctx, alias)
	if err != nil {
		return nil, apierr.New(500, "internal_error", "catalog lookup failed")
	}
	if route == nil {
		return nil, apierr.Newf(404, "model_not_found",
			"model '%s' does not exist or its provider is disabled", alias).WithParam("model")
	}
	needed := []string{endpoint}
	if streaming && endpoint == "chat" {
		needed = append(needed, "chat_stream")
	}
	var missing []string
	for _, cap := range needed {
		if !route.Capabilities[cap] {
			missing = append(missing, cap)
		}
	}
	if len(missing) > 0 {
		supported := make([]string, 0, len(route.Capabilities))
		for cap := range route.Capabilities {
			supported = append(supported, cap)
		}
		sort.Strings(supported)
		return nil, apierr.Newf(400, "endpoint_not_supported",
			"model '%s' does not support %v; supported capabilities: %v",
			alias, missing, supported).WithParam("model")
	}
	return route, nil
}

func (c *Catalog) lookup(ctx context.Context, alias string) (*Route, error) {
	now := time.Now()
	c.mu.Lock()
	if entry, ok := c.cache[alias]; ok && entry.expires.After(now) {
		c.mu.Unlock()
		return entry.route, nil
	}
	c.mu.Unlock()

	route, err := c.load(ctx, alias)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.cache[alias] = cacheEntry{expires: now.Add(c.ttl), route: route}
	c.mu.Unlock()
	return route, nil
}

func (c *Catalog) load(ctx context.Context, alias string) (*Route, error) {
	binding, provider, overrides, err := c.store.ResolveAlias(ctx, alias)
	if err != nil || binding == nil || provider == nil {
		return nil, err
	}
	credential := ""
	if provider.CredentialCiphertext.Valid && provider.CredentialCiphertext.String != "" {
		credential, err = secrets.DecryptCredential(c.secret, provider.CredentialCiphertext.String)
		if err != nil {
			return nil, err
		}
	}
	caps := make(map[string]bool)
	for _, cap := range strings.Split(binding.CapabilitySet, ",") {
		if cap != "" {
			caps[cap] = true
		}
	}
	return &Route{
		ProviderID:     provider.ID,
		ProviderName:   provider.Name,
		WireFormat:     provider.WireFormat,
		BaseURL:        provider.BaseURL,
		Credential:     credential,
		VerifyTLS:      provider.VerifyTLS,
		CAPEM:          provider.CAPEM.String,
		TimeoutConnect: time.Duration(provider.TimeoutConnect * float64(time.Second)),
		TimeoutRead:    time.Duration(provider.TimeoutRead * float64(time.Second)),
		MaxConcurrency: int(provider.MaxConcurrency.Int64),
		Alias:          binding.Alias,
		TargetAlias:    binding.TargetAlias,
		UpstreamName:   binding.UpstreamName,
		Capabilities:   caps,
		URLOverrides:   overrides,
	}, nil
}

// RouteForProvider builds a model-less route for provider-level calls
// (discovery). The credential is decrypted the same way as for inference.
func RouteForProvider(p *store.Provider, secret []byte) (*Route, error) {
	credential := ""
	if p.CredentialCiphertext.Valid && p.CredentialCiphertext.String != "" {
		var err error
		credential, err = secrets.DecryptCredential(secret, p.CredentialCiphertext.String)
		if err != nil {
			return nil, err
		}
	}
	return &Route{
		ProviderID:     p.ID,
		ProviderName:   p.Name,
		WireFormat:     p.WireFormat,
		BaseURL:        p.BaseURL,
		Credential:     credential,
		VerifyTLS:      p.VerifyTLS,
		CAPEM:          p.CAPEM.String,
		TimeoutConnect: time.Duration(p.TimeoutConnect * float64(time.Second)),
		TimeoutRead:    time.Duration(p.TimeoutRead * float64(time.Second)),
		MaxConcurrency: int(p.MaxConcurrency.Int64),
		Capabilities:   map[string]bool{},
		URLOverrides:   map[string]string{},
	}, nil
}
