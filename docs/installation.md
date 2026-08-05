# Installation

llmproxy is a single static Go binary with no CGO and a pure-Go SQLite driver.
There is nothing else to install: no separate runtime, no external migration
tool, no sidecar.

## Build from source

The module's `go.mod` pins the exact Go version, so any reasonably recent
toolchain will fetch the right compiler automatically (Go's default
`GOTOOLCHAIN=auto` behavior). The web UI's compiled assets are committed in
`internal/server/uidist/` and embedded at build time, so building the binary
needs no node toolchain; `just ui` rebuilds them from `ui/` when you change
the frontend.

```bash
git clone https://github.com/monadical/llmproxy
cd llmproxy
just build          # -> bin/llmproxy (static, roughly 14 MiB)
```

`just build` runs:

```bash
CGO_ENABLED=0 go build -ldflags="-s -w" -o bin/llmproxy ./cmd/llmproxy
```

Cross-compile the same way, for example for a Linux server:

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o llmproxy ./cmd/llmproxy
```

The binary has four subcommands: `serve`, `key create|list|delete`,
`principal create` and `version`. The `key` and `principal` commands operate
directly on the database, which is how you bootstrap before the HTTP API has
any keys.

## Where state lives

Three pieces of state exist outside the process, all created on first use
relative to the working directory unless configured otherwise:

- The database. `LLMPROXY_DATABASE_URL` defaults to `llmproxy.db`, a SQLite
  file opened in WAL mode (so you will also see `llmproxy.db-wal` and
  `llmproxy.db-shm` alongside it). Point it at a `postgres://` URL for
  Postgres instead; see [configuration.md](configuration.md).
- The secret. If `LLMPROXY_KEY_SECRET` is not set, a random 32-byte secret is
  generated once and written to `LLMPROXY_SECRET_FILE` (default
  `.llmproxy/secret`, mode 0600). This secret is what makes API key hashes
  and encrypted provider credentials usable; losing it invalidates every key
  and stored upstream credential. Back it up together with the database, and
  keep it out of the database's backup destination if you want the "stolen DB
  is useless" property to hold. Details in
  [configuration.md](configuration.md#the-secret).
- The admin password. Unless `LLMPROXY_ADMIN_PASSWORD` is set (or password
  login is disabled), a random password is generated on first boot and written
  to `LLMPROXY_ADMIN_PASSWORD_FILE` (default `.llmproxy/admin-password`, mode
  0600). It signs the local admin into the browser UI and is never stored in
  the database. Losing it is harmless: delete the file and restart to get a
  new one.

Run the CLI from the same working directory (or with the same environment
variables) as the server, otherwise `llmproxy key create` will mint keys
against a different database and secret than the server is using.

## First login (local mode)

Without any `LLMPROXY_OIDC_*` configuration the proxy runs in local
single-admin mode: on boot it creates a `local-admin` principal with the admin
role (name configurable via `LLMPROXY_LOCAL_ADMIN_NAME`).

The fastest path is the browser: start the server, read the generated password
from `.llmproxy/admin-password`, open `http://127.0.0.1:4000` and sign in. The
UI covers provider registration, model bindings, API keys and usage.

To bootstrap over the API instead, mint the first key with the CLI:

```bash
bin/llmproxy key create -label bootstrap
# key id:    3f2a...
# principal: local-admin
# api key:   lp_...
# Store it now; it will not be shown again.
```

The plaintext is printed once; only a keyed HMAC-SHA256 hash is stored. After
this, everything else can be done over the HTTP API with that key.

Then run the server:

```bash
bin/llmproxy serve       # llmproxy 1.0.0 listening on http://127.0.0.1:4000
```

## The loopback guard

In local mode any key minted on the box grants access, so the server refuses
to bind to a non-loopback address (`127.0.0.1`, `::1` and `localhost` are the
allowed hosts) unless you explicitly override:

```bash
bin/llmproxy serve -host 0.0.0.0            # refuses with an error
bin/llmproxy serve -host 0.0.0.0 -allow-nonlocal   # binds, prints a loud warning
```

`LLMPROXY_ALLOW_NONLOCAL=1` is the environment equivalent of
`-allow-nonlocal`. The guard does not apply once SSO is configured
(`LLMPROXY_OIDC_ISSUER` set), because access is then gated by the identity
provider. If you need local mode reachable from other machines, prefer keeping
the bind on loopback and fronting it with a reverse proxy you control.

## systemd unit example

```ini
[Unit]
Description=llmproxy
After=network-online.target
Wants=network-online.target

[Service]
User=llmproxy
Group=llmproxy
WorkingDirectory=/var/lib/llmproxy
ExecStart=/usr/local/bin/llmproxy serve
Restart=on-failure
Environment=LLMPROXY_DATABASE_URL=/var/lib/llmproxy/llmproxy.db
Environment=LLMPROXY_SECRET_FILE=/var/lib/llmproxy/secret
# Environment=LLMPROXY_OIDC_ISSUER=https://auth.example.com/application/o/llmproxy/
# ... see docs/configuration.md and docs/sso.md

# Hardening (optional but cheap; the process only needs its state directory)
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=/var/lib/llmproxy
PrivateTmp=true

[Install]
WantedBy=multi-user.target
```

Create the state directory and user first:

```bash
useradd --system --home /var/lib/llmproxy --shell /usr/sbin/nologin llmproxy
mkdir -p /var/lib/llmproxy && chown llmproxy:llmproxy /var/lib/llmproxy
```

Bootstrap the first key as that user so the CLI and the service share state:

```bash
sudo -u llmproxy env LLMPROXY_DATABASE_URL=/var/lib/llmproxy/llmproxy.db \
  LLMPROXY_SECRET_FILE=/var/lib/llmproxy/secret \
  /usr/local/bin/llmproxy key create -label bootstrap
```

The server shuts down cleanly on SIGINT/SIGTERM: it stops accepting
connections, gives in-flight requests up to 10 seconds, and drains the
detached usage-accounting goroutines so no usage records are lost.

## Upgrades

The schema is applied on every boot with idempotent `CREATE TABLE IF NOT
EXISTS` / `CREATE INDEX IF NOT EXISTS` statements; there is no separate
migration step. Upgrading is: stop the service, replace the binary, start the
service. New tables and indexes introduced by a newer version are created
automatically on the next boot.

Two caveats worth knowing:

- `IF NOT EXISTS` creates missing tables but does not alter existing ones.
  Additive column changes are applied automatically at boot (`api_key.key_suffix`
  arrived this way; keys minted before it list an empty display
  suffix until recreated). Pre-1.0 development versions had a model `exposed`
  flag that was later removed: every binding now serves as soon as it exists,
  so a binding that was deliberately unexposed on an older database becomes
  live after the upgrade; delete it if
  that is not wanted. Take a database backup before upgrading regardless
  (for SQLite, copy the `.db` file while the service is stopped, or use
  `sqlite3 llmproxy.db ".backup ..."` while running).
- The secret file format is stable and is never rewritten by the server; do
  not regenerate it during upgrades.
