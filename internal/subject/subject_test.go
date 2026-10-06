package subject

import (
	"net/http"
	"testing"

	"github.com/wyrd-company/nats-webhook-relay/internal/message"
)

func TestExpand(t *testing.T) {
	m := &message.Message{
		Headers: http.Header{"X-Event-Type": {"order.created"}},
		Payload: []byte(`{"region":"eu west","items":[{"sku":"A*1"}],"count":3,"flag":true}`),
	}
	cases := map[string]string{
		"events.{headers.x-event-type}":        "events.order_created",
		"r.{payload.region}":                   "r.eu_west",
		"sku.{payload.items[0].sku}":           "sku.A_1",
		"n.{payload.count}.{payload.flag}.end": "n.3.true.end",
		"static.subject":                       "static.subject",
	}
	for raw, want := range cases {
		tpl, err := Parse(raw)
		if err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
		got, err := tpl.Expand(m)
		if err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
		if got != want {
			t.Errorf("%s: got %q want %q", raw, got, want)
		}
	}
}

func TestExpandErrors(t *testing.T) {
	if _, err := Parse("a.{nope.x}"); err == nil {
		t.Fatal("bad prefix accepted")
	}
	tpl, _ := Parse("a.{headers.Missing}")
	if _, err := tpl.Expand(message.New("", nil)); err == nil {
		t.Fatal("missing reference accepted")
	}
	tpl, _ = Parse("a.{payload.obj}")
	if _, err := tpl.Expand(message.New("", []byte(`{"obj":{}}`))); err == nil {
		t.Fatal("object reference accepted")
	}
}
