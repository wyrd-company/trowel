/*
---
relationships:
  references: [types, display, vault]
---
*/

package config

import (
	"strings"
	"time"
)

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
