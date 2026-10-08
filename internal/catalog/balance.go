package catalog

import (
	"math/rand/v2"
	"sync"
	"time"
)

// Strategies an alias with several targets may declare.
var Strategies = []string{"failover", "round_robin", "weighted", "least_busy"}

func IsStrategy(name string) bool {
	for _, s := range Strategies {
		if s == name {
			return true
		}
	}
	return false
}

// Plan lists the routes of one alias. Strategy is empty with one route.
type Plan struct {
	Alias    string
	Strategy string
	Routes   []*Route
}

// Capabilities is the intersection over every route.
func (p *Plan) Capabilities() map[string]bool {
	out := map[string]bool{}
	for cap := range p.Routes[0].Capabilities {
		out[cap] = true
	}
	for _, r := range p.Routes[1:] {
		for cap := range out {
			if !r.Capabilities[cap] {
				delete(out, cap)
			}
		}
	}
	return out
}

// Balancer holds the per-replica rotation, in-flight and cooldown state.
type Balancer struct {
	cooldown time.Duration
	now      func() time.Time
	rand     func(n int) int

	mu          sync.Mutex
	next        map[string]uint64
	inflight    map[string]int
	cooledUntil map[string]time.Time
}

func NewBalancer(cooldown time.Duration) *Balancer {
	return &Balancer{
		cooldown:    cooldown,
		now:         time.Now,
		rand:        rand.IntN,
		next:        map[string]uint64{},
		inflight:    map[string]int{},
		cooledUntil: map[string]time.Time{},
	}
}

// TargetKey names the model a route serves, whichever alias reaches it.
func (r *Route) TargetKey() string {
	if r.TargetAlias != "" {
		return r.TargetAlias
	}
	return r.Alias
}

// Order puts the strategy's pick first and routes in cooldown last.
func (b *Balancer) Order(p *Plan) []*Route {
	if len(p.Routes) < 2 {
		return p.Routes
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	start := b.start(p)
	ordered := make([]*Route, 0, len(p.Routes))
	ordered = append(ordered, p.Routes[start])
	for i, r := range p.Routes {
		if i != start {
			ordered = append(ordered, r)
		}
	}
	now := b.now()
	ready := make([]*Route, 0, len(ordered))
	var cooled []*Route
	for _, r := range ordered {
		if until, ok := b.cooledUntil[r.TargetKey()]; ok && now.Before(until) {
			cooled = append(cooled, r)
		} else {
			ready = append(ready, r)
		}
	}
	if len(ready) == 0 {
		return p.Routes
	}
	return append(ready, cooled...)
}

func (b *Balancer) start(p *Plan) int {
	switch p.Strategy {
	case "round_robin":
		n := b.next[p.Alias]
		b.next[p.Alias] = n + 1
		return int(n % uint64(len(p.Routes)))
	case "weighted":
		total := 0
		for _, r := range p.Routes {
			total += max(r.Weight, 1)
		}
		pick := b.rand(total)
		for i, r := range p.Routes {
			pick -= max(r.Weight, 1)
			if pick < 0 {
				return i
			}
		}
	case "least_busy":
		best := 0
		for i, r := range p.Routes {
			if b.inflight[r.TargetKey()] < b.inflight[p.Routes[best].TargetKey()] {
				best = i
			}
		}
		return best
	}
	return 0
}

// Fail puts a route in cooldown.
func (b *Balancer) Fail(r *Route) {
	if b.cooldown <= 0 {
		return
	}
	b.mu.Lock()
	b.cooledUntil[r.TargetKey()] = b.now().Add(b.cooldown)
	b.mu.Unlock()
}

// CoolingDown reports whether a route is in cooldown and until when.
func (b *Balancer) CoolingDown(r *Route) (bool, time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	until, ok := b.cooledUntil[r.TargetKey()]
	if !ok {
		return false, time.Time{}
	}
	if !b.now().Before(until) {
		delete(b.cooledUntil, r.TargetKey())
		return false, time.Time{}
	}
	return true, until
}

// Acquire counts a request in flight until the returned func runs.
func (b *Balancer) Acquire(r *Route) func() {
	key := r.TargetKey()
	b.mu.Lock()
	b.inflight[key]++
	b.mu.Unlock()
	return func() {
		b.mu.Lock()
		if b.inflight[key]--; b.inflight[key] <= 0 {
			delete(b.inflight, key)
		}
		b.mu.Unlock()
	}
}
