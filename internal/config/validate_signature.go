/*
---
relationships:
  references: [types, secrets]
---
*/

package config

import (
	"fmt"
)

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
