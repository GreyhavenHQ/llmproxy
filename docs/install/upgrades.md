# Upgrades and backup

Upgrading is: back up, stop, replace the binary, start. There is no separate
migration step and no external migration tool.

## Back up

Take a backup before every upgrade. There are no down migrations, so a backup
is the only way back.

Two things to copy, both of them:

```bash
# SQLite, while the service is running
sqlite3 /var/lib/llmproxy/llmproxy.db ".backup /backup/llmproxy-$(date +%F).db"

# The key secret
cp /var/lib/llmproxy/secret /backup/llmproxy-secret
```

With the service stopped, copying the `.db` file directly works too. For
Postgres, use `pg_dump` as usual; the secret still lives on disk and still
needs its own copy.

The database without the secret is not a usable backup: every API key stops
validating and every stored upstream credential becomes undecryptable. Keep
the two backups in different places.

## Upgrade

```bash
systemctl stop llmproxy
curl -fsSL https://github.com/greyhavenhq/llmproxy/releases/latest/download/llmproxy_Linux_x86_64.tar.gz | tar xz
sudo install -m 0755 llmproxy /usr/local/bin/llmproxy
systemctl start llmproxy
systemctl status llmproxy
```

For containers, change the image tag and redeploy; see
[docker.md](docker.md).

The server applies any pending schema migrations on boot. A migration that
fails is a startup error, not a warning: the server refuses to serve rather
than run against a schema it cannot account for. Check the logs if it does not
come back up.

## What to know about migrations

- **Each migration runs at most once.** The change and its bookkeeping row
  commit in the same transaction, so a database is never left half-migrated.
- **The applied history is queryable:**
  `SELECT version, applied_at FROM schema_migration ORDER BY version`.
- **Downgrades are not supported.** Roll back by restoring the backup.
- **Data a migration cannot invent stays empty rather than wrong.** Keys
  minted before `api_key.key_suffix` existed show an empty display suffix
  until they are recreated.
- **Never regenerate the secret file during an upgrade.** Its format is stable
  and the server never rewrites it.

## Version pinning

Pin the exact version in production. For binaries, install a specific release
rather than `latest`. For containers, use `:1.0.0` rather than `:latest` or
`:1`, both of which move under you.

## Where next

- [binary.md](binary.md) for the systemd setup.
- [docker.md](docker.md) for the container route.
