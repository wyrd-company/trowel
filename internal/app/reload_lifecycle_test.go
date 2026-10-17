package app

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wyrd-company/trowel/internal/config"
	"github.com/wyrd-company/trowel/internal/relay"
)

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
	finish := e.holdRequest("/hooks/a")
	e.write(strings.Replace(e.base(hookA), "nats:\n", "nats:\n  name: trowel-next\n", 1))
	e.waitLog(reloaded, 1)
	time.Sleep(500 * time.Millisecond)
	if len(e.conns()["trowel"]) != 1 {
		t.Error("old connection closed while a request was using it")
	}
	if code := finish(); code != http.StatusAccepted {
		t.Errorf("request in progress finished with status %d", code)
	}
	waitFor(t, func() bool { return len(e.conns()["trowel"]) == 0 })
}

// TestReloadDeliversNothingWhenStartFails queues messages for new relays
// whose generation then fails to start on a later relay, and expects none
// of the new relays to take a message.
func TestReloadDeliversNothingWhenStartFails(t *testing.T) {
	dest, delivered := countingDest(t)
	e := startReloadEnv(t, func(e *reloadEnv) string { return e.base(hookA) })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for i := range 20 {
		if _, err := e.js.Publish(ctx, "events.backlog", []byte(fmt.Sprintf(`{"n":%d}`, i))); err != nil {
			t.Fatal(err)
		}
	}
	relays := "subjects:\n"
	for i := range 8 {
		relays += relayTo(fmt.Sprintf("backlog%d", i), "events.backlog", dest.URL)
	}
	e.write(e.base(hookA) + relays +
		"  - name: missing\n    stream: NOPE\n    subject: other.x\n    destinations:\n      - name: d\n        command:\n          argv: [\"true\"]\n")
	e.waitLog(reloadFailed, 1)
	time.Sleep(500 * time.Millisecond)
	if n := delivered.Load(); n != 0 {
		t.Errorf("rejected generation delivered %d messages", n)
	}
	for i := range 8 {
		info, err := e.js.Consumer(ctx, "EVENTS", fmt.Sprintf("relay-backlog%d-api", i))
		if err != nil {
			t.Fatal(err)
		}
		if info.CachedInfo().Delivered.Consumer != 0 {
			t.Errorf("rejected consumer backlog%d took %d messages", i, info.CachedInfo().Delivered.Consumer)
		}
	}
}

// TestActivateDeliversNothingWhenConsumeFails prepares a candidate relay
// with a backlog, then fails to subscribe a later worker, and expects the
// candidate to deliver nothing and return what it received.
func TestActivateDeliversNothingWhenConsumeFails(t *testing.T) {
	dest, delivered := countingDest(t)
	e := startReloadEnv(t, func(e *reloadEnv) string { return e.base(hookA) })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for i := range 20 {
		if _, err := e.js.Publish(ctx, "events.backlog", []byte(fmt.Sprintf(`{"n":%d}`, i))); err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := config.Parse([]byte(e.base(hookA) + "subjects:\n" + relayTo("backlog", "events.backlog", dest.URL)))
	if err != nil {
		t.Fatal(err)
	}
	cur := e.app.gen.Load()
	g, err := build(cfg, cur.conn, e.app.server, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	if err := g.prepare(ctx); err != nil {
		t.Fatal(err)
	}
	// A worker that was never prepared cannot subscribe.
	unprepared, err := relay.NewWorkers(config.SubjectRelay{Name: "late", Subject: "events.late",
		Destinations: []config.Destination{{Name: "d", Command: &config.Command{Argv: []string{"true"}}}}}, cur.conn.js, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	g.workers = append(g.workers, unprepared...)
	if err := g.activate(); err == nil {
		t.Fatal("activate succeeded with an unprepared worker")
	}
	time.Sleep(time.Second)
	if n := delivered.Load(); n != 0 {
		t.Errorf("rejected generation delivered %d messages", n)
	}
	cons, err := e.js.Consumer(ctx, "EVENTS", "relay-backlog-api")
	if err != nil {
		t.Fatal(err)
	}
	if acked := cons.CachedInfo().AckFloor.Consumer; acked != 0 {
		t.Errorf("rejected consumer acknowledged %d messages", acked)
	}
}

// TestReloadStopsRemovedRelayWhileRequestsFinish removes a relay while a
// webhook request on the old generation is still open, and expects the
// removed relay to deliver nothing published afterwards.
func TestReloadStopsRemovedRelayWhileRequestsFinish(t *testing.T) {
	dest, delivered := countingDest(t)
	relays := "subjects:\n" + relayTo("removed", "events.removed", dest.URL)
	e := startReloadEnv(t, func(e *reloadEnv) string { return e.base(hookA) + relays })
	finish := e.holdRequest("/hooks/a")
	e.write(e.base(hookA))
	e.waitLog(reloaded, 1)
	time.Sleep(500 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := e.js.Publish(ctx, "events.removed", []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Second)
	if n := delivered.Load(); n != 0 {
		t.Errorf("removed relay delivered %d messages", n)
	}
	if code := finish(); code != http.StatusAccepted {
		t.Errorf("request in progress finished with status %d", code)
	}
}

// TestReloadKeepsSharedConnectionForOldestRequest holds a request on the
// first generation across a reload that shares its connection and a second
// reload that replaces it, and expects the request to publish.
func TestReloadKeepsSharedConnectionForOldestRequest(t *testing.T) {
	e := startReloadEnv(t, func(e *reloadEnv) string { return e.base(hookA) })
	finish := e.holdRequest("/hooks/a")
	e.write(e.base(hookA + hookB))
	e.waitLog(reloaded, 1)
	e.write(strings.Replace(e.base(hookA), "nats:\n", "nats:\n  name: trowel-next\n", 1))
	e.waitLog(reloaded, 2)
	time.Sleep(500 * time.Millisecond)
	if len(e.conns()["trowel"]) != 1 {
		t.Error("shared connection closed while the oldest generation was using it")
	}
	if code := finish(); code != http.StatusAccepted {
		t.Errorf("oldest request finished with status %d", code)
	}
	waitFor(t, func() bool { return len(e.conns()["trowel"]) == 0 })
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
