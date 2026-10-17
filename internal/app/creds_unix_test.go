//go:build unix

package app

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/nats-io/jwt/v2"
	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nkeys"

	"github.com/wyrd-company/trowel/internal/config"
)

// jwtNATS runs a NATS server that authenticates users by JWT, and returns
// its URL and a function that issues credentials for a new user.
func jwtNATS(t *testing.T) (*natsserver.Server, string, func() []byte) {
	t.Helper()
	operator, _ := nkeys.CreateOperator()
	opub, _ := operator.PublicKey()
	ojwt, err := jwt.NewOperatorClaims(opub).Encode(operator)
	if err != nil {
		t.Fatal(err)
	}
	oclaims, err := jwt.DecodeOperatorClaims(ojwt)
	if err != nil {
		t.Fatal(err)
	}
	account, _ := nkeys.CreateAccount()
	apub, _ := account.PublicKey()
	ajwt, err := jwt.NewAccountClaims(apub).Encode(operator)
	if err != nil {
		t.Fatal(err)
	}
	resolver := &natsserver.MemAccResolver{}
	if err := resolver.Store(apub, ajwt); err != nil {
		t.Fatal(err)
	}
	ns, err := natsserver.NewServer(&natsserver.Options{
		Port: -1, NoLog: true, NoSigs: true,
		TrustedOperators: []*jwt.OperatorClaims{oclaims},
		AccountResolver:  resolver,
	})
	if err != nil {
		t.Fatal(err)
	}
	go ns.Start()
	if !ns.ReadyForConnections(10 * time.Second) {
		t.Fatal("nats server not ready")
	}
	t.Cleanup(ns.Shutdown)
	issue := func() []byte {
		user, _ := nkeys.CreateUser()
		upub, _ := user.PublicKey()
		ujwt, err := jwt.NewUserClaims(upub).Encode(account)
		if err != nil {
			t.Fatal(err)
		}
		seed, _ := user.Seed()
		creds, err := jwt.FormatUserConfig(ujwt, seed)
		if err != nil {
			t.Fatal(err)
		}
		return creds
	}
	return ns, ns.ClientURL(), issue
}

// TestReloadOnSIGHUPReconnectsWithRotatedCredsFile replaces the contents
// of creds_file at the same path, then with garbage, reloading each time.
func TestReloadOnSIGHUPReconnectsWithRotatedCredsFile(t *testing.T) {
	ns, url, issue := jwtNATS(t)
	creds := filepath.Join(t.TempDir(), "user.creds")
	if err := os.WriteFile(creds, issue(), 0o600); err != nil {
		t.Fatal(err)
	}
	logs := &logRecorder{}
	path := filepath.Join(t.TempDir(), "config.yml")
	raw := []byte(fmt.Sprintf("server:\n  listen: %q\nnats:\n  url: %q\n  creds_file: %q\nwebhooks:\n%s", freePort(t), url, creds, hookA))
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	a, err := New(cfg, slog.New(logs), WithReload(path, raw))
	if err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	t.Cleanup(func() { stop(); <-done })
	<-a.watching
	users := func() map[string]bool {
		cz, err := ns.Connz(&natsserver.ConnzOptions{Username: true})
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]bool{}
		for _, c := range cz.Conns {
			out[c.AuthorizedUser] = true
		}
		return out
	}
	first := users()
	if len(first) != 1 {
		t.Fatalf("connections %v", first)
	}

	rotated := issue()
	if err := os.WriteFile(creds, rotated, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Kill(os.Getpid(), syscall.SIGHUP); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return logs.count(reloaded) == 1 })
	waitFor(t, func() bool {
		now := users()
		for u := range first {
			if now[u] {
				return false
			}
		}
		return len(now) == 1
	})
	second := users()

	if err := os.WriteFile(creds, []byte("not a credentials file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Kill(os.Getpid(), syscall.SIGHUP); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return logs.count(reloadFailed) == 1 })
	now := users()
	for u := range second {
		if !now[u] {
			t.Errorf("rejected credentials replaced the connection: %v then %v", second, now)
		}
	}
}
