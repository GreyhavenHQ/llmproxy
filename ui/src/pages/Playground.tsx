import { useEffect, useRef, useState } from 'react'
import { api } from '@/lib/api'
import { useAsync } from '@/lib/useAsync'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Card,
  CardAction,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'
import { Spinner } from '@/components/ui/spinner'
import { Textarea } from '@/components/ui/textarea'
import {
  DropdownMenu,
  DropdownMenuCheckboxItem,
  DropdownMenuContent,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { ChevronDown, Globe, ImagePlus, SendHorizontal, Square, Trash2, X } from 'lucide-react'

// In-browser chat against the proxy's own /v1/chat/completions, using the
// session cookie for auth. Conversations live only in component state: the
// proxy never persists content and neither does this page. Selecting several
// models fans one prompt out to all of them and shows the answers side by
// side with speed and token metrics read from the SSE usage chunk.

interface Attachment {
  name: string
  dataUrl: string
}

interface Reply {
  text: string
  status: 'streaming' | 'done' | 'error'
  error?: string
  // URLs the model fetched through the web_fetch tool, in call order.
  tools?: string[]
  firstTokenMs?: number
  totalMs?: number
  promptTokens?: number
  completionTokens?: number
}

interface ToolCall {
  id: string
  type: 'function'
  function: { name: string; arguments: string }
}

// One user prompt and, per selected model, that model's answer. Each model
// column is its own conversation: a model only ever sees its own replies.
interface Turn {
  id: number
  text: string
  images: Attachment[]
  replies: Record<string, Reply>
}

const MODELS_STORAGE_KEY = 'llmproxy.playground.models'
const WEBFETCH_STORAGE_KEY = 'llmproxy.playground.webfetch'

// The tool offered to models when web fetch is on; the browser cannot fetch
// cross-origin, so calls are executed via the proxy's /my/webfetch.
const WEB_FETCH_TOOL = {
  type: 'function',
  function: {
    name: 'web_fetch',
    description: 'Fetch a public web page over HTTP(S) and return its text content.',
    parameters: {
      type: 'object',
      properties: {
        url: { type: 'string', description: 'The absolute http(s) URL to fetch.' },
      },
      required: ['url'],
    },
  },
}

// Rounds of tool calls per prompt before the model must answer with what it has.
const MAX_TOOL_ROUNDS = 5

function loadSelected(): string[] {
  try {
    const raw = JSON.parse(localStorage.getItem(MODELS_STORAGE_KEY) ?? '[]')
    return Array.isArray(raw) ? raw.filter((m): m is string => typeof m === 'string') : []
  } catch {
    return []
  }
}

// The OpenAI content form: a plain string unless images are attached.
function userContent(t: { text: string; images: Attachment[] }): unknown {
  if (t.images.length === 0) return t.text
  return [
    ...(t.text ? [{ type: 'text', text: t.text }] : []),
    ...t.images.map((img) => ({ type: 'image_url', image_url: { url: img.dataUrl } })),
  ]
}

export function Playground() {
  // chat_stream is a separate capability; models without it get a unary
  // request instead of SSE.
  const models = useAsync(async () => {
    const [chat, streaming] = await Promise.all([
      api.get<{ data: { id: string }[] }>('/v1/models?endpoint=chat'),
      api.get<{ data: { id: string }[] }>('/v1/models?endpoint=chat_stream'),
    ])
    return { chat, streamable: new Set(streaming.data.map((m) => m.id)) }
  })
  const [selected, setSelected] = useState<string[]>(loadSelected)
  const [webFetch, setWebFetch] = useState(
    () => localStorage.getItem(WEBFETCH_STORAGE_KEY) === '1',
  )
  const [turns, setTurns] = useState<Turn[]>([])
  const [input, setInput] = useState('')
  const [images, setImages] = useState<Attachment[]>([])
  const [busy, setBusy] = useState(false)
  const abortRef = useRef<AbortController | null>(null)
  const nextTurnID = useRef(1)
  const fileInputRef = useRef<HTMLInputElement>(null)
  const bottomRef = useRef<HTMLDivElement>(null)

  const aliases = (models.data?.chat.data ?? []).map((m) => m.id)

  // Drop remembered selections whose model no longer exists.
  useEffect(() => {
    if (!models.data) return
    setSelected((sel) => {
      const kept = sel.filter((m) => aliases.includes(m))
      return kept.length === sel.length ? sel : kept
    })
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [models.data])

  useEffect(() => {
    localStorage.setItem(MODELS_STORAGE_KEY, JSON.stringify(selected))
  }, [selected])

  useEffect(() => {
    localStorage.setItem(WEBFETCH_STORAGE_KEY, webFetch ? '1' : '0')
  }, [webFetch])

  // Streaming aborts on unmount (tab switch); state is gone anyway.
  useEffect(() => () => abortRef.current?.abort(), [])

  const toggleModel = (alias: string, on: boolean) => {
    setSelected((sel) => (on ? [...sel, alias] : sel.filter((m) => m !== alias)))
  }

  const patchReply = (turnID: number, model: string, patch: Partial<Reply>) => {
    setTurns((ts) =>
      ts.map((t) =>
        t.id === turnID
          ? { ...t, replies: { ...t.replies, [model]: { ...t.replies[model], ...patch } } }
          : t,
      ),
    )
  }

  const streamOne = async (
    model: string,
    prior: Turn[],
    turn: Turn,
    useWebFetch: boolean,
    signal: AbortSignal,
  ) => {
    // convo grows during the turn as tool rounds add assistant/tool messages.
    const convo: Record<string, unknown>[] = []
    for (const t of prior) {
      convo.push({ role: 'user', content: userContent(t) })
      const r = t.replies[model]
      if (r?.status === 'done' && r.text) {
        convo.push({ role: 'assistant', content: r.text })
      }
    }
    convo.push({ role: 'user', content: userContent(turn) })

    const canStream = models.data?.streamable.has(model) ?? true
    const started = performance.now()
    let firstTokenMs: number | undefined
    let text = ''
    let promptTokens: number | undefined
    let completionTokens: number | undefined
    const toolLines: string[] = []

    const finish = (status: Reply['status'], error?: string) =>
      patchReply(turn.id, model, {
        status,
        error,
        text,
        firstTokenMs,
        promptTokens,
        completionTokens,
        totalMs: performance.now() - started,
      })

    // Tokens are summed over tool rounds so the metrics line covers the
    // whole exchange, not just the last request.
    const addUsage = (u?: { prompt_tokens?: number; completion_tokens?: number } | null) => {
      if (!u) return
      if (u.prompt_tokens !== undefined) promptTokens = (promptTokens ?? 0) + u.prompt_tokens
      if (u.completion_tokens !== undefined) {
        completionTokens = (completionTokens ?? 0) + u.completion_tokens
      }
    }

    const appendText = (delta: string, roundStart: boolean) => {
      if (firstTokenMs === undefined) firstTokenMs = performance.now() - started
      if (roundStart && text) text += '\n\n'
      text += delta
      patchReply(turn.id, model, { text })
    }

    // One model request: streams (or reads) any answer text into the visible
    // reply and returns the tool calls the model wants, appending the
    // assistant message to convo when there are any. 'error' means the reply
    // is already finalized.
    const requestOnce = async (): Promise<ToolCall[] | 'error'> => {
      const resp = await fetch('/v1/chat/completions', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        credentials: 'same-origin',
        body: JSON.stringify({
          model,
          messages: convo,
          stream: canStream,
          ...(useWebFetch ? { tools: [WEB_FETCH_TOOL] } : {}),
        }),
        signal,
      })
      if (!resp.ok || !resp.body) {
        let msg = `HTTP ${resp.status}`
        try {
          const err = (await resp.json()) as { error?: { message?: string } }
          msg = err.error?.message ?? msg
        } catch {
          // non-JSON error body; keep the status line
        }
        finish('error', msg)
        return 'error'
      }
      if (!canStream) {
        const full = (await resp.json()) as {
          choices?: { message?: { content?: unknown; tool_calls?: ToolCall[] } }[]
          usage?: { prompt_tokens?: number; completion_tokens?: number } | null
        }
        const msg = full.choices?.[0]?.message
        const content = typeof msg?.content === 'string' ? msg.content : ''
        if (content) appendText(content, true)
        addUsage(full.usage)
        const calls = msg?.tool_calls ?? []
        if (calls.length > 0) {
          convo.push({ role: 'assistant', content: content || null, tool_calls: calls })
        }
        return calls
      }
      const reader = resp.body.getReader()
      const decoder = new TextDecoder()
      const toolCalls: ToolCall[] = []
      let roundText = ''
      let buf = ''
      for (;;) {
        const { done, value } = await reader.read()
        if (done) break
        buf += decoder.decode(value, { stream: true })
        let idx: number
        while ((idx = buf.indexOf('\n')) >= 0) {
          const line = buf.slice(0, idx).trim()
          buf = buf.slice(idx + 1)
          if (!line.startsWith('data:')) continue
          const payload = line.slice(5).trim()
          if (!payload || payload === '[DONE]') continue
          let chunk: {
            choices?: {
              delta?: {
                content?: string
                tool_calls?: {
                  index?: number
                  id?: string
                  function?: { name?: string; arguments?: string }
                }[]
              }
            }[]
            usage?: { prompt_tokens?: number; completion_tokens?: number } | null
          }
          try {
            chunk = JSON.parse(payload)
          } catch {
            continue
          }
          const delta = chunk.choices?.[0]?.delta
          if (delta?.content) {
            appendText(delta.content, roundText === '')
            roundText += delta.content
          }
          for (const tc of delta?.tool_calls ?? []) {
            const i = tc.index ?? 0
            toolCalls[i] ??= { id: '', type: 'function', function: { name: '', arguments: '' } }
            if (tc.id) toolCalls[i].id = tc.id
            if (tc.function?.name) toolCalls[i].function.name += tc.function.name
            if (tc.function?.arguments) toolCalls[i].function.arguments += tc.function.arguments
          }
          addUsage(chunk.usage)
        }
      }
      if (toolCalls.length > 0) {
        convo.push({ role: 'assistant', content: roundText || null, tool_calls: toolCalls })
      }
      return toolCalls
    }

    try {
      for (let round = 0; round < MAX_TOOL_ROUNDS; round++) {
        const calls = await requestOnce()
        if (calls === 'error') return
        if (calls.length === 0) {
          finish('done')
          return
        }
        for (const tc of calls) {
          let url = ''
          try {
            url = String((JSON.parse(tc.function.arguments) as { url?: unknown }).url ?? '')
          } catch {
            // unparsable arguments; reported to the model below
          }
          toolLines.push(url || '(invalid tool arguments)')
          patchReply(turn.id, model, { tools: [...toolLines] })
          let result: string
          if (tc.function.name !== 'web_fetch' || !url) {
            result = 'error: only the web_fetch tool with a url argument is available'
          } else {
            try {
              const res = await api.post<{ status: number; text: string; truncated: boolean }>(
                '/my/webfetch',
                { url },
              )
              result = `HTTP ${res.status}\n${res.text}${res.truncated ? '\n[content truncated]' : ''}`
            } catch (err) {
              result = `fetch failed: ${err instanceof Error ? err.message : String(err)}`
            }
          }
          convo.push({ role: 'tool', tool_call_id: tc.id, content: result })
        }
      }
      // Tool budget exhausted; whatever text arrived stands.
      finish('done')
    } catch (err) {
      if (signal.aborted) {
        // Keep whatever streamed in before the stop.
        if (text) finish('done')
        else finish('error', 'stopped')
      } else {
        finish('error', err instanceof Error ? err.message : String(err))
      }
    }
  }

  const send = async () => {
    const text = input.trim()
    if ((!text && images.length === 0) || selected.length === 0 || busy) return
    const prior = turns
    const turn: Turn = { id: nextTurnID.current++, text, images, replies: {} }
    for (const m of selected) turn.replies[m] = { text: '', status: 'streaming' }
    setTurns([...prior, turn])
    setInput('')
    setImages([])
    setBusy(true)
    requestAnimationFrame(() => bottomRef.current?.scrollIntoView({ behavior: 'smooth' }))
    const ac = new AbortController()
    abortRef.current = ac
    await Promise.all(selected.map((m) => streamOne(m, prior, turn, webFetch, ac.signal)))
    setBusy(false)
  }

  const stop = () => abortRef.current?.abort()

  const addFiles = (files: Iterable<File>) => {
    for (const file of files) {
      if (!file.type.startsWith('image/')) continue
      const reader = new FileReader()
      reader.onload = () => {
        const dataUrl = reader.result
        if (typeof dataUrl === 'string') {
          setImages((imgs) => [...imgs, { name: file.name, dataUrl }])
        }
      }
      reader.readAsDataURL(file)
    }
  }

  const onPaste = (e: React.ClipboardEvent) => {
    const files = Array.from(e.clipboardData.files)
    if (files.length > 0) {
      e.preventDefault()
      addFiles(files)
    }
  }

  const onKeyDown = (e: React.KeyboardEvent) => {
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault()
      void send()
    }
  }

  if (models.loading && !models.data) return <Spinner />
  if (models.error) return <p className="text-sm text-destructive">{models.error}</p>

  return (
    <Card>
      <CardHeader>
        <CardTitle className="font-serif">Playground</CardTitle>
        <CardDescription>
          Chat with your models straight from the browser; nothing is saved.
        </CardDescription>
        <CardAction className="flex flex-wrap items-center justify-end gap-2">
          <Button
            variant={webFetch ? 'secondary' : 'outline'}
            aria-pressed={webFetch}
            title="Let the model fetch public web pages"
            onClick={() => setWebFetch((on) => !on)}
          >
            <Globe />
            Web fetch: {webFetch ? 'on' : 'off'}
          </Button>
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <Button variant="outline">
                {selected.length === 0
                  ? 'Select models'
                  : selected.length === 1
                    ? selected[0]
                    : `${selected.length} models`}
                <ChevronDown />
              </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end" className="max-h-96">
              <DropdownMenuLabel className="font-normal text-muted-foreground">
                Pick several to compare
              </DropdownMenuLabel>
              <DropdownMenuSeparator />
              {aliases.length === 0 && (
                <DropdownMenuLabel className="font-normal text-muted-foreground">
                  No chat models configured
                </DropdownMenuLabel>
              )}
              {aliases.map((alias) => (
                <DropdownMenuCheckboxItem
                  key={alias}
                  checked={selected.includes(alias)}
                  onCheckedChange={(on) => toggleModel(alias, on === true)}
                  onSelect={(e) => e.preventDefault()}
                >
                  {alias}
                </DropdownMenuCheckboxItem>
              ))}
            </DropdownMenuContent>
          </DropdownMenu>
          {turns.length > 0 && (
            <Button variant="outline" disabled={busy} onClick={() => setTurns([])}>
              <Trash2 />
              Clear chat
            </Button>
          )}
        </CardAction>
      </CardHeader>
      <CardContent className="flex flex-col gap-6">
        {turns.length === 0 && (
          <p className="py-12 text-center text-sm text-muted-foreground">
            {selected.length === 0
              ? 'Select a model to start.'
              : 'Send a message to start.'}
          </p>
        )}
        {turns.map((turn) => (
          <TurnView key={turn.id} turn={turn} />
        ))}
        <div ref={bottomRef} />

        <div className="flex flex-col gap-2 border-t pt-4">
          {images.length > 0 && (
            <div className="flex flex-wrap gap-2">
              {images.map((img, i) => (
                <div key={i} className="relative">
                  <img
                    src={img.dataUrl}
                    alt={img.name}
                    className="h-16 w-16 rounded-md border object-cover"
                  />
                  <Button
                    variant="secondary"
                    size="icon-sm"
                    aria-label={`Remove ${img.name}`}
                    className="absolute -top-2 -right-2 size-5 rounded-full"
                    onClick={() => setImages((imgs) => imgs.filter((_, j) => j !== i))}
                  >
                    <X className="size-3" />
                  </Button>
                </div>
              ))}
            </div>
          )}
          <div className="flex items-end gap-2">
            <input
              ref={fileInputRef}
              type="file"
              accept="image/*"
              multiple
              className="hidden"
              onChange={(e) => {
                addFiles(e.target.files ?? [])
                e.target.value = ''
              }}
            />
            <Button
              variant="outline"
              size="icon"
              aria-label="Attach image"
              title="Attach image"
              onClick={() => fileInputRef.current?.click()}
            >
              <ImagePlus />
            </Button>
            <Textarea
              className="max-h-48 min-h-9"
              rows={1}
              placeholder={
                selected.length > 1
                  ? `Message ${selected.length} models`
                  : 'Message'
              }
              value={input}
              onChange={(e) => setInput(e.target.value)}
              onKeyDown={onKeyDown}
              onPaste={onPaste}
            />
            {busy ? (
              <Button variant="outline" size="icon" aria-label="Stop" title="Stop" onClick={stop}>
                <Square />
              </Button>
            ) : (
              <Button
                size="icon"
                aria-label="Send"
                title="Send"
                disabled={(!input.trim() && images.length === 0) || selected.length === 0}
                onClick={() => void send()}
              >
                <SendHorizontal />
              </Button>
            )}
          </div>
        </div>
      </CardContent>
    </Card>
  )
}

function TurnView({ turn }: { turn: Turn }) {
  const models = Object.keys(turn.replies)
  return (
    <div className="flex flex-col gap-3">
      <div className="ml-auto max-w-[85%]">
        <div className="rounded-lg bg-primary px-3 py-2 text-sm whitespace-pre-wrap text-primary-foreground">
          {turn.images.length > 0 && (
            <div className="mb-1 flex flex-wrap gap-2">
              {turn.images.map((img, i) => (
                <img
                  key={i}
                  src={img.dataUrl}
                  alt={img.name}
                  className="max-h-40 rounded-md"
                />
              ))}
            </div>
          )}
          {turn.text}
        </div>
      </div>
      <div className="grid gap-3 [grid-template-columns:repeat(auto-fit,minmax(260px,1fr))]">
        {models.map((model) => (
          <ReplyView key={model} model={model} reply={turn.replies[model]} solo={models.length === 1} />
        ))}
      </div>
    </div>
  )
}

function ReplyView({ model, reply, solo }: { model: string; reply: Reply; solo: boolean }) {
  return (
    <div className={solo ? '' : 'rounded-lg border p-3'}>
      <div className="mb-1 flex flex-wrap items-center gap-2">
        <Badge variant="secondary" className="font-mono">
          {model}
        </Badge>
        {reply.status === 'streaming' && <Spinner className="size-3" />}
        <ReplyMetrics reply={reply} />
      </div>
      {reply.tools && reply.tools.length > 0 && (
        <div className="mb-1 flex flex-col gap-0.5">
          {reply.tools.map((url, i) => (
            <span
              key={i}
              className="flex items-center gap-1 break-all text-xs text-muted-foreground"
            >
              <Globe className="size-3 shrink-0" />
              {url}
            </span>
          ))}
        </div>
      )}
      {reply.status === 'error' ? (
        <p className="text-sm text-destructive">{reply.error}</p>
      ) : (
        <div className="text-sm whitespace-pre-wrap">
          {reply.text || (reply.status === 'done' && (
            <span className="text-muted-foreground">(empty response)</span>
          ))}
        </div>
      )}
    </div>
  )
}

// The speed line under each answer. tok/s uses the generation phase (first
// token to last) so it measures decoding speed, not queueing or prompt
// processing; sub-second single-chunk responses fall back to the total.
function ReplyMetrics({ reply }: { reply: Reply }) {
  if (reply.status !== 'done') return null
  const parts: string[] = []
  if (reply.firstTokenMs !== undefined) {
    parts.push(`${(reply.firstTokenMs / 1000).toFixed(2)}s to first token`)
  }
  if (reply.totalMs !== undefined) {
    parts.push(`${(reply.totalMs / 1000).toFixed(2)}s total`)
  }
  if (reply.completionTokens && reply.totalMs) {
    const genMs =
      reply.firstTokenMs !== undefined && reply.totalMs - reply.firstTokenMs > 100
        ? reply.totalMs - reply.firstTokenMs
        : reply.totalMs
    parts.push(`${(reply.completionTokens / (genMs / 1000)).toFixed(1)} tok/s`)
  }
  if (reply.promptTokens !== undefined || reply.completionTokens !== undefined) {
    parts.push(`${reply.promptTokens ?? '?'} in / ${reply.completionTokens ?? '?'} out`)
  }
  if (parts.length === 0) return null
  return <span className="text-xs text-muted-foreground">{parts.join(' · ')}</span>
}
