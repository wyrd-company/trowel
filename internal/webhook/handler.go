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

	"github.com/wyrd-company/nats-relay/internal/config"
	"github.com/wyrd-company/nats-relay/internal/message"
	"github.com/wyrd-company/nats-relay/internal/signing"
	"github.com/wyrd-company/nats-relay/internal/subject"
	"github.com/wyrd-company/nats-relay/internal/transform"
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
	return &Handler{
		cfg:       cfg,
		verifier:  v,
		transform: t,
		subject:   tpl,
		publisher: pub,
		log:       log.With("webhook", cfg.Path),
		now:       time.Now,
	}, nil
}

// Path is the route this handler serves.
func (h *Handler) Path() string { return h.cfg.Path }

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, h.cfg.MaxBodyBytes))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			http.Error(w, "payload too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}
	if err := h.verifier.Verify(r.Header, body, h.now()); err != nil {
		h.log.Warn("verification failed", "remote", r.RemoteAddr)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
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
		h.log.Error("transform failed", "error", err)
		http.Error(w, "transform failed", http.StatusUnprocessableEntity)
		return
	}
	m.Subject, err = h.subject.Expand(m)
	if err != nil {
		h.log.Error("subject expansion failed", "error", err)
		http.Error(w, "subject expansion failed", http.StatusUnprocessableEntity)
		return
	}
	if err := h.publisher.Publish(r.Context(), m, msgID); err != nil {
		h.log.Error("publish failed", "subject", m.Subject, "error", err)
		http.Error(w, "publish failed", http.StatusServiceUnavailable)
		return
	}
	h.log.Info("relayed", "subject", m.Subject, "bytes", len(m.Payload), "message_id", msgID)
	w.WriteHeader(http.StatusAccepted)
}
