# Trowel

A small Go service that bridges HTTP webhooks and NATS JetStream in both directions.

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

Endpoints: `/healthz` (process up), `/readyz` (NATS connected and consumers running), plus every configured webhook path. Health is served on the same listener.

## Configuration

`${NAME}` anywhere in a value is replaced from the environment at load time. A reference to an unset variable is a startup error. Every secret field also has a `*_file` variant that reads a mounted file and trims whitespace. Exactly one of the inline or file form must be set.

See [`config.example.yml`](config.example.yml) for a complete annotated example.

### `server`

| Field | Default | Notes |
| --- | --- | --- |
| `listen` | `:8080` | |
| `read_header_timeout` | `10s` | Guards against idle connections holding request slots |

### `nats`

| Field | Default |
| --- | --- |
| `url` | `nats://127.0.0.1:4222` |
| `name` | `trowel` |
| `creds_file` | none |

The relay does not create streams. A stream must already cover every subject a webhook can publish to, every subject a relay consumes, and every DLQ subject. Publishing to an uncovered subject returns `503`.

### `webhooks[]`

| Field | Notes |
| --- | --- |
| `path` | Route, must start with `/`. `POST` only. |
| `max_body_bytes` | Default `10485760`. Larger requests get `413`. |
| `subject` | Template. `{headers.Name}` and `{payload.a.b[0]}` expand from the message **after** transforms. Header lookup is case-insensitive. Expanded values have `.`, `*`, `>`, and whitespace replaced with `_`. A reference that resolves to nothing returns `422`. |
| `message_id_header` | Copied into `Nats-Msg-Id` so JetStream de-duplicates retries from the sender. |
| `transform.headers`, `transform.payload` | JSONata, see below. |
| `verify` | Exactly one of `hmac`, `ed25519`, `static`. Omit to accept unverified requests. |

Responses: `202` after JetStream acknowledges, `401` on failed verification, `413` on oversize body, `422` on transform or subject failure, `503` when the publish fails.

Transport headers (`Host`, `Content-Length`, `Connection`, and similar) are dropped before relaying. The `static` verification header is also dropped so the shared secret never leaves the relay. Signature headers are kept so downstream consumers can re-verify.

### `subjects[]`

| Field | Default | Notes |
| --- | --- | --- |
| `name` | | Used in the durable consumer name `relay-<name>-<destination>` |
| `stream` | resolved from subject | |
| `subject` | | Filter subject for the consumer |
| `transform` | | JSONata, applied before delivery. Transform errors dead-letter immediately. |
| `consumer.deliver_policy` | `all` | `all`, `new`, `last` |
| `consumer.ack_wait` | `30s` | |
| `consumer.max_deliver` | `5` | `-1` for unlimited |
| `consumer.backoff` | none | List of delays. Must have fewer entries than `max_deliver`. |
| `consumer.max_ack_pending` | `64` | |
| `consumer.dlq_subject` | none | Requires a positive `max_deliver` |
| `destinations[]` | | One or more, each exactly one of `http` or `command` |

Each destination gets its own durable consumer. One destination failing does not redeliver to the others.

When a delivery fails and attempts remain, the message is `NAK`ed and JetStream redelivers after `backoff` or `ack_wait`. When attempts are exhausted, or the transform fails, the original message is published to `dlq_subject` with these headers, then terminated:

`Relay-Origin-Subject`, `Relay-Origin-Stream`, `Relay-Origin-Sequence`, `Relay-Name`, `Relay-Destination`, `Relay-Attempts`, `Relay-Error`, `Relay-Failed-At`.

If no `dlq_subject` is set the message is terminated and logged.

#### `http` destination

| Field | Default |
| --- | --- |
| `url` | required |
| `method` | `POST` |
| `headers` | none. Static headers, set after relayed headers. |
| `timeout` | `30s` |
| `sign` | none. Exactly one of `hmac`, `ed25519`, `static`. |

The request carries the message payload as the body, the relayed message headers, the static headers, `X-Relay-Subject`, and the signature. Any status outside `2xx` is a failed delivery.

#### `command` destination

| Field | Default |
| --- | --- |
| `argv` | required. No shell is involved unless you invoke one. |
| `dir` | inherited |
| `env` | none. Extra variables. |
| `env_prefix` | `RELAY_HEADER_` |
| `timeout` | `60s` |

The payload is written to stdin. Each message header becomes `<env_prefix><NAME>` with the name upper-cased and non-alphanumerics replaced by `_`. The subject is `RELAY_SUBJECT`. Non-zero exit or timeout is a failed delivery. Stderr is included in the failure reason.

### Verification and signing strategies

The same three shapes are used for inbound `verify` and outbound `sign`.

**`hmac`**: `algorithm` (`sha1`, `sha256`, `sha512`; default `sha256`), `header`, optional `prefix` such as `sha256=`, `encoding` (`hex` default, or `base64`), `secret` or `secret_file`. The MAC covers the raw body. GitHub: header `X-Hub-Signature-256`, prefix `sha256=`.

**`ed25519`**: `signature_header`, `timestamp_header`, `encoding`, `tolerance` (default `5m`), and `public_key`/`public_key_file` for verify or `private_key`/`private_key_file` for sign. Keys are hex or base64. The private key is a 32-byte seed or a 64-byte key. The signed content is `<unix seconds>.<body>`. Timestamps outside the tolerance window are rejected.

**`static`**: `header` and `secret`/`secret_file`. The header must equal the secret exactly.

### JSONata transforms

Each expression receives:

```json
{"headers": {"X-GitHub-Event": "push", "...": "..."}, "payload": <decoded JSON or the raw string>, "subject": "github.push"}
```

- `transform.payload` replaces the payload with the JSON encoding of the result and sets `Content-Type: application/json`.
- `transform.headers` replaces the entire header set with the returned object. Values are stringified; arrays become repeated headers.
- Header names containing `-` must be quoted with backticks: `` headers.`X-GitHub-Event` ``.

Transforms run before subject expansion, so a computed field can drive routing.

## Development

```sh
task test      # go test ./...
task lint      # go vet + gofmt check
task build     # bin/trowel
task validate CONFIG=config.yml
```

The end-to-end test starts an embedded NATS server. No external services are needed.

## Release

Pushing a SemVer tag without a `v` prefix, for example `0.1.0`, builds a multi-arch image and publishes `ghcr.io/wyrd-company/trowel:0.1.0`, `:0.1`, and `:latest`. Pushes to `main` publish `:edge`.
