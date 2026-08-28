# Security policy

## Reporting a vulnerability

Report vulnerabilities privately through GitHub:
[open a security advisory](https://github.com/greyhavenhq/llmproxy/security/advisories/new).
Please do not open a public issue for a security problem.

Include what you need to reproduce it: the version (`llmproxy version`), the
configuration that matters (SSO or local mode, SQLite or Postgres), the request
that triggers it, and what you observed. Leave out any real credentials.

You should get an acknowledgement within a few days. Fixes ship in a normal
tagged release, credited to you unless you would rather stay anonymous.

## Supported versions

The latest tagged release is the supported one. Fixes go on top of `main` and
are released from there; there are no maintained backport branches.

## What is in scope

The proxy and its published artifacts: the HTTP surface (proxy, admin, auth),
the API key and session mechanisms, the OIDC integration, the transparent
Anthropic relay, credential storage, and the release binaries and container
image.

Things that are known and deliberate, not vulnerabilities:

- **Local (no-SSO) mode trusts anyone who can reach it.** Any key minted on the
  box grants access, which is why the server refuses to bind a non-loopback
  address without `-allow-nonlocal`. Running it exposed with that flag is a
  configuration choice, documented in
  [docs/install/binary.md](docs/install/binary.md).
- **The proxy does not inspect request bodies.** It forwards them byte-for-byte.
  Content filtering and prompt-level policy are out of scope by design; see the
  non-goals table in [docs/concepts/architecture.md](docs/concepts/architecture.md).
- **Losing the key secret invalidates keys, not confidentiality of past
  traffic.** The secret (`LLMPROXY_KEY_SECRET` or the generated secret file)
  is what makes stored key hashes and encrypted provider credentials usable.
  Keeping it in the same backup as the database defeats the "stolen database is
  useless" property, and that trade-off is yours to make.

Do report anything that lets a caller reach a model they were not granted,
recover another principal's key or session, extract stored upstream
credentials, escalate to admin, or get request or response content to land in
the database or the logs. That last one is a hard guarantee of the project and
a breach of it is a serious bug.
