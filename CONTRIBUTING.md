# Contributing

Thanks for looking. Bug reports, questions and patches are all welcome.

## Development setup

You need a recent Go toolchain (`go.mod` pins the version and `GOTOOLCHAIN=auto`
fetches it) and [just](https://just.systems). Node is only needed if you touch
the web UI.

```bash
git clone https://github.com/greyhavenhq/llmproxy.git && cd llmproxy
just build      # static binary -> bin/llmproxy
just test       # go test ./...
just race       # race detector, -count=1
just lint       # golangci-lint
just ui         # rebuild the embedded web UI (requires node)
just stress     # load harness, see docs/reference/performance.md
```

`just` on its own lists every recipe.

Tests are the fast path to understanding the codebase: most server tests go
through `internal/server/testenv_test.go`, which boots a real server against a
temporary SQLite database. `go test ./internal/server -run TestName` runs one.

## Before you open a pull request

- `just lint`, `just test` and `just race` pass.
- If you changed anything under `ui/`, run `just ui` and commit the
  regenerated `internal/server/uidist/`. That directory is committed on
  purpose so a plain `go build` embeds the current UI without a node
  toolchain; the `ui-drift` CI job fails if it is stale.
- If you changed configuration, document the variable in
  `docs/reference/configuration.md` and expose it in `docker-compose.coolify.yml` as
  `${VAR:-default}`.
- New behaviour comes with a test.

## Writing documentation

`docs/` is organised by what the reader is trying to do: `getting-started.md`
to try it, `install/` to deploy it, `guides/` to operate it, `reference/` to
look something up, `concepts/` to understand why. Put a new page where its
reader would look for it, and add it to the nav in `mkdocs.yml` and the router
in `docs/README.md`.

Six rules keep the task pages consistent:

- **Second person, imperative, present tense.** "Create a key", not "a key can
  be created".
- **One action per step.** Number the steps, and give each one its command and
  the output that says it worked.
- **Happy path first.** Open with the goal and the prerequisites, then the
  minimal path. Variations (TLS, Postgres, overrides) come after, under their
  own headings, so they can be skipped.
- **Rationale is one sentence and a link.** The why belongs in
  `concepts/architecture.md`, stated once.
- **Consequences stay, justifications go.** "Losing the secret invalidates
  every key" survives the cut. "The alternative would buy microseconds" does
  not, outside `concepts/`.
- **Reference pages are tables**, not narrative. The tutorial stays one tested
  path.

`concepts/architecture.md` and the README are the exception: they explain
rather than instruct, and their voice is deliberately different.

Check a change with `just docs` (local preview) or `mkdocs build --strict`,
which fails on broken internal links. The `Docs` workflow runs the strict
build on every PR that touches `docs/`.

## Design rules that are not negotiable

Read [docs/concepts/architecture.md](docs/concepts/architecture.md) before any structural change.
It states every rule with its rationale. The ones that will get a patch
rejected outright:

- **No request or response content is ever persisted.** No prompt, completion
  or embedding input may reach the database or the logs. Two tests enforce
  this: a schema test rejects content-shaped column names, and an integration
  test sends a marker string through a real request and asserts it never
  reaches disk. Never add a column, log field or debug dump that could hold a
  body.
- **The database is the single source of truth.** Caches are short-TTL hints
  invalidated on admin writes. Never hold a database connection across an
  upstream call.
- **Bodies pass through byte-for-byte.** Proxy metadata goes in `x-llmproxy-*`
  headers, never into response bodies.
- **Upstream credentials attach at exactly one code site**, shared by the
  unary and streaming paths, with a regression test on both.
- **Prometheus labels stay aggregate-only** (provider, model, endpoint, unit,
  outcome); never principals or keys.

The non-goals table in `docs/concepts/architecture.md` is deliberate. If a feature is
listed there, open an issue to discuss before writing code.

## Commit messages

Commit subjects follow [Conventional Commits](https://www.conventionalcommits.org/),
because the release notes are generated from them:

```
feat(usage): per-app consumption from x-llmproxy-tags
fix(store): do not leak the transaction on a failed migration
docs: describe the error_kind classification
```

`feat:` and `fix:` land in their own sections of the release notes, `docs:`
in the documentation section, and `chore:`, `test:`, `refactor:`, `ci:` and
`style:` are filtered out. Anything else ends up under "Other work", so use a
prefix even when the change is small.

## Releases

Releases are cut by pushing a tag; there is no release PR and no bot.

1. Check that `main` is green.
2. Pick the version. Semver: a breaking change to the HTTP API, the CLI or the
   configuration is a major bump, a new capability is a minor bump, and
   everything else is a patch. A schema migration on its own is not breaking:
   migrations are applied automatically and forward-only.
3. Tag and push:

   ```bash
   git tag v1.2.0 && git push origin v1.2.0
   ```

4. `.github/workflows/release.yml` runs two jobs. `binaries` runs goreleaser:
   static binaries for linux/amd64, linux/arm64 and darwin/arm64, tar.gz
   archives, `checksums.txt`, and a GitHub release with notes grouped from the
   commit subjects. `image` builds `./Dockerfile` with buildx and pushes
   multi-arch `ghcr.io/greyhavenhq/llmproxy` tagged `:X.Y.Z`, plus `:X` and
   `:latest` for a stable tag.
5. Skim the generated notes on the release page and edit them there if a
   commit subject reads badly.

To check a `.goreleaser.yaml` change without publishing anything:

```bash
goreleaser release --snapshot --clean
```

`CHANGELOG.md` carries a curated entry for 1.0.0. Everything after it lives on
the releases page.
