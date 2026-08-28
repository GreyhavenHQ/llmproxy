import { useState } from 'react'
import { toast } from 'sonner'
import { api, formatDay, type KeyInfo, type RelayTokenInfo } from '@/lib/api'
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

export function Keys() {
  return (
    <div className="flex flex-col gap-6">
      <ApiKeysCard />
      <RelayTokensCard />
    </div>
  )
}

function ApiKeysCard() {
  const { data, loading, error, reload } = useAsync(() =>
    api.get<{ keys: KeyInfo[] }>('/my/keys'),
  )
  const [label, setLabel] = useState('')
  const [creating, setCreating] = useState(false)
  const [newKey, setNewKey] = useState<KeyInfo | null>(null)

  const create = async (e: React.FormEvent) => {
    e.preventDefault()
    setCreating(true)
    try {
      const key = await api.post<KeyInfo>('/my/keys', { label })
      setNewKey(key)
      setLabel('')
      reload()
    } catch (err) {
      toast.error(String(err instanceof Error ? err.message : err))
    } finally {
      setCreating(false)
    }
  }

  const remove = async (id: string) => {
    try {
      await api.del(`/my/keys/${id}`)
      toast.success('Key deleted')
      reload()
    } catch (err) {
      toast.error(String(err instanceof Error ? err.message : err))
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle className="font-serif">Your API keys</CardTitle>
        <CardDescription>
          Shown exactly once at creation; only a keyed hash is stored.
        </CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        <form onSubmit={create} className="flex flex-wrap items-center gap-2">
          <Input
            className="w-64"
            placeholder="Label (e.g. laptop)"
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
              New key{newKey.label ? ` "${newKey.label}"` : ''} — copy it now,
              it will not be shown again.
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

function RelayTokensCard() {
  const { data, loading, error, reload } = useAsync(() =>
    api.get<{ relay_tokens: RelayTokenInfo[] }>('/my/relay-tokens'),
  )
  const [label, setLabel] = useState('')
  const [creating, setCreating] = useState(false)
  const [newToken, setNewToken] = useState<RelayTokenInfo | null>(null)

  const create = async (e: React.FormEvent) => {
    e.preventDefault()
    setCreating(true)
    try {
      const token = await api.post<RelayTokenInfo>('/my/relay-tokens', { label })
      setNewToken(token)
      setLabel('')
      reload()
    } catch (err) {
      toast.error(String(err instanceof Error ? err.message : err))
    } finally {
      setCreating(false)
    }
  }

  const remove = async (id: string) => {
    try {
      await api.del(`/my/relay-tokens/${id}`)
      toast.success('Relay token deleted')
      reload()
    } catch (err) {
      toast.error(String(err instanceof Error ? err.message : err))
    }
  }

  const setupCommand = (token: string) =>
    `export ANTHROPIC_BASE_URL=${window.location.origin}/transparent/anthropic/${token}\nclaude`

  return (
    <Card>
      <CardHeader>
        <CardTitle className="font-serif">Relay tokens</CardTitle>
        <CardDescription>
          Tracks your usage on the transparent Anthropic relay. Your Anthropic
          credentials pass through untouched; the token grants no proxy
          access.
        </CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        <form onSubmit={create} className="flex flex-wrap items-center gap-2">
          <Input
            className="w-64"
            placeholder="Label (e.g. claude-code laptop)"
            maxLength={120}
            value={label}
            onChange={(e) => setLabel(e.target.value)}
          />
          <Button type="submit" disabled={creating}>
            {creating && <Spinner />}
            Create relay token
          </Button>
        </form>

        {newToken?.token && (
          <div className="rounded-md border border-primary/40 bg-primary/5 p-4">
            <p className="mb-2 text-sm font-medium">
              New relay token{newToken.label ? ` "${newToken.label}"` : ''} —
              copy the setup command now, the token will not be shown again.
            </p>
            <div className="flex items-start gap-2">
              <pre className="flex-1 overflow-x-auto rounded bg-background px-2 py-1.5 font-mono text-sm">
                {setupCommand(newToken.token)}
              </pre>
              <Button
                variant="outline"
                size="icon-sm"
                aria-label="Copy setup command"
                onClick={() => {
                  navigator.clipboard.writeText(setupCommand(newToken.token ?? ''))
                  toast.success('Copied to clipboard')
                }}
              >
                <Copy />
              </Button>
            </div>
            <p className="mt-2 text-xs text-muted-foreground">
              Make it permanent via your shell profile or {'"env"'} in
              ~/.claude/settings.json.
            </p>
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
                <TableHead>Token</TableHead>
                <TableHead>Created</TableHead>
                <TableHead>Last used</TableHead>
                <TableHead className="w-24" />
              </TableRow>
            </TableHeader>
            <TableBody>
              {data?.relay_tokens.length === 0 && (
                <TableRow>
                  <TableCell colSpan={5} className="text-muted-foreground">
                    No relay tokens yet.
                  </TableCell>
                </TableRow>
              )}
              {data?.relay_tokens.map((rt) => (
                <TableRow key={rt.id}>
                  <TableCell className="wrap-anywhere">
                    {rt.label || <span className="text-muted-foreground">(no label)</span>}
                  </TableCell>
                  <TableCell className="font-mono text-xs">***{rt.token_suffix}</TableCell>
                  <TableCell className="whitespace-nowrap">{formatDay(rt.created_at)}</TableCell>
                  <TableCell className="whitespace-nowrap">
                    {formatDay(rt.last_used_at) || 'never'}
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
                          <AlertDialogTitle>Delete this relay token?</AlertDialogTitle>
                          <AlertDialogDescription>
                            Anything relaying through{' '}
                            {rt.label ? `"${rt.label}"` : `***${rt.token_suffix}`}{' '}
                            stops working immediately. This cannot be undone.
                          </AlertDialogDescription>
                        </AlertDialogHeader>
                        <AlertDialogFooter>
                          <AlertDialogCancel>Cancel</AlertDialogCancel>
                          <AlertDialogAction onClick={() => remove(rt.id)}>
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
