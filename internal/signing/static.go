package signing

import (
	"crypto/subtle"
	"net/http"
	"time"

	"github.com/wyrd-company/trowel/internal/config"
)

type staticStrategy struct {
	header string
	secret string
}

func newStaticStrategy(cfg *config.Static) (*staticStrategy, error) {
	secret, err := config.SecretValue(cfg.Secret, cfg.SecretFile)
	if err != nil {
		return nil, err
	}
	return &staticStrategy{header: cfg.Header, secret: secret}, nil
}

type staticVerifier struct{ *staticStrategy }

func newStaticVerifier(cfg *config.Static) (Verifier, error) {
	s, err := newStaticStrategy(cfg)
	if err != nil {
		return nil, err
	}
	return staticVerifier{s}, nil
}

func (v staticVerifier) Verify(headers http.Header, _ []byte, _ time.Time) error {
	if subtle.ConstantTimeCompare([]byte(headers.Get(v.header)), []byte(v.secret)) != 1 {
		return ErrUnauthorized
	}
	return nil
}

// StripHeaders removes the shared secret so it is never relayed onward.
func (v staticVerifier) StripHeaders() []string { return []string{v.header} }

type staticSigner struct{ *staticStrategy }

func newStaticSigner(cfg *config.Static) (Signer, error) {
	s, err := newStaticStrategy(cfg)
	if err != nil {
		return nil, err
	}
	return staticSigner{s}, nil
}

func (s staticSigner) Sign(headers http.Header, _ []byte, _ time.Time) error {
	headers.Set(s.header, s.secret)
	return nil
}
