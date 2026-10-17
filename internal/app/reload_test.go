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

func TestReloadAppliesChangedConfig(t *testing.T) {
	e := startReloadEnv(t, func(e *reloadEnv) string { return e.base(hookA) })
	if code := e.post("/hooks/b", nil); code != http.StatusNotFound {
		t.Fatalf("before reload /hooks/b status %d", code)
	}
	before := e.conns()["trowel"]
	e.write(e.base(hookB))
	e.waitLog(reloaded, 1)
	if after := e.conns()["trowel"]; len(after) != 1 || len(before) != 1 || after[0] != before[0] {
		t.Errorf("unchanged nats settings replaced the connection: %v then %v", before, after)
	}
	if code := e.post("/hooks/b", nil); code != http.StatusAccepted {
		t.Errorf("after reload /hooks/b status %d", code)
	}
	if code := e.post("/hooks/a", nil); code != http.StatusNotFound {
		t.Errorf("after reload removed /hooks/a status %d", code)
	}
}

func TestReloadKeepsCurrentConfigWhenNewOneFails(t *testing.T) {
	cases := map[string]func(e *reloadEnv) string{
		"unparseable": func(*reloadEnv) string { return "webhooks: [" },
		"invalid":     func(e *reloadEnv) string { return e.base("  - path: no-slash\n    subject: events.b\n") },
		"unset env":   func(e *reloadEnv) string { return e.base("  - path: /hooks/b\n    subject: ${TROWEL_TEST_UNSET}\n") },
		"server":      func(e *reloadEnv) string { return strings.Replace(e.base(hookB), e.listen, "127.0.0.1:1", 1) },
		"nats down":   func(e *reloadEnv) string { return strings.Replace(e.base(hookB), e.url, "nats://127.0.0.1:1", 1) },
		"start": func(e *reloadEnv) string {
			return e.base(hookB) + "subjects:\n  - name: missing\n    stream: NOPE\n    subject: other.x\n    destinations:\n      - name: d\n        command:\n          argv: [\"true\"]\n"
		},
	}
	for name, next := range cases {
		t.Run(name, func(t *testing.T) {
			e := startReloadEnv(t, func(e *reloadEnv) string { return e.base(hookA) })
			e.write(next(e))
			e.waitLog(reloadFailed, 1)
			if n := e.logs.count(reloaded); n != 0 {
				t.Fatalf("reloaded %d times", n)
			}
			if code := e.post("/hooks/a", nil); code != http.StatusAccepted {
				t.Errorf("current /hooks/a status %d", code)
			}
			if code := e.post("/hooks/b", nil); code != http.StatusNotFound {
				t.Errorf("rejected /hooks/b status %d", code)
			}
		})
	}
}

func TestReloadRejectsServerChangeWithReason(t *testing.T) {
	e := startReloadEnv(t, func(e *reloadEnv) string { return e.base(hookA) })
	e.write(strings.Replace(e.base(hookA), e.listen, "127.0.0.1:1", 1))
	e.waitLog(reloadFailed, 1)
	if m := e.logs.find(reloadFailed); !strings.Contains(m, errServerChanged.Error()) {
		t.Errorf("failure log %q does not name the server change", m)
	}
}

func TestReloadIgnoresUnrelatedDirectoryChanges(t *testing.T) {
	e := startReloadEnv(t, func(e *reloadEnv) string { return e.base(hookA) })
	if err := os.WriteFile(filepath.Join(filepath.Dir(e.path), "other.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Rewriting identical content is not a change either.
	raw, _ := os.ReadFile(e.path)
	e.write(string(raw))
	time.Sleep(3 * settleDelay)
	if n := e.logs.count(reloaded) + e.logs.count(reloadFailed); n != 0 {
		t.Errorf("unrelated changes caused %d reload attempts", n)
	}
}

// TestReloadFollowsSymlinkTarget edits the file a symlinked config path
// points at, in another directory.
func TestReloadFollowsSymlinkTarget(t *testing.T) {
	target := filepath.Join(t.TempDir(), "real.yml")
	e := startReloadEnvAt(t, func(e *reloadEnv) string {
		link := filepath.Join(t.TempDir(), "config.yml")
		if err := os.WriteFile(target, []byte(e.base(hookA)), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, link); err != nil {
			t.Skip("symlinks unavailable:", err)
		}
		return link
	})
	if err := os.WriteFile(target, []byte(e.base(hookB)), 0o600); err != nil {
		t.Fatal(err)
	}
	e.waitLog(reloaded, 1)
	if code := e.post("/hooks/b", nil); code != http.StatusAccepted {
		t.Errorf("after reload /hooks/b status %d", code)
	}
}

// TestReloadSwitchesNATSConnection replaces the connection when its
// settings change and drains the old one.
func TestReloadSwitchesNATSConnection(t *testing.T) {
	e := startReloadEnv(t, func(e *reloadEnv) string { return e.base(hookA) })
	if len(e.conns()["trowel"]) != 1 {
		t.Fatal("initial connection not named trowel")
	}
	e.write(strings.Replace(e.base(hookA), "nats:\n", "nats:\n  name: trowel-next\n", 1))
	e.waitLog(reloaded, 1)
	waitFor(t, func() bool { c := e.conns(); return len(c["trowel-next"]) == 1 && len(c["trowel"]) == 0 })
	if code := e.post("/hooks/a", nil); code != http.StatusAccepted {
		t.Errorf("publish on new connection status %d", code)
	}
}

// TestReloadClosesNewConnectionWhenStartFails opens a connection for
// changed nats settings, fails to start a consumer, and expects only the
// original connection to remain.
func TestReloadClosesNewConnectionWhenStartFails(t *testing.T) {
	e := startReloadEnv(t, func(e *reloadEnv) string { return e.base(hookA) })
	broken := strings.Replace(e.base(hookA), "nats:\n", "nats:\n  name: trowel-next\n", 1) +
		"subjects:\n  - name: missing\n    stream: NOPE\n    subject: other.x\n    destinations:\n      - name: d\n        command:\n          argv: [\"true\"]\n"
	e.write(broken)
	e.waitLog(reloadFailed, 1)
	waitFor(t, func() bool { return len(e.conns()["trowel-next"]) == 0 })
	if len(e.conns()["trowel"]) != 1 {
		t.Error("original connection closed")
	}
}

// TestReloadKeepsOldConnectionForRequestsInProgress holds a webhook request
// open across a reload that replaces the connection, then finishes it.
func TestReloadKeepsOldConnectionForRequestsInProgress(t *testing.T) {
	e := startReloadEnv(t, func(e *reloadEnv) string { return e.base(hookA) })
	body, feed := io.Pipe()
	status := make(chan int, 1)
	go func() {
		req, _ := http.NewRequest(http.MethodPost, "http://"+e.listen+"/hooks/a", body)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			status <- 0
			return
		}
		resp.Body.Close()
		status <- resp.StatusCode
	}()
	old := e.app.gen.Load()
	waitFor(t, func() bool { old.mu.Lock(); defer old.mu.Unlock(); return old.active == 1 })

	e.write(strings.Replace(e.base(hookA), "nats:\n", "nats:\n  name: trowel-next\n", 1))
	e.waitLog(reloaded, 1)
	time.Sleep(500 * time.Millisecond)
	if len(e.conns()["trowel"]) != 1 {
		t.Error("old connection closed while a request was using it")
	}
	_, _ = feed.Write([]byte(`{}`))
	feed.Close()
	if code := <-status; code != http.StatusAccepted {
		t.Errorf("request in progress finished with status %d", code)
	}
	waitFor(t, func() bool { return len(e.conns()["trowel"]) == 0 })
}

// TestReloadCoalescesRapidWrites saves the file several times within the
// settle delay and expects one reload of the last content.
func TestReloadCoalescesRapidWrites(t *testing.T) {
	e := startReloadEnv(t, func(e *reloadEnv) string { return e.base(hookA) })
	e.write(e.base(hookA + hookB))
	e.write(e.base(hookB))
	e.write(e.base(hookA + hookB))
	e.waitLog(reloaded, 1)
	time.Sleep(3 * settleDelay)
	if n := e.logs.count(reloaded); n != 1 {
		t.Errorf("%d reloads, want 1", n)
	}
	if code := e.post("/hooks/a", nil); code != http.StatusAccepted {
		t.Errorf("/hooks/a status %d after the last save", code)
	}
}

// TestReloadLetsDeliveryInProgressFinish holds a delivery open across a
// reload and checks it completes once, uncancelled.
func TestReloadLetsDeliveryInProgressFinish(t *testing.T) {
	entered := make(chan struct{}, 4)
	release := make(chan struct{})
	var requests, cancelled atomic.Int32
	dest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		entered <- struct{}{}
		<-release
		if r.Context().Err() != nil {
			cancelled.Add(1)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer dest.Close()
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	relayCfg := fmt.Sprintf("subjects:\n  - name: slow\n    subject: events.a\n    consumer:\n      ack_wait: 30s\n    destinations:\n      - name: api\n        http:\n          url: %q\n", dest.URL)
	e := startReloadEnv(t, func(e *reloadEnv) string { return e.base(hookA) + relayCfg })
	if code := e.post("/hooks/a", nil); code != http.StatusAccepted {
		t.Fatalf("post status %d", code)
	}
	<-entered
	e.write(e.base(hookA+hookB) + relayCfg)
	e.waitLog(reloaded, 1)
	close(release)
	waitFor(t, func() bool { return e.logs.count("delivered") >= 1 })
	time.Sleep(500 * time.Millisecond)
	if n := requests.Load(); n != 1 {
		t.Errorf("destination got %d requests, want 1", n)
	}
	if n := cancelled.Load(); n != 0 {
		t.Errorf("%d deliveries were cancelled by the reload", n)
	}
}

// TestShutdownCutsRetirementShort stops Trowel while a replaced worker
// still holds a delivery, and expects Run to return without it.
func TestShutdownCutsRetirementShort(t *testing.T) {
	entered := make(chan struct{}, 4)
	release := make(chan struct{})
	dest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entered <- struct{}{}
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer dest.Close()
	defer close(release)
	relayCfg := fmt.Sprintf("subjects:\n  - name: slow\n    subject: events.a\n    consumer:\n      ack_wait: 30s\n    destinations:\n      - name: api\n        http:\n          url: %q\n          timeout: 60s\n", dest.URL)
	e := startReloadEnv(t, func(e *reloadEnv) string { return e.base(hookA) + relayCfg })
	if code := e.post("/hooks/a", nil); code != http.StatusAccepted {
		t.Fatalf("post status %d", code)
	}
	<-entered
	e.write(e.base(hookA+hookB) + relayCfg)
	e.waitLog(reloaded, 1)

	stopped := make(chan error, 1)
	go func() { stopped <- e.stop() }()
	select {
	case err := <-stopped:
		if err != nil {
			t.Errorf("run: %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("shutdown waited for a retired delivery")
	}
}
