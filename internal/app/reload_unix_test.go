//go:build unix

package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"testing"
)

// TestReloadOnSIGHUPRereadsSecretFiles rotates a secret file outside the
// config directory and applies it with SIGHUP.
func TestReloadOnSIGHUPRereadsSecretFiles(t *testing.T) {
	secret := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(secret, []byte("old-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	e := startReloadEnv(t, func(e *reloadEnv) string {
		return e.base(fmt.Sprintf("  - path: /hooks/a\n    subject: events.a\n    verify:\n      static:\n        header: X-Token\n        secret_file: %q\n", secret))
	})
	token := func(v string) http.Header { return http.Header{"X-Token": {v}} }
	if code := e.post("/hooks/a", token("old-token")); code != http.StatusAccepted {
		t.Fatalf("old token status %d", code)
	}
	if err := os.WriteFile(secret, []byte("new-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Kill(os.Getpid(), syscall.SIGHUP); err != nil {
		t.Fatal(err)
	}
	e.waitLog(reloaded, 1)
	if code := e.post("/hooks/a", token("new-token")); code != http.StatusAccepted {
		t.Errorf("new token status %d", code)
	}
	if code := e.post("/hooks/a", token("old-token")); code != http.StatusUnauthorized {
		t.Errorf("old token status %d after rotation", code)
	}
}

// TestReloadOnSIGHUPRereadsVaultSecrets rotates a secret in a KV version 2
// server and applies it with SIGHUP.
func TestReloadOnSIGHUPRereadsVaultSecrets(t *testing.T) {
	var value atomic.Value
	value.Store("old-token")
	kv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/secret/data/trowel/hook" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{
			"data":     map[string]any{"token": value.Load()},
			"metadata": map[string]any{"version": 1},
		}})
	}))
	defer kv.Close()
	e := startReloadEnv(t, func(e *reloadEnv) string {
		return fmt.Sprintf("vault:\n  address: %q\n", kv.URL) + e.base("  - path: /hooks/a\n    subject: events.a\n    verify:\n      static:\n        header: X-Token\n        secret_vault: {path: trowel/hook, field: token}\n")
	})
	token := func(v string) http.Header { return http.Header{"X-Token": {v}} }
	if code := e.post("/hooks/a", token("old-token")); code != http.StatusAccepted {
		t.Fatalf("old token status %d", code)
	}
	value.Store("new-token")
	if err := syscall.Kill(os.Getpid(), syscall.SIGHUP); err != nil {
		t.Fatal(err)
	}
	e.waitLog(reloaded, 1)
	if code := e.post("/hooks/a", token("new-token")); code != http.StatusAccepted {
		t.Errorf("new token status %d", code)
	}
}
