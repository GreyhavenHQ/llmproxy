package catalog

import (
	"testing"
	"time"
)

func testPlan(strategy string, names ...string) *Plan {
	p := &Plan{Alias: "ha", Strategy: strategy}
	for _, n := range names {
		p.Routes = append(p.Routes, &Route{Alias: "ha", TargetAlias: n, ProviderName: n, Weight: 1})
	}
	return p
}

func names(routes []*Route) string {
	out := ""
	for _, r := range routes {
		out += r.TargetAlias
	}
	return out
}

func newTestBalancer(cooldown time.Duration) (*Balancer, *time.Time) {
	now := time.Unix(1000, 0)
	b := NewBalancer(cooldown)
	b.now = func() time.Time { return now }
	return b, &now
}

func TestOrderFailoverKeepsListedOrder(t *testing.T) {
	b, _ := newTestBalancer(time.Minute)
	p := testPlan("failover", "a", "b", "c")
	for i := 0; i < 3; i++ {
		if got := names(b.Order(p)); got != "abc" {
			t.Fatalf("order = %s", got)
		}
	}
}

func TestOrderRoundRobinRotatesStart(t *testing.T) {
	b, _ := newTestBalancer(time.Minute)
	p := testPlan("round_robin", "a", "b", "c")
	var got []string
	for i := 0; i < 4; i++ {
		got = append(got, names(b.Order(p)))
	}
	want := []string{"abc", "bac", "cab", "abc"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("orders = %v, want %v", got, want)
		}
	}
}

func TestOrderWeightedFollowsWeights(t *testing.T) {
	b, _ := newTestBalancer(time.Minute)
	p := testPlan("weighted", "a", "b")
	p.Routes[0].Weight = 3
	p.Routes[1].Weight = 1
	picks := []int{0, 2, 3}
	i := 0
	b.rand = func(n int) int {
		if n != 4 {
			t.Fatalf("rand range = %d, want total weight 4", n)
		}
		v := picks[i]
		i++
		return v
	}
	for _, want := range []string{"ab", "ab", "ba"} {
		if got := names(b.Order(p)); got != want {
			t.Fatalf("order = %s, want %s", got, want)
		}
	}
}

func TestOrderLeastBusyPicksFewestInFlight(t *testing.T) {
	b, _ := newTestBalancer(time.Minute)
	p := testPlan("least_busy", "a", "b", "c")
	releaseA := b.Acquire(p.Routes[0])
	releaseB := b.Acquire(p.Routes[1])
	if got := names(b.Order(p)); got != "cab" {
		t.Fatalf("order = %s", got)
	}
	releaseA()
	releaseB()
	if got := names(b.Order(p)); got != "abc" {
		t.Fatalf("after release = %s", got)
	}
}

func TestOrderMovesCooledTargetsLast(t *testing.T) {
	b, now := newTestBalancer(30 * time.Second)
	p := testPlan("failover", "a", "b", "c")
	b.Fail(p.Routes[0])
	if got := names(b.Order(p)); got != "bca" {
		t.Fatalf("order = %s", got)
	}
	if cooled, _ := b.CoolingDown(p.Routes[0]); !cooled {
		t.Fatal("a must be cooling down")
	}
	b.Fail(p.Routes[1])
	b.Fail(p.Routes[2])
	if got := names(b.Order(p)); got != "abc" {
		t.Fatalf("all cooled = %s, want listed order", got)
	}
	*now = now.Add(31 * time.Second)
	if got := names(b.Order(p)); got != "abc" {
		t.Fatalf("after expiry = %s", got)
	}
	if cooled, _ := b.CoolingDown(p.Routes[0]); cooled {
		t.Fatal("cooldown must expire")
	}
}

func TestZeroCooldownDisables(t *testing.T) {
	b, _ := newTestBalancer(0)
	p := testPlan("failover", "a", "b")
	b.Fail(p.Routes[0])
	if got := names(b.Order(p)); got != "ab" {
		t.Fatalf("order = %s", got)
	}
}

func TestPlanCapabilitiesIntersect(t *testing.T) {
	p := testPlan("failover", "a", "b")
	p.Routes[0].Capabilities = map[string]bool{"chat": true, "chat_stream": true, "embeddings": true}
	p.Routes[1].Capabilities = map[string]bool{"chat": true, "chat_stream": true}
	caps := p.Capabilities()
	if !caps["chat"] || !caps["chat_stream"] || caps["embeddings"] {
		t.Fatalf("caps = %v", caps)
	}
}
