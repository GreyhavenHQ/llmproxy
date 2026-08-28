package main

import "testing"

func TestVersionStringUnstamped(t *testing.T) {
	got := versionString("dev", "", "")
	if got != "dev" {
		t.Fatalf("unstamped build: got %q, want %q", got, "dev")
	}
}

func TestVersionStringStamped(t *testing.T) {
	got := versionString("v1.1.0", "abc1234", "2026-08-27T10:00:00Z")
	want := "v1.1.0 (abc1234, built 2026-08-27T10:00:00Z)"
	if got != want {
		t.Fatalf("stamped build: got %q, want %q", got, want)
	}
}

func TestVersionStringCommitOnly(t *testing.T) {
	got := versionString("v1.1.0", "abc1234", "")
	want := "v1.1.0 (abc1234)"
	if got != want {
		t.Fatalf("commit-only build: got %q, want %q", got, want)
	}
}
