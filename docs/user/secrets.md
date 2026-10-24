---
title: Secrets
product: trowel
order: 4
---

Every secret in the configuration takes one of three forms. Exactly one must
be set.

| Form | Example | Read |
| --- | --- | --- |
| Inline | `secret: ${GITHUB_WEBHOOK_SECRET}` | at load, from the value or the environment |
| File | `secret_file: /run/secrets/github-webhook` | at startup and on reload, trimmed |
| Vault | `secret_vault: {path: trowel/github, field: webhook-secret}` | at startup and on reload, from OpenBao or Vault |

The fields with these forms are `secret` (HMAC and static), `public_key` and
`private_key` (Ed25519), and `nats.creds`, which has only the file and Vault
forms: `creds_file` or `creds_vault`. A `creds_vault` field holds the whole
text of a NATS `.creds` file.

## OpenBao and Vault

A `*_vault` reference names a field of a KV version 2 secret:

```yaml
verify:
  hmac:
    header: X-Hub-Signature-256
    prefix: "sha256="
    secret_vault:
      mount: kv              # optional; defaults to vault.kv_mount
      path: trowel/github    # no leading or trailing /
      field: webhook-secret  # must hold a non-empty string
```

Any reference requires a `vault` section:

```yaml
vault:
  address: https://openbao.example.internal:8200
  namespace: team-a          # optional
  ca_file: /etc/trowel/openbao-ca.pem  # optional, https only
  kv_mount: secret           # default secret
  timeout: 10s               # default 10s, covers login and every read
  auth:                      # optional, at most one method
    approle:
      mount: approle         # default approle
      role_id: ${TROWEL_VAULT_ROLE_ID}
      secret_id_file: /run/secrets/openbao-secret-id
```

Trowel reads every reference at startup, before it connects to NATS, and
again on every reload, then uses the values like inline secrets. Each secret
path is read once per load even when several fields come from it. A missing
secret, a missing field, a field that is not a string, or a denied read stops
startup, or fails the reload, with the location of the reference. Send
`SIGHUP` to pick up a rotated secret. A reload whose `nats.creds_file` or
`nats.creds_vault` contents changed opens a new NATS connection with them. See
[Reload](/docs/trowel/operations#reload).

`-validate` checks references and the `vault` section without contacting the
server.

### Addresses

| Address | Use |
| --- | --- |
| `https://host:port` | A server reached over TLS. Use `ca_file` for a private CA. |
| `http://127.0.0.1:port` | Plain HTTP, accepted only on a loopback host, such as a local OpenBao Agent or Proxy listener. |
| `unix:///path/to/socket` | A local socket, such as a Wyrwood container socket or an OpenBao Agent or Proxy listener. |

Trowel never reads `VAULT_*` or `BAO_*` environment variables. Put the address
in the configuration, or reference the variable yourself:
`address: ${VAULT_ADDR}`.

### Authentication

| `auth` | Behaviour |
| --- | --- |
| omitted | Requests carry no token. Use this when the socket or agent injects one. |
| `token_file` | Sends the token in the file. Trowel never revokes it. |
| `approle` | Logs in with `role_id` and the secret ID in `secret_id_file`, reads the secrets, then revokes the login token. |

The secret ID has no inline form, so it never sits in the configuration.

A policy for the AppRole only needs read access to the data path:

```hcl
path "kv/data/trowel/*" {
  capabilities = ["read"]
}
```

### Inside a Wyrwood container

[Wyrwood](/docs/wyrwood) gives a bound container an OpenBao socket that
already carries the container's own token, usually
`VAULT_ADDR=unix:///run/wyrwood/vault.sock`. Point Trowel at it with no `auth`
section:

```yaml
vault:
  address: ${VAULT_ADDR}
  kv_mount: kv
```

The container's OpenBao policy decides which secrets Trowel can read.

### Behind an OpenBao Agent or Proxy

An agent or proxy with `use_auto_auth_token` adds its own token, so again
leave out `auth` and point `address` at its listener, either
`unix:///run/openbao/agent.sock` or `http://127.0.0.1:8100`.
