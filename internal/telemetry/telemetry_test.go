package telemetry

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/log/global"
	"go.opentelemetry.io/otel/propagation"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

var otelEnv = []string{
	"OTEL_SDK_DISABLED",
	"OTEL_TRACES_EXPORTER", "OTEL_METRICS_EXPORTER", "OTEL_LOGS_EXPORTER",
	"OTEL_EXPORTER_OTLP_ENDPOINT",
	"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "OTEL_EXPORTER_OTLP_METRICS_ENDPOINT", "OTEL_EXPORTER_OTLP_LOGS_ENDPOINT",
}

// clearEnv unsets every variable that enables a signal for the test, and
// restores the global providers afterwards.
func clearEnv(t *testing.T) {
	t.Helper()
	for _, k := range otelEnv {
		t.Setenv(k, "")
	}
	tp, mp, lp, prop := otel.GetTracerProvider(), otel.GetMeterProvider(), global.GetLoggerProvider(), otel.GetTextMapPropagator()
	t.Cleanup(func() {
		otel.SetTracerProvider(tp)
		otel.SetMeterProvider(mp)
		global.SetLoggerProvider(lp)
		otel.SetTextMapPropagator(prop)
	})
}

func TestConfigured(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		want bool
	}{
		{"nothing set", nil, false},
		{"shared endpoint", map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": "http://collector:4318"}, true},
		{"own endpoint", map[string]string{"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT": "http://collector:4318/v1/traces"}, true},
		{"other signal's endpoint", map[string]string{"OTEL_EXPORTER_OTLP_LOGS_ENDPOINT": "http://collector:4318/v1/logs"}, false},
		{"exporter named", map[string]string{"OTEL_TRACES_EXPORTER": "console"}, true},
		{"other signal's exporter", map[string]string{"OTEL_METRICS_EXPORTER": "console"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			clearEnv(t)
			for k, v := range c.env {
				t.Setenv(k, v)
			}
			if got := configured("TRACES"); got != c.want {
				t.Errorf("configured(TRACES) = %v, want %v", got, c.want)
			}
		})
	}
}

func TestSetupWithoutExporterInstallsNothing(t *testing.T) {
	clearEnv(t)
	before := otel.GetTracerProvider()
	tel, err := Setup(context.Background(), "test")
	if err != nil {
		t.Fatal(err)
	}
	if len(tel.shutdown) != 0 || tel.logs != nil {
		t.Errorf("installed %d providers, logs %v", len(tel.shutdown), tel.logs)
	}
	if otel.GetTracerProvider() != before {
		t.Error("tracer provider replaced")
	}
	if _, ok := otel.GetTextMapPropagator().(propagation.TextMapPropagator); !ok || len(otel.GetTextMapPropagator().Fields()) == 0 {
		t.Error("propagator not installed")
	}
}

func TestSetupDisabledInstallsNothing(t *testing.T) {
	clearEnv(t)
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://127.0.0.1:1")
	t.Setenv("OTEL_SDK_DISABLED", "true")
	tel, err := Setup(context.Background(), "test")
	if err != nil {
		t.Fatal(err)
	}
	if len(tel.shutdown) != 0 {
		t.Errorf("installed %d providers while disabled", len(tel.shutdown))
	}
}

func TestSetupWithEndpointInstallsEverySignal(t *testing.T) {
	clearEnv(t)
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://127.0.0.1:1")
	t.Setenv("OTEL_EXPORTER_OTLP_PROTOCOL", "http/protobuf")
	t.Setenv("OTEL_TRACES_EXPORTER", "none")
	tel, err := Setup(context.Background(), "test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = tel.Shutdown(ctx)
	})
	if _, ok := otel.GetTracerProvider().(*sdktrace.TracerProvider); ok {
		t.Error("traces installed although OTEL_TRACES_EXPORTER is none")
	}
	if tel.logs == nil {
		t.Error("logs not installed")
	}
	if len(tel.shutdown) != 2 {
		t.Errorf("installed %d providers, want metrics and logs", len(tel.shutdown))
	}
}

func TestSetupRejectsUnknownExporter(t *testing.T) {
	clearEnv(t)
	t.Setenv("OTEL_TRACES_EXPORTER", "carrier-pigeon")
	if _, err := Setup(context.Background(), "test"); err == nil {
		t.Fatal("unknown exporter accepted")
	}
}

const traceparent = "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01"

func TestExtractIgnoresHeaderCase(t *testing.T) {
	clearEnv(t)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	ctx := Extract(context.Background(), http.Header{"TraceParent": {traceparent}})
	if got := trace.SpanContextFromContext(ctx).TraceID().String(); got != "0af7651916cd43dd8448eb211c80319c" {
		t.Errorf("trace id %q", got)
	}
}

func TestInjectReplacesExistingTraceContext(t *testing.T) {
	clearEnv(t)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	tp := sdktrace.NewTracerProvider()
	ctx, span := tp.Tracer("t").Start(context.Background(), "s")
	defer span.End()
	h := http.Header{"TraceParent": {traceparent}, "Traceparent": {traceparent}}
	Inject(ctx, h)
	if len(h) != 1 || len(h["Traceparent"]) != 1 {
		t.Fatalf("headers %v", h)
	}
	if !strings.Contains(h.Get("Traceparent"), span.SpanContext().TraceID().String()) {
		t.Errorf("traceparent %q not from the span", h.Get("Traceparent"))
	}
}

// memExporter keeps exported log records.
type memExporter struct {
	mu   sync.Mutex
	recs []string
}

func (e *memExporter) Export(_ context.Context, recs []sdklog.Record) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, r := range recs {
		e.recs = append(e.recs, r.Body().AsString())
	}
	return nil
}
func (e *memExporter) Shutdown(context.Context) error   { return nil }
func (e *memExporter) ForceFlush(context.Context) error { return nil }

// textRecorder keeps formatted records from the stderr side.
type textRecorder struct {
	mu  sync.Mutex
	out strings.Builder
}

func (r *textRecorder) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.out.Write(p)
}

func TestLogHandlerAddsTraceAndExportsAtLevel(t *testing.T) {
	exp := &memExporter{}
	tel := &Telemetry{logs: sdklog.NewLoggerProvider(sdklog.WithProcessor(sdklog.NewSimpleProcessor(exp)))}
	var text textRecorder
	log := slog.New(tel.LogHandler(slog.NewTextHandler(&text, &slog.HandlerOptions{Level: slog.LevelInfo}), slog.LevelInfo))

	ctx, span := sdktrace.NewTracerProvider().Tracer("t").Start(context.Background(), "s")
	defer span.End()
	log.InfoContext(ctx, "kept")
	log.Debug("dropped")

	if !strings.Contains(text.out.String(), "trace_id="+span.SpanContext().TraceID().String()) {
		t.Errorf("stderr record lacks trace_id: %s", text.out.String())
	}
	if strings.Contains(text.out.String(), "dropped") {
		t.Error("stderr kept a record below level")
	}
	exp.mu.Lock()
	defer exp.mu.Unlock()
	if len(exp.recs) != 1 || exp.recs[0] != "kept" {
		t.Errorf("exported %v, want only the info record", exp.recs)
	}
}

func TestLogHandlerWithoutLogsExportsNothing(t *testing.T) {
	var text textRecorder
	h := (&Telemetry{}).LogHandler(slog.NewTextHandler(&text, nil), slog.LevelInfo)
	if _, ok := h.(traceHandler); !ok {
		t.Errorf("handler %T, want only the stderr handler", h)
	}
}
