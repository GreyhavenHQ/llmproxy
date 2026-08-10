package server

import (
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

// tagsFrom normalises the header into a canonical string: lowercased,
// trimmed, malformed pairs dropped, one value per key, sorted, capped.
func TestTagsFrom(t *testing.T) {
	cases := []struct {
		name   string
		header string
		want   string
	}{
		{"empty", "", ""},
		{"absent pairs", ",,,", ""},
		{"single", "app:dataindex", "app:dataindex"},
		{"sorted and trimmed", " context:search , app:dataindex ", "app:dataindex,context:search"},
		{"lowercased", "App:DataIndex", "app:dataindex"},
		{"malformed dropped", "app:dataindex,nonsense,=bad:x,ctx:", "app:dataindex"},
		{"empty key or value", ":x,y:,app:ok", "app:ok"},
		{"leading punctuation rejected", "_app:x,app:_x,app:ok", "app:ok"},
		{"spaces inside rejected", "app:data index,app:ok", "app:ok"},
		{"first occurrence of a key wins", "app:one,app:two", "app:one"},
		{"punctuation allowed", "app.name:a-b_c.1", "app.name:a-b_c.1"},
		// Sorted by key, not by the whole pair: ':' sorts after digits, so a
		// whole-string sort would put "app2:a" before "app:z".
		{"sorted by key", "app2:a,app:z", "app:z,app2:a"},
		{"sorted by key, reversed input", "app:z,app2:a", "app:z,app2:a"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "/v1/chat/completions", nil)
			r.Header.Set(tagsHeader, tc.header)
			if got := tagsFrom(r); got != tc.want {
				t.Fatalf("tagsFrom(%q) = %q, want %q", tc.header, got, tc.want)
			}
		})
	}
}

func TestTagsFromCaps(t *testing.T) {
	// Ten well-formed pairs: sorted first, then capped, so the eight lowest
	// keys survive whatever order the caller sent them in.
	var pairs []string
	for _, k := range []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j"} {
		pairs = append(pairs, k+":v")
	}
	const wantCapped = "a:v,b:v,c:v,d:v,e:v,f:v,g:v,h:v"
	r := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	r.Header.Set(tagsHeader, strings.Join(pairs, ","))
	if got := tagsFrom(r); got != wantCapped {
		t.Fatalf("pair cap: got %q, want %q", got, wantCapped)
	}
	// Reversed input must produce the identical string: the cap applies to
	// the sorted set, not to the order of arrival.
	slices.Reverse(pairs)
	r.Header.Set(tagsHeader, strings.Join(pairs, ","))
	if got := tagsFrom(r); got != wantCapped {
		t.Fatalf("pair cap is order-dependent: got %q, want %q", got, wantCapped)
	}

	// Long values hit the byte cap; whole pairs drop rather than truncate.
	long := "app:" + strings.Repeat("x", 200) + ",context:" + strings.Repeat("y", 200)
	r.Header.Set(tagsHeader, long)
	got := tagsFrom(r)
	if len(got) > tagsMaxLen {
		t.Fatalf("byte cap: %d bytes: %q", len(got), got)
	}
	if got != "app:"+strings.Repeat("x", 200) {
		t.Fatalf("byte cap dropped the wrong pair: %q", got)
	}
	for _, pair := range strings.Split(got, ",") {
		if !tagPair.MatchString(pair) {
			t.Fatalf("byte cap produced a malformed pair: %q", pair)
		}
	}
}

// Invalid UTF-8 never reaches the column: the pattern rejects non-ASCII
// outright, so a broken sequence takes its pair with it.
func TestTagsFromInvalidUTF8(t *testing.T) {
	r := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	r.Header.Set(tagsHeader, "app:da\xffta,context:search")
	got := tagsFrom(r)
	if got != "context:search" {
		t.Fatalf("tagsFrom = %q, want context:search", got)
	}
	if strings.ContainsRune(got, '�') || !isValidUTF8(got) {
		t.Fatalf("tagsFrom returned invalid UTF-8: %q", got)
	}
}

func isValidUTF8(s string) bool {
	return strings.ToValidUTF8(s, "") == s
}

// Everything past tagsRawMaxLen is ignored, keeping parse time bounded on
// the request goroutine.
func TestTagsFromBoundsRawInput(t *testing.T) {
	r := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	r.Header.Set(tagsHeader, strings.Repeat("x", tagsRawMaxLen)+",app:ok")
	if got := tagsFrom(r); got != "" {
		t.Fatalf("tagsFrom = %q, want empty", got)
	}
}

// The lowercase step lives in statsFilter, and only this pins it: SQLite's
// LIKE is ASCII-case-insensitive, so the HTTP-level filter tests pass with
// or without it.
func TestStatsFilterLowercasesTags(t *testing.T) {
	var s Server
	r := httptest.NewRequest("GET", "/stats/requests?tag=App:DataIndex&tag=Context:Search", nil)
	f, ok := s.statsFilter(httptest.NewRecorder(), r)
	if !ok {
		t.Fatal("statsFilter rejected the request")
	}
	want := []string{"app:dataindex", "context:search"}
	if !slices.Equal(f.Tags, want) {
		t.Fatalf("Tags = %v, want %v", f.Tags, want)
	}
}
