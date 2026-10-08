package store_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/greyhavenhq/llmproxy/internal/store"
)

type targetFixture struct {
	st     *store.Store
	pa, pb *store.Provider
	a, b   *store.ModelBinding
}

func newTargetFixture(t *testing.T) *targetFixture {
	t.Helper()
	ctx := context.Background()
	f := &targetFixture{st: openTestStore(t)}
	mk := func(name string) *store.Provider {
		p := &store.Provider{Name: name, WireFormat: "openai", BaseURL: "https://" + name,
			VerifyTLS: true, TimeoutConnect: 5, TimeoutRead: 30, Enabled: true}
		if err := f.st.CreateProvider(ctx, p, nil, nil); err != nil {
			t.Fatal(err)
		}
		return p
	}
	f.pa, f.pb = mk("pa"), mk("pb")
	bind := func(alias string, p *store.Provider, caps string) *store.ModelBinding {
		b := &store.ModelBinding{Alias: alias, ProviderID: p.ID, UpstreamName: "up-" + alias,
			CapabilitySet: caps, Origin: "declared"}
		if err := f.st.CreateBinding(ctx, b, nil); err != nil {
			t.Fatal(err)
		}
		return b
	}
	f.a = bind("a", f.pa, "chat,chat_stream,embeddings")
	f.b = bind("b", f.pb, "chat,chat_stream")
	return f
}

func (f *targetFixture) alias(t *testing.T, name string) *store.ModelBinding {
	t.Helper()
	b, err := f.st.GetBindingByAlias(context.Background(), name)
	if err != nil || b == nil {
		t.Fatalf("get %s: %v %v", name, b, err)
	}
	return b
}

func TestAliasTargetsRoundTrip(t *testing.T) {
	ctx := context.Background()
	f := newTargetFixture(t)
	ha := &store.ModelBinding{Alias: "ha", Origin: "declared", Strategy: "weighted",
		Targets: []store.BindingTarget{{ID: f.b.ID, Weight: 3}, {ID: f.a.ID, Weight: 1}}}
	if err := f.st.CreateBinding(ctx, ha, nil); err != nil {
		t.Fatal(err)
	}
	got := f.alias(t, "ha")
	if got.Strategy != "weighted" || len(got.Targets) != 2 {
		t.Fatalf("got %+v", got)
	}
	if got.Targets[0].Alias != "b" || got.Targets[0].Weight != 3 || got.Targets[1].Alias != "a" {
		t.Fatalf("targets = %+v", got.Targets)
	}
	if got.TargetID.Valid {
		t.Fatal("multi-target alias must not set target_id")
	}
	if got.CapabilitySet != "chat,chat_stream" {
		t.Fatalf("capabilities = %q, want the intersection", got.CapabilitySet)
	}

	list, err := f.st.ListBindings(ctx, "", 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range list {
		if b.Alias == "ha" && (len(b.Targets) != 2 || b.CapabilitySet != "chat,chat_stream") {
			t.Fatalf("list targets = %+v", b.Targets)
		}
	}
	for _, p := range []string{"pa", "pb"} {
		byProvider, err := f.st.ListBindings(ctx, p, 100, 0)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, b := range byProvider {
			found = found || b.Alias == "ha"
		}
		if !found {
			t.Fatalf("ha not listed under provider %s", p)
		}
	}
}

func TestAliasSwitchBetweenOneAndSeveralTargets(t *testing.T) {
	ctx := context.Background()
	f := newTargetFixture(t)
	ha := &store.ModelBinding{Alias: "ha", Origin: "declared",
		Targets: []store.BindingTarget{{ID: f.a.ID}}}
	if err := f.st.CreateBinding(ctx, ha, nil); err != nil {
		t.Fatal(err)
	}
	got := f.alias(t, "ha")
	if !got.TargetID.Valid || got.TargetID.String != f.a.ID || got.Strategy != "" {
		t.Fatalf("one target: %+v", got)
	}
	if len(got.Targets) != 1 || got.Targets[0].Alias != "a" || got.UpstreamName != "up-a" {
		t.Fatalf("one target read: %+v", got)
	}

	got.Targets = []store.BindingTarget{{ID: f.a.ID}, {ID: f.b.ID}}
	got.Strategy = "round_robin"
	if err := f.st.UpdateBinding(ctx, got, nil); err != nil {
		t.Fatal(err)
	}
	got = f.alias(t, "ha")
	if got.TargetID.Valid || got.Strategy != "round_robin" || len(got.Targets) != 2 {
		t.Fatalf("two targets: %+v", got)
	}
	if got.Targets[0].Weight != 1 {
		t.Fatalf("default weight = %d", got.Targets[0].Weight)
	}

	got.Targets = []store.BindingTarget{{ID: f.b.ID}}
	got.Strategy = ""
	if err := f.st.UpdateBinding(ctx, got, nil); err != nil {
		t.Fatal(err)
	}
	got = f.alias(t, "ha")
	if !got.TargetID.Valid || got.TargetID.String != f.b.ID || got.Strategy != "" || len(got.Targets) != 1 {
		t.Fatalf("back to one: %+v", got)
	}
	if got.UpstreamName != "up-b" || got.ProviderName != "pb" {
		t.Fatalf("one target routing: %+v", got)
	}
}

func TestDeleteGuardSeesSeveralTargets(t *testing.T) {
	ctx := context.Background()
	f := newTargetFixture(t)
	ha := &store.ModelBinding{Alias: "ha", Origin: "declared", Strategy: "failover",
		Targets: []store.BindingTarget{{ID: f.a.ID}, {ID: f.b.ID}}}
	if err := f.st.CreateBinding(ctx, ha, nil); err != nil {
		t.Fatal(err)
	}
	one := &store.ModelBinding{Alias: "one", Origin: "declared", TargetID: sql.NullString{String: f.b.ID, Valid: true}}
	if err := f.st.CreateBinding(ctx, one, nil); err != nil {
		t.Fatal(err)
	}
	got, err := f.st.ListBindingsTargeting(ctx, f.b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "ha" || got[1] != "one" {
		t.Fatalf("targeting = %v", got)
	}
}

func TestResolveAliasSeveralTargets(t *testing.T) {
	ctx := context.Background()
	f := newTargetFixture(t)
	ha := &store.ModelBinding{Alias: "ha", Origin: "declared", Strategy: "failover",
		Targets: []store.BindingTarget{{ID: f.b.ID}, {ID: f.a.ID, Weight: 2}}}
	if err := f.st.CreateBinding(ctx, ha, nil); err != nil {
		t.Fatal(err)
	}
	b, targets, err := f.st.ResolveAlias(ctx, "ha")
	if err != nil || b == nil {
		t.Fatalf("resolve: %v %v", b, err)
	}
	if b.Strategy != "failover" || len(targets) != 2 {
		t.Fatalf("resolved %+v %d", b, len(targets))
	}
	if targets[0].Binding.Alias != "b" || targets[0].Provider.Name != "pb" || targets[1].Weight != 2 {
		t.Fatalf("targets[0] = %+v", targets[0])
	}

	f.pb.Enabled = false
	if err := f.st.UpdateProvider(ctx, f.pb, nil); err != nil {
		t.Fatal(err)
	}
	_, targets, err = f.st.ResolveAlias(ctx, "ha")
	if err != nil || len(targets) != 1 || targets[0].Binding.Alias != "a" {
		t.Fatalf("disabled provider target not dropped: %+v %v", targets, err)
	}
	servable, err := f.st.ListServableBindings(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, s := range servable {
		found = found || s.Alias == "ha"
	}
	if !found {
		t.Fatal("ha must stay servable while one target provider is enabled")
	}

	_, direct, err := f.st.ResolveAlias(ctx, "a")
	if err != nil || len(direct) != 1 || direct[0].Binding.UpstreamName != "up-a" {
		t.Fatalf("direct resolve: %+v %v", direct, err)
	}
}

func TestDeleteProviderShrinksAliasTargets(t *testing.T) {
	ctx := context.Background()
	f := newTargetFixture(t)
	ha := &store.ModelBinding{Alias: "ha", Origin: "declared", Strategy: "failover",
		Targets: []store.BindingTarget{{ID: f.a.ID}, {ID: f.b.ID}}}
	if err := f.st.CreateBinding(ctx, ha, nil); err != nil {
		t.Fatal(err)
	}
	if err := f.st.DeleteProvider(ctx, f.pa.ID, nil); err != nil {
		t.Fatal(err)
	}
	got := f.alias(t, "ha")
	if got.Strategy != "" || !got.TargetID.Valid || got.TargetID.String != f.b.ID || got.ProviderName != "pb" {
		t.Fatalf("after provider delete: %+v", got)
	}
}

func TestUsageEventFailedOverRoundTrip(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	ev := &store.UsageEvent{PrincipalID: "p", APIKeyID: "k", ProviderID: "x", Alias: "ha",
		UpstreamName: "u", Endpoint: "chat", Outcome: "unreachable", FailedOver: true}
	if err := st.InsertUsageEvent(ctx, ev, nil); err != nil {
		t.Fatal(err)
	}
	evs, err := st.ListUsageEvents(ctx)
	if err != nil || len(evs) != 1 || !evs[0].FailedOver {
		t.Fatalf("events = %+v %v", evs, err)
	}
}

func TestRequestCountsExcludeFailedOver(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	for _, ev := range []*store.UsageEvent{
		{PrincipalID: "p", APIKeyID: "k", ProviderID: "x", Alias: "ha", UpstreamName: "u",
			Endpoint: "chat", Outcome: "upstream_error", StatusCode: sql.NullInt64{Int64: 429, Valid: true}, FailedOver: true},
		{PrincipalID: "p", APIKeyID: "k", ProviderID: "y", Alias: "ha", UpstreamName: "u",
			Endpoint: "chat", Outcome: "ok", StatusCode: sql.NullInt64{Int64: 200, Valid: true}},
	} {
		if err := st.InsertUsageEvent(ctx, ev, nil); err != nil {
			t.Fatal(err)
		}
	}
	summary, err := st.UsageSummary(ctx, "", "", "")
	if err != nil || len(summary) != 1 || summary[0].Requests != 1 {
		t.Fatalf("summary = %+v %v", summary, err)
	}
	series, err := st.UsageSeries(ctx, store.UsageFilter{}, false)
	if err != nil || len(series) != 1 || series[0].Requests != 1 || series[0].Failed != 0 {
		t.Fatalf("series = %+v %v", series, err)
	}
	errs, err := st.ErrorSeries(ctx, store.UsageFilter{}, false)
	if err != nil || len(errs) != 1 || errs[0].Requests != 2 {
		t.Fatalf("error series = %+v %v", errs, err)
	}
}
