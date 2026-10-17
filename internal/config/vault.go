package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
)

// SecretRef points a Vault reference at the inline field its resolved value
// replaces.
type SecretRef struct {
	// Where locates the reference in the config, for error messages.
	Where  string
	Ref    *VaultRef
	Target *string
}

// SecretRefs lists every *_vault reference in the config in document order.
func (c *Config) SecretRefs() []SecretRef {
	var refs []SecretRef
	add := func(where string, ref *VaultRef, target *string) {
		if ref != nil {
			refs = append(refs, SecretRef{Where: where, Ref: ref, Target: target})
		}
	}
	addHMAC := func(where string, h *HMAC) {
		if h != nil {
			add(where+".secret_vault", h.SecretVault, &h.Secret)
		}
	}
	addEd25519 := func(where string, e *Ed25519) {
		if e != nil {
			add(where+".public_key_vault", e.PublicKeyVault, &e.PublicKey)
			add(where+".private_key_vault", e.PrivateKeyVault, &e.PrivateKey)
		}
	}
	addStatic := func(where string, s *Static) {
		if s != nil {
			add(where+".secret_vault", s.SecretVault, &s.Secret)
		}
	}

	add("nats.creds_vault", c.NATS.CredsVault, &c.NATS.Creds)
	for i := range c.Webhooks {
		if v := c.Webhooks[i].Verify; v != nil {
			where := fmt.Sprintf("webhooks[%d].verify", i)
			addHMAC(where+".hmac", v.HMAC)
			addEd25519(where+".ed25519", v.Ed25519)
			addStatic(where+".static", v.Static)
		}
	}
	for i := range c.Subjects {
		for j := range c.Subjects[i].Destinations {
			d := c.Subjects[i].Destinations[j]
			if d.HTTP == nil || d.HTTP.Sign == nil {
				continue
			}
			where := fmt.Sprintf("subjects[%d].destinations[%d].http.sign", i, j)
			addHMAC(where+".hmac", d.HTTP.Sign.HMAC)
			addEd25519(where+".ed25519", d.HTTP.Sign.Ed25519)
			addStatic(where+".static", d.HTTP.Sign.Static)
		}
	}
	return refs
}

var mountRe = regexp.MustCompile(`^[A-Za-z0-9_.-]+(/[A-Za-z0-9_.-]+)*$`)

func (c *Config) validateVault() error {
	refs := c.SecretRefs()
	if c.Vault == nil {
		if len(refs) > 0 {
			return fmt.Errorf("%s: requires a vault section", refs[0].Where)
		}
		return nil
	}
	v := c.Vault
	if err := validateVaultAddress(v.Address); err != nil {
		return fmt.Errorf("vault.address: %w", err)
	}
	if v.CAFile != "" {
		if strings.HasPrefix(v.Address, "unix://") {
			return errors.New("vault.ca_file: not used with a unix:// address")
		}
		if !filepath.IsAbs(v.CAFile) {
			return errors.New("vault.ca_file: must be an absolute path")
		}
	}
	if !mountRe.MatchString(v.KVMount) {
		return fmt.Errorf("vault.kv_mount: must match %s", mountRe)
	}
	if v.Timeout < 0 {
		return errors.New("vault.timeout: must be positive")
	}
	if v.Auth.TokenFile != "" && v.Auth.AppRole != nil {
		return errors.New("vault.auth: set at most one of token_file or approle")
	}
	if a := v.Auth.AppRole; a != nil {
		if !mountRe.MatchString(a.Mount) {
			return fmt.Errorf("vault.auth.approle.mount: must match %s", mountRe)
		}
		if a.RoleID == "" || a.SecretIDFile == "" {
			return errors.New("vault.auth.approle: role_id and secret_id_file are required")
		}
	}
	for _, r := range refs {
		if !mountRe.MatchString(r.Ref.Mount) {
			return fmt.Errorf("%s.mount: must match %s", r.Where, mountRe)
		}
		if p := strings.Trim(r.Ref.Path, "/"); p == "" || p != r.Ref.Path || strings.Contains(p, "..") {
			return fmt.Errorf("%s.path: must be a relative secret path without leading or trailing /", r.Where)
		}
		if r.Ref.Field == "" {
			return fmt.Errorf("%s.field: is required", r.Where)
		}
	}
	return nil
}

// validateVaultAddress accepts https URLs, http URLs on a loopback host, and
// unix:// URLs naming an absolute socket path.
func validateVaultAddress(address string) error {
	if address == "" {
		return errors.New("is required")
	}
	if socket, ok := strings.CutPrefix(address, "unix://"); ok {
		if !filepath.IsAbs(socket) {
			return errors.New("unix:// address must name an absolute socket path, as in unix:///run/vault.sock")
		}
		return nil
	}
	u, err := url.Parse(address)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("must be an https URL, an http URL on a loopback host, or a unix:// socket path")
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		ip := net.ParseIP(u.Hostname())
		if strings.EqualFold(u.Hostname(), "localhost") || (ip != nil && ip.IsLoopback()) {
			return nil
		}
		return errors.New("http is only allowed on a loopback host; use https")
	}
	return errors.New("must be an https URL, an http URL on a loopback host, or a unix:// socket path")
}
