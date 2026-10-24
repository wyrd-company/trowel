---
title: Dev container Feature
product: trowel
order: 5
---

`ghcr.io/wyrd-company/trowel/trowel` installs Trowel in a dev container and
runs it as an [s6-overlay](https://github.com/just-containers/s6-overlay)
longrun service. It needs a Debian or Ubuntu image with s6-overlay 3 and
`/init` as PID 1, such as the Wyrd Company base image, on `amd64` or `arm64`.
Keep the image command with `"overrideCommand": false`.

```json
{
  "image": "ghcr.io/wyrd-company/devcontainers/base:noble",
  "overrideCommand": false,
  "features": {
    "ghcr.io/wyrd-company/trowel/trowel:0": {
      "natsUrl": "nats://nats:4222"
    }
  }
}
```

The Feature's major version follows Trowel's.

## Options

| Option | Type | Default | Description |
| --- | --- | --- | --- |
| `version` | string | `latest` | Release to install, such as `0.1.0`. `latest` resolves the newest stable release. |
| `listen` | string | `127.0.0.1:8080` | Listen address in the starter configuration. |
| `natsUrl` | string | `nats://127.0.0.1:4222` | NATS URL in the starter configuration. |
| `serviceUser` | string | `automatic` | User that runs Trowel. Automatic prefers the remote user, container user, `vscode`, then `root`. |

The installer verifies the release archive against the release's
`checksums.txt`.

## Configuration

The Feature writes a starter configuration to `/etc/trowel/config.yml` with
one webhook, `POST /hooks/example`, that publishes to `trowel.example`. A
JetStream stream must cover that subject before publishes succeed. Trowel
exits when NATS is unreachable, and s6 restarts it until NATS is up.

Replace the starter configuration by bind-mounting a directory that holds a
`config.yml` over `/etc/trowel`:

```json
{
  "mounts": [
    "source=${localWorkspaceFolder}/.devcontainer/trowel,target=/etc/trowel,type=bind,readonly"
  ]
}
```

Trowel reloads `config.yml` when it changes. Mount the directory rather than
the file: an editor that saves by replacing the file leaves a single-file bind
mount showing the old file until the container is recreated, and neither a
reload nor a service restart sees the new one.

Reload on demand, for example after a secret rotates, with:

```bash
sudo /command/s6-svc -h /run/service/trowel
```

## Secret file

The service loads `NAME=value` lines from `/run/openbao/secrets/trowel.env`
when that file exists, so `${NAME}` references in the configuration resolve
from it. A variable the container environment already sets keeps its
container value. With the OpenBao Agent Feature from
`wyrd-company/devcontainers`, Trowel waits for the file to render:

```hcl
template {
  destination = "/run/openbao/secrets/trowel.env"
  perms       = "0640"
  contents    = <<-EOT
    {{ with secret "secret/data/trowel/github" -}}
    GITHUB_WEBHOOK_SECRET={{ .Data.data.webhook_secret }}
    {{- end }}
  EOT
}
```

Trowel's own `*_vault` references work in a dev container too; see
[Secrets](/docs/trowel/secrets).

## Installed files

| Path | Purpose |
| --- | --- |
| `/usr/local/bin/trowel` | The binary. |
| `/usr/local/bin/trowel-service` | Launcher that loads the secret file and runs `trowel -config /etc/trowel/config.yml`. |
| `/etc/s6-overlay/s6-rc.d/trowel` | The s6 longrun, in the `user` bundle. |
