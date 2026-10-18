# Changelog

## 0.2.0

### Features

- Export OpenTelemetry traces, metrics, and logs, configured with the standard OTEL_* environment variables, and carry W3C trace context from webhooks through JetStream to HTTP and command destinations and dead letters.
- Reload the configuration when its file changes or on SIGHUP, keeping the running configuration when the new one fails to load, validate, resolve its secrets, or start.
- SIGHUP reloads the configuration instead of stopping the process, and the systemd unit maps systemctl reload trowel to it.
- Name HTTP destinations in logs, dead-letter Relay-Error headers, and spans by their URL as written, with ${NAME} references left unexpanded unless the new http.show lists them and userinfo redacted. A URL written without references shows only its scheme, host, and port, where it previously appeared in full.

### Fixes

- Keep generated Python bytecode out of the repository.
- Publish Linux packages with the AUR service installation profile.
- Document trace continuity from a central webhook publisher to satellite relays with distinct durable consumers.

## 0.1.0

### Features

- Relay HTTP webhooks into NATS JetStream and JetStream messages to HTTP endpoints or commands, with secrets read inline, from files, or from OpenBao or Vault over https or a unix socket, published as a container image, APT, RPM, and AUR packages, and an s6-overlay devcontainer Feature.
