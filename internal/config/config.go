// Package config loads and validates the relay YAML configuration.
package config

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the root of the YAML configuration file.
type Config struct {
	Server   Server         `yaml:"server"`
	NATS     NATS           `yaml:"nats"`
	Webhooks []Webhook      `yaml:"webhooks"`
	Subjects []SubjectRelay `yaml:"subjects"`
	Vault    *Vault         `yaml:"vault"`
}

// Server configures the inbound HTTP listener.
type Server struct {
	Listen            string        `yaml:"listen"`
	ReadHeaderTimeout time.Duration `yaml:"read_header_timeout"`
}

// NATS configures the connection to the NATS server.
type NATS struct {
	URL       string `yaml:"url"`
	Name      string `yaml:"name"`
	CredsFile string `yaml:"creds_file"`
	// CredsVault reads the contents of a NATS credentials file from Vault.
	CredsVault *VaultRef `yaml:"creds_vault"`
	// Creds holds credentials resolved from CredsVault. It is never read
	// from YAML.
	Creds string `yaml:"-"`
}

// Vault configures the OpenBao or Vault server that *_vault secret
// references are read from. Address is an https URL, an http URL on a
// loopback host, or unix:///absolute/path for a local socket.
type Vault struct {
	Address   string        `yaml:"address"`
	Namespace string        `yaml:"namespace"`
	CAFile    string        `yaml:"ca_file"`
	KVMount   string        `yaml:"kv_mount"`
	Timeout   time.Duration `yaml:"timeout"`
	Auth      VaultAuth     `yaml:"auth"`
}

// VaultAuth selects at most one way to obtain a token. With neither set,
// requests carry no token, which suits a socket or agent that injects one.
type VaultAuth struct {
	TokenFile string   `yaml:"token_file"`
	AppRole   *AppRole `yaml:"approle"`
}

// AppRole logs in with a role ID and a secret ID read from a file.
type AppRole struct {
	Mount        string `yaml:"mount"`
	RoleID       string `yaml:"role_id"`
	SecretIDFile string `yaml:"secret_id_file"`
}

// VaultRef names one field of a KV version 2 secret.
type VaultRef struct {
	Mount string `yaml:"mount"`
	Path  string `yaml:"path"`
	Field string `yaml:"field"`
}

// Transform holds optional JSONata expressions. Each expression receives an
// object with `headers`, `payload`, and `subject` fields.
type Transform struct {
	Headers string `yaml:"headers"`
	Payload string `yaml:"payload"`
}

// Webhook relays HTTP POST requests to a NATS subject.
type Webhook struct {
	Path            string    `yaml:"path"`
	Subject         string    `yaml:"subject"`
	MaxBodyBytes    int64     `yaml:"max_body_bytes"`
	MessageIDHeader string    `yaml:"message_id_header"`
	Transform       Transform `yaml:"transform"`
	Verify          *Verify   `yaml:"verify"`
}

// Verify selects exactly one inbound verification strategy.
type Verify struct {
	HMAC    *HMAC    `yaml:"hmac"`
	Ed25519 *Ed25519 `yaml:"ed25519"`
	Static  *Static  `yaml:"static"`
}

// Sign selects exactly one outbound signing strategy.
type Sign struct {
	HMAC    *HMAC    `yaml:"hmac"`
	Ed25519 *Ed25519 `yaml:"ed25519"`
	Static  *Static  `yaml:"static"`
}

// HMAC describes a shared-secret signature over the body.
type HMAC struct {
	Algorithm   string    `yaml:"algorithm"`
	Header      string    `yaml:"header"`
	Prefix      string    `yaml:"prefix"`
	Encoding    string    `yaml:"encoding"`
	Secret      string    `yaml:"secret"`
	SecretFile  string    `yaml:"secret_file"`
	SecretVault *VaultRef `yaml:"secret_vault"`
}

// Ed25519 describes an asymmetric signature over `timestamp.body`.
type Ed25519 struct {
	SignatureHeader string        `yaml:"signature_header"`
	TimestampHeader string        `yaml:"timestamp_header"`
	Encoding        string        `yaml:"encoding"`
	Tolerance       time.Duration `yaml:"tolerance"`
	PublicKey       string        `yaml:"public_key"`
	PublicKeyFile   string        `yaml:"public_key_file"`
	PublicKeyVault  *VaultRef     `yaml:"public_key_vault"`
	PrivateKey      string        `yaml:"private_key"`
	PrivateKeyFile  string        `yaml:"private_key_file"`
	PrivateKeyVault *VaultRef     `yaml:"private_key_vault"`
}

// Static describes a shared secret sent verbatim in a header.
type Static struct {
	Header      string    `yaml:"header"`
	Secret      string    `yaml:"secret"`
	SecretFile  string    `yaml:"secret_file"`
	SecretVault *VaultRef `yaml:"secret_vault"`
}

// SubjectRelay consumes a NATS subject and delivers each message to one or
// more destinations. Each destination gets its own durable consumer.
type SubjectRelay struct {
	Name         string        `yaml:"name"`
	Stream       string        `yaml:"stream"`
	Subject      string        `yaml:"subject"`
	Transform    Transform     `yaml:"transform"`
	Consumer     Consumer      `yaml:"consumer"`
	Destinations []Destination `yaml:"destinations"`
}

// Consumer holds JetStream consumer delivery and acknowledgement settings.
type Consumer struct {
	DeliverPolicy string          `yaml:"deliver_policy"`
	AckWait       time.Duration   `yaml:"ack_wait"`
	MaxDeliver    int             `yaml:"max_deliver"`
	Backoff       []time.Duration `yaml:"backoff"`
	MaxAckPending int             `yaml:"max_ack_pending"`
	DLQSubject    string          `yaml:"dlq_subject"`
}

// Destination is exactly one of an HTTP endpoint or a command.
type Destination struct {
	Name    string   `yaml:"name"`
	HTTP    *HTTP    `yaml:"http"`
	Command *Command `yaml:"command"`
}

// HTTP delivers the message body as an HTTP request.
type HTTP struct {
	URL     string            `yaml:"url"`
	Method  string            `yaml:"method"`
	Headers map[string]string `yaml:"headers"`
	Timeout time.Duration     `yaml:"timeout"`
	Sign    *Sign             `yaml:"sign"`
	// Show lists ${NAME} references in URL whose values may appear where
	// the destination is named. Others stay written as ${NAME}.
	Show []string `yaml:"show"`
	// Template is URL as written, before ${NAME} expansion. It is never
	// read from YAML.
	Template string `yaml:"-"`
	// DisplayURL names the destination in logs, dead-letter headers, and
	// spans. It is empty when URL has no references, and then the URL is
	// redacted in full.
	DisplayURL string `yaml:"-"`
}

// Command delivers the message body on stdin of a child process.
type Command struct {
	Argv      []string          `yaml:"argv"`
	Dir       string            `yaml:"dir"`
	Env       map[string]string `yaml:"env"`
	EnvPrefix string            `yaml:"env_prefix"`
	Timeout   time.Duration     `yaml:"timeout"`
}

// Defaults applied when a field is unset.
const (
	DefaultListen            = ":8080"
	DefaultReadHeaderTimeout = 10 * time.Second
	DefaultMaxBodyBytes      = 10 << 20
	DefaultNATSURL           = "nats://127.0.0.1:4222"
	DefaultAckWait           = 30 * time.Second
	DefaultMaxDeliver        = 5
	DefaultMaxAckPending     = 64
	DefaultHTTPTimeout       = 30 * time.Second
	DefaultCommandTimeout    = 60 * time.Second
	DefaultHTTPMethod        = "POST"
	DefaultEncoding          = "hex"
	DefaultTolerance         = 5 * time.Minute
	DefaultEnvPrefix         = "RELAY_HEADER_"
	DefaultDeliverPolicy     = "all"
	DefaultVaultKVMount      = "secret"
	DefaultVaultTimeout      = 10 * time.Second
	DefaultAppRoleMount      = "approle"
)

// Parse decodes YAML, expands environment references in values, applies
// defaults, and validates.
func Parse(raw []byte) (*Config, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	var cfg Config
	if doc.Kind != 0 {
		written, err := expandNode(&doc)
		if err != nil {
			return nil, err
		}
		if err := decodeStrict(&doc, &cfg); err != nil {
			return nil, fmt.Errorf("parse config: %w", err)
		}
		cfg.setTemplates(&doc, written)
	}
	cfg.applyDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// decodeStrict decodes a node into v, rejecting unknown fields.
func decodeStrict(n *yaml.Node, v any) error {
	out, err := yaml.Marshal(n)
	if err != nil {
		return err
	}
	dec := yaml.NewDecoder(strings.NewReader(string(out)))
	dec.KnownFields(true)
	if err := dec.Decode(v); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

func (c *Config) applyDefaults() {
	if c.Server.Listen == "" {
		c.Server.Listen = DefaultListen
	}
	if c.Server.ReadHeaderTimeout == 0 {
		c.Server.ReadHeaderTimeout = DefaultReadHeaderTimeout
	}
	if c.NATS.URL == "" {
		c.NATS.URL = DefaultNATSURL
	}
	if c.NATS.Name == "" {
		c.NATS.Name = "trowel"
	}
	if v := c.Vault; v != nil {
		v.Address = strings.TrimRight(v.Address, "/")
		v.Namespace = strings.Trim(v.Namespace, "/")
		if v.KVMount == "" {
			v.KVMount = DefaultVaultKVMount
		}
		if v.Timeout == 0 {
			v.Timeout = DefaultVaultTimeout
		}
		if v.Auth.AppRole != nil && v.Auth.AppRole.Mount == "" {
			v.Auth.AppRole.Mount = DefaultAppRoleMount
		}
		for _, r := range c.SecretRefs() {
			if r.Ref.Mount == "" {
				r.Ref.Mount = v.KVMount
			}
		}
	}
	for i := range c.Webhooks {
		w := &c.Webhooks[i]
		if w.MaxBodyBytes == 0 {
			w.MaxBodyBytes = DefaultMaxBodyBytes
		}
		if w.Verify != nil {
			applyHMACDefaults(w.Verify.HMAC)
			applyEd25519Defaults(w.Verify.Ed25519)
		}
	}
	for i := range c.Subjects {
		s := &c.Subjects[i]
		if s.Consumer.DeliverPolicy == "" {
			s.Consumer.DeliverPolicy = DefaultDeliverPolicy
		}
		if s.Consumer.AckWait == 0 {
			s.Consumer.AckWait = DefaultAckWait
		}
		if s.Consumer.MaxDeliver == 0 {
			s.Consumer.MaxDeliver = DefaultMaxDeliver
		}
		if s.Consumer.MaxAckPending == 0 {
			s.Consumer.MaxAckPending = DefaultMaxAckPending
		}
		for j := range s.Destinations {
			d := &s.Destinations[j]
			if d.HTTP != nil {
				if d.HTTP.Method == "" {
					d.HTTP.Method = DefaultHTTPMethod
				}
				if d.HTTP.Timeout == 0 {
					d.HTTP.Timeout = DefaultHTTPTimeout
				}
				d.HTTP.DisplayURL = displayURL(d.HTTP.Template, d.HTTP.URL, d.HTTP.Show)
				if d.HTTP.Sign != nil {
					applyHMACDefaults(d.HTTP.Sign.HMAC)
					applyEd25519Defaults(d.HTTP.Sign.Ed25519)
				}
			}
			if d.Command != nil {
				if d.Command.Timeout == 0 {
					d.Command.Timeout = DefaultCommandTimeout
				}
				if d.Command.EnvPrefix == "" {
					d.Command.EnvPrefix = DefaultEnvPrefix
				}
			}
		}
	}
}

func applyHMACDefaults(h *HMAC) {
	if h == nil {
		return
	}
	if h.Algorithm == "" {
		h.Algorithm = "sha256"
	}
	if h.Encoding == "" {
		h.Encoding = DefaultEncoding
	}
}

func applyEd25519Defaults(e *Ed25519) {
	if e == nil {
		return
	}
	if e.Encoding == "" {
		e.Encoding = DefaultEncoding
	}
	if e.Tolerance == 0 {
		e.Tolerance = DefaultTolerance
	}
}

var nameRe = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// Validate reports the first structural problem found.
func (c *Config) Validate() error {
	if len(c.Webhooks) == 0 && len(c.Subjects) == 0 {
		return errors.New("config defines no webhooks and no subjects")
	}
	if err := c.validateVault(); err != nil {
		return err
	}
	if c.NATS.CredsFile != "" && c.NATS.CredsVault != nil {
		return errors.New("nats: set at most one of creds_file or creds_vault")
	}
	routes := http.NewServeMux()
	routes.HandleFunc("GET /healthz", http.NotFound)
	routes.HandleFunc("GET /readyz", http.NotFound)
	paths := map[string]bool{}
	for i, w := range c.Webhooks {
		where := fmt.Sprintf("webhooks[%d]", i)
		if !strings.HasPrefix(w.Path, "/") {
			return fmt.Errorf("%s: path must start with /", where)
		}
		if paths[w.Path] {
			return fmt.Errorf("%s: duplicate path %q", where, w.Path)
		}
		paths[w.Path] = true
		if err := registerRoute(routes, w.Path); err != nil {
			return fmt.Errorf("%s: path: %w", where, err)
		}
		if w.Subject == "" {
			return fmt.Errorf("%s: subject is required", where)
		}
		if w.MaxBodyBytes < 0 {
			return fmt.Errorf("%s: max_body_bytes must be positive", where)
		}
		if w.Verify != nil {
			if err := validateOneOf(where+".verify", w.Verify.HMAC != nil, w.Verify.Ed25519 != nil, w.Verify.Static != nil); err != nil {
				return err
			}
			if err := validateHMAC(where+".verify.hmac", w.Verify.HMAC); err != nil {
				return err
			}
			if err := validateEd25519(where+".verify.ed25519", w.Verify.Ed25519, false); err != nil {
				return err
			}
			if err := validateStatic(where+".verify.static", w.Verify.Static); err != nil {
				return err
			}
		}
	}
	names := map[string]bool{}
	for i, s := range c.Subjects {
		where := fmt.Sprintf("subjects[%d]", i)
		if !nameRe.MatchString(s.Name) {
			return fmt.Errorf("%s: name must match %s", where, nameRe)
		}
		if names[s.Name] {
			return fmt.Errorf("%s: duplicate name %q", where, s.Name)
		}
		names[s.Name] = true
		if s.Subject == "" {
			return fmt.Errorf("%s: subject is required", where)
		}
		switch s.Consumer.DeliverPolicy {
		case "all", "new", "last":
		default:
			return fmt.Errorf("%s.consumer.deliver_policy: must be all, new, or last", where)
		}
		if s.Consumer.MaxDeliver < -1 || s.Consumer.MaxDeliver == 0 {
			return fmt.Errorf("%s.consumer.max_deliver: must be -1 (unlimited) or positive", where)
		}
		if s.Consumer.DLQSubject != "" && s.Consumer.MaxDeliver == -1 {
			return fmt.Errorf("%s.consumer: dlq_subject requires a positive max_deliver", where)
		}
		if n := len(s.Consumer.Backoff); n > 0 && s.Consumer.MaxDeliver != -1 && s.Consumer.MaxDeliver <= n {
			return fmt.Errorf("%s.consumer: max_deliver must exceed the number of backoff entries", where)
		}
		if len(s.Destinations) == 0 {
			return fmt.Errorf("%s: at least one destination is required", where)
		}
		dests := map[string]bool{}
		for j, d := range s.Destinations {
			dw := fmt.Sprintf("%s.destinations[%d]", where, j)
			if !nameRe.MatchString(d.Name) {
				return fmt.Errorf("%s: name must match %s", dw, nameRe)
			}
			if dests[d.Name] {
				return fmt.Errorf("%s: duplicate name %q", dw, d.Name)
			}
			dests[d.Name] = true
			if err := validateOneOf(dw, d.HTTP != nil, d.Command != nil); err != nil {
				return err
			}
			if d.HTTP != nil {
				if d.HTTP.URL == "" {
					return fmt.Errorf("%s.http: url is required", dw)
				}
				if err := validateShow(dw, d.HTTP.Template, d.HTTP.Show); err != nil {
					return err
				}
				if d.HTTP.Sign != nil {
					if err := validateOneOf(dw+".http.sign", d.HTTP.Sign.HMAC != nil, d.HTTP.Sign.Ed25519 != nil, d.HTTP.Sign.Static != nil); err != nil {
						return err
					}
					if err := validateHMAC(dw+".http.sign.hmac", d.HTTP.Sign.HMAC); err != nil {
						return err
					}
					if err := validateEd25519(dw+".http.sign.ed25519", d.HTTP.Sign.Ed25519, true); err != nil {
						return err
					}
					if err := validateStatic(dw+".http.sign.static", d.HTTP.Sign.Static); err != nil {
						return err
					}
				}
			}
			if d.Command != nil && len(d.Command.Argv) == 0 {
				return fmt.Errorf("%s.command: argv is required", dw)
			}
		}
	}
	return nil
}

// registerRoute adds path to routes, reporting an invalid or conflicting
// pattern as an error rather than the panic http.ServeMux raises.
func registerRoute(routes *http.ServeMux, path string) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("%v", r)
		}
	}()
	routes.HandleFunc(path, http.NotFound)
	return nil
}

func validateOneOf(where string, set ...bool) error {
	n := 0
	for _, s := range set {
		if s {
			n++
		}
	}
	if n != 1 {
		return fmt.Errorf("%s: exactly one strategy must be set", where)
	}
	return nil
}

func validateHMAC(where string, h *HMAC) error {
	if h == nil {
		return nil
	}
	switch h.Algorithm {
	case "sha1", "sha256", "sha512":
	default:
		return fmt.Errorf("%s.algorithm: must be sha1, sha256, or sha512", where)
	}
	if h.Header == "" {
		return fmt.Errorf("%s.header: is required", where)
	}
	if err := validateEncoding(where, h.Encoding); err != nil {
		return err
	}
	return validateSecret(where, "secret", h.Secret, h.SecretFile, h.SecretVault)
}

func validateEd25519(where string, e *Ed25519, private bool) error {
	if e == nil {
		return nil
	}
	if e.SignatureHeader == "" || e.TimestampHeader == "" {
		return fmt.Errorf("%s: signature_header and timestamp_header are required", where)
	}
	if err := validateEncoding(where, e.Encoding); err != nil {
		return err
	}
	if private {
		if e.PublicKeyVault != nil {
			return fmt.Errorf("%s: public_key_vault is not used when signing", where)
		}
		return validateSecret(where, "private_key", e.PrivateKey, e.PrivateKeyFile, e.PrivateKeyVault)
	}
	if e.PrivateKeyVault != nil {
		return fmt.Errorf("%s: private_key_vault is not used when verifying", where)
	}
	return validateSecret(where, "public_key", e.PublicKey, e.PublicKeyFile, e.PublicKeyVault)
}

func validateStatic(where string, s *Static) error {
	if s == nil {
		return nil
	}
	if s.Header == "" {
		return fmt.Errorf("%s.header: is required", where)
	}
	return validateSecret(where, "secret", s.Secret, s.SecretFile, s.SecretVault)
}

func validateEncoding(where, enc string) error {
	switch enc {
	case "hex", "base64":
		return nil
	}
	return fmt.Errorf("%s.encoding: must be hex or base64", where)
}

func validateSecret(where, field, inline, file string, vault *VaultRef) error {
	n := 0
	for _, set := range []bool{inline != "", file != "", vault != nil} {
		if set {
			n++
		}
	}
	if n != 1 {
		return fmt.Errorf("%s: exactly one of %s, %s_file, or %s_vault is required", where, field, field, field)
	}
	return nil
}

// SecretValue returns the inline value or the trimmed contents of the file.
func SecretValue(inline, file string) (string, error) {
	if inline != "" {
		return inline, nil
	}
	b, err := os.ReadFile(file)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}
