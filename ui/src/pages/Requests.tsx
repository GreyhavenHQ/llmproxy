// The request explorer: one filtered, paged view of the recorded request
// metadata. Filters and paging are server-side, so the page never holds more
// than what it shows. Dropdown options come from a facets call scoped to the
// selected window, which sees failures too; the usage summary would not.
//
// The table keeps eight columns: who, what, how it went, what it cost, how
// long it took. Everything else (endpoint, key, exact client, tags, per-unit
// tokens) lives one click away in the expanded row, so the table fits the
// card at any width without losing detail. Filters seed from the URL query,
// so the errors dashboard can deep-link a pre-filtered view.
//
// Windows are UTC, matching how the proxy stores timestamps and how the
// usage dashboard buckets. Metadata only; no request or response content is
// recorded anywhere to show.

import { Fragment, useEffect, useMemo, useState } from 'react'
import {
  api,
  clientFamily,
  formatCompact,
  formatCost,
  formatDate,
  formatDuration,
  formatNumber,
  formatTokens,
  tagPairs,
  tagValue,
  type RequestFacets,
  type RequestPage,
  type RequestRow,
} from '@/lib/api'
import { useAsync } from '@/lib/useAsync'
import { FilterSelect, type FilterOption } from '@/components/FilterSelect'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
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
import {
  ChevronDown,
  ChevronLeft,
  ChevronRight,
  FilterX,
  RefreshCw,
  Zap,
} from 'lucide-react'

interface Range {
  key: string
  label: string
  hours: number // 0 = everything recorded
}

const RANGES: Range[] = [
  { key: '24h', label: 'Last 24 hours', hours: 24 },
  { key: '7d', label: 'Last 7 days', hours: 24 * 7 },
  { key: '30d', label: 'Last 30 days', hours: 24 * 30 },
  { key: 'all', label: 'All time', hours: 0 },
  { key: 'custom', label: 'Custom range', hours: 0 },
]

const PAGE_SIZES = [25, 50, 100, 200]

const OUTCOME_OPTIONS: FilterOption[] = [
  { value: 'failed', label: 'Errors only' },
  { value: 'upstream_error', label: 'Provider errors' },
  { value: 'unreachable', label: 'Unreachable' },
  { value: 'cancelled', label: 'Cancelled' },
  { value: 'ok', label: 'OK only' },
]

// A date input holds a UTC day: "2026-08-07" means that day 00:00Z. from is
// inclusive, to is inclusive of the whole day, so it ends at the next
// midnight (the server's until is exclusive).
function dayStart(day: string): string | null {
  return day ? `${day}T00:00:00Z` : null
}

function dayEnd(day: string): string | null {
  if (!day) return null
  const d = new Date(`${day}T00:00:00Z`)
  d.setUTCDate(d.getUTCDate() + 1)
  return d.toISOString()
}

// shortTime keeps the column narrow; the full local timestamp stays on hover.
function shortTime(ts: string): string {
  const d = new Date(ts)
  if (isNaN(d.getTime())) return ts
  return d.toLocaleString(undefined, {
    month: 'short',
    day: 'numeric',
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
  })
}

function OutcomeBadge({ row }: { row: RequestRow }) {
  if (row.cancelled) return <Badge variant="warning">cancelled</Badge>
  if (row.outcome === 'ok') return <Badge variant="success">ok</Badge>
  return (
    <Badge variant="destructive">
      {row.error_kind || row.outcome}
      {row.status_code ? ` · ${row.status_code}` : ''}
    </Badge>
  )
}

// UserCell: the caller, with the last four of the key they used trailing it.
// Relay traffic carries a relay token rather than a key, so those rows show
// the name alone. A long name truncates with the full value on hover, like
// the model column: this table is dense enough that wrapping costs more than
// it gives.
function UserCell({ row }: { row: RequestRow }) {
  return (
    <span className="flex items-baseline">
      <span className="max-w-36 truncate" title={row.principal}>
        {row.principal}
      </span>
      {row.key_suffix && (
        <span
          className="ml-1.5 shrink-0 font-mono text-xs text-muted-foreground"
          title={row.key_label || undefined}
        >
          ···{row.key_suffix}
        </span>
      )}
    </span>
  )
}

// AppCell: the app tag when the caller sent one, the client family as a
// fallback. The exact User-Agent and the full tag list live in the expanded
// row.
function AppCell({ row }: { row: RequestRow }) {
  const app = tagValue(row.tags, 'app')
  if (app) {
    return (
      <Badge variant="tag" size="sm" className="max-w-full font-mono" title={app}>
        <span className="min-w-0 truncate">{app}</span>
      </Badge>
    )
  }
  if (row.client) {
    return (
      <span className="font-mono text-xs text-muted-foreground" title={row.client}>
        {clientFamily(row.client)}
      </span>
    )
  }
  return <span className="text-muted-foreground">—</span>
}

function TokensCell({ row }: { row: RequestRow }) {
  const input = row.units['input_tokens']
  const output = row.units['output_tokens']
  if (!input && !output) return <span className="text-muted-foreground">—</span>
  return (
    <span className="whitespace-nowrap tabular-nums">
      {formatCompact(input ?? 0)}
      <span className="text-muted-foreground"> → </span>
      {formatCompact(output ?? 0)}
    </span>
  )
}

// One labelled figure of the expanded row.
function Detail({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="flex flex-col gap-0.5">
      <span className="text-xs text-muted-foreground">{label}</span>
      <span className="text-sm">{children}</span>
    </div>
  )
}

function RowDetails({ row }: { row: RequestRow }) {
  const units = Object.entries(row.units).sort()
  return (
    <div className="grid grid-cols-2 gap-x-8 gap-y-3 py-2 md:grid-cols-4">
      <Detail label="Provider">
        <span className="font-mono text-xs">{row.provider || '—'}</span>
      </Detail>
      <Detail label="Endpoint">{row.endpoint}</Detail>
      <Detail label="Key">
        {row.key_suffix ? (
          <span className="font-mono text-xs">
            {row.key_label ? `${row.key_label} ` : ''}···{row.key_suffix}
          </span>
        ) : (
          '—'
        )}
      </Detail>
      <Detail label="Recorded (UTC)">
        <span className="font-mono text-xs">{row.ts.replace('T', ' ').slice(0, 19)}</span>
      </Detail>
      <Detail label="Client">
        <span className="break-all font-mono text-xs">{row.client || '—'}</span>
      </Detail>
      <Detail label="Tags">
        {row.tags ? (
          <span className="flex flex-wrap gap-1">
            {tagPairs(row.tags).map((pair) => (
              <Badge key={pair} variant="tag" size="sm" className="font-mono">
                {pair}
              </Badge>
            ))}
          </span>
        ) : (
          '—'
        )}
      </Detail>
      <Detail label="Outcome">
        {row.outcome}
        {row.error_kind ? ` · ${row.error_kind}` : ''}
        {row.status_code ? ` · http ${row.status_code}` : ''}
        {row.streamed ? ' · streamed' : ''}
      </Detail>
      <Detail label="Cost">
        {row.cost !== null ? (
          <span className="tabular-nums">{formatCost(row.cost)}</span>
        ) : row.unpriced ? (
          <span className="text-muted-foreground">unpriced</span>
        ) : (
          '—'
        )}
      </Detail>
      {units.map(([unit, quantity]) => (
        <Detail key={unit} label={unit.replaceAll('_', ' ')}>
          <span className="tabular-nums">{formatTokens(quantity) || '0'}</span>
        </Detail>
      ))}
      <Detail label="Duration">
        <span className="tabular-nums">{formatNumber(row.duration_ms)} ms</span>
      </Detail>
    </div>
  )
}

export function Requests() {
  // Filters seed from the URL query once, so /requests?provider=x&outcome=failed
  // (the errors dashboard's deep link) lands pre-filtered.
  const initial = useMemo(() => new URLSearchParams(window.location.search), [])
  const [rangeKey, setRangeKey] = useState('7d')
  const [from, setFrom] = useState('')
  const [to, setTo] = useState('')
  const [principal, setPrincipal] = useState(initial.get('principal') ?? '')
  const [keyID, setKeyID] = useState('')
  const [client, setClient] = useState('') // a client family; the server matches on prefix
  const [model, setModel] = useState(initial.get('model') ?? '')
  const [provider, setProvider] = useState(initial.get('provider') ?? '')
  const [app, setApp] = useState(initial.get('app') ?? '')
  const [outcome, setOutcome] = useState(initial.get('outcome') ?? '')
  const [pageSize, setPageSize] = useState(50)
  const [page, setPage] = useState(0)
  const [open, setOpen] = useState<string | null>(null)

  // Every fetch, manual or automatic, goes through this counter, so a
  // relative window is recomputed exactly when we refetch and never between
  // two pages of the same set.
  const [tick, setTick] = useState(0)
  const refresh = () => setTick((t) => t + 1)

  const custom = rangeKey === 'custom'
  const range = RANGES.find((r) => r.key === rangeKey) ?? RANGES[1]

  const { since, until } = useMemo(() => {
    if (custom) return { since: dayStart(from), until: dayEnd(to) }
    if (!range.hours) return { since: null, until: null }
    return {
      since: new Date(Date.now() - range.hours * 3600_000).toISOString(),
      until: null,
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [rangeKey, custom, from, to, tick])

  const windowQuery =
    (since ? `&since=${encodeURIComponent(since)}` : '') +
    (until ? `&until=${encodeURIComponent(until)}` : '')
  const filterQuery =
    (principal ? `&principal=${encodeURIComponent(principal)}` : '') +
    (keyID ? `&key=${encodeURIComponent(keyID)}` : '') +
    (client ? `&client=${encodeURIComponent(client)}` : '') +
    (model ? `&model=${encodeURIComponent(model)}` : '') +
    (provider ? `&provider=${encodeURIComponent(provider)}` : '') +
    (app ? `&tag=${encodeURIComponent(`app:${app}`)}` : '') +
    (outcome ? `&outcome=${encodeURIComponent(outcome)}` : '')

  const requests = useAsync(
    () =>
      api.get<RequestPage>(
        `/stats/requests?limit=${pageSize}&offset=${page * pageSize}` +
          windowQuery +
          filterQuery,
      ),
    [tick, since, until, principal, keyID, client, model, provider, app, outcome, pageSize, page],
  )
  // Options depend on the window only, so they stay put while you narrow.
  const facets = useAsync(
    () =>
      api.get<RequestFacets>(
        '/stats/requests/facets' + (windowQuery ? '?' + windowQuery.slice(1) : ''),
      ),
    [tick, since, until],
  )

  // Auto-refresh only on the first page of a relative window: on a later page
  // new events would slide the rows out from under the reader, and a fixed
  // window has nothing new to show. The refresh button always works.
  const live = page === 0 && !custom
  useEffect(() => {
    if (!live) return
    const t = setInterval(refresh, 30_000)
    return () => clearInterval(t)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [live])

  // Any filter change puts you back on the first page; the old offset would
  // point into a different set.
  function narrow<T>(set: (v: T) => void) {
    return (v: T) => {
      set(v)
      setPage(0)
      setOpen(null)
    }
  }

  const keyOptions: FilterOption[] = (facets.data?.keys ?? []).map((k) => ({
    value: k.id,
    label: `${k.principal} · ${k.label || `…${k.key_suffix}`}`,
  }))
  const clientOptions = [
    ...new Set((facets.data?.clients ?? []).map((c) => clientFamily(c))),
  ].sort()
  const appOptions = [
    ...new Set(
      (facets.data?.tags ?? []).filter((t) => t.startsWith('app:')).map((t) => t.slice(4)),
    ),
  ].sort()

  const filtered = principal || keyID || client || model || provider || app || outcome
  function clearFilters() {
    setPrincipal('')
    setKeyID('')
    setClient('')
    setModel('')
    setProvider('')
    setApp('')
    setOutcome('')
    setPage(0)
    setOpen(null)
  }

  const total = requests.data?.total ?? 0
  const shown = requests.data?.requests.length ?? 0
  const firstShown = total === 0 ? 0 : page * pageSize + 1
  const pages = Math.max(1, Math.ceil(total / pageSize))

  return (
    <Card>
      <CardHeader className="flex flex-row items-start justify-between">
        <div className="flex flex-col gap-1.5">
          <CardTitle className="font-serif">Request explorer</CardTitle>
          <CardDescription>
            Metadata only; content is never stored. Click a row for details.
          </CardDescription>
        </div>
        <Button
          variant="outline"
          size="icon-sm"
          aria-label="Refresh"
          onClick={refresh}
        >
          <RefreshCw />
        </Button>
      </CardHeader>
      <CardContent className="space-y-4">
        <div className="flex flex-wrap items-center gap-2">
          <Select value={rangeKey} onValueChange={narrow(setRangeKey)}>
            <SelectTrigger className="w-44" aria-label="Range">
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
          {custom && (
            <>
              <Input
                type="date"
                className="w-40"
                aria-label="From (UTC)"
                value={from}
                onChange={(e) => narrow(setFrom)(e.target.value)}
              />
              <Input
                type="date"
                className="w-40"
                aria-label="To (UTC)"
                value={to}
                onChange={(e) => narrow(setTo)(e.target.value)}
              />
            </>
          )}
          <FilterSelect
            label="User"
            value={principal}
            onChange={narrow(setPrincipal)}
            options={facets.data?.principals ?? []}
            allLabel="All users"
          />
          <FilterSelect
            label="Key"
            value={keyID}
            onChange={narrow(setKeyID)}
            options={keyOptions}
            allLabel="All keys"
          />
          <FilterSelect
            label="Client"
            value={client}
            onChange={narrow(setClient)}
            options={clientOptions}
            allLabel="All clients"
          />
          <FilterSelect
            label="App"
            value={app}
            onChange={narrow(setApp)}
            options={appOptions}
            allLabel="All apps"
          />
          <FilterSelect
            label="Model"
            value={model}
            onChange={narrow(setModel)}
            options={facets.data?.models ?? []}
            allLabel="All models"
          />
          <FilterSelect
            label="Provider"
            value={provider}
            onChange={narrow(setProvider)}
            options={facets.data?.providers ?? []}
            allLabel="All providers"
          />
          <FilterSelect
            label="Outcome"
            value={outcome}
            onChange={narrow(setOutcome)}
            options={OUTCOME_OPTIONS}
            allLabel="All outcomes"
          />
          {filtered && (
            <Button variant="ghost" size="sm" onClick={clearFilters}>
              <FilterX /> Clear filters
            </Button>
          )}
        </div>

        {requests.loading && !requests.data ? (
          <Spinner />
        ) : requests.error ? (
          <p className="text-sm text-destructive">{requests.error}</p>
        ) : (
          <>
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead className="w-6" />
                  <TableHead>Time</TableHead>
                  <TableHead>User</TableHead>
                  <TableHead>App</TableHead>
                  <TableHead>Model</TableHead>
                  <TableHead>Status</TableHead>
                  <TableHead className="text-right">Tokens</TableHead>
                  <TableHead className="text-right">Cost</TableHead>
                  <TableHead className="text-right">Duration</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {shown === 0 && (
                  <TableRow>
                    <TableCell colSpan={9} className="text-muted-foreground">
                      {filtered || since
                        ? 'No requests match these filters.'
                        : 'No requests recorded yet.'}
                    </TableCell>
                  </TableRow>
                )}
                {requests.data?.requests.map((r) => (
                  <Fragment key={r.id}>
                    <TableRow
                      className="cursor-pointer"
                      onClick={() => setOpen(open === r.id ? null : r.id)}
                    >
                      <TableCell className="pr-0">
                        <button
                          type="button"
                          aria-label={open === r.id ? 'Hide details' : 'Show details'}
                          aria-expanded={open === r.id}
                          className="flex cursor-pointer items-center rounded-sm text-muted-foreground outline-none focus-visible:ring-2 focus-visible:ring-ring"
                        >
                          {open === r.id ? (
                            <ChevronDown className="size-3.5" />
                          ) : (
                            <ChevronRight className="size-3.5" />
                          )}
                        </button>
                      </TableCell>
                      <TableCell
                        className="whitespace-nowrap text-xs"
                        title={formatDate(r.ts)}
                      >
                        {shortTime(r.ts)}
                      </TableCell>
                      <TableCell>
                        <UserCell row={r} />
                      </TableCell>
                      <TableCell className="max-w-44">
                        <AppCell row={r} />
                      </TableCell>
                      <TableCell
                        className="max-w-44 truncate font-mono text-xs"
                        title={r.model || undefined}
                      >
                        {r.model || <span className="text-muted-foreground">—</span>}
                      </TableCell>
                      <TableCell>
                        <span className="flex items-center gap-1.5">
                          <OutcomeBadge row={r} />
                          {r.streamed && (
                            <span title="streamed">
                              <Zap className="size-3 text-muted-foreground" aria-label="streamed" />
                            </span>
                          )}
                        </span>
                      </TableCell>
                      <TableCell
                        className="text-right"
                        title={
                          r.units['input_tokens'] || r.units['output_tokens']
                            ? `${formatTokens(r.units['input_tokens']) || 0} in · ` +
                              `${formatTokens(r.units['output_tokens']) || 0} out` +
                              (r.units['cached_input_tokens']
                                ? ` · ${formatTokens(r.units['cached_input_tokens'])} cached`
                                : '')
                            : undefined
                        }
                      >
                        <TokensCell row={r} />
                      </TableCell>
                      <TableCell className="text-right tabular-nums">
                        {r.cost !== null ? (
                          formatCost(r.cost)
                        ) : r.unpriced ? (
                          <span className="text-muted-foreground">unpriced</span>
                        ) : (
                          // Nothing was consumed (a failure with no usage):
                          // there is no cost to be missing.
                          <span className="text-muted-foreground">—</span>
                        )}
                      </TableCell>
                      <TableCell className="text-right tabular-nums">
                        {formatDuration(r.duration_ms)}
                      </TableCell>
                    </TableRow>
                    {open === r.id && (
                      <TableRow className="bg-muted/30 hover:bg-muted/30">
                        <TableCell />
                        <TableCell colSpan={8}>
                          <RowDetails row={r} />
                        </TableCell>
                      </TableRow>
                    )}
                  </Fragment>
                ))}
              </TableBody>
            </Table>

            <div className="flex flex-wrap items-center justify-between gap-2">
              <p className="text-sm text-muted-foreground">
                {total === 0
                  ? 'No requests'
                  : `Showing ${formatNumber(firstShown)}–${formatNumber(
                      firstShown + shown - 1,
                    )} of ${formatNumber(total)}`}
              </p>
              <div className="flex items-center gap-2">
                <Select
                  value={String(pageSize)}
                  onValueChange={(v) => narrow(setPageSize)(Number(v))}
                >
                  <SelectTrigger className="w-32" aria-label="Page size">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {PAGE_SIZES.map((n) => (
                      <SelectItem key={n} value={String(n)}>
                        {n} per page
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
                <span className="text-sm text-muted-foreground">
                  Page {page + 1} of {formatNumber(pages)}
                </span>
                <Button
                  variant="outline"
                  size="icon-sm"
                  aria-label="Previous page"
                  disabled={page === 0}
                  onClick={() => {
                    setPage(page - 1)
                    setOpen(null)
                  }}
                >
                  <ChevronLeft />
                </Button>
                <Button
                  variant="outline"
                  size="icon-sm"
                  aria-label="Next page"
                  disabled={page + 1 >= pages}
                  onClick={() => {
                    setPage(page + 1)
                    setOpen(null)
                  }}
                >
                  <ChevronRight />
                </Button>
              </div>
            </div>
          </>
        )}
      </CardContent>
    </Card>
  )
}
