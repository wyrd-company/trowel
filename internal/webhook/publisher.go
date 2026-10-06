package webhook

import (
	"context"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/wyrd-company/nats-webhook-relay/internal/message"
)

// JetStreamPublisher publishes with a stream acknowledgement.
type JetStreamPublisher struct {
	JS jetstream.JetStream
}

// Publish sends the message and waits for the stream to acknowledge it.
func (p JetStreamPublisher) Publish(ctx context.Context, m *message.Message, msgID string) error {
	msg := &nats.Msg{Subject: m.Subject, Data: m.Payload, Header: nats.Header(m.Headers.Clone())}
	var opts []jetstream.PublishOpt
	if msgID != "" {
		opts = append(opts, jetstream.WithMsgID(msgID))
	}
	_, err := p.JS.PublishMsg(ctx, msg, opts...)
	return err
}
