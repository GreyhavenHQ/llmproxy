package server

import "testing"

func TestMaskTransparentPath(t *testing.T) {
	cases := map[string]string{
		"/transparent/anthropic/lpt_abcdefgh1234/v1/messages": "/transparent/anthropic/***1234/v1/messages",
		"/transparent/anthropic/lpt_abcdefgh1234":             "/transparent/anthropic/***1234",
		"/transparent/anthropic/":                             "/transparent/anthropic/",
		"/v1/chat/completions":                                "/v1/chat/completions",
		"/":                                                   "/",
	}
	for in, want := range cases {
		if got := maskTransparentPath(in); got != want {
			t.Errorf("maskTransparentPath(%q) = %q, want %q", in, got, want)
		}
	}
}
