# Trowel

Installs [Trowel](https://wyrd.foo/docs/trowel), a webhook and NATS JetStream relay, and runs it as an s6-overlay longrun service. The Feature requires a Debian/Ubuntu image with s6-overlay 3, such as `ghcr.io/wyrd-company/devcontainers/base`, and supports `amd64` and `arm64`.

```json
{
  "image": "ghcr.io/wyrd-company/devcontainers/base:noble",
  "overrideCommand": false,
  "features": {
    "ghcr.io/wyrd-company/trowel/trowel:0": {}
  }
}
```

## Options

| Option        | Type   | Default                 | Description                                   |
| ------------- | ------ | ----------------------- | --------------------------------------------- |
| `version`     | string | `latest`                | Release to install, such as `0.1.0`.          |
| `listen`      | string | `127.0.0.1:8080`        | Listen address in the starter configuration.  |
| `natsUrl`     | string | `nats://127.0.0.1:4222` | NATS URL in the starter configuration.        |
| `serviceUser` | string | `automatic`             | User that runs Trowel.                        |

See [Dev container Feature](https://wyrd.foo/docs/trowel/devcontainer) for configuration, the secret file, and installed files.
