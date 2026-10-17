package vault

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openbao/openbao/api/v2"
	"gopkg.in/yaml.v3"

	"github.com/wyrd-company/trowel/internal/config"
)

const rootToken = "sample-root-token"

// devServer is a real `bao server -dev -dev-tls` process. Tests skip when
// no bao binary is available; CI sets TROWEL_BAO so they always run there.
type devServer struct {
	addr   string
	caFile string
	root   *api.Client
}

func startBao(t *testing.T) *devServer {
	t.Helper()
	bin := os.Getenv("TROWEL_BAO")
	if bin == "" {
		var err error
		if bin, err = exec.LookPath("bao"); err != nil {
			t.Skip("bao executable not available; set TROWEL_BAO")
		}
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	listen := l.Addr().String()
	l.Close()

	certDir := t.TempDir()
	cmd := exec.Command(bin, "server", "-dev", "-dev-tls",
		"-dev-root-token-id="+rootToken,
		"-dev-listen-address="+listen,
		"-dev-tls-cert-dir="+certDir)
	cmd.Env = []string{"HOME=" + t.TempDir(), "PATH=" + os.Getenv("PATH")}
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })

	s := &devServer{addr: "https://" + listen, caFile: filepath.Join(certDir, "vault-ca.pem")}
	deadline := time.Now().Add(30 * time.Second)
	for {
		if _, err := os.Stat(s.caFile); err == nil {
			conf := api.NewConfig()
			conf.Address = s.addr
			if err := conf.ConfigureTLS(&api.TLSConfig{CACert: s.caFile}); err != nil {
				t.Fatal(err)
			}
			c, err := api.NewClient(conf)
			if err != nil {
				t.Fatal(err)
			}
			c.SetToken(rootToken)
			if h, err := c.Sys().Health(); err == nil && h.Initialized && !h.Sealed {
				s.root = c
				return s
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("bao dev server did not become healthy")
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func (s *devServer) put(t *testing.T, path string, data map[string]any) {
	t.Helper()
	if _, err := s.root.KVv2("secret").Put(context.Background(), path, data); err != nil {
		t.Fatal(err)
	}
}

func (s *devServer) logical(t *testing.T, path string, data map[string]any) *api.Secret {
	t.Helper()
	out, err := s.root.Logical().Write(path, data)
	if err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return out
}

// appRole creates a role allowed to read secret/data/relay/* and writes its
// secret ID to a file.
func (s *devServer) appRole(t *testing.T) (roleID, secretIDFile string) {
	t.Helper()
	if err := s.root.Sys().EnableAuthWithOptions("approle", &api.EnableAuthOptions{Type: "approle"}); err != nil {
		t.Fatal(err)
	}
	if err := s.root.Sys().PutPolicy("relay", `path "secret/data/relay/*" { capabilities = ["read"] }`); err != nil {
		t.Fatal(err)
	}
	s.logical(t, "auth/approle/role/relay", map[string]any{"token_policies": "relay", "token_ttl": "5m"})
	id, err := s.root.Logical().Read("auth/approle/role/relay/role-id")
	if err != nil {
		t.Fatal(err)
	}
	secret := s.logical(t, "auth/approle/role/relay/secret-id", nil)
	secretIDFile = writeFile(t, "secret-id", secret.Data["secret_id"].(string)+"\n")
	return id.Data["role_id"].(string), secretIDFile
}

func writeFile(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// socketProxy serves the dev server's API on a unix socket and injects a
// token into every request, the way a wyrwood container socket does.
func socketProxy(t *testing.T, s *devServer, token string) string {
	t.Helper()
	target, _ := url.Parse(s.addr)
	proxy := httputil.NewSingleHostReverseProxy(target)
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSClientConfig = s.root.CloneConfig().HttpClient.Transport.(*http.Transport).TLSClientConfig.Clone()
	proxy.Transport = tr
	director := proxy.Director
	proxy.Director = func(r *http.Request) {
		director(r)
		r.Header.Del("X-Vault-Token")
		r.Header.Set("X-Vault-Token", token)
	}
	// Socket paths are limited to about 100 bytes, so avoid t.TempDir.
	dir, err := os.MkdirTemp("", "trowel-vault")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "vault.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: proxy, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(func() { _ = srv.Close() })
	return sock
}

func relayConfig(t *testing.T, v *config.Vault, hmacRef, staticRef *config.VaultRef) *config.Config {
	t.Helper()
	cfg := &config.Config{
		Vault: v,
		Webhooks: []config.Webhook{{
			Path:    "/hooks/orders",
			Subject: "orders.created",
			Verify:  &config.Verify{HMAC: &config.HMAC{Header: "X-Signature", SecretVault: hmacRef}},
		}},
		Subjects: []config.SubjectRelay{{
			Name: "notify", Subject: "orders.created",
			Destinations: []config.Destination{{
				Name: "api",
				HTTP: &config.HTTP{URL: "http://example.invalid", Sign: &config.Sign{
					Static: &config.Static{Header: "X-Api-Key", SecretVault: staticRef},
				}},
			}},
		}},
	}
	raw, err := marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := config.Parse(raw)
	if err != nil {
		t.Fatalf("config: %v\n%s", err, raw)
	}
	return parsed
}

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestResolveOverHTTPSWithAppRole(t *testing.T) {
	s := startBao(t)
	s.put(t, "relay/orders", map[string]any{"hmac": "sample-hmac", "key": "sample-key"})
	roleID, secretIDFile := s.appRole(t)

	cfg := relayConfig(t, &config.Vault{
		Address: s.addr,
		CAFile:  s.caFile,
		Auth:    config.VaultAuth{AppRole: &config.AppRole{RoleID: roleID, SecretIDFile: secretIDFile}},
	}, &config.VaultRef{Path: "relay/orders", Field: "hmac"}, &config.VaultRef{Path: "relay/orders", Field: "key"})

	if err := Resolve(context.Background(), cfg, quietLog()); err != nil {
		t.Fatal(err)
	}
	if got := cfg.Webhooks[0].Verify.HMAC.Secret; got != "sample-hmac" {
		t.Errorf("hmac secret = %q", got)
	}
	if got := cfg.Subjects[0].Destinations[0].HTTP.Sign.Static.Secret; got != "sample-key" {
		t.Errorf("static secret = %q", got)
	}

	// The login token is revoked once secrets are resolved.
	accessors, err := s.root.Logical().List("auth/token/accessors")
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range accessors.Data["keys"].([]any) {
		info, err := s.root.Auth().Token().LookupAccessor(a.(string))
		if err == nil && info.Data["path"] == "auth/approle/login" {
			t.Errorf("approle token %s was not revoked", a)
		}
	}
}

func TestResolveOverHTTPSWithTokenFile(t *testing.T) {
	s := startBao(t)
	s.put(t, "relay/orders", map[string]any{"hmac": "sample-hmac", "key": "sample-key"})
	cfg := relayConfig(t, &config.Vault{
		Address: s.addr,
		CAFile:  s.caFile,
		Auth:    config.VaultAuth{TokenFile: writeFile(t, "token", rootToken+"\n")},
	}, &config.VaultRef{Path: "relay/orders", Field: "hmac"}, &config.VaultRef{Path: "relay/orders", Field: "key"})
	if err := Resolve(context.Background(), cfg, quietLog()); err != nil {
		t.Fatal(err)
	}
	if got := cfg.Webhooks[0].Verify.HMAC.Secret; got != "sample-hmac" {
		t.Errorf("hmac secret = %q", got)
	}
	// A token supplied by the operator is not revoked.
	if _, err := s.root.Auth().Token().LookupSelf(); err != nil {
		t.Errorf("token file token was revoked: %v", err)
	}
}

func TestResolveOverUnixSocketWithInjectedToken(t *testing.T) {
	s := startBao(t)
	s.put(t, "relay/orders", map[string]any{"hmac": "sample-hmac", "key": "sample-key"})
	sock := socketProxy(t, s, rootToken)
	cfg := relayConfig(t, &config.Vault{Address: "unix://" + sock},
		&config.VaultRef{Path: "relay/orders", Field: "hmac"}, &config.VaultRef{Path: "relay/orders", Field: "key"})
	if err := Resolve(context.Background(), cfg, quietLog()); err != nil {
		t.Fatal(err)
	}
	if got := cfg.Subjects[0].Destinations[0].HTTP.Sign.Static.Secret; got != "sample-key" {
		t.Errorf("static secret = %q", got)
	}
}

func TestResolveIgnoresAmbientEnvironment(t *testing.T) {
	s := startBao(t)
	s.put(t, "relay/orders", map[string]any{"hmac": "sample-hmac", "key": "sample-key"})
	for _, prefix := range []string{"BAO_", "VAULT_"} {
		t.Setenv(prefix+"TOKEN", rootToken)
		t.Setenv(prefix+"ADDR", "https://127.0.0.1:1")
		t.Setenv(prefix+"NAMESPACE", "sample-namespace")
	}
	cfg := relayConfig(t, &config.Vault{Address: s.addr, CAFile: s.caFile},
		&config.VaultRef{Path: "relay/orders", Field: "hmac"}, &config.VaultRef{Path: "relay/orders", Field: "key"})
	err := Resolve(context.Background(), cfg, quietLog())
	if err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("expected the ambient token to be ignored, got %v", err)
	}
}

func TestResolveRequiresTrustedCertificate(t *testing.T) {
	s := startBao(t)
	t.Setenv("BAO_SKIP_VERIFY", "true")
	t.Setenv("VAULT_SKIP_VERIFY", "true")
	cfg := relayConfig(t, &config.Vault{Address: s.addr, Auth: config.VaultAuth{TokenFile: writeFile(t, "token", rootToken)}},
		&config.VaultRef{Path: "relay/orders", Field: "hmac"}, &config.VaultRef{Path: "relay/orders", Field: "key"})
	err := Resolve(context.Background(), cfg, quietLog())
	if err == nil || !strings.Contains(err.Error(), "certificate") {
		t.Fatalf("expected a certificate error, got %v", err)
	}
}

func TestResolveReportsMissingSecretsAndFields(t *testing.T) {
	s := startBao(t)
	s.put(t, "relay/orders", map[string]any{"hmac": "sample-hmac", "count": 3})
	tokenFile := writeFile(t, "token", rootToken)
	cases := map[string]struct {
		ref  config.VaultRef
		want string
	}{
		"missing secret":   {config.VaultRef{Path: "relay/absent", Field: "hmac"}, "secret not found"},
		"missing field":    {config.VaultRef{Path: "relay/orders", Field: "absent"}, `has no field "absent"`},
		"non-string field": {config.VaultRef{Path: "relay/orders", Field: "count"}, "non-empty string"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ref := tc.ref
			cfg := relayConfig(t, &config.Vault{Address: s.addr, CAFile: s.caFile, Auth: config.VaultAuth{TokenFile: tokenFile}},
				&ref, &config.VaultRef{Path: "relay/orders", Field: "hmac"})
			err := Resolve(context.Background(), cfg, quietLog())
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want %q", err, tc.want)
			}
			if !strings.HasPrefix(err.Error(), "webhooks[0].verify.hmac.secret_vault") {
				t.Errorf("error does not locate the reference: %v", err)
			}
		})
	}
}

func TestResolveWithoutReferencesDoesNotConnect(t *testing.T) {
	cfg := &config.Config{Vault: &config.Vault{Address: "https://127.0.0.1:1", Timeout: time.Second}}
	if err := Resolve(context.Background(), cfg, quietLog()); err != nil {
		t.Fatal(err)
	}
}

func TestDescribeHidesServerText(t *testing.T) {
	err := describe(&api.ResponseError{StatusCode: 403, Errors: []string{"sample server detail"}})
	if strings.Contains(err.Error(), "sample server detail") {
		t.Errorf("server text leaked: %v", err)
	}
	if !strings.Contains(err.Error(), fmt.Sprint(403)) {
		t.Errorf("status missing: %v", err)
	}
}

// marshal round-trips a config through YAML so Parse applies defaults and
// validation exactly as it does for a file.
func marshal(cfg *config.Config) ([]byte, error) { return yaml.Marshal(cfg) }
