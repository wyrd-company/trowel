package signing

import (
	"crypto"
	"crypto/hmac"
	"net/http"
	"strings"
	"time"

	"github.com/wyrd-company/nats-relay/internal/config"
)

type hmacStrategy struct {
	hash   crypto.Hash
	header string
	prefix string
	enc    encoding
	secret []byte
}

func newHMACStrategy(cfg *config.HMAC) (*hmacStrategy, error) {
	h, err := hashFor(cfg.Algorithm)
	if err != nil {
		return nil, err
	}
	enc, err := encodingFor(cfg.Encoding)
	if err != nil {
		return nil, err
	}
	secret, err := config.SecretValue(cfg.Secret, cfg.SecretFile)
	if err != nil {
		return nil, err
	}
	return &hmacStrategy{hash: h, header: cfg.Header, prefix: cfg.Prefix, enc: enc, secret: []byte(secret)}, nil
}

func (s *hmacStrategy) mac(body []byte) []byte {
	m := hmac.New(s.hash.New, s.secret)
	m.Write(body)
	return m.Sum(nil)
}

type hmacVerifier struct{ *hmacStrategy }

func newHMACVerifier(cfg *config.HMAC) (Verifier, error) {
	s, err := newHMACStrategy(cfg)
	if err != nil {
		return nil, err
	}
	return hmacVerifier{s}, nil
}

func (v hmacVerifier) Verify(headers http.Header, body []byte, _ time.Time) error {
	got := headers.Get(v.header)
	if got == "" || !strings.HasPrefix(got, v.prefix) {
		return ErrUnauthorized
	}
	sig, err := v.enc.decode(strings.TrimPrefix(got, v.prefix))
	if err != nil || !hmac.Equal(sig, v.mac(body)) {
		return ErrUnauthorized
	}
	return nil
}

func (v hmacVerifier) StripHeaders() []string { return nil }

type hmacSigner struct{ *hmacStrategy }

func newHMACSigner(cfg *config.HMAC) (Signer, error) {
	s, err := newHMACStrategy(cfg)
	if err != nil {
		return nil, err
	}
	return hmacSigner{s}, nil
}

func (s hmacSigner) Sign(headers http.Header, body []byte, _ time.Time) error {
	headers.Set(s.header, s.prefix+s.enc.encode(s.mac(body)))
	return nil
}
