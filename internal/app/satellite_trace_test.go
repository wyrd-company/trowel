// relationships:
//
//	references: operations
package app

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"

	"github.com/wyrd-company/trowel/internal/config"
)

// TestTelemetryAcrossCentralAndSatellites uses independent Apps, configurations,
// NATS connections, and background Run contexts. The shared global providers only
// record spans; they do not transfer a span context between Apps. The only channel
// carrying context from the central publisher to either satellite is the NATS
// message, as it would be between processes. Do not run in parallel with tests
// that replace the global telemetry providers.
func TestTelemetryAcrossCentralAndSatellites(t *testing.T) {
	spans, _ := installTestTelemetry(t)
	_, url := startNATS(t)
	nc, err := nats.Connect(url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(nc.Close)
	js, err := jetstream.New(nc)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := js.CreateStream(t.Context(), jetstream.StreamConfig{Name: "EVENTS", Subjects: []string{"events.>"}}); err != nil {
		t.Fatal(err)
	}

	start := func(body string) string {
		t.Helper()
		listen := freePort(t)
		raw := fmt.Sprintf("server:\n  listen: %q\nnats:\n  url: %q\n", listen, url) + body
		cfg, err := config.Parse([]byte(raw))
		if err != nil {
			t.Fatal(err)
		}
		a, err := New(cfg, slog.New(slog.DiscardHandler))
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- a.Run(ctx) }()
		t.Cleanup(func() {
			cancel()
			if err := <-done; err != nil {
				t.Errorf("run: %v", err)
			}
		})
		waitReady(t, "http://"+listen+"/readyz")
		return "http://" + listen
	}

	central := start("webhooks:\n" + hookA)
	const satellites = 2
	headers := make([]chan string, satellites)
	for i := range satellites {
		headers[i] = make(chan string, 1)
		dest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			headers[i] <- r.Header.Get("Traceparent")
			w.WriteHeader(http.StatusOK)
		}))
		t.Cleanup(dest.Close)
		// Distinct relay names produce distinct durable consumers, so each
		// satellite receives the publication rather than sharing its delivery.
		start("subjects:\n" + relayTo(fmt.Sprintf("satellite-%d", i), "events.a", dest.URL))
	}

	resp, err := http.Post(central+"/hooks/a", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("post status %d", resp.StatusCode)
	}
	waitFor(t, func() bool {
		var producers, processes, clients int
		for _, s := range spans.Ended() {
			switch s.Name() {
			case "send events.a":
				producers++
			case "process events.a":
				processes++
			case "HTTP POST":
				clients++
			}
		}
		return producers == 1 && processes == satellites && clients == satellites
	})

	var producer sdktrace.ReadOnlySpan
	processes := map[string]sdktrace.ReadOnlySpan{}
	clients := map[trace.SpanID]sdktrace.ReadOnlySpan{}
	for _, s := range spans.Ended() {
		switch s.Name() {
		case "send events.a":
			producer = s
		case "process events.a":
			for _, attr := range s.Attributes() {
				if attr.Key == "trowel.relay.name" {
					processes[attr.Value.AsString()] = s
				}
			}
		case "HTTP POST":
			clients[s.Parent().SpanID()] = s
		}
	}
	if producer.SpanKind() != trace.SpanKindProducer {
		t.Fatalf("send span kind %v", producer.SpanKind())
	}
	stream, err := js.Stream(t.Context(), "EVENTS")
	if err != nil {
		t.Fatal(err)
	}
	published, err := stream.GetLastMsgForSubject(t.Context(), "events.a")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := published.Header.Get("Traceparent"), traceparentOf(producer.SpanContext()); got != want {
		t.Errorf("published traceparent %q, want producer %q", got, want)
	}
	for i := range satellites {
		name := fmt.Sprintf("satellite-%d", i)
		process := processes[name]
		if process == nil {
			t.Errorf("no process span for %s", name)
			continue
		}
		if process.SpanKind() != trace.SpanKindConsumer || !process.Parent().IsRemote() ||
			process.Parent().SpanID() != producer.SpanContext().SpanID() ||
			process.SpanContext().TraceID() != producer.SpanContext().TraceID() {
			t.Errorf("%s process span is not a remote child of the central send span", name)
		}
		client := clients[process.SpanContext().SpanID()]
		if client == nil {
			t.Errorf("no HTTP client span under %s process span", name)
			continue
		}
		if client.SpanKind() != trace.SpanKindClient || client.SpanContext().TraceID() != producer.SpanContext().TraceID() {
			t.Errorf("%s HTTP client span is not in the central trace", name)
		}
		select {
		case got := <-headers[i]:
			if want := traceparentOf(client.SpanContext()); got != want {
				t.Errorf("%s destination traceparent %q, want HTTP client %q", name, got, want)
			}
		default:
			t.Errorf("%s destination did not receive a request", name)
		}
	}
}
