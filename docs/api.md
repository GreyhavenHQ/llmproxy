# Supported API surface

Map of the OpenAI-compatible surface against what llmproxy implements. Wire
shapes follow OpenAI's own API reference; requests are forwarded as-is except
for the model rewrite and `stream_options.include_usage` injection on streams.
The full machine-readable spec is in [openapi.yaml](openapi.yaml).

## Caller-facing (`/v1`, OpenAI dialect)

| Endpoint | Status | Notes |
|---|---|---|
| `POST /v1/chat/completions` | **Supported** | Unary JSON and SSE streaming. Vision content parts, tool/function calls, response_format, logprobs etc. pass through untouched. Capability `chat` (+ `chat_stream` for streaming). |
| `POST /v1/completions` | **Supported** | Legacy text completions, unary and streamed. Capability `completions`. |
| `GET /v1/models` | **Supported** | Aliases on enabled providers; `?endpoint=chat\|embeddings\|...` filters by capability. Each entry carries the OpenAI fields plus `capabilities` (resolved) and `alias_of` (the model this name points at, or `null`). Public, no API key required. |
| `GET /v1/models/{id}` | Not yet | Trivial to add; nothing has needed it. |
| `POST /v1/embeddings` | **Supported** | Unary passthrough. Capability `embeddings`. Array `input` is capped at `LLMPROXY_MAX_EMBEDDING_BATCH` items (default 2048); larger batches get 400 `embedding_batch_too_large`. |
| `POST /v1/audio/transcriptions` | Planned | Streamed multipart, no disk spill; `audio_seconds` unit reserved. |
| `POST /v1/audio/translations` | Out of scope | Add on demand once transcription exists. |
| `POST /v1/responses` | Out of scope for 1.0 | OpenAI Responses API; revisit with evidence. |
| `/v1/batches`, `/v1/files` | Out of scope | Deliberate non-goal; see [architecture.md](architecture.md#scope-and-non-goals). |
| `/v1/images/*`, `/v1/audio/speech`, `/v1/moderations`, fine-tuning, assistants | Out of scope | Non-goals. |
| `POST /v1/messages` (Anthropic dialect) | Planned | Second ingress adapter over the same catalog. Until then, the transparent relay (below) covers Anthropic-native tooling that brings its own credentials. |

Auth: `Authorization: Bearer lp_...` or `x-api-key`. Unknown (including
deleted) keys get 401 `invalid_api_key`; key management is
database-authoritative, so deletion revokes a key immediately. `GET
/v1/models` is the exception: it is served without authentication, since it
only exposes curated aliases and provider names.

Model names callers see are curated aliases, globally unique across all
providers; there is no bare-upstream-name fallback and no multi-provider
resolution. Calling an endpoint outside a model's capability set fails at the
proxy with a 400 naming the supported capabilities, never with a confusing
upstream 404.

Every response carries `x-llmproxy-provider` and `x-llmproxy-model`. Errors are
OpenAI-shaped with an added `llmproxy.source` field plus
`x-llmproxy-error-source: proxy|upstream`; upstream error bodies and status
codes pass through intact.

Requests may carry `x-llmproxy-tags`, a comma-separated list of `key:value`
pairs (`app:dataindex,context:search`) naming the calling application. The
proxy stores the normalised list on the usage event and the Usage tab's Apps
subtab breaks spend down by it; see
[keys-and-usage.md](keys-and-usage.md#application-tags). Malformed pairs are
dropped rather than rejected, and the header never reaches an upstream.

No request or response content is ever logged or persisted, and there is no
flag to turn that on; see [architecture.md](architecture.md).

Proxy-generated error codes: `missing_api_key`, `invalid_api_key`,
`admin_required`, `model_not_found` (404), `unknown_endpoint` (404, with a
hint when the path suggests a missing `/v1` prefix or an unsupported OpenAI
endpoint), `endpoint_not_supported` (400, names the supported capability
set), `invalid_json`, `model_required`, `request_too_large` (413),
`provider_unreachable` (502).

## Self-service (`/my`)

| Endpoint | Purpose |
|---|---|
| `POST /my/keys` | Mint a key for your own principal (plaintext shown once) |
| `GET /my/keys` | List own keys (metadata only) |
| `DELETE /my/keys/{id}` | Delete own key (deletion is the revocation mechanism) |
| `POST /my/relay-tokens` | Mint a transparent-relay token (`lpt_`, plaintext shown once; not an API key) |
| `GET /my/relay-tokens` | List own relay tokens (metadata only) |
| `DELETE /my/relay-tokens/{id}` | Delete own relay token (revocation) |
| `GET /my/usage?since&until` | Own usage by model/endpoint with per-unit quantities and cost |
| `GET /my/usage/series?bucket&since&until` | Own usage bucketed by hour/day/week/month, gap-filled, UTC |

## Team statistics (`/stats`, any authenticated user)

The proxy's usage is team-visible by design; the admin role gates
configuration, not visibility. Every endpoint accepts the same filters:
`principal`, `key` (an API key id), `provider`, `model`, `client`, `tag`,
`outcome` and the `since`/`until` window; see
[keys-and-usage.md](keys-and-usage.md#team-statistics). `tag` takes one exact
`key:value` pair, is repeatable up to four times, and several pairs narrow
together; a pair nothing carries simply matches nothing. `outcome` takes
`ok`, `upstream_error`, `unreachable`, `cancelled` or the meta value
`failed` (everything not ok); an unknown value is a 400.

| Endpoint | Purpose |
|---|---|
| `GET /stats/series?bucket&since&until` | Bucketed usage across everyone |
| `GET /stats/summary?since&until` | Usage aggregated per (principal, provider, model, endpoint, client, tags) |
| `GET /stats/requests?limit&offset` | One page of the filtered request metadata log (never content), newest first; returns `{requests, limit, offset, total}` |
| `GET /stats/requests/facets?since&until` | Distinct principals, keys, providers, models, clients and tags in the window, for the explorer's filter options |
| `GET /stats/errors?bucket&since&until` | The errors dashboard in one call: a gap-filled series of counts per outcome, plus a breakdown per (provider, model, endpoint, client, tags, outcome, error_kind, status_code) with request count, average duration, last-seen and time-to-outcome bands (<1s, 1-5s, 5-15s, 15-30s, 30-60s, 60-120s, >=120s). Rows with outcome `ok` are included so error rates have their denominator. |

Failures carry an `error_kind` classification token: the transport class on
`unreachable` (`timeout`, `connection_error`, ...) and the upstream's
`error.type`/`error.code` on `upstream_error` (e.g. `rate_limit_error`),
sanitised to a short identifier. Provider error *messages* are never stored;
they can echo request content.

## Transparent Anthropic relay (`/transparent/anthropic`)

`{ANY} /transparent/anthropic/{token}/{path...}` forwards verbatim to the
configured Anthropic base URL: method, path suffix, query, headers (minus
hop-by-hop, `Cookie`, `Accept-Encoding` and `x-llmproxy-tags`) and body
untouched, the caller's
own `x-api-key` or OAuth bearer passed through. The relay token attributes
usage to a principal and authenticates nothing else; API keys are rejected
here and relay tokens are rejected everywhere else. Usage is read off the
response (`message_start`/`message_delta` on streams, the `usage` object on
unary bodies) and recorded under the sentinel provider
`transparent:anthropic`; endpoints without usage still get a metadata-only
event, except HEAD/OPTIONS probes, which relay unrecorded.
Proxy-generated errors: 404 `unknown_relay_token`, 404
`transparent_relay_disabled`, 502 `provider_unreachable`. Docs:
[transparent-relay.md](transparent-relay.md).

## Admin (`/admin/v1`, admin role required, lists paginated)

| Endpoint | Purpose |
|---|---|
| `POST/GET/PATCH/DELETE /providers[/{name}]` | Register, inspect, edit, unregister providers (upstream key encrypted at rest, never returned) |
| `GET /providers/{name}/discover` | Upstream model listing; read-only, never auto-binds |
| `POST/GET/PATCH/DELETE /models[/{alias}]` | Bind upstream models to globally unique aliases with capability sets and per-unit prices; a name can instead `target` another model and inherit its provider, capabilities and prices (one hop). Everything, including the name, is editable in place; a binding serves as soon as it exists (disable the provider to take it offline) |
| `GET /resolve?model&endpoint&stream` | Dry-run alias resolution |
| `POST/GET /principals` | Users and service principals |
| `POST /principals/{id}/revoke-sessions` | Delete every browser session of a principal (API keys untouched) |
| `POST/GET/DELETE /keys[/{id}]` | Keys for any principal |
| `POST/GET /pricing` | Load/inspect the versioned pricing feed (bulk; per-model prices go through `/models`) |
| `GET /usage/summary?since&until&principal` | Usage and cost by principal/model/endpoint/unit |
| `GET /usage/series?bucket&since&until&principal` | Usage bucketed by hour/day/week/month across everyone |
| `GET /requests?limit&offset` | The request metadata log with per-unit quantities (who, key, model, outcome, tokens; never content); same filters as `/stats/requests` |
| `GET /events` | Metadata-only admin audit trail |

## Browser auth (`/auth`)

| Endpoint | Purpose |
|---|---|
| `GET /auth/me` | Who am I: `{authenticated, name, role, sso_enabled, password_enabled}`; 200 even when anonymous so the login screen can render |
| `POST /auth/password` | JSON `{password}` login for the local admin (from `LLMPROXY_ADMIN_PASSWORD` or the generated password file); works with or without SSO |
| `GET /auth/login` | SSO only: set the signed state cookie, redirect to the IdP authorization endpoint |
| `GET /auth/callback` | SSO only: verify state, exchange the code, fetch userinfo, apply group gate/role mapping, upsert the principal (keyed on `sub`), issue the session cookie |
| `GET /auth/logout` | Delete the caller's server-side session row and clear the cookie; even a stolen copy of the cookie dies with it |

Sessions carry the principal's role and authenticate `/v1`, `/my`, `/stats`
and (admin role) `/admin/v1`. Non-GET session requests are origin-checked;
API keys need no Origin.

## Operational

| Endpoint | Purpose |
|---|---|
| `GET /` | Built-in UI: a React app (source in `ui/`, Vite build committed to `internal/server/uidist/` and embedded via `go:embed`; fonts and all assets served from the binary). It is a plain client of `/auth`, `/my`, `/stats` and `/admin/v1`. Sign-in via SSO and/or the admin password; self-service keys and a usage dashboard (requests, tokens and cost per hour/day/week/month) for everyone; providers (with upstream discovery and quick-bind), models with their prices and the request log for admins. |
| `GET /healthz` | Liveness |
| `GET /metrics` | Prometheus text: requests, duration, per-unit usage, unpriced volume. Aggregate labels only (provider, model, endpoint, unit, outcome). |

## Model-management compatibility endpoints

A compatibility surface for existing proxy management tooling (admin role
required). The JSON field names below, `litellm_params` included, are the
wire format such tooling sends and expects.

| Endpoint | Behaviour |
|---|---|
| `POST /model/new` | Accepts a deployment payload (`model_name`, `litellm_params.{model,api_base,api_key}`, extra fields ignored). Creates or reuses a provider for `api_base` (name derived from the host; a supplied `api_key` becomes the provider credential), then binds `model_name` as a live alias. `model_info.mode` `embedding`/`completion` maps to capabilities; default is `chat, chat_stream`. Re-registering the same deployment is an idempotent 200. Because aliases are globally unique and there is no load-balancing pool, a second, different deployment under an existing `model_name` is rejected with 409. |
| `GET /model/info` | Deployments as `{data: [{model_name, litellm_params: {model, api_base, custom_llm_provider}, model_info: {id}}]}`. |
| `POST /model/delete` `{id}` | Deletes the binding by the `model_info.id` from `/model/info`. |
| `POST /chat/completions`, `POST /completions`, `POST /embeddings`, `GET /models` (root, no `/v1`) | Aliases of the `/v1` handlers, for clients configured without the prefix. `/models` is also a page of the built-in UI: a request whose `Accept` header lists `text/html` (a browser navigation) gets the app instead of JSON. API clients are unaffected, and `/v1/models` always returns JSON. |
