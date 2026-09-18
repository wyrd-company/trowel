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

	"github.com/wyrd-company/nats-relay/internal/config"
	"github.com/wyrd-company/nats-relay/internal/message"
	"github.com/wyrd-company/nats-relay/internal/transform"
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

	mu      sync.Mutex
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
		workers = append(workers, &Worker{relay: relay, dest: d, transform: t, deliver: dest, js: js, log: wlog})
	}
	return workers, nil
}

// DurableName is the consumer name for this worker.
func (w *Worker) DurableName() string {
	return "relay-" + w.relay.Name + "-" + w.dest.Name
}

// Start creates or updates the consumer and begins delivering.
func (w *Worker) Start(ctx context.Context) error {
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
	cc, err := cons.Consume(w.handle, jetstream.ConsumeErrHandler(func(_ jetstream.ConsumeContext, err error) {
		w.log.Warn("consume error", "error", err)
	}))
	if err != nil {
		return fmt.Errorf("relay %s: consume: %w", w.relay.Name, err)
	}
	w.mu.Lock()
	w.consume = cc
	w.ctx, w.cancel = context.WithCancel(context.Background())
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

func (w *Worker) handle(msg jetstream.Msg) {
	attempt := uint64(1)
	if md, err := msg.Metadata(); err == nil {
		attempt = md.NumDelivered
	}
	log := w.log.With("subject", msg.Subject(), "attempt", attempt)

	m := &message.Message{Subject: msg.Subject(), Headers: http.Header(msg.Headers()), Payload: msg.Data()}
	if m.Headers == nil {
		m.Headers = http.Header{}
	}

	err := w.process(m)
	if err == nil {
		if err := msg.Ack(); err != nil {
			log.Warn("ack failed", "error", err)
		}
		log.Info("delivered")
		return
	}

	exhausted := w.relay.Consumer.MaxDeliver > 0 && attempt >= uint64(w.relay.Consumer.MaxDeliver)
	var perm *permanentError
	if errors.As(err, &perm) || exhausted {
		log.Error("delivery failed permanently", "error", err)
		w.deadLetter(msg, m, err, attempt)
		if err := msg.Term(); err != nil {
			log.Warn("term failed", "error", err)
		}
		return
	}
	delay := w.retryDelay(attempt)
	log.Warn("delivery failed, will retry", "error", err, "retry_in", delay.String())
	if delay > 0 {
		err = msg.NakWithDelay(delay)
	} else {
		err = msg.Nak()
	}
	if err != nil {
		log.Warn("nak failed", "error", err)
	}
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
// retrying cannot change the outcome.
func (w *Worker) process(m *message.Message) error {
	m, err := w.transform.Apply(m)
	if err != nil {
		return &permanentError{err}
	}
	w.mu.Lock()
	ctx := w.ctx
	w.mu.Unlock()
	if ctx == nil {
		ctx = context.Background()
	}
	return w.deliver.Deliver(ctx, m)
}

// deadLetter publishes the original message to the DLQ subject with failure
// metadata. Publish errors are logged; the message is still terminated so a
// broken DLQ cannot wedge the consumer.
func (w *Worker) deadLetter(msg jetstream.Msg, m *message.Message, cause error, attempt uint64) {
	dlq := w.relay.Consumer.DLQSubject
	if dlq == "" {
		return
	}
	out := &nats.Msg{Subject: dlq, Data: msg.Data(), Header: nats.Header(message.CopyHeaders(m.Headers))}
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
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := w.js.PublishMsg(ctx, out); err != nil {
		w.log.Error("dead-letter publish failed", "dlq", dlq, "error", err)
		return
	}
	w.log.Info("dead-lettered", "dlq", dlq)
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
