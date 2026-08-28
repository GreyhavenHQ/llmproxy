# Architecture

llmproxy is a single-binary gateway between OpenAI-compatible clients and a
curated set of upstream model servers, with per-user keys and per-unit usage
accounting. This page explains the design decisions behind it: what is in
scope, why the endpoint families are treated as structurally different, and
the rules the implementation follows. Each rule is stated with its rationale
so the shape of the system can be judged on its own terms.

## Scope and non-goals

The expensive part of an LLM gateway is the N-provider translation matrix.
llmproxy deliberately supports **two wire formats**: OpenAI-compatible HTTP
(vLLM, SGLang, OpenAI itself, anything speaking that dialect) and Anthropic's
native API. Everything else in the design assumes it never grows past that.

In scope:

- OpenAI ingress: chat completions, legacy completions, embeddings, model
  listing; transcription planned (the `audio_seconds` billing unit and the
  `transcription` capability already exist in the schema).
- SSE streaming as a first-class path, with cancellation propagation.
- API keys bound to a principal (human or service), OIDC login for the UI,
  group-derived roles, and a no-IdP local single-admin mode.
- Provider and model declaration, upstream model discovery, explicit
  capability sets per model.
- Usage and cost aggregation in per-unit quantities, with no content
  retention.
- A transparent Anthropic relay that meters traffic without owning the
  credential.

Explicit non-goals, revisited only with evidence:

| Not building | Why |
|---|---|
| Guardrails / PII detection | Different product; bolt on later if needed |
| Semantic caching | Needs a vector store; large complexity for unclear win |
| 20+ provider adapters | Two wire formats is the whole point |
| Prompt management, evals, playground | Not a gateway concern |
| MCP gateway | Separate problem, separate lifecycle |
| Batch API + Files API | Stateful async job lifecycle; see below |
| Fine-tuning proxying | No current use case |
| Text-to-speech, image generation, translations endpoint | Add when a caller actually asks |

**On batch, specifically.** It is not one more endpoint. It needs a Files
API, persistent job state, polling, and cost attribution that lands hours
after the request, possibly against a key that has since been revoked. That
breaks the clean "authenticate, call, account, forget" loop everything else
follows. If cheap bulk inference is needed later, the honest options are
letting callers hit the provider batch API directly with their own key, or
building it as a separate service that consumes this proxy, not inside it.

## The three endpoint families are not interchangeable

The single most important structural fact about the surface is that chat,
embeddings and transcription do not share a request model. The ingress layer
never assumes "JSON in, JSON or SSE out".

| | Chat | Embeddings | Transcription |
|---|---|---|---|
| Request body | JSON | JSON | `multipart/form-data` |
| Response | JSON or SSE stream | JSON | JSON, `text`, `srt` or `vtt` |
| Billing unit | input + output tokens | input tokens | seconds of audio |
| Streaming | yes | no | no |
| Body size | small | small to large (batched inputs) | up to tens of MB |
| Cancellation matters | yes | barely | somewhat |

This is why the usage schema records `(unit, quantity)` pairs per event
rather than a single token count, why multipart handling must stream without
disk spill, and why cancellation is treated as a first-class chat concern.

## Design rules

### Database-authoritative state

Postgres or SQLite is the single source of truth for providers, models, keys
and usage. Caches are derived hints with a short TTL (5 seconds by default)
and are explicitly invalidated on every admin write. In-memory authority
would buy microseconds of overhead and cost horizontal scaling and restart
safety; a proxy at this scale wants two interchangeable replicas more than it
wants those microseconds.

### Wire format is data, not code

A provider row carries a `wire_format` field. Adding an upstream is an
INSERT, not a vendor package. The alternative, one code module per vendor
behind a wide interface, only pays off when the vendor count grows, and the
non-goals table caps that count at two.

### Capabilities are a small per-model set, checked once

Each model binding declares a capability set (`chat`, `chat_stream`,
`completions`, `embeddings`, `transcription`), and the resolver checks it in
exactly one place. A call outside the set fails with a typed 400
`endpoint_not_supported` naming the supported capabilities, instead of a
confusing upstream 404. Enforcing capability at every call site instead
invites forgotten-gate bugs; enforcing it once cannot drift.

### Credentials attach at a single site

The upstream `Authorization: Bearer` header is attached in exactly one code
path, shared by unary and streamed requests, and a regression test asserts
the header is forwarded on both. Streaming paths that duplicate request
construction are where credential omission bugs live; a single site plus a
test removes the class.

### Usage is merged max-wins across stream chunks

The proxy forces `stream_options.include_usage` on streamed requests (there
is otherwise nothing to bill) and merges usage fields across chunks taking
the maximum per field, because some upstreams emit usage before the final
chunk and some only at the end.

### Partial usage is billed on client abort

A client disconnect cancels the upstream request within one chunk interval,
and the usage reported up to that point is still recorded, flagged
`cancelled`. The upstream billed for it; dropping the record would
under-count real spend.

### Streaming is bytes-through, with a bounded usage sniffer

Streamed responses are relayed as raw bytes, never re-framed or re-serialized.
A line scanner with a bounded buffer parses only lines that contain
`"usage"`; everything else passes untouched. Billing does not require
understanding the stream, only spotting the usage object, and re-framing is
where proxies corrupt SSE.

### Proxy metadata lives in headers, never in bodies

Every response carries `x-llmproxy-provider` and `x-llmproxy-model`, and
errors carry `x-llmproxy-error-source: proxy|upstream` plus a stable machine
code. Response bodies are never rewritten, so SDKs parse exactly what the
upstream produced, and callers can still tell who served the request and
whether an error came from the proxy or the upstream without string matching.

### Unpriced is a state, never zero

Pricing is keyed on `(model, unit)`. A pair with no entry records the
quantity as unpriced and flags the event; summaries show `cost: null` and
`/metrics` counts unpriced volume. Costing unknown models at zero silently
turns missing configuration into "free", which is exactly the failure a
spend-accounting system exists to prevent.

### Usage numbers are normalised at read time

The two supported wire formats disagree about what "input tokens" counts. The
OpenAI shape reports cached tokens as a subset of the prompt count
(`prompt_tokens_details.cached_tokens` ⊆ `prompt_tokens`), while Anthropic
reports cache reads and writes *outside* `input_tokens`. Summing either shape
naively produces a number that means something different per provider.

Stored quantities stay raw as reported, because pricing multiplies each unit
by its own rate and cache reads and writes have their own rates. Every
aggregate (`/my/usage*`, `/admin/v1/usage*`, `/stats/*`) then normalises
`input_tokens` to the non-cached input: the OpenAI cached subset is subtracted
back out, and the relay's `cache_creation_tokens` (fresh input billed at a
premium) folds in while cache reads stay their own unit.

So `input_tokens` always means "input processed at the full input rate" and is
comparable across providers, and `cached_input_tokens` is always the cheap
cache-read count on top of it. Normalising at write time instead would destroy
the raw numbers pricing needs; normalising in each caller would let two
dashboards disagree.

An upstream that never reports `cached_tokens` shows no cached quantity.
Absence means "not reported", never "free", for the same reason unpriced is
not zero.

### The schema is content-free by construction

This is the hard privacy guarantee: **no prompt, completion, embedding input
or other request/response content is ever persisted**. There is no
content-logging flag to leave off; the capability does not exist. A usage
event is identifiers, flags and numbers, and no table has a column that could
hold a body. Two tests guard it: a schema test rejects content-shaped column
names in the DDL, and an integration test sends a marker string through a
real request and asserts it never reaches disk. Building content capture out
entirely, rather than suppressing it, deletes a whole class of configuration,
review and incident.

### Caller keys are keyed hashes, shown once

API keys are stored only as an HMAC-SHA256 digest under a secret kept outside
the database, plus a last-4 display suffix; the plaintext is returned exactly
once, at mint. A stolen database can neither recover nor verify keys.
Recoverable secrets, or unsalted plain hashes, would make a database dump a
credential dump.

### Metric labels are aggregate-only

Prometheus series are labelled with provider, model alias, endpoint, unit and
outcome; never with principals or keys. Per-principal detail lives in the
database, where cardinality is a query concern rather than a time-series
explosion. Mutable, unbounded label values are a predictable operational
incident.

### Admin lists are paginated from day one

Every admin list endpoint takes `limit` and `offset` and echoes them back.
Pagination retrofitted later always leaves an unpaginated escape hatch
behind; starting paginated costs nothing.

### Aliases are globally unique; ambiguity is rejected at write time

A caller-facing model name maps to exactly one binding, enforced by a unique
constraint, and a conflicting registration gets a 409 at write time. There is
no bare-upstream-name fallback and no silent multi-provider resolution: if
several deployments could serve one name, something has to pick, and any
silent pick makes routing a mystery. A dry-run `resolve` endpoint answers
"what would this request do" without calling anything. A name may point at
another model, but only one hop, resolved in the same query, so lookup stays
single-step and cycles are unrepresentable.

### Admin mutations are audited in-transaction

Every admin mutation writes a metadata-only `admin_event` row in the same
transaction as the change itself, so the audit trail cannot miss a mutation
or record one that rolled back. Events carry who, what and when; never
payloads.

### Pricing is a versioned data feed

Prices are data, not code: a feed with a mandatory version, loadable from a
file at startup or through the admin API, with the active version visible.
Editing one model's prices publishes a new version carrying every other entry
over, so the price in force at any moment is always attributable to one feed
version.

### Never hold a database connection across an upstream call

Authentication does its query and returns the connection before the upstream
request starts; the relay holds no store handle, and usage is written after
the fact, detached from the request path. Holding a pooled connection for the
duration of a streamed response pins one connection per in-flight stream and
collapses the pool at modest concurrency. This rule is what lets a small pool
serve thousands of concurrent streams; see [performance](../reference/performance.md).
