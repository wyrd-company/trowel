package app

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"reflect"
	"syscall"
	"time"

	"github.com/fsnotify/fsnotify"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/wyrd-company/trowel/internal/config"
	"github.com/wyrd-company/trowel/internal/vault"
)

// settleDelay is how long the config directory must stay quiet after a
// change before the file is read, so a save made of several writes is
// applied once, complete.
const settleDelay = 500 * time.Millisecond

// errServerChanged rejects a reload that changes the listener, which only
// a restart can apply.
var errServerChanged = errors.New("server settings changed; restart trowel to apply them")

// watch reloads the configuration when its directory changes or the
// process receives SIGHUP, until ctx ends. Watching the directory rather
// than the file follows editors and orchestrators that replace the file.
// When the path is a symlink, the directory of its target is watched too,
// and that watch follows the link when it is retargeted.
func (a *App) watch(ctx context.Context) {
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	defer signal.Stop(hup)

	var events <-chan fsnotify.Event
	var errs <-chan error
	var cw *configWatch
	if w, err := fsnotify.NewWatcher(); err != nil {
		a.log.Error("config watch unavailable; reload with SIGHUP", "path", a.path, "error", err)
	} else if err := w.Add(filepath.Dir(a.path)); err != nil {
		w.Close()
		a.log.Error("config watch unavailable; reload with SIGHUP", "path", a.path, "error", err)
	} else {
		defer w.Close()
		cw = &configWatch{w: w, path: a.path}
		cw.followTarget(a.log)
		events, errs = w.Events, w.Errors
	}
	close(a.watching)

	settle := time.NewTimer(settleDelay)
	settle.Stop()
	defer settle.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-hup:
			cw.followTarget(a.log)
			a.reloadFile(ctx, true)
		case _, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			settle.Reset(settleDelay)
		case err, ok := <-errs:
			if !ok {
				errs = nil
				continue
			}
			a.log.Warn("config watch error", "error", err)
		case <-settle.C:
			cw.followTarget(a.log)
			a.reloadFile(ctx, false)
		}
	}
}

// configWatch watches the config file's directory and, when the path is
// a symlink into another directory, that directory too.
type configWatch struct {
	w      *fsnotify.Watcher
	path   string
	target string
}

// followTarget moves the target watch to the directory the path resolves
// to now. A nil configWatch does nothing.
func (cw *configWatch) followTarget(log *slog.Logger) {
	if cw == nil {
		return
	}
	resolved, err := filepath.EvalSymlinks(cw.path)
	if err != nil {
		return
	}
	dir := filepath.Dir(resolved)
	if dir == filepath.Dir(cw.path) {
		dir = ""
	}
	if dir == cw.target {
		return
	}
	if cw.target != "" {
		_ = cw.w.Remove(cw.target)
	}
	cw.target = ""
	if dir == "" {
		return
	}
	if err := cw.w.Add(dir); err != nil {
		log.Warn("config watch cannot follow symlink target; reload with SIGHUP", "path", cw.path, "target", dir, "error", err)
		return
	}
	cw.target = dir
}

// reloadFile reads the config file and applies it. Unless forced, content
// identical to the last attempt is skipped, so unrelated changes in the
// directory do nothing. Any failure keeps the current configuration.
func (a *App) reloadFile(ctx context.Context, force bool) {
	raw, err := os.ReadFile(a.path)
	if err == nil {
		sum := sha256.Sum256(raw)
		if !force && sum == a.lastSum {
			return
		}
		a.lastSum = sum
		err = a.reload(ctx, raw)
	}
	if err != nil {
		a.reloads.Add(ctx, 1, metric.WithAttributes(attribute.String("trowel.outcome", "failed")))
		a.log.ErrorContext(ctx, "config reload failed; keeping current configuration", "path", a.path, "error", err)
		return
	}
	a.reloads.Add(ctx, 1, metric.WithAttributes(attribute.String("trowel.outcome", "applied")))
	cfg := a.gen.Load().cfg
	a.log.InfoContext(ctx, "config reloaded", "path", a.path, "webhooks", len(cfg.Webhooks), "subjects", len(cfg.Subjects))
}

// reload parses raw, resolves its secrets, and applies it.
func (a *App) reload(ctx context.Context, raw []byte) error {
	cfg, err := config.Parse(raw)
	if err != nil {
		return err
	}
	if err := vault.Resolve(ctx, cfg, a.log); err != nil {
		return fmt.Errorf("resolve vault secrets: %w", err)
	}
	return a.apply(ctx, cfg)
}

// apply builds and starts a generation for cfg, then switches webhooks to
// it and retires the previous one. Until the switch nothing the current
// generation uses is touched, so any failure leaves it running. The NATS
// connection is reused when its settings are unchanged.
func (a *App) apply(ctx context.Context, cfg *config.Config) error {
	old := a.gen.Load()
	if !reflect.DeepEqual(cfg.Server, old.cfg.Server) {
		return errServerChanged
	}
	conn := old.conn
	if !reflect.DeepEqual(cfg.NATS, conn.cfg) {
		c, err := connect(cfg.NATS, a.log)
		if err != nil {
			return err
		}
		conn = c
	}
	g, err := build(cfg, conn, a.server, a.log)
	if err == nil {
		err = g.start(ctx)
	}
	if err != nil {
		if conn != old.conn {
			conn.nc.Close()
		}
		return err
	}
	conn.acquire()
	a.gen.Store(g)
	a.retiring.Go(func() { old.retire(a.retireCtx) })
	return nil
}
