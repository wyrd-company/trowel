package config

import (
	"strings"
	"testing"
	"time"
)

const minimal = `
webhooks:
  - path: /hooks/orders
    subject: orders.{headers.X-Event-Type}
    verify:
      hmac:
        header: X-Signature
        prefix: "sha256="
        secret: ${TEST_ORDERS_SECRET}
subjects:
  - name: notify
    subject: orders.created
    consumer:
      dlq_subject: orders.dlq
    destinations:
      - name: api
        http:
          url: http://example.invalid/notify
      - name: script
        command:
          argv: ["/bin/true"]
`

func TestParseAppliesDefaults(t *testing.T) {
	t.Setenv("TEST_ORDERS_SECRET", "s3cret")
	cfg, err := Parse([]byte(minimal))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Listen != DefaultListen || cfg.Server.ReadHeaderTimeout != DefaultReadHeaderTimeout {
		t.Errorf("server defaults not applied: %+v", cfg.Server)
	}
	if cfg.Webhooks[0].MaxBodyBytes != DefaultMaxBodyBytes {
		t.Errorf("webhook max_body_bytes default not applied: %d", cfg.Webhooks[0].MaxBodyBytes)
	}
	h := cfg.Webhooks[0].Verify.HMAC
	if h.Algorithm != "sha256" || h.Encoding != "hex" || h.Secret != "s3cret" {
		t.Errorf("hmac defaults/expansion wrong: %+v", h)
	}
	c := cfg.Subjects[0].Consumer
	if c.AckWait != DefaultAckWait || c.MaxDeliver != DefaultMaxDeliver || c.DeliverPolicy != "all" {
		t.Errorf("consumer defaults wrong: %+v", c)
	}
	if cfg.Subjects[0].Destinations[0].HTTP.Method != "POST" || cfg.Subjects[0].Destinations[0].HTTP.Timeout != 30*time.Second {
		t.Errorf("http defaults wrong")
	}
	if cfg.Subjects[0].Destinations[1].Command.Timeout != 60*time.Second || cfg.Subjects[0].Destinations[1].Command.EnvPrefix != DefaultEnvPrefix {
		t.Errorf("command defaults wrong")
	}
}

func TestParseRejectsMissingEnv(t *testing.T) {
	_, err := Parse([]byte(minimal))
	if err == nil || !strings.Contains(err.Error(), "TEST_ORDERS_SECRET") {
		t.Fatalf("expected missing env error, got %v", err)
	}
}

func TestValidateErrors(t *testing.T) {
	cases := map[string]string{
		"empty":            "",
		"two strategies":   "webhooks:\n  - path: /a\n    subject: a\n    verify:\n      hmac: {header: X, secret: s}\n      static: {header: Y, secret: s}\n",
		"no destinations":  "subjects:\n  - name: a\n    subject: a\n",
		"bad policy":       "subjects:\n  - name: a\n    subject: a\n    consumer: {deliver_policy: sometimes}\n    destinations: [{name: d, command: {argv: [x]}}]\n",
		"dlq unlimited":    "subjects:\n  - name: a\n    subject: a\n    consumer: {max_deliver: -1, dlq_subject: d}\n    destinations: [{name: d, command: {argv: [x]}}]\n",
		"backoff too long": "subjects:\n  - name: a\n    subject: a\n    consumer: {max_deliver: 2, backoff: [1s, 2s]}\n    destinations: [{name: d, command: {argv: [x]}}]\n",
		"secret and file":  "webhooks:\n  - path: /a\n    subject: a\n    verify:\n      static: {header: X, secret: s, secret_file: /f}\n",
		"unknown field":    "webhooks:\n  - path: /a\n    subject: a\n    bogus: 1\n",
		"dup path":         "webhooks:\n  - {path: /a, subject: a}\n  - {path: /a, subject: b}\n",
		"negative body":    "webhooks:\n  - {path: /a, subject: a, max_body_bytes: -1}\n",
		"bad wildcard":     "webhooks:\n  - {path: \"/hooks/{\", subject: a}\n",
		"route conflict":   "webhooks:\n  - {path: \"/hooks/{a}\", subject: a}\n  - {path: \"/hooks/{b}\", subject: b}\n",
	}
	for name, raw := range cases {
		if _, err := Parse([]byte(raw)); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

const vaultWebhook = "webhooks:\n  - path: /a\n    subject: a\n    verify:\n      static: {header: X, secret_vault: {path: relay/a, field: token}}\n"

func TestParseVaultReferences(t *testing.T) {
	raw := vaultWebhook + `
nats:
  creds_vault: {mount: nats, path: relay, field: creds}
vault:
  address: unix:///run/sample/vault.sock
  kv_mount: kv/
`
	cfg, err := Parse([]byte(raw))
	if err == nil {
		t.Fatal("expected kv_mount with a trailing slash to be rejected")
	}
	cfg, err = Parse([]byte(strings.Replace(raw, "kv/", "kv", 1)))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Vault.Timeout != DefaultVaultTimeout {
		t.Errorf("vault timeout default not applied: %v", cfg.Vault.Timeout)
	}
	refs := cfg.SecretRefs()
	if len(refs) != 2 {
		t.Fatalf("got %d refs", len(refs))
	}
	if refs[0].Where != "nats.creds_vault" || refs[0].Ref.Mount != "nats" {
		t.Errorf("explicit mount not kept: %+v", refs[0])
	}
	if refs[1].Where != "webhooks[0].verify.static.secret_vault" || refs[1].Ref.Mount != "kv" {
		t.Errorf("kv_mount default not applied: %+v", refs[1])
	}
	*refs[1].Target = "resolved"
	if cfg.Webhooks[0].Verify.Static.Secret != "resolved" {
		t.Error("ref target does not point at the inline field")
	}
}

func TestVaultAddresses(t *testing.T) {
	cases := map[string]bool{
		"https://openbao.example.test:8200": true,
		"http://127.0.0.1:8200":             true,
		"http://localhost:8200":             true,
		"http://[::1]:8200":                 true,
		"unix:///run/sample/vault.sock":     true,
		"http://openbao.example.test:8200":  false,
		"unix://relative/vault.sock":        false,
		"unix:/run/sample/vault.sock":       false,
		"tcp://127.0.0.1:8200":              false,
		"https://user:pass@example.test":    false,
		"":                                  false,
	}
	for addr, ok := range cases {
		_, err := Parse([]byte(vaultWebhook + "vault: {address: \"" + addr + "\"}\n"))
		if (err == nil) != ok {
			t.Errorf("%q: ok=%v, err=%v", addr, ok, err)
		}
	}
}

func TestVaultValidateErrors(t *testing.T) {
	vault := "vault: {address: \"https://openbao.example.test\"}\n"
	cases := map[string]string{
		"no vault section":   vaultWebhook,
		"inline and vault":   strings.Replace(vaultWebhook, "secret_vault", "secret: s, secret_vault", 1) + vault,
		"missing field":      strings.Replace(vaultWebhook, ", field: token", "", 1) + vault,
		"absolute path":      strings.Replace(vaultWebhook, "path: relay/a", "path: /relay/a", 1) + vault,
		"dot dot path":       strings.Replace(vaultWebhook, "path: relay/a", "path: relay/../a", 1) + vault,
		"two auth methods":   vaultWebhook + "vault:\n  address: https://openbao.example.test\n  auth: {token_file: /t, approle: {role_id: r, secret_id_file: /s}}\n",
		"approle no secret":  vaultWebhook + "vault:\n  address: https://openbao.example.test\n  auth: {approle: {role_id: r}}\n",
		"ca file with unix":  vaultWebhook + "vault: {address: \"unix:///run/v.sock\", ca_file: /ca.pem}\n",
		"relative ca file":   vaultWebhook + "vault: {address: \"https://openbao.example.test\", ca_file: ca.pem}\n",
		"creds file and ref": vaultWebhook + vault + "nats: {creds_file: /c, creds_vault: {path: p, field: f}}\n",
		"private key verify": "webhooks:\n  - path: /a\n    subject: a\n    verify:\n      ed25519: {signature_header: S, timestamp_header: T, public_key: k, private_key_vault: {path: p, field: f}}\n" + vault,
	}
	for name, raw := range cases {
		if _, err := Parse([]byte(raw)); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}
