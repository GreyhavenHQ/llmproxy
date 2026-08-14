package server

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/monadical/llmproxy/internal/apierr"
	"github.com/monadical/llmproxy/internal/secrets"
	"github.com/monadical/llmproxy/internal/store"
)

// Transparent Anthropic relay: /transparent/anthropic/{token}/{path...} is
// forwarded to the configured Anthropic base URL with the caller's own
// credentials (x-api-key or OAuth bearer) passed through byte-for-byte. The
// proxy adds nothing and rewrites nothing; the relay token in the path only
// attributes the usage to a principal. Request and response bodies stream
// through unbuffered, with usage extracted from the response on the way past.

// transparentProvider is the sentinel provider id/name on usage events from
// the relay. There is no provider row; the column carries no FK.
const transparentProvider = "transparent:anthropic"

// transparentScanCap bounds the buffer used to extract usage from a unary
// response. An oversize body still relays in full; it just goes unmetered.
const transparentScanCap = 8 << 20

func newTransparentClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			DialContext:         (&net.Dialer{Timeout: 10 * time.Second}).DialContext,
			TLSHandshakeTimeout: 10 * time.Second,
			// Non-streaming Anthropic requests can think for a long time
			// before headers arrive; be generous.
			ResponseHeaderTimeout: 10 * time.Minute,
			MaxIdleConnsPerHost:   32,
			IdleConnTimeout:       90 * time.Second,
			ForceAttemptHTTP2:     true,
			// The proxy must see response bytes as sent to extract usage, and
			// the client negotiated no encoding (Accept-Encoding is stripped),
			// so the transport must not inject its own gzip either.
			DisableCompression: true,
		},
		// No overall timeout: it would bound entire streamed responses.
	}
}

// hopByHopHeaders never cross a proxy (RFC 9110 §7.6.1).
var hopByHopHeaders = []string{
	"Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization",
	"Proxy-Connection", "Te", "Trailer", "Transfer-Encoding", "Upgrade",
}

func copyEndToEndHeaders(dst, src http.Header) {
	dropped := map[string]bool{}
	for _, name := range hopByHopHeaders {
		dropped[name] = true
	}
	for _, name := range src.Values("Connection") {
		dropped[http.CanonicalHeaderKey(strings.TrimSpace(name))] = true
	}
	for name, values := range src {
		if dropped[name] {
			continue
		}
		dst[name] = append([]string(nil), values...)
	}
}

// maskTransparentPath hides the relay token in a path for logging: the token
// segment becomes ***<last4>. Non-relay paths come back unchanged.
func maskTransparentPath(path string) string {
	const prefix = "/transparent/anthropic/"
	rest, ok := strings.CutPrefix(path, prefix)
	if !ok || rest == "" {
		return path
	}
	token, remainder, hasMore := strings.Cut(rest, "/")
	masked := prefix + "***" + secrets.KeySuffix(token)
	if hasMore {
		masked += "/" + remainder
	}
	return masked
}

func (s *Server) handleTransparentAnthropic(w http.ResponseWriter, r *http.Request) {
	if s.cfg.TransparentAnthropicBaseURL == "" {
		writeProxyError(w, apierr.New(404, "transparent_relay_disabled",
			"the transparent Anthropic relay is disabled"))
		return
	}
	token := r.PathValue("token")
	relay, err := s.store.AuthByRelayTokenHash(r.Context(), secrets.HashAPIKey(s.secret, token))
	if err != nil {
		slog.Error("relay token lookup failed", "error", err)
		writeProxyError(w, apierr.New(500, "internal_error", "relay token lookup failed"))
		return
	}
	if relay == nil {
		writeProxyError(w, apierr.New(404, "unknown_relay_token",
			"unknown relay token; create one under /my/relay-tokens and put it in the URL path"))
		return
	}
	// Same coarse last-used refresh as API keys: at most once a minute.
	threshold := time.Now().Add(-time.Minute).UTC().Format("2006-01-02T15:04:05.000000Z")
	if !relay.LastUsedAt.Valid || relay.LastUsedAt.String < threshold {
		if err := s.store.TouchRelayToken(r.Context(), relay.TokenID); err != nil {
			slog.Warn("failed to update relay token last_used_at", "error", err)
		}
	}

	endpoint := r.PathValue("path")
	upstreamURL := strings.TrimSuffix(s.cfg.TransparentAnthropicBaseURL, "/") + "/" + endpoint
	if r.URL.RawQuery != "" {
		upstreamURL += "?" + r.URL.RawQuery
	}
	// The body streams straight through: no buffering, no size cap, no
	// rewrites. Agent contexts get large and none of it is the proxy's
	// business.
	req, err := http.NewRequestWithContext(r.Context(), r.Method, upstreamURL, r.Body)
	if err != nil {
		writeProxyError(w, apierr.New(500, "internal_error", "failed to build upstream request"))
		return
	}
	req.ContentLength = r.ContentLength
	copyEndToEndHeaders(req.Header, r.Header)
	// Cookies are proxy-local browser state; Accept-Encoding is dropped so
	// the response arrives unencoded and usage stays extractable; the tags
	// header is addressed to the proxy and has no business upstream.
	req.Header.Del("Cookie")
	req.Header.Del("Accept-Encoding")
	req.Header.Del(tagsHeader)

	started := time.Now()
	resp, err := s.transparent.Do(req)
	if err != nil {
		outcome, cancelled, kind := "unreachable", false, errClass(err)
		if r.Context().Err() != nil {
			outcome, cancelled, kind = "cancelled", true, ""
		}
		s.recordTransparentAsync(relay, r.Method, "", endpoint, clientFrom(r), tagsFrom(r), usageOutcome{
			Outcome: outcome, ErrorKind: kind, Cancelled: cancelled,
			DurationMs: time.Since(started).Milliseconds(),
		})
		writeProxyError(w, apierr.Newf(502, "provider_unreachable",
			"request to the Anthropic API failed: %s", errClass(err)))
		return
	}
	defer resp.Body.Close()

	copyEndToEndHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)

	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		s.relayTransparentStream(w, r, relay, endpoint, resp, started)
		return
	}
	s.relayTransparentUnary(w, r, relay, endpoint, resp, started)
}

// relayTransparentUnary streams the body through while keeping a capped copy
// to pull model and usage out of once the response completes.
func (s *Server) relayTransparentUnary(w http.ResponseWriter, r *http.Request,
	relay *store.RelayAuthResult, endpoint string, resp *http.Response, started time.Time) {
	flusher, _ := w.(http.Flusher)
	var scan []byte
	outcome, cancelled, kind := "ok", false, ""
	buf := make([]byte, 32*1024)
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if _, writeErr := w.Write(buf[:n]); writeErr != nil {
				outcome, cancelled = "cancelled", true
				break
			}
			if flusher != nil {
				flusher.Flush()
			}
			if len(scan) <= transparentScanCap {
				scan = append(scan, buf[:n]...)
			}
		}
		if readErr != nil {
			if readErr != io.EOF {
				if r.Context().Err() != nil {
					outcome, cancelled = "cancelled", true
				} else {
					outcome, kind = "upstream_error", errClass(readErr)
				}
			}
			break
		}
	}
	var model string
	var usage map[string]any
	if resp.StatusCode >= 400 {
		outcome = "upstream_error"
		// A read failure above already classified the transport error; the
		// body it left behind is truncated and would classify worse.
		if kind == "" && len(scan) <= transparentScanCap {
			kind = errorKindFromBody(scan)
		}
	} else if len(scan) <= transparentScanCap &&
		strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") {
		var doc struct {
			Model string         `json:"model"`
			Usage map[string]any `json:"usage"`
		}
		if err := json.Unmarshal(scan, &doc); err == nil {
			model, usage = doc.Model, doc.Usage
		}
	}
	s.recordTransparentAsync(relay, r.Method, model, endpoint, clientFrom(r), tagsFrom(r), usageOutcome{
		StatusCode: resp.StatusCode, Outcome: outcome, ErrorKind: kind, Cancelled: cancelled,
		Usage: usage, DurationMs: time.Since(started).Milliseconds(),
	})
}

// relayTransparentStream forwards SSE bytes untouched while scanning completed
// lines for Anthropic usage: message_start carries the model plus input and
// cache tokens (nested under message), message_delta carries cumulative
// output tokens. The max-wins merge accumulates both shapes.
func (s *Server) relayTransparentStream(w http.ResponseWriter, r *http.Request,
	relay *store.RelayAuthResult, endpoint string, resp *http.Response, started time.Time) {
	flusher, _ := w.(http.Flusher)
	var model string
	var usage map[string]any
	outcome, cancelled, kind := "ok", false, ""
	if resp.StatusCode >= 400 {
		outcome = "upstream_error"
	}
	var pending []byte
	buf := make([]byte, 32*1024)
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if _, writeErr := w.Write(buf[:n]); writeErr != nil {
				outcome, cancelled = "cancelled", true
				break
			}
			if flusher != nil {
				flusher.Flush()
			}
			pending = append(pending, buf[:n]...)
			for {
				idx := bytes.IndexByte(pending, '\n')
				if idx < 0 {
					break
				}
				if m, u := parseAnthropicSSELine(pending[:idx]); u != nil {
					usage = mergeUsage(usage, u)
					if m != "" {
						model = m
					}
				}
				pending = pending[idx+1:]
			}
			// Bound the scan buffer against upstreams that never send newlines.
			if len(pending) > 1<<20 {
				pending = pending[len(pending)-64*1024:]
			}
		}
		if readErr != nil {
			if readErr == io.EOF {
				break
			}
			if r.Context().Err() != nil {
				outcome, cancelled = "cancelled", true
			} else {
				outcome, kind = "upstream_error", errClass(readErr)
			}
			break
		}
	}
	if m, u := parseAnthropicSSELine(pending); u != nil {
		usage = mergeUsage(usage, u)
		if m != "" {
			model = m
		}
	}
	s.recordTransparentAsync(relay, r.Method, model, endpoint, clientFrom(r), tagsFrom(r), usageOutcome{
		StatusCode: resp.StatusCode, Outcome: outcome, ErrorKind: kind, Cancelled: cancelled,
		Streamed: true, Usage: usage, DurationMs: time.Since(started).Milliseconds(),
	})
}

// parseAnthropicSSELine extracts model and usage from one SSE line, if
// present. The cheap marker check skips content chunks.
func parseAnthropicSSELine(line []byte) (string, map[string]any) {
	line = bytes.TrimSpace(line)
	if !bytes.HasPrefix(line, dataPrefix) {
		return "", nil
	}
	data := bytes.TrimSpace(line[len(dataPrefix):])
	if !bytes.Contains(data, usageMarker) {
		return "", nil
	}
	var doc struct {
		Usage   map[string]any `json:"usage"`
		Message struct {
			Model string         `json:"model"`
			Usage map[string]any `json:"usage"`
		} `json:"message"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return "", nil
	}
	if doc.Usage != nil {
		return doc.Message.Model, doc.Usage
	}
	return doc.Message.Model, doc.Message.Usage
}

func (s *Server) recordTransparentAsync(relay *store.RelayAuthResult, method, model, endpoint, client, tags string, rec usageOutcome) {
	// HEAD and OPTIONS are connectivity probes (Claude Code sends one per
	// session start): nothing to meter, and Anthropic's root answers them
	// 404, so recording would fill the request log with phantom failures.
	if method == http.MethodHead || method == http.MethodOptions {
		return
	}
	s.recordAsync(func(ctx context.Context) {
		ev := &store.UsageEvent{
			PrincipalID:  relay.PrincipalID,
			APIKeyID:     relay.TokenID,
			ProviderID:   transparentProvider,
			Alias:        model,
			UpstreamName: model,
			Endpoint:     endpoint,
			Client:       client,
			Tags:         tags,
			Outcome:      rec.Outcome,
			ErrorKind:    rec.ErrorKind,
			Cancelled:    rec.Cancelled,
			Streamed:     rec.Streamed,
			DurationMs:   rec.DurationMs,
		}
		if rec.StatusCode != 0 {
			ev.StatusCode = sql.NullInt64{Int64: int64(rec.StatusCode), Valid: true}
		}
		s.priceAndInsert(ctx, ev, transparentProvider, anthropicQuantities(rec.Usage), model)
	})
}
