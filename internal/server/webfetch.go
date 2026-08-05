package server

import (
	"context"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/monadical/llmproxy/internal/apierr"
)

// Web fetch for the playground's web_fetch tool: the browser cannot fetch
// arbitrary sites (CORS), so the proxy fetches on its behalf and returns
// extracted text. Nothing about the fetch is persisted or logged. The proxy
// runs inside the network perimeter, so the dialer refuses non-public
// addresses; validating at dial time covers redirects and DNS games too.

const (
	maxWebFetchBodyBytes = 2 << 20  // raw bytes read from the fetched site
	maxWebFetchTextBytes = 64 << 10 // text handed back to the model
	webFetchTimeout      = 15 * time.Second
)

func newWebFetchClient(allowPrivate bool) *http.Client {
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
			if err != nil {
				return nil, err
			}
			var lastErr error
			for _, cand := range ips {
				if !allowPrivate && !isPublicIP(cand.IP) {
					lastErr = fmt.Errorf("%s resolves to a non-public address", host)
					continue
				}
				// Dial the vetted IP itself so a re-resolving name cannot
				// swap in a private address after the check.
				conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(cand.IP.String(), port))
				if err == nil {
					return conn, nil
				}
				lastErr = err
			}
			if lastErr == nil {
				lastErr = fmt.Errorf("no addresses for %s", host)
			}
			return nil, lastErr
		},
		TLSHandshakeTimeout: 10 * time.Second,
		MaxIdleConns:        4,
	}
	return &http.Client{Transport: transport, Timeout: webFetchTimeout}
}

func isPublicIP(ip net.IP) bool {
	return !ip.IsLoopback() && !ip.IsPrivate() && !ip.IsLinkLocalUnicast() &&
		!ip.IsLinkLocalMulticast() && !ip.IsInterfaceLocalMulticast() &&
		!ip.IsMulticast() && !ip.IsUnspecified()
}

func (s *Server) handleMyWebFetch(w http.ResponseWriter, r *http.Request, auth *Auth) {
	var req struct {
		URL string `json:"url"`
	}
	if perr := readJSONBody(r, 16<<10, &req); perr != nil {
		writeProxyError(w, perr)
		return
	}
	parsed, err := url.Parse(strings.TrimSpace(req.URL))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		writeProxyError(w, apierr.New(400, "invalid_url",
			"'url' must be an absolute http(s) URL").WithParam("url"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), webFetchTimeout)
	defer cancel()
	fetchReq, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		writeProxyError(w, apierr.New(400, "invalid_url",
			"'url' must be an absolute http(s) URL").WithParam("url"))
		return
	}
	fetchReq.Header.Set("User-Agent", "llmproxy-playground")
	fetchReq.Header.Set("Accept", "text/html, text/plain;q=0.9, application/json;q=0.8, */*;q=0.5")
	resp, err := s.webfetch.Do(fetchReq)
	if err != nil {
		writeProxyError(w, apierr.Newf(502, "fetch_failed",
			"fetching %s failed: %s", parsed.Host, fetchErrDetail(err)))
		return
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxWebFetchBodyBytes))
	if err != nil {
		writeProxyError(w, apierr.Newf(502, "fetch_failed",
			"reading %s failed: %s", parsed.Host, errClass(err)))
		return
	}
	contentType := resp.Header.Get("Content-Type")
	text, ok := webContentText(contentType, raw)
	if !ok {
		writeProxyError(w, apierr.Newf(415, "unsupported_content",
			"cannot extract text from content type %q", contentType))
		return
	}
	truncated := false
	if len(text) > maxWebFetchTextBytes {
		text = strings.ToValidUTF8(text[:maxWebFetchTextBytes], "")
		truncated = true
	}
	writeJSON(w, 200, map[string]any{
		"url":          parsed.String(),
		"status":       resp.StatusCode,
		"content_type": contentType,
		"text":         text,
		"truncated":    truncated,
	})
}

// fetchErrDetail keeps the dialer's non-public-address explanation (it names
// only the caller-supplied host) and reduces everything else to a class.
func fetchErrDetail(err error) string {
	if strings.Contains(err.Error(), "non-public address") {
		return "the host resolves to a non-public address"
	}
	return errClass(err)
}

// webContentText extracts text for the model: HTML is stripped to readable
// text, other text-like types pass through, binaries are refused.
func webContentText(contentType string, raw []byte) (string, bool) {
	mediaType := strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0]))
	switch {
	case mediaType == "text/html", mediaType == "application/xhtml+xml":
		return htmlToText(string(raw)), true
	case strings.HasPrefix(mediaType, "text/"),
		mediaType == "application/json",
		mediaType == "application/xml",
		strings.HasSuffix(mediaType, "+json"),
		strings.HasSuffix(mediaType, "+xml"),
		mediaType == "": // no header: assume text, the common case for plain files
		return strings.ToValidUTF8(string(raw), ""), true
	}
	return "", false
}

// blockTags separate text when stripped; everything else becomes a space.
var blockTags = map[string]bool{
	"p": true, "div": true, "br": true, "li": true, "tr": true, "ul": true,
	"ol": true, "table": true, "h1": true, "h2": true, "h3": true, "h4": true,
	"h5": true, "h6": true, "section": true, "article": true, "header": true,
	"footer": true, "main": true, "blockquote": true, "pre": true,
}

// skipTags have no readable content; their whole subtree is dropped.
var skipTags = map[string]bool{
	"script": true, "style": true, "noscript": true, "head": true,
	"template": true, "svg": true,
}

// htmlToText is a crude tag stripper, deliberately not a parser: good enough
// for a model to read a page, small enough to owe nothing to a dependency.
func htmlToText(raw string) string {
	var b strings.Builder
	i := 0
	for i < len(raw) {
		if raw[i] != '<' {
			j := strings.IndexByte(raw[i:], '<')
			if j < 0 {
				b.WriteString(raw[i:])
				break
			}
			b.WriteString(raw[i : i+j])
			i += j
			continue
		}
		if strings.HasPrefix(raw[i:], "<!--") {
			j := strings.Index(raw[i:], "-->")
			if j < 0 {
				break
			}
			i += j + 3
			continue
		}
		j := strings.IndexByte(raw[i:], '>')
		if j < 0 {
			break
		}
		tag := raw[i+1 : i+j]
		i += j + 1
		closing := strings.HasPrefix(tag, "/")
		name := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(tag, "/")))
		if sp := strings.IndexAny(name, " \t\r\n"); sp >= 0 {
			name = name[:sp]
		}
		switch {
		case skipTags[name] && !closing:
			k := strings.Index(strings.ToLower(raw[i:]), "</"+name)
			if k < 0 {
				i = len(raw)
			} else {
				i += k
			}
		case blockTags[name]:
			b.WriteByte('\n')
		default:
			b.WriteByte(' ')
		}
	}
	var lines []string
	for _, line := range strings.Split(html.UnescapeString(b.String()), "\n") {
		if line = strings.Join(strings.Fields(line), " "); line != "" {
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "\n")
}
