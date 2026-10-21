package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/greyhavenhq/llmproxy/internal/store"
)

func at(d time.Duration) string {
	return time.Now().Add(d).UTC().Format(store.TimeFormat)
}

func TestExtendSession(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	p, err := st.GetOrCreatePrincipal(ctx, "alice", "user", "member", nil)
	if err != nil {
		t.Fatal(err)
	}

	if err := st.CreateSession(ctx, p.ID, "live", at(time.Minute)); err != nil {
		t.Fatal(err)
	}
	live := sessionID(t, st, "live")
	later := at(time.Hour)
	if err := st.ExtendSession(ctx, live, later); err != nil {
		t.Fatal(err)
	}
	res, err := st.AuthBySessionHash(ctx, "live")
	if err != nil || res == nil {
		t.Fatalf("live session: %v %v", res, err)
	}
	if res.ExpiresAt != later {
		t.Fatalf("expires_at: got %s, want %s", res.ExpiresAt, later)
	}

	if err := st.CreateSession(ctx, p.ID, "expired", at(-time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := st.ExtendSession(ctx, expiredID(t, st), at(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if res, _ := st.AuthBySessionHash(ctx, "expired"); res != nil {
		t.Fatal("an expired session must not be revived")
	}

	if err := st.DeleteSessionByHash(ctx, "live"); err != nil {
		t.Fatal(err)
	}
	if err := st.ExtendSession(ctx, live, at(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if res, _ := st.AuthBySessionHash(ctx, "live"); res != nil {
		t.Fatal("a deleted session must not be revived")
	}
}

func sessionID(t *testing.T, st *store.Store, hash string) string {
	t.Helper()
	res, err := st.AuthBySessionHash(context.Background(), hash)
	if err != nil || res == nil {
		t.Fatalf("session %s: %v %v", hash, res, err)
	}
	return res.SessionID
}

func expiredID(t *testing.T, st *store.Store) string {
	t.Helper()
	var id string
	if err := st.DB().QueryRow(`SELECT id FROM session WHERE token_hash = 'expired'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestHasCredentials(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	if has, err := st.HasCredentials(ctx); err != nil || has {
		t.Fatalf("empty store: %v %v", has, err)
	}
	p, err := st.GetOrCreatePrincipal(ctx, "alice", "user", "member", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateAPIKey(ctx, p.ID, "hash", "abcd", "k", nil); err != nil {
		t.Fatal(err)
	}
	if has, err := st.HasCredentials(ctx); err != nil || !has {
		t.Fatalf("store with a key: %v %v", has, err)
	}
}
