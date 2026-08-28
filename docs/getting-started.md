# Getting started

Take a running proxy from nothing to a first chat completion in about ten
minutes. At the end you have llmproxy running, one upstream provider
registered, one model bound, your own API key, and a working call from curl
and the Python OpenAI SDK.

You need:

- A Linux or macOS machine.
- An OpenAI-compatible upstream to route to: a local vLLM or SGLang node, or
  any endpoint serving the OpenAI API shape (including
  `https://api.openai.com/v1` with your own OpenAI key).

## 1. Install the binary

```bash
curl -fsSL https://github.com/greyhavenhq/llmproxy/releases/latest/download/llmproxy_Linux_x86_64.tar.gz | tar xz
sudo install -m 0755 llmproxy /usr/local/bin/llmproxy
llmproxy version
```

```
llmproxy v1.0.0
```

Other platforms and checksum verification are in
[install/binary.md](install/binary.md). To build from source instead:
`git clone https://github.com/greyhavenhq/llmproxy.git && cd llmproxy && just build`.

## 2. Start the server

```bash
llmproxy serve
```

```
llmproxy v1.0.0 listening on http://127.0.0.1:4000
```

State appears in the working directory on first boot: `llmproxy.db`, the
key-hashing secret in `.llmproxy/secret`, and a generated admin password in
`.llmproxy/admin-password`.

## 3. Sign in

Read the generated password:

```bash
cat .llmproxy/admin-password
```

Open <http://127.0.0.1:4000> and sign in with it.

The UI does everything the rest of this tutorial does: providers, models,
keys and the usage dashboard. The steps below use the HTTP API so they are
copy-pasteable. Follow either.

## 4. Mint an admin key

Run this in a second terminal, from the same directory as the server:

```bash
llmproxy key create -label tutorial
```

```
key id:    3f2a...
principal: local-admin
api key:   lp_...
Store it now; it will not be shown again.
```

Set the two variables the remaining steps use:

```bash
ADMIN=lp_...                  # the key you just minted
P=http://127.0.0.1:4000
```

## 5. Register a provider

A provider is an upstream endpoint. `base_url` includes `/v1`. Adjust both
values for your upstream; drop `api_key` if it needs no authentication.

```bash
curl -s $P/admin/v1/providers -H "authorization: Bearer $ADMIN" \
  -H 'content-type: application/json' -d '{
    "name": "vllm-1",
    "wire_format": "openai",
    "base_url": "http://10.0.0.5:8000/v1",
    "api_key": "upstream-secret"
  }'
```

The response echoes the provider with `has_credential: true`. The credential
is stored encrypted and no endpoint ever returns it.

Ask the upstream what it serves:

```bash
curl -s $P/admin/v1/providers/vllm-1/discover -H "authorization: Bearer $ADMIN"
```

```json
{
  "provider": "vllm-1",
  "models": [
    {"upstream_name": "Qwen/Qwen2.5-VL-72B-Instruct", "bound_alias": null}
  ]
}
```

Discovery is read-only and binds nothing.

## 6. Bind a model

Use an `upstream_name` from the discovery output. The alias is the name your
callers will use.

```bash
curl -s $P/admin/v1/models -H "authorization: Bearer $ADMIN" \
  -H 'content-type: application/json' -d '{
    "alias": "qwen-72b",
    "provider": "vllm-1",
    "upstream_name": "Qwen/Qwen2.5-VL-72B-Instruct",
    "capabilities": ["chat", "chat_stream"]
  }'
```

The binding serves callers as soon as it exists. Check what it resolves to
without calling the upstream:

```bash
curl -s "$P/admin/v1/resolve?model=qwen-72b" -H "authorization: Bearer $ADMIN"
```

```json
{
  "alias": "qwen-72b",
  "provider": "vllm-1",
  "upstream_name": "Qwen/Qwen2.5-VL-72B-Instruct",
  "url": "http://10.0.0.5:8000/v1/chat/completions"
}
```

## 7. Create a caller key

The admin key works for inference too, but day-to-day callers use their own.
Any key or session holder mints keys for themselves:

```bash
curl -s $P/my/keys -H "authorization: Bearer $ADMIN" \
  -H 'content-type: application/json' -d '{"label": "laptop"}'
```

The response contains the plaintext `key` exactly once; listings afterwards
show only `***xxxx`.

```bash
KEY=lp_...                    # the new key
```

## 8. Make a call

```bash
curl -s $P/v1/chat/completions -H "authorization: Bearer $KEY" \
  -H 'content-type: application/json' -d '{
    "model": "qwen-72b",
    "messages": [{"role": "user", "content": "Say hello."}]
  }'
```

The response is the upstream's own body, untouched. The
`x-llmproxy-provider` and `x-llmproxy-model` response headers name who served
it.

From the Python OpenAI SDK, one line differs from stock usage:

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

## 9. See the usage

Back in the UI, the Usage tab now shows the requests and tokens. Cost appears
once you set prices; see [guides/pricing.md](guides/pricing.md).

## Where next

- Deploy it properly: [install/binary.md](install/binary.md) for systemd, or
  [install/docker.md](install/docker.md) for containers.
- Give it to your team: [guides/sso.md](guides/sso.md).
- Meter Claude Code: [guides/claude-code.md](guides/claude-code.md).
