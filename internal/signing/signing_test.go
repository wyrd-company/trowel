package signing

import (
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"testing"
	"time"

	"github.com/wyrd-company/trowel/internal/config"
)

func TestHMACRoundTrip(t *testing.T) {
	cfg := &config.HMAC{Algorithm: "sha256", Header: "X-Sig", Prefix: "sha256=", Encoding: "hex", Secret: "topsecret"}
	body := []byte(`{"a":1}`)
	s, err := NewSigner(&config.Sign{HMAC: cfg})
	if err != nil {
		t.Fatal(err)
	}
	v, err := NewVerifier(&config.Verify{HMAC: cfg})
	if err != nil {
		t.Fatal(err)
	}
	h := http.Header{}
	if err := s.Sign(h, body, time.Now()); err != nil {
		t.Fatal(err)
	}
	mac := hmac.New(sha256.New, []byte("topsecret"))
	mac.Write(body)
	if want := "sha256=" + hex.EncodeToString(mac.Sum(nil)); h.Get("X-Sig") != want {
		t.Fatalf("signature %q, want %q", h.Get("X-Sig"), want)
	}
	if err := v.Verify(h, body, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := v.Verify(h, []byte("tampered"), time.Now()); err == nil {
		t.Fatal("tampered body accepted")
	}
	if err := v.Verify(http.Header{}, body, time.Now()); err == nil {
		t.Fatal("missing header accepted")
	}
}

func TestEd25519RoundTripAndTolerance(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	base := config.Ed25519{SignatureHeader: "X-Sig", TimestampHeader: "X-Ts", Encoding: "base64", Tolerance: time.Minute}
	sc := base
	sc.PrivateKey = hex.EncodeToString(priv.Seed())
	vc := base
	vc.PublicKey = hex.EncodeToString(pub)
	s, err := NewSigner(&config.Sign{Ed25519: &sc})
	if err != nil {
		t.Fatal(err)
	}
	v, err := NewVerifier(&config.Verify{Ed25519: &vc})
	if err != nil {
		t.Fatal(err)
	}
	body := []byte("payload")
	now := time.Unix(1_700_000_000, 0)
	h := http.Header{}
	if err := s.Sign(h, body, now); err != nil {
		t.Fatal(err)
	}
	if h.Get("X-Ts") != "1700000000" {
		t.Fatalf("timestamp %q", h.Get("X-Ts"))
	}
	if err := v.Verify(h, body, now.Add(30*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := v.Verify(h, body, now.Add(2*time.Minute)); err == nil {
		t.Fatal("stale timestamp accepted")
	}
	if err := v.Verify(h, []byte("other"), now); err == nil {
		t.Fatal("tampered body accepted")
	}
	h.Set("X-Ts", "1700000001")
	if err := v.Verify(h, body, now); err == nil {
		t.Fatal("altered timestamp accepted")
	}
}

func TestStaticStripsSecretHeader(t *testing.T) {
	cfg := &config.Static{Header: "X-Token", Secret: "abc"}
	v, err := NewVerifier(&config.Verify{Static: cfg})
	if err != nil {
		t.Fatal(err)
	}
	h := http.Header{"X-Token": {"abc"}}
	if err := v.Verify(h, nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	h.Set("X-Token", "abd")
	if err := v.Verify(h, nil, time.Now()); err == nil {
		t.Fatal("wrong token accepted")
	}
	if got := v.StripHeaders(); len(got) != 1 || got[0] != "X-Token" {
		t.Fatalf("strip headers %v", got)
	}
	s, _ := NewSigner(&config.Sign{Static: cfg})
	out := http.Header{}
	_ = s.Sign(out, nil, time.Now())
	if out.Get("X-Token") != "abc" {
		t.Fatal("signer did not set token")
	}
}

func TestSecretFromFile(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/secret"
	if err := writeFile(path, "filesecret\n"); err != nil {
		t.Fatal(err)
	}
	v, err := NewVerifier(&config.Verify{Static: &config.Static{Header: "X-T", SecretFile: path}})
	if err != nil {
		t.Fatal(err)
	}
	if err := v.Verify(http.Header{"X-T": {"filesecret"}}, nil, time.Now()); err != nil {
		t.Fatal("trimmed file secret rejected")
	}
}
