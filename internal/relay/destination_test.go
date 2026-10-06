package relay

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/wyrd-company/nats-webhook-relay/internal/config"
	"github.com/wyrd-company/nats-webhook-relay/internal/message"
)

func TestHTTPDestination(t *testing.T) {
	var got *http.Request
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	d, err := NewDestination(config.Destination{Name: "api", HTTP: &config.HTTP{
		URL: srv.URL + "/in", Method: "PUT", Timeout: time.Second,
		Headers: map[string]string{"X-Static": "yes"},
		Sign:    &config.Sign{Static: &config.Static{Header: "X-Token", Secret: "tok"}},
	}}, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	m := &message.Message{Subject: "s.one", Headers: http.Header{"X-Orig": {"a"}, "Nats-Msg-Id": {"x"}}, Payload: []byte("hi")}
	if err := d.Deliver(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	if got.Method != "PUT" || body != "hi" {
		t.Errorf("method %s body %q", got.Method, body)
	}
	for k, want := range map[string]string{"X-Static": "yes", "X-Token": "tok", "X-Orig": "a", "X-Relay-Subject": "s.one"} {
		if got.Header.Get(k) != want {
			t.Errorf("header %s = %q", k, got.Header.Get(k))
		}
	}
	if got.Header.Get("Nats-Msg-Id") != "" {
		t.Error("nats header leaked")
	}
}

func TestHTTPDestinationStatusError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(500) }))
	defer srv.Close()
	d, _ := NewDestination(config.Destination{Name: "api", HTTP: &config.HTTP{URL: srv.URL, Method: "POST", Timeout: time.Second}}, slog.New(slog.DiscardHandler))
	if err := d.Deliver(context.Background(), message.New("s", nil)); err == nil {
		t.Fatal("500 accepted")
	}
}

func TestCommandDestination(t *testing.T) {
	out := t.TempDir() + "/out"
	d, _ := NewDestination(config.Destination{Name: "sh", Command: &config.Command{
		Argv:      []string{"sh", "-c", `printf '%s|%s|%s' "$RELAY_SUBJECT" "$RELAY_HEADER_X_KIND" "$(cat)" > "$OUT"`},
		Env:       map[string]string{"OUT": out},
		EnvPrefix: "RELAY_HEADER_",
		Timeout:   5 * time.Second,
	}}, slog.New(slog.DiscardHandler))
	m := &message.Message{Subject: "s.two", Headers: http.Header{"X-Kind": {"k"}}, Payload: []byte("data")}
	if err := d.Deliver(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(out)
	if string(b) != "s.two|k|data" {
		t.Errorf("got %q", b)
	}
}

func TestCommandDestinationFailures(t *testing.T) {
	log := slog.New(slog.DiscardHandler)
	d, _ := NewDestination(config.Destination{Name: "sh", Command: &config.Command{
		Argv: []string{"sh", "-c", "echo boom >&2; exit 3"}, Timeout: 5 * time.Second,
	}}, log)
	err := d.Deliver(context.Background(), message.New("s", nil))
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("expected stderr in error, got %v", err)
	}
	d, _ = NewDestination(config.Destination{Name: "sh", Command: &config.Command{
		Argv: []string{"sleep", "5"}, Timeout: 100 * time.Millisecond,
	}}, log)
	start := time.Now()
	err = d.Deliver(context.Background(), message.New("s", nil))
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("expected timeout, got %v", err)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("timeout not enforced")
	}
}
