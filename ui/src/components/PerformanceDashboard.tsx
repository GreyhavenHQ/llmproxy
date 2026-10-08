// The performance view: latency, time to first token, output speed and
// concurrency, from one /stats/performance call.

import { useMemo, useState } from 'react'
import {
  api,
  formatCompact,
  formatDuration,
  formatNumber,
  type PerfFigure,
  type PerformanceResponse,
  type RequestFacets,
} from '@/lib/api'
import { addBuckets, bucketLabel, bucketTitle, floorBucket, RANGES } from '@/lib/timerange'
import { useAsync } from '@/lib/useAsync'
import { useQueryParam } from '@/lib/useQueryParam'
import {
  LineChart,
  SERIES_ACCENT,
  SERIES_GRAY,
  StatTile,
  type ChartPoint,
  type ChartSeries,
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
import { FilterX, RefreshCw } from 'lucide-react'

const PERCENTILES: ChartSeries[] = [
  { ...SERIES_ACCENT, name: 'p50' },
  { ...SERIES_GRAY, name: 'p95' },
]
const CONCURRENCY: ChartSeries[] = [
  { ...SERIES_ACCENT, name: 'average' },
  { ...SERIES_GRAY, name: 'peak' },
]

const ms = (n: number) => formatDuration(Math.round(n))
const rate = (n: number) => `${formatNumber(Math.round(n))} tok/s`
const level = (n: number) =>
  Number.isInteger(n)
    ? formatCompact(n)
    : n < 0.01
      ? '<0.01'
      : n < 10
        ? n.toFixed(2)
        : formatCompact(Math.round(n))
const dash = (n: number | null, format: (n: number) => string) => (n === null ? '—' : format(n))

function FigureTile({
  label,
  figure,
  format,
}: {
  label: string
  figure: PerfFigure
  format: (n: number) => string
}) {
  return (
    <StatTile
      label={`${label}, median`}
      value={dash(figure.p50, format)}
      secondary={figure.p95 === null ? undefined : `p95 ${format(figure.p95)}`}
      hint={figure.mean === null ? undefined : `mean ${format(figure.mean)}`}
    />
  )
}

export function PerformanceDashboard() {
  const [rangeKey, setRangeKey] = useQueryParam('range', '7d')
  const [principal, setPrincipal] = useQueryParam('user')
  const [provider, setProvider] = useQueryParam('provider')
  const [model, setModel] = useQueryParam('model')
  const [app, setApp] = useQueryParam('app')
  const range = RANGES.find((r) => r.key === rangeKey) ?? RANGES[1]

  const windowEnd = floorBucket(new Date(), range.bucket)
  const windowStart = range.count
    ? addBuckets(windowEnd, range.bucket, -(range.count - 1))
    : null
  const windowQuery = windowStart ? `&since=${windowStart.toISOString()}` : ''
  const filterQuery =
    (principal ? `&principal=${encodeURIComponent(principal)}` : '') +
    (provider ? `&provider=${encodeURIComponent(provider)}` : '') +
    (model ? `&model=${encodeURIComponent(model)}` : '') +
    (app ? `&tag=${encodeURIComponent(`app:${app}`)}` : '')

  const perf = useAsync(
    () =>
      api.get<PerformanceResponse>(
        `/stats/performance?bucket=${range.bucket}` + windowQuery + filterQuery,
      ),
    [rangeKey, principal, provider, model, app],
  )
  const facets = useAsync(
    () =>
      api.get<RequestFacets>(
        '/stats/requests/facets' + (windowQuery ? '?' + windowQuery.slice(1) : ''),
      ),
    [rangeKey],
  )

  const appOptions = useMemo(
    () =>
      [
        ...new Set(
          (facets.data?.tags ?? [])
            .filter((t) => t.startsWith('app:'))
            .map((t) => t.slice(4)),
        ),
      ].sort(),
    [facets.data],
  )

  const summary = perf.data?.summary
  const buckets = perf.data?.series ?? []
  const models = perf.data?.models ?? []

  const inProgress = (start: string) => new Date(start).getTime() === windowEnd.getTime()
  const pointsOf = (values: (b: (typeof buckets)[number]) => (number | null)[]): ChartPoint[] =>
    buckets.map((b) => ({
      label: bucketLabel(b.start, range.bucket),
      title: bucketTitle(b.start, range.bucket) + (inProgress(b.start) ? ' · in progress' : ''),
      values: values(b),
      rows: [{ label: 'requests', value: formatNumber(b.requests) }],
    }))

  const charts = [
    {
      title: 'Duration',
      description: 'Time from request to last byte.',
      points: pointsOf((b) => [b.duration_ms.p50, b.duration_ms.p95]),
      format: ms,
    },
    {
      title: 'Time to first token',
      description: 'Time from request to first streamed byte.',
      points: pointsOf((b) => [b.ttft_ms.p50, b.ttft_ms.p95]),
      format: ms,
    },
    {
      title: 'Output speed',
      description: 'Output tokens per second after the first token.',
      points: pointsOf((b) => [b.tokens_per_second.p50, b.tokens_per_second.p95]),
      format: rate,
    },
  ]
  const concurrencyPoints = pointsOf((b) => [b.concurrency.average, b.concurrency.peak])

  const loading = perf.loading && !perf.data
  const stale = !loading && perf.loading

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
          options={facets.data?.principals ?? []}
          allLabel="All users"
        />
        <FilterSelect
          label="Provider"
          value={provider}
          onChange={setProvider}
          options={facets.data?.providers ?? []}
          allLabel="All providers"
        />
        <FilterSelect
          label="Model"
          value={model}
          onChange={setModel}
          options={facets.data?.models ?? []}
          allLabel="All models"
        />
        <FilterSelect
          label="App"
          value={app}
          onChange={setApp}
          options={appOptions}
          allLabel="All apps"
        />
        {(principal || provider || model || app) && (
          <Button
            variant="ghost"
            size="sm"
            onClick={() => {
              setPrincipal('')
              setProvider('')
              setModel('')
              setApp('')
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
          onClick={() => perf.reload()}
        >
          <RefreshCw />
        </Button>
      </div>

      {loading ? (
        <Spinner />
      ) : perf.error ? (
        <p className="text-sm text-destructive">{perf.error}</p>
      ) : summary ? (
        <div className={stale ? 'flex flex-col gap-6 opacity-60' : 'flex flex-col gap-6'}>
          <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
            <FigureTile label="Duration" figure={summary.duration_ms} format={ms} />
            <FigureTile label="Time to first token" figure={summary.ttft_ms} format={ms} />
            <FigureTile label="Output speed" figure={summary.tokens_per_second} format={rate} />
            <StatTile
              label="Concurrent requests, average"
              value={level(summary.concurrency.average)}
              secondary={`peak ${formatNumber(summary.concurrency.peak)}`}
              hint={`${formatNumber(summary.requests)} requests`}
            />
          </div>

          <div className="grid gap-6 lg:grid-cols-2">
            {charts.map((c) => (
              <Card key={c.title}>
                <CardHeader>
                  <CardTitle className="font-serif">{c.title}</CardTitle>
                  <CardDescription>{c.description}</CardDescription>
                </CardHeader>
                <CardContent>
                  <LineChart points={c.points} series={PERCENTILES} format={c.format} />
                </CardContent>
              </Card>
            ))}
            <Card>
              <CardHeader>
                <CardTitle className="font-serif">Concurrent requests</CardTitle>
                <CardDescription>Requests in flight at the same time.</CardDescription>
              </CardHeader>
              <CardContent>
                <LineChart points={concurrencyPoints} series={CONCURRENCY} format={level} />
              </CardContent>
            </Card>
          </div>

          <Card>
            <CardHeader>
              <CardTitle className="font-serif">By model</CardTitle>
              <CardDescription>Median and p95 for each model.</CardDescription>
            </CardHeader>
            <CardContent>
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>Provider</TableHead>
                    <TableHead>Model</TableHead>
                    <TableHead className="text-right">Requests</TableHead>
                    <TableHead className="text-right">Duration</TableHead>
                    <TableHead className="text-right">First token</TableHead>
                    <TableHead className="text-right">Output speed</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {models.length === 0 && (
                    <TableRow>
                      <TableCell colSpan={6} className="text-muted-foreground">
                        No requests in this range.
                      </TableCell>
                    </TableRow>
                  )}
                  {models.map((m) => (
                    <TableRow key={`${m.provider} ${m.model}`}>
                      <TableCell className="font-mono text-xs">{m.provider || '—'}</TableCell>
                      <TableCell className="font-mono text-xs">{m.model || '—'}</TableCell>
                      <TableCell className="text-right tabular-nums">
                        {formatNumber(m.requests)}
                      </TableCell>
                      <FigureCell figure={m.duration_ms} format={ms} />
                      <FigureCell figure={m.ttft_ms} format={ms} />
                      <FigureCell figure={m.tokens_per_second} format={rate} />
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            </CardContent>
          </Card>
        </div>
      ) : null}
    </div>
  )
}

function FigureCell({ figure, format }: { figure: PerfFigure; format: (n: number) => string }) {
  return (
    <TableCell className="whitespace-nowrap text-right tabular-nums">
      {dash(figure.p50, format)}
      <span className="text-muted-foreground"> / {dash(figure.p95, format)}</span>
    </TableCell>
  )
}
