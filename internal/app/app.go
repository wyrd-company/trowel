// Package app wires configuration, NATS, webhooks, and relays together.
package app

import (
	"context"
	"crypto/sha256"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"

	"github.com/wyrd-company/trowel/internal/config"
	"github.com/wyrd-company/trowel/internal/server"
	"github.com/wyrd-company/trowel/internal/telemetry"
	"github.com/wyrd-company/trowel/internal/vault"
)

// App is a configured, runnable relay.
type App struct {
	log    *slog.Logger
	server *server.Server
	gen    atomic.Pointer[generation]

	// path, when set, is the config file reloaded on change or SIGHUP.
	path     string
	lastSum  [sha256.Size]byte
	watching chan struct{}
	reloads  metric.Int64Counter

	// retiring tracks replaced generations still finishing their work.
	// Ending retireCtx cuts that work short at shutdown.
	retiring     sync.WaitGroup
	retireCtx    context.Context
	cancelRetire context.CancelFunc
}

// Option configures an App.
type Option func(*App)

// WithReload reloads the configuration from path, whose current content is
// raw, when the file changes or the process receives SIGHUP.
func WithReload(path string, raw []byte) Option {
	return func(a *App) {
		a.path = path
		a.lastSum = sha256.Sum256(raw)
	}
}

// New connects to NATS and builds every webhook and relay from cfg.
func New(cfg *config.Config, log *slog.Logger, opts ...Option) (*App, error) {
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
	a := &App{log: log, server: srv, watching: make(chan struct{})}
	a.reloads, err = otel.Meter(telemetry.ScopeName).Int64Counter("trowel.config.reloads",
		metric.WithUnit("{reload}"),
		metric.WithDescription("Configuration reload attempts, by outcome: applied or failed."))
	telemetry.Handle(err)
	a.retireCtx, a.cancelRetire = context.WithCancel(context.Background())
	for _, o := range opts {
		o(a)
	}
	conn.acquire()
	a.gen.Store(g)
	srv.SetRoutes(http.HandlerFunc(a.serve))
	return a, nil
}

// serve routes a request to the current generation's webhooks.
func (a *App) serve(w http.ResponseWriter, r *http.Request) {
	for {
		g := a.gen.Load()
		if g.enter() {
			defer g.leave()
			g.routes.ServeHTTP(w, r)
			return
		}
	}
}

// Run starts consumers and the HTTP server and blocks until a signal or
// failure. It always drains and closes every NATS connection.
func (a *App) Run(ctx context.Context) error {
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	defer func() { a.gen.Load().conn.release() }()
	defer a.cancelRetire()

	if err := a.gen.Load().start(ctx); err != nil {
		return err
	}
	a.server.SetReady(true)

	errCh := make(chan error, 1)
	go func() { errCh <- a.server.ListenAndServe() }()

	watchCtx, stopWatch := context.WithCancel(ctx)
	var watcher sync.WaitGroup
	if a.path != "" {
		watcher.Go(func() { a.watch(watchCtx) })
	}

	var runErr error
	select {
	case <-ctx.Done():
		a.log.Info("shutting down")
	case err := <-errCh:
		if err != nil {
			runErr = fmt.Errorf("http server: %w", err)
		}
	}
	stopWatch()
	watcher.Wait()
	a.server.SetReady(false)
	a.gen.Load().stop()
	if err := a.server.Shutdown(context.Background()); err != nil && !errors.Is(err, context.DeadlineExceeded) && runErr == nil {
		runErr = err
	}
	a.cancelRetire()
	a.retiring.Wait()
	return runErr
}

// Main is the CLI entry point. It returns the process exit code.
func Main(args []string) int {
	fs := flag.NewFlagSet("trowel", flag.ContinueOnError)
	path := fs.String("config", envOr("TROWEL_CONFIG", "config.yml"), "path to the YAML configuration file")
	validate := fs.Bool("validate", false, "load and validate the configuration, then exit")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	level := logLevel()
	base := slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: level})
	stderr := slog.New(base)
	log := stderr
	if !*validate {
		tel, err := telemetry.Setup(context.Background(), Version)
		if err != nil {
			stderr.Error("set up telemetry", "error", err)
			return 1
		}
		defer flushTelemetry(tel, stderr)
		// Export errors go to stderr only, so a failing log exporter cannot
		// feed its own errors back into the export.
		otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) { stderr.Warn("telemetry export", "error", err) }))
		log = slog.New(tel.LogHandler(base, level))
	}

	log.Info("trowel starting", "version", Version)

	raw, err := os.ReadFile(*path)
	var cfg *config.Config
	if err == nil {
		cfg, err = config.Parse(raw)
	}
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
	a, err := New(cfg, log, WithReload(*path, raw))
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

// telemetryFlushTimeout bounds how long exit waits to export buffered
// telemetry, so an unreachable collector cannot hold the process open.
const telemetryFlushTimeout = 5 * time.Second

func flushTelemetry(tel *telemetry.Telemetry, log *slog.Logger) {
	ctx, cancel := context.WithTimeout(context.Background(), telemetryFlushTimeout)
	defer cancel()
	if err := tel.Shutdown(ctx); err != nil {
		log.Warn("flush telemetry", "error", err)
	}
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
