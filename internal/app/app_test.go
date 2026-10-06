package app

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/wyrd-company/nats-webhook-relay/internal/config"
)

func startNATS(t *testing.T) (*server.Server, string) {
	t.Helper()
	opts := &server.Options{Port: -1, JetStream: true, StoreDir: t.TempDir(), NoLog: true, NoSigs: true}
	ns, err := server.NewServer(opts)
	if err != nil {
		t.Fatal(err)
	}
	go ns.Start()
	if !ns.ReadyForConnections(10 * time.Second) {
		t.Fatal("nats server not ready")
	}
	t.Cleanup(ns.Shutdown)
	return ns, ns.ClientURL()
}

func sign(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func freePort(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(nil)
	addr := srv.Listener.Addr().String()
	srv.Close()
	return addr
}

// TestEndToEnd drives a webhook through JetStream to an HTTP destination and
// a command destination, then forces a failure into the DLQ.
func TestEndToEnd(t *testing.T) {
	_, url := startNATS(t)
	nc, err := nats.Connect(url)
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	js, _ := jetstream.New(nc)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := js.CreateStream(ctx, jetstream.StreamConfig{Name: "EVENTS", Subjects: []string{"events.>", "dlq.>"}}); err != nil {
		t.Fatal(err)
	}

	var received atomic.Int32
	var lastBody []byte
	var lastHeaders http.Header
	var failing atomic.Bool
	dest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if failing.Load() {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		buf := new(bytes.Buffer)
		_, _ = buf.ReadFrom(r.Body)
		lastBody = buf.Bytes()
		lastHeaders = r.Header.Clone()
		received.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer dest.Close()
	cmdOut := t.TempDir() + "/cmd.txt"

	listen := freePort(t)
	t.Setenv("HOOK_SECRET", "hook-secret")
	t.Setenv("OUT_SECRET", "out-secret")
	raw := fmt.Sprintf(`
server:
  listen: %q
nats:
  url: %q
webhooks:
  - path: /hooks/inventory
    subject: events.{headers.X-Event-Kind}
    message_id_header: X-Delivery
    transform:
      payload: '{"item": payload.item, "qty": payload.qty * 2}'
    verify:
      hmac:
        header: X-Signature
        prefix: "sha256="
        secret: ${HOOK_SECRET}
subjects:
  - name: inventory
    subject: events.restock
    consumer:
      max_deliver: 2
      ack_wait: 2s
      dlq_subject: dlq.inventory
    destinations:
      - name: api
        http:
          url: %q
          timeout: 2s
          sign:
            hmac:
              header: X-Out-Sig
              encoding: base64
              secret: ${OUT_SECRET}
      - name: script
        command:
          argv: ["sh", "-c", "cat >> %s"]
`, listen, url, dest.URL, cmdOut)
	cfg, err := config.Parse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	a, err := New(cfg, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	runCtx, stopRun := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(runCtx) }()
	defer func() {
		stopRun()
		if err := <-done; err != nil {
			t.Errorf("run: %v", err)
		}
	}()
	waitReady(t, "http://"+listen+"/readyz")

	post := func(kind, delivery string, body []byte) int {
		req, _ := http.NewRequest(http.MethodPost, "http://"+listen+"/hooks/inventory", bytes.NewReader(body))
		req.Header.Set("X-Signature", sign("hook-secret", body))
		req.Header.Set("X-Event-Kind", kind)
		req.Header.Set("X-Delivery", delivery)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}

	body := []byte(`{"item":"widget","qty":4}`)
	if code := post("restock", "d1", body); code != http.StatusAccepted {
		t.Fatalf("post status %d", code)
	}
	// Duplicate delivery id is de-duplicated by JetStream.
	if code := post("restock", "d1", body); code != http.StatusAccepted {
		t.Fatalf("dup post status %d", code)
	}
	waitFor(t, func() bool { return received.Load() == 1 })
	time.Sleep(200 * time.Millisecond)
	if n := received.Load(); n != 1 {
		t.Fatalf("destination received %d deliveries, want 1", n)
	}
	if string(lastBody) != `{"item":"widget","qty":8}` {
		t.Errorf("body %s", lastBody)
	}
	if lastHeaders.Get("X-Event-Kind") != "restock" || lastHeaders.Get("X-Relay-Subject") != "events.restock" {
		t.Errorf("headers %v", lastHeaders)
	}
	mac := hmac.New(sha256.New, []byte("out-secret"))
	mac.Write(lastBody)
	if lastHeaders.Get("X-Out-Sig") == "" || lastHeaders.Get("X-Out-Sig") != base64Of(mac.Sum(nil)) {
		t.Errorf("outbound signature %q", lastHeaders.Get("X-Out-Sig"))
	}
	waitFor(t, func() bool {
		b, _ := os.ReadFile(cmdOut)
		return string(b) == `{"item":"widget","qty":8}`
	})

	// A different subject token routes elsewhere and is not consumed.
	if code := post("audit", "d2", body); code != http.StatusAccepted {
		t.Fatalf("post status %d", code)
	}

	// Failure path: destination returns 502 twice, message lands in the DLQ.
	failing.Store(true)
	dlq, err := js.CreateConsumer(ctx, "EVENTS", jetstream.ConsumerConfig{FilterSubject: "dlq.inventory"})
	if err != nil {
		t.Fatal(err)
	}
	if code := post("restock", "d3", []byte(`{"item":"gizmo","qty":1}`)); code != http.StatusAccepted {
		t.Fatalf("post status %d", code)
	}
	msg, err := dlq.Next(jetstream.FetchMaxWait(20 * time.Second))
	if err != nil {
		t.Fatalf("no DLQ message: %v", err)
	}
	if msg.Headers().Get("Relay-Destination") != "api" || msg.Headers().Get("Relay-Attempts") != "2" || msg.Headers().Get("Relay-Origin-Subject") != "events.restock" {
		t.Errorf("dlq headers %v", msg.Headers())
	}
	if string(msg.Data()) != `{"item":"gizmo","qty":2}` {
		t.Errorf("dlq body %s", msg.Data())
	}
	// The script destination has its own consumer and still succeeded.
	waitFor(t, func() bool {
		b, _ := os.ReadFile(cmdOut)
		return string(b) == `{"item":"widget","qty":8}{"item":"gizmo","qty":2}`
	})
}

func base64Of(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

func waitReady(t *testing.T, url string) {
	t.Helper()
	waitFor(t, func() bool {
		resp, err := http.Get(url)
		if err != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	})
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("condition not met in time")
}
