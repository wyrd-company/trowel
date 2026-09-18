package signing

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/wyrd-company/nats-relay/internal/config"
)

// Ed25519 signatures cover the ASCII string "<unix seconds>.<body>".
func ed25519Content(timestamp string, body []byte) []byte {
	out := make([]byte, 0, len(timestamp)+1+len(body))
	out = append(out, timestamp...)
	out = append(out, '.')
	return append(out, body...)
}

// decodeKey accepts hex or base64 (standard or raw) key material.
func decodeKey(s string, want int) ([]byte, error) {
	if b, err := hex.DecodeString(s); err == nil && len(b) == want {
		return b, nil
	}
	if b, err := base64.StdEncoding.DecodeString(s); err == nil && len(b) == want {
		return b, nil
	}
	if b, err := base64.RawStdEncoding.DecodeString(s); err == nil && len(b) == want {
		return b, nil
	}
	return nil, fmt.Errorf("key must be %d bytes in hex or base64", want)
}

type ed25519Verifier struct {
	pub       ed25519.PublicKey
	sigHeader string
	tsHeader  string
	enc       encoding
	tolerance time.Duration
}

func newEd25519Verifier(cfg *config.Ed25519) (Verifier, error) {
	enc, err := encodingFor(cfg.Encoding)
	if err != nil {
		return nil, err
	}
	raw, err := config.SecretValue(cfg.PublicKey, cfg.PublicKeyFile)
	if err != nil {
		return nil, err
	}
	pub, err := decodeKey(raw, ed25519.PublicKeySize)
	if err != nil {
		return nil, fmt.Errorf("ed25519 public key: %w", err)
	}
	return &ed25519Verifier{pub: pub, sigHeader: cfg.SignatureHeader, tsHeader: cfg.TimestampHeader, enc: enc, tolerance: cfg.Tolerance}, nil
}

func (v *ed25519Verifier) Verify(headers http.Header, body []byte, now time.Time) error {
	ts := headers.Get(v.tsHeader)
	sigText := headers.Get(v.sigHeader)
	if ts == "" || sigText == "" {
		return ErrUnauthorized
	}
	unix, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return ErrUnauthorized
	}
	if d := now.Sub(time.Unix(unix, 0)); d > v.tolerance || d < -v.tolerance {
		return ErrUnauthorized
	}
	sig, err := v.enc.decode(sigText)
	if err != nil || !ed25519.Verify(v.pub, ed25519Content(ts, body), sig) {
		return ErrUnauthorized
	}
	return nil
}

func (v *ed25519Verifier) StripHeaders() []string { return nil }

type ed25519Signer struct {
	priv      ed25519.PrivateKey
	sigHeader string
	tsHeader  string
	enc       encoding
}

func newEd25519Signer(cfg *config.Ed25519) (Signer, error) {
	enc, err := encodingFor(cfg.Encoding)
	if err != nil {
		return nil, err
	}
	raw, err := config.SecretValue(cfg.PrivateKey, cfg.PrivateKeyFile)
	if err != nil {
		return nil, err
	}
	var priv ed25519.PrivateKey
	if b, err := decodeKey(raw, ed25519.SeedSize); err == nil {
		priv = ed25519.NewKeyFromSeed(b)
	} else if b, err := decodeKey(raw, ed25519.PrivateKeySize); err == nil {
		priv = ed25519.PrivateKey(b)
	} else {
		return nil, fmt.Errorf("ed25519 private key: must be a 32-byte seed or 64-byte key in hex or base64")
	}
	return &ed25519Signer{priv: priv, sigHeader: cfg.SignatureHeader, tsHeader: cfg.TimestampHeader, enc: enc}, nil
}

func (s *ed25519Signer) Sign(headers http.Header, body []byte, now time.Time) error {
	ts := strconv.FormatInt(now.Unix(), 10)
	sig := ed25519.Sign(s.priv, ed25519Content(ts, body))
	headers.Set(s.tsHeader, ts)
	headers.Set(s.sigHeader, s.enc.encode(sig))
	return nil
}
