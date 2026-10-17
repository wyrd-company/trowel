package telemetry

import (
	"context"
	"net/url"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/embedded"
)

// Redacted replaces sensitive parts of a URL.
const Redacted = "REDACTED"

// RedactURL keeps the scheme, host, and port of raw and replaces the
// userinfo, the path, and every query value, any of which can carry a
// credential, as in signed URLs or webhook URLs with a token in the path.
func RedactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return Redacted
	}
	if u.User != nil {
		u.User = url.UserPassword(Redacted, Redacted)
	}
	if u.Path != "" && u.Path != "/" {
		u.Path, u.RawPath = "/"+Redacted, ""
	}
	if u.RawQuery != "" {
		q := u.Query()
		for k := range q {
			q[k] = []string{Redacted}
		}
		u.RawQuery = q.Encode()
	}
	u.Fragment, u.RawFragment = "", ""
	return u.String()
}

// RedactingTracerProvider returns a TracerProvider over the global one
// whose spans record url.full as display returns it, when url.full is set
// after the span starts, as HTTP client instrumentation does.
func RedactingTracerProvider(display func(url string) string) trace.TracerProvider {
	return redactingProvider{display: display}
}

type redactingProvider struct {
	embedded.TracerProvider
	display func(string) string
}

func (p redactingProvider) Tracer(name string, opts ...trace.TracerOption) trace.Tracer {
	return redactingTracer{Tracer: otel.GetTracerProvider().Tracer(name, opts...), display: p.display}
}

type redactingTracer struct {
	trace.Tracer
	display func(string) string
}

func (t redactingTracer) Start(ctx context.Context, name string, opts ...trace.SpanStartOption) (context.Context, trace.Span) {
	ctx, span := t.Tracer.Start(ctx, name, opts...)
	span = redactingSpan{span, t.display}
	return trace.ContextWithSpan(ctx, span), span
}

type redactingSpan struct {
	trace.Span
	display func(string) string
}

func (s redactingSpan) SetAttributes(kv ...attribute.KeyValue) {
	out := make([]attribute.KeyValue, len(kv))
	for i, a := range kv {
		if a.Key == semconv.URLFullKey {
			a = semconv.URLFull(s.display(a.Value.AsString()))
		}
		out[i] = a
	}
	s.Span.SetAttributes(out...)
}
