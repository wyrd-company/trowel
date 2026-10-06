// Package server hosts the webhook routes and health endpoints.
package server

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/wyrd-company/nats-webhook-relay/internal/config"
)

// Server is the inbound HTTP listener.
type Server struct {
	http  *http.Server
	mux   *http.ServeMux
	ready atomic.Bool
	log   *slog.Logger
}

// New builds a server with health routes registered.
func New(cfg config.Server, log *slog.Logger) *Server {
	s := &Server{mux: http.NewServeMux(), log: log}
	s.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	s.mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) {
		if !s.ready.Load() {
			http.Error(w, "not ready", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ready\n"))
	})
	s.http = &http.Server{
		Addr:              cfg.Listen,
		Handler:           s.mux,
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
	}
	return s
}

// Handle registers a route.
func (s *Server) Handle(path string, h http.Handler) { s.mux.Handle(path, h) }

// SetReady flips the readiness probe.
func (s *Server) SetReady(v bool) { s.ready.Store(v) }

// Handler exposes the mux for tests.
func (s *Server) Handler() http.Handler { return s.mux }

// ListenAndServe blocks until the listener closes.
func (s *Server) ListenAndServe() error {
	ln, err := net.Listen("tcp", s.http.Addr)
	if err != nil {
		return err
	}
	s.log.Info("http listening", "addr", ln.Addr().String())
	if err := s.http.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// Shutdown stops accepting requests and waits for in-flight ones.
func (s *Server) Shutdown(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	return s.http.Shutdown(ctx)
}
