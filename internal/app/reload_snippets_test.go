package app

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wyrd-company/trowel/internal/config"
)

// snippetDir is the env's snippet directory.
func (e *reloadEnv) snippetDir() string { return config.SnippetDir(e.path) }

// writeSnippet replaces the snippet name the way an editor saving
// atomically does, creating the snippet directory when needed.
func (e *reloadEnv) writeSnippet(name, body string) {
	e.t.Helper()
	if err := os.MkdirAll(e.snippetDir(), 0o755); err != nil {
		e.t.Fatal(err)
	}
	p := filepath.Join(e.snippetDir(), name)
	// The temporary name is not a snippet, so it never loads half written.
	tmp := filepath.Join(e.snippetDir(), "."+name+".tmp")
	if err := os.WriteFile(tmp, []byte(body), 0o600); err != nil {
		e.t.Fatal(err)
	}
	if err := os.Rename(tmp, p); err != nil {
		e.t.Fatal(err)
	}
}

func snippetHooks(hooks string) string { return "webhooks:\n" + hooks }

func TestStartupLoadsSnippets(t *testing.T) {
	e := startReloadEnvAt(t, func(e *reloadEnv) string {
		dir := t.TempDir()
		path := filepath.Join(dir, "config.yml")
		if err := os.WriteFile(path, []byte(e.base(hookA)), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(dir, "conf.d"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "conf.d", "b.yml"), []byte(snippetHooks(hookB)), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	})
	for _, p := range []string{"/hooks/a", "/hooks/b"} {
		if code := e.post(p, nil); code != http.StatusAccepted {
			t.Errorf("%s status %d", p, code)
		}
	}
}

func TestReloadAppliesAddedChangedAndRemovedSnippets(t *testing.T) {
	e := startReloadEnv(t, func(e *reloadEnv) string { return e.base(hookA) })

	// The snippet directory does not exist at startup, so this also proves
	// a directory created later is watched.
	e.writeSnippet("b.yml", snippetHooks(hookB))
	e.waitLog(reloaded, 1)
	if m := e.logs.find(reloaded); !strings.Contains(m, "snippets=1") {
		t.Errorf("reload log %q does not count the snippet", m)
	}
	if code := e.post("/hooks/b", nil); code != http.StatusAccepted {
		t.Fatalf("added snippet /hooks/b status %d", code)
	}

	e.writeSnippet("b.yml", snippetHooks("  - path: /hooks/c\n    subject: events.c\n"))
	e.waitLog(reloaded, 2)
	if code := e.post("/hooks/c", nil); code != http.StatusAccepted {
		t.Errorf("changed snippet /hooks/c status %d", code)
	}
	if code := e.post("/hooks/b", nil); code != http.StatusNotFound {
		t.Errorf("replaced snippet /hooks/b status %d", code)
	}

	if err := os.Remove(filepath.Join(e.snippetDir(), "b.yml")); err != nil {
		t.Fatal(err)
	}
	e.waitLog(reloaded, 3)
	if code := e.post("/hooks/c", nil); code != http.StatusNotFound {
		t.Errorf("removed snippet /hooks/c status %d", code)
	}
	if code := e.post("/hooks/a", nil); code != http.StatusAccepted {
		t.Errorf("config file /hooks/a status %d", code)
	}
}

func TestReloadWatchesRecreatedSnippetDir(t *testing.T) {
	e := startReloadEnv(t, func(e *reloadEnv) string { return e.base(hookA) })
	e.writeSnippet("b.yml", snippetHooks(hookB))
	e.waitLog(reloaded, 1)
	if err := os.RemoveAll(e.snippetDir()); err != nil {
		t.Fatal(err)
	}
	e.waitLog(reloaded, 2)
	e.writeSnippet("c.yml", snippetHooks("  - path: /hooks/c\n    subject: events.c\n"))
	e.waitLog(reloaded, 3)
	// A second change inside the recreated directory proves it is watched,
	// not only seen being created.
	e.writeSnippet("c.yml", snippetHooks("  - path: /hooks/d\n    subject: events.d\n"))
	e.waitLog(reloaded, 4)
	if code := e.post("/hooks/d", nil); code != http.StatusAccepted {
		t.Errorf("recreated snippet directory /hooks/d status %d", code)
	}
}

func TestReloadKeepsCurrentConfigWhenSnippetFails(t *testing.T) {
	cases := map[string]string{
		"unparseable":       "webhooks: [",
		"duplicate path":    snippetHooks(hookA),
		"section set twice": "nats:\n  name: other\n",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			e := startReloadEnv(t, func(e *reloadEnv) string { return e.base(hookA) })
			e.writeSnippet("bad.yml", body)
			e.waitLog(reloadFailed, 1)
			if n := e.logs.count(reloaded); n != 0 {
				t.Fatalf("reloaded %d times", n)
			}
			if code := e.post("/hooks/a", nil); code != http.StatusAccepted {
				t.Errorf("current /hooks/a status %d", code)
			}
		})
	}
}

func TestReloadIgnoresNonSnippetFiles(t *testing.T) {
	e := startReloadEnv(t, func(e *reloadEnv) string { return e.base(hookA) })
	if err := os.MkdirAll(e.snippetDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	// Creating the directory is a change in the config directory, which is
	// skipped because no file changed, and starts the snippet watch.
	time.Sleep(3 * settleDelay)
	for _, name := range []string{"README.md", ".swap.yml", "b.yml.bak"} {
		if err := os.WriteFile(filepath.Join(e.snippetDir(), name), []byte("webhooks: ["), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(3 * settleDelay)
	if n := e.logs.count(reloaded) + e.logs.count(reloadFailed); n != 0 {
		t.Errorf("files that are not snippets caused %d reload attempts", n)
	}
}
