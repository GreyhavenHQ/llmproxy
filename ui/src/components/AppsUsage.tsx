// Per-application usage: what each app spends, sends and burns. Apps
// identify themselves with the x-llmproxy-tags request header
// ("app:dataindex,context:search"); the proxy stores the normalised pairs on
// the usage event and never sees anything else about them.
//
// Data flow mirrors the usage overview: the time series is filtered
// server-side by tag, user and model, while the summary is fetched once per
// range and sliced client-side, so one request feeds the bars, the tables and
// the dropdowns. Both requests carry app_tagged=1, so untagged traffic never
// enters this view; the totals here cover tagged apps only.

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
  SERIES_GRAY_MID,
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

// Untagged app traffic is dropped server-side (app_tagged=1), so no app bucket
// carries this label. It survives only for the context breakdown, where an
// app-tagged event may still lack a context tag.
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
  const [user, setUser] = useState('')
  const [model, setModel] = useState('')
  const [appTable, setAppTable] = useState(false) // Apps card: bars or full table
  const range = RANGES.find((r) => r.key === rangeKey) ?? RANGES[2]

  const now = new Date()
  const windowEnd = floorBucket(now, range.bucket)
  const windowStart = range.count ? addBuckets(windowEnd, range.bucket, -(range.count - 1)) : null
  // Fetch the previous window too, so the tiles can show a change.
  const fetchFrom = windowStart ? addBuckets(windowStart, range.bucket, -range.count) : null

  // app_tagged=1 drops untagged traffic; user and model narrow server-side so
  // the tiles and time series match the sliced tables below.
  const filterQuery =
    '&app_tagged=1' +
    (app ? `&tag=${encodeURIComponent(`app:${app}`)}` : '') +
    (context ? `&tag=${encodeURIComponent(`context:${context}`)}` : '') +
    (user ? `&principal=${encodeURIComponent(user)}` : '') +
    (model ? `&model=${encodeURIComponent(model)}` : '')
  const series = useAsync(
    () =>
      api.get<{ bucket: Bucket; series: UsageBucket[] }>(
        `/stats/series?bucket=${range.bucket}` +
          (fetchFrom ? `&since=${fetchFrom.toISOString()}` : '') +
          filterQuery,
      ),
    [rangeKey, app, context, user, model],
  )
  // Embeddings billed as input tokens only, kept on their own so the token
  // graph above stays about chat traffic. Same window and filters as the main
  // series, narrowed to the embeddings endpoint.
  const embed = useAsync(
    () =>
      api.get<{ bucket: Bucket; series: UsageBucket[] }>(
        `/stats/series?bucket=${range.bucket}` +
          (fetchFrom ? `&since=${fetchFrom.toISOString()}` : '') +
          filterQuery +
          '&endpoint=embeddings',
      ),
    [rangeKey, app, context, user, model],
  )
  // Only untagged traffic is dropped here; app, context, user and model stay
  // unfiltered so the distinct values feed the dropdowns and one request serves
  // every breakdown, sliced client-side.
  const summary = useAsync(
    () =>
      api.get<{ usage: StatsRow[] }>(
        '/stats/summary?app_tagged=1' +
          (windowStart ? `&since=${windowStart.toISOString()}` : ''),
      ),
    [rangeKey],
  )

  const all = series.data?.series ?? []
  const startMs = windowStart ? windowStart.getTime() : -Infinity
  const buckets = all.filter((b) => new Date(b.start).getTime() >= startMs)
  const previous = all.filter((b) => new Date(b.start).getTime() < startMs)
  const comparable = previous.length > 0

  // Embeddings share the input_tokens unit with chat, so the chat token graph
  // subtracts the embeddings the second series reports for the same bucket.
  const embedByStart = useMemo(() => {
    const m = new Map<string, UsageBucket>()
    for (const b of embed.data?.series ?? []) m.set(b.start, b)
    return m
  }, [embed.data])

  const output = (b: UsageBucket) => b.units['output_tokens'] ?? 0
  const cached = (b: UsageBucket) => b.units['cached_input_tokens'] ?? 0
  const embedInput = (b: UsageBucket) => embedByStart.get(b.start)?.units['input_tokens'] ?? 0
  // Chat input is the non-embedding remainder; clamp guards a bucket race.
  const chatInput = (b: UsageBucket) => Math.max(0, (b.units['input_tokens'] ?? 0) - embedInput(b))
  const chatTokens = (b: UsageBucket) => chatInput(b) + output(b)

  const requests = sum(buckets, (b) => b.requests)
  const chatInputTotal = sum(buckets, chatInput)
  const outputTotal = sum(buckets, output)
  const cachedTotal = sum(buckets, cached)
  const chatTokensTotal = chatInputTotal + outputTotal
  const embedInputTotal = sum(buckets, embedInput)

  const inProgress = (b: UsageBucket) => new Date(b.start).getTime() === windowEnd.getTime()
  const label = (b: UsageBucket) => bucketLabel(b.start, range.bucket)
  const title = (b: UsageBucket) =>
    bucketTitle(b.start, range.bucket) + (inProgress(b) ? ' · in progress' : '')

  const requestPoints: ChartPoint[] = buckets.map((b) => ({
    label: label(b),
    title: title(b),
    values: [b.ok + b.cancelled, b.failed],
    rows: b.cancelled ? [{ label: 'cancelled', value: formatNumber(b.cancelled) }] : undefined,
  }))
  const tokenPoints: ChartPoint[] = buckets.map((b) => ({
    label: label(b),
    title: title(b),
    values: [chatInput(b), cached(b), output(b)],
  }))
  const embedPoints: ChartPoint[] = buckets.map((b) => ({
    label: label(b),
    title: title(b),
    values: [embedInput(b)],
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
  const userOptions = useMemo(
    () => [...new Set(rows.map((r) => r.principal))].sort(),
    [rows],
  )
  const modelOptions = useMemo(
    () => [...new Set(rows.map((r) => r.model))].sort(),
    [rows],
  )
  const filtered = useMemo(
    () =>
      rows.filter(
        (r) =>
          (!app || tagValue(r.tags, 'app') === app) &&
          (!context || tagValue(r.tags, 'context') === context) &&
          (!user || r.principal === user) &&
          (!model || r.model === model),
      ),
    [rows, app, context, user, model],
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

  // Requests per user as a donut: who leans on the proxy the most.
  const byUser = useMemo(
    () =>
      groupBy(filtered, (r) => r.principal, (name) => ({ name, ...emptyAgg() })).sort(
        (a, b) => b.requests - a.requests,
      ),
    [filtered],
  )
  const userUsage = useMemo(() => {
    const slice = (u: { name?: string } & Agg, labelOverride?: string): DonutSlice => ({
      label: labelOverride ?? u.name ?? '',
      value: u.requests,
      rows: [{ label: 'tokens', value: formatCompact(tokensOf(u)) }],
    })
    return foldDonut(
      byUser,
      (u) => slice(u),
      (tail) => {
        const merged = emptyAgg()
        for (const u of tail) {
          merged.requests += u.requests
          for (const [unit, quantity] of Object.entries(u.units)) {
            merged.units[unit] = (merged.units[unit] ?? 0) + quantity
          }
        }
        return slice(merged, `Other (${tail.length} users)`)
      },
    )
  }, [byUser])

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
  const stale = !loading && (series.loading || summary.loading || embed.loading)

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
        {app && (
          <FilterSelect
            label="Context"
            value={context}
            onChange={setContext}
            options={contextOptions}
            allLabel="All contexts"
          />
        )}
        <FilterSelect
          label="User"
          value={user}
          onChange={setUser}
          options={userOptions}
          allLabel="All users"
        />
        <FilterSelect
          label="Model"
          value={model}
          onChange={setModel}
          options={modelOptions}
          allLabel="All models"
        />
        {(app || context || user || model) && (
          <Button
            variant="ghost"
            size="sm"
            onClick={() => {
              setApp('')
              setContext('')
              setUser('')
              setModel('')
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
            embed.reload()
          }}
        >
          <RefreshCw />
        </Button>
      </div>

      {loading ? (
        <Spinner />
      ) : series.error || summary.error || embed.error ? (
        <p className="text-sm text-destructive">
          {series.error ?? summary.error ?? embed.error}
        </p>
      ) : (
        <div className={stale ? 'flex flex-col gap-6 opacity-60' : 'flex flex-col gap-6'}>
          <div className="grid gap-4 sm:grid-cols-3">
            <StatTile
              label="Requests"
              value={formatCompact(requests)}
              delta={delta(requests, sum(previous, (b) => b.requests), comparable)}
            />
            <StatTile
              label="Tokens"
              value={formatCompact(chatTokensTotal)}
              secondary={`${formatCompact(chatInputTotal)} in · ${formatCompact(cachedTotal)} cached · ${formatCompact(outputTotal)} out`}
              delta={delta(chatTokensTotal, sum(previous, (b) => chatInput(b) + output(b)), comparable)}
            />
            <StatTile
              label="Embedding"
              value={formatCompact(embedInputTotal)}
              secondary="in"
              delta={delta(embedInputTotal, sum(previous, embedInput), comparable)}
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
                        <TableHead className="text-right">Cached</TableHead>
                        <TableHead className="text-right">Output</TableHead>
                        <TableHead className="text-right">Spend</TableHead>
                      </TableRow>
                    </TableHeader>
                    <TableBody>
                      {byApp.length === 0 && (
                        <TableRow>
                          <TableCell colSpan={7} className="text-muted-foreground">
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
                            title={formatTokens(a.units['cached_input_tokens'])}
                          >
                            {a.units['cached_input_tokens']
                              ? formatCompact(a.units['cached_input_tokens'])
                              : ''}
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
                <CardTitle className="font-serif">Users</CardTitle>
                <CardDescription>Share of requests per user.</CardDescription>
              </CardHeader>
              <CardContent>
                <Donut
                  // Remount on filter changes so the unfolded state resets
                  // with the data.
                  key={`${rangeKey} ${app} ${context} ${user} ${model}`}
                  slices={userUsage.slices}
                  overflow={userUsage.overflow}
                  format={formatCompact}
                  emptyMessage="No usage in this range."
                />
              </CardContent>
            </Card>

            {app && (
              <Card className="lg:col-span-2">
                <CardHeader>
                  <CardTitle className="font-serif">Contexts</CardTitle>
                  <CardDescription>Requests per context in {app}.</CardDescription>
                </CardHeader>
                <CardContent>
                  <ShareBars rows={contextRows} format={formatCompact} />
                </CardContent>
              </Card>
            )}
          </div>

          <Card>
            <CardHeader>
              <CardTitle className="font-serif">Tokens</CardTitle>
              <CardDescription>
                Chat input, cached input and output per {range.bucket}.
              </CardDescription>
            </CardHeader>
            <CardContent>
              <ColumnChart
                points={tokenPoints}
                series={[
                  { ...SERIES_ACCENT, name: 'input' },
                  { ...SERIES_GRAY_MID, name: 'cached' },
                  { ...SERIES_GRAY, name: 'output' },
                ]}
                format={formatCompact}
              />
            </CardContent>
          </Card>

          <Card>
            <CardHeader>
              <CardTitle className="font-serif">Embedding</CardTitle>
              <CardDescription>Input tokens on embeddings per {range.bucket}.</CardDescription>
            </CardHeader>
            <CardContent>
              <ColumnChart
                points={embedPoints}
                series={[{ ...SERIES_ACCENT, name: 'input' }]}
                format={formatCompact}
                emptyMessage="No embeddings in this range."
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
                    <TableHead className="text-right">Cached</TableHead>
                    <TableHead className="text-right">Output</TableHead>
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
                        {formatTokens(r.units['cached_input_tokens'])}
                      </TableCell>
                      <TableCell className="text-right tabular-nums">
                        {formatTokens(r.units['output_tokens'])}
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
