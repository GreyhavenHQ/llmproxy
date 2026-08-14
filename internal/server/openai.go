package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/monadical/llmproxy/internal/apierr"
	"github.com/monadical/llmproxy/internal/catalog"
)

// OpenAI-compatible ingress. The request body is forwarded as received except
// for two rewrites: the model field is replaced with the upstream model name,
// and stream_options.include_usage is forced on for streamed requests so
// usage arrives in the final SSE chunk. Message content (vision image parts,
// tool calls, everything else) is kept as raw bytes and never inspected.

func (s *Server) handleListModels(w http.ResponseWriter, r *http.Request, auth *Auth) {
	endpoint := r.URL.Query().Get("endpoint")
	if endpoint != "" && !catalog.IsCapability(endpoint) {
		writeProxyError(w, apierr.Newf(400, "invalid_endpoint",
			"'endpoint' must be one of %v", catalog.Capabilities))
		return
	}
	bindings, err := s.store.ListServableBindings(r.Context())
	if err != nil {
		writeProxyError(w, apierr.New(500, "internal_error", "failed to list models"))
		return
	}
	data := make([]map[string]any, 0, len(bindings))
	for _, b := range bindings {
		if endpoint != "" && !capabilitySetHas(b.CapabilitySet, endpoint) {
			continue
		}
		created := int64(0)
		if t, err := time.Parse("2006-01-02T15:04:05.000000Z", b.CreatedAt); err == nil {
			created = t.Unix()
		}
		data = append(data, map[string]any{
			"id":       b.Alias,
			"object":   "model",
			"created":  created,
			"owned_by": b.ProviderName,
		})
	}
	writeJSON(w, 200, map[string]any{"object": "list", "data": data})
}

func capabilitySetHas(set, cap string) bool {
	for _, c := range strings.Split(set, ",") {
		if c == cap {
			return true
		}
	}
	return false
}

func (s *Server) handleChatCompletions(w http.ResponseWriter, r *http.Request, auth *Auth) {
	s.proxyCompletion(w, r, auth, "chat")
}

func (s *Server) handleCompletions(w http.ResponseWriter, r *http.Request, auth *Auth) {
	s.proxyCompletion(w, r, auth, "completions")
}

func (s *Server) handleEmbeddings(w http.ResponseWriter, r *http.Request, auth *Auth) {
	s.proxyCompletion(w, r, auth, "embeddings")
}

func (s *Server) proxyCompletion(w http.ResponseWriter, r *http.Request, auth *Auth, endpoint string) {
	body, perr := readLimited(r, s.cfg.MaxBodyBytes)
	if perr != nil {
		writeProxyError(w, perr)
		return
	}
	// Top-level fields only; nested values (messages, tools, images) stay raw.
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		writeProxyError(w, apierr.New(400, "invalid_json", "request body is not valid JSON"))
		return
	}
	var model string
	if raw, ok := fields["model"]; !ok || json.Unmarshal(raw, &model) != nil || model == "" {
		writeProxyError(w, apierr.New(400, "model_required",
			"'model' is required and must be a string").WithParam("model"))
		return
	}
	stream := false
	if endpoint != "embeddings" {
		if raw, ok := fields["stream"]; ok {
			_ = json.Unmarshal(raw, &stream)
		}
	} else if perr := s.checkEmbeddingBatch(fields); perr != nil {
		writeProxyError(w, perr)
		return
	}

	route, rerr := s.catalog.Resolve(r.Context(), model, endpoint, stream)
	if rerr != nil {
		writeProxyError(w, rerr)
		return
	}

	fields["model"], _ = json.Marshal(route.UpstreamName)
	if stream {
		var opts map[string]any
		if raw, ok := fields["stream_options"]; ok {
			_ = json.Unmarshal(raw, &opts)
		}
		if opts == nil {
			opts = make(map[string]any)
		}
		opts["include_usage"] = true
		fields["stream_options"], _ = json.Marshal(opts)
	}
	outBody, err := json.Marshal(fields)
	if err != nil {
		writeProxyError(w, apierr.New(400, "invalid_json", "request body could not be re-encoded"))
		return
	}

	reqCtx := r.Context()
	if !stream {
		var cancel context.CancelFunc
		reqCtx, cancel = context.WithTimeout(reqCtx, route.TimeoutRead)
		defer cancel()
	}
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost,
		route.EndpointURL(endpoint), bytes.NewReader(outBody))
	if err != nil {
		writeProxyError(w, apierr.New(500, "internal_error", "failed to build upstream request"))
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if route.Credential != "" {
		req.Header.Set("Authorization", "Bearer "+route.Credential)
	}

	started := time.Now()
	resp, err := s.pool.ClientFor(route).Do(req)
	if err != nil {
		// A caller that hung up before the upstream answered is a
		// cancellation, not a provider failure.
		outcome, cancelled, kind := "unreachable", false, errClass(err)
		if r.Context().Err() != nil {
			outcome, cancelled, kind = "cancelled", true, ""
		}
		s.recordUsageAsync(auth, route, endpoint, usageOutcome{
			Outcome: outcome, ErrorKind: kind, Cancelled: cancelled, Streamed: stream,
			DurationMs: time.Since(started).Milliseconds(),
		})
		writeProxyError(w, apierr.Newf(502, "provider_unreachable",
			"request to provider '%s' failed: %s", route.ProviderName, errClass(err)))
		return
	}
	defer resp.Body.Close()

	w.Header().Set("x-llmproxy-provider", route.ProviderName)
	w.Header().Set("x-llmproxy-model", route.UpstreamName)

	if !stream || resp.StatusCode >= 400 {
		s.relayUnary(w, auth, route, endpoint, resp, stream, started)
		return
	}
	s.relayStream(w, r, auth, route, endpoint, resp, started)
}

// maxUnaryResponseBytes caps buffered upstream bodies. Oversize responses are
// failed with an error rather than silently truncated.
const maxUnaryResponseBytes = 64 << 20

var errResponseTooLarge = errors.New("upstream response exceeded the buffering limit")

// relayUnary passes a buffered response (or a streamed request's upstream
// error) through unchanged, extracting usage from successful JSON bodies.
func (s *Server) relayUnary(w http.ResponseWriter, auth *Auth, route *catalog.Route,
	endpoint string, resp *http.Response, streamed bool, started time.Time) {
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxUnaryResponseBytes+1))
	if err == nil && int64(len(data)) > maxUnaryResponseBytes {
		err = errResponseTooLarge
	}
	if err != nil {
		s.recordUsageAsync(auth, route, endpoint, usageOutcome{
			StatusCode: resp.StatusCode, Outcome: "upstream_error", ErrorKind: errClass(err),
			Streamed: streamed, DurationMs: time.Since(started).Milliseconds(),
		})
		writeProxyError(w, apierr.Newf(502, "provider_unreachable",
			"reading response from provider '%s' failed: %s", route.ProviderName, errClass(err)))
		return
	}
	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/json"
	}
	outcome, kind := "ok", ""
	var usage map[string]any
	if resp.StatusCode >= 400 {
		outcome, kind = "upstream_error", errorKindFromBody(data)
		w.Header().Set("x-llmproxy-error-source", "upstream")
	} else if strings.HasPrefix(contentType, "application/json") {
		usage = extractUsage(data)
	}
	s.recordUsageAsync(auth, route, endpoint, usageOutcome{
		StatusCode: resp.StatusCode, Outcome: outcome, ErrorKind: kind, Streamed: streamed,
		Usage: usage, DurationMs: time.Since(started).Milliseconds(),
	})
	w.Header().Set("Content-Type", contentType)
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(data)
}

// relayStream forwards SSE bytes untouched while scanning completed lines for
// the usage chunk. A client disconnect cancels the request context, which
// aborts the upstream read; the partial request is then accounted, flagged
// cancelled.
func (s *Server) relayStream(w http.ResponseWriter, r *http.Request, auth *Auth,
	route *catalog.Route, endpoint string, resp *http.Response, started time.Time) {
	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "text/event-stream"
	}
	w.Header().Set("Content-Type", contentType)
	w.WriteHeader(resp.StatusCode)
	flusher, _ := w.(http.Flusher)

	var usage map[string]any
	outcome, kind := "ok", ""
	cancelled := false
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
				if u := parseSSEUsage(pending[:idx]); u != nil {
					usage = mergeUsage(usage, u)
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
	// Some upstreams end the stream without a trailing newline; scan the
	// leftover partial line for a usage chunk too.
	if u := parseSSEUsage(pending); u != nil {
		usage = mergeUsage(usage, u)
	}
	s.recordUsageAsync(auth, route, endpoint, usageOutcome{
		StatusCode: resp.StatusCode, Outcome: outcome, ErrorKind: kind, Cancelled: cancelled,
		Streamed: true, Usage: usage, DurationMs: time.Since(started).Milliseconds(),
	})
}

// checkEmbeddingBatch caps embedding input arrays: an unbounded batch is an
// accidental denial of service against your own inference node.
func (s *Server) checkEmbeddingBatch(fields map[string]json.RawMessage) *apierr.ProxyError {
	if s.cfg.MaxEmbeddingBatch <= 0 {
		return nil
	}
	raw, ok := fields["input"]
	if !ok {
		return nil
	}
	trimmed := bytes.TrimLeft(raw, " \t\r\n")
	if len(trimmed) == 0 || trimmed[0] != '[' {
		return nil
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil // malformed input is the upstream's problem to reject
	}
	if len(items) > s.cfg.MaxEmbeddingBatch {
		return apierr.Newf(400, "embedding_batch_too_large",
			"embedding input arrays are capped at %d items (got %d)",
			s.cfg.MaxEmbeddingBatch, len(items)).WithParam("input")
	}
	return nil
}

// errClass names the failure category without echoing any request detail.
func errClass(err error) string {
	if err == nil {
		return "unknown"
	}
	if errors.Is(err, errResponseTooLarge) {
		return "response_too_large"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	var t interface{ Timeout() bool }
	if errors.As(err, &t) && t.Timeout() {
		return "timeout"
	}
	return "connection_error"
}
