/*
---
relationships:
  references: [types, validate_signature, vault, display]
---
*/

package config

import (
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
)

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
