// The request explorer: one filtered, paged view of the recorded request
// metadata. Filters and paging are server-side, so the page never holds more
// than what it shows. Dropdown options come from a facets call scoped to the
// selected window, which sees failures too; the usage summary would not.
//
// Windows are UTC, matching how the proxy stores timestamps and how the
// usage dashboard buckets. Metadata only; no request or response content is
// recorded anywhere to show.

import { useEffect, useMemo, useState } from 'react'
import {
  api,
  clientFamily,
  formatCost,
  formatDate,
  formatNumber,
  formatTokens,
  tagPairs,
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
import { ChevronLeft, ChevronRight, FilterX, RefreshCw } from 'lucide-react'

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

function OutcomeBadge({ row }: { row: RequestRow }) {
  if (row.cancelled) return <Badge variant="warning">cancelled</Badge>
  if (row.outcome === 'ok') {
    return <Badge variant="success">ok{row.streamed ? ' · stream' : ''}</Badge>
  }
  return (
    <Badge variant="destructive">
      {row.outcome}
      {row.status_code ? ` · ${row.status_code}` : ''}
    </Badge>
  )
}

// UserCell: the caller, with the last four of the key they used trailing it.
// Both filters are in the row above, so the key needs no column of its own;
// the label rides along in the tooltip. Relay traffic carries a relay token
// rather than a key, so those rows show the name alone.
function UserCell({ row }: { row: RequestRow }) {
  return (
    <span className="whitespace-nowrap">
      {row.principal}
      {row.key_suffix && (
        <span
          className="ml-1.5 font-mono text-xs text-muted-foreground"
          title={row.key_label || undefined}
        >
          ···{row.key_suffix}
        </span>
      )}
    </span>
  )
}

export function Requests() {
  const [rangeKey, setRangeKey] = useState('7d')
  const [from, setFrom] = useState('')
  const [to, setTo] = useState('')
  const [principal, setPrincipal] = useState('')
  const [keyID, setKeyID] = useState('')
  const [client, setClient] = useState('') // a client family; the server matches on prefix
  const [model, setModel] = useState('')
  const [provider, setProvider] = useState('')
  const [pageSize, setPageSize] = useState(50)
  const [page, setPage] = useState(0)

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
    (provider ? `&provider=${encodeURIComponent(provider)}` : '')

  const requests = useAsync(
    () =>
      api.get<RequestPage>(
        `/stats/requests?limit=${pageSize}&offset=${page * pageSize}` +
          windowQuery +
          filterQuery,
      ),
    [tick, since, until, principal, keyID, client, model, provider, pageSize, page],
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
    }
  }

  const keyOptions: FilterOption[] = (facets.data?.keys ?? []).map((k) => ({
    value: k.id,
    label: `${k.principal} · ${k.label || `…${k.key_suffix}`}`,
  }))
  const clientOptions = [
    ...new Set((facets.data?.clients ?? []).map((c) => clientFamily(c))),
  ].sort()

  const filtered = principal || keyID || client || model || provider
  function clearFilters() {
    setPrincipal('')
    setKeyID('')
    setClient('')
    setModel('')
    setProvider('')
    setPage(0)
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
            Metadata only; content is never stored. Times are UTC.
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
                  <TableHead>Time</TableHead>
                  <TableHead>User</TableHead>
                  <TableHead>Model</TableHead>
                  <TableHead>Endpoint</TableHead>
                  <TableHead>Client</TableHead>
                  <TableHead>Tags</TableHead>
                  <TableHead>Outcome</TableHead>
                  <TableHead className="text-right">Input</TableHead>
                  <TableHead className="text-right">Output</TableHead>
                  <TableHead className="text-right">Cached</TableHead>
                  <TableHead className="text-right">Cost</TableHead>
                  <TableHead className="text-right">Duration</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {shown === 0 && (
                  <TableRow>
                    <TableCell colSpan={12} className="text-muted-foreground">
                      {filtered || since
                        ? 'No requests match these filters.'
                        : 'No requests recorded yet.'}
                    </TableCell>
                  </TableRow>
                )}
                {requests.data?.requests.map((r) => (
                  <TableRow key={r.id}>
                    <TableCell className="whitespace-nowrap text-xs">
                      {formatDate(r.ts)}
                    </TableCell>
                    <TableCell>
                      <UserCell row={r} />
                    </TableCell>
                    <TableCell className="font-mono text-xs">{r.model}</TableCell>
                    <TableCell>{r.endpoint}</TableCell>
                    <TableCell className="font-mono text-xs" title={r.client || undefined}>
                      {r.client ? (
                        clientFamily(r.client)
                      ) : (
                        <span className="text-muted-foreground">—</span>
                      )}
                    </TableCell>
                    <TableCell>
                      {r.tags ? (
                        <span className="flex flex-wrap gap-1">
                          {tagPairs(r.tags).map((pair) => (
                            <Badge key={pair} variant="tag" size="sm" className="font-mono">
                              {pair}
                            </Badge>
                          ))}
                        </span>
                      ) : (
                        <span className="text-muted-foreground">—</span>
                      )}
                    </TableCell>
                    <TableCell>
                      <OutcomeBadge row={r} />
                    </TableCell>
                    <TableCell className="text-right">
                      {formatTokens(r.units['input_tokens'])}
                    </TableCell>
                    <TableCell className="text-right">
                      {formatTokens(r.units['output_tokens'])}
                    </TableCell>
                    <TableCell className="text-right">
                      {formatTokens(r.units['cached_input_tokens'])}
                    </TableCell>
                    <TableCell className="text-right">
                      {r.cost === null ? (
                        <span className="text-muted-foreground">unpriced</span>
                      ) : (
                        formatCost(r.cost)
                      )}
                    </TableCell>
                    <TableCell className="text-right">{r.duration_ms} ms</TableCell>
                  </TableRow>
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
                  onClick={() => setPage(page - 1)}
                >
                  <ChevronLeft />
                </Button>
                <Button
                  variant="outline"
                  size="icon-sm"
                  aria-label="Next page"
                  disabled={page + 1 >= pages}
                  onClick={() => setPage(page + 1)}
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
