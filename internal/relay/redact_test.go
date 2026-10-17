package relay

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/wyrd-company/trowel/internal/config"
	"github.com/wyrd-company/trowel/internal/message"
)

// secretParts are the credential-bearing parts of the destination URLs.
var secretParts = []string{"s3cret-user", "s3cret-pass", "s3cret-path", "s3cret-sig"}

func leaks(s string) string {
	for _, p := range secretParts {
		if strings.Contains(s, p) {
			return p
		}
	}
	return ""
}

// assertSpansRedacted fails when any recorded span attribute, status, or
// event carries a secret part of the URL.
func assertSpansRedacted(t *testing.T, spans *tracetest.SpanRecorder) {
	t.Helper()
	if len(spans.Ended()) == 0 {
		t.Fatal("no spans recorded")
	}
	for _, s := range spans.Ended() {
		for _, kv := range s.Attributes() {
			if p := leaks(kv.Value.Emit()); p != "" {
				t.Errorf("span %q attribute %s carries %s: %s", s.Name(), kv.Key, p, kv.Value.Emit())
			}
		}
		if p := leaks(s.Status().Description); p != "" {
			t.Errorf("span %q status carries %s", s.Name(), p)
		}
		for _, ev := range s.Events() {
			for _, kv := range ev.Attributes {
				if p := leaks(kv.Value.Emit()); p != "" {
					t.Errorf("span %q event %s carries %s", s.Name(), ev.Name, p)
				}
			}
		}
	}
}

func recordSpans(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()
	prev := otel.GetTracerProvider()
	spans := tracetest.NewSpanRecorder()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans)))
	t.Cleanup(func() { otel.SetTracerProvider(prev) })
	return spans
}

func secretURL(base string) string {
	return strings.Replace(base, "http://", "http://s3cret-user:s3cret-pass@", 1) + "/hook/s3cret-path?sig=s3cret-sig&x=1"
}

func TestHTTPDestinationRedactsURLCredentials(t *testing.T) {
	statuses := map[string]int{"delivered": http.StatusOK, "rejected": http.StatusBadGateway}
	for name, status := range statuses {
		t.Run(name, func(t *testing.T) {
			spans := recordSpans(t)
			var got *http.Request
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				got = r
				w.WriteHeader(status)
			}))
			defer srv.Close()
			d, err := NewDestination(config.Destination{Name: "api", HTTP: &config.HTTP{URL: secretURL(srv.URL), Method: "POST", Timeout: time.Second}}, slog.New(slog.DiscardHandler))
			if err != nil {
				t.Fatal(err)
			}
			err = d.Deliver(context.Background(), &message.Message{Subject: "s", Headers: http.Header{}, Payload: []byte("x")})
			if (err == nil) != (status == http.StatusOK) {
				t.Fatalf("deliver: %v", err)
			}
			if err != nil {
				if p := leaks(err.Error()); p != "" {
					t.Errorf("error carries %s: %v", p, err)
				}
			}
			user, pass, _ := got.BasicAuth()
			if got.URL.Path != "/hook/s3cret-path" || got.URL.Query().Get("sig") != "s3cret-sig" || user != "s3cret-user" || pass != "s3cret-pass" {
				t.Errorf("destination received %s with auth %q:%q, want the configured URL", got.URL, user, pass)
			}
			assertSpansRedacted(t, spans)
		})
	}
}

func TestHTTPDestinationRedactsMalformedRedirect(t *testing.T) {
	spans := recordSpans(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "/hook/s3cret-path/%zz?sig=s3cret-sig")
		w.WriteHeader(http.StatusFound)
	}))
	defer srv.Close()
	d, err := NewDestination(config.Destination{Name: "api", HTTP: &config.HTTP{URL: srv.URL, Method: "POST", Timeout: time.Second}}, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	err = d.Deliver(context.Background(), &message.Message{Subject: "s", Headers: http.Header{}, Payload: []byte("x")})
	if err == nil {
		t.Fatal("followed a malformed redirect")
	}
	if p := leaks(err.Error()); p != "" {
		t.Errorf("error carries %s: %v", p, err)
	}
	assertSpansRedacted(t, spans)
}

func TestSafeCauseKeepsHostOnlyErrors(t *testing.T) {
	refused := &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}
	for _, err := range []error{refused, context.DeadlineExceeded, io.EOF} {
		if got := safeCause(err); got != err {
			t.Errorf("safeCause(%v) = %v, want it kept", err, got)
		}
	}
	if got := safeCause(fmt.Errorf("failed to parse Location header %q", "/s3cret-path")); got != errCauseWithheld {
		t.Errorf("a quoting error was kept: %v", got)
	}
}

func TestHTTPDestinationRedactsURLWhenUnreachable(t *testing.T) {
	spans := recordSpans(t)
	srv := httptest.NewServer(http.NotFoundHandler())
	base := srv.URL
	srv.Close()
	d, err := NewDestination(config.Destination{Name: "api", HTTP: &config.HTTP{URL: secretURL(base), Method: "POST", Timeout: time.Second}}, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	err = d.Deliver(context.Background(), &message.Message{Subject: "s", Headers: http.Header{}, Payload: []byte("x")})
	if err == nil {
		t.Fatal("delivered to a closed server")
	}
	if p := leaks(err.Error()); p != "" {
		t.Errorf("error carries %s: %v", p, err)
	}
	assertSpansRedacted(t, spans)
}

func TestCommandEnvDropsInheritedTraceContext(t *testing.T) {
	prev := otel.GetTextMapPropagator()
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))
	t.Cleanup(func() { otel.SetTextMapPropagator(prev) })
	t.Setenv("TRACEPARENT", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	t.Setenv("TRACESTATE", "vendor=outside")
	t.Setenv("BAGGAGE", "secret=outside")
	out := filepath.Join(t.TempDir(), "env")
	d, err := NewDestination(config.Destination{Name: "script", Command: &config.Command{
		Argv: []string{"sh", "-c", "env > " + out}, Timeout: 5 * time.Second, EnvPrefix: "RELAY_HEADER_",
	}}, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	ctx, span := sdktrace.NewTracerProvider().Tracer("t").Start(context.Background(), "process")
	defer span.End()
	if err := d.Deliver(ctx, &message.Message{Subject: "s", Headers: http.Header{}, Payload: []byte("x")}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	env := map[string]string{}
	for _, line := range strings.Split(string(b), "\n") {
		if k, v, ok := strings.Cut(line, "="); ok {
			env[k] = v
		}
	}
	if v, ok := env["TRACESTATE"]; ok {
		t.Errorf("TRACESTATE inherited: %q", v)
	}
	if v, ok := env["BAGGAGE"]; ok {
		t.Errorf("BAGGAGE inherited: %q", v)
	}
	if !strings.Contains(env["TRACEPARENT"], span.SpanContext().TraceID().String()) {
		t.Errorf("TRACEPARENT %q is not the span's", env["TRACEPARENT"])
	}
}
