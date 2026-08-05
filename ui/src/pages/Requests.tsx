import { useEffect } from 'react'
import {
  api,
  clientFamily,
  formatCost,
  formatDate,
  formatTokens,
  type RequestRow,
} from '@/lib/api'
import { useAsync } from '@/lib/useAsync'
import { Badge } from '@/components/ui/badge'
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
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { RefreshCw } from 'lucide-react'

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

export function Requests() {
  const requests = useAsync(() =>
    api.get<{ requests: RequestRow[] }>('/stats/requests?limit=50'),
  )

  useEffect(() => {
    const t = setInterval(() => requests.reload(), 30_000)
    return () => clearInterval(t)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  return (
    <Card>
      <CardHeader className="flex flex-row items-start justify-between">
        <div className="flex flex-col gap-1.5">
          <CardTitle className="font-serif">Recent requests</CardTitle>
          <CardDescription>
            Metadata only; content is never stored. Auto-refreshes every 30
            seconds.
          </CardDescription>
        </div>
        <Button
          variant="outline"
          size="icon-sm"
          aria-label="Refresh"
          onClick={() => requests.reload()}
        >
          <RefreshCw />
        </Button>
      </CardHeader>
      <CardContent>
        {requests.loading && !requests.data ? (
          <Spinner />
        ) : requests.error ? (
          <p className="text-sm text-destructive">{requests.error}</p>
        ) : (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Time</TableHead>
                <TableHead>User</TableHead>
                <TableHead>Model</TableHead>
                <TableHead>Endpoint</TableHead>
                <TableHead>Client</TableHead>
                <TableHead>Outcome</TableHead>
                <TableHead className="text-right">Input</TableHead>
                <TableHead className="text-right">Output</TableHead>
                <TableHead className="text-right">Cached</TableHead>
                <TableHead className="text-right">Cost</TableHead>
                <TableHead className="text-right">Duration</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {requests.data?.requests.length === 0 && (
                <TableRow>
                  <TableCell colSpan={11} className="text-muted-foreground">
                    No requests recorded yet.
                  </TableCell>
                </TableRow>
              )}
              {requests.data?.requests.map((r, i) => (
                <TableRow key={i}>
                  <TableCell className="whitespace-nowrap text-xs">
                    {formatDate(r.ts)}
                  </TableCell>
                  <TableCell>{r.principal}</TableCell>
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
        )}
      </CardContent>
    </Card>
  )
}
