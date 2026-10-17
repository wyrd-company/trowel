package relay

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/baggage"
	"go.opentelemetry.io/otel/codes"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"

	"github.com/wyrd-company/trowel/internal/config"
	"github.com/wyrd-company/trowel/internal/message"
	"github.com/wyrd-company/trowel/internal/telemetry"
	"github.com/wyrd-company/trowel/internal/transform"
)

// Worker binds one destination of one subject relay to its own durable
// consumer, so each destination retries and dead-letters independently.
type Worker struct {
	relay     config.SubjectRelay
	dest      config.Destination
	transform *transform.Transformer
	deliver   Destination
	js        jetstream.JetStream
	log       *slog.Logger
	tracer    trace.Tracer
	metrics   metrics

	mu      sync.Mutex
	cons    jetstream.Consumer
	stream  string
	consume jetstream.ConsumeContext
	ctx     context.Context
	cancel  context.CancelFunc
}

// NewWorkers builds a Worker for every destination of relay.
func NewWorkers(relay config.SubjectRelay, js jetstream.JetStream, log *slog.Logger) ([]*Worker, error) {
	t, err := transform.New(relay.Transform)
	if err != nil {
		return nil, fmt.Errorf("subject relay %s: %w", relay.Name, err)
	}
	workers := make([]*Worker, 0, len(relay.Destinations))
	for _, d := range relay.Destinations {
		wlog := log.With("relay", relay.Name, "destination", d.Name)
		dest, err := NewDestination(d, wlog)
		if err != nil {
			return nil, fmt.Errorf("subject relay %s: %w", relay.Name, err)
		}
		workers = append(workers, &Worker{
			relay: relay, dest: d, transform: t, deliver: dest, js: js, log: wlog,
			tracer: otel.Tracer(telemetry.ScopeName), metrics: newMetrics(relay.Name, d.Name),
		})
	}
	return workers, nil
}

// DurableName is the consumer name for this worker.
func (w *Worker) DurableName() string {
	return "relay-" + w.relay.Name + "-" + w.dest.Name
}

// Prepare creates or updates the durable consumer without taking any
// messages from it.
func (w *Worker) Prepare(ctx context.Context) error {
	streamName := w.relay.Stream
	if streamName == "" {
		name, err := w.js.StreamNameBySubject(ctx, w.relay.Subject)
		if err != nil {
			return fmt.Errorf("relay %s: no stream covers subject %q: %w", w.relay.Name, w.relay.Subject, err)
		}
		streamName = name
	}
	stream, err := w.js.Stream(ctx, streamName)
	if err != nil {
		return fmt.Errorf("relay %s: stream %q: %w", w.relay.Name, streamName, err)
	}
	cons, err := stream.CreateOrUpdateConsumer(ctx, w.consumerConfig())
	if err != nil {
		return fmt.Errorf("relay %s: consumer %s: %w", w.relay.Name, w.DurableName(), err)
	}
	w.mu.Lock()
	w.cons, w.stream = cons, streamName
	w.mu.Unlock()
	return nil
}

// Consume begins delivering from the consumer Prepare set up.
func (w *Worker) Consume() error {
	w.mu.Lock()
	cons, streamName := w.cons, w.stream
	w.mu.Unlock()
	if cons == nil {
		return fmt.Errorf("relay %s: consumer %s is not prepared", w.relay.Name, w.DurableName())
	}
	ctx, cancel := context.WithCancel(context.Background())
	w.mu.Lock()
	w.ctx, w.cancel = ctx, cancel
	w.mu.Unlock()
	cc, err := cons.Consume(w.handle, jetstream.ConsumeErrHandler(func(_ jetstream.ConsumeContext, err error) {
		w.log.Warn("consume error", "error", err)
	}))
	if err != nil {
		cancel()
		return fmt.Errorf("relay %s: consume: %w", w.relay.Name, err)
	}
	w.mu.Lock()
	w.consume = cc
	w.mu.Unlock()
	w.log.Info("consuming", "stream", streamName, "consumer", w.DurableName(), "subject", w.relay.Subject)
	return nil
}

// Stop drains the consumer.
func (w *Worker) Stop() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.consume != nil {
		w.consume.Drain()
		w.consume = nil
	}
	if w.cancel != nil {
		w.cancel()
	}
}

// Drain stops taking new messages and waits until deliveries in progress
// finish or ctx ends, then cancels any that remain.
func (w *Worker) Drain(ctx context.Context) {
	w.mu.Lock()
	cc, cancel := w.consume, w.cancel
	w.consume = nil
	w.mu.Unlock()
	if cc != nil {
		cc.Drain()
		select {
		case <-cc.Closed():
		case <-ctx.Done():
		}
	}
	if cancel != nil {
		cancel()
	}
}

func (w *Worker) consumerConfig() jetstream.ConsumerConfig {
	c := w.relay.Consumer
	cfg := jetstream.ConsumerConfig{
		Durable:       w.DurableName(),
		FilterSubject: w.relay.Subject,
		AckPolicy:     jetstream.AckExplicitPolicy,
		AckWait:       c.AckWait,
		MaxDeliver:    c.MaxDeliver,
		MaxAckPending: c.MaxAckPending,
		BackOff:       c.Backoff,
	}
	switch c.DeliverPolicy {
	case "new":
		cfg.DeliverPolicy = jetstream.DeliverNewPolicy
	case "last":
		cfg.DeliverPolicy = jetstream.DeliverLastPolicy
	default:
		cfg.DeliverPolicy = jetstream.DeliverAllPolicy
	}
	return cfg
}

// handle delivers one message inside a consumer span whose parent is the
// trace context carried in the message headers.
func (w *Worker) handle(msg jetstream.Msg) {
	attempt := uint64(1)
	if md, err := msg.Metadata(); err == nil {
		attempt = md.NumDelivered
	}
	m := &message.Message{Subject: msg.Subject(), Headers: http.Header(msg.Headers()), Payload: msg.Data()}
	if m.Headers == nil {
		m.Headers = http.Header{}
	}
	ctx, span := w.tracer.Start(telemetry.Extract(context.Background(), m.Headers), "process "+w.relay.Subject,
		trace.WithSpanKind(trace.SpanKindConsumer),
		trace.WithAttributes(
			semconv.MessagingSystemKey.String("nats"),
			semconv.MessagingOperationTypeProcess,
			semconv.MessagingOperationName("process"),
			semconv.MessagingDestinationName(msg.Subject()),
			semconv.MessagingConsumerGroupName(w.DurableName()),
			attribute.String("trowel.relay.name", w.relay.Name),
			attribute.String("trowel.destination.name", w.dest.Name),
			attribute.Int64("trowel.delivery.attempt", int64(attempt)),
		))
	defer span.End()
	log := w.log.With("subject", msg.Subject(), "attempt", attempt)

	start := time.Now()
	err := w.process(ctx, m)
	took := time.Since(start)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}
	outcome := w.settle(ctx, log, msg, m, err, attempt)
	span.SetAttributes(attribute.String("trowel.outcome", outcome))
	w.metrics.delivery(ctx, outcome, took)
}

// settle acknowledges, retries, or terminates msg after a delivery attempt
// that returned err, and returns the outcome.
func (w *Worker) settle(ctx context.Context, log *slog.Logger, msg jetstream.Msg, m *message.Message, err error, attempt uint64) string {
	if err == nil {
		if err := msg.Ack(); err != nil {
			log.WarnContext(ctx, "ack failed", "error", err)
		}
		log.InfoContext(ctx, "delivered")
		return outcomeDelivered
	}

	exhausted := w.relay.Consumer.MaxDeliver > 0 && attempt >= uint64(w.relay.Consumer.MaxDeliver)
	var perm *permanentError
	if errors.As(err, &perm) || exhausted {
		log.ErrorContext(ctx, "delivery failed permanently", "error", err)
		w.deadLetter(ctx, msg, m, err, attempt)
		if err := msg.Term(); err != nil {
			log.WarnContext(ctx, "term failed", "error", err)
		}
		return outcomeTerminated
	}
	delay := w.retryDelay(attempt)
	log.WarnContext(ctx, "delivery failed, will retry", "error", err, "retry_in", delay.String())
	if delay > 0 {
		err = msg.NakWithDelay(delay)
	} else {
		err = msg.Nak()
	}
	if err != nil {
		log.WarnContext(ctx, "nak failed", "error", err)
	}
	return outcomeRetry
}

// retryDelay picks the backoff entry for the attempt that just failed. The
// last entry repeats once the list is exhausted. Without a backoff list the
// message is redelivered immediately.
func (w *Worker) retryDelay(attempt uint64) time.Duration {
	b := w.relay.Consumer.Backoff
	if len(b) == 0 {
		return 0
	}
	i := int(attempt) - 1
	if i < 0 {
		i = 0
	}
	if i >= len(b) {
		i = len(b) - 1
	}
	return b[i]
}

// process transforms and delivers. Transform errors are permanent because
// retrying cannot change the outcome. Delivery is cancelled with the
// worker and carries the trace context of ctx.
func (w *Worker) process(ctx context.Context, m *message.Message) error {
	m, err := w.transform.Apply(m)
	if err != nil {
		return &permanentError{err}
	}
	w.mu.Lock()
	wctx := w.ctx
	w.mu.Unlock()
	if wctx == nil {
		wctx = context.Background()
	}
	dctx := trace.ContextWithSpan(baggage.ContextWithBaggage(wctx, baggage.FromContext(ctx)), trace.SpanFromContext(ctx))
	return w.deliver.Deliver(dctx, m)
}

// deadLetter publishes the original message to the DLQ subject with failure
// metadata. Publish errors are logged; the message is still terminated so a
// broken DLQ cannot wedge the consumer.
func (w *Worker) deadLetter(ctx context.Context, msg jetstream.Msg, m *message.Message, cause error, attempt uint64) {
	dlq := w.relay.Consumer.DLQSubject
	if dlq == "" {
		return
	}
	ctx, span := w.tracer.Start(ctx, "send "+dlq, trace.WithSpanKind(trace.SpanKindProducer), trace.WithAttributes(
		semconv.MessagingSystemKey.String("nats"),
		semconv.MessagingOperationTypeSend,
		semconv.MessagingOperationName("publish"),
		semconv.MessagingDestinationName(dlq),
	))
	defer span.End()
	headers := message.CopyHeaders(m.Headers)
	telemetry.Inject(ctx, headers)
	out := &nats.Msg{Subject: dlq, Data: msg.Data(), Header: nats.Header(headers)}
	out.Header.Set("Relay-Origin-Subject", msg.Subject())
	out.Header.Set("Relay-Name", w.relay.Name)
	out.Header.Set("Relay-Destination", w.dest.Name)
	out.Header.Set("Relay-Attempts", strconv.FormatUint(attempt, 10))
	out.Header.Set("Relay-Error", headerSafe(cause.Error()))
	out.Header.Set("Relay-Failed-At", time.Now().UTC().Format(time.RFC3339))
	if md, err := msg.Metadata(); err == nil {
		out.Header.Set("Relay-Origin-Stream", md.Stream)
		out.Header.Set("Relay-Origin-Sequence", strconv.FormatUint(md.Sequence.Stream, 10))
	}
	pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if _, err := w.js.PublishMsg(pctx, out); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		w.metrics.deadLetter(ctx, "failed")
		w.log.ErrorContext(ctx, "dead-letter publish failed", "dlq", dlq, "error", err)
		return
	}
	w.metrics.deadLetter(ctx, "published")
	w.log.InfoContext(ctx, "dead-lettered", "dlq", dlq)
}

// maxErrorHeader bounds Relay-Error so command stderr cannot bloat the
// dead-letter message.
const maxErrorHeader = 1024

func headerSafe(s string) string {
	s = strings.NewReplacer("\r", " ", "\n", " ").Replace(s)
	if len(s) > maxErrorHeader {
		s = s[:maxErrorHeader] + "..."
	}
	return s
}

type permanentError struct{ err error }

func (p *permanentError) Error() string { return p.err.Error() }
func (p *permanentError) Unwrap() error { return p.err }
