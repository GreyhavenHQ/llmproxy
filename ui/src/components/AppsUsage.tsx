// Per-application usage: what each app spends, sends and burns. Apps
// identify themselves with the x-llmproxy-tags request header
// ("app:dataindex,context:search"); the proxy stores the normalised pairs on
// the usage event and never sees anything else about them.
//
// Data flow mirrors the usage overview: the time series is filtered
// server-side by tag, while the summary is fetched once per range and sliced
// client-side, so one request feeds the bars, the tables and the dropdowns.

import { useMemo, useState } from 'react'
import {
  api,
  formatCompact,
  formatMoney,
  formatNumber,
  formatTokens,
  tagValue,
  type StatsRow,
  type UsageBucket,
} from '@/lib/api'
import {
  addBuckets,
  bucketLabel,
  bucketTitle,
  floorBucket,
  RANGES,
  type Bucket,
} from '@/lib/timerange'
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
import { FilterSelect } from '@/components/FilterSelect'
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
import { ChartBar, FilterX, RefreshCw, TableProperties } from 'lucide-react'

// Events that carry no app tag are still someone's traffic, so they get a
// bucket of their own and the totals here match the overview tab.
const UNTAGGED = 'untagged'

interface Agg {
  requests: number
  cost: number | null
  units: Record<string, number>
}

function emptyAgg(): Agg {
  return { requests: 0, cost: null, units: {} }
}

// add folds one summary row into an aggregate: counts and units sum, cost
// stays null until a priced row contributes (unpriced is a state, never zero).
function add(acc: Agg, row: StatsRow) {
  acc.requests += row.requests
  if (row.cost !== null) acc.cost = (acc.cost ?? 0) + row.cost
  for (const [unit, quantity] of Object.entries(row.units)) {
    acc.units[unit] = (acc.units[unit] ?? 0) + quantity
  }
}

function groupBy<T extends Agg>(rows: StatsRow[], keyOf: (r: StatsRow) => string, make: (key: string) => T): T[] {
  const out = new Map<string, T>()
  for (const row of rows) {
    const key = keyOf(row)
    let acc = out.get(key)
    if (!acc) {
      acc = make(key)
      out.set(key, acc)
    }
    add(acc, row)
  }
  return [...out.values()]
}

function tokensOf(a: { units: Record<string, number> }): number {
  return (a.units['input_tokens'] ?? 0) + (a.units['output_tokens'] ?? 0)
}

function sum(buckets: UsageBucket[], pick: (b: UsageBucket) => number): number {
  return buckets.reduce((total, b) => total + pick(b), 0)
}

function delta(current: number, previous: number, comparable: boolean): number | null {
  if (!comparable || previous === 0) return null
  return (current - previous) / previous
}

function appOf(row: StatsRow): string {
  return tagValue(row.tags, 'app') || UNTAGGED
}

function contextOf(row: StatsRow): string {
  return tagValue(row.tags, 'context') || UNTAGGED
}

export function AppsUsage() {
  const [rangeKey, setRangeKey] = useState('30d')
  const [app, setApp] = useState('')
  const [context, setContext] = useState('')
  const [appTable, setAppTable] = useState(false) // Apps card: bars or full table
  const range = RANGES.find((r) => r.key === rangeKey) ?? RANGES[2]

  const now = new Date()
  const windowEnd = floorBucket(now, range.bucket)
  const windowStart = range.count ? addBuckets(windowEnd, range.bucket, -(range.count - 1)) : null
  // Fetch the previous window too, so the tiles can show a change.
  const fetchFrom = windowStart ? addBuckets(windowStart, range.bucket, -range.count) : null

  const tagQuery =
    (app ? `&tag=${encodeURIComponent(`app:${app}`)}` : '') +
    (context ? `&tag=${encodeURIComponent(`context:${context}`)}` : '')
  const series = useAsync(
    () =>
      api.get<{ bucket: Bucket; series: UsageBucket[] }>(
        `/stats/series?bucket=${range.bucket}` +
          (fetchFrom ? `&since=${fetchFrom.toISOString()}` : '') +
          tagQuery,
      ),
    [rangeKey, app, context],
  )
  // Unfiltered on purpose: the distinct tags feed the dropdowns, and slicing
  // happens client-side so one request serves every breakdown.
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
  const tokens = (b: UsageBucket) => input(b) + output(b)

  const requests = sum(buckets, (b) => b.requests)
  const tokensTotal = sum(buckets, tokens)
  const costTotal = sum(buckets, (b) => b.cost ?? 0)
  const priced = buckets.some((b) => b.cost !== null)
  const unpricedRequests = sum(buckets, (b) => b.unpriced_requests)

  const inProgress = (b: UsageBucket) => new Date(b.start).getTime() === windowEnd.getTime()
  const label = (b: UsageBucket) => bucketLabel(b.start, range.bucket)
  const title = (b: UsageBucket) =>
    bucketTitle(b.start, range.bucket) + (inProgress(b) ? ' · in progress' : '')

  const costPoints: ChartPoint[] = buckets.map((b) => ({
    label: label(b),
    title: title(b),
    values: [b.cost],
    rows: b.unpriced_requests
      ? [{ label: 'unpriced requests', value: formatNumber(b.unpriced_requests) }]
      : undefined,
  }))
  const requestPoints: ChartPoint[] = buckets.map((b) => ({
    label: label(b),
    title: title(b),
    values: [b.ok + b.cancelled, b.failed],
    rows: b.cancelled ? [{ label: 'cancelled', value: formatNumber(b.cancelled) }] : undefined,
  }))
  const tokenPoints: ChartPoint[] = buckets.map((b) => ({
    label: label(b),
    title: title(b),
    values: [input(b), output(b)],
  }))

  // ---------- summary-derived breakdowns and filter options ----------

  const rows = useMemo(() => summary.data?.usage ?? [], [summary.data])
  // Only real tag values are offered: "untagged" is a bucket in the
  // breakdowns, not something the server can filter on.
  const appOptions = useMemo(
    () => [...new Set(rows.map((r) => tagValue(r.tags, 'app')).filter(Boolean))].sort(),
    [rows],
  )
  const contextOptions = useMemo(
    () =>
      [
        ...new Set(
          rows
            .filter((r) => !app || tagValue(r.tags, 'app') === app)
            .map((r) => tagValue(r.tags, 'context'))
            .filter(Boolean),
        ),
      ].sort(),
    [rows, app],
  )
  const filtered = useMemo(
    () =>
      rows.filter(
        (r) =>
          (!app || tagValue(r.tags, 'app') === app) &&
          (!context || tagValue(r.tags, 'context') === context),
      ),
    [rows, app, context],
  )

  const byApp = useMemo(
    () =>
      groupBy(filtered, appOf, (name) => ({ name, ...emptyAgg() })).sort(
        (a, b) => b.requests - a.requests,
      ),
    [filtered],
  )
  const appRequestsTotal = byApp.reduce((total, a) => total + a.requests, 0)

  const appRequestRows: ShareRow[] = useMemo(
    () => byApp.map((a) => ({ label: a.name, value: a.requests })),
    [byApp],
  )

  // Spend share as a donut; apps with nothing priced have nothing to show.
  const appSpend = useMemo(() => {
    const slice = (a: { name?: string } & Agg, labelOverride?: string): DonutSlice => ({
      label: labelOverride ?? a.name ?? '',
      value: a.cost ?? 0,
      rows: [
        { label: 'requests', value: formatNumber(a.requests) },
        { label: 'tokens', value: formatCompact(tokensOf(a)) },
      ],
    })
    const pricedApps = byApp
      .filter((a) => a.cost !== null)
      .sort((a, b) => (b.cost ?? 0) - (a.cost ?? 0))
    return foldDonut(
      pricedApps,
      (a) => slice(a),
      (tail) => {
        const merged = emptyAgg()
        for (const a of tail) {
          merged.requests += a.requests
          if (a.cost !== null) merged.cost = (merged.cost ?? 0) + a.cost
          for (const [unit, quantity] of Object.entries(a.units)) {
            merged.units[unit] = (merged.units[unit] ?? 0) + quantity
          }
        }
        return slice(merged, `Other (${tail.length} apps)`)
      },
    )
  }, [byApp])

  const byContext = useMemo(
    () =>
      groupBy(filtered, contextOf, (name) => ({ name, ...emptyAgg() })).sort(
        (a, b) => b.requests - a.requests,
      ),
    [filtered],
  )
  const contextRows: ShareRow[] = useMemo(
    () => byContext.map((c) => ({ label: c.name, value: c.requests })),
    [byContext],
  )

  const byAppModel = useMemo(
    () =>
      groupBy(
        filtered,
        (r) => `${appOf(r)} ${r.model}`,
        (key) => {
          const at = key.indexOf(' ')
          return { name: key.slice(0, at), model: key.slice(at + 1), ...emptyAgg() }
        },
      ).sort((a, b) =>
        a.name !== b.name ? a.name.localeCompare(b.name) : a.model.localeCompare(b.model),
      ),
    [filtered],
  )

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
          label="App"
          value={app}
          onChange={(v) => {
            setApp(v)
            setContext('')
          }}
          options={appOptions}
          allLabel="All apps"
        />
        <FilterSelect
          label="Context"
          value={context}
          onChange={setContext}
          options={contextOptions}
          allLabel="All contexts"
        />
        {(app || context) && (
          <Button
            variant="ghost"
            size="sm"
            onClick={() => {
              setApp('')
              setContext('')
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
          <div className="grid gap-4 sm:grid-cols-3">
            <StatTile
              label="Spend"
              value={priced ? formatMoney(costTotal) : 'unpriced'}
              delta={
                priced ? delta(costTotal, sum(previous, (b) => b.cost ?? 0), comparable) : null
              }
              hint={
                unpricedRequests
                  ? `${formatNumber(unpricedRequests)} request${unpricedRequests === 1 ? '' : 's'} unpriced`
                  : undefined
              }
            />
            <StatTile
              label="Requests"
              value={formatCompact(requests)}
              delta={delta(requests, sum(previous, (b) => b.requests), comparable)}
            />
            <StatTile
              label="Tokens"
              value={formatCompact(tokensTotal)}
              delta={delta(tokensTotal, sum(previous, tokens), comparable)}
            />
          </div>

          <div className="grid gap-6 lg:grid-cols-2">
            <Card className={appTable ? 'lg:col-span-2' : undefined}>
              <CardHeader className="flex flex-row items-start justify-between">
                <div className="flex flex-col gap-1.5">
                  <CardTitle className="font-serif">Apps</CardTitle>
                  <CardDescription>Requests per app, from the app tag.</CardDescription>
                </div>
                <Button
                  variant="outline"
                  size="icon-sm"
                  aria-label={appTable ? 'Show chart' : 'Show all apps as a table'}
                  title={appTable ? 'Show chart' : 'Show all apps as a table'}
                  onClick={() => setAppTable((v) => !v)}
                >
                  {appTable ? <ChartBar /> : <TableProperties />}
                </Button>
              </CardHeader>
              <CardContent>
                {appTable ? (
                  <Table>
                    <TableHeader>
                      <TableRow>
                        <TableHead>App</TableHead>
                        <TableHead className="text-right">Share</TableHead>
                        <TableHead className="text-right">Requests</TableHead>
                        <TableHead className="text-right">Input</TableHead>
                        <TableHead className="text-right">Output</TableHead>
                        <TableHead className="text-right">Spend</TableHead>
                      </TableRow>
                    </TableHeader>
                    <TableBody>
                      {byApp.length === 0 && (
                        <TableRow>
                          <TableCell colSpan={6} className="text-muted-foreground">
                            No usage in this range.
                          </TableCell>
                        </TableRow>
                      )}
                      {byApp.map((a, i) => (
                        <TableRow key={i}>
                          <TableCell className="font-mono text-xs">{a.name}</TableCell>
                          <TableCell className="text-right tabular-nums">
                            {appRequestsTotal > 0
                              ? `${((a.requests / appRequestsTotal) * 100).toFixed(1)}%`
                              : ''}
                          </TableCell>
                          <TableCell className="text-right tabular-nums">
                            {formatCompact(a.requests)}
                          </TableCell>
                          <TableCell
                            className="text-right tabular-nums"
                            title={formatTokens(a.units['input_tokens'])}
                          >
                            {a.units['input_tokens'] ? formatCompact(a.units['input_tokens']) : ''}
                          </TableCell>
                          <TableCell
                            className="text-right tabular-nums"
                            title={formatTokens(a.units['output_tokens'])}
                          >
                            {a.units['output_tokens']
                              ? formatCompact(a.units['output_tokens'])
                              : ''}
                          </TableCell>
                          <TableCell className="text-right tabular-nums">
                            {a.cost === null ? (
                              <span className="text-muted-foreground">unpriced</span>
                            ) : (
                              formatMoney(a.cost)
                            )}
                          </TableCell>
                        </TableRow>
                      ))}
                    </TableBody>
                  </Table>
                ) : (
                  <ShareBars rows={appRequestRows} format={formatCompact} />
                )}
              </CardContent>
            </Card>

            <Card>
              <CardHeader>
                <CardTitle className="font-serif">App spend</CardTitle>
                <CardDescription>Share of total cost per app.</CardDescription>
              </CardHeader>
              <CardContent>
                <Donut
                  // Remount on filter changes so the unfolded state resets
                  // with the data.
                  key={`${rangeKey} ${app} ${context}`}
                  slices={appSpend.slices}
                  overflow={appSpend.overflow}
                  format={formatMoney}
                  emptyMessage="Nothing priced in this range."
                />
              </CardContent>
            </Card>

            <Card className="lg:col-span-2">
              <CardHeader>
                <CardTitle className="font-serif">Contexts</CardTitle>
                <CardDescription>
                  Requests per context{app ? ` in ${app}` : ', across every app'}.
                </CardDescription>
              </CardHeader>
              <CardContent>
                <ShareBars rows={contextRows} format={formatCompact} />
              </CardContent>
            </Card>
          </div>

          <Card>
            <CardHeader>
              <CardTitle className="font-serif">Spend</CardTitle>
              <CardDescription>
                Per {range.bucket}; unpriced stays empty, never zero.
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
              <CardTitle className="font-serif">Requests</CardTitle>
              <CardDescription>Per {range.bucket}, split by outcome.</CardDescription>
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
              <CardTitle className="font-serif">{app ? 'By model' : 'By app and model'}</CardTitle>
              <CardDescription>Totals by {app ? '' : 'app and '}model.</CardDescription>
            </CardHeader>
            <CardContent>
              <Table>
                <TableHeader>
                  <TableRow>
                    {!app && <TableHead>App</TableHead>}
                    <TableHead>Model</TableHead>
                    <TableHead className="text-right">Requests</TableHead>
                    <TableHead className="text-right">Input</TableHead>
                    <TableHead className="text-right">Output</TableHead>
                    <TableHead className="text-right">Cached</TableHead>
                    <TableHead className="text-right">Spend</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {byAppModel.length === 0 && (
                    <TableRow>
                      <TableCell colSpan={app ? 6 : 7} className="text-muted-foreground">
                        No usage in this range.
                      </TableCell>
                    </TableRow>
                  )}
                  {byAppModel.map((r, i) => (
                    <TableRow key={i}>
                      {!app && <TableCell className="font-mono text-xs">{r.name}</TableCell>}
                      <TableCell className="font-mono text-xs">{r.model}</TableCell>
                      <TableCell className="text-right tabular-nums">
                        {formatCompact(r.requests)}
                      </TableCell>
                      <TableCell className="text-right tabular-nums">
                        {formatTokens(r.units['input_tokens'])}
                      </TableCell>
                      <TableCell className="text-right tabular-nums">
                        {formatTokens(r.units['output_tokens'])}
                      </TableCell>
                      <TableCell className="text-right tabular-nums">
                        {formatTokens(r.units['cached_input_tokens'])}
                      </TableCell>
                      <TableCell className="text-right tabular-nums">
                        {r.cost === null ? (
                          <span className="text-muted-foreground">unpriced</span>
                        ) : (
                          formatMoney(r.cost)
                        )}
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            </CardContent>
          </Card>
        </div>
      )}
    </div>
  )
}
