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
