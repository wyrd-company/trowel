package relay

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
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
