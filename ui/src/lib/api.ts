// Thin client for the proxy's JSON APIs. Errors are the OpenAI-shaped
// envelope; ApiError surfaces the stable machine code alongside the message.

export class ApiError extends Error {
  code: string
  status: number

  constructor(message: string, code: string, status: number) {
    super(message)
    this.code = code
    this.status = status
  }
}

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const resp = await fetch(path, {
    method,
    headers: body !== undefined ? { 'Content-Type': 'application/json' } : undefined,
    body: body !== undefined ? JSON.stringify(body) : undefined,
    credentials: 'same-origin',
  })
  const text = await resp.text()
  let data: unknown = null
  try {
    data = text ? JSON.parse(text) : null
  } catch {
    // non-JSON body; fall through to status handling
  }
  if (!resp.ok) {
    const err = (data as { error?: { message?: string; code?: string } } | null)?.error
    throw new ApiError(err?.message ?? `HTTP ${resp.status}`, err?.code ?? 'http_error', resp.status)
  }
  return data as T
}

export const api = {
  get: <T>(path: string) => request<T>('GET', path),
  post: <T>(path: string, body?: unknown) => request<T>('POST', path, body),
  patch: <T>(path: string, body?: unknown) => request<T>('PATCH', path, body),
  del: <T>(path: string) => request<T>('DELETE', path),
}

export interface Me {
  authenticated: boolean
  name?: string
  role?: string
  sso_enabled: boolean
  password_enabled: boolean
}

export interface KeyInfo {
  id: string
  key_suffix: string
  label: string
  created_at: string
  last_used_at: string | null
  key?: string // present only in the creation response
  principal?: string // present only on the admin endpoints
}

// A principal is anything that can own keys and usage: a person (kind
// "user") or a service account (kind "service").
export interface Principal {
  id: string
  name: string
  kind: string
  role: string
}

export interface RelayTokenInfo {
  id: string
  token_suffix: string
  label: string
  created_at: string
  last_used_at: string | null
  token?: string // present only in the creation response
}

export interface UsageRow {
  model: string
  endpoint: string
  requests: number
  cancelled: number
  cost: number | null
  units: Record<string, number>
  principal?: string
}

// One cell of /stats/summary: the window aggregated over every recorded
// dimension. The dashboard rolls these up per dimension and derives its
// filter options from the distinct values.
export interface StatsRow {
  principal: string
  provider: string
  model: string
  endpoint: string
  client: string
  requests: number
  cancelled: number
  cost: number | null
  units: Record<string, number>
}

// clientProduct reduces a stored User-Agent to its versioned product token
// ("claude-cli/2.0.13 (external, cli)" -> "claude-cli/2.0.13");
// clientFamily drops the version too ("claude-cli"). These are the
// dashboard's two grouping levels; the exact string stays visible in
// tooltips and the request log. The server's client filter is a prefix
// match, so both are valid filter values.
export function clientProduct(client: string): string {
  return client.trim().split(' ')[0] || 'unknown'
}

export function clientFamily(client: string): string {
  const token = client.trim().split(' ')[0].split('/')[0]
  return token || 'unknown'
}

export interface Provider {
  name: string
  base_url: string
  has_credential: boolean
  enabled: boolean
}

export interface Model {
  alias: string
  // provider, upstream_name and capabilities are always the resolved ones:
  // for an alias they are the target's. target names the model pointed at,
  // and is null for a model that routes to a provider directly.
  provider: string
  upstream_name: string
  capabilities: string[]
  target: string | null
  origin: string
  created_at: string
  // Prices per million units, keyed by unit; a unit without an entry is
  // unpriced. Inherited means they are keyed on the upstream model name
  // rather than on this alias.
  pricing: Record<string, number>
  pricing_inherited: boolean
}

// One time bucket of the usage series. cost is null when nothing in the
// bucket had a price: unpriced is a state, never zero.
export interface UsageBucket {
  start: string
  // requests is the total; ok, cancelled and failed partition it.
  requests: number
  ok: number
  cancelled: number
  failed: number
  unpriced_requests: number
  cost: number | null
  units: Record<string, number>
}

export interface DiscoveredModel {
  upstream_name: string
  bound_alias: string | null
}

export interface RequestRow {
  ts: string
  principal: string
  model: string
  endpoint: string
  client: string
  outcome: string
  status_code: number | null
  streamed: boolean
  cancelled: boolean
  cost: number | null
  unpriced: boolean
  duration_ms: number
  units: Record<string, number>
}

export function formatCost(cost: number | null): string {
  if (cost === null || cost === undefined) return 'unpriced'
  return `$${cost.toFixed(6)}`
}

// formatMoney is the readable form for headline figures and axes: enough
// precision to see small amounts, not enough to read as a ledger.
export function formatMoney(cost: number | null): string {
  if (cost === null || cost === undefined) return 'unpriced'
  if (cost === 0) return '$0'
  if (cost < 0.01) return `$${cost.toFixed(5)}`
  if (cost < 1000) return `$${cost.toFixed(2)}`
  return `$${new Intl.NumberFormat(undefined, { maximumFractionDigits: 0 }).format(cost)}`
}

export function formatTokens(n: number | undefined): string {
  if (!n) return ''
  return new Intl.NumberFormat().format(n)
}

export function formatNumber(n: number): string {
  return new Intl.NumberFormat().format(n)
}

// formatCompact keeps big token counts short enough for a stat tile or an axis.
export function formatCompact(n: number): string {
  if (Math.abs(n) < 1000) return new Intl.NumberFormat().format(n)
  return new Intl.NumberFormat(undefined, {
    notation: 'compact',
    maximumFractionDigits: 1,
  }).format(n)
}

export function formatDate(ts: string | null): string {
  if (!ts) return ''
  const d = new Date(ts)
  return isNaN(d.getTime()) ? ts : d.toLocaleString()
}

export function formatDay(ts: string | null): string {
  if (!ts) return ''
  const d = new Date(ts)
  return isNaN(d.getTime()) ? ts : d.toLocaleDateString()
}
