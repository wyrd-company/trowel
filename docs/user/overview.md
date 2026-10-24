---
title: Overview
product: trowel
order: 1
---

Trowel bridges HTTP webhooks and NATS JetStream in both directions. One
process, one YAML file.

![A signed webhook is accepted, published to JetStream, and delivered to a command, while a forged one is rejected](assets/demo.gif)

- **Webhook to JetStream.** Trowel accepts `POST` requests on configured
  paths, verifies them with HMAC, Ed25519, or a static token, optionally
  reshapes headers and payload with JSONata, expands a subject template, and
  answers `202` only after JetStream acknowledges the publish.
- **JetStream to HTTP or a command.** Trowel consumes subjects with a durable
  consumer per destination, optionally transforms each message, and delivers
  it to an HTTP endpoint, signed if you like, or to a child process on stdin.
  Failed deliveries retry on the consumer's schedule and dead-letter to a
  subject when attempts run out.

Trowel exports OpenTelemetry traces, metrics, and logs, and one trace follows
each webhook through JetStream to every destination. See
[Telemetry](/docs/trowel/operations#telemetry).

Nothing is specific to one sender. GitHub webhooks are the first intended
producer, and the example configuration shows them.

## What Trowel does not do

- It does not create streams. A stream must already cover every subject a
  webhook can publish to, every subject a relay consumes, and every
  dead-letter subject.
- It does not fan one consumer out to many destinations. Each destination has
  its own durable consumer, so one failing endpoint never redelivers to the
  others.
- It does not apply `server` settings or environment variables while it runs.
  Restart it after changing those. Everything else in the configuration, and
  every secret, reloads. See [Reload](/docs/trowel/operations#reload).

## Where it runs

| Target | How |
| --- | --- |
| Linux host | APT, RPM, or AUR package with a systemd unit. See [Installation](/docs/trowel/install). |
| Container | `ghcr.io/wyrd-company/trowel` for `amd64` and `arm64`. |
| Dev container | An s6-overlay Feature. See [Dev container Feature](/docs/trowel/devcontainer). |

Secrets can be inline, read from files, or read from OpenBao or Vault at
startup and on reload. See [Secrets](/docs/trowel/secrets).
