// Package vault resolves *_vault secret references from an OpenBao or
// HashiCorp Vault KV version 2 mount at startup.
package vault

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/openbao/openbao/api/v2"

	"github.com/wyrd-company/trowel/internal/config"
)

// Resolve reads every *_vault reference in cfg and stores each value in the
// matching inline field, so the rest of the relay treats it like an inline
// secret. Each secret path is read once. It does nothing when the config has
// no references.
func Resolve(ctx context.Context, cfg *config.Config, log *slog.Logger) error {
	refs := cfg.SecretRefs()
	if len(refs) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, cfg.Vault.Timeout)
	defer cancel()

	c, err := Connect(ctx, *cfg.Vault)
	if err != nil {
		return err
	}
	defer c.Close(context.WithoutCancel(ctx), log)

	cache := map[string]map[string]any{}
	for _, r := range refs {
		key := r.Ref.Mount + "\x00" + r.Ref.Path
		data, ok := cache[key]
		if !ok {
			data, err = c.read(ctx, r.Ref.Mount, r.Ref.Path)
			if err != nil {
				return fmt.Errorf("%s: read %s/%s: %w", r.Where, r.Ref.Mount, r.Ref.Path, err)
			}
			cache[key] = data
		}
		raw, ok := data[r.Ref.Field]
		if !ok {
			return fmt.Errorf("%s: secret %s/%s has no field %q", r.Where, r.Ref.Mount, r.Ref.Path, r.Ref.Field)
		}
		value, ok := raw.(string)
		if !ok || value == "" {
			return fmt.Errorf("%s: field %q of secret %s/%s must be a non-empty string", r.Where, r.Ref.Field, r.Ref.Mount, r.Ref.Path)
		}
		*r.Target = value
	}
	log.Info("resolved vault secrets", "references", len(refs), "secrets", len(cache))
	return nil
}

// Client is an authenticated connection to one Vault server.
type Client struct {
	api *api.Client
	// ownsToken is set when the token came from a login this process made,
	// so Close revokes it.
	ownsToken bool
}

// Connect builds a client for cfg and authenticates. It never reads VAULT_*
// or BAO_* environment variables, so no ambient address or token is used.
func Connect(ctx context.Context, cfg config.Vault) (*Client, error) {
	conf := api.NewConfig()
	if conf.Error != nil {
		return nil, fmt.Errorf("vault client: %w", conf.Error)
	}
	conf.Address = cfg.Address
	conf.MaxRetries = 0
	conf.Timeout = cfg.Timeout
	if cfg.CAFile != "" {
		if err := conf.ConfigureTLS(&api.TLSConfig{CACert: cfg.CAFile}); err != nil {
			return nil, fmt.Errorf("vault ca_file: %w", err)
		}
	}
	a, err := api.NewClient(conf)
	if err != nil {
		return nil, fmt.Errorf("vault client: %w", err)
	}
	a.ClearToken()
	a.ClearNamespace()
	if cfg.Namespace != "" {
		a.SetNamespace(cfg.Namespace)
	}
	a.SetCloneToken(false)

	c := &Client{api: a}
	switch {
	case cfg.Auth.TokenFile != "":
		token, err := config.SecretValue("", cfg.Auth.TokenFile)
		if err != nil {
			return nil, fmt.Errorf("vault token_file: %w", err)
		}
		if token == "" {
			return nil, errors.New("vault token_file: is empty")
		}
		a.SetToken(token)
	case cfg.Auth.AppRole != nil:
		if err := c.appRoleLogin(ctx, cfg.Auth.AppRole); err != nil {
			return nil, err
		}
	}
	return c, nil
}

func (c *Client) appRoleLogin(ctx context.Context, role *config.AppRole) error {
	secretID, err := config.SecretValue("", role.SecretIDFile)
	if err != nil {
		return fmt.Errorf("vault approle secret_id_file: %w", err)
	}
	secret, err := c.api.Logical().WriteWithContext(ctx, "auth/"+role.Mount+"/login", map[string]any{
		"role_id":   role.RoleID,
		"secret_id": secretID,
	})
	if err != nil {
		return fmt.Errorf("vault approle login: %w", describe(err))
	}
	if secret == nil || secret.Auth == nil || secret.Auth.ClientToken == "" {
		return errors.New("vault approle login: response carried no token")
	}
	c.api.SetToken(secret.Auth.ClientToken)
	c.ownsToken = true
	return nil
}

func (c *Client) read(ctx context.Context, mount, path string) (map[string]any, error) {
	s, err := c.api.KVv2(mount).Get(ctx, path)
	if err != nil {
		if errors.Is(err, api.ErrSecretNotFound) {
			return nil, errors.New("secret not found")
		}
		return nil, describe(err)
	}
	return s.Data, nil
}

// Close revokes a token this client obtained by logging in. Tokens supplied
// from a file or injected by a socket belong to someone else and are kept.
func (c *Client) Close(ctx context.Context, log *slog.Logger) {
	if !c.ownsToken {
		return
	}
	if err := c.api.Auth().Token().RevokeSelfWithContext(ctx, ""); err != nil {
		log.Warn("revoke vault token", "error", describe(err))
	}
}

// describe reduces an API error to its status, so server-supplied text is
// never logged.
func describe(err error) error {
	var resp *api.ResponseError
	if errors.As(err, &resp) {
		switch resp.StatusCode {
		case http.StatusForbidden, http.StatusUnauthorized:
			return fmt.Errorf("permission denied (HTTP %d)", resp.StatusCode)
		case http.StatusNotFound:
			return fmt.Errorf("not found (HTTP %d)", resp.StatusCode)
		}
		return fmt.Errorf("vault returned HTTP %d", resp.StatusCode)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return errors.New("timed out")
	}
	return err
}
