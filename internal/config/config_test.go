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
	if cfg.Server.Listen != DefaultListen || cfg.Server.MaxBodyBytes != DefaultMaxBodyBytes {
		t.Errorf("server defaults not applied: %+v", cfg.Server)
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
	}
	for name, raw := range cases {
		if _, err := Parse([]byte(raw)); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}
