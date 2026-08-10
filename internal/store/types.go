package store

import "database/sql"

type Principal struct {
	ID          string
	Name        string
	Kind        string // user | service
	Role        string // member | admin
	ExternalSub sql.NullString
	Email       sql.NullString
	CreatedAt   string
	// SessionsRevokedBefore invalidates browser sessions issued before it;
	// API keys are unaffected.
	SessionsRevokedBefore sql.NullString
}

type APIKey struct {
	ID          string
	PrincipalID string
	// Suffix is the plaintext's last characters, kept for display ("***xxxx").
	Suffix     string
	Label      string
	CreatedAt  string
	LastUsedAt sql.NullString
	// PrincipalName is populated by joined queries.
	PrincipalName string
}

// RelayToken attributes transparent-relay traffic to a principal. Same
// lifecycle mechanics as an API key (keyed hash, shown once, deletion is
// revocation) but structurally a different object: it authenticates nothing.
type RelayToken struct {
	ID          string
	PrincipalID string
	Suffix      string
	Label       string
	CreatedAt   string
	LastUsedAt  sql.NullString
	// PrincipalName is populated by joined queries.
	PrincipalName string
}

type Provider struct {
	ID                   string
	Name                 string
	WireFormat           string
	BaseURL              string
	CredentialCiphertext sql.NullString
	VerifyTLS            bool
	CAPEM                sql.NullString
	TimeoutConnect       float64
	TimeoutRead          float64
	TimeoutWrite         float64
	MaxConcurrency       sql.NullInt64
	Enabled              bool
	CreatedAt            string
}

// ModelBinding is one caller-facing name. It either routes directly to a
// provider's model or is an alias for another binding (TargetID). Reads
// resolve the alias, so ProviderID, ProviderName, UpstreamName and
// CapabilitySet always describe where a call actually goes; TargetAlias says
// whether that came from this row or from the one it points at.
type ModelBinding struct {
	ID            string
	Alias         string
	ProviderID    string
	UpstreamName  string
	CapabilitySet string
	Origin        string
	DiscoveredAt  sql.NullString
	CreatedAt     string
	// TargetID points at the binding this one aliases; invalid when direct.
	TargetID sql.NullString
	// TargetAlias and ProviderName are populated by joined queries.
	// TargetAlias is empty for a direct binding.
	TargetAlias  string
	ProviderName string
}

type UsageEvent struct {
	ID           string
	TS           string
	PrincipalID  string
	APIKeyID     string
	ProviderID   string
	Alias        string
	UpstreamName string
	Endpoint     string
	// Client is the caller's User-Agent header, truncated at capture time.
	// Header metadata only, never content.
	Client     string
	StatusCode sql.NullInt64
	Outcome    string
	Cancelled  bool
	Streamed   bool
	Cost       sql.NullFloat64
	Unpriced   bool
	DurationMs int64
}

type UsageQuantity struct {
	Unit        string
	Quantity    float64
	UnitPrice   sql.NullFloat64
	Priced      bool
	Measurement string
}

type AdminEvent struct {
	TS               string
	ActorPrincipalID string
	Action           string
	TargetKind       string
	TargetRef        string
}

// Audit describes the metadata-only admin_event row written inside the same
// transaction as a mutation.
type Audit struct {
	Actor      string
	Action     string
	TargetKind string
	TargetRef  string
}

type AuthResult struct {
	KeyID         string
	PrincipalID   string
	PrincipalName string
	Role          string
	LastUsedAt    sql.NullString
}

// RequestLogRow is one usage event with its per-unit quantities and the
// principal name resolved, for the request log (metadata only). KeyLabel and
// KeySuffix are empty when the event's api_key_id names no API key row: a
// deleted key, or a relay token (the relay stores its own token id there).
type RequestLogRow struct {
	ID            string
	TS            string
	PrincipalName string
	Provider      string
	Alias         string
	Endpoint      string
	Client        string
	APIKeyID      string
	KeyLabel      string
	KeySuffix     string
	Outcome       string
	StatusCode    sql.NullInt64
	Streamed      bool
	Cancelled     bool
	Cost          sql.NullFloat64
	Unpriced      bool
	DurationMs    int64
	Units         map[string]float64
}

// RequestFacets are the distinct values available in a time window, feeding
// the request explorer's filter dropdowns. Derived from every event, not just
// the completed ones the usage breakdown keeps: the explorer exists to find
// failures, so a user or key that has only ever failed must still be
// selectable. Each list is capped, so a high-cardinality dimension (clients,
// above all) cannot inflate the response.
type RequestFacets struct {
	Principals []string
	Providers  []string
	Models     []string
	Clients    []string
	Keys       []FacetKey
}

// FacetKey is one API key offered as a filter option. Labels and suffixes are
// team-visible here, as the rest of the stats surface already is.
type FacetKey struct {
	ID        string
	Label     string
	Suffix    string
	Principal string
}

type UsageSummaryRow struct {
	PrincipalID string
	Alias       string
	Endpoint    string
	Requests    int64
	Cancelled   int64
	Cost        sql.NullFloat64
	Units       map[string]float64
}

// UsageFilter narrows usage queries. Empty fields disable that filter.
// Provider matches the resolved provider name (the relay sentinel included);
// Model matches the alias the caller used. Client is a prefix match on the
// stored User-Agent, so a product token like "claude-cli" covers every
// version. APIKeyID matches the key the request authenticated with; relay
// traffic carries a relay token id there and so never matches.
type UsageFilter struct {
	PrincipalID string
	APIKeyID    string
	Provider    string
	Model       string
	Client      string
	Since       string
	Until       string
}

// UsageBreakdownRow is one cell of the full-dimensional aggregate: usage for
// the filter window grouped by principal, provider, model, endpoint and
// client. The UI rolls these up into whichever dimension it displays.
type UsageBreakdownRow struct {
	PrincipalID string
	Provider    string
	Alias       string
	Endpoint    string
	Client      string
	Requests    int64
	Cancelled   int64
	Cost        sql.NullFloat64
	Units       map[string]float64
}

// UsageSeriesRow is one time bucket of aggregated usage. Bucket is the
// truncated timestamp key ("2006-01-02" by day, "2006-01-02T15" by hour);
// coarser granularities are rolled up from days by the caller.
type UsageSeriesRow struct {
	Bucket string
	// Requests is the total; OK, Cancelled and Failed partition it.
	Requests  int64
	OK        int64
	Cancelled int64
	Failed    int64
	Unpriced  int64
	Cost      sql.NullFloat64
	Units     map[string]float64
}
