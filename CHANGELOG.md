# Changelog

## 0.3.0

### Breaking

- Start the devcontainer Feature's Trowel service only after the Ansible Feature's ansible-setup s6-rc oneshot succeeds when that service is installed, and install after the Ansible Feature. Without the Ansible Feature, Trowel starts as before, and the OpenBao Agent dependency stays independent.
  
  <!-- bump translation: contract-1 `minor` for `trowel` at 0.2.0 is `major` here, so that the next version stays 0.3.0. -->

### Features

- Read configuration snippets from the conf.d directory beside the config file, such as /etc/trowel/conf.d. Their webhooks and subjects are added in file name order, a snippet that sets server, nats, or vault already set elsewhere is an error, and Trowel reloads when a snippet is added, changed, or removed. DEB and RPM packages and the devcontainer Feature create an empty /etc/trowel/conf.d.

### Fixes

- Publish the public docs from docs/user in the wyrd.foo format, and point the demo recorder at docs/user/assets.

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
