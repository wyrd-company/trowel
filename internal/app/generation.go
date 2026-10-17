package app

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"github.com/wyrd-company/trowel/internal/config"
	"github.com/wyrd-company/trowel/internal/natsconn"
	"github.com/wyrd-company/trowel/internal/relay"
	"github.com/wyrd-company/trowel/internal/server"
	"github.com/wyrd-company/trowel/internal/webhook"
)

// connection is one NATS connection. Generations with equal NATS settings
// share it, and it is drained when the last of them releases it.
type connection struct {
	cfg config.NATS
	nc  *nats.Conn
	js  jetstream.JetStream

	mu   sync.Mutex
	refs int
}

// acquire records one more generation using the connection.
func (c *connection) acquire() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.refs++
}

// release records that a generation is done with the connection and
// drains it once no generation uses it.
func (c *connection) release() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.refs--
	if c.refs == 0 {
		_ = c.nc.Drain()
	}
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

	// mu guards the count of requests the routes are serving, so a retired
	// generation keeps its connection until they finish.
	mu      sync.Mutex
	active  int
	retired bool
	idle    chan struct{}
}

// build constructs a generation for cfg on conn. It starts nothing.
func build(cfg *config.Config, conn *connection, srv *server.Server, log *slog.Logger) (*generation, error) {
	g := &generation{cfg: cfg, conn: conn, idle: make(chan struct{})}
	routes := srv.NewRoutes()
	pub := webhook.JetStreamPublisher{JS: conn.js}
	for _, w := range cfg.Webhooks {
		h, err := webhook.New(w, pub, log)
		if err != nil {
			return nil, err
		}
		routes.Handle(w.Path, h)
	}
	g.routes = otelhttp.NewHandler(routes, "webhook",
		// Webhooks usually arrive from outside, so a sender's trace context
		// is linked rather than adopted as the parent.
		otelhttp.WithPublicEndpointFn(func(*http.Request) bool { return true }),
		otelhttp.WithFilter(func(r *http.Request) bool { return r.URL.Path != "/healthz" && r.URL.Path != "/readyz" }),
		otelhttp.WithSpanNameFormatter(spanName),
	)
	for _, s := range cfg.Subjects {
		ws, err := relay.NewWorkers(s, conn.js, log)
		if err != nil {
			return nil, err
		}
		g.workers = append(g.workers, ws...)
	}
	return g, nil
}

// spanName names a server span for the method and the matched route. A
// route registered with a method already starts with it.
func spanName(_ string, r *http.Request) string {
	switch {
	case r.Pattern == "":
		return r.Method
	case strings.Contains(r.Pattern, " "):
		return r.Pattern
	}
	return r.Method + " " + r.Pattern
}

// start prepares and then activates every worker.
func (g *generation) start(ctx context.Context) error {
	if err := g.prepare(ctx); err != nil {
		return err
	}
	return g.activate()
}

// prepare creates or updates every consumer without taking a message.
func (g *generation) prepare(ctx context.Context) error {
	for _, w := range g.workers {
		if err := w.Prepare(ctx); err != nil {
			return fmt.Errorf("start: %w", err)
		}
	}
	return nil
}

// activate subscribes every worker and admits deliveries only once all of
// them are subscribed, so a generation that fails to start delivers
// nothing. On failure the subscribed workers return what they received
// and stop.
func (g *generation) activate() error {
	for i, w := range g.workers {
		if err := w.Consume(); err != nil {
			for _, started := range g.workers[:i] {
				started.Refuse()
				started.Stop()
			}
			return fmt.Errorf("start: %w", err)
		}
	}
	for _, w := range g.workers {
		w.Admit()
	}
	return nil
}

// stop stops every worker, cancelling deliveries in progress.
func (g *generation) stop() {
	for _, w := range g.workers {
		w.Stop()
	}
}

// enter records a request on the routes. It reports false once the
// generation is retired, so the caller retries on the current one.
func (g *generation) enter() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.retired {
		return false
	}
	g.active++
	return true
}

func (g *generation) leave() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.active--
	if g.retired && g.active == 0 {
		close(g.idle)
	}
}

// retire lets a replaced generation finish its work and releases its
// connection. Its workers stop taking messages at once and finish the
// deliveries they hold, while requests in progress on its routes finish.
// Ending ctx cuts the waits short.
func (g *generation) retire(ctx context.Context) {
	var drained sync.WaitGroup
	for _, w := range g.workers {
		drained.Go(func() { w.Drain(ctx) })
	}

	g.mu.Lock()
	g.retired = true
	if g.active == 0 {
		close(g.idle)
	}
	g.mu.Unlock()
	select {
	case <-g.idle:
	case <-ctx.Done():
	}
	drained.Wait()
	g.conn.release()
}
