package store

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"strconv"
	"strings"

	"github.com/monadical/llmproxy/internal/secrets"
)

func (s *Store) InsertUsageEvent(ctx context.Context, ev *UsageEvent, quantities []UsageQuantity) error {
	ev.ID = secrets.NewID()
	if ev.TS == "" {
		ev.TS = Now()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, s.q(`
		INSERT INTO usage_event (id, ts, principal_id, api_key_id, provider_id, alias, upstream_name,
			endpoint, client, tags, status_code, outcome, cancelled, streamed, cost, unpriced, duration_ms)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`),
		ev.ID, ev.TS, ev.PrincipalID, ev.APIKeyID, ev.ProviderID, ev.Alias, ev.UpstreamName,
		ev.Endpoint, ev.Client, ev.Tags, ev.StatusCode, ev.Outcome, boolInt(ev.Cancelled), boolInt(ev.Streamed),
		ev.Cost, boolInt(ev.Unpriced), ev.DurationMs); err != nil {
		return err
	}
	for _, q := range quantities {
		if _, err := tx.ExecContext(ctx, s.q(`
			INSERT INTO usage_quantity (id, usage_event_id, unit, quantity, unit_price, priced, measurement)
			VALUES (?, ?, ?, ?, ?, ?, ?)`),
			secrets.NewID(), ev.ID, q.Unit, q.Quantity, q.UnitPrice, boolInt(q.Priced), q.Measurement); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ListUsageEvents returns all events (test and small-scale introspection use).
func (s *Store) ListUsageEvents(ctx context.Context) ([]UsageEvent, error) {
	rows, err := s.db.QueryContext(ctx, s.q(`
		SELECT id, ts, principal_id, api_key_id, provider_id, alias, upstream_name, endpoint,
			client, tags, status_code, outcome, cancelled, streamed, cost, unpriced, duration_ms
		FROM usage_event ORDER BY ts`))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UsageEvent
	for rows.Next() {
		var ev UsageEvent
		var cancelled, streamed, unpriced int64
		if err := rows.Scan(&ev.ID, &ev.TS, &ev.PrincipalID, &ev.APIKeyID, &ev.ProviderID,
			&ev.Alias, &ev.UpstreamName, &ev.Endpoint, &ev.Client, &ev.Tags, &ev.StatusCode, &ev.Outcome,
			&cancelled, &streamed, &ev.Cost, &unpriced, &ev.DurationMs); err != nil {
			return nil, err
		}
		ev.Cancelled = cancelled != 0
		ev.Streamed = streamed != 0
		ev.Unpriced = unpriced != 0
		out = append(out, ev)
	}
	return out, rows.Err()
}

func (s *Store) ListQuantities(ctx context.Context, usageEventID string) ([]UsageQuantity, error) {
	rows, err := s.db.QueryContext(ctx, s.q(`
		SELECT unit, quantity, unit_price, priced, measurement FROM usage_quantity WHERE usage_event_id = ?`),
		usageEventID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UsageQuantity
	for rows.Next() {
		var q UsageQuantity
		var priced int64
		if err := rows.Scan(&q.Unit, &q.Quantity, &q.UnitPrice, &priced, &q.Measurement); err != nil {
			return nil, err
		}
		q.Priced = priced != 0
		out = append(out, q)
	}
	return out, rows.Err()
}

// ListRequests returns one page of the filtered request log, newest first,
// with quantities and the principal, provider and key names resolved. The
// second ordering key keeps paging stable when several events share a
// timestamp.
func (s *Store) ListRequests(ctx context.Context, f UsageFilter, limit, offset int) ([]RequestLogRow, error) {
	where, args := usageWhere(f)
	// The provider join must be aliased `p`, as usageWhere's provider clause
	// expects; the principal join takes `pp`.
	rows, err := s.db.QueryContext(ctx, s.q(`
		SELECT e.id, e.ts, COALESCE(pp.name, e.principal_id), `+providerNameSQL+`, e.alias, e.endpoint,
			e.client, e.tags, e.api_key_id, COALESCE(k.label, ''), COALESCE(k.key_suffix, ''), e.outcome,
			e.status_code, e.streamed, e.cancelled, e.cost, e.unpriced, e.duration_ms
		FROM usage_event e
			LEFT JOIN principal pp ON e.principal_id = pp.id
			LEFT JOIN provider p ON e.provider_id = p.id
			LEFT JOIN api_key k ON e.api_key_id = k.id`+where+`
		ORDER BY e.ts DESC, e.id DESC LIMIT ? OFFSET ?`), append(args, limit, offset)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RequestLogRow
	index := make(map[string]int)
	ids := make([]any, 0, limit)
	for rows.Next() {
		var r RequestLogRow
		var streamed, cancelled, unpriced int64
		if err := rows.Scan(&r.ID, &r.TS, &r.PrincipalName, &r.Provider, &r.Alias, &r.Endpoint,
			&r.Client, &r.Tags, &r.APIKeyID, &r.KeyLabel, &r.KeySuffix, &r.Outcome,
			&r.StatusCode, &streamed, &cancelled, &r.Cost, &unpriced, &r.DurationMs); err != nil {
			return nil, err
		}
		r.Streamed = streamed != 0
		r.Cancelled = cancelled != 0
		r.Unpriced = unpriced != 0
		r.Units = make(map[string]float64)
		index[r.ID] = len(out)
		ids = append(ids, r.ID)
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return out, nil
	}
	// Fetch quantities for exactly the ids on this page. Re-deriving the set
	// with a subquery would have to repeat the filter and the offset, and any
	// event recorded in between would shift it.
	quantities, err := s.db.QueryContext(ctx, s.q(`
		SELECT q.usage_event_id, q.unit, `+anthropicFlagSQL+`, q.quantity
		FROM usage_quantity q JOIN usage_event e ON q.usage_event_id = e.id
		WHERE q.usage_event_id IN (`+placeholders(len(ids))+`)`), ids...)
	if err != nil {
		return nil, err
	}
	defer quantities.Close()
	for quantities.Next() {
		var eventID, unit string
		var anthropic int64
		var quantity float64
		if err := quantities.Scan(&eventID, &unit, &anthropic, &quantity); err != nil {
			return nil, err
		}
		if i, ok := index[eventID]; ok {
			addQuantity(out[i].Units, unit, quantity, anthropic != 0)
		}
	}
	return out, quantities.Err()
}

// CountRequests is the size of the filtered set, for the pager.
func (s *Store) CountRequests(ctx context.Context, f UsageFilter) (int64, error) {
	where, args := usageWhere(f)
	var n int64
	err := s.db.QueryRowContext(ctx, s.q(`
		SELECT COUNT(*) FROM usage_event e
			LEFT JOIN provider p ON e.provider_id = p.id`+where), args...).Scan(&n)
	return n, err
}

// facetLimit caps every facet list. A User-Agent column is high cardinality
// by nature, and an unbounded DISTINCT over "all time" would return a list no
// dropdown can use.
const facetLimit = 500

// RequestFacets lists the distinct filter values present in a window. Six
// small scans, fetched only when the window changes.
func (s *Store) RequestFacets(ctx context.Context, since, until string) (RequestFacets, error) {
	where, args := usageWhere(UsageFilter{Since: since, Until: until})
	var out RequestFacets

	strs := func(query string) ([]string, error) {
		rows, err := s.db.QueryContext(ctx, s.q(query), args...)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var values []string
		for rows.Next() {
			var v string
			if err := rows.Scan(&v); err != nil {
				return nil, err
			}
			if v != "" {
				values = append(values, v)
			}
		}
		return values, rows.Err()
	}

	var err error
	if out.Principals, err = strs(`
		SELECT DISTINCT COALESCE(pp.name, e.principal_id) AS name
		FROM usage_event e LEFT JOIN principal pp ON e.principal_id = pp.id` + where + `
		ORDER BY name LIMIT ` + strconv.Itoa(facetLimit)); err != nil {
		return out, err
	}
	if out.Providers, err = strs(`
		SELECT DISTINCT ` + providerNameSQL + ` AS name
		FROM usage_event e LEFT JOIN provider p ON e.provider_id = p.id` + where + `
		ORDER BY name LIMIT ` + strconv.Itoa(facetLimit)); err != nil {
		return out, err
	}
	if out.Models, err = strs(`
		SELECT DISTINCT e.alias FROM usage_event e` + where + `
		ORDER BY e.alias LIMIT ` + strconv.Itoa(facetLimit)); err != nil {
		return out, err
	}
	if out.Clients, err = strs(`
		SELECT DISTINCT e.client FROM usage_event e` + where + `
		ORDER BY e.client LIMIT ` + strconv.Itoa(facetLimit)); err != nil {
		return out, err
	}

	// Tags are stored as one canonical list per event, so the distinct lists
	// are split back into pairs here rather than in SQL.
	lists, err := strs(`
		SELECT DISTINCT e.tags FROM usage_event e` + where + ` AND e.tags <> ''
		ORDER BY e.tags LIMIT ` + strconv.Itoa(facetLimit))
	if err != nil {
		return out, err
	}
	pairs := make(map[string]bool)
	for _, list := range lists {
		for _, pair := range strings.Split(list, ",") {
			if pair != "" {
				pairs[pair] = true
			}
		}
	}
	for pair := range pairs {
		out.Tags = append(out.Tags, pair)
	}
	sort.Strings(out.Tags)
	// Each list holds up to 8 pairs, so the split can multiply the scan's cap
	// several times over; every facet list is capped the same way.
	if len(out.Tags) > facetLimit {
		out.Tags = out.Tags[:facetLimit]
	}

	// Keys are the API keys that actually appear in the window; relay tokens
	// share the column but match no api_key row, so they drop out here.
	keys, err := s.db.QueryContext(ctx, s.q(`
		SELECT DISTINCT k.id, k.label, k.key_suffix, COALESCE(pp.name, k.principal_id) AS owner
		FROM usage_event e
			JOIN api_key k ON e.api_key_id = k.id
			LEFT JOIN principal pp ON k.principal_id = pp.id`+where+`
		ORDER BY owner, k.label LIMIT `+strconv.Itoa(facetLimit)), args...)
	if err != nil {
		return out, err
	}
	defer keys.Close()
	for keys.Next() {
		var k FacetKey
		if err := keys.Scan(&k.ID, &k.Label, &k.Suffix, &k.Principal); err != nil {
			return out, err
		}
		out.Keys = append(out.Keys, k)
	}
	return out, keys.Err()
}

// placeholders builds "?, ?, …" for an IN list; Store.q rewrites them
// positionally for Postgres.
func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?, ", n), ", ")
}

func (s *Store) CountUsageEvents(ctx context.Context) (int64, error) {
	var n int64
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM usage_event`).Scan(&n)
	return n, err
}

// anthropicProviderID is the transparent relay's sentinel provider (colons
// are invalid in real provider names, so it cannot collide). The two wire
// shapes disagree on cache accounting: the OpenAI shape counts cached tokens
// as a subset of the prompt count, Anthropic reports cache reads and writes
// outside input_tokens. Read-side aggregation normalises both so that
// input_tokens means non-cached input: the OpenAI cached subset is
// subtracted out, and Anthropic cache writes (fresh input billed at a
// premium rate) fold in while cache reads stay their own unit. Per-event
// quantities stay raw as the upstream reported them.
const anthropicProviderID = "transparent:anthropic"

// anthropicFlagSQL marks quantity rows from the relay for the fold above.
const anthropicFlagSQL = `CASE WHEN e.provider_id = '` + anthropicProviderID + `' THEN 1 ELSE 0 END`

// providerNameSQL resolves a usage event's provider for display and
// filtering: the provider's name, the relay sentinel as itself, and events
// whose provider row was deleted as "(deleted)" rather than a raw id.
// Queries using it must join `LEFT JOIN provider p ON e.provider_id = p.id`.
const providerNameSQL = `CASE WHEN p.name IS NOT NULL THEN p.name ` +
	`WHEN e.provider_id = '` + anthropicProviderID + `' THEN e.provider_id ` +
	`ELSE '(deleted)' END`

// completedSQL keeps the events that actually consumed the upstream: ok and
// cancelled (billed) requests. Failures carry an arbitrary requested model
// and provider, so they stay out of the usage breakdowns; the series and the
// request log still show them.
const completedSQL = ` AND (e.cancelled = 1 OR e.outcome = 'ok')`

// addQuantity accumulates one aggregated quantity into a units map,
// normalising input_tokens to the non-cached input; see the comment on
// anthropicProviderID.
func addQuantity(units map[string]float64, unit string, quantity float64, anthropic bool) {
	units[unit] += quantity
	if anthropic && unit == "cache_creation_tokens" {
		units["input_tokens"] += quantity
	}
	if !anthropic && unit == "cached_input_tokens" {
		units["input_tokens"] -= quantity
	}
}

// likeEscaper neutralises the LIKE metacharacters, so a filter value is
// matched literally.
var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

// likePrefix escapes LIKE metacharacters and appends the wildcard, so the
// filter value is matched as a literal prefix.
func likePrefix(value string) string {
	return likeEscaper.Replace(value) + "%"
}

// likeTag matches one exact "key:value" pair inside the stored comma-joined
// tag list. The list is wrapped in commas on both sides so a pair cannot
// match a prefix of a longer one.
func likeTag(pair string) string {
	return "%," + likeEscaper.Replace(pair) + ",%"
}

// usageWhere builds the WHERE clause shared by the usage aggregates. The
// provider filter matches the resolved provider name, so every query using it
// must join `LEFT JOIN provider p ON e.provider_id = p.id`. Tag entries
// narrow together: an event must carry every pair asked for.
func usageWhere(f UsageFilter) (string, []any) {
	where := ` WHERE 1=1`
	var args []any
	if f.PrincipalID != "" {
		where += ` AND e.principal_id = ?`
		args = append(args, f.PrincipalID)
	}
	if f.APIKeyID != "" {
		where += ` AND e.api_key_id = ?`
		args = append(args, f.APIKeyID)
	}
	if f.Provider != "" {
		where += ` AND ` + providerNameSQL + ` = ?`
		args = append(args, f.Provider)
	}
	if f.Model != "" {
		where += ` AND e.alias = ?`
		args = append(args, f.Model)
	}
	if f.Client != "" {
		where += ` AND e.client LIKE ? ESCAPE '\'`
		args = append(args, likePrefix(f.Client))
	}
	// || is the portable string concatenation in both dialects.
	for _, pair := range f.Tags {
		where += ` AND (',' || e.tags || ',') LIKE ? ESCAPE '\'`
		args = append(args, likeTag(pair))
	}
	// An app tag is a ",app:" pair in the comma-wrapped list. Values are
	// non-empty (the tag grammar starts alphanumeric), so a present key is a
	// real app, and the leading comma keeps "app:" from matching a longer key.
	if f.AppTagged {
		where += ` AND (',' || e.tags || ',') LIKE '%,app:%'`
	}
	if f.Since != "" {
		where += ` AND e.ts >= ?`
		args = append(args, f.Since)
	}
	if f.Until != "" {
		where += ` AND e.ts < ?`
		args = append(args, f.Until)
	}
	return where, args
}

// UsageSummary aggregates by (principal, alias, endpoint) with per-unit sums.
// Empty principalID/since/until disable the corresponding filter.
func (s *Store) UsageSummary(ctx context.Context, principalID, since, until string) ([]UsageSummaryRow, error) {
	where, args := usageWhere(UsageFilter{PrincipalID: principalID, Since: since, Until: until})

	rowsByKey := make(map[[3]string]*UsageSummaryRow)
	events, err := s.db.QueryContext(ctx, s.q(`
		SELECT e.principal_id, e.alias, e.endpoint, COUNT(*), SUM(e.cost), SUM(e.cancelled)
		FROM usage_event e`+where+`
		GROUP BY e.principal_id, e.alias, e.endpoint`), args...)
	if err != nil {
		return nil, err
	}
	defer events.Close()
	for events.Next() {
		var r UsageSummaryRow
		if err := events.Scan(&r.PrincipalID, &r.Alias, &r.Endpoint, &r.Requests, &r.Cost, &r.Cancelled); err != nil {
			return nil, err
		}
		r.Units = make(map[string]float64)
		rowsByKey[[3]string{r.PrincipalID, r.Alias, r.Endpoint}] = &r
	}
	if err := events.Err(); err != nil {
		return nil, err
	}

	quantities, err := s.db.QueryContext(ctx, s.q(`
		SELECT e.principal_id, e.alias, e.endpoint, q.unit, `+anthropicFlagSQL+`, SUM(q.quantity)
		FROM usage_quantity q JOIN usage_event e ON q.usage_event_id = e.id`+where+`
		GROUP BY e.principal_id, e.alias, e.endpoint, q.unit, `+anthropicFlagSQL), args...)
	if err != nil {
		return nil, err
	}
	defer quantities.Close()
	for quantities.Next() {
		var pid, alias, endpoint, unit string
		var anthropic int64
		var quantity float64
		if err := quantities.Scan(&pid, &alias, &endpoint, &unit, &anthropic, &quantity); err != nil {
			return nil, err
		}
		row, ok := rowsByKey[[3]string{pid, alias, endpoint}]
		if !ok {
			row = &UsageSummaryRow{PrincipalID: pid, Alias: alias, Endpoint: endpoint, Units: make(map[string]float64)}
			rowsByKey[[3]string{pid, alias, endpoint}] = row
		}
		addQuantity(row.Units, unit, quantity, anthropic != 0)
	}
	if err := quantities.Err(); err != nil {
		return nil, err
	}

	out := make([]UsageSummaryRow, 0, len(rowsByKey))
	for _, r := range rowsByKey {
		out = append(out, *r)
	}
	return out, nil
}

// UsageBreakdown aggregates the filter window across every recorded dimension:
// (principal, provider, alias, endpoint, client, tags), with per-unit sums. One
// query feeds every roll-up the dashboard shows, and the distinct values
// double as its filter options. Only completed (ok or cancelled) requests
// that carry a model count: a failure's model and provider are whatever the
// caller asked for, and model-less events (the relay's count_tokens and
// models probes report no model and no usage) have no place in a by-model
// breakdown. Both stay visible in the series and the log.
func (s *Store) UsageBreakdown(ctx context.Context, f UsageFilter) ([]UsageBreakdownRow, error) {
	where, args := usageWhere(f)
	where += completedSQL + ` AND e.alias <> ''`
	const dims = `e.principal_id, ` + providerNameSQL + `, e.alias, e.endpoint, e.client, e.tags`

	rowsByKey := make(map[[6]string]*UsageBreakdownRow)
	events, err := s.db.QueryContext(ctx, s.q(`
		SELECT `+dims+`, COUNT(*), SUM(e.cost), SUM(e.cancelled)
		FROM usage_event e LEFT JOIN provider p ON e.provider_id = p.id`+where+`
		GROUP BY `+dims), args...)
	if err != nil {
		return nil, err
	}
	defer events.Close()
	for events.Next() {
		var r UsageBreakdownRow
		if err := events.Scan(&r.PrincipalID, &r.Provider, &r.Alias, &r.Endpoint, &r.Client, &r.Tags,
			&r.Requests, &r.Cost, &r.Cancelled); err != nil {
			return nil, err
		}
		r.Units = make(map[string]float64)
		rowsByKey[[6]string{r.PrincipalID, r.Provider, r.Alias, r.Endpoint, r.Client, r.Tags}] = &r
	}
	if err := events.Err(); err != nil {
		return nil, err
	}

	quantities, err := s.db.QueryContext(ctx, s.q(`
		SELECT `+dims+`, q.unit, SUM(q.quantity)
		FROM usage_quantity q JOIN usage_event e ON q.usage_event_id = e.id
			LEFT JOIN provider p ON e.provider_id = p.id`+where+`
		GROUP BY `+dims+`, q.unit`), args...)
	if err != nil {
		return nil, err
	}
	defer quantities.Close()
	for quantities.Next() {
		var key [6]string
		var unit string
		var quantity float64
		if err := quantities.Scan(&key[0], &key[1], &key[2], &key[3], &key[4], &key[5],
			&unit, &quantity); err != nil {
			return nil, err
		}
		row, ok := rowsByKey[key]
		if !ok {
			row = &UsageBreakdownRow{PrincipalID: key[0], Provider: key[1], Alias: key[2],
				Endpoint: key[3], Client: key[4], Tags: key[5], Units: make(map[string]float64)}
			rowsByKey[key] = row
		}
		addQuantity(row.Units, unit, quantity, key[1] == anthropicProviderID)
	}
	if err := quantities.Err(); err != nil {
		return nil, err
	}

	out := make([]UsageBreakdownRow, 0, len(rowsByKey))
	for _, r := range rowsByKey {
		out = append(out, *r)
	}
	return out, nil
}

// UsageSeries aggregates usage into fixed-width time buckets, by hour or by
// day. Bucketing is a prefix of the stored UTC timestamp, so it needs no
// backend-specific date functions. Empty filter fields disable that filter.
func (s *Store) UsageSeries(ctx context.Context, f UsageFilter, hourly bool) ([]UsageSeriesRow, error) {
	bucket := "SUBSTR(e.ts, 1, 10)"
	if hourly {
		bucket = "SUBSTR(e.ts, 1, 13)"
	}
	where, args := usageWhere(f)

	// A cancelled request is its own outcome, so the three counts partition
	// the total exactly.
	rowsByBucket := make(map[string]*UsageSeriesRow)
	events, err := s.db.QueryContext(ctx, s.q(`
		SELECT `+bucket+`, COUNT(*),
			SUM(CASE WHEN e.cancelled = 0 AND e.outcome = 'ok' THEN 1 ELSE 0 END),
			SUM(e.cancelled),
			SUM(CASE WHEN e.cancelled = 0 AND e.outcome <> 'ok' THEN 1 ELSE 0 END),
			SUM(e.unpriced), SUM(e.cost)
		FROM usage_event e LEFT JOIN provider p ON e.provider_id = p.id`+where+`
		GROUP BY `+bucket), args...)
	if err != nil {
		return nil, err
	}
	defer events.Close()
	for events.Next() {
		var r UsageSeriesRow
		if err := events.Scan(&r.Bucket, &r.Requests, &r.OK, &r.Cancelled, &r.Failed,
			&r.Unpriced, &r.Cost); err != nil {
			return nil, err
		}
		r.Units = make(map[string]float64)
		rowsByBucket[r.Bucket] = &r
	}
	if err := events.Err(); err != nil {
		return nil, err
	}

	quantities, err := s.db.QueryContext(ctx, s.q(`
		SELECT `+bucket+`, q.unit, `+anthropicFlagSQL+`, SUM(q.quantity)
		FROM usage_quantity q JOIN usage_event e ON q.usage_event_id = e.id
			LEFT JOIN provider p ON e.provider_id = p.id`+where+`
		GROUP BY `+bucket+`, q.unit, `+anthropicFlagSQL), args...)
	if err != nil {
		return nil, err
	}
	defer quantities.Close()
	for quantities.Next() {
		var key, unit string
		var anthropic int64
		var quantity float64
		if err := quantities.Scan(&key, &unit, &anthropic, &quantity); err != nil {
			return nil, err
		}
		row, ok := rowsByBucket[key]
		if !ok {
			row = &UsageSeriesRow{Bucket: key, Units: make(map[string]float64)}
			rowsByBucket[key] = row
		}
		addQuantity(row.Units, unit, quantity, anthropic != 0)
	}
	if err := quantities.Err(); err != nil {
		return nil, err
	}

	out := make([]UsageSeriesRow, 0, len(rowsByBucket))
	for _, r := range rowsByBucket {
		out = append(out, *r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Bucket < out[j].Bucket })
	return out, nil
}

// StorePricingFeed deactivates prior feeds and inserts the new one.
func (s *Store) StorePricingFeed(ctx context.Context, version, origin string, entries map[[2]string]float64, audit *Audit) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := s.insertFeedTx(ctx, tx, version, origin, entries); err != nil {
		return err
	}
	if err := s.auditTx(ctx, tx, audit); err != nil {
		return err
	}
	return tx.Commit()
}

// ReplacePricingForModels rewrites the active feed as a new version with every
// entry keyed on one of the named models replaced by that model's price set
// (per unit, not per million; an empty set clears the model). Entries for
// models not named carry over untouched. It returns the new entry set so the
// caller can swap in the in-memory index.
func (s *Store) ReplacePricingForModels(ctx context.Context, version, origin string,
	sets map[string]map[string]float64, audit *Audit) (map[[2]string]float64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	// Read the live feed inside the transaction so two concurrent model edits
	// cannot lose one another's entries.
	current, err := s.activeEntriesTx(ctx, tx)
	if err != nil {
		return nil, err
	}
	entries := make(map[[2]string]float64, len(current))
	for key, price := range current {
		if _, replaced := sets[key[0]]; !replaced {
			entries[key] = price
		}
	}
	for model, prices := range sets {
		for unit, price := range prices {
			entries[[2]string{model, unit}] = price
		}
	}
	if err := s.insertFeedTx(ctx, tx, version, origin, entries); err != nil {
		return nil, err
	}
	if err := s.auditTx(ctx, tx, audit); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return entries, nil
}

func (s *Store) insertFeedTx(ctx context.Context, tx *sql.Tx, version, origin string, entries map[[2]string]float64) error {
	if _, err := tx.ExecContext(ctx, `UPDATE pricing_feed SET active = 0`); err != nil {
		return err
	}
	feedID := secrets.NewID()
	if _, err := tx.ExecContext(ctx, s.q(`
		INSERT INTO pricing_feed (id, version, origin, loaded_at, active) VALUES (?, ?, ?, ?, 1)`),
		feedID, version, origin, Now()); err != nil {
		return err
	}
	for key, price := range entries {
		if _, err := tx.ExecContext(ctx, s.q(`
			INSERT INTO pricing_entry (id, feed_id, model, unit, price_per_unit) VALUES (?, ?, ?, ?, ?)`),
			secrets.NewID(), feedID, key[0], key[1], price); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) activeEntriesTx(ctx context.Context, tx *sql.Tx) (map[[2]string]float64, error) {
	entries := make(map[[2]string]float64)
	rows, err := tx.QueryContext(ctx, `
		SELECT model, unit, price_per_unit FROM pricing_entry
		WHERE feed_id = (SELECT id FROM pricing_feed WHERE active = 1 ORDER BY loaded_at DESC LIMIT 1)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var model, unit string
		var price float64
		if err := rows.Scan(&model, &unit, &price); err != nil {
			return nil, err
		}
		entries[[2]string{model, unit}] = price
	}
	return entries, rows.Err()
}

// LoadActivePricing returns the active feed's version and entries; empty
// version means no feed is loaded.
func (s *Store) LoadActivePricing(ctx context.Context) (string, map[[2]string]float64, error) {
	var feedID, version string
	err := s.db.QueryRowContext(ctx, `
		SELECT id, version FROM pricing_feed WHERE active = 1 ORDER BY loaded_at DESC LIMIT 1`).
		Scan(&feedID, &version)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil, nil // no active feed
	}
	if err != nil {
		return "", nil, err
	}
	rows, err := s.db.QueryContext(ctx,
		s.q(`SELECT model, unit, price_per_unit FROM pricing_entry WHERE feed_id = ?`), feedID)
	if err != nil {
		return "", nil, err
	}
	defer rows.Close()
	entries := make(map[[2]string]float64)
	for rows.Next() {
		var model, unit string
		var price float64
		if err := rows.Scan(&model, &unit, &price); err != nil {
			return "", nil, err
		}
		entries[[2]string{model, unit}] = price
	}
	return version, entries, rows.Err()
}
