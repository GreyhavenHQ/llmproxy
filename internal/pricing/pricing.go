// Package pricing: a versioned data feed keyed on (model, unit).
//
// A (model, unit) pair with no entry is recorded as unpriced, never as zero.
// Lookup walks a model's names most-specific first: its alias, the alias it
// points at, then the upstream model name.
package pricing

import (
	"encoding/json"
	"fmt"
)

var ValidUnits = map[string]bool{
	"input_tokens":          true,
	"output_tokens":         true,
	"cached_input_tokens":   true,
	"cache_creation_tokens": true,
	"audio_seconds":         true,
}

type Index struct {
	Version string
	entries map[[2]string]float64
}

func NewIndex(version string, entries map[[2]string]float64) *Index {
	if entries == nil {
		entries = make(map[[2]string]float64)
	}
	return &Index{Version: version, entries: entries}
}

func Empty() *Index { return NewIndex("", nil) }

// Lookup resolves a unit's price against the names a model is known by, in
// order of specificity: its own alias, the alias it points at, then the
// upstream model name. The first entry found wins, so a name can override
// what it would otherwise inherit. Empty names are skipped.
func (i *Index) Lookup(unit string, models ...string) (float64, bool) {
	for _, model := range models {
		if model == "" {
			continue
		}
		if price, ok := i.entries[[2]string{model, unit}]; ok {
			return price, true
		}
	}
	return 0, false
}

// LookupModel resolves one exact key, without the upstream-name fallback.
func (i *Index) LookupModel(model, unit string) (float64, bool) {
	price, ok := i.entries[[2]string{model, unit}]
	return price, ok
}

func (i *Index) Len() int { return len(i.entries) }

func (i *Index) Entries() map[[2]string]float64 { return i.entries }

type feedDoc struct {
	Version string `json:"version"`
	Entries []struct {
		Model           string   `json:"model"`
		Unit            string   `json:"unit"`
		PricePerUnit    *float64 `json:"price_per_unit"`
		PricePerMillion *float64 `json:"price_per_million"`
	} `json:"entries"`
}

// ParseFeed validates and normalises a feed document to per-unit prices.
func ParseFeed(raw []byte) (*Index, error) {
	var doc feedDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("pricing feed is not valid JSON: %w", err)
	}
	if doc.Version == "" {
		return nil, fmt.Errorf("pricing feed needs a string 'version'")
	}
	entries := make(map[[2]string]float64, len(doc.Entries))
	for i, e := range doc.Entries {
		if e.Model == "" || !ValidUnits[e.Unit] {
			return nil, fmt.Errorf("entry %d: needs 'model' and a valid 'unit'", i)
		}
		switch {
		case e.PricePerUnit != nil:
			entries[[2]string{e.Model, e.Unit}] = *e.PricePerUnit
		case e.PricePerMillion != nil:
			entries[[2]string{e.Model, e.Unit}] = *e.PricePerMillion / 1_000_000
		default:
			return nil, fmt.Errorf("entry %d: needs 'price_per_unit' or 'price_per_million'", i)
		}
	}
	return NewIndex(doc.Version, entries), nil
}
