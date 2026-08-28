# Getting started

This tutorial takes you from a fresh clone to a first successful chat
completion through the proxy. At the end you will have a running llmproxy, a
registered upstream provider, a bound model, your own API key, and a working
call from both curl and the Python OpenAI SDK.

You need:

- A recent Go toolchain. `go.mod` pins the exact version and Go's default
  `GOTOOLCHAIN=auto` fetches it, so any reasonably current install works.
- [`just`](https://github.com/casey/just), the command runner the project
  uses for its recipes.
- An OpenAI-compatible upstream to route to: a local vLLM or SGLang node, or
  any endpoint serving the OpenAI API shape (including
  `https://api.openai.com/v1` with your own OpenAI key).

No node toolchain is needed: the web UI's compiled assets are committed and
embedded at build time.

## 1. Build

```bash
git clone https://github.com/greyhavenhq/llmproxy.git
cd llmproxy
just build          # -> bin/llmproxy (static, roughly 14 MiB)
```

The result is one static binary (no CGO, pure-Go SQLite). The other recipes,
for later: `just ui` (rebuild the embedded web UI), `just test`, `just race`,
`just stress` and `just lint`.

## 2. Run

```bash
bin/llmproxy serve
# llmproxy v1.1.0 listening on http://127.0.0.1:4000
```

Without any OIDC configuration the proxy runs in local single-admin mode and
binds loopback only. State appears in the working directory on first boot:
`llmproxy.db` (SQLite, WAL mode), the key-hashing secret in
`.llmproxy/secret`, and a generated admin password in
`.llmproxy/admin-password`.

## 3. Sign in

Open <http://127.0.0.1:4000> and sign in with the admin password:

```bash
cat .llmproxy/admin-password
```

(Set `LLMPROXY_ADMIN_PASSWORD` before starting the server if you want to
choose it yourself.) The UI covers everything this tutorial does: provider
registration, model binding, keys and the usage dashboard. The rest of the
tutorial uses the HTTP API so every step is copy-pasteable; feel free to do
the same steps in the UI instead.

For the terminal steps, mint an admin API key. Run this from the same
directory as the server, in a second terminal:

```bash
bin/llmproxy key create -label tutorial
# key id:    3f2a...
# principal: local-admin
# api key:   lp_...
# Store it now; it will not be shown again.
```

```bash
ADMIN=lp_...                  # the key you just minted
P=http://127.0.0.1:4000
```

## 4. Register a provider

A provider is an upstream endpoint speaking a wire format. `base_url`
includes `/v1`. The upstream key is optional (many self-hosted nodes are
unauthenticated) and is stored encrypted, never returned by any API.

```bash
curl -s $P/admin/v1/providers -H "authorization: Bearer $ADMIN" \
  -H 'content-type: application/json' -d '{
    "name": "vllm-1",
    "wire_format": "openai",
    "base_url": "http://10.0.0.5:8000/v1",
    "api_key": "upstream-secret"
  }'
```

Adjust `base_url` (and `api_key`) for your upstream. You can ask the upstream
what it serves; discovery is read-only and never binds anything:

```bash
curl -s $P/admin/v1/providers/vllm-1/discover -H "authorization: Bearer $ADMIN"
```

## 5. Bind a model

A binding maps a caller-facing alias to one (provider, upstream model) pair
and serves as soon as it exists. Use an upstream name from the discovery
output:

```bash
curl -s $P/admin/v1/models -H "authorization: Bearer $ADMIN" \
  -H 'content-type: application/json' -d '{
    "alias": "qwen-72b",
    "provider": "vllm-1",
    "upstream_name": "Qwen/Qwen2.5-VL-72B-Instruct",
    "capabilities": ["chat", "chat_stream"]
  }'
```

Check what the alias resolves to without calling anything:

```bash
curl -s "$P/admin/v1/resolve?model=qwen-72b" -H "authorization: Bearer $ADMIN"
```

## 6. Create your key

The admin key works for inference too, but day-to-day callers use their own
keys. Any key or session holder can mint keys for themselves:

```bash
curl -s $P/my/keys -H "authorization: Bearer $ADMIN" \
  -H 'content-type: application/json' -d '{"label": "laptop"}'
```

The response contains the plaintext `key` exactly once; listings afterwards
show only `***xxxx`.

```bash
KEY=lp_...                    # the new key
```

## 7. First chat completion

With curl:

```bash
curl -s $P/v1/chat/completions -H "authorization: Bearer $KEY" \
  -H 'content-type: application/json' -d '{
    "model": "qwen-72b",
    "messages": [{"role": "user", "content": "Say hello."}]
  }'
```

The response is the upstream's own body, untouched; the
`x-llmproxy-provider` and `x-llmproxy-model` headers tell you who served it.

With the Python OpenAI SDK, one line changed from stock usage:

```python
from openai import OpenAI

client = OpenAI(base_url="http://127.0.0.1:4000/v1", api_key="lp_...")

print(client.models.list())            # curated aliases only

resp = client.chat.completions.create(
    model="qwen-72b",
    messages=[{"role": "user", "content": "Say hello."}],
)
print(resp.choices[0].message.content)

# Streaming works the same way; SSE passes through byte-for-byte.
for chunk in client.chat.completions.create(
    model="qwen-72b",
    messages=[{"role": "user", "content": "Count to five."}],
    stream=True,
):
    print(chunk.choices[0].delta.content or "", end="")
```

Back in the UI, the usage dashboard now shows the requests, tokens and (once
you set prices) cost.

## Where next

- [installation.md](installation.md) for systemd, state layout and upgrades;
  [docker.md](docker.md) for the container route.
- [providers-and-models.md](providers-and-models.md) for capabilities,
  aliases, per-endpoint overrides and TLS options.
- [keys-and-usage.md](keys-and-usage.md) for pricing, usage accounting and
  metrics.
- [sso.md](sso.md) to move from the local admin password to OIDC SSO.
- [transparent-relay.md](transparent-relay.md) to meter Claude Code and other
  Anthropic-native tools.
