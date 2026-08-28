import { useState } from 'react'
import { toast } from 'sonner'
import { api, formatDay, type KeyInfo } from '@/lib/api'
import { useAsync } from '@/lib/useAsync'
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
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogTrigger,
} from '@/components/ui/alert-dialog'

export function AllKeys() {
  const { data, loading, error, reload } = useAsync(() =>
    api.get<{ keys: KeyInfo[] }>('/admin/v1/keys?limit=500'),
  )
  const [q, setQ] = useState('')
  const needle = q.trim().toLowerCase()
  const keys = (data?.keys ?? []).filter((k) =>
    `${k.principal} ${k.label} ${k.key_suffix}`.toLowerCase().includes(needle),
  )

  const remove = async (id: string) => {
    try {
      await api.del(`/admin/v1/keys/${id}`)
      toast.success('Key deleted')
      reload()
    } catch (err) {
      toast.error(err instanceof Error ? err.message : String(err))
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle className="font-serif">All keys</CardTitle>
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        <Input
          className="w-64"
          placeholder="Filter by owner, label or suffix"
          value={q}
          onChange={(e) => setQ(e.target.value)}
        />
        {loading && !data ? (
          <Spinner />
        ) : error ? (
          <p className="text-sm text-destructive">{error}</p>
        ) : (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Owner</TableHead>
                <TableHead>Label</TableHead>
                <TableHead>Key</TableHead>
                <TableHead>Created</TableHead>
                <TableHead>Last used</TableHead>
                <TableHead className="w-24" />
              </TableRow>
            </TableHeader>
            <TableBody>
              {keys.length === 0 && (
                <TableRow>
                  <TableCell colSpan={6} className="text-muted-foreground">
                    {data?.keys.length ? 'No match.' : 'No keys yet.'}
                  </TableCell>
                </TableRow>
              )}
              {keys.map((k) => (
                <TableRow key={k.id}>
                  <TableCell className="wrap-anywhere">{k.principal}</TableCell>
                  <TableCell className="wrap-anywhere">
                    {k.label || <span className="text-muted-foreground">(no label)</span>}
                  </TableCell>
                  <TableCell className="font-mono text-xs">***{k.key_suffix}</TableCell>
                  <TableCell className="whitespace-nowrap">{formatDay(k.created_at)}</TableCell>
                  <TableCell className="whitespace-nowrap">
                    {formatDay(k.last_used_at) || 'never'}
                  </TableCell>
                  <TableCell className="whitespace-nowrap">
                    <AlertDialog>
                      <AlertDialogTrigger asChild>
                        <Button variant="ghost" size="sm">
                          Delete
                        </Button>
                      </AlertDialogTrigger>
                      <AlertDialogContent>
                        <AlertDialogHeader>
                          <AlertDialogTitle>
                            Delete this key of {k.principal}?
                          </AlertDialogTitle>
                          <AlertDialogDescription>
                            It stops working immediately.
                          </AlertDialogDescription>
                        </AlertDialogHeader>
                        <AlertDialogFooter>
                          <AlertDialogCancel>Cancel</AlertDialogCancel>
                          <AlertDialogAction onClick={() => remove(k.id)}>
                            Delete
                          </AlertDialogAction>
                        </AlertDialogFooter>
                      </AlertDialogContent>
                    </AlertDialog>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
      </CardContent>
    </Card>
  )
}
