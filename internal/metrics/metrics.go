// Package metrics: minimal in-process Prometheus-text counters.
//
// Aggregate labels only (provider, model alias, endpoint, unit, outcome); no
// per-principal labels, so cardinality stays bounded. Per-principal detail
// lives in the database.
package metrics

import (
	"fmt"
	"sort"
	"strings"
	"sync"
)

type Metrics struct {
	mu       sync.Mutex
	counters map[string]float64
}

func New() *Metrics {
	return &Metrics{counters: make(map[string]float64)}
}

func key(name string, labels [][2]string) string {
	sort.Slice(labels, func(i, j int) bool { return labels[i][0] < labels[j][0] })
	var b strings.Builder
	b.WriteString(name)
	if len(labels) > 0 {
		b.WriteByte('{')
		for i, l := range labels {
			if i > 0 {
				b.WriteByte(',')
			}
			v := strings.ReplaceAll(l[1], `\`, `\\`)
			v = strings.ReplaceAll(v, `"`, `\"`)
			fmt.Fprintf(&b, `%s=%q`, l[0], v)
		}
		b.WriteByte('}')
	}
	return b.String()
}

func (m *Metrics) Inc(name string, labels [][2]string, value float64) {
	k := key(name, labels)
	m.mu.Lock()
	m.counters[k] += value
	m.mu.Unlock()
}

func (m *Metrics) ObserveRequest(endpoint, provider, alias, outcome string, durationMs int64) {
	base := [][2]string{{"endpoint", endpoint}, {"provider", provider}, {"model", alias}}
	m.Inc("llmproxy_requests_total", append(append([][2]string{}, base...), [2]string{"outcome", outcome}), 1)
	m.Inc("llmproxy_request_seconds_sum", append([][2]string{}, base...), float64(durationMs)/1000)
	m.Inc("llmproxy_request_seconds_count", append([][2]string{}, base...), 1)
}

func (m *Metrics) ObserveUnits(provider, alias, unit string, quantity float64, priced bool) {
	m.Inc("llmproxy_usage_units_total",
		[][2]string{{"provider", provider}, {"model", alias}, {"unit", unit}}, quantity)
	if !priced {
		m.Inc("llmproxy_unpriced_units_total",
			[][2]string{{"model", alias}, {"unit", unit}}, quantity)
	}
}

func (m *Metrics) Render() string {
	m.mu.Lock()
	keys := make([]string, 0, len(m.counters))
	for k := range m.counters {
		keys = append(keys, k)
	}
	values := make(map[string]float64, len(m.counters))
	for k, v := range m.counters {
		values[k] = v
	}
	m.mu.Unlock()
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&b, "%s %g\n", k, values[k])
	}
	return b.String()
}
