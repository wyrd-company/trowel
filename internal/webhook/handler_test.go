package webhook

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/wyrd-company/trowel/internal/config"
	"github.com/wyrd-company/trowel/internal/message"
)

type fakePublisher struct {
	msgs []*message.Message
	ids  []string
	err  error
}

func (f *fakePublisher) Publish(_ context.Context, m *message.Message, id string) error {
	if f.err != nil {
		return f.err
	}
	f.msgs = append(f.msgs, m)
	f.ids = append(f.ids, id)
	return nil
}

func sign(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func newHandler(t *testing.T, pub Publisher) *Handler {
	t.Helper()
	cfg := config.Webhook{
		Path:            "/hooks/shop",
		Subject:         "shop.{headers.X-Event-Type}.{payload.region}",
		MaxBodyBytes:    1 << 20,
		MessageIDHeader: "X-Delivery-Id",
		Transform:       config.Transform{Payload: `{"region": payload.region, "n": $count(payload.items)}`},
		Verify: &config.Verify{HMAC: &config.HMAC{
			Algorithm: "sha256", Header: "X-Signature", Prefix: "sha256=", Encoding: "hex", Secret: "shh",
		}},
	}
	h, err := New(cfg, pub, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestHandlerRelays(t *testing.T) {
	pub := &fakePublisher{}
	h := newHandler(t, pub)
	body := []byte(`{"region":"north","items":[1,2,3]}`)
	req := httptest.NewRequest(http.MethodPost, "/hooks/shop", strings.NewReader(string(body)))
	req.Header.Set("X-Signature", sign("shh", body))
	req.Header.Set("X-Event-Type", "order.paid")
	req.Header.Set("X-Delivery-Id", "d-1")
	req.Header.Set("Content-Length", "999")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if len(pub.msgs) != 1 {
		t.Fatalf("published %d", len(pub.msgs))
	}
	m := pub.msgs[0]
	if m.Subject != "shop.order_paid.north" {
		t.Errorf("subject %q", m.Subject)
	}
	if string(m.Payload) != `{"n":3,"region":"north"}` {
		t.Errorf("payload %s", m.Payload)
	}
	if pub.ids[0] != "d-1" {
		t.Errorf("msg id %q", pub.ids[0])
	}
	if m.Headers.Get("Content-Length") != "" || m.Headers.Get("X-Event-Type") != "order.paid" {
		t.Errorf("headers %v", m.Headers)
	}
}

func TestHandlerRejects(t *testing.T) {
	pub := &fakePublisher{}
	h := newHandler(t, pub)
	body := []byte(`{"region":"north","items":[]}`)

	do := func(mutate func(*http.Request)) int {
		req := httptest.NewRequest(http.MethodPost, "/hooks/shop", strings.NewReader(string(body)))
		req.Header.Set("X-Signature", sign("shh", body))
		req.Header.Set("X-Event-Type", "x")
		mutate(req)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	if got := do(func(r *http.Request) { r.Header.Set("X-Signature", "sha256=00") }); got != http.StatusUnauthorized {
		t.Errorf("bad signature: %d", got)
	}
	if got := do(func(r *http.Request) { r.Method = http.MethodGet }); got != http.StatusMethodNotAllowed {
		t.Errorf("GET: %d", got)
	}
	if got := do(func(r *http.Request) { r.Header.Del("X-Event-Type") }); got != http.StatusUnprocessableEntity {
		t.Errorf("missing subject var: %d", got)
	}
	pub.err = errors.New("nats down")
	if got := do(func(*http.Request) {}); got != http.StatusServiceUnavailable {
		t.Errorf("publish error: %d", got)
	}
}

func TestHandlerBodyLimit(t *testing.T) {
	cfg := config.Webhook{Path: "/x", Subject: "x", MaxBodyBytes: 8}
	h, _ := New(cfg, &fakePublisher{}, slog.New(slog.DiscardHandler))
	req := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader("more than eight bytes"))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status %d", rec.Code)
	}
}
