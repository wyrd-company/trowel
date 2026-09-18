// Package signing verifies inbound webhook requests and signs outbound HTTP
// requests using HMAC, Ed25519, or a static shared secret.
package signing

import (
	"crypto"
	_ "crypto/sha1" // register hash
	_ "crypto/sha256"
	_ "crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/wyrd-company/nats-relay/internal/config"
)

// ErrUnauthorized is returned by every Verifier on failure. Callers must not
// distinguish reasons to the remote party.
var ErrUnauthorized = errors.New("request verification failed")

// Verifier checks an inbound request against a strategy.
type Verifier interface {
	// Verify checks headers and body. Now is the reference time for
	// timestamp-tolerant strategies.
	Verify(headers http.Header, body []byte, now time.Time) error
	// StripHeaders lists headers that carry a secret and must not be relayed.
	StripHeaders() []string
}

// Signer adds authentication headers to an outbound request.
type Signer interface {
	Sign(headers http.Header, body []byte, now time.Time) error
}

func hashFor(alg string) (crypto.Hash, error) {
	switch alg {
	case "sha1":
		return crypto.SHA1, nil
	case "sha256":
		return crypto.SHA256, nil
	case "sha512":
		return crypto.SHA512, nil
	}
	return 0, fmt.Errorf("unsupported hash algorithm %q", alg)
}

type encoding struct {
	encode func([]byte) string
	decode func(string) ([]byte, error)
}

func encodingFor(name string) (encoding, error) {
	switch name {
	case "hex":
		return encoding{hex.EncodeToString, hex.DecodeString}, nil
	case "base64":
		return encoding{base64.StdEncoding.EncodeToString, func(s string) ([]byte, error) {
			b, err := base64.StdEncoding.DecodeString(s)
			if err != nil {
				return base64.RawStdEncoding.DecodeString(s)
			}
			return b, nil
		}}, nil
	}
	return encoding{}, fmt.Errorf("unsupported encoding %q", name)
}

// NewVerifier builds the Verifier described by cfg. A nil cfg yields a
// verifier that accepts everything.
func NewVerifier(cfg *config.Verify) (Verifier, error) {
	if cfg == nil {
		return acceptAll{}, nil
	}
	switch {
	case cfg.HMAC != nil:
		return newHMACVerifier(cfg.HMAC)
	case cfg.Ed25519 != nil:
		return newEd25519Verifier(cfg.Ed25519)
	case cfg.Static != nil:
		return newStaticVerifier(cfg.Static)
	}
	return nil, errors.New("verify: no strategy configured")
}

// NewSigner builds the Signer described by cfg. A nil cfg yields a signer
// that adds nothing.
func NewSigner(cfg *config.Sign) (Signer, error) {
	if cfg == nil {
		return noSign{}, nil
	}
	switch {
	case cfg.HMAC != nil:
		return newHMACSigner(cfg.HMAC)
	case cfg.Ed25519 != nil:
		return newEd25519Signer(cfg.Ed25519)
	case cfg.Static != nil:
		return newStaticSigner(cfg.Static)
	}
	return nil, errors.New("sign: no strategy configured")
}

type acceptAll struct{}

func (acceptAll) Verify(http.Header, []byte, time.Time) error { return nil }
func (acceptAll) StripHeaders() []string                      { return nil }

type noSign struct{}

func (noSign) Sign(http.Header, []byte, time.Time) error { return nil }
