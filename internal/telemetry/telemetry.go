// Package telemetry sets up OpenTelemetry traces, metrics, and logs from
// the standard OTEL_* environment variables.
package telemetry

import (
	"context"
	"errors"
	"os"
	"strings"

	"go.opentelemetry.io/contrib/exporters/autoexport"
	"go.opentelemetry.io/contrib/instrumentation/runtime"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/log/global"
	"go.opentelemetry.io/otel/propagation"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
)

// ScopeName is the instrumentation scope of every tracer, meter, and
// logger Trowel creates.
const ScopeName = "github.com/wyrd-company/trowel"

// Telemetry holds the providers Setup installed.
type Telemetry struct {
	logs     *sdklog.LoggerProvider
	shutdown []func(context.Context) error
}

// Setup installs the W3C trace context and baggage propagators, then a
// provider for each signal whose exporter is configured. A signal is
// configured when OTEL_<SIGNAL>_EXPORTER names an exporter other than
// none, or when an OTLP endpoint is set for it. Unconfigured signals stay
// no-ops, so Trowel exports nothing until told where to. OTEL_SDK_DISABLED
// set to true disables every signal.
func Setup(ctx context.Context, version string) (*Telemetry, error) {
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))
	t := &Telemetry{}
	if strings.EqualFold(os.Getenv("OTEL_SDK_DISABLED"), "true") {
		return t, nil
	}
	res, err := resource.New(ctx,
		resource.WithAttributes(semconv.ServiceName("trowel"), semconv.ServiceVersion(version)),
		resource.WithFromEnv(),
		resource.WithTelemetrySDK(),
	)
	if err != nil {
		return nil, err
	}
	if err := t.setupTraces(ctx, res); err != nil {
		return nil, errors.Join(err, t.Shutdown(ctx))
	}
	if err := t.setupMetrics(ctx, res); err != nil {
		return nil, errors.Join(err, t.Shutdown(ctx))
	}
	if err := t.setupLogs(ctx, res); err != nil {
		return nil, errors.Join(err, t.Shutdown(ctx))
	}
	return t, nil
}

// configured reports whether signal (TRACES, METRICS, or LOGS) has an
// exporter configured.
func configured(signal string) bool {
	if v := os.Getenv("OTEL_" + signal + "_EXPORTER"); v != "" {
		return true
	}
	return os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") != "" || os.Getenv("OTEL_EXPORTER_OTLP_"+signal+"_ENDPOINT") != ""
}

func (t *Telemetry) setupTraces(ctx context.Context, res *resource.Resource) error {
	if !configured("TRACES") {
		return nil
	}
	exp, err := autoexport.NewSpanExporter(ctx)
	if err != nil {
		return err
	}
	if autoexport.IsNoneSpanExporter(exp) {
		return nil
	}
	tp := sdktrace.NewTracerProvider(sdktrace.WithBatcher(exp), sdktrace.WithResource(res))
	otel.SetTracerProvider(tp)
	t.shutdown = append(t.shutdown, tp.Shutdown)
	return nil
}

func (t *Telemetry) setupMetrics(ctx context.Context, res *resource.Resource) error {
	if !configured("METRICS") {
		return nil
	}
	reader, err := autoexport.NewMetricReader(ctx)
	if err != nil {
		return err
	}
	if autoexport.IsNoneMetricReader(reader) {
		return nil
	}
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader), sdkmetric.WithResource(res))
	otel.SetMeterProvider(mp)
	t.shutdown = append(t.shutdown, mp.Shutdown)
	return runtime.Start(runtime.WithMeterProvider(mp))
}

func (t *Telemetry) setupLogs(ctx context.Context, res *resource.Resource) error {
	if !configured("LOGS") {
		return nil
	}
	exp, err := autoexport.NewLogExporter(ctx)
	if err != nil {
		return err
	}
	if autoexport.IsNoneLogExporter(exp) {
		return nil
	}
	lp := sdklog.NewLoggerProvider(sdklog.WithProcessor(sdklog.NewBatchProcessor(exp)), sdklog.WithResource(res))
	global.SetLoggerProvider(lp)
	t.logs = lp
	t.shutdown = append(t.shutdown, lp.Shutdown)
	return nil
}

// Shutdown flushes and stops every provider Setup installed.
func (t *Telemetry) Shutdown(ctx context.Context) error {
	var errs []error
	for _, f := range t.shutdown {
		errs = append(errs, f(ctx))
	}
	t.shutdown = nil
	return errors.Join(errs...)
}

// Handle reports a non-nil error to the OpenTelemetry error handler.
func Handle(err error) {
	if err != nil {
		otel.Handle(err)
	}
}
