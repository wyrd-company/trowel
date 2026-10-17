// Package relay consumes JetStream subjects and delivers messages to HTTP
// endpoints or commands, with retry and dead-letter handling.
package relay

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/wyrd-company/trowel/internal/config"
	"github.com/wyrd-company/trowel/internal/message"
	"github.com/wyrd-company/trowel/internal/signing"
)

// Destination delivers one message. A returned error triggers a retry.
type Destination interface {
	Deliver(ctx context.Context, m *message.Message) error
}

// NewDestination builds the destination described by cfg.
func NewDestination(cfg config.Destination, log *slog.Logger) (Destination, error) {
	switch {
	case cfg.HTTP != nil:
		return newHTTPDestination(cfg.HTTP)
	case cfg.Command != nil:
		return newCommandDestination(cfg.Command, log), nil
	}
	return nil, fmt.Errorf("destination %s: no delivery method", cfg.Name)
}

// HTTPDestination forwards the message as an HTTP request.
type HTTPDestination struct {
	cfg    config.HTTP
	client *http.Client
	signer signing.Signer
	now    func() time.Time
}

func newHTTPDestination(cfg *config.HTTP) (*HTTPDestination, error) {
	s, err := signing.NewSigner(cfg.Sign)
	if err != nil {
		return nil, err
	}
	return &HTTPDestination{
		cfg:    *cfg,
		client: &http.Client{Timeout: cfg.Timeout},
		signer: s,
		now:    time.Now,
	}, nil
}

// Deliver sends the payload with relayed headers, static headers, and the
// signature. Any non-2xx status is an error.
func (d *HTTPDestination) Deliver(ctx context.Context, m *message.Message) error {
	req, err := http.NewRequestWithContext(ctx, d.cfg.Method, d.cfg.URL, bytes.NewReader(m.Payload))
	if err != nil {
		return err
	}
	for k, vals := range message.CopyHeaders(m.Headers) {
		req.Header[k] = append([]string(nil), vals...)
	}
	for k, v := range d.cfg.Headers {
		req.Header.Set(k, v)
	}
	req.Header.Set("X-Relay-Subject", m.Subject)
	if err := d.signer.Sign(req.Header, m.Payload, d.now()); err != nil {
		return err
	}
	resp, err := d.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("http %s %s: status %d", d.cfg.Method, d.cfg.URL, resp.StatusCode)
	}
	return nil
}

// CommandDestination runs a child process with the payload on stdin and
// headers in the environment.
type CommandDestination struct {
	cfg config.Command
	log *slog.Logger
}

func newCommandDestination(cfg *config.Command, log *slog.Logger) *CommandDestination {
	return &CommandDestination{cfg: *cfg, log: log}
}

const outputLimit = 16 << 10

// Deliver runs the command. A non-zero exit or a timeout is an error.
func (d *CommandDestination) Deliver(ctx context.Context, m *message.Message) error {
	ctx, cancel := context.WithTimeout(ctx, d.cfg.Timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, d.cfg.Argv[0], d.cfg.Argv[1:]...)
	cmd.Dir = d.cfg.Dir
	cmd.Stdin = bytes.NewReader(m.Payload)
	cmd.Env = d.env(m)
	var stdout, stderr limitedBuffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.WaitDelay = 2 * time.Second

	err := cmd.Run()
	if stdout.Len() > 0 {
		d.log.Info("command output", "stdout", strings.TrimSpace(stdout.String()))
	}
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("command %s: timed out after %s", d.cfg.Argv[0], d.cfg.Timeout)
		}
		return fmt.Errorf("command %s: %w: %s", d.cfg.Argv[0], err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

func (d *CommandDestination) env(m *message.Message) []string {
	env := os.Environ()
	for k, v := range d.cfg.Env {
		env = append(env, k+"="+v)
	}
	env = append(env, "RELAY_SUBJECT="+m.Subject)
	for k, vals := range message.CopyHeaders(m.Headers) {
		env = append(env, d.cfg.EnvPrefix+envName(k)+"="+strings.Join(vals, ", "))
	}
	return env
}

// envName converts a header name to an environment-safe identifier.
func envName(h string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(h) {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	return b.String()
}

// limitedBuffer keeps the first outputLimit bytes and discards the rest.
type limitedBuffer struct{ bytes.Buffer }

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if room := outputLimit - b.Len(); room > 0 {
		if len(p) > room {
			p = p[:room]
		}
		b.Buffer.Write(p)
	}
	return len(p), nil
}
