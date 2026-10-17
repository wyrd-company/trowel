package telemetry

import (
	"context"
	"net/http"
	"strings"

	"go.opentelemetry.io/otel"
)

// Inject writes the trace context and baggage in ctx into h, replacing
// any value already there under any capitalization.
func Inject(ctx context.Context, h http.Header) {
	otel.GetTextMapPropagator().Inject(ctx, headerCarrier(h))
}

// Extract returns ctx with the trace context and baggage found in h. NATS
// header names keep the publisher's capitalization, so lookup ignores case.
func Extract(ctx context.Context, h http.Header) context.Context {
	return otel.GetTextMapPropagator().Extract(ctx, headerCarrier(h))
}

// headerCarrier is a TextMapCarrier over headers that matches names
// without regard to case.
type headerCarrier http.Header

func (c headerCarrier) Get(key string) string {
	if v := http.Header(c).Get(key); v != "" {
		return v
	}
	for k, v := range c {
		if strings.EqualFold(k, key) && len(v) > 0 {
			return v[0]
		}
	}
	return ""
}

func (c headerCarrier) Set(key, value string) {
	for k := range c {
		if strings.EqualFold(k, key) {
			delete(c, k)
		}
	}
	http.Header(c).Set(key, value)
}

func (c headerCarrier) Keys() []string {
	keys := make([]string, 0, len(c))
	for k := range c {
		keys = append(keys, k)
	}
	return keys
}
