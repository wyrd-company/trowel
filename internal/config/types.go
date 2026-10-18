/*
---
relationships:
  references: [config, defaults, validate]
---
*/

package config

import (
	"time"
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
