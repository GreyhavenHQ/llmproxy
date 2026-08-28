// Package upstream caches one HTTP client per provider configuration.
// A config change produces a new cache key and therefore a fresh client.
package upstream

import (
	"crypto/tls"
	"crypto/x509"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/greyhavenhq/llmproxy/internal/catalog"
)

type Pool struct {
	mu      sync.Mutex
	clients map[string]*http.Client
}

func New() *Pool {
	return &Pool{clients: make(map[string]*http.Client)}
}

func (p *Pool) ClientFor(route *catalog.Route) *http.Client {
	key := route.ClientKey()
	p.mu.Lock()
	defer p.mu.Unlock()
	if client, ok := p.clients[key]; ok {
		return client
	}
	tlsConfig := &tls.Config{}
	if !route.VerifyTLS {
		tlsConfig.InsecureSkipVerify = true
	} else if route.CAPEM != "" {
		roots := x509.NewCertPool()
		roots.AppendCertsFromPEM([]byte(route.CAPEM))
		tlsConfig.RootCAs = roots
	}
	transport := &http.Transport{
		DialContext:           (&net.Dialer{Timeout: route.TimeoutConnect}).DialContext,
		TLSClientConfig:       tlsConfig,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: route.TimeoutRead,
		MaxConnsPerHost:       route.MaxConcurrency, // 0 = unlimited
		MaxIdleConnsPerHost:   32,
		IdleConnTimeout:       90 * time.Second,
		ForceAttemptHTTP2:     true,
	}
	// No http.Client.Timeout: it would bound entire streamed responses.
	// Unary requests get a per-request context deadline instead.
	client := &http.Client{Transport: transport}
	p.clients[key] = client
	return client
}

func (p *Pool) Reset() {
	p.mu.Lock()
	old := p.clients
	p.clients = make(map[string]*http.Client)
	p.mu.Unlock()
	for _, c := range old {
		c.CloseIdleConnections()
	}
}
