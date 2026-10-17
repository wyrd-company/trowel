package relay

import (
	"context"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/wyrd-company/trowel/internal/telemetry"
)

// Delivery outcomes recorded on spans and metrics.
const (
	outcomeDelivered  = "delivered"
	outcomeRetry      = "retry"
	outcomeTerminated = "terminated"
)

// metrics are the instruments one worker records to.
type metrics struct {
	attrs       attribute.Set
	deliveries  metric.Int64Counter
	duration    metric.Float64Histogram
	deadLetters metric.Int64Counter
}

func newMetrics(relay, destination string) metrics {
	meter := otel.Meter(telemetry.ScopeName)
	m := metrics{attrs: attribute.NewSet(
		attribute.String("trowel.relay.name", relay),
		attribute.String("trowel.destination.name", destination),
	)}
	var err error
	m.deliveries, err = meter.Int64Counter("trowel.relay.deliveries",
		metric.WithUnit("{delivery}"),
		metric.WithDescription("Delivery attempts, by outcome: delivered, retry, or terminated."))
	telemetry.Handle(err)
	m.duration, err = meter.Float64Histogram("trowel.relay.delivery.duration",
		metric.WithUnit("s"),
		metric.WithDescription("Time to transform and deliver one message, by outcome."))
	telemetry.Handle(err)
	m.deadLetters, err = meter.Int64Counter("trowel.relay.dead_letters",
		metric.WithUnit("{message}"),
		metric.WithDescription("Dead-letter publishes, by outcome: published or failed."))
	telemetry.Handle(err)
	return m
}

func (m metrics) delivery(ctx context.Context, outcome string, took time.Duration) {
	opts := metric.WithAttributeSet(attribute.NewSet(append(m.attrs.ToSlice(), attribute.String("trowel.outcome", outcome))...))
	m.deliveries.Add(ctx, 1, opts)
	m.duration.Record(ctx, took.Seconds(), opts)
}

func (m metrics) deadLetter(ctx context.Context, outcome string) {
	m.deadLetters.Add(ctx, 1, metric.WithAttributeSet(attribute.NewSet(append(m.attrs.ToSlice(), attribute.String("trowel.outcome", outcome))...)))
}
