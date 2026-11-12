---
title: Configuration
product: trowel
order: 3
---

Trowel reads one YAML file, given by `-config` or `TROWEL_CONFIG`, and then
any [snippets](#snippets) in the `conf.d` directory beside it. Unknown fields
are errors. Check the configuration, snippets included, without connecting to
anything:

```bash
trowel -config /etc/trowel/config.yml -validate
```

`${NAME}` anywhere in a value is replaced from the environment at load time. A reference to an unset variable is a startup error. Every secret field also has a `*_file` variant that reads a mounted file and trims whitespace, and a `*_vault` variant that reads a field from OpenBao or Vault; see [Secrets](/docs/trowel/secrets). Exactly one form must be set.

Trowel reloads the configuration when the file or a snippet changes and on
`SIGHUP`. A configuration that fails to load or validate leaves the running
one in place. See [Reload](/docs/trowel/operations#reload).

See [`config.example.yml`](https://github.com/wyrd-company/trowel/blob/main/config.example.yml) for a complete annotated example.

## Snippets

A snippet is a YAML file in the `conf.d` directory beside the configuration
file: `/etc/trowel/conf.d` for `/etc/trowel/config.yml`, or `/config/conf.d`
in the container image. Snippets let a package, a provisioning tool, or a
project add its own webhooks and relays without editing the main file.

- A snippet's name ends in `.yml` or `.yaml` and does not start with `.`.
  Other files and subdirectories are ignored, so an editor's swap file or a
  `README.md` does nothing. A symlink to a file is read through.
- Trowel reads the configuration file first, then the snippets in lexical
  order of file name. Prefix names with numbers, such as `10-github.yml`, to
  fix the order.
- `webhooks` and `subjects` lists are joined in that order. A webhook path or
  relay name used twice across files is an error, as within one file.
- `server`, `nats`, and `vault` may each be set in only one file, the
  configuration file or a snippet. Setting one in two files is an error that
  names both files, so no file silently overrides another.
- Each snippet is a complete YAML document, with the same fields and
  `${NAME}` references as the configuration file. Anchors do not reach across
  files.
- An error in a snippet names it. Any error fails the whole configuration:
  Trowel does not start, or, on reload, keeps the running configuration.
- A missing `conf.d` directory holds no snippets. A `conf.d` that exists but is
  not a readable directory is an error.

The DEB and RPM packages and the devcontainer Feature create an empty
`/etc/trowel/conf.d`.

```yaml
# /etc/trowel/conf.d/20-deploy.yml
webhooks:
  - path: /hooks/deploy
    subject: deploy.requested
    verify:
      static:
        header: X-Deploy-Token
        secret: ${DEPLOY_TOKEN}
```

## `server`

| Field | Default | Notes |
| --- | --- | --- |
| `listen` | `:8080` | |
| `read_header_timeout` | `10s` | Guards against idle connections holding request slots |

## `nats`

| Field | Default |
| --- | --- |
| `url` | `nats://127.0.0.1:4222` |
| `name` | `trowel` |
| `creds_file` | none |
| `creds_vault` | none. The contents of a credentials file, read from Vault. |

The relay does not create streams. A stream must already cover every subject a webhook can publish to, every subject a relay consumes, and every DLQ subject. Publishing to an uncovered subject returns `503`.

## `vault`

Required only when a `*_vault` reference is used. See
[Secrets](/docs/trowel/secrets) for addresses, authentication, and examples.

| Field | Default | Notes |
| --- | --- | --- |
| `address` | required | `https://...`, `http://` on a loopback host, or `unix:///path` |
| `namespace` | none | |
| `ca_file` | system roots | Absolute path. Not used with `unix://`. |
| `kv_mount` | `secret` | KV version 2 mount for references without `mount` |
| `timeout` | `10s` | Covers login and every read |
| `auth.token_file` | none | |
| `auth.approle` | none | `mount` (default `approle`), `role_id`, `secret_id_file` |

## `webhooks[]`

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

## `subjects[]`

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

### `http` destination

| Field | Default |
| --- | --- |
| `url` | required |
| `method` | `POST` |
| `headers` | none. Static headers, set after relayed headers. |
| `timeout` | `30s` |
| `sign` | none. Exactly one of `hmac`, `ed25519`, `static`. |
| `show` | none. `${NAME}` references in `url` whose values may appear where the destination is named. |

Logs, `Relay-Error` headers, and spans name the destination by its URL as
written in the configuration. Each `${NAME}` reference stays as written
unless `show` lists it. Userinfo reads `REDACTED:REDACTED`, which needs the
scheme, `://`, and `@` written literally; a URL whose userinfo a reference
could hide or move is named like a URL without references. Such a URL keeps
only its scheme, host, and port, because Trowel cannot tell which part is
secret.

```yaml
http:
  url: https://${HOOK_HOST}/services/${HOOK_TOKEN}
  show: [HOOK_HOST]
  # named as https://hooks.example.test/services/${HOOK_TOKEN}
```

The request carries the message payload as the body, the relayed message headers, the static headers, `X-Relay-Subject`, and the signature. Any status outside `2xx` is a failed delivery.

### `command` destination

| Field | Default |
| --- | --- |
| `argv` | required. No shell is involved unless you invoke one. |
| `dir` | inherited |
| `env` | none. Extra variables. |
| `env_prefix` | `RELAY_HEADER_` |
| `timeout` | `60s` |

The payload is written to stdin. Each message header becomes `<env_prefix><NAME>` with the name upper-cased and non-alphanumerics replaced by `_`. The subject is `RELAY_SUBJECT`. Non-zero exit or timeout is a failed delivery. Stderr is included in the failure reason.

## Verification and signing strategies

The same three shapes are used for inbound `verify` and outbound `sign`.

**`hmac`**: `algorithm` (`sha1`, `sha256`, `sha512`; default `sha256`), `header`, optional `prefix` such as `sha256=`, `encoding` (`hex` default, or `base64`), `secret`, `secret_file`, or `secret_vault`. The MAC covers the raw body. GitHub: header `X-Hub-Signature-256`, prefix `sha256=`.

**`ed25519`**: `signature_header`, `timestamp_header`, `encoding`, `tolerance` (default `5m`), and `public_key`/`public_key_file`/`public_key_vault` for verify or `private_key`/`private_key_file`/`private_key_vault` for sign. Keys are hex or base64. The private key is a 32-byte seed or a 64-byte key. The signed content is `<unix seconds>.<body>`. Timestamps outside the tolerance window are rejected.

**`static`**: `header` and `secret`/`secret_file`/`secret_vault`. The header must equal the secret exactly.

## JSONata transforms

Each expression receives:

```json
{"headers": {"X-GitHub-Event": "push", "...": "..."}, "payload": <decoded JSON or the raw string>, "subject": "github.push"}
```

- `transform.payload` replaces the payload with the JSON encoding of the result and sets `Content-Type: application/json`.
- `transform.headers` replaces the entire header set with the returned object. Values are stringified; arrays become repeated headers.
- Header names containing `-` must be quoted with backticks: `` headers.`X-GitHub-Event` ``.

Transforms run before subject expansion, so a computed field can drive routing.
