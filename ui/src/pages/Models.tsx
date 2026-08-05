import { useEffect, useRef, useState } from 'react'
import { toast } from 'sonner'
import { api, type DiscoveredModel, type Model, type Provider } from '@/lib/api'
import { useAsync } from '@/lib/useAsync'
import { Combobox, type ComboboxOption } from '@/components/Combobox'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
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
import { Tabs, TabsList, TabsTrigger } from '@/components/ui/tabs'
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
import { Pencil, Trash2 } from 'lucide-react'

const ALL_CAPABILITIES = ['chat', 'chat_stream', 'completions', 'embeddings']

// The units the proxy meters. Prices are per million units and belong to the
// model: a unit left blank is unpriced, never zero.
const PRICE_UNITS = [
  { unit: 'input_tokens', label: 'Input $/M', short: 'in' },
  { unit: 'output_tokens', label: 'Output $/M', short: 'out' },
  { unit: 'cached_input_tokens', label: 'Cached input $/M', short: 'cached' },
]

type PriceForm = Record<string, string>

function errMsg(err: unknown): string {
  return err instanceof Error ? err.message : String(err)
}

function toPriceForm(prices: Record<string, number>): PriceForm {
  const form: PriceForm = {}
  for (const { unit } of PRICE_UNITS) {
    if (prices[unit] !== undefined) form[unit] = String(prices[unit])
  }
  return form
}

// Returns the complete price set for the model, or null if a field is not a
// non-negative number. Blank fields are left out, which clears them.
function fromPriceForm(form: PriceForm): Record<string, number> | null {
  const prices: Record<string, number> = {}
  for (const { unit } of PRICE_UNITS) {
    const raw = (form[unit] ?? '').trim()
    if (raw === '') continue
    const value = Number(raw)
    if (!isFinite(value) || value < 0) return null
    prices[unit] = value
  }
  return prices
}

function PriceFields({
  id,
  value,
  onChange,
  inherited,
}: {
  id: string
  value: PriceForm
  onChange: (form: PriceForm) => void
  // An alias with no price of its own charges its target's; setting one here
  // overrides that.
  inherited?: boolean
}) {
  return (
    <>
      {PRICE_UNITS.map(({ unit, label }) => (
        <div key={unit} className="flex flex-col gap-2">
          <Label htmlFor={`${id}-${unit}`}>{label}</Label>
          <Input
            id={`${id}-${unit}`}
            type="number"
            min="0"
            step="any"
            placeholder={inherited ? 'inherited' : 'unpriced'}
            value={value[unit] ?? ''}
            onChange={(e) => onChange({ ...value, [unit]: e.target.value })}
          />
        </div>
      ))}
    </>
  )
}

function PriceSummary({ model }: { model: Model }) {
  const parts = PRICE_UNITS.filter(({ unit }) => model.pricing[unit] !== undefined).map(
    ({ unit, short }) => `${short} ${model.pricing[unit]}`,
  )
  if (parts.length === 0) {
    return <span className="text-muted-foreground">unpriced</span>
  }
  return (
    <span className="whitespace-nowrap tabular-nums">
      {parts.join(' · ')}
      {model.pricing_inherited && (
        <Badge variant="muted" className="ml-2" title="Priced under the upstream model name">
          inherited
        </Badge>
      )}
    </span>
  )
}

function CapabilityChecks({
  value,
  onChange,
}: {
  value: string[]
  onChange: (caps: string[]) => void
}) {
  return (
    <>
      {ALL_CAPABILITIES.map((cap) => (
        <label key={cap} className="flex items-center gap-2 text-sm">
          <Checkbox
            checked={value.includes(cap)}
            onCheckedChange={(c) =>
              onChange(c === true ? [...value, cap] : value.filter((x) => x !== cap))
            }
          />
          {cap}
        </label>
      ))}
    </>
  )
}

// A model either routes to a provider's model or is another name for a model
// already bound; an alias inherits everything but its name and its price.
type Kind = 'provider' | 'alias'

interface ModelForm {
  kind: Kind
  alias: string
  provider: string
  upstream_name: string
  target: string
  capabilities: string[]
  prices: PriceForm
}

interface EditState extends ModelForm {
  original: string // the alias the row is stored under, before any rename
}

// Only a model with a provider of its own can be pointed at: aliases are one
// hop, so an alias never targets another alias.
function targetOptions(models: Model[], self?: string): ComboboxOption[] {
  return models
    .filter((m) => m.target === null && m.alias !== self)
    .map((m) => ({ value: m.alias, hint: `${m.provider} · ${m.upstream_name}` }))
}

// Both kinds stay visible side by side; the fields below follow the choice.
function KindSwitch({ value, onChange }: { value: Kind; onChange: (kind: Kind) => void }) {
  return (
    <Tabs value={value} onValueChange={(v) => onChange(v as Kind)}>
      <TabsList>
        <TabsTrigger value="provider">Provider model</TabsTrigger>
        <TabsTrigger value="alias">Alias &rarr; model</TabsTrigger>
      </TabsList>
    </Tabs>
  )
}

// Discovery is a live call to the provider, so cache it per provider for the
// life of the page: the picker is opened repeatedly while binding models.
const discoveryCache = new Map<string, DiscoveredModel[]>()

function useDiscovery(provider: string) {
  const [models, setModels] = useState<DiscoveredModel[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    if (provider === '') {
      setModels([])
      setError(null)
      return
    }
    const cached = discoveryCache.get(provider)
    if (cached) {
      setModels(cached)
      setError(null)
      return
    }
    let live = true
    setLoading(true)
    setError(null)
    api
      .get<{ models: DiscoveredModel[] }>(
        `/admin/v1/providers/${encodeURIComponent(provider)}/discover`,
      )
      .then((data) => {
        if (!live) return
        discoveryCache.set(provider, data.models)
        setModels(data.models)
      })
      .catch((err: unknown) => live && setError(errMsg(err)))
      .finally(() => live && setLoading(false))
    return () => {
      live = false
    }
  }, [provider])

  const options: ComboboxOption[] = models.map((m) => ({
    value: m.upstream_name,
    hint: m.bound_alias ? `bound to ${m.bound_alias}` : undefined,
  }))
  // The picker is a suggestion list, never a gate: say why it is empty and
  // let the name be typed.
  const note = !provider
    ? 'Select a provider to list its models.'
    : error
      ? `Could not list this provider's models (${error}). Type the name.`
      : loading
        ? 'Asking the provider what it serves…'
        : models.length === 0
          ? 'The provider listed no models. Type the name.'
          : undefined
  return { options, loading, note }
}

export function Models() {
  const models = useAsync(() => api.get<{ models: Model[] }>('/admin/v1/models'))
  const providers = useAsync(() =>
    api.get<{ providers: Provider[] }>('/admin/v1/providers'),
  )
  const [form, setForm] = useState<ModelForm>({
    kind: 'provider',
    alias: '',
    provider: '',
    upstream_name: '',
    target: '',
    capabilities: ['chat', 'chat_stream'],
    prices: {},
  })
  const [busy, setBusy] = useState(false)
  const [editing, setEditing] = useState<EditState | null>(null)
  const newDiscovery = useDiscovery(form.kind === 'provider' ? form.provider : '')
  const editDiscovery = useDiscovery(editing?.kind === 'provider' ? editing.provider : '')
  const allModels = models.data?.models ?? []

  // When a provider is picked and the upstream field is empty, prefill it
  // with the first model discovery returned (preferring one not yet bound).
  // Once per provider pick, so clearing the field does not refill it.
  const prefilledFor = useRef('')
  useEffect(() => {
    if (form.kind !== 'provider' || form.provider === '') return
    if (prefilledFor.current === form.provider) return
    if (newDiscovery.loading || newDiscovery.options.length === 0) return
    prefilledFor.current = form.provider
    if (form.upstream_name.trim() === '') {
      const first = newDiscovery.options.find((o) => !o.hint) ?? newDiscovery.options[0]
      setForm((f) => ({ ...f, upstream_name: first.value }))
    }
  }, [form.kind, form.provider, form.upstream_name, newDiscovery.loading, newDiscovery.options])

  // The two kinds send different bodies; everything else is shared.
  const routing = (f: ModelForm) =>
    f.kind === 'alias'
      ? { target: f.target.trim() }
      : {
          provider: f.provider,
          upstream_name: f.upstream_name.trim(),
          capabilities: f.capabilities,
        }

  const invalid = (f: ModelForm): string | null => {
    if (f.kind === 'alias') {
      if (f.target.trim() === '') return 'Pick the model this one points at'
      if (f.alias.trim() === '') return 'An alias for another model needs its own name'
      return null
    }
    if (f.upstream_name.trim() === '') return 'Upstream model name is required'
    if (f.capabilities.length === 0) return 'Pick at least one capability'
    return null
  }

  const create = async (e: React.FormEvent) => {
    e.preventDefault()
    const problem = invalid(form)
    if (problem) {
      toast.error(problem)
      return
    }
    const prices = fromPriceForm(form.prices)
    if (prices === null) {
      toast.error('Prices must be non-negative numbers')
      return
    }
    // An empty alias means "serve it under the upstream's own name".
    const alias = form.alias.trim() || form.upstream_name.trim()
    setBusy(true)
    try {
      await api.post('/admin/v1/models', {
        alias,
        ...routing(form),
        pricing: Object.keys(prices).length > 0 ? prices : undefined,
      })
      toast.success(`Model "${alias}" bound`)
      setForm({ ...form, alias: '', upstream_name: '', target: '', prices: {} })
      models.reload()
    } catch (err) {
      toast.error(errMsg(err))
    } finally {
      setBusy(false)
    }
  }

  const saveEdit = async () => {
    if (!editing) return
    if (editing.alias.trim() === '') {
      toast.error('Alias is required')
      return
    }
    const problem = invalid(editing)
    if (problem) {
      toast.error(problem)
      return
    }
    const prices = fromPriceForm(editing.prices)
    if (prices === null) {
      toast.error('Prices must be non-negative numbers')
      return
    }
    try {
      await api.patch(`/admin/v1/models/${encodeURIComponent(editing.original)}`, {
        alias: editing.alias.trim(),
        // An empty target turns an alias back into its own binding, which is
        // why the provider fields ride along.
        target: editing.kind === 'alias' ? editing.target.trim() : '',
        ...(editing.kind === 'alias' ? {} : routing(editing)),
        pricing: prices,
      })
      toast.success(
        editing.alias.trim() === editing.original
          ? `Model "${editing.original}" updated`
          : `Model "${editing.original}" renamed to "${editing.alias.trim()}"`,
      )
      setEditing(null)
      models.reload()
    } catch (err) {
      toast.error(errMsg(err))
    }
  }

  const remove = async (alias: string) => {
    try {
      await api.del(`/admin/v1/models/${encodeURIComponent(alias)}`)
      toast.success(`Model "${alias}" deleted`)
      models.reload()
    } catch (err) {
      toast.error(errMsg(err))
    }
  }

  const nameField = (
    <div className="flex flex-col gap-2">
      <Label htmlFor="m-alias">{form.kind === 'alias' ? 'Name' : 'Alias'}</Label>
      <Input
        id="m-alias"
        placeholder={
          form.kind === 'alias'
            ? 'a new name for it'
            : form.upstream_name.trim() || 'same as the upstream name'
        }
        value={form.alias}
        onChange={(e) => setForm({ ...form, alias: e.target.value })}
      />
    </div>
  )

  return (
    <div className="flex flex-col gap-6">
      <Card>
        <CardHeader>
          <CardTitle className="font-serif">Bind a model</CardTitle>
          <CardDescription>
            A model name is what callers pass as <code className="font-mono">model</code>;
            names are globally unique and serve as soon as they exist (disable
            the provider to take its models offline). It either points at a
            provider's model, or at another model already bound, in which case
            it inherits that one's provider, capabilities and prices. Prices
            are per million units: a unit left blank records usage as unpriced
            rather than free.
          </CardDescription>
        </CardHeader>
        <CardContent>
          <form onSubmit={create} className="flex flex-col gap-4">
            <KindSwitch value={form.kind} onChange={(kind) => setForm({ ...form, kind })} />
            {/* In alias mode the new name comes first; the optional alias of a
                provider binding stays last, after what it binds. */}
            <div className="grid gap-4 sm:grid-cols-3">
              {form.kind === 'alias' && nameField}
              {form.kind === 'provider' ? (
                <>
                  <div className="flex flex-col gap-2">
                    <Label>Provider</Label>
                    <Select
                      value={form.provider}
                      onValueChange={(v) => setForm({ ...form, provider: v })}
                      required
                    >
                      <SelectTrigger className="w-full">
                        <SelectValue placeholder="Select a provider" />
                      </SelectTrigger>
                      <SelectContent>
                        {providers.data?.providers.map((p) => (
                          <SelectItem key={p.name} value={p.name}>
                            {p.name}
                          </SelectItem>
                        ))}
                      </SelectContent>
                    </Select>
                  </div>
                  <div className="flex flex-col gap-2">
                    <Label htmlFor="m-upstream">Upstream model name</Label>
                    <Combobox
                      id="m-upstream"
                      value={form.upstream_name}
                      onChange={(v) => setForm({ ...form, upstream_name: v })}
                      {...newDiscovery}
                    />
                  </div>
                </>
              ) : (
                <div className="flex flex-col gap-2 sm:col-span-2">
                  <Label htmlFor="m-target">Model it points at</Label>
                  <Combobox
                    id="m-target"
                    value={form.target}
                    onChange={(v) => setForm({ ...form, target: v })}
                    options={targetOptions(allModels)}
                    note={
                      allModels.some((m) => m.target === null)
                        ? undefined
                        : 'Bind a provider model first; an alias points at one of those.'
                    }
                  />
                </div>
              )}
              {form.kind === 'provider' && nameField}
            </div>
            <div className="grid gap-4 sm:grid-cols-3">
              <PriceFields
                id="new"
                value={form.prices}
                onChange={(prices) => setForm({ ...form, prices })}
                inherited={form.kind === 'alias'}
              />
            </div>
            <div className="flex flex-wrap items-center gap-x-6 gap-y-2">
              {form.kind === 'provider' ? (
                <CapabilityChecks
                  value={form.capabilities}
                  onChange={(caps) => setForm({ ...form, capabilities: caps })}
                />
              ) : (
                <span className="text-sm text-muted-foreground">
                  Capabilities are inherited from the model it points at.
                </span>
              )}
              <Button
                type="submit"
                disabled={
                  busy ||
                  (form.kind === 'provider'
                    ? form.provider === '' || form.upstream_name.trim() === ''
                    : form.target.trim() === '' || form.alias.trim() === '')
                }
              >
                {busy && <Spinner />}
                Bind model
              </Button>
            </div>
          </form>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle className="font-serif">Models</CardTitle>
        </CardHeader>
        <CardContent>
          {models.loading && !models.data ? (
            <Spinner />
          ) : models.error ? (
            <p className="text-sm text-destructive">{models.error}</p>
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Alias</TableHead>
                  <TableHead>Provider</TableHead>
                  <TableHead>Upstream name</TableHead>
                  <TableHead>Capabilities</TableHead>
                  <TableHead>Pricing ($/M)</TableHead>
                  <TableHead className="w-24" />
                </TableRow>
              </TableHeader>
              <TableBody>
                {models.data?.models.length === 0 && (
                  <TableRow>
                    <TableCell colSpan={6} className="text-muted-foreground">
                      No model bindings yet. Register a provider first, then
                      bind models here or from provider discovery.
                    </TableCell>
                  </TableRow>
                )}
                {models.data?.models.map((m) =>
                  editing?.original === m.alias ? (
                    // Editing takes the whole row: upstream target,
                    // capabilities and prices are one form.
                    <TableRow key={m.alias}>
                      <TableCell colSpan={6}>
                        <div className="flex flex-col gap-4 py-2">
                          <KindSwitch
                            value={editing.kind}
                            onChange={(kind) => setEditing({ ...editing, kind })}
                          />
                          <div className="grid gap-4 sm:grid-cols-3">
                            {editing.kind === 'provider' ? (
                              <>
                                <div className="flex flex-col gap-2">
                                  <Label>Provider</Label>
                                  <Select
                                    value={editing.provider}
                                    onValueChange={(v) =>
                                      setEditing({ ...editing, provider: v })
                                    }
                                  >
                                    <SelectTrigger className="w-full">
                                      <SelectValue />
                                    </SelectTrigger>
                                    <SelectContent>
                                      {providers.data?.providers.map((p) => (
                                        <SelectItem key={p.name} value={p.name}>
                                          {p.name}
                                        </SelectItem>
                                      ))}
                                    </SelectContent>
                                  </Select>
                                </div>
                                <div className="flex flex-col gap-2">
                                  <Label htmlFor="edit-upstream">Upstream model name</Label>
                                  <Combobox
                                    id="edit-upstream"
                                    value={editing.upstream_name}
                                    onChange={(v) =>
                                      setEditing({ ...editing, upstream_name: v })
                                    }
                                    {...editDiscovery}
                                  />
                                </div>
                              </>
                            ) : (
                              <div className="flex flex-col gap-2 sm:col-span-2">
                                <Label htmlFor="edit-target">Model it points at</Label>
                                <Combobox
                                  id="edit-target"
                                  value={editing.target}
                                  onChange={(v) => setEditing({ ...editing, target: v })}
                                  options={targetOptions(allModels, editing.original)}
                                />
                              </div>
                            )}
                            <div className="flex flex-col gap-2">
                              <Label htmlFor="edit-alias">Name</Label>
                              <Input
                                id="edit-alias"
                                value={editing.alias}
                                onChange={(e) =>
                                  setEditing({ ...editing, alias: e.target.value })
                                }
                              />
                            </div>
                          </div>
                          <div className="grid gap-4 sm:grid-cols-3">
                            <PriceFields
                              id="edit"
                              value={editing.prices}
                              onChange={(prices) => setEditing({ ...editing, prices })}
                              inherited={editing.kind === 'alias'}
                            />
                          </div>
                          <div className="flex flex-wrap items-center gap-x-6 gap-y-2">
                            {editing.kind === 'provider' ? (
                              <CapabilityChecks
                                value={editing.capabilities}
                                onChange={(caps) =>
                                  setEditing({ ...editing, capabilities: caps })
                                }
                              />
                            ) : (
                              <span className="text-sm text-muted-foreground">
                                Capabilities are inherited from the model it points at.
                              </span>
                            )}
                            <div className="flex items-center gap-2">
                              <Button size="sm" onClick={saveEdit}>
                                Save
                              </Button>
                              <Button
                                variant="ghost"
                                size="sm"
                                onClick={() => setEditing(null)}
                              >
                                Cancel
                              </Button>
                            </div>
                          </div>
                        </div>
                      </TableCell>
                    </TableRow>
                  ) : (
                    <TableRow key={m.alias}>
                      <TableCell className="font-medium">{m.alias}</TableCell>
                      <TableCell>{m.provider}</TableCell>
                      <TableCell className="font-mono text-xs">
                        {m.target ? (
                          // An alias shows what it points at, and under it
                          // where that lands.
                          <span className="flex flex-col">
                            <span>→ {m.target}</span>
                            <span className="text-muted-foreground">{m.upstream_name}</span>
                          </span>
                        ) : (
                          m.upstream_name
                        )}
                      </TableCell>
                      <TableCell>
                        <div className="flex flex-wrap gap-1">
                          {m.capabilities.map((c) => (
                            <Badge key={c} variant="muted">
                              {c}
                            </Badge>
                          ))}
                        </div>
                      </TableCell>
                      <TableCell className="text-sm">
                        <PriceSummary model={m} />
                      </TableCell>
                      <TableCell className="whitespace-nowrap">
                        <Button
                          variant="ghost"
                          size="icon-sm"
                          aria-label={`Edit ${m.alias}`}
                          onClick={() =>
                            setEditing({
                              original: m.alias,
                              kind: m.target ? 'alias' : 'provider',
                              alias: m.alias,
                              provider: m.provider,
                              upstream_name: m.upstream_name,
                              target: m.target ?? '',
                              capabilities: [...m.capabilities],
                              // Inherited prices show as blank so saving
                              // keeps inheriting; a typed value overrides.
                              prices: m.pricing_inherited ? {} : toPriceForm(m.pricing),
                            })
                          }
                        >
                          <Pencil />
                        </Button>
                        <AlertDialog>
                          <AlertDialogTrigger asChild>
                            <Button variant="ghost" size="icon-sm" aria-label={`Delete ${m.alias}`}>
                              <Trash2 />
                            </Button>
                          </AlertDialogTrigger>
                          <AlertDialogContent>
                            <AlertDialogHeader>
                              <AlertDialogTitle>Delete model "{m.alias}"?</AlertDialogTitle>
                              <AlertDialogDescription>
                                Callers using this name get model_not_found
                                immediately. Usage history is kept, and a model
                                other names point at cannot be deleted until
                                they are.
                              </AlertDialogDescription>
                            </AlertDialogHeader>
                            <AlertDialogFooter>
                              <AlertDialogCancel>Cancel</AlertDialogCancel>
                              <AlertDialogAction onClick={() => remove(m.alias)}>
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
    </div>
  )
}
