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

	"github.com/wyrd-company/trowel/internal/config"
)

// Server is the inbound HTTP listener. Its routes can be replaced while it
// serves.
type Server struct {
	http   *http.Server
	routes atomic.Pointer[http.Handler]
	ready  atomic.Bool
	log    *slog.Logger
}

// New builds a server that answers 404 until routes are set.
func New(cfg config.Server, log *slog.Logger) *Server {
	s := &Server{log: log}
	s.SetRoutes(http.NotFoundHandler())
	s.http = &http.Server{
		Addr:              cfg.Listen,
		Handler:           http.HandlerFunc(s.serve),
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
	}
	return s
}

// NewRoutes returns a mux with the health routes registered, for callers to
// add webhook routes to before passing it to SetRoutes.
func (s *Server) NewRoutes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) {
		if !s.ready.Load() {
			http.Error(w, "not ready", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ready\n"))
	})
	return mux
}

// SetRoutes replaces the handler for every request that starts after it
// returns. Requests already in progress finish on the previous handler.
func (s *Server) SetRoutes(h http.Handler) { s.routes.Store(&h) }

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	(*s.routes.Load()).ServeHTTP(w, r)
}

// SetReady flips the readiness probe.
func (s *Server) SetReady(v bool) { s.ready.Store(v) }

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
