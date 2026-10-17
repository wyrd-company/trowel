// Package app wires configuration, NATS, webhooks, and relays together.
package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/wyrd-company/trowel/internal/config"
	"github.com/wyrd-company/trowel/internal/natsconn"
	"github.com/wyrd-company/trowel/internal/relay"
	"github.com/wyrd-company/trowel/internal/server"
	"github.com/wyrd-company/trowel/internal/webhook"
)

// App is a configured, runnable relay.
type App struct {
	cfg     *config.Config
	log     *slog.Logger
	nc      *nats.Conn
	js      jetstream.JetStream
	server  *server.Server
	workers []*relay.Worker
}

// New connects to NATS and builds every webhook and relay from cfg.
func New(cfg *config.Config, log *slog.Logger) (*App, error) {
	nc, js, err := natsconn.Connect(cfg.NATS, log)
	if err != nil {
		return nil, err
	}
	a := &App{cfg: cfg, log: log, nc: nc, js: js, server: server.New(cfg.Server, log)}

	pub := webhook.JetStreamPublisher{JS: js}
	for _, w := range cfg.Webhooks {
		h, err := webhook.New(w, pub, log)
		if err != nil {
			nc.Close()
			return nil, err
		}
		a.server.Handle(w.Path, h)
	}
	for _, s := range cfg.Subjects {
		ws, err := relay.NewWorkers(s, js, log)
		if err != nil {
			nc.Close()
			return nil, err
		}
		a.workers = append(a.workers, ws...)
	}
	return a, nil
}

// Run starts consumers and the HTTP server and blocks until a signal or
// failure. It always drains and closes the NATS connection.
func (a *App) Run(ctx context.Context) error {
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	defer a.nc.Drain()

	for _, w := range a.workers {
		if err := w.Start(ctx); err != nil {
			return err
		}
	}
	a.server.SetReady(true)

	errCh := make(chan error, 1)
	go func() { errCh <- a.server.ListenAndServe() }()

	select {
	case <-ctx.Done():
		a.log.Info("shutting down")
	case err := <-errCh:
		if err != nil {
			return fmt.Errorf("http server: %w", err)
		}
	}
	a.server.SetReady(false)
	for _, w := range a.workers {
		w.Stop()
	}
	if err := a.server.Shutdown(context.Background()); err != nil && !errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return nil
}

// Main is the CLI entry point. It returns the process exit code.
func Main(args []string) int {
	fs := flag.NewFlagSet("trowel", flag.ContinueOnError)
	path := fs.String("config", envOr("TROWEL_CONFIG", "config.yml"), "path to the YAML configuration file")
	validate := fs.Bool("validate", false, "load and validate the configuration, then exit")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	log := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: logLevel()}))

	log.Info("trowel starting", "version", Version)

	cfg, err := config.Load(*path)
	if err != nil {
		log.Error("load config", "path", *path, "error", err)
		return 2
	}
	if *validate {
		log.Info("config valid", "path", *path, "webhooks", len(cfg.Webhooks), "subjects", len(cfg.Subjects))
		return 0
	}
	a, err := New(cfg, log)
	if err != nil {
		log.Error("start", "error", err)
		return 1
	}
	if err := a.Run(context.Background()); err != nil {
		log.Error("run", "error", err)
		return 1
	}
	return 0
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func logLevel() slog.Level {
	switch os.Getenv("TROWEL_LOG_LEVEL") {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	}
	return slog.LevelInfo
}
