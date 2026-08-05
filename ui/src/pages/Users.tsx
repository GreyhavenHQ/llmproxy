import { useState } from 'react'
import { toast } from 'sonner'
import { api, formatDay, type KeyInfo, type Principal } from '@/lib/api'
import { useAsync } from '@/lib/useAsync'
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

export function Users() {
  const { data, loading, error, reload } = useAsync(() =>
    api.get<{ principals: Principal[] }>('/admin/v1/principals?limit=500'),
  )
  // The principal whose keys are open below the table.
  const [selected, setSelected] = useState<Principal | null>(null)

  return (
    <div className="flex flex-col gap-6">
      <Card>
        <CardHeader>
          <CardTitle className="font-serif">Users and service accounts</CardTitle>
          <CardDescription>
            Everything that can own keys and usage. People appear here after
            their first sign-in; service accounts are created below.
          </CardDescription>
        </CardHeader>
        <CardContent>
          {loading && !data ? (
            <Spinner />
          ) : error ? (
            <p className="text-sm text-destructive">{error}</p>
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Name</TableHead>
                  <TableHead>Kind</TableHead>
                  <TableHead>Role</TableHead>
                  <TableHead className="w-48" />
                </TableRow>
              </TableHeader>
              <TableBody>
                {data?.principals.length === 0 && (
                  <TableRow>
                    <TableCell colSpan={4} className="text-muted-foreground">
                      No principals yet.
                    </TableCell>
                  </TableRow>
                )}
                {data?.principals.map((p) => (
                  <TableRow key={p.id}>
                    <TableCell>{p.name}</TableCell>
                    <TableCell>
                      {p.kind === 'service' ? (
                        <Badge variant="outline">service</Badge>
                      ) : (
                        'user'
                      )}
                    </TableCell>
                    <TableCell>
                      {p.role === 'admin' ? (
                        <Badge variant="secondary">admin</Badge>
                      ) : (
                        'member'
                      )}
                    </TableCell>
                    <TableCell className="text-right">
                      <Button
                        variant={selected?.id === p.id ? 'secondary' : 'ghost'}
                        size="sm"
                        onClick={() =>
                          setSelected(selected?.id === p.id ? null : p)
                        }
                      >
                        Keys
                      </Button>
                      <RevokeSessionsButton principal={p} />
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )}
        </CardContent>
      </Card>

      {selected && <PrincipalKeysCard principal={selected} />}

      <CreateServiceAccountCard
        onCreated={(p) => {
          reload()
          setSelected(p) // jump straight to minting its first key
        }}
      />
    </div>
  )
}

// Sessions die immediately; API keys are untouched. After a group change at
// the IdP this forces the next UI access through a fresh login.
function RevokeSessionsButton({ principal }: { principal: Principal }) {
  const revoke = async () => {
    try {
      const res = await api.post<{ deleted_sessions: number }>(
        `/admin/v1/principals/${principal.id}/revoke-sessions`,
      )
      toast.success(
        `Signed out ${principal.name} (${res.deleted_sessions} session${res.deleted_sessions === 1 ? '' : 's'})`,
      )
    } catch (err) {
      toast.error(errMsg(err))
    }
  }
  return (
    <AlertDialog>
      <AlertDialogTrigger asChild>
        <Button variant="ghost" size="sm">
          Sign out
        </Button>
      </AlertDialogTrigger>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>Sign out {principal.name} everywhere?</AlertDialogTitle>
          <AlertDialogDescription>
            Deletes all browser sessions of {principal.name}; their next UI
            access requires a fresh login (which re-reads group membership).
            API keys keep working.
          </AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel>Cancel</AlertDialogCancel>
          <AlertDialogAction onClick={revoke}>Sign out</AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  )
}

function PrincipalKeysCard({ principal }: { principal: Principal }) {
  const { data, loading, error, reload } = useAsync(
    () =>
      api.get<{ keys: KeyInfo[] }>(
        `/admin/v1/keys?principal=${encodeURIComponent(principal.name)}`,
      ),
    [principal.name],
  )
  const [label, setLabel] = useState('')
  const [creating, setCreating] = useState(false)
  const [newKey, setNewKey] = useState<KeyInfo | null>(null)

  const create = async (e: React.FormEvent) => {
    e.preventDefault()
    setCreating(true)
    try {
      const key = await api.post<KeyInfo>('/admin/v1/keys', {
        principal: principal.name,
        label,
      })
      setNewKey(key)
      setLabel('')
      reload()
    } catch (err) {
      toast.error(errMsg(err))
    } finally {
      setCreating(false)
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
        <CardTitle className="font-serif">Keys of {principal.name}</CardTitle>
        <CardDescription>
          Admin-issued keys behave exactly like self-minted ones: shown once,
          revoked by deletion, usage booked to {principal.name}.
        </CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        <form onSubmit={create} className="flex flex-wrap items-center gap-2">
          <Input
            className="w-64"
            placeholder="Label (e.g. prod)"
            maxLength={120}
            value={label}
            onChange={(e) => setLabel(e.target.value)}
          />
          <Button type="submit" disabled={creating}>
            {creating && <Spinner />}
            Create key
          </Button>
        </form>

        {newKey && (
          <div className="rounded-md border border-primary/40 bg-primary/5 p-4">
            <p className="mb-2 text-sm font-medium">
              New key{newKey.label ? ` "${newKey.label}"` : ''} for{' '}
              {principal.name} — copy it now, it will not be shown again.
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
                <TableHead>Label</TableHead>
                <TableHead>Key</TableHead>
                <TableHead>Created</TableHead>
                <TableHead>Last used</TableHead>
                <TableHead className="w-24" />
              </TableRow>
            </TableHeader>
            <TableBody>
              {data?.keys.length === 0 && (
                <TableRow>
                  <TableCell colSpan={5} className="text-muted-foreground">
                    No keys yet.
                  </TableCell>
                </TableRow>
              )}
              {data?.keys.map((k) => (
                <TableRow key={k.id}>
                  <TableCell>
                    {k.label || <span className="text-muted-foreground">(no label)</span>}
                  </TableCell>
                  <TableCell className="font-mono text-xs">***{k.key_suffix}</TableCell>
                  <TableCell>{formatDay(k.created_at)}</TableCell>
                  <TableCell>{formatDay(k.last_used_at) || 'never'}</TableCell>
                  <TableCell>
                    <AlertDialog>
                      <AlertDialogTrigger asChild>
                        <Button variant="ghost" size="sm">
                          Delete
                        </Button>
                      </AlertDialogTrigger>
                      <AlertDialogContent>
                        <AlertDialogHeader>
                          <AlertDialogTitle>Delete this key?</AlertDialogTitle>
                          <AlertDialogDescription>
                            Anything using{' '}
                            {k.label ? `"${k.label}"` : `***${k.key_suffix}`}{' '}
                            stops working immediately. This cannot be undone.
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

function CreateServiceAccountCard({
  onCreated,
}: {
  onCreated: (p: Principal) => void
}) {
  const [name, setName] = useState('')
  const [role, setRole] = useState('member')
  const [creating, setCreating] = useState(false)

  const create = async (e: React.FormEvent) => {
    e.preventDefault()
    setCreating(true)
    try {
      const p = await api.post<Principal>('/admin/v1/principals', {
        name,
        kind: 'service',
        role,
      })
      toast.success(`Service account "${p.name}" created`)
      setName('')
      setRole('member')
      onCreated(p)
    } catch (err) {
      toast.error(errMsg(err))
    } finally {
      setCreating(false)
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle className="font-serif">Create a service account</CardTitle>
        <CardDescription>
          For workloads rather than people, so their usage is booked under the
          service&apos;s own name. Mint its keys from the list above.
        </CardDescription>
      </CardHeader>
      <CardContent>
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
          <Select value={role} onValueChange={setRole}>
            <SelectTrigger className="w-32">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="member">member</SelectItem>
              <SelectItem value="admin">admin</SelectItem>
            </SelectContent>
          </Select>
          <Button type="submit" disabled={creating}>
            {creating && <Spinner />}
            Create service account
          </Button>
        </form>
      </CardContent>
    </Card>
  )
}
