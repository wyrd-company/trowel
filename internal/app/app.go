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

	"github.com/wyrd-company/trowel/internal/config"
	"github.com/wyrd-company/trowel/internal/server"
	"github.com/wyrd-company/trowel/internal/vault"
)

// App is a configured, runnable relay.
type App struct {
	log    *slog.Logger
	server *server.Server
	gen    *generation
}

// New connects to NATS and builds every webhook and relay from cfg.
func New(cfg *config.Config, log *slog.Logger) (*App, error) {
	conn, err := connect(cfg.NATS, log)
	if err != nil {
		return nil, err
	}
	srv := server.New(cfg.Server, log)
	g, err := build(cfg, conn, srv, log)
	if err != nil {
		conn.nc.Close()
		return nil, err
	}
	srv.SetRoutes(g.routes)
	return &App{log: log, server: srv, gen: g}, nil
}

// Run starts consumers and the HTTP server and blocks until a signal or
// failure. It always drains and closes the NATS connection.
func (a *App) Run(ctx context.Context) error {
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	defer a.gen.conn.nc.Drain()

	if err := a.gen.start(ctx); err != nil {
		return err
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
	a.gen.stop()
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
	if err := vault.Resolve(context.Background(), cfg, log); err != nil {
		log.Error("resolve vault secrets", "error", err)
		return 1
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
