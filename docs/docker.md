# Docker

## The published image

Tagged releases push a multi-arch (amd64 and arm64) image to the GitHub
Container Registry:

```bash
docker pull ghcr.io/greyhavenhq/llmproxy:latest      # or :1.1.0, or :1
docker run -p 127.0.0.1:4000:4000 -v llmproxy-data:/data \
  -e LLMPROXY_ALLOW_NONLOCAL=1 ghcr.io/greyhavenhq/llmproxy:latest
```

Pin the exact version in production (`:1.1.0`); `:latest` and the major tag
`:1` move under you, and a prerelease tag never moves either of them. The image
is built from the same `Dockerfile` described below, with the version stamped
into the binary.

## Building the image

```bash
just docker        # docker build -t llmproxy .
```

The Dockerfile is a two-stage build: a `golang` Alpine stage compiles the
static binary with the committed web UI embedded (`CGO_ENABLED=0`), and the
runtime stage is plain `alpine` with just the binary. The UI is not rebuilt in
the image; the committed `internal/server/uidist/` is embedded as-is, and the
`ui-drift` CI job guarantees it matches the `ui/` source. What the runtime image
sets up:

- A non-root user `llmproxy` (uid 1000); the process never runs as root.
- A `/data` volume owned by that user, and environment defaults that put all
  state there: `LLMPROXY_DATABASE_URL=/data/llmproxy.db`,
  `LLMPROXY_SECRET_FILE=/data/secret` and
  `LLMPROXY_ADMIN_PASSWORD_FILE=/data/admin-password`. Mount something at
  `/data` or the database, the secret and the admin password vanish with the
  container.
- `LLMPROXY_HOST=0.0.0.0` and `LLMPROXY_PORT=4000`, with port 4000 exposed.
  Binding loopback inside a container would make the service unreachable from
  outside it, so the container binds all interfaces and leaves exposure
  control to Docker's port publishing.
- `ENTRYPOINT ["llmproxy"]` with `CMD ["serve"]`, so the container runs the
  server by default and `docker run llmproxy key create ...` style overrides
  run the CLI against the same `/data` state.

Because the container binds `0.0.0.0`, running in local (no-SSO) mode trips
the loopback guard: the server refuses to start unless
`LLMPROXY_ALLOW_NONLOCAL=1` is set. That is intentional friction. Inside a
container the guard's premise ("loopback means only this machine") no longer
maps to who can reach the socket; the real exposure boundary is the port
mapping, so you have to opt in explicitly and then publish the port narrowly.
In SSO mode the guard does not apply and the variable is unnecessary.

## docker compose

The repository ships two compose files: `docker-compose.yml` for local
development and `docker-compose.coolify.yml` for deployment behind a PaaS
reverse proxy (see [Deploying on Coolify](#deploying-on-coolify)).

`docker-compose.yml` encodes the safe local-mode setup: SQLite state in a
named volume, `LLMPROXY_ALLOW_NONLOCAL=1`, and the port published only on
the host's loopback (`127.0.0.1:4000:4000`) so the proxy stays local to the
host despite the container-internal `0.0.0.0` bind. Widen the port mapping
deliberately, ideally only after switching to SSO.

```bash
docker compose up -d
curl -s http://127.0.0.1:4000/healthz
# {"status":"ok"}
```

### First login

Read the generated admin password and sign in at `http://127.0.0.1:4000`:

```bash
docker compose exec llmproxy cat /data/admin-password
```

Or set `LLMPROXY_ADMIN_PASSWORD` in the compose file to choose it yourself.
From the UI you can register providers, bind models and mint keys.

### Minting the first key over the CLI

The CLI shares the container's `/data`, so bootstrap through `exec`:

```bash
docker compose exec llmproxy llmproxy key create -label bootstrap
# key id:    9d0b...
# principal: local-admin
# api key:   lp_...
# Store it now; it will not be shown again.
```

(First `llmproxy` is the compose service name, second is the binary.)

### SSO mode

Uncomment and fill in the `LLMPROXY_OIDC_*` variables in the compose file
(issuer, client id, client secret, redirect URL, optionally admin and required
groups) and remove `LLMPROXY_ALLOW_NONLOCAL`. The redirect URL must be the
externally reachable `https://your-proxy/auth/callback`, which implies a TLS
reverse proxy in front; see [sso.md](sso.md).

## Switching to Postgres

The compose file contains a commented-out `db` service. Uncomment it, the
`llmproxy-pg` volume, and the `LLMPROXY_DATABASE_URL` line:

```yaml
services:
  llmproxy:
    environment:
      LLMPROXY_DATABASE_URL: postgres://llmproxy:change-me@db:5432/llmproxy

  db:
    image: postgres:17-alpine
    environment:
      POSTGRES_USER: llmproxy
      POSTGRES_PASSWORD: change-me
      POSTGRES_DB: llmproxy
    volumes:
      - llmproxy-pg:/var/lib/postgresql/data
    restart: unless-stopped

volumes:
  llmproxy-pg:
```

The schema is created automatically on boot, same as with SQLite. Note there
is no data migration between backends; switching starts from an empty
database, so plan the switch before you accumulate keys and usage you care
about. The secret file in `/data` stays authoritative either way: keep the
`llmproxy-data` volume (or set `LLMPROXY_KEY_SECRET`), because existing API
keys and encrypted provider credentials are only valid under that secret.

## Deploying on Coolify

`docker-compose.coolify.yml` targets Coolify or any compose-based PaaS. It
differs from the development file in two ways:

- Postgres is the default backend, as a `db` service with a healthcheck the
  app waits on.
- There is no `ports` mapping. The platform's reverse proxy routes to the
  container on `LLMPROXY_PORT` (default 4000); nothing is published on the
  host directly.

Every supported `LLMPROXY_*` variable appears in the file as
`${VAR:-default}`. Coolify parses these placeholders and lists each one as
an editable environment variable in its UI, so configuration happens there
rather than by editing the file. At minimum set `POSTGRES_PASSWORD` (it
feeds both the `db` service and the connection URL). Once the deployment is
reachable over HTTPS, configure the `LLMPROXY_OIDC_*` variables for SSO;
until then the file defaults to local mode with `LLMPROXY_ALLOW_NONLOCAL=1`,
which means anyone who can reach the deployment can use its keys.

The `/data` volume still matters with Postgres: it holds the generated key
secret and admin password files. Alternatively pin `LLMPROXY_KEY_SECRET` and
`LLMPROXY_ADMIN_PASSWORD` through the environment.
