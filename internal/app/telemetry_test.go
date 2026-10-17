package app

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

// installTestTelemetry replaces the global providers with in-memory ones
// for the test.
func installTestTelemetry(t *testing.T) (*tracetest.SpanRecorder, *sdkmetric.ManualReader) {
	t.Helper()
	tp0, mp0, prop0 := otel.GetTracerProvider(), otel.GetMeterProvider(), otel.GetTextMapPropagator()
	spans := tracetest.NewSpanRecorder()
	reader := sdkmetric.NewManualReader()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans)))
	otel.SetMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)))
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))
	t.Cleanup(func() {
		otel.SetTracerProvider(tp0)
		otel.SetMeterProvider(mp0)
		otel.SetTextMapPropagator(prop0)
	})
	return spans, reader
}

func traceparentOf(sc trace.SpanContext) string {
	return fmt.Sprintf("00-%s-%s-%s", sc.TraceID(), sc.SpanID(), sc.TraceFlags())
}

// sum returns the value of an Int64 counter's data point whose attributes
// include want.
func sum(t *testing.T, reader *sdkmetric.ManualReader, name string, want ...attribute.KeyValue) int64 {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	var total int64
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != name {
				continue
			}
			data, ok := m.Data.(metricdata.Sum[int64])
			if !ok {
				t.Fatalf("%s is %T", name, m.Data)
			}
		points:
			for _, dp := range data.DataPoints {
				for _, kv := range want {
					if v, ok := dp.Attributes.Value(kv.Key); !ok || v != kv.Value {
						continue points
					}
				}
				total += dp.Value
			}
		}
	}
	return total
}

func hasMetric(t *testing.T, reader *sdkmetric.ManualReader, name string) bool {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name == name {
				return true
			}
		}
	}
	return false
}

// TestTelemetryFollowsAMessageEndToEnd posts one webhook and follows its
// trace through JetStream to an HTTP destination, a command, and the
// dead-letter subject, then checks the metrics each step records.
func TestTelemetryFollowsAMessageEndToEnd(t *testing.T) {
	spans, reader := installTestTelemetry(t)

	var mu sync.Mutex
	var destTraceparent string
	dest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		destTraceparent = r.Header.Get("Traceparent")
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer dest.Close()
	envOut := filepath.Join(t.TempDir(), "traceparent")

	relayCfg := fmt.Sprintf(`subjects:
  - name: a
    subject: events.a
    consumer:
      max_deliver: 1
      dlq_subject: events.dlq
    destinations:
      - name: api
        http:
          url: %q
      - name: script
        command:
          argv: ["sh", "-c", "printf %%s \"$TRACEPARENT\" > %s"]
      - name: broken
        command:
          argv: ["false"]
`, dest.URL, envOut)
	e := startReloadEnv(t, func(e *reloadEnv) string { return e.base(hookA) + relayCfg })
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	dlq, err := e.js.CreateConsumer(ctx, "EVENTS", jetstream.ConsumerConfig{FilterSubject: "events.dlq"})
	if err != nil {
		t.Fatal(err)
	}

	const outside = "4bf92f3577b34da6a3ce929d0e0e4736"
	if code := e.post("/hooks/a", http.Header{"Traceparent": {"00-" + outside + "-00f067aa0ba902b7-01"}}); code != http.StatusAccepted {
		t.Fatalf("post status %d", code)
	}
	msg, err := dlq.Next(jetstream.FetchMaxWait(15 * time.Second))
	if err != nil {
		t.Fatalf("no dead letter: %v", err)
	}
	waitFor(t, func() bool { b, _ := os.ReadFile(envOut); return len(b) > 0 })
	waitFor(t, func() bool {
		n := 0
		for _, s := range spans.Ended() {
			if s.Name() == "process events.a" {
				n++
			}
		}
		return n == 3
	})

	byName := map[string][]sdktrace.ReadOnlySpan{}
	for _, s := range spans.Ended() {
		byName[s.Name()] = append(byName[s.Name()], s)
	}
	server := byName["POST /hooks/a"]
	if len(server) != 1 || server[0].SpanKind() != trace.SpanKindServer {
		t.Fatalf("server spans %v", server)
	}
	traceID := server[0].SpanContext().TraceID().String()
	if traceID == outside {
		t.Error("the sender's trace context was adopted as the parent")
	}
	if links := server[0].Links(); len(links) != 1 || links[0].SpanContext.TraceID().String() != outside {
		t.Errorf("server span links %v, want the sender's trace", links)
	}
	producer := byName["send events.a"]
	if len(producer) != 1 || producer[0].SpanKind() != trace.SpanKindProducer || producer[0].Parent().SpanID() != server[0].SpanContext().SpanID() {
		t.Fatalf("producer span %v is not a child of the server span", producer)
	}
	for _, s := range byName["process events.a"] {
		if s.SpanKind() != trace.SpanKindConsumer || s.Parent().SpanID() != producer[0].SpanContext().SpanID() {
			t.Errorf("process span %v is not a consumer child of the producer span", s.Attributes())
		}
	}
	if dl := byName["send events.dlq"]; len(dl) != 1 || dl[0].SpanContext().TraceID().String() != traceID {
		t.Errorf("dead-letter spans %v", dl)
	}

	if n := len(byName["GET /readyz"]) + len(byName["GET"]); n != 0 {
		t.Errorf("%d health check spans recorded", n)
	}

	// Each hop carries the span that made it, not a copy of an earlier one.
	processFor := func(dest string) sdktrace.ReadOnlySpan {
		for _, s := range byName["process events.a"] {
			for _, kv := range s.Attributes() {
				if kv.Key == "trowel.destination.name" && kv.Value.AsString() == dest {
					return s
				}
			}
		}
		t.Fatalf("no process span for %s", dest)
		return nil
	}
	client := byName["HTTP POST"]
	if len(client) != 1 || client[0].Parent().SpanID() != processFor("api").SpanContext().SpanID() {
		t.Fatalf("HTTP client spans %v", client)
	}
	mu.Lock()
	got := destTraceparent
	mu.Unlock()
	if want := client[0].SpanContext(); got != traceparentOf(want) {
		t.Errorf("HTTP destination traceparent %q, want %q", got, traceparentOf(want))
	}
	if b, _ := os.ReadFile(envOut); string(b) != traceparentOf(processFor("script").SpanContext()) {
		t.Errorf("command TRACEPARENT %q, want the script's process span", b)
	}
	if tp := msg.Headers().Get("Traceparent"); tp != traceparentOf(byName["send events.dlq"][0].SpanContext()) {
		t.Errorf("dead letter traceparent %q, want the dead-letter span", tp)
	}

	outcome := func(v string) attribute.KeyValue { return attribute.String("trowel.outcome", v) }
	destination := func(v string) attribute.KeyValue { return attribute.String("trowel.destination.name", v) }
	if n := sum(t, reader, "trowel.webhook.requests", attribute.String("trowel.webhook.path", "/hooks/a"), outcome("published")); n != 1 {
		t.Errorf("published webhook requests %d", n)
	}
	for _, d := range []string{"api", "script"} {
		if n := sum(t, reader, "trowel.relay.deliveries", destination(d), outcome("delivered")); n != 1 {
			t.Errorf("%s delivered %d", d, n)
		}
	}
	if n := sum(t, reader, "trowel.relay.deliveries", destination("broken"), outcome("terminated")); n != 1 {
		t.Errorf("broken terminated %d", n)
	}
	if n := sum(t, reader, "trowel.relay.dead_letters", destination("broken"), outcome("published")); n != 1 {
		t.Errorf("dead letters published %d", n)
	}
	for _, name := range []string{"trowel.relay.delivery.duration", "http.server.request.duration", "http.client.request.duration"} {
		if !hasMetric(t, reader, name) {
			t.Errorf("metric %s not recorded", name)
		}
	}

	e.write(e.base(hookA+hookB) + relayCfg)
	e.waitLog(reloaded, 1)
	e.write("webhooks: [")
	e.waitLog(reloadFailed, 1)
	if n := sum(t, reader, "trowel.config.reloads", outcome("applied")); n != 1 {
		t.Errorf("applied reloads %d", n)
	}
	if n := sum(t, reader, "trowel.config.reloads", outcome("failed")); n != 1 {
		t.Errorf("failed reloads %d", n)
	}
}
