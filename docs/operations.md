---
title: Operations
order: 6
docs: true
---

## Command line

```text
trowel [-config PATH] [-validate]
```

| Flag or variable | Purpose | Default |
| --- | --- | --- |
| `-config`, `TROWEL_CONFIG` | Path to the YAML configuration | `config.yml` |
| `-validate` | Load and validate the configuration, then exit | |
| `TROWEL_LOG_LEVEL` | `debug`, `info`, `warn`, or `error` | `info` |

`SIGHUP` reloads the configuration. See [Reload](#reload).

Exit codes: `0` on a clean shutdown or a valid configuration, `2` when the
configuration does not load, `1` when startup or the relay fails.

## Endpoints

Health is served on the same listener as the webhooks.

| Path | Answers `200` when |
| --- | --- |
| `/healthz` | the process is up |
| `/readyz` | NATS is connected and every consumer is running |

## Startup order

1. Load and validate the configuration, expanding `${NAME}` references.
2. Read every `*_vault` reference from OpenBao or Vault.
3. Connect to NATS.
4. Create or update one durable consumer per destination, named
   `relay-<subject name>-<destination name>`, and start consuming.
5. Serve HTTP and report ready.

`SIGINT` or `SIGTERM` stops accepting requests, stops consumers, and drains
the NATS connection.

## Reload

Trowel watches the directory that holds its configuration file. When the file
changes, Trowel waits until the directory is quiet for half a second, reads
the file, and applies it if its content differs from the last attempt.
`SIGHUP` reloads the file even when its content has not changed, which picks
up rotated `*_file` and `*_vault` secrets. The packaged systemd unit maps
`systemctl reload trowel` to `SIGHUP`.

A reload builds the new configuration while the current one keeps serving:

1. Load and validate the file, and read every `*_vault` reference.
2. Connect to NATS again only if the `nats` section changed.
3. Build every webhook and destination, then create or update each consumer
   and start consuming.
4. Route new webhook requests to the new configuration.
5. Let the replaced consumers finish the deliveries they hold, then stop
   them, and drain the replaced NATS connection.

If any of steps 1 to 3 fails, Trowel logs `config reload failed; keeping
current configuration` with the reason and keeps the current configuration.
A success logs `config reloaded`.

Some changes still need a restart:

- A change to the `server` section fails the reload.
- `${NAME}` references read the environment Trowel started with.

When step 3 fails after it updated some consumers, those consumers keep their
new settings on the NATS server. A consumer for a removed relay or
destination stays on the server; delete it with `nats consumer rm`.

## Logs

Logs are JSON lines on stderr. Delivery failures log the subject, the
destination, the attempt, and the reason. A command destination's stdout is
logged on success, and its stderr is part of the failure reason.

## Dead letters

When a delivery exhausts `max_deliver`, or its transform fails, Trowel
publishes the original message to `dlq_subject` with `Relay-*` headers that
describe the failure, then terminates it. Inspect them with the NATS CLI:

```bash
nats stream view EVENTS --subject 'dlq.>'
```

Replay a dead letter by publishing its payload back to the original subject
named in `Relay-Origin-Subject`.
