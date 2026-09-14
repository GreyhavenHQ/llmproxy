import { api, type RateLimitSnapshot, formatNumber } from '@/lib/api'
import { useAsync } from '@/lib/useAsync'
import { Button } from '@/components/ui/button'
import { Spinner } from '@/components/ui/spinner'
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'
import { cn } from '@/lib/utils'
import { RefreshCw } from 'lucide-react'

function ago(ts: string): string {
  const ms = Date.now() - new Date(ts).getTime()
  if (ms < 60_000) return 'just now'
  const mins = Math.floor(ms / 60_000)
  if (mins < 60) return `${mins}m ago`
  const hours = Math.floor(mins / 60)
  if (hours < 24) return `${hours}h ago`
  return `${Math.floor(hours / 24)}d ago`
}

function pct(remaining: number | undefined, limit: number | undefined): string | null {
  if (remaining === undefined || limit === undefined || limit === 0) return null
  return `${Math.round((remaining / limit) * 100)}%`
}

function Meter({
  label,
  remaining,
  limit,
}: {
  label: string
  remaining: number | undefined
  limit: number | undefined
}) {
  if (remaining === undefined && limit === undefined) return null
  const ratio = limit && limit > 0 ? (remaining ?? 0) / limit : null
  const color =
    ratio === null
      ? 'bg-muted-foreground'
      : ratio > 0.5
        ? 'bg-green-500'
        : ratio > 0.2
          ? 'bg-yellow-500'
          : 'bg-red-500'
  return (
    <div className="flex flex-col gap-1">
      <div className="flex items-baseline justify-between text-xs">
        <span className="text-muted-foreground">{label}</span>
        <span className="tabular-nums">
          {remaining !== undefined ? formatNumber(remaining) : '?'}
          {limit !== undefined ? ` / ${formatNumber(limit)}` : ''}
          {pct(remaining, limit) && (
            <span className="ml-1 text-muted-foreground">({pct(remaining, limit)})</span>
          )}
        </span>
      </div>
      {ratio !== null && (
        <div className="h-1.5 w-full overflow-hidden rounded-full bg-muted">
          <div
            className={cn('h-full rounded-full transition-all', color)}
            style={{ width: `${Math.max(ratio * 100, 1)}%` }}
          />
        </div>
      )}
    </div>
  )
}

function ProviderCard({ snap }: { snap: RateLimitSnapshot }) {
  const hasRequests = snap.limit_requests !== undefined || snap.remaining_requests !== undefined
  const hasTokens = snap.limit_tokens !== undefined || snap.remaining_tokens !== undefined
  return (
    <Card>
      <CardHeader className="pb-3">
        <div className="flex items-center justify-between">
          <CardTitle className="text-base font-medium">{snap.provider}</CardTitle>
          <span
            className={cn(
              'text-xs tabular-nums',
              snap.stale ? 'text-yellow-600 dark:text-yellow-400' : 'text-muted-foreground',
            )}
            title={new Date(snap.observed_at).toLocaleString()}
          >
            {ago(snap.observed_at)}
            {snap.stale && ' (stale)'}
          </span>
        </div>
      </CardHeader>
      <CardContent className="flex flex-col gap-3">
        {hasRequests && (
          <Meter
            label="Requests"
            remaining={snap.remaining_requests}
            limit={snap.limit_requests}
          />
        )}
        {hasTokens && (
          <Meter label="Tokens" remaining={snap.remaining_tokens} limit={snap.limit_tokens} />
        )}
        {!hasRequests && !hasTokens && (
          <p className="text-sm text-muted-foreground">No rate-limit data yet.</p>
        )}
      </CardContent>
    </Card>
  )
}

export function ProvidersUsage() {
  const { data, loading, error, reload } = useAsync(
    () => api.get<{ rate_limits: RateLimitSnapshot[] }>('/stats/rate-limits'),
    [],
  )

  return (
    <div className="flex flex-col gap-6">
      <div className="flex items-center justify-between">
        <div>
          <h2 className="font-serif text-lg font-semibold">Provider rate limits</h2>
          <p className="text-sm text-muted-foreground">
            Upstream quota reported by each configured provider.
          </p>
        </div>
        <Button variant="outline" size="sm" onClick={reload} disabled={loading}>
          {loading ? <Spinner /> : <RefreshCw className="size-4" />}
          Refresh
        </Button>
      </div>

      {loading && !data ? (
        <div className="flex justify-center py-12">
          <Spinner className="size-6" />
        </div>
      ) : error ? (
        <p className="text-sm text-destructive">{error}</p>
      ) : data?.rate_limits.length === 0 ? (
        <Card>
          <CardContent className="py-8 text-center text-sm text-muted-foreground">
            No provider reports rate limits yet.
          </CardContent>
        </Card>
      ) : (
        <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
          {data?.rate_limits.map((snap) => (
            <ProviderCard key={snap.provider_id} snap={snap} />
          ))}
        </div>
      )}
    </div>
  )
}
