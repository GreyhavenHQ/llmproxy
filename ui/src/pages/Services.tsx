import { useState } from 'react'
import { toast } from 'sonner'
import { api, ApiError, formatDay, type KeyInfo, type Principal } from '@/lib/api'
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
import { Copy } from 'lucide-react'

function errMsg(err: unknown): string {
  return err instanceof Error ? err.message : String(err)
}

// One service, one key: a row is a key owned by a service principal.
// Creating a service creates the principal (reusing it if the name exists)
// and mints its key in the same step.
export function Services() {
  const { data, loading, error, reload } = useAsync(async () => {
    const [{ principals }, { keys }] = await Promise.all([
      api.get<{ principals: Principal[] }>('/admin/v1/principals?limit=500'),
      api.get<{ keys: KeyInfo[] }>('/admin/v1/keys?limit=500'),
    ])
    const services = new Set(
      principals.filter((p) => p.kind === 'service').map((p) => p.name),
    )
    return {
      users: new Set(principals.filter((p) => p.kind === 'user').map((p) => p.name)),
      services,
      keys: keys.filter((k) => k.principal && services.has(k.principal)),
    }
  })
  const [name, setName] = useState('')
  const [creating, setCreating] = useState(false)
  const [newKey, setNewKey] = useState<KeyInfo | null>(null)

  const mint = async (service: string) => {
    const key = await api.post<KeyInfo>('/admin/v1/keys', { principal: service, label: '' })
    setNewKey(key)
    reload()
  }

  const create = async (e: React.FormEvent) => {
    e.preventDefault()
    if (data?.users.has(name)) {
      toast.error(`"${name}" is a user, not a service`)
      return
    }
    setCreating(true)
    try {
      if (!data?.services.has(name)) {
        try {
          await api.post('/admin/v1/principals', { name, kind: 'service', role: 'member' })
        } catch (err) {
          // Someone else may have created it since the last load.
          if (!(err instanceof ApiError && err.code === 'principal_exists')) throw err
        }
      }
      await mint(name)
      setName('')
    } catch (err) {
      toast.error(errMsg(err))
    } finally {
      setCreating(false)
    }
  }

  const rotate = async (k: KeyInfo) => {
    try {
      await mint(k.principal ?? '')
      await api.del(`/admin/v1/keys/${k.id}`)
      reload()
    } catch (err) {
      toast.error(errMsg(err))
    }
  }

  const remove = async (id: string) => {
    try {
      await api.del(`/admin/v1/keys/${id}`)
      toast.success('Key deleted')
      reload()
    } catch (err) {
      toast.error(errMsg(err))
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle className="font-serif">Services</CardTitle>
        <CardDescription>One key per service, shown once.</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        <form onSubmit={create} className="flex flex-wrap items-center gap-2">
          <Input
            className="w-64"
            placeholder="Name (e.g. batch-service)"
            maxLength={120}
            pattern="[a-z0-9][a-z0-9._\-]*"
            title="lowercase letters, digits, dots, dashes, underscores"
            required
            value={name}
            onChange={(e) => setName(e.target.value)}
          />
          <Button type="submit" disabled={creating}>
            {creating && <Spinner />}
            Create service
          </Button>
        </form>

        {newKey && (
          <div className="rounded-md border border-primary/40 bg-primary/5 p-4">
            <p className="mb-2 text-sm font-medium">
              Key for {newKey.principal} — copy it now, it will not be shown
              again.
            </p>
            <div className="flex items-center gap-2">
              <code className="flex-1 overflow-x-auto rounded bg-background px-2 py-1.5 font-mono text-sm">
                {newKey.key}
              </code>
              <Button
                variant="outline"
                size="icon-sm"
                aria-label="Copy key"
                onClick={() => {
                  navigator.clipboard.writeText(newKey.key ?? '')
                  toast.success('Copied to clipboard')
                }}
              >
                <Copy />
              </Button>
            </div>
          </div>
        )}

        {loading && !data ? (
          <Spinner />
        ) : error ? (
          <p className="text-sm text-destructive">{error}</p>
        ) : (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Service</TableHead>
                <TableHead>Key</TableHead>
                <TableHead>Created</TableHead>
                <TableHead>Last used</TableHead>
                <TableHead className="w-40" />
              </TableRow>
            </TableHeader>
            <TableBody>
              {data?.keys.length === 0 && (
                <TableRow>
                  <TableCell colSpan={5} className="text-muted-foreground">
                    No services yet.
                  </TableCell>
                </TableRow>
              )}
              {data?.keys.map((k) => (
                <TableRow key={k.id}>
                  <TableCell>{k.principal}</TableCell>
                  <TableCell className="font-mono text-xs">***{k.key_suffix}</TableCell>
                  <TableCell>{formatDay(k.created_at)}</TableCell>
                  <TableCell>{formatDay(k.last_used_at) || 'never'}</TableCell>
                  <TableCell className="text-right">
                    <AlertDialog>
                      <AlertDialogTrigger asChild>
                        <Button variant="ghost" size="sm">
                          Rotate
                        </Button>
                      </AlertDialogTrigger>
                      <AlertDialogContent>
                        <AlertDialogHeader>
                          <AlertDialogTitle>Rotate the key of {k.principal}?</AlertDialogTitle>
                          <AlertDialogDescription>
                            The old key stops working immediately.
                          </AlertDialogDescription>
                        </AlertDialogHeader>
                        <AlertDialogFooter>
                          <AlertDialogCancel>Cancel</AlertDialogCancel>
                          <AlertDialogAction onClick={() => rotate(k)}>
                            Rotate
                          </AlertDialogAction>
                        </AlertDialogFooter>
                      </AlertDialogContent>
                    </AlertDialog>
                    <AlertDialog>
                      <AlertDialogTrigger asChild>
                        <Button variant="ghost" size="sm">
                          Delete
                        </Button>
                      </AlertDialogTrigger>
                      <AlertDialogContent>
                        <AlertDialogHeader>
                          <AlertDialogTitle>Delete {k.principal}?</AlertDialogTitle>
                          <AlertDialogDescription>
                            Its key stops working immediately.
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
