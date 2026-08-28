// What the platform serves, for everyone rather than admins only: the model
// list any caller sees on GET /v1/models, joined with how much each one is
// used.
//
// The catalog is the spine of the table, so a model with no traffic in the
// range still has a row; usage comes from the same team-wide /stats/summary
// the other usage views read, folded per model client-side. A model used
// before it was renamed, or since removed, is not in the catalog and does not
// appear here.

import { useMemo, useState } from 'react'
import {
  api,
  formatCompact,
  formatMoney,
  formatTokens,
  type CatalogModel,
  type StatsRow,
} from '@/lib/api'
import { addBuckets, floorBucket, RANGES } from '@/lib/timerange'
import { useAsync } from '@/lib/useAsync'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
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
import { cn } from '@/lib/utils'
import { RefreshCw } from 'lucide-react'

type SortKey = 'name' | 'requests' | 'tokens' | 'cost'

interface Row {
  model: CatalogModel
  requests: number
  tokens: number
  // null while nothing priced has landed: unpriced is a state, never zero.
  cost: number | null
}

// A sortable header: name reads A→Z, the usage columns read biggest first.
function SortHead({
  label,
  sortKey,
  active,
  onSort,
  numeric,
}: {
  label: string
  sortKey: SortKey
  active: boolean
  onSort: (key: SortKey) => void
  numeric?: boolean
}) {
  return (
    <TableHead className={numeric ? 'text-right' : undefined}>
      <button
        type="button"
        onClick={() => onSort(sortKey)}
        aria-label={`Sort by ${label.toLowerCase()}`}
        className={cn(
          'cursor-pointer outline-none focus-visible:ring-2 focus-visible:ring-ring',
          active ? 'font-medium text-foreground' : 'hover:text-foreground',
        )}
      >
        {label}
        {active && <span aria-hidden> {sortKey === 'name' ? '↑' : '↓'}</span>}
      </button>
    </TableHead>
  )
}

export function ModelsCatalog() {
  const [rangeKey, setRangeKey] = useState('30d')
  const [sort, setSort] = useState<SortKey>('name')
  const [showHidden, setShowHidden] = useState(false)
  const range = RANGES.find((r) => r.key === rangeKey) ?? RANGES[2]

  const windowEnd = floorBucket(new Date(), range.bucket)
  const windowStart = range.count
    ? addBuckets(windowEnd, range.bucket, -(range.count - 1))
    : null

  // The catalog does not change with the range, so it is fetched once. Hidden
  // models come along: they are off the caller-facing list but can still have
  // spent money, so they belong here behind a toggle.
  const models = useAsync(
    () => api.get<{ data: CatalogModel[] }>('/v1/models?include_hidden=1'),
    [],
  )
  const summary = useAsync(
    () =>
      api.get<{ usage: StatsRow[] }>(
        '/stats/summary' + (windowStart ? `?since=${windowStart.toISOString()}` : ''),
      ),
    [rangeKey],
  )

  const rows = useMemo(() => {
    const usage = new Map<string, { requests: number; tokens: number; cost: number | null }>()
    for (const row of summary.data?.usage ?? []) {
      let acc = usage.get(row.model)
      if (!acc) {
        acc = { requests: 0, tokens: 0, cost: null }
        usage.set(row.model, acc)
      }
      acc.requests += row.requests
      acc.tokens += (row.units['input_tokens'] ?? 0) + (row.units['output_tokens'] ?? 0)
      if (row.cost !== null) acc.cost = (acc.cost ?? 0) + row.cost
    }
    const out: Row[] = (models.data?.data ?? [])
      .filter((model) => showHidden || !model.hidden)
      .map((model) => ({
        model,
        requests: usage.get(model.id)?.requests ?? 0,
        tokens: usage.get(model.id)?.tokens ?? 0,
        cost: usage.get(model.id)?.cost ?? null,
      }))
    // Alphabetical is the tie-break under every sort, so equally unused
    // models keep a stable, readable order.
    const byName = (a: Row, b: Row) => a.model.id.localeCompare(b.model.id)
    return out.sort((a, b) => {
      if (sort === 'name') return byName(a, b)
      if (sort === 'cost') return (b.cost ?? -1) - (a.cost ?? -1) || byName(a, b)
      return b[sort] - a[sort] || byName(a, b)
    })
  }, [models.data, summary.data, sort, showHidden])

  const hiddenCount = (models.data?.data ?? []).filter((m) => m.hidden).length

  const loading = models.loading && !models.data
  const stale = !loading && (models.loading || summary.loading)

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
        <Button
          variant="outline"
          size="icon-sm"
          aria-label="Refresh"
          onClick={() => {
            models.reload()
            summary.reload()
          }}
        >
          <RefreshCw />
        </Button>
        {hiddenCount > 0 && (
          <label className="flex items-center gap-2 text-sm text-muted-foreground">
            <Checkbox
              checked={showHidden}
              onCheckedChange={(c) => setShowHidden(c === true)}
            />
            Show hidden
          </label>
        )}
      </div>

      {loading ? (
        <Spinner />
      ) : models.error || summary.error ? (
        <p className="text-sm text-destructive">{models.error ?? summary.error}</p>
      ) : (
        <Card className={stale ? 'opacity-60' : undefined}>
          <CardHeader>
            <CardTitle className="font-serif">Models</CardTitle>
            <CardDescription>Every model you can call, and how much it is used.</CardDescription>
          </CardHeader>
          <CardContent>
            <Table>
              <TableHeader>
                <TableRow>
                  <SortHead label="Model" sortKey="name" active={sort === 'name'} onSort={setSort} />
                  <TableHead>Capabilities</TableHead>
                  <SortHead
                    label="Requests"
                    sortKey="requests"
                    active={sort === 'requests'}
                    onSort={setSort}
                    numeric
                  />
                  <SortHead
                    label="Tokens"
                    sortKey="tokens"
                    active={sort === 'tokens'}
                    onSort={setSort}
                    numeric
                  />
                  <SortHead
                    label="Spend"
                    sortKey="cost"
                    active={sort === 'cost'}
                    onSort={setSort}
                    numeric
                  />
                </TableRow>
              </TableHeader>
              <TableBody>
                {rows.length === 0 && (
                  <TableRow>
                    <TableCell colSpan={5} className="text-muted-foreground">
                      No models available yet.
                    </TableCell>
                  </TableRow>
                )}
                {rows.map((r) => (
                  <TableRow key={r.model.id}>
                    <TableCell className="font-medium wrap-anywhere">
                      <span className="flex flex-col">
                        <span className="flex flex-wrap items-center gap-2">
                          {r.model.id}
                          {r.model.hidden && <Badge variant="muted">hidden</Badge>}
                        </span>
                        {r.model.alias_of && (
                          <span className="font-mono text-xs text-muted-foreground">
                            → {r.model.alias_of}
                          </span>
                        )}
                      </span>
                    </TableCell>
                    <TableCell>
                      <div className="flex flex-wrap gap-1">
                        {r.model.capabilities.map((c) => (
                          <Badge key={c} variant="muted">
                            {c}
                          </Badge>
                        ))}
                      </div>
                    </TableCell>
                    <TableCell className="text-right tabular-nums">
                      {r.requests ? formatCompact(r.requests) : ''}
                    </TableCell>
                    <TableCell className="text-right tabular-nums" title={formatTokens(r.tokens)}>
                      {r.tokens ? formatCompact(r.tokens) : ''}
                    </TableCell>
                    <TableCell className="text-right tabular-nums">
                      {!r.requests ? (
                        ''
                      ) : r.cost === null ? (
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
      )}
    </div>
  )
}
