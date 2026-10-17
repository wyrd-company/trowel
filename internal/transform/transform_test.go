package transform

import (
	"net/http"
	"testing"

	"github.com/wyrd-company/trowel/internal/config"
	"github.com/wyrd-company/trowel/internal/message"
)

func TestApplyPayloadAndHeaders(t *testing.T) {
	tr, err := New(config.Transform{
		Payload: `{"id": payload.order.id, "total": $sum(payload.order.lines.price), "subject": subject}`,
		Headers: "{\"X-Order-Id\": $string(payload.order.id), \"X-Source\": headers.`X-Source`, \"X-Count\": $count(payload.order.lines)}",
	})
	if err != nil {
		t.Fatal(err)
	}
	in := &message.Message{
		Subject: "orders.created",
		Headers: http.Header{"X-Source": {"shop"}, "Content-Type": {"text/plain"}},
		Payload: []byte(`{"order":{"id":42,"lines":[{"price":1.5},{"price":2}]}}`),
	}
	out, err := tr.Apply(in)
	if err != nil {
		t.Fatal(err)
	}
	if string(out.Payload) != `{"id":42,"subject":"orders.created","total":3.5}` {
		t.Errorf("payload %s", out.Payload)
	}
	if out.Headers.Get("X-Order-Id") != "42" || out.Headers.Get("X-Source") != "shop" || out.Headers.Get("X-Count") != "2" {
		t.Errorf("headers %v", out.Headers)
	}
	if out.Headers.Get("Content-Type") != "" {
		t.Errorf("headers transform should replace the header set, got %v", out.Headers)
	}
	if string(in.Payload) != `{"order":{"id":42,"lines":[{"price":1.5},{"price":2}]}}` {
		t.Error("input mutated")
	}
}

func TestPayloadOnlySetsContentType(t *testing.T) {
	tr, _ := New(config.Transform{Payload: `payload.a`})
	out, err := tr.Apply(&message.Message{Headers: http.Header{}, Payload: []byte(`{"a":{"b":1}}`)})
	if err != nil {
		t.Fatal(err)
	}
	if string(out.Payload) != `{"b":1}` || out.Headers.Get("Content-Type") != "application/json" {
		t.Errorf("got %s %v", out.Payload, out.Headers)
	}
}

func TestNonJSONPayloadIsString(t *testing.T) {
	tr, _ := New(config.Transform{Payload: `$uppercase(payload)`})
	out, err := tr.Apply(&message.Message{Headers: http.Header{}, Payload: []byte(`hello`)})
	if err != nil {
		t.Fatal(err)
	}
	if string(out.Payload) != `"HELLO"` {
		t.Errorf("got %s", out.Payload)
	}
}

func TestNoop(t *testing.T) {
	tr, _ := New(config.Transform{})
	m := message.New("s", []byte("x"))
	if out, _ := tr.Apply(m); out != m {
		t.Error("noop transform should return the same message")
	}
}

func TestBadExpression(t *testing.T) {
	if _, err := New(config.Transform{Headers: `{"a": `}); err == nil {
		t.Fatal("expected compile error")
	}
}
