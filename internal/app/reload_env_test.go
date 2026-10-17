package app

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/wyrd-company/trowel/internal/config"
	"github.com/wyrd-company/trowel/internal/vault"
)

// logRecorder keeps every log message so tests can wait for reload
// outcomes.
type logRecorder struct {
	mu   sync.Mutex
	msgs []string
}

func (l *logRecorder) Enabled(context.Context, slog.Level) bool { return true }
func (l *logRecorder) WithAttrs([]slog.Attr) slog.Handler       { return l }
func (l *logRecorder) WithGroup(string) slog.Handler            { return l }
func (l *logRecorder) Handle(_ context.Context, r slog.Record) error {
	var b strings.Builder
	b.WriteString(r.Message)
	r.Attrs(func(a slog.Attr) bool {
		fmt.Fprintf(&b, " %s=%v", a.Key, a.Value)
		return true
	})
	l.mu.Lock()
	l.msgs = append(l.msgs, b.String())
	l.mu.Unlock()
	return nil
}

// count reports how many messages start with prefix.
func (l *logRecorder) count(prefix string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, m := range l.msgs {
		if strings.HasPrefix(m, prefix) {
			n++
		}
	}
	return n
}

func (l *logRecorder) find(prefix string) string {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, m := range l.msgs {
		if strings.HasPrefix(m, prefix) {
			return m
		}
	}
	return ""
}

const (
	reloaded     = "config reloaded"
	reloadFailed = "config reload failed"
)

type reloadEnv struct {
	t      *testing.T
	ns     *natsserver.Server
	url    string
	js     jetstream.JetStream
	listen string
	path   string
	app    *App
	logs   *logRecorder
	// stop ends Run and returns its error. It is safe to call twice.
	stop func() error
}

// startReloadEnv runs an App against an embedded NATS server with a stream
// covering events.>, loading the config returned by body.
func startReloadEnv(t *testing.T, body func(e *reloadEnv) string) *reloadEnv {
	t.Helper()
	return startReloadEnvAt(t, func(e *reloadEnv) string {
		path := filepath.Join(t.TempDir(), "config.yml")
		if err := os.WriteFile(path, []byte(body(e)), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	})
}

// startReloadEnvAt is startReloadEnv with the config file placed by place,
// which returns its path.
func startReloadEnvAt(t *testing.T, place func(e *reloadEnv) string) *reloadEnv {
	t.Helper()
	ns, url := startNATS(t)
	nc, err := nats.Connect(url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(nc.Close)
	js, _ := jetstream.New(nc)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := js.CreateStream(ctx, jetstream.StreamConfig{Name: "EVENTS", Subjects: []string{"events.>"}}); err != nil {
		t.Fatal(err)
	}
	e := &reloadEnv{t: t, ns: ns, url: url, js: js, listen: freePort(t), logs: &logRecorder{}}
	e.path = place(e)
	raw, err := os.ReadFile(e.path)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := vault.Resolve(context.Background(), cfg, slog.New(slog.DiscardHandler)); err != nil {
		t.Fatal(err)
	}
	e.app, err = New(cfg, slog.New(e.logs), WithReload(e.path, raw))
	if err != nil {
		t.Fatal(err)
	}
	runCtx, cancelRun := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- e.app.Run(runCtx) }()
	var once sync.Once
	var runErr error
	e.stop = func() error {
		once.Do(func() {
			cancelRun()
			runErr = <-done
		})
		return runErr
	}
	t.Cleanup(func() {
		if err := e.stop(); err != nil {
			t.Errorf("run: %v", err)
		}
	})
	waitReady(t, "http://"+e.listen+"/readyz")
	<-e.app.watching
	return e
}

// write replaces the config file the way an editor saving atomically does.
func (e *reloadEnv) write(body string) {
	e.t.Helper()
	tmp := e.path + ".tmp"
	if err := os.WriteFile(tmp, []byte(body), 0o600); err != nil {
		e.t.Fatal(err)
	}
	if err := os.Rename(tmp, e.path); err != nil {
		e.t.Fatal(err)
	}
}

// waitLog waits until prefix has been logged want times.
func (e *reloadEnv) waitLog(prefix string, want int) {
	e.t.Helper()
	waitFor(e.t, func() bool { return e.logs.count(prefix) >= want })
}

func (e *reloadEnv) post(path string, header http.Header) int {
	e.t.Helper()
	req, _ := http.NewRequest(http.MethodPost, "http://"+e.listen+path, bytes.NewReader([]byte(`{}`)))
	for k, v := range header {
		req.Header[k] = v
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

// conns lists the NATS server's client connection IDs by name.
func (e *reloadEnv) conns() map[string][]uint64 {
	e.t.Helper()
	cz, err := e.ns.Connz(&natsserver.ConnzOptions{})
	if err != nil {
		e.t.Fatal(err)
	}
	out := map[string][]uint64{}
	for _, c := range cz.Conns {
		out[c.Name] = append(out[c.Name], c.Cid)
	}
	return out
}

func (e *reloadEnv) base(webhooks string) string {
	return fmt.Sprintf("server:\n  listen: %q\nnats:\n  url: %q\nwebhooks:\n%s", e.listen, e.url, webhooks)
}

const hookA = "  - path: /hooks/a\n    subject: events.a\n"
const hookB = "  - path: /hooks/b\n    subject: events.b\n"

// holdRequest starts a webhook request whose body stays open until the
// returned finish is called, and waits until the current generation is
// serving it. finish returns the response status.
func (e *reloadEnv) holdRequest(path string) (finish func() int) {
	e.t.Helper()
	body, feed := io.Pipe()
	status := make(chan int, 1)
	g := e.app.gen.Load()
	g.mu.Lock()
	before := g.active
	g.mu.Unlock()
	go func() {
		req, _ := http.NewRequest(http.MethodPost, "http://"+e.listen+path, body)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			status <- 0
			return
		}
		resp.Body.Close()
		status <- resp.StatusCode
	}()
	waitFor(e.t, func() bool { g.mu.Lock(); defer g.mu.Unlock(); return g.active == before+1 })
	return func() int {
		_, _ = feed.Write([]byte(`{}`))
		feed.Close()
		return <-status
	}
}

// countingDest is an HTTP destination that counts requests.
func countingDest(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var n atomic.Int32
	dest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(dest.Close)
	return dest, &n
}

func relayTo(name, subject, url string) string {
	return fmt.Sprintf("  - name: %s\n    subject: %s\n    destinations:\n      - name: api\n        http:\n          url: %q\n", name, subject, url)
}
