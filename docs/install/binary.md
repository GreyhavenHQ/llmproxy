# Install the binary

llmproxy is one static Go binary with no CGO and a pure-Go SQLite driver.
There is no runtime to install, no migration tool and no sidecar.

## Install a release

Every tag publishes archives for linux/amd64, linux/arm64 and darwin/arm64 on
the [releases page](https://github.com/greyhavenhq/llmproxy/releases).

1. Download and install:

   ```bash
   curl -fsSL https://github.com/greyhavenhq/llmproxy/releases/latest/download/llmproxy_Linux_x86_64.tar.gz | tar xz
   sudo install -m 0755 llmproxy /usr/local/bin/llmproxy
   llmproxy version
   ```

   ```
   llmproxy v1.0.0
   ```

   Archive names follow `llmproxy_<Os>_<Arch>.tar.gz`, with `Os` in
   `Linux`/`Darwin` and `Arch` in `x86_64`/`arm64`.

2. Verify the download:

   ```bash
   curl -fsSLO https://github.com/greyhavenhq/llmproxy/releases/latest/download/checksums.txt
   sha256sum -c --ignore-missing checksums.txt
   ```

   ```
   llmproxy_Linux_x86_64.tar.gz: OK
   ```

For containers, use the published image instead; see [docker.md](docker.md).

## Build from source instead

You need a recent Go toolchain (`go.mod` pins the version and Go's default
`GOTOOLCHAIN=auto` fetches it) and [just](https://just.systems). No node
toolchain is needed: the web UI's compiled assets are committed and embedded
at build time.

```bash
git clone https://github.com/greyhavenhq/llmproxy
cd llmproxy
just build          # -> bin/llmproxy (static, roughly 14 MiB)
```

Cross-compile for another target:

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o llmproxy ./cmd/llmproxy
```

## Where state lives

Three pieces of state live outside the process. All are created on first use,
relative to the working directory unless configured otherwise.

| What | Default location | Set with |
|---|---|---|
| Database | `llmproxy.db` (SQLite, WAL mode, plus `-wal` and `-shm` files) | `LLMPROXY_DATABASE_URL`, or a `postgres://` URL |
| Key secret | `.llmproxy/secret` (32 random bytes, mode 0600) | `LLMPROXY_KEY_SECRET` or `LLMPROXY_SECRET_FILE` |
| Admin password | `.llmproxy/admin-password` (mode 0600) | `LLMPROXY_ADMIN_PASSWORD` or `LLMPROXY_ADMIN_PASSWORD_FILE` |

Two consequences to plan for:

- **Losing the key secret invalidates every API key and every stored upstream
  credential.** Back it up with the database, and keep the backup out of the
  database's backup destination if you want a stolen database to stay useless.
  See [the secret](../reference/configuration.md#the-secret).
- **Losing the admin password is harmless.** Delete the file and restart to
  get a new one.

Run the CLI from the same working directory (or with the same environment
variables) as the server. Otherwise `llmproxy key create` mints keys against a
different database and secret than the server reads.

## First login

Without any `LLMPROXY_OIDC_*` configuration the proxy runs in local
single-admin mode: on boot it creates a `local-admin` principal with the admin
role (name configurable via `LLMPROXY_LOCAL_ADMIN_NAME`).

1. Start the server:

   ```bash
   llmproxy serve
   ```

   ```
   llmproxy v1.0.0 listening on http://127.0.0.1:4000
   ```

2. Read the generated password:

   ```bash
   cat .llmproxy/admin-password
   ```

3. Open <http://127.0.0.1:4000> and sign in with it. The UI covers providers,
   model bindings, API keys and usage.

To bootstrap over the API instead, mint the first key with the CLI. This works
before any HTTP credential exists, because the CLI writes to the database
directly:

```bash
llmproxy key create -label bootstrap
```

```
key id:    3f2a...
principal: local-admin
api key:   lp_...
Store it now; it will not be shown again.
```

The plaintext is printed once; only a keyed hash is stored.

The binary has five subcommands: `serve`, `key create|list|delete`,
`relay-token create|list|delete`, `principal create` and `version`.

## The loopback guard

In local mode any key minted on the box grants access, so the server refuses
to bind a non-loopback address. The allowed hosts are `127.0.0.1`, `::1` and
`localhost`.

```bash
llmproxy serve -host 0.0.0.0                       # refuses with an error
llmproxy serve -host 0.0.0.0 -allow-nonlocal       # binds, prints a loud warning
```

`LLMPROXY_ALLOW_NONLOCAL=1` is the environment equivalent of
`-allow-nonlocal`. The guard does not apply once `LLMPROXY_OIDC_ISSUER` is
set, because the identity provider then gates access.

To reach local mode from other machines, keep the bind on loopback and front
it with a reverse proxy you control.

## Run as a systemd service

1. Create the user and the state directory:

   ```bash
   useradd --system --home /var/lib/llmproxy --shell /usr/sbin/nologin llmproxy
   mkdir -p /var/lib/llmproxy && chown llmproxy:llmproxy /var/lib/llmproxy
   ```

2. Write `/etc/systemd/system/llmproxy.service`:

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

   # Hardening: the process only needs its state directory
   NoNewPrivileges=true
   ProtectSystem=strict
   ProtectHome=true
   ReadWritePaths=/var/lib/llmproxy
   PrivateTmp=true

   [Install]
   WantedBy=multi-user.target
   ```

3. Start it:

   ```bash
   systemctl daemon-reload
   systemctl enable --now llmproxy
   systemctl status llmproxy
   ```

4. Mint the first key as the service user, so the CLI and the service share
   state:

   ```bash
   sudo -u llmproxy env LLMPROXY_DATABASE_URL=/var/lib/llmproxy/llmproxy.db \
     LLMPROXY_SECRET_FILE=/var/lib/llmproxy/secret \
     /usr/local/bin/llmproxy key create -label bootstrap
   ```

On SIGINT and SIGTERM the server stops accepting connections, gives in-flight
requests up to 10 seconds and drains the usage-accounting goroutines, so a
restart loses no usage records.

## Where next

- [upgrades.md](upgrades.md) to keep it current.
- [../guides/providers.md](../guides/providers.md) to register your first
  upstream.
- [../guides/sso.md](../guides/sso.md) to replace the admin password with
  OIDC.
- [../reference/configuration.md](../reference/configuration.md) for every
  environment variable.
