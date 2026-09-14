package catalog

import (
	"testing"
)

func TestParseRateLimitHeaders(t *testing.T) {
	tests := []struct {
		name string
		spec string
		want map[string]string
	}{
		{"empty", "", nil},
		{"standard openai", "limit_requests:x-ratelimit-limit-requests,remaining_requests:x-ratelimit-remaining-requests,limit_tokens:x-ratelimit-limit-tokens,remaining_tokens:x-ratelimit-remaining-tokens",
			map[string]string{
				"limit_requests":     "x-ratelimit-limit-requests",
				"remaining_requests": "x-ratelimit-remaining-requests",
				"limit_tokens":       "x-ratelimit-limit-tokens",
				"remaining_tokens":   "x-ratelimit-remaining-tokens",
			}},
		{"with reset headers", "limit_requests:x-ratelimit-limit-requests,reset_requests:x-ratelimit-reset-requests,reset_tokens:x-ratelimit-reset-tokens",
			map[string]string{
				"limit_requests": "x-ratelimit-limit-requests",
				"reset_requests": "x-ratelimit-reset-requests",
				"reset_tokens":   "x-ratelimit-reset-tokens",
			}},
		{"invalid key ignored", "limit_requests:x-foo,bogus_key:x-bar",
			map[string]string{"limit_requests": "x-foo"}},
		{"empty value ignored", "limit_requests:", nil},
		{"no colon ignored", "limit_requests", nil},
		{"whitespace trimmed", " limit_requests : x-foo , remaining_tokens : x-bar ",
			map[string]string{"limit_requests": "x-foo", "remaining_tokens": "x-bar"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseRateLimitHeaders(tt.spec)
			if tt.want == nil {
				if got != nil {
					t.Fatalf("expected nil, got %v", got)
				}
				return
			}
			if len(got) != len(tt.want) {
				t.Fatalf("expected %d entries, got %d: %v", len(tt.want), len(got), got)
			}
			for k, v := range tt.want {
				if got[k] != v {
					t.Errorf("key %q: expected %q, got %q", k, v, got[k])
				}
			}
		})
	}
}

func TestFormatRateLimitHeaders(t *testing.T) {
	tests := []struct {
		name string
		m    map[string]string
		want string
	}{
		{"nil", nil, ""},
		{"empty", map[string]string{}, ""},
		{"single", map[string]string{"limit_requests": "x-foo"}, "limit_requests:x-foo"},
		{"sorted by key order", map[string]string{
			"remaining_tokens": "x-bar",
			"limit_requests":   "x-foo",
		}, "limit_requests:x-foo,remaining_tokens:x-bar"},
		{"all six", map[string]string{
			"limit_requests":     "a",
			"remaining_requests": "b",
			"limit_tokens":       "c",
			"remaining_tokens":   "d",
			"reset_requests":     "e",
			"reset_tokens":       "f",
		}, "limit_requests:a,remaining_requests:b,limit_tokens:c,remaining_tokens:d,reset_requests:e,reset_tokens:f"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FormatRateLimitHeaders(tt.m)
			if got != tt.want {
				t.Fatalf("expected %q, got %q", tt.want, got)
			}
		})
	}
}

func TestRoundtrip(t *testing.T) {
	original := map[string]string{
		"limit_requests":     "x-ratelimit-limit-requests",
		"remaining_requests": "x-ratelimit-remaining-requests",
		"limit_tokens":       "x-ratelimit-limit-tokens",
		"remaining_tokens":   "x-ratelimit-remaining-tokens",
	}
	spec := FormatRateLimitHeaders(original)
	parsed := ParseRateLimitHeaders(spec)
	if len(parsed) != len(original) {
		t.Fatalf("roundtrip lost entries: %v -> %q -> %v", original, spec, parsed)
	}
	for k, v := range original {
		if parsed[k] != v {
			t.Errorf("key %q: expected %q, got %q", k, v, parsed[k])
		}
	}
}
