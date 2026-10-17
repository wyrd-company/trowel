package app

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
)

// TestMalformedRedirectKeepsURLSecretsOutOfEveryOutput fails a delivery on
// a redirect whose Location carries secrets, and checks the log, the dead
// letter, and every span.
func TestMalformedRedirectKeepsURLSecretsOutOfEveryOutput(t *testing.T) {
	spans, _ := installTestTelemetry(t)
	dest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "/hook/s3cret-path/%zz?sig=s3cret-sig")
		w.WriteHeader(http.StatusFound)
	}))
	defer dest.Close()
	relayCfg := fmt.Sprintf("subjects:\n  - name: a\n    subject: events.a\n    consumer:\n      max_deliver: 1\n      dlq_subject: events.dlq\n    destinations:\n      - name: api\n        http:\n          url: %q\n", dest.URL)
	e := startReloadEnv(t, func(e *reloadEnv) string { return e.base(hookA) + relayCfg })
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	dlq, err := e.js.CreateConsumer(ctx, "EVENTS", jetstream.ConsumerConfig{FilterSubject: "events.dlq"})
	if err != nil {
		t.Fatal(err)
	}
	if code := e.post("/hooks/a", nil); code != http.StatusAccepted {
		t.Fatalf("post status %d", code)
	}
	msg, err := dlq.Next(jetstream.FetchMaxWait(15 * time.Second))
	if err != nil {
		t.Fatalf("no dead letter: %v", err)
	}
	e.waitLog("delivery failed permanently", 1)
	waitFor(t, func() bool {
		for _, s := range spans.Ended() {
			if s.Name() == "process events.a" {
				return true
			}
		}
		return false
	})

	secret := func(s string) bool { return strings.Contains(s, "s3cret") }
	if v := msg.Headers().Get("Relay-Error"); v == "" || secret(v) {
		t.Errorf("Relay-Error %q", v)
	}
	if m := e.logs.find("delivery failed permanently"); secret(m) {
		t.Errorf("log %q", m)
	}
	for _, s := range spans.Ended() {
		if secret(s.Status().Description) {
			t.Errorf("span %q status %q", s.Name(), s.Status().Description)
		}
		for _, kv := range s.Attributes() {
			if secret(kv.Value.Emit()) {
				t.Errorf("span %q attribute %s=%s", s.Name(), kv.Key, kv.Value.Emit())
			}
		}
		for _, ev := range s.Events() {
			for _, kv := range ev.Attributes {
				if secret(kv.Value.Emit()) {
					t.Errorf("span %q event %s %s=%s", s.Name(), ev.Name, kv.Key, kv.Value.Emit())
				}
			}
		}
	}
}
