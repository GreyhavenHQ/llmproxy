package server

import (
	"testing"
	"time"
)

func TestPercentileNearestRank(t *testing.T) {
	values := []float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	if got := percentile(values, 50); got != 5 {
		t.Fatalf("p50 = %v, want 5", got)
	}
	if got := percentile(values, 95); got != 10 {
		t.Fatalf("p95 = %v, want 10", got)
	}
	if got := percentile([]float64{42}, 95); got != 42 {
		t.Fatalf("p95 of one value = %v, want 42", got)
	}
}

func TestFigureEmptyIsNull(t *testing.T) {
	f := newFigure(nil)
	if f.Mean != nil || f.P50 != nil || f.P95 != nil {
		t.Fatalf("empty figure = %+v, want all nil", f)
	}
	f = newFigure([]float64{3, 1, 2})
	if *f.Mean != 2 || *f.P50 != 2 || *f.P95 != 3 {
		t.Fatalf("figure = %v/%v/%v, want 2/2/3", *f.Mean, *f.P50, *f.P95)
	}
}

func TestConcurrencySweep(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	at := func(s int) time.Time { return base.Add(time.Duration(s) * time.Second) }
	from, to := base, at(100)

	overlapping := concurrencySegments([]interval{{at(0), at(30)}, {at(10), at(40)}, {at(20), at(50)}})
	avg, peak := concurrency(overlapping, from, to)
	if peak != 3 {
		t.Fatalf("overlapping peak = %d, want 3", peak)
	}
	if avg != 0.9 {
		t.Fatalf("overlapping average = %v, want 0.9", avg)
	}

	touching := concurrencySegments([]interval{{at(0), at(10)}, {at(10), at(20)}})
	if _, peak := concurrency(touching, from, to); peak != 1 {
		t.Fatalf("touching peak = %d, want 1", peak)
	}

	split := concurrencySegments([]interval{{at(90), at(110)}})
	avg, peak = concurrency(split, from, to)
	if peak != 1 || avg != 0.1 {
		t.Fatalf("boundary-spanning = %v/%d, want 0.1/1", avg, peak)
	}
}
