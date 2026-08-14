package server

import "testing"

// The classification token must never preserve free text: a value shaped
// like a message is dropped whole, not trimmed into a survivable fragment.
func TestErrorKindToken(t *testing.T) {
	cases := []struct{ in, want string }{
		{"rate_limit_error", "rate_limit_error"},
		{"  Overloaded_Error ", "overloaded_error"},
		{"model_not_found", "model_not_found"},
		{"context_length_exceeded", "context_length_exceeded"},
		{"429", "429"},
		{"", ""},
		{"the prompt said 'hello'", ""},
		{"Invalid request: missing field", ""},
		{"_leading_underscore", ""},
		{string(make([]byte, 80)), ""},
	}
	for _, c := range cases {
		if got := errorKindToken(c.in); got != c.want {
			t.Errorf("errorKindToken(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestErrorKindFromBody(t *testing.T) {
	cases := []struct {
		name, body, want string
	}{
		{"openai type", `{"error":{"message":"rate limited","type":"rate_limit_error"}}`, "rate_limit_error"},
		{"code wins over type", `{"error":{"type":"invalid_request_error","code":"model_not_found"}}`, "model_not_found"},
		{"numeric code", `{"error":{"type":"","code":1013}}`, "1013"},
		{"anthropic", `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`, "overloaded_error"},
		{"message never read", `{"error":{"message":"secret content"}}`, ""},
		{"free text code rejected, type kept", `{"error":{"type":"server_error","code":"try again later please"}}`, "server_error"},
		{"not json", `<html>bad gateway</html>`, ""},
		{"null code", `{"error":{"type":"server_error","code":null}}`, "server_error"},
	}
	for _, c := range cases {
		if got := errorKindFromBody([]byte(c.body)); got != c.want {
			t.Errorf("%s: errorKindFromBody = %q, want %q", c.name, got, c.want)
		}
	}
}
