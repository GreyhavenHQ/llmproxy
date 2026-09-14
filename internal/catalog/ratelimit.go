package catalog

import "strings"

// ValidRateLimitKeys are the metric keys a rate_limit_headers spec may map.
var ValidRateLimitKeys = []string{
	"limit_requests", "remaining_requests",
	"limit_tokens", "remaining_tokens",
	"reset_requests", "reset_tokens",
}

func isValidRateLimitKey(key string) bool {
	for _, k := range ValidRateLimitKeys {
		if k == key {
			return true
		}
	}
	return false
}

// ParseRateLimitHeaders parses the stored comma-separated "key:header" spec
// into a map. Invalid pairs are silently dropped.
func ParseRateLimitHeaders(spec string) map[string]string {
	if spec == "" {
		return nil
	}
	out := make(map[string]string)
	for _, pair := range strings.Split(spec, ",") {
		key, header, ok := strings.Cut(pair, ":")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		header = strings.TrimSpace(header)
		if key == "" || header == "" {
			continue
		}
		if !isValidRateLimitKey(key) {
			continue
		}
		out[key] = header
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// FormatRateLimitHeaders serialises a map back to the stored comma-separated
// "key:header" canonical form, sorted by key for stable output.
func FormatRateLimitHeaders(m map[string]string) string {
	if len(m) == 0 {
		return ""
	}
	var parts []string
	for _, key := range ValidRateLimitKeys {
		if header, ok := m[key]; ok && header != "" {
			parts = append(parts, key+":"+header)
		}
	}
	return strings.Join(parts, ",")
}
