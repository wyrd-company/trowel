package webhook

import (
	"context"
	"net/http"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/wyrd-company/trowel/internal/message"
	"github.com/wyrd-company/trowel/internal/telemetry"
)

// JetStreamPublisher publishes with a stream acknowledgement.
type JetStreamPublisher struct {
	JS jetstream.JetStream
}

// Publish sends the message with the trace context in ctx and waits for
// the stream to acknowledge it.
func (p JetStreamPublisher) Publish(ctx context.Context, m *message.Message, msgID string) error {
	h := m.Headers.Clone()
	if h == nil {
		h = http.Header{}
	}
	telemetry.Inject(ctx, h)
	msg := &nats.Msg{Subject: m.Subject, Data: m.Payload, Header: nats.Header(h)}
	var opts []jetstream.PublishOpt
	if msgID != "" {
		opts = append(opts, jetstream.WithMsgID(msgID))
	}
	_, err := p.JS.PublishMsg(ctx, msg, opts...)
	return err
}
