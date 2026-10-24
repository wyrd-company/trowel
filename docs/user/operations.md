---
title: Operations
product: trowel
order: 6
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
| `OTEL_*` | OpenTelemetry export. See [Telemetry](#telemetry). | nothing exported |

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

Trowel watches the directory that holds its configuration file, and, when
the path is a symlink, the directory of the file it points at now. When the
file changes, Trowel waits until the directory is quiet for half a second, reads
the file, and applies it if its content differs from the last attempt.
`SIGHUP` reloads the file even when its content has not changed, which picks
up rotated `*_file` and `*_vault` secrets. The packaged systemd unit maps
`systemctl reload trowel` to `SIGHUP`.

A reload builds the new configuration while the current one keeps serving:

1. Load and validate the file, and read every `*_vault` reference.
2. Connect to NATS again only if the `nats` section changed.
3. Build every webhook and destination, and create or update each consumer.
   Only when every consumer succeeds do the new consumers start taking
   messages, so a configuration that fails here delivers nothing.
4. Route new webhook requests to the new configuration.
5. Stop the replaced consumers taking messages, and let them finish the
   deliveries they hold while replaced webhook requests finish. A replaced
   NATS connection is drained once no configuration still uses it.

If any of steps 1 to 3 fails, Trowel logs `config reload failed; keeping
current configuration` with the reason and keeps the current configuration.
A success logs `config reloaded`.

Some changes still need a restart:

- A change to the `server` section fails the reload.
- `${NAME}` references read the environment Trowel started with.

When step 3 fails after it updated some consumers, those consumers keep their
new settings on the NATS server, although no message was taken from them. A
consumer for a removed relay or destination stays on the server; delete it
with `nats consumer rm`.

## Logs

Logs are JSON lines on stderr. Delivery failures log the subject, the
destination, the attempt, and the reason. An HTTP destination appears in a
failure reason, a `Relay-Error` header, or a span by its display URL. See
[`http` destination](/docs/trowel/configuration#http-destination). Records about a
traced message also carry `trace_id` and `span_id`. A command destination's
stdout is logged on success, and its stderr is part of the failure reason.

## Telemetry

Trowel exports OpenTelemetry traces, metrics, and logs. It reads the
standard `OTEL_*` environment variables and exports nothing until a signal
is configured. A signal is configured when `OTEL_<SIGNAL>_EXPORTER` names
an exporter, or when `OTEL_EXPORTER_OTLP_ENDPOINT` or
`OTEL_EXPORTER_OTLP_<SIGNAL>_ENDPOINT` is set. `<SIGNAL>` is `TRACES`,
`METRICS`, or `LOGS`.

```bash
export OTEL_EXPORTER_OTLP_ENDPOINT=http://collector:4318
export OTEL_RESOURCE_ATTRIBUTES=deployment.environment.name=production
trowel -config /etc/trowel/config.yml
```

| Variable | Effect |
| --- | --- |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | Sends every signal over OTLP to this collector |
| `OTEL_EXPORTER_OTLP_PROTOCOL` | `http/protobuf` (default) or `grpc` |
| `OTEL_TRACES_EXPORTER`, `OTEL_METRICS_EXPORTER`, `OTEL_LOGS_EXPORTER` | `otlp`, `console`, or `none`. Metrics also take `prometheus`. |
| `OTEL_SERVICE_NAME`, `OTEL_RESOURCE_ATTRIBUTES` | Resource attributes. The service name defaults to `trowel`. |
| `OTEL_TRACES_SAMPLER`, `OTEL_TRACES_SAMPLER_ARG` | Sampling. The default samples every trace. |
| `OTEL_SDK_DISABLED` | `true` turns every signal off |

The OpenTelemetry SDK reads the other standard variables, such as headers
and timeouts. These variables are read once at startup; a reload does not
change them. On exit Trowel waits up to 5 seconds to export what it holds.

### Traces

One trace follows a webhook to every destination:

| Span | Kind | Notes |
| --- | --- | --- |
| `POST <path>` | server | One per webhook request. A sender's `traceparent` becomes a link, not the parent, because webhooks usually come from outside. |
| `send <subject template>` | producer | The JetStream publish |
| `process <subject>` | consumer | One per delivery attempt per destination |
| `HTTP <method>` | client | The request to an HTTP destination |
| `send <dlq_subject>` | producer | The dead-letter publish |

Trowel writes the trace context into the `traceparent` and `baggage`
headers of each message it publishes, so other JetStream consumers can join
the trace. HTTP destinations receive it in the same headers. Command
destinations receive it in the `TRACEPARENT`, `TRACESTATE`, and `BAGGAGE`
environment variables, which replace any value Trowel inherited or the
destination's `env` sets for them.

### Central and satellite instances

A central instance can receive webhooks and publish to JetStream while
satellite instances deliver those messages to local processors. The central
`send` span writes its trace context into the NATS message headers. Each
satellite extracts that context, so its `process` span is a child of the
central `send` span in the same trace. Its HTTP client span and the
`traceparent` received by its HTTP destination continue that trace.

Give each satellite a distinct relay or destination name so that it has its
own durable consumer. Those consumers each receive the message and branch
from the same central `send` span. Export the instances' traces to the same
telemetry backend to view them together.

### Metrics

| Metric | Attributes |
| --- | --- |
| `trowel.webhook.requests` | `trowel.webhook.path`, `trowel.outcome`: `published`, `unauthorized`, `too_large`, `bad_request`, `method_not_allowed`, `transform_failed`, `subject_failed`, `publish_failed` |
| `trowel.relay.deliveries` | `trowel.relay.name`, `trowel.destination.name`, `trowel.outcome`: `delivered`, `retry`, `terminated` |
| `trowel.relay.delivery.duration` | as `trowel.relay.deliveries`, in seconds |
| `trowel.relay.dead_letters` | `trowel.relay.name`, `trowel.destination.name`, `trowel.outcome`: `published`, `failed` |
| `trowel.config.reloads` | `trowel.outcome`: `applied`, `failed` |

Trowel also records the standard `http.server.*` and `http.client.*`
metrics and the Go runtime metrics.

### Logs

With logs configured, every record at or above `TROWEL_LOG_LEVEL` is also
exported. Records about a message carry its `trace_id` and `span_id`, on
stderr and in the export.

## Dead letters

When a delivery exhausts `max_deliver`, or its transform fails, Trowel
publishes the original message to `dlq_subject` with `Relay-*` headers that
describe the failure, then terminates it. Inspect them with the NATS CLI:

```bash
nats stream view EVENTS --subject 'dlq.>'
```

Replay a dead letter by publishing its payload back to the original subject
named in `Relay-Origin-Subject`.
