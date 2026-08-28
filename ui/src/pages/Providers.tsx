import { useState } from 'react'
import { toast } from 'sonner'
import { api, type DiscoveredModel, type Provider } from '@/lib/api'
import { useAsync } from '@/lib/useAsync'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
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
import { Pencil, RefreshCw, Trash2 } from 'lucide-react'

function errMsg(err: unknown): string {
  return err instanceof Error ? err.message : String(err)
}

// The editable operational settings of one provider. Numbers stay strings
// while typed; parsing happens on save.
interface EditState {
  name: string
  base_url: string
  api_key: string // empty = keep the stored credential
  remove_credential: boolean
  verify_tls: boolean
  timeout_connect: string
  timeout_read: string
  max_concurrency: string // empty = unlimited
}

function editStateOf(p: Provider): EditState {
  return {
    name: p.name,
    base_url: p.base_url,
    api_key: '',
    remove_credential: false,
    verify_tls: p.verify_tls,
    timeout_connect: String(p.timeout_connect),
    timeout_read: String(p.timeout_read),
    max_concurrency: p.max_concurrency === null ? '' : String(p.max_concurrency),
  }
}

export function Providers() {
  const { data, loading, error, reload } = useAsync(() =>
    api.get<{ providers: Provider[] }>('/admin/v1/providers'),
  )
  const [form, setForm] = useState({ name: '', base_url: '', api_key: '' })
  const [busy, setBusy] = useState(false)
  const [editing, setEditing] = useState<EditState | null>(null)
  const [saving, setSaving] = useState(false)
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

  const saveEdit = async () => {
    if (!editing) return
    const connect = Number(editing.timeout_connect)
    const read = Number(editing.timeout_read)
    if (!(connect > 0) || !(read > 0)) {
      toast.error('Timeouts must be positive seconds')
      return
    }
    const concurrency = editing.max_concurrency.trim() === '' ? 0 : Number(editing.max_concurrency)
    if (!Number.isInteger(concurrency) || concurrency < 0) {
      toast.error('Max concurrency must be a whole number, or empty for unlimited')
      return
    }
    setSaving(true)
    try {
      await api.patch(`/admin/v1/providers/${encodeURIComponent(editing.name)}`, {
        base_url: editing.base_url.trim(),
        verify_tls: editing.verify_tls,
        timeout_connect: connect,
        timeout_read: read,
        max_concurrency: concurrency, // 0 clears the cap
        ...(editing.api_key !== '' ? { api_key: editing.api_key } : {}),
        ...(editing.remove_credential ? { remove_credential: true } : {}),
      })
      toast.success(`Provider "${editing.name}" updated`)
      setEditing(null)
      reload()
    } catch (err) {
      toast.error(errMsg(err))
    } finally {
      setSaving(false)
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
            The base URL includes <code className="font-mono">/v1</code>.
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
                  <TableHead title="Read timeout · max concurrency">Limits</TableHead>
                  <TableHead>Enabled</TableHead>
                  <TableHead className="w-56" />
                </TableRow>
              </TableHeader>
              <TableBody>
                {data?.providers.length === 0 && (
                  <TableRow>
                    <TableCell colSpan={6} className="text-muted-foreground">
                      No providers registered yet.
                    </TableCell>
                  </TableRow>
                )}
                {data?.providers.map((p) =>
                  editing?.name === p.name ? (
                    <TableRow key={p.name} className="bg-muted/30 hover:bg-muted/30">
                      <TableCell className="align-top font-medium">{p.name}</TableCell>
                      <TableCell colSpan={5}>
                        <div className="flex flex-col gap-4 py-1">
                          <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
                            <div className="flex flex-col gap-2">
                              <Label htmlFor="e-url">Base URL</Label>
                              <Input
                                id="e-url"
                                value={editing.base_url}
                                onChange={(e) =>
                                  setEditing({ ...editing, base_url: e.target.value })
                                }
                              />
                            </div>
                            <div className="flex flex-col gap-2">
                              <Label htmlFor="e-key">Upstream API key</Label>
                              <Input
                                id="e-key"
                                type="password"
                                autoComplete="off"
                                placeholder={p.has_credential ? 'unchanged' : 'none'}
                                value={editing.api_key}
                                disabled={editing.remove_credential}
                                onChange={(e) =>
                                  setEditing({ ...editing, api_key: e.target.value })
                                }
                              />
                              {p.has_credential && (
                                <label className="flex items-center gap-2 text-xs text-muted-foreground">
                                  <Checkbox
                                    checked={editing.remove_credential}
                                    onCheckedChange={(v) =>
                                      setEditing({ ...editing, remove_credential: v === true })
                                    }
                                  />
                                  Remove the stored key
                                </label>
                              )}
                            </div>
                            <div className="flex flex-col gap-2">
                              <Label htmlFor="e-conc">Max concurrency</Label>
                              <Input
                                id="e-conc"
                                inputMode="numeric"
                                placeholder="unlimited"
                                value={editing.max_concurrency}
                                onChange={(e) =>
                                  setEditing({ ...editing, max_concurrency: e.target.value })
                                }
                              />
                            </div>
                            <div className="flex flex-col gap-2">
                              <Label htmlFor="e-connect">Connect timeout (s)</Label>
                              <Input
                                id="e-connect"
                                inputMode="decimal"
                                value={editing.timeout_connect}
                                onChange={(e) =>
                                  setEditing({ ...editing, timeout_connect: e.target.value })
                                }
                              />
                            </div>
                            <div className="flex flex-col gap-2">
                              <Label htmlFor="e-read">Read timeout (s)</Label>
                              <Input
                                id="e-read"
                                inputMode="decimal"
                                value={editing.timeout_read}
                                onChange={(e) =>
                                  setEditing({ ...editing, timeout_read: e.target.value })
                                }
                              />
                              <p className="text-xs text-muted-foreground">
                                Caps the wait for a response; whole request on
                                non-streaming calls.
                              </p>
                            </div>
                            <div className="flex flex-col gap-2">
                              <Label htmlFor="e-tls">Verify TLS</Label>
                              <Switch
                                id="e-tls"
                                checked={editing.verify_tls}
                                onCheckedChange={(v) =>
                                  setEditing({ ...editing, verify_tls: v })
                                }
                              />
                            </div>
                          </div>
                          <div className="flex gap-2">
                            <Button size="sm" disabled={saving} onClick={saveEdit}>
                              {saving && <Spinner />}
                              Save
                            </Button>
                            <Button
                              variant="outline"
                              size="sm"
                              onClick={() => setEditing(null)}
                            >
                              Cancel
                            </Button>
                          </div>
                        </div>
                      </TableCell>
                    </TableRow>
                  ) : (
                    <TableRow key={p.name}>
                      <TableCell className="font-medium wrap-anywhere">{p.name}</TableCell>
                      <TableCell className="font-mono text-xs wrap-anywhere">{p.base_url}</TableCell>
                      <TableCell>
                        {p.has_credential ? (
                          <Badge variant="secondary">set</Badge>
                        ) : (
                          <Badge variant="muted">none</Badge>
                        )}
                      </TableCell>
                      <TableCell
                        className="whitespace-nowrap text-xs text-muted-foreground"
                        title="Read timeout · max concurrency"
                      >
                        {p.timeout_read}s ·{' '}
                        {p.max_concurrency === null ? 'unlimited' : p.max_concurrency}
                      </TableCell>
                      <TableCell>
                        <Switch
                          checked={p.enabled}
                          onCheckedChange={() => toggle(p)}
                          aria-label={`Enable ${p.name}`}
                        />
                      </TableCell>
                      <TableCell className="flex items-center justify-end gap-1 whitespace-nowrap">
                        <Button
                          variant="outline"
                          size="sm"
                          disabled={discovering === p.name}
                          onClick={() => discover(p.name)}
                        >
                          {discovering === p.name ? <Spinner /> : <RefreshCw />}
                          Discover models
                        </Button>
                        <Button
                          variant="ghost"
                          size="icon-sm"
                          aria-label={`Edit ${p.name}`}
                          onClick={() => setEditing(editStateOf(p))}
                        >
                          <Pencil />
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
                  ),
                )}
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
                    <TableCell className="font-mono text-xs wrap-anywhere">{m.upstream_name}</TableCell>
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
