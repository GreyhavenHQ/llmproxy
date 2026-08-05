import { formatCost, formatTokens, type UsageRow } from '@/lib/api'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'

export function UsageTable({
  rows,
  withPrincipal,
}: {
  rows: UsageRow[]
  withPrincipal?: boolean
}) {
  return (
    <Table>
      <TableHeader>
        <TableRow>
          {withPrincipal && <TableHead>User</TableHead>}
          <TableHead>Model</TableHead>
          <TableHead>Endpoint</TableHead>
          <TableHead className="text-right">Requests</TableHead>
          <TableHead className="text-right">Input</TableHead>
          <TableHead className="text-right">Output</TableHead>
          <TableHead className="text-right">Cached</TableHead>
          <TableHead className="text-right">Cost</TableHead>
        </TableRow>
      </TableHeader>
      <TableBody>
        {rows.length === 0 && (
          <TableRow>
            <TableCell
              colSpan={withPrincipal ? 8 : 7}
              className="text-muted-foreground"
            >
              No usage recorded yet.
            </TableCell>
          </TableRow>
        )}
        {rows.map((r, i) => (
          <TableRow key={i}>
            {withPrincipal && <TableCell>{r.principal}</TableCell>}
            <TableCell className="font-mono text-xs">{r.model}</TableCell>
            <TableCell>{r.endpoint}</TableCell>
            <TableCell className="text-right tabular-nums">{formatTokens(r.requests)}</TableCell>
            <TableCell className="text-right tabular-nums">{formatTokens(r.units['input_tokens'])}</TableCell>
            <TableCell className="text-right tabular-nums">{formatTokens(r.units['output_tokens'])}</TableCell>
            <TableCell className="text-right tabular-nums">{formatTokens(r.units['cached_input_tokens'])}</TableCell>
            <TableCell className="text-right tabular-nums">
              {r.cost === null ? (
                <span className="text-muted-foreground">unpriced</span>
              ) : (
                formatCost(r.cost)
              )}
            </TableCell>
          </TableRow>
        ))}
      </TableBody>
    </Table>
  )
}
