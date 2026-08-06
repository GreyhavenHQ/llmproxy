// The shared usage dashboard: every authenticated user sees the whole
// proxy's usage, filterable by user, provider and client. One filter row
// scopes the stat tiles, the charts and the breakdowns under them. Buckets
// are UTC, matching how the proxy records events.
//
// Data flow: the time series is filtered server-side (buckets carry no
// dimensions), while the summary is fetched once per range and sliced
// client-side — its rows carry every dimension, so they feed the donuts, the
// client bars, the table and the filter dropdowns from a single request.

import { useMemo, useState } from 'react'
import {
  api,
  clientFamily,
  clientProduct,
  formatCompact,
  formatMoney,
  formatNumber,
  formatTokens,
  type StatsRow,
  type UsageBucket,
  type UsageRow,
} from '@/lib/api'
import { useAsync } from '@/lib/useAsync'
import {
  ColumnChart,
  Donut,
  foldDonut,
  SERIES_ACCENT,
  SERIES_GRAY,
  ShareBars,
  StatTile,
  type ChartPoint,
  type DonutSlice,
  type ShareRow,
} from '@/components/charts'
import { UsageTable } from '@/components/UsageTable'
import { Button } from '@/components/ui/button'
import { Spinner } from '@/components/ui/spinner'
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { ChartPie, ChartBar, FilterX, RefreshCw, TableProperties, Tags, X } from 'lucide-react'

type Bucket = 'hour' | 'day' | 'week' | 'month'

interface Range {
  key: string
  label: string
  bucket: Bucket
  count: number // 0 = everything recorded, no previous window to compare against
}

const RANGES: Range[] = [
  { key: '24h', label: 'Last 24 hours', bucket: 'hour', count: 24 },
  { key: '7d', label: 'Last 7 days', bucket: 'day', count: 7 },
  { key: '30d', label: 'Last 30 days', bucket: 'day', count: 30 },
  { key: '12w', label: 'Last 12 weeks', bucket: 'week', count: 12 },
  { key: '12mo', label: 'Last 12 months', bucket: 'month', count: 12 },
  { key: 'all', label: 'All time', bucket: 'month', count: 0 },
]

function floorBucket(date: Date, bucket: Bucket): Date {
  const d = new Date(date)
  d.setUTCMilliseconds(0)
  d.setUTCSeconds(0)
  d.setUTCMinutes(0)
  if (bucket === 'hour') return d
  d.setUTCHours(0)
  if (bucket === 'month') {
    d.setUTCDate(1)
    return d
  }
  if (bucket === 'week') {
    d.setUTCDate(d.getUTCDate() - ((d.getUTCDay() + 6) % 7)) // back to Monday
  }
  return d
}

function addBuckets(date: Date, bucket: Bucket, n: number): Date {
  const d = new Date(date)
  if (bucket === 'hour') d.setUTCHours(d.getUTCHours() + n)
  else if (bucket === 'day') d.setUTCDate(d.getUTCDate() + n)
  else if (bucket === 'week') d.setUTCDate(d.getUTCDate() + n * 7)
  else d.setUTCMonth(d.getUTCMonth() + n)
  return d
}

const UTC: Intl.DateTimeFormatOptions = { timeZone: 'UTC' }

function bucketLabel(start: string, bucket: Bucket): string {
  const d = new Date(start)
  if (bucket === 'hour') {
    return d.toLocaleTimeString(undefined, { ...UTC, hour: '2-digit', minute: '2-digit' })
  }
  if (bucket === 'month') {
    return d.toLocaleDateString(undefined, { ...UTC, month: 'short', year: '2-digit' })
  }
  return d.toLocaleDateString(undefined, { ...UTC, month: 'short', day: 'numeric' })
}

function bucketTitle(start: string, bucket: Bucket): string {
  const d = new Date(start)
  switch (bucket) {
    case 'hour':
      return `${d.toLocaleDateString(undefined, { ...UTC, month: 'short', day: 'numeric' })}, ${d.toLocaleTimeString(undefined, { ...UTC, hour: '2-digit', minute: '2-digit' })} UTC`
    case 'week':
      return `Week of ${d.toLocaleDateString(undefined, { ...UTC, month: 'short', day: 'numeric', year: 'numeric' })}`
    case 'month':
      return d.toLocaleDateString(undefined, { ...UTC, month: 'long', year: 'numeric' })
    default:
      return d.toLocaleDateString(undefined, {
        ...UTC,
        month: 'short',
        day: 'numeric',
        year: 'numeric',
      })
  }
}

function sum(buckets: UsageBucket[], pick: (b: UsageBucket) => number): number {
  return buckets.reduce((total, b) => total + pick(b), 0)
}

// delta is the change against the previous window of the same length; null
// when there is nothing to compare against.
function delta(current: number, previous: number, comparable: boolean): number | null {
  if (!comparable || previous === 0) return null
  return (current - previous) / previous
}

function tokensOf(row: { units: Record<string, number> }): number {
  return (row.units['input_tokens'] ?? 0) + (row.units['output_tokens'] ?? 0)
}

// rollup merges stats rows sharing a key: counts and units add, cost stays
// null until a priced row contributes (unpriced is a state, never zero).
function rollup<T extends { requests: number; cancelled: number; cost: number | null; units: Record<string, number> }>(
  rows: StatsRow[],
  keyOf: (r: StatsRow) => string,
  make: (r: StatsRow) => T,
): Map<string, T> {
  const out = new Map<string, T>()
  for (const row of rows) {
    const key = keyOf(row)
    let acc = out.get(key)
    if (!acc) {
      acc = make(row)
      out.set(key, acc)
    }
    acc.requests += row.requests
    acc.cancelled += row.cancelled
    if (row.cost !== null) acc.cost = (acc.cost ?? 0) + row.cost
    for (const [unit, quantity] of Object.entries(row.units)) {
      acc.units[unit] = (acc.units[unit] ?? 0) + quantity
    }
  }
  return out
}

// mergeUsage folds rollup entries into one aggregate, with the same
// null-until-priced cost rule.
interface UsageAgg {
  requests: number
  cost: number | null
  units: Record<string, number>
}

function mergeUsage(items: UsageAgg[]): UsageAgg {
  const out: UsageAgg = { requests: 0, cost: null, units: {} }
  for (const item of items) {
    out.requests += item.requests
    if (item.cost !== null) out.cost = (out.cost ?? 0) + item.cost
    for (const [unit, quantity] of Object.entries(item.units)) {
      out.units[unit] = (out.units[unit] ?? 0) + quantity
    }
  }
  return out
}

// userDonut folds a per-user rollup into donut slices sized by one metric;
// the rows builder carries the other metrics into the tooltip.
function userDonut(
  users: (UsageAgg & { principal?: string })[],
  value: (u: UsageAgg) => number,
  rows: (u: UsageAgg) => { label: string; value: string }[],
): { slices: DonutSlice[]; overflow: DonutSlice[] } {
  const slice = (u: UsageAgg & { principal?: string }, labelOverride?: string): DonutSlice => ({
    label: labelOverride ?? u.principal ?? '',
    value: value(u),
    rows: rows(u),
  })
  return foldDonut(
    users,
    (u) => slice(u),
    (tail) => slice(mergeUsage(tail), `Other (${tail.length} users)`),
  )
}

const ALL = 'all'

function FilterSelect({
  label,
  value,
  onChange,
  options,
  allLabel,
}: {
  label: string
  value: string
  onChange: (v: string) => void
  options: string[]
  allLabel: string
}) {
  return (
    <div className="relative">
      <Select value={value || ALL} onValueChange={(v) => onChange(v === ALL ? '' : v)}>
        <SelectTrigger className="w-44" aria-label={label}>
          {/* The value pads itself when the clear button is shown, so the
              chevron keeps its natural spot at the far right. */}
          <SelectValue className={value ? 'pr-6' : undefined} />
        </SelectTrigger>
        <SelectContent>
          <SelectItem value={ALL}>{allLabel}</SelectItem>
          {options.map((name) => (
            <SelectItem key={name} value={name}>
              {name}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
      {value && (
        <Button
          variant="ghost"
          size="icon-sm"
          aria-label={`Clear ${label.toLowerCase()} filter`}
          title={`Clear ${label.toLowerCase()} filter`}
          className="absolute top-1/2 right-8 size-6 -translate-y-1/2"
          onClick={() => onChange('')}
        >
          <X className="size-3.5" />
        </Button>
      )}
    </div>
  )
}

export function UsageDashboard({ ssoEnabled }: { ssoEnabled: boolean }) {
  const [rangeKey, setRangeKey] = useState('30d')
  const [principal, setPrincipal] = useState('')
  const [provider, setProvider] = useState('')
  const [model, setModel] = useState('')
  const [client, setClient] = useState('') // a client family; server filter is a prefix match
  const [modelTable, setModelTable] = useState(false) // Models card: donut or full table
  const [clientTable, setClientTable] = useState(false) // Clients card: bars or full table
  const [clientByVersion, setClientByVersion] = useState(false) // group per version instead of per tool
  const range = RANGES.find((r) => r.key === rangeKey) ?? RANGES[2]

  const now = new Date()
  const windowEnd = floorBucket(now, range.bucket)
  const windowStart = range.count
    ? addBuckets(windowEnd, range.bucket, -(range.count - 1))
    : null
  // Fetch the previous window too, so the tiles can show a change.
  const fetchFrom = windowStart ? addBuckets(windowStart, range.bucket, -range.count) : null

  const filterQuery =
    (principal ? `&principal=${encodeURIComponent(principal)}` : '') +
    (provider ? `&provider=${encodeURIComponent(provider)}` : '') +
    (model ? `&model=${encodeURIComponent(model)}` : '') +
    (client ? `&client=${encodeURIComponent(client)}` : '')
  const series = useAsync(
    () =>
      api.get<{ bucket: Bucket; series: UsageBucket[] }>(
        `/stats/series?bucket=${range.bucket}` +
          (fetchFrom ? `&since=${fetchFrom.toISOString()}` : '') +
          filterQuery,
      ),
    [rangeKey, principal, provider, model, client],
  )
  // Unfiltered on purpose: the distinct values feed the filter dropdowns, and
  // slicing happens client-side so one request serves every breakdown.
  const summary = useAsync(
    () =>
      api.get<{ usage: StatsRow[] }>(
        '/stats/summary' + (windowStart ? `?since=${windowStart.toISOString()}` : ''),
      ),
    [rangeKey],
  )

  const all = series.data?.series ?? []
  const startMs = windowStart ? windowStart.getTime() : -Infinity
  const buckets = all.filter((b) => new Date(b.start).getTime() >= startMs)
  const previous = all.filter((b) => new Date(b.start).getTime() < startMs)
  const comparable = previous.length > 0

  const input = (b: UsageBucket) => b.units['input_tokens'] ?? 0
  const output = (b: UsageBucket) => b.units['output_tokens'] ?? 0
  const cached = (b: UsageBucket) => b.units['cached_input_tokens'] ?? 0

  const requests = sum(buckets, (b) => b.requests)
  const inputTotal = sum(buckets, input)
  const cachedTotal = sum(buckets, cached)
  const outputTotal = sum(buckets, output)
  const costTotal = sum(buckets, (b) => b.cost ?? 0)
  const priced = buckets.some((b) => b.cost !== null)
  const unpricedRequests = sum(buckets, (b) => b.unpriced_requests)

  // The newest bucket is still filling; say so rather than let it read as a
  // collapse in traffic.
  const inProgress = (b: UsageBucket) =>
    new Date(b.start).getTime() === windowEnd.getTime()
  const label = (b: UsageBucket) => bucketLabel(b.start, range.bucket)
  const title = (b: UsageBucket) =>
    bucketTitle(b.start, range.bucket) + (inProgress(b) ? ' · in progress' : '')

  // Cancelled requests reached the upstream and are billed, so they stack with
  // the successful ones; only real failures get their own segment. Failures
  // carry no usage, so they stay out of the average-size denominator.
  const requestPoints: ChartPoint[] = buckets.map((b) => {
    const done = b.ok + b.cancelled
    const rows = [
      ...(b.cancelled ? [{ label: 'cancelled', value: formatNumber(b.cancelled) }] : []),
      ...(done > 0
        ? [
            { label: 'avg input / request', value: formatCompact(Math.round(input(b) / done)) },
            { label: 'avg output / response', value: formatCompact(Math.round(output(b) / done)) },
          ]
        : []),
    ]
    return {
      label: label(b),
      title: title(b),
      values: [done, b.failed],
      rows: rows.length ? rows : undefined,
    }
  })
  const tokenPoints: ChartPoint[] = buckets.map((b) => ({
    label: label(b),
    title: title(b),
    values: [input(b), output(b)],
    rows: cached(b) ? [{ label: 'cached input', value: formatCompact(cached(b)) }] : undefined,
  }))
  const costPoints: ChartPoint[] = buckets.map((b) => ({
    label: label(b),
    title: title(b),
    values: [b.cost],
    rows: b.unpriced_requests
      ? [{ label: 'unpriced requests', value: formatNumber(b.unpriced_requests) }]
      : undefined,
  }))

  // ---------- summary-derived breakdowns and filter options ----------

  const rows = useMemo(() => summary.data?.usage ?? [], [summary.data])
  const options = useMemo(
    () => ({
      principals: [...new Set(rows.map((r) => r.principal))].sort(),
      providers: [...new Set(rows.map((r) => r.provider))].sort(),
      models: [...new Set(rows.filter((r) => r.model).map((r) => r.model))].sort(),
      clients: [...new Set(rows.filter((r) => r.client).map((r) => clientFamily(r.client)))].sort(),
    }),
    [rows],
  )
  const filtered = useMemo(
    () =>
      rows.filter(
        (r) =>
          (!principal || r.principal === principal) &&
          (!provider || r.provider === provider) &&
          (!model || r.model === model) &&
          (!client || clientFamily(r.client) === client),
      ),
    [rows, principal, provider, model, client],
  )

  // Per-model rollup by total tokens: the metric that exists for every
  // request, priced or not. Feeds the donut (top slices plus "Other") and the
  // expanded model table.
  const byModel = useMemo(
    () =>
      [...rollup(filtered, (r) => r.model, (r) => ({
        model: r.model, requests: 0, cancelled: 0, cost: null as number | null, units: {} as Record<string, number>,
      })).values()].sort((a, b) => tokensOf(b) - tokensOf(a)),
    [filtered],
  )
  const modelTokensTotal = byModel.reduce((sum, m) => sum + tokensOf(m), 0)

  // The donut shows the top models plus an "Other" slice; the folded tail
  // rides along so the component can unfold it into the list on demand.
  const { slices: donutSlices, overflow: donutOverflow } = useMemo(() => {
    const slice = (m: UsageAgg & { model?: string }, labelOverride?: string): DonutSlice => ({
      label: labelOverride ?? (m.model || '(none)'),
      value: tokensOf(m),
      rows: [
        { label: 'requests', value: formatNumber(m.requests) },
        ...(m.cost !== null ? [{ label: 'cost', value: formatMoney(m.cost) }] : []),
      ],
    })
    return foldDonut(
      byModel,
      (m) => slice(m),
      (tail) => slice(mergeUsage(tail), `Other (${tail.length} models)`),
    )
  }, [byModel])

  // Per-user rollup for the two user donuts, shown only with SSO: a single
  // user has nothing to share out.
  const byUser = useMemo(
    () =>
      [...rollup(filtered, (r) => r.principal, (r) => ({
        principal: r.principal, requests: 0, cancelled: 0, cost: null as number | null, units: {} as Record<string, number>,
      })).values()],
    [filtered],
  )

  // Sized by tokens; requests and cost ride along in the tooltip.
  const userTokens = useMemo(
    () =>
      userDonut(
        [...byUser].sort((a, b) => tokensOf(b) - tokensOf(a)),
        tokensOf,
        (u) => [
          { label: 'requests', value: formatNumber(u.requests) },
          ...(u.cost !== null ? [{ label: 'cost', value: formatMoney(u.cost) }] : []),
        ],
      ),
    [byUser],
  )

  // Sized by cost; unpriced users have nothing to show here.
  const userCost = useMemo(
    () =>
      userDonut(
        byUser.filter((u) => u.cost !== null).sort((a, b) => (b.cost ?? 0) - (a.cost ?? 0)),
        (u) => u.cost ?? 0,
        (u) => [
          { label: 'tokens', value: formatCompact(tokensOf(u)) },
          { label: 'requests', value: formatNumber(u.requests) },
        ],
      ),
    [byUser],
  )

  // Per-client rollup: "which tools do we use", grouped per tool by default
  // or per exact version when toggled. Feeds the share bars and the expanded
  // client table; exact User-Agent strings stay on hover.
  const byClient = useMemo(() => {
    const groups = new Map<
      string,
      {
        label: string
        requests: number
        cancelled: number
        cost: number | null
        units: Record<string, number>
        exact: Set<string>
      }
    >()
    for (const r of filtered) {
      const label = !r.client
        ? 'unknown'
        : clientByVersion
          ? clientProduct(r.client)
          : clientFamily(r.client)
      let acc = groups.get(label)
      if (!acc) {
        acc = { label, requests: 0, cancelled: 0, cost: null, units: {}, exact: new Set() }
        groups.set(label, acc)
      }
      acc.requests += r.requests
      acc.cancelled += r.cancelled
      if (r.cost !== null) acc.cost = (acc.cost ?? 0) + r.cost
      for (const [unit, quantity] of Object.entries(r.units)) {
        acc.units[unit] = (acc.units[unit] ?? 0) + quantity
      }
      if (r.client) acc.exact.add(r.client)
    }
    return [...groups.values()].sort((a, b) => b.requests - a.requests)
  }, [filtered, clientByVersion])
  const clientRequestsTotal = byClient.reduce((sum, c) => sum + c.requests, 0)

  const clientRows: ShareRow[] = useMemo(
    () =>
      byClient.map((c) => ({
        label: c.label,
        title: c.exact.size ? [...c.exact].sort().join('\n') : 'no User-Agent header',
        value: c.requests,
      })),
    [byClient],
  )

  // The by-user table keeps its historical shape: (user, model, endpoint).
  const tableRows: UsageRow[] = useMemo(() => {
    const merged = rollup(filtered, (r) => `${r.principal} ${r.model} ${r.endpoint}`, (r) => ({
      principal: r.principal, model: r.model, endpoint: r.endpoint,
      requests: 0, cancelled: 0, cost: null as number | null, units: {} as Record<string, number>,
    }))
    return [...merged.values()].sort((a, b) =>
      a.principal !== b.principal
        ? a.principal.localeCompare(b.principal)
        : a.model !== b.model
          ? a.model.localeCompare(b.model)
          : a.endpoint.localeCompare(b.endpoint),
    )
  }, [filtered])

  const loading = (series.loading && !series.data) || (summary.loading && !summary.data)
  const stale = !loading && (series.loading || summary.loading)

  return (
    <div className="flex flex-col gap-6">
      <div className="flex flex-wrap items-center gap-2">
        <Select value={rangeKey} onValueChange={setRangeKey}>
          <SelectTrigger className="w-44" aria-label="Time range">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {RANGES.map((r) => (
              <SelectItem key={r.key} value={r.key}>
                {r.label}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <FilterSelect
          label="User"
          value={principal}
          onChange={setPrincipal}
          options={options.principals}
          allLabel="All users"
        />
        <FilterSelect
          label="Provider"
          value={provider}
          onChange={setProvider}
          options={options.providers}
          allLabel="All providers"
        />
        <FilterSelect
          label="Model"
          value={model}
          onChange={setModel}
          options={options.models}
          allLabel="All models"
        />
        <FilterSelect
          label="Client"
          value={client}
          onChange={setClient}
          options={options.clients}
          allLabel="All clients"
        />
        {(principal || provider || model || client) && (
          <Button
            variant="ghost"
            size="sm"
            onClick={() => {
              setPrincipal('')
              setProvider('')
              setModel('')
              setClient('')
            }}
          >
            <FilterX />
            Clear filters
          </Button>
        )}
        <Button
          variant="outline"
          size="icon-sm"
          aria-label="Refresh"
          onClick={() => {
            series.reload()
            summary.reload()
          }}
        >
          <RefreshCw />
        </Button>
      </div>

      {loading ? (
        <Spinner />
      ) : series.error || summary.error ? (
        <p className="text-sm text-destructive">{series.error ?? summary.error}</p>
      ) : (
        <div className={stale ? 'flex flex-col gap-6 opacity-60' : 'flex flex-col gap-6'}>
          <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
            <StatTile
              label="Requests"
              value={formatCompact(requests)}
              delta={delta(requests, sum(previous, (b) => b.requests), comparable)}
            />
            <StatTile
              label="Input tokens"
              value={formatCompact(inputTotal)}
              secondary={cachedTotal > 0 ? `${formatCompact(cachedTotal)} cached` : undefined}
              delta={delta(inputTotal, sum(previous, input), comparable)}
            />
            <StatTile
              label="Output tokens"
              value={formatCompact(outputTotal)}
              delta={delta(outputTotal, sum(previous, output), comparable)}
            />
            <StatTile
              label="Cost"
              value={priced ? formatMoney(costTotal) : 'unpriced'}
              delta={
                priced
                  ? delta(costTotal, sum(previous, (b) => b.cost ?? 0), comparable)
                  : null
              }
              hint={
                unpricedRequests
                  ? `${formatNumber(unpricedRequests)} request${unpricedRequests === 1 ? '' : 's'} unpriced`
                  : undefined
              }
            />
          </div>

          <div className="grid gap-6 lg:grid-cols-2">
            {/* The table needs the full row; the donut shares it. */}
            <Card className={modelTable ? 'lg:col-span-2' : undefined}>
              <CardHeader className="flex flex-row items-start justify-between">
                <div className="flex flex-col gap-1.5">
                  <CardTitle className="font-serif">Models</CardTitle>
                  <CardDescription>
                    Share of total tokens, cached input included.
                  </CardDescription>
                </div>
                <Button
                  variant="outline"
                  size="icon-sm"
                  aria-label={modelTable ? 'Show chart' : 'Show all models as a table'}
                  title={modelTable ? 'Show chart' : 'Show all models as a table'}
                  onClick={() => setModelTable((v) => !v)}
                >
                  {modelTable ? <ChartPie /> : <TableProperties />}
                </Button>
              </CardHeader>
              <CardContent>
                {modelTable ? (
                  <Table>
                    <TableHeader>
                      <TableRow>
                        <TableHead>Model</TableHead>
                        <TableHead className="text-right">Share</TableHead>
                        <TableHead className="text-right">Requests</TableHead>
                        <TableHead className="text-right">Input</TableHead>
                        <TableHead className="text-right">Cached</TableHead>
                        <TableHead className="text-right">Output</TableHead>
                        <TableHead className="text-right">Cost</TableHead>
                      </TableRow>
                    </TableHeader>
                    <TableBody>
                      {byModel.length === 0 && (
                        <TableRow>
                          <TableCell colSpan={7} className="text-muted-foreground">
                            No usage in this range.
                          </TableCell>
                        </TableRow>
                      )}
                      {byModel.map((m, i) => (
                        <TableRow key={i}>
                          <TableCell className="font-mono text-xs">
                            {m.model || <span className="text-muted-foreground">(none)</span>}
                          </TableCell>
                          <TableCell className="text-right tabular-nums">
                            {modelTokensTotal > 0
                              ? `${((tokensOf(m) / modelTokensTotal) * 100).toFixed(1)}%`
                              : ''}
                          </TableCell>
                          <TableCell className="text-right tabular-nums">
                            {formatCompact(m.requests)}
                          </TableCell>
                          <TableCell
                            className="text-right tabular-nums"
                            title={formatTokens(m.units['input_tokens'])}
                          >
                            {m.units['input_tokens'] ? formatCompact(m.units['input_tokens']) : ''}
                          </TableCell>
                          <TableCell
                            className="text-right tabular-nums"
                            title={formatTokens(m.units['cached_input_tokens'])}
                          >
                            {m.units['cached_input_tokens']
                              ? formatCompact(m.units['cached_input_tokens'])
                              : ''}
                          </TableCell>
                          <TableCell
                            className="text-right tabular-nums"
                            title={formatTokens(m.units['output_tokens'])}
                          >
                            {m.units['output_tokens'] ? formatCompact(m.units['output_tokens']) : ''}
                          </TableCell>
                          <TableCell className="text-right tabular-nums">
                            {m.cost === null ? (
                              <span className="text-muted-foreground">unpriced</span>
                            ) : (
                              formatMoney(m.cost)
                            )}
                          </TableCell>
                        </TableRow>
                      ))}
                    </TableBody>
                  </Table>
                ) : (
                  <Donut
                    // Remount on filter changes so the unfolded state resets
                    // with the data.
                    key={`${rangeKey} ${principal} ${provider} ${model} ${client}`}
                    slices={donutSlices}
                    overflow={donutOverflow}
                    format={formatCompact}
                  />
                )}
              </CardContent>
            </Card>

            <Card className={clientTable ? 'lg:col-span-2' : undefined}>
              <CardHeader className="flex flex-row items-start justify-between">
                <div className="flex flex-col gap-1.5">
                  <CardTitle className="font-serif">Clients</CardTitle>
                  <CardDescription>
                    {clientByVersion
                      ? 'Requests per client version, from the User-Agent.'
                      : 'Requests per client, from the User-Agent.'}
                  </CardDescription>
                </div>
                <div className="flex gap-1">
                  <Button
                    variant={clientByVersion ? 'secondary' : 'outline'}
                    size="icon-sm"
                    aria-label={clientByVersion ? 'Group by tool' : 'Split by version'}
                    aria-pressed={clientByVersion}
                    title={clientByVersion ? 'Group by tool' : 'Split by version'}
                    onClick={() => setClientByVersion((v) => !v)}
                  >
                    <Tags />
                  </Button>
                  <Button
                    variant="outline"
                    size="icon-sm"
                    aria-label={clientTable ? 'Show chart' : 'Show all clients as a table'}
                    title={clientTable ? 'Show chart' : 'Show all clients as a table'}
                    onClick={() => setClientTable((v) => !v)}
                  >
                    {clientTable ? <ChartBar /> : <TableProperties />}
                  </Button>
                </div>
              </CardHeader>
              <CardContent>
                {clientTable ? (
                  <Table>
                    <TableHeader>
                      <TableRow>
                        <TableHead>Client</TableHead>
                        <TableHead className="text-right">Share</TableHead>
                        <TableHead className="text-right">Requests</TableHead>
                        <TableHead className="text-right">Input</TableHead>
                        <TableHead className="text-right">Cached</TableHead>
                        <TableHead className="text-right">Output</TableHead>
                        <TableHead className="text-right">Cost</TableHead>
                      </TableRow>
                    </TableHeader>
                    <TableBody>
                      {byClient.length === 0 && (
                        <TableRow>
                          <TableCell colSpan={7} className="text-muted-foreground">
                            No usage in this range.
                          </TableCell>
                        </TableRow>
                      )}
                      {byClient.map((c, i) => (
                        <TableRow key={i}>
                          <TableCell
                            className="font-mono text-xs"
                            title={c.exact.size ? [...c.exact].sort().join('\n') : undefined}
                          >
                            {c.label}
                          </TableCell>
                          <TableCell className="text-right tabular-nums">
                            {clientRequestsTotal > 0
                              ? `${((c.requests / clientRequestsTotal) * 100).toFixed(1)}%`
                              : ''}
                          </TableCell>
                          <TableCell className="text-right tabular-nums">
                            {formatCompact(c.requests)}
                          </TableCell>
                          <TableCell
                            className="text-right tabular-nums"
                            title={formatTokens(c.units['input_tokens'])}
                          >
                            {c.units['input_tokens'] ? formatCompact(c.units['input_tokens']) : ''}
                          </TableCell>
                          <TableCell
                            className="text-right tabular-nums"
                            title={formatTokens(c.units['cached_input_tokens'])}
                          >
                            {c.units['cached_input_tokens']
                              ? formatCompact(c.units['cached_input_tokens'])
                              : ''}
                          </TableCell>
                          <TableCell
                            className="text-right tabular-nums"
                            title={formatTokens(c.units['output_tokens'])}
                          >
                            {c.units['output_tokens'] ? formatCompact(c.units['output_tokens']) : ''}
                          </TableCell>
                          <TableCell className="text-right tabular-nums">
                            {c.cost === null ? (
                              <span className="text-muted-foreground">unpriced</span>
                            ) : (
                              formatMoney(c.cost)
                            )}
                          </TableCell>
                        </TableRow>
                      ))}
                    </TableBody>
                  </Table>
                ) : (
                  <ShareBars rows={clientRows} format={formatCompact} />
                )}
              </CardContent>
            </Card>

            {/* Per-user donuts only make sense with SSO; without it there is
                a single user and nothing to share out. */}
            {ssoEnabled && (
              <>
                <Card>
                  <CardHeader>
                    <CardTitle className="font-serif">User tokens</CardTitle>
                    <CardDescription>Share of total tokens per user.</CardDescription>
                  </CardHeader>
                  <CardContent>
                    <Donut
                      // Remount on filter changes so the unfolded state
                      // resets with the data.
                      key={`${rangeKey} ${principal} ${provider} ${model} ${client}`}
                      slices={userTokens.slices}
                      overflow={userTokens.overflow}
                      format={formatCompact}
                    />
                  </CardContent>
                </Card>

                <Card>
                  <CardHeader>
                    <CardTitle className="font-serif">User cost</CardTitle>
                    <CardDescription>Share of total cost per user.</CardDescription>
                  </CardHeader>
                  <CardContent>
                    <Donut
                      key={`${rangeKey} ${principal} ${provider} ${model} ${client}`}
                      slices={userCost.slices}
                      overflow={userCost.overflow}
                      format={formatMoney}
                      emptyMessage="Nothing priced in this range."
                    />
                  </CardContent>
                </Card>
              </>
            )}
          </div>

          <Card>
            <CardHeader>
              <CardTitle className="font-serif">Tokens</CardTitle>
              <CardDescription>
                Input and output per {range.bucket}; cached input included.
              </CardDescription>
            </CardHeader>
            <CardContent>
              <ColumnChart
                points={tokenPoints}
                series={[
                  { ...SERIES_ACCENT, name: 'input' },
                  { ...SERIES_GRAY, name: 'output' },
                ]}
                format={formatCompact}
              />
            </CardContent>
          </Card>

          <Card>
            <CardHeader>
              <CardTitle className="font-serif">Requests</CardTitle>
              <CardDescription>
                Per {range.bucket}, split by outcome.
              </CardDescription>
            </CardHeader>
            <CardContent>
              <ColumnChart
                points={requestPoints}
                series={[
                  { ...SERIES_ACCENT, name: 'ok' },
                  { ...SERIES_GRAY, name: 'failed' },
                ]}
                format={formatCompact}
                integer
              />
            </CardContent>
          </Card>

          <Card>
            <CardHeader>
              <CardTitle className="font-serif">Cost</CardTitle>
              <CardDescription>
                Per {range.bucket}, from the model prices; unpriced stays
                empty, never zero.
              </CardDescription>
            </CardHeader>
            <CardContent>
              <ColumnChart
                points={costPoints}
                series={[{ ...SERIES_ACCENT, name: 'cost' }]}
                format={(n) => formatMoney(n)}
                emptyMessage="Nothing priced in this range."
              />
            </CardContent>
          </Card>

          <Card>
            <CardHeader>
              <CardTitle className="font-serif">
                {principal ? 'By model' : 'By user and model'}
              </CardTitle>
              <CardDescription>
                Totals by {principal ? '' : 'user, '}model and endpoint.
              </CardDescription>
            </CardHeader>
            <CardContent>
              <UsageTable rows={tableRows} withPrincipal={!principal} />
            </CardContent>
          </Card>
        </div>
      )}
    </div>
  )
}
