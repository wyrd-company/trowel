// Package webhook accepts HTTP POST requests and publishes them to NATS.
package webhook

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"

	"github.com/wyrd-company/trowel/internal/config"
	"github.com/wyrd-company/trowel/internal/message"
	"github.com/wyrd-company/trowel/internal/signing"
	"github.com/wyrd-company/trowel/internal/subject"
	"github.com/wyrd-company/trowel/internal/telemetry"
	"github.com/wyrd-company/trowel/internal/transform"
)

// Publisher sends a message to NATS. msgID, when non-empty, is used for
// JetStream de-duplication.
type Publisher interface {
	Publish(ctx context.Context, m *message.Message, msgID string) error
}

// Handler is one configured webhook path.
type Handler struct {
	cfg       config.Webhook
	verifier  signing.Verifier
	transform *transform.Transformer
	subject   *subject.Template
	publisher Publisher
	log       *slog.Logger
	now       func() time.Time
	tracer    trace.Tracer
	requests  metric.Int64Counter
}

// New builds a Handler from config.
func New(cfg config.Webhook, pub Publisher, log *slog.Logger) (*Handler, error) {
	v, err := signing.NewVerifier(cfg.Verify)
	if err != nil {
		return nil, fmt.Errorf("webhook %s: %w", cfg.Path, err)
	}
	t, err := transform.New(cfg.Transform)
	if err != nil {
		return nil, fmt.Errorf("webhook %s: %w", cfg.Path, err)
	}
	tpl, err := subject.Parse(cfg.Subject)
	if err != nil {
		return nil, fmt.Errorf("webhook %s: %w", cfg.Path, err)
	}
	requests, err := otel.Meter(telemetry.ScopeName).Int64Counter("trowel.webhook.requests",
		metric.WithUnit("{request}"),
		metric.WithDescription("Webhook requests handled, by outcome."))
	telemetry.Handle(err)
	return &Handler{
		cfg:       cfg,
		verifier:  v,
		transform: t,
		subject:   tpl,
		publisher: pub,
		log:       log.With("webhook", cfg.Path),
		now:       time.Now,
		tracer:    otel.Tracer(telemetry.ScopeName),
		requests:  requests,
	}, nil
}

// Path is the route this handler serves.
func (h *Handler) Path() string { return h.cfg.Path }

// Outcomes recorded on trowel.webhook.requests.
const (
	outcomePublished        = "published"
	outcomeMethodNotAllowed = "method_not_allowed"
	outcomeTooLarge         = "too_large"
	outcomeBadRequest       = "bad_request"
	outcomeUnauthorized     = "unauthorized"
	outcomeTransformFailed  = "transform_failed"
	outcomeSubjectFailed    = "subject_failed"
	outcomePublishFailed    = "publish_failed"
)

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	outcome := h.serve(w, r)
	h.requests.Add(r.Context(), 1, metric.WithAttributes(
		attribute.String("trowel.webhook.path", h.cfg.Path),
		attribute.String("trowel.outcome", outcome)))
}

// serve answers the request and returns its outcome.
func (h *Handler) serve(w http.ResponseWriter, r *http.Request) string {
	ctx := r.Context()
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return outcomeMethodNotAllowed
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, h.cfg.MaxBodyBytes))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			http.Error(w, "payload too large", http.StatusRequestEntityTooLarge)
			return outcomeTooLarge
		}
		http.Error(w, "read body", http.StatusBadRequest)
		return outcomeBadRequest
	}
	if err := h.verifier.Verify(r.Header, body, h.now()); err != nil {
		h.log.WarnContext(ctx, "verification failed", "remote", r.RemoteAddr)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return outcomeUnauthorized
	}

	m := &message.Message{
		Headers: message.CopyHeaders(r.Header, h.verifier.StripHeaders()...),
		Payload: body,
	}
	msgID := ""
	if h.cfg.MessageIDHeader != "" {
		msgID = r.Header.Get(h.cfg.MessageIDHeader)
	}
	m, err = h.transform.Apply(m)
	if err != nil {
		h.log.ErrorContext(ctx, "transform failed", "error", err)
		http.Error(w, "transform failed", http.StatusUnprocessableEntity)
		return outcomeTransformFailed
	}
	m.Subject, err = h.subject.Expand(m)
	if err != nil {
		h.log.ErrorContext(ctx, "subject expansion failed", "error", err)
		http.Error(w, "subject expansion failed", http.StatusUnprocessableEntity)
		return outcomeSubjectFailed
	}
	if err := h.publish(ctx, m, msgID); err != nil {
		h.log.ErrorContext(ctx, "publish failed", "subject", m.Subject, "error", err)
		http.Error(w, "publish failed", http.StatusServiceUnavailable)
		return outcomePublishFailed
	}
	h.log.InfoContext(ctx, "relayed", "subject", m.Subject, "bytes", len(m.Payload), "message_id", msgID)
	w.WriteHeader(http.StatusAccepted)
	return outcomePublished
}

// publish sends m inside a producer span named for the subject template.
func (h *Handler) publish(ctx context.Context, m *message.Message, msgID string) error {
	attrs := []attribute.KeyValue{
		semconv.MessagingSystemKey.String("nats"),
		semconv.MessagingOperationTypeSend,
		semconv.MessagingOperationName("publish"),
		semconv.MessagingDestinationName(m.Subject),
		semconv.MessagingDestinationTemplate(h.cfg.Subject),
	}
	if msgID != "" {
		attrs = append(attrs, semconv.MessagingMessageID(msgID))
	}
	ctx, span := h.tracer.Start(ctx, "send "+h.cfg.Subject, trace.WithSpanKind(trace.SpanKindProducer), trace.WithAttributes(attrs...))
	defer span.End()
	err := h.publisher.Publish(ctx, m, msgID)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}
	return err
}
