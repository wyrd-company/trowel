---
title: Installation
product: trowel
order: 2
install: true
---

Trowel supports Linux on `amd64` and `arm64`. Packages install
`/usr/bin/trowel` and a `trowel.service` systemd unit. A fresh installation
stays stopped until you write `/etc/trowel/config.yml` and enable the unit.

## APT

```bash
sudo install -d -m 0755 /etc/apt/keyrings
curl -fsSL https://repo.wyrd.foo/pubkey.gpg |
  sudo tee /etc/apt/keyrings/wyrd-company.gpg >/dev/null
echo "deb [signed-by=/etc/apt/keyrings/wyrd-company.gpg] \
https://repo.wyrd.foo/apt stable main" |
  sudo tee /etc/apt/sources.list.d/wyrd-company.list >/dev/null
sudo apt update
sudo apt install trowel
```

## RPM

```bash
sudo curl -fsSL https://repo.wyrd.foo/wyrd.repo \
  -o /etc/yum.repos.d/wyrd.repo
sudo dnf install trowel
```

## Arch User Repository

```bash
paru -S trowel-bin
```

## Configure and start the service

DEB and RPM packages ship an annotated example at
`/usr/share/doc/trowel/config.example.yml`. The same file is in every release
archive and in the [repository](https://github.com/wyrd-company/trowel/blob/main/config.example.yml).

```bash
sudo install -d -m 0755 /etc/trowel
sudo cp /usr/share/doc/trowel/config.example.yml /etc/trowel/config.yml
sudoedit /etc/trowel/config.yml
sudo trowel -config /etc/trowel/config.yml -validate
sudo systemctl enable --now trowel
```

The unit reads optional `NAME=value` lines from `/etc/trowel/trowel.env`, so
`${NAME}` references in the configuration can stay out of the file itself.
Keep that file mode `0600`.

The unit runs as a systemd dynamic user with a read-only view of the system.
Secret files must be readable by that user; the simplest way is systemd
credentials, which the service reads from `$CREDENTIALS_DIRECTORY`:

```ini
# sudo systemctl edit trowel
[Service]
LoadCredential=github-webhook:/etc/trowel/github-webhook
Environment=GITHUB_WEBHOOK_SECRET_FILE=%d/github-webhook
```

```yaml
secret_file: ${GITHUB_WEBHOOK_SECRET_FILE}
```

Command destinations run as the same dynamic user. When a command needs a
fixed account or write access, override the unit:

```ini
[Service]
DynamicUser=no
User=deploy
ReadWritePaths=/srv/deploy
```

Upgrades restart a running relay on the new binary. Removing the package
stops and disables the unit and leaves `/etc/trowel` in place.

## Container image

```bash
docker run -d --name trowel \
  -p 127.0.0.1:8080:8080 \
  -v "$PWD/config.yml:/config/config.yml:ro" \
  -e NATS_URL=nats://nats.example.internal:4222 \
  ghcr.io/wyrd-company/trowel:latest
```

The image runs as an unprivileged `trowel` user, reads
`/config/config.yml`, and has a health check on `/readyz`. Tags are the
release version, `major.minor`, and `latest`, all from release tags.

The repository's `docker-compose.yml` starts a JetStream-enabled NATS server,
creates the stream the example configuration expects, and runs Trowel:

```bash
GITHUB_WEBHOOK_SECRET=change-me docker compose up --build
```

## Release archive

Each [GitHub release](https://github.com/wyrd-company/trowel/releases) has
`trowel_<version>_linux_<x86_64|aarch64>.tar.gz` and a `checksums.txt`. The
archive holds the binary, the example configuration, and the systemd unit
under `packaging/`.

```bash
sha256sum --check --ignore-missing checksums.txt
tar -xzf trowel_*_linux_x86_64.tar.gz trowel
sudo install -m 0755 trowel /usr/local/bin/trowel
```
