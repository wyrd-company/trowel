package app

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

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
		"bad route":   func(e *reloadEnv) string { return e.base(hookB + "  - path: \"/hooks/{\"\n    subject: events.c\n") },
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

// TestReloadFollowsSymlinkRetarget points the config symlink at a file in
// a second directory, then edits that file.
func TestReloadFollowsSymlinkRetarget(t *testing.T) {
	first := filepath.Join(t.TempDir(), "real.yml")
	second := filepath.Join(t.TempDir(), "real.yml")
	var link string
	e := startReloadEnvAt(t, func(e *reloadEnv) string {
		link = filepath.Join(t.TempDir(), "config.yml")
		if err := os.WriteFile(first, []byte(e.base(hookA)), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(first, link); err != nil {
			t.Skip("symlinks unavailable:", err)
		}
		return link
	})
	if err := os.WriteFile(second, []byte(e.base(hookA+hookB)), 0o600); err != nil {
		t.Fatal(err)
	}
	tmp := link + ".tmp"
	if err := os.Symlink(second, tmp); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, link); err != nil {
		t.Fatal(err)
	}
	e.waitLog(reloaded, 1)
	if err := os.WriteFile(second, []byte(e.base(hookB)), 0o600); err != nil {
		t.Fatal(err)
	}
	e.waitLog(reloaded, 2)
	if code := e.post("/hooks/a", nil); code != http.StatusNotFound {
		t.Errorf("after editing the new target /hooks/a status %d", code)
	}
}

// TestReloadCoalescesRapidWrites saves the file several times, each save
// within the settle delay of the last, and expects one reload of the last
// content.
func TestReloadCoalescesRapidWrites(t *testing.T) {
	e := startReloadEnv(t, func(e *reloadEnv) string { return e.base(hookA) })
	for _, hooks := range []string{hookA + hookB, hookB, hookA + hookB} {
		e.write(e.base(hooks))
		time.Sleep(settleDelay / 5)
	}
	e.waitLog(reloaded, 1)
	time.Sleep(3 * settleDelay)
	if n := e.logs.count(reloaded); n != 1 {
		t.Errorf("%d reloads, want 1", n)
	}
	if code := e.post("/hooks/a", nil); code != http.StatusAccepted {
		t.Errorf("/hooks/a status %d after the last save", code)
	}
}
