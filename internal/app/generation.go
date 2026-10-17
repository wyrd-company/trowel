package app

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/wyrd-company/trowel/internal/config"
	"github.com/wyrd-company/trowel/internal/natsconn"
	"github.com/wyrd-company/trowel/internal/relay"
	"github.com/wyrd-company/trowel/internal/server"
	"github.com/wyrd-company/trowel/internal/webhook"
)

// connection is one NATS connection. Generations with equal NATS settings
// share it.
type connection struct {
	cfg config.NATS
	nc  *nats.Conn
	js  jetstream.JetStream
}

func connect(cfg config.NATS, log *slog.Logger) (*connection, error) {
	nc, js, err := natsconn.Connect(cfg, log)
	if err != nil {
		return nil, err
	}
	return &connection{cfg: cfg, nc: nc, js: js}, nil
}

// generation is everything built from one configuration: the webhook
// routes and the relay workers, over a NATS connection.
type generation struct {
	cfg     *config.Config
	conn    *connection
	routes  http.Handler
	workers []*relay.Worker
}

// build constructs a generation for cfg on conn. It starts nothing.
func build(cfg *config.Config, conn *connection, srv *server.Server, log *slog.Logger) (*generation, error) {
	g := &generation{cfg: cfg, conn: conn}
	routes := srv.NewRoutes()
	pub := webhook.JetStreamPublisher{JS: conn.js}
	for _, w := range cfg.Webhooks {
		h, err := webhook.New(w, pub, log)
		if err != nil {
			return nil, err
		}
		routes.Handle(w.Path, h)
	}
	g.routes = routes
	for _, s := range cfg.Subjects {
		ws, err := relay.NewWorkers(s, conn.js, log)
		if err != nil {
			return nil, err
		}
		g.workers = append(g.workers, ws...)
	}
	return g, nil
}

// start creates or updates every consumer and begins delivering. On
// failure it stops the workers it started.
func (g *generation) start(ctx context.Context) error {
	for i, w := range g.workers {
		if err := w.Start(ctx); err != nil {
			for _, started := range g.workers[:i] {
				started.Stop()
			}
			return fmt.Errorf("start: %w", err)
		}
	}
	return nil
}

// stop stops every worker, cancelling deliveries in progress.
func (g *generation) stop() {
	for _, w := range g.workers {
		w.Stop()
	}
}
