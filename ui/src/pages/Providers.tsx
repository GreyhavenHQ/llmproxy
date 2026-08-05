import { useState } from 'react'
import { toast } from 'sonner'
import { api, type DiscoveredModel, type Provider } from '@/lib/api'
import { useAsync } from '@/lib/useAsync'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Spinner } from '@/components/ui/spinner'
import { Switch } from '@/components/ui/switch'
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
import { RefreshCw, Trash2 } from 'lucide-react'

function errMsg(err: unknown): string {
  return err instanceof Error ? err.message : String(err)
}

export function Providers() {
  const { data, loading, error, reload } = useAsync(() =>
    api.get<{ providers: Provider[] }>('/admin/v1/providers'),
  )
  const [form, setForm] = useState({ name: '', base_url: '', api_key: '' })
  const [busy, setBusy] = useState(false)
  const [discovering, setDiscovering] = useState<string | null>(null)
  const [discovered, setDiscovered] = useState<{
    provider: string
    models: DiscoveredModel[]
  } | null>(null)

  const create = async (e: React.FormEvent) => {
    e.preventDefault()
    setBusy(true)
    try {
      await api.post('/admin/v1/providers', {
        name: form.name.trim(),
        wire_format: 'openai',
        base_url: form.base_url.trim(),
        ...(form.api_key !== '' ? { api_key: form.api_key } : {}),
      })
      toast.success(`Provider "${form.name.trim()}" registered`)
      setForm({ name: '', base_url: '', api_key: '' })
      reload()
    } catch (err) {
      toast.error(errMsg(err))
    } finally {
      setBusy(false)
    }
  }

  const toggle = async (p: Provider) => {
    try {
      await api.patch(`/admin/v1/providers/${encodeURIComponent(p.name)}`, {
        enabled: !p.enabled,
      })
      reload()
    } catch (err) {
      toast.error(errMsg(err))
    }
  }

  const remove = async (name: string) => {
    try {
      await api.del(`/admin/v1/providers/${encodeURIComponent(name)}`)
      toast.success(`Provider "${name}" deleted`)
      if (discovered?.provider === name) setDiscovered(null)
      reload()
    } catch (err) {
      toast.error(errMsg(err))
    }
  }

  const discover = async (name: string) => {
    setDiscovering(name)
    try {
      const result = await api.get<{ models: DiscoveredModel[] }>(
        `/admin/v1/providers/${encodeURIComponent(name)}/discover`,
      )
      setDiscovered({ provider: name, models: result.models })
    } catch (err) {
      toast.error(errMsg(err))
    } finally {
      setDiscovering(null)
    }
  }

  const bind = async (provider: string, upstreamName: string) => {
    try {
      await api.post('/admin/v1/models', {
        alias: upstreamName,
        provider,
        upstream_name: upstreamName,
        capabilities: ['chat', 'chat_stream'],
      })
      toast.success(`Bound "${upstreamName}" (chat capabilities; adjust in Models)`)
      discover(provider)
    } catch (err) {
      toast.error(errMsg(err))
    }
  }

  return (
    <div className="flex flex-col gap-6">
      <Card>
        <CardHeader>
          <CardTitle className="font-serif">Register a provider</CardTitle>
          <CardDescription>
            An upstream OpenAI-compatible endpoint (vLLM, SGLang, OpenAI, ...).
            The base URL includes <code className="font-mono">/v1</code>. The
            upstream key is stored encrypted and never shown again.
          </CardDescription>
        </CardHeader>
        <CardContent>
          <form onSubmit={create} className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
            <div className="flex flex-col gap-2">
              <Label htmlFor="p-name">Name</Label>
              <Input
                id="p-name"
                placeholder="vllm-1"
                value={form.name}
                onChange={(e) => setForm({ ...form, name: e.target.value })}
                required
              />
            </div>
            <div className="flex flex-col gap-2">
              <Label htmlFor="p-url">Base URL</Label>
              <Input
                id="p-url"
                placeholder="http://10.0.0.5:8000/v1"
                value={form.base_url}
                onChange={(e) => setForm({ ...form, base_url: e.target.value })}
                required
              />
            </div>
            <div className="flex flex-col gap-2">
              <Label htmlFor="p-key">Upstream API key (optional)</Label>
              <Input
                id="p-key"
                type="password"
                autoComplete="off"
                value={form.api_key}
                onChange={(e) => setForm({ ...form, api_key: e.target.value })}
              />
            </div>
            <div className="flex items-end">
              <Button type="submit" disabled={busy}>
                {busy && <Spinner />}
                Register
              </Button>
            </div>
          </form>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle className="font-serif">Providers</CardTitle>
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
                  <TableHead>Base URL</TableHead>
                  <TableHead>Credential</TableHead>
                  <TableHead>Enabled</TableHead>
                  <TableHead className="w-56" />
                </TableRow>
              </TableHeader>
              <TableBody>
                {data?.providers.length === 0 && (
                  <TableRow>
                    <TableCell colSpan={5} className="text-muted-foreground">
                      No providers registered yet.
                    </TableCell>
                  </TableRow>
                )}
                {data?.providers.map((p) => (
                  <TableRow key={p.name}>
                    <TableCell className="font-medium">{p.name}</TableCell>
                    <TableCell className="font-mono text-xs">{p.base_url}</TableCell>
                    <TableCell>
                      {p.has_credential ? (
                        <Badge variant="secondary">set</Badge>
                      ) : (
                        <Badge variant="muted">none</Badge>
                      )}
                    </TableCell>
                    <TableCell>
                      <Switch
                        checked={p.enabled}
                        onCheckedChange={() => toggle(p)}
                        aria-label={`Enable ${p.name}`}
                      />
                    </TableCell>
                    <TableCell className="flex items-center justify-end gap-1">
                      <Button
                        variant="outline"
                        size="sm"
                        disabled={discovering === p.name}
                        onClick={() => discover(p.name)}
                      >
                        {discovering === p.name ? <Spinner /> : <RefreshCw />}
                        Discover models
                      </Button>
                      <AlertDialog>
                        <AlertDialogTrigger asChild>
                          <Button variant="ghost" size="icon-sm" aria-label={`Delete ${p.name}`}>
                            <Trash2 />
                          </Button>
                        </AlertDialogTrigger>
                        <AlertDialogContent>
                          <AlertDialogHeader>
                            <AlertDialogTitle>Delete provider "{p.name}"?</AlertDialogTitle>
                            <AlertDialogDescription>
                              All model bindings on this provider are deleted
                              with it, and calls to those aliases start
                              failing immediately.
                            </AlertDialogDescription>
                          </AlertDialogHeader>
                          <AlertDialogFooter>
                            <AlertDialogCancel>Cancel</AlertDialogCancel>
                            <AlertDialogAction onClick={() => remove(p.name)}>
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

      {discovered && (
        <Card>
          <CardHeader>
            <CardTitle className="font-serif">
              Models on "{discovered.provider}"
            </CardTitle>
            <CardDescription>
              Read from the upstream's own model list. Binding uses the
              upstream name as the alias with chat capabilities; refine
              aliases and capabilities in the Models tab.
            </CardDescription>
          </CardHeader>
          <CardContent>
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Upstream name</TableHead>
                  <TableHead>Bound alias</TableHead>
                  <TableHead className="w-24" />
                </TableRow>
              </TableHeader>
              <TableBody>
                {discovered.models.length === 0 && (
                  <TableRow>
                    <TableCell colSpan={3} className="text-muted-foreground">
                      The upstream reported no models.
                    </TableCell>
                  </TableRow>
                )}
                {discovered.models.map((m) => (
                  <TableRow key={m.upstream_name}>
                    <TableCell className="font-mono text-xs">{m.upstream_name}</TableCell>
                    <TableCell>
                      {m.bound_alias ? (
                        <Badge variant="secondary">{m.bound_alias}</Badge>
                      ) : (
                        <span className="text-muted-foreground">not bound</span>
                      )}
                    </TableCell>
                    <TableCell>
                      {!m.bound_alias && (
                        <Button
                          variant="outline"
                          size="sm"
                          onClick={() => bind(discovered.provider, m.upstream_name)}
                        >
                          Bind
                        </Button>
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
