// Package natsconn opens the NATS connection and JetStream context.
package natsconn

import (
	"fmt"
	"log/slog"

	"github.com/nats-io/jwt/v2"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/nats-io/nkeys"

	"github.com/wyrd-company/trowel/internal/config"
)

// Connect dials the server described by cfg with reconnects enabled.
func Connect(cfg config.NATS, log *slog.Logger) (*nats.Conn, jetstream.JetStream, error) {
	opts := []nats.Option{
		nats.Name(cfg.Name),
		nats.MaxReconnects(-1),
		nats.DisconnectErrHandler(func(_ *nats.Conn, err error) {
			if err != nil {
				log.Warn("nats disconnected", "error", err)
			}
		}),
		nats.ReconnectHandler(func(nc *nats.Conn) {
			log.Info("nats reconnected", "url", nc.ConnectedUrl())
		}),
	}
	if cfg.CredsFile != "" {
		opts = append(opts, nats.UserCredentials(cfg.CredsFile))
	}
	if cfg.Creds != "" {
		opt, err := credsOption(cfg.Creds)
		if err != nil {
			return nil, nil, err
		}
		opts = append(opts, opt)
	}
	nc, err := nats.Connect(cfg.URL, opts...)
	if err != nil {
		return nil, nil, fmt.Errorf("connect to nats: %w", err)
	}
	js, err := jetstream.New(nc)
	if err != nil {
		nc.Close()
		return nil, nil, fmt.Errorf("jetstream: %w", err)
	}
	return nc, js, nil
}

// credsOption authenticates with the contents of a NATS credentials file
// held in memory, as resolved from creds_vault.
func credsOption(creds string) (nats.Option, error) {
	user, err := jwt.ParseDecoratedJWT([]byte(creds))
	if err != nil {
		return nil, fmt.Errorf("nats creds_vault: %w", err)
	}
	kp, err := nkeys.ParseDecoratedNKey([]byte(creds))
	if err != nil {
		return nil, fmt.Errorf("nats creds_vault: %w", err)
	}
	seed, err := kp.Seed()
	if err != nil {
		return nil, fmt.Errorf("nats creds_vault: %w", err)
	}
	return nats.UserJWTAndSeed(user, string(seed)), nil
}
