<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="docs/assets/icon-dark.svg">
    <img src="docs/assets/icon-light.svg" alt="Trowel icon" width="160">
  </picture>
</p>

# Trowel

A small Go service that bridges HTTP webhooks and NATS JetStream in both directions. Documentation lives at [wyrd.foo/docs/trowel](https://wyrd.foo/docs/trowel), published from [`docs/`](docs).

- **Webhook → NATS**: accepts `POST` requests on configured paths, verifies them, optionally transforms headers and payload with JSONata, expands a subject template, and publishes to JetStream with a stream acknowledgement.
- **NATS → HTTP or command**: consumes JetStream subjects with a durable consumer per destination, optionally transforms, then delivers to an HTTP endpoint (with optional signing) or to a child process. Failed deliveries retry per the consumer settings and dead-letter to a subject when attempts run out.

The first intended producer is GitHub webhooks, but nothing is GitHub-specific.

## Run

```sh
cp config.example.yml config.yml     # edit paths, subjects, secrets
export NATS_URL=nats://localhost:4222 GITHUB_WEBHOOK_SECRET=...
go run ./cmd/trowel -config config.yml -validate
go run ./cmd/trowel -config config.yml
```

With Docker Compose, which starts a JetStream-enabled NATS server alongside the relay:

```sh
GITHUB_WEBHOOK_SECRET=... docker compose up --build
```

Flags and environment:

| Flag / variable | Purpose | Default |
| --- | --- | --- |
| `-config`, `TROWEL_CONFIG` | Path to the YAML config | `config.yml` |
| `-validate` | Load and validate the config, then exit | |
| `TROWEL_LOG_LEVEL` | `debug`, `info`, `warn`, `error` | `info` |

Trowel reloads the config when the file changes and on `SIGHUP`, and keeps the running config when the new one fails. See [`docs/operations.md`](docs/operations.md#reload).

Endpoints: `/healthz` (process up), `/readyz` (NATS connected and consumers running), plus every configured webhook path. Health is served on the same listener.

Secrets can be inline, read from files, or read from OpenBao or Vault over https or a unix socket at startup and on reload. See [`docs/secrets.md`](docs/secrets.md), [`docs/configuration.md`](docs/configuration.md), and the annotated [`config.example.yml`](config.example.yml).

## Install

- APT, RPM, and AUR (`trowel-bin`) packages from [repo.wyrd.foo](https://repo.wyrd.foo), with a systemd unit. See [`docs/install.md`](docs/install.md).
- Container image `ghcr.io/wyrd-company/trowel`.
- Release archives and checksums on [GitHub releases](https://github.com/wyrd-company/trowel/releases).
- Devcontainer Feature `ghcr.io/wyrd-company/trowel/trowel`, which runs Trowel under s6-overlay. See [`docs/devcontainer.md`](docs/devcontainer.md). Its source is [`features/`](features).

## Development

Needs Go and [Task](https://taskfile.dev).

```sh
task test      # go test ./...
task check     # vet, format, build, GoReleaser, scripts, workflows, intentional, tests
task build     # bin/trowel
task validate CONFIG=config.yml
```

The end-to-end test starts an embedded NATS server. The OpenBao tests run a real `bao server -dev` and skip without one; set `TROWEL_BAO` to the binary or put `bao` on `PATH`. `task ci` fails when they skip.

Feature tests need Docker and the [devcontainer CLI](https://github.com/devcontainers/cli): `task feature:test` and `task feature:runtime` install a binary built from the checkout.

`task demo` records `docs/assets/demo.gif` from [`docs/demo/demo.tape`](docs/demo/demo.tape) with vhs in Docker.

## Releases

[Intentional](https://wyrd.foo/docs/intentional) is the only version and release mechanism. Record a user-visible change with `intentional add`, and release with `intentional plan`, `apply`, and `tag`. `apply` writes `CHANGELOG.md` and the Feature version. Tags are bare SemVer values such as `0.1.0`.

Pushing a release tag:

1. Proves the pinned [repo.wyrd.foo](https://github.com/wyrd-company/repo.wyrd.foo) publisher contract, then creates the GitHub release with archives, DEB and RPM packages, `checksums.txt`, and notes from `CHANGELOG.md`.
2. Publishes the multi-arch image as `:<version>`, `:<major>.<minor>`, and `:latest`. Nothing publishes from `main` alone.
3. After the release succeeds, submits the release manifest to repo.wyrd.foo, publishes `docs/` to wyrd.foo, and publishes the devcontainer Feature.

`REPO_WYRD_FOO_PUBLISHER_COMMIT` in `.github/workflows/release.yml` pins the repo.wyrd.foo commit whose publisher contract and `config/products.json` each release is checked against. Move it when repo.wyrd.foo changes that contract or trowel's allowlist entry.

Publishing uses these repository or organization secrets:

- `REPO_WYRD_FOO_PUBLISHER_APP_ID`
- `REPO_WYRD_FOO_PUBLISHER_PRIVATE_KEY`
- `WYRD_TOOLS_DOCS_PUBLISHER_APP_ID`
- `WYRD_TOOLS_DOCS_PUBLISHER_PRIVATE_KEY`

Recover a failed step by dispatching Submit package release, Publish docs, or Publish devcontainer Feature with the release tag.
