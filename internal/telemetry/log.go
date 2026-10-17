package telemetry

import (
	"context"
	"log/slog"

	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/otel/trace"
)

// LogHandler wraps base so records logged with a span in their context
// carry trace_id and span_id. When log export is configured, records at or
// above level are also exported.
func (t *Telemetry) LogHandler(base slog.Handler, level slog.Leveler) slog.Handler {
	h := slog.Handler(traceHandler{base})
	if t.logs == nil {
		return h
	}
	exported := levelHandler{otelslog.NewHandler(ScopeName, otelslog.WithLoggerProvider(t.logs)), level}
	return slog.NewMultiHandler(h, exported)
}

// traceHandler adds the span in a record's context to the record.
type traceHandler struct{ slog.Handler }

func (h traceHandler) Handle(ctx context.Context, r slog.Record) error {
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		r = r.Clone()
		r.AddAttrs(slog.String("trace_id", sc.TraceID().String()), slog.String("span_id", sc.SpanID().String()))
	}
	return h.Handler.Handle(ctx, r)
}

func (h traceHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return traceHandler{h.Handler.WithAttrs(attrs)}
}

func (h traceHandler) WithGroup(name string) slog.Handler {
	return traceHandler{h.Handler.WithGroup(name)}
}

// levelHandler drops records below level.
type levelHandler struct {
	slog.Handler
	level slog.Leveler
}

func (h levelHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return l >= h.level.Level() && h.Handler.Enabled(ctx, l)
}

func (h levelHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return levelHandler{h.Handler.WithAttrs(attrs), h.level}
}

func (h levelHandler) WithGroup(name string) slog.Handler {
	return levelHandler{h.Handler.WithGroup(name), h.level}
}
