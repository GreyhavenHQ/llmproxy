package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func doWebFetch(t *testing.T, s *Server, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/my/webfetch", strings.NewReader(body))
	rec := httptest.NewRecorder()
	s.cfg.MaxBodyBytes = 1 << 20
	s.handleMyWebFetch(rec, req, &Auth{PrincipalID: "p1", Role: "member"})
	return rec
}

func TestWebFetchExtractsText(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<html><head><title>t</title><script>var x = "hidden";</script></head>` +
			`<body><h1>Hello &amp; welcome</h1><p>Visible   text.</p></body></html>`))
	}))
	defer upstream.Close()

	s := &Server{webfetch: newWebFetchClient(true)} // loopback allowed for the test
	rec := doWebFetch(t, s, `{"url":"`+upstream.URL+`"}`)
	if rec.Code != 200 {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Status    int    `json:"status"`
		Text      string `json:"text"`
		Truncated bool   `json:"truncated"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Status != 200 || out.Truncated {
		t.Fatalf("unexpected result: %+v", out)
	}
	if !strings.Contains(out.Text, "Hello & welcome") || !strings.Contains(out.Text, "Visible text.") {
		t.Fatalf("text not extracted: %q", out.Text)
	}
	if strings.Contains(out.Text, "hidden") {
		t.Fatalf("script content leaked: %q", out.Text)
	}
}

func TestWebFetchRejectsPrivateAddresses(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("the guard let a loopback fetch through")
	}))
	defer upstream.Close()

	s := &Server{webfetch: newWebFetchClient(false)} // production posture
	rec := doWebFetch(t, s, `{"url":"`+upstream.URL+`"}`)
	if rec.Code != 502 {
		t.Fatalf("status = %d, want 502; body %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "non-public address") {
		t.Fatalf("unexpected error body: %s", rec.Body.String())
	}
}

func TestWebFetchRejectsBadURLs(t *testing.T) {
	s := &Server{webfetch: newWebFetchClient(true)}
	for _, body := range []string{
		`{"url":"ftp://example.com/x"}`,
		`{"url":"file:///etc/passwd"}`,
		`{"url":"not a url"}`,
		`{"url":""}`,
	} {
		if rec := doWebFetch(t, s, body); rec.Code != 400 {
			t.Errorf("%s: status = %d, want 400", body, rec.Code)
		}
	}
}

func TestWebFetchRefusesBinaries(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write([]byte{0x00, 0x01, 0x02})
	}))
	defer upstream.Close()

	s := &Server{webfetch: newWebFetchClient(true)}
	if rec := doWebFetch(t, s, `{"url":"`+upstream.URL+`"}`); rec.Code != 415 {
		t.Fatalf("status = %d, want 415", rec.Code)
	}
}

func TestHTMLToText(t *testing.T) {
	got := htmlToText(`<ul><li>one</li><li>two &lt;3</li></ul><style>.a{color:red}</style>after`)
	if !strings.Contains(got, "one") || !strings.Contains(got, "two <3") || !strings.Contains(got, "after") {
		t.Fatalf("got %q", got)
	}
	if strings.Contains(got, "color") {
		t.Fatalf("style leaked: %q", got)
	}
}
