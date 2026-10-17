// Package transform applies optional JSONata expressions to a message's
// headers and payload. Each expression is evaluated against
// {"headers": {...}, "payload": <json>, "subject": "..."}.
package transform

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	jsonata "github.com/blues/jsonata-go"

	"github.com/wyrd-company/trowel/internal/config"
	"github.com/wyrd-company/trowel/internal/message"
)

// Transformer holds compiled expressions. The zero value is a no-op.
type Transformer struct {
	headers *jsonata.Expr
	payload *jsonata.Expr
}

// New compiles the expressions in cfg. Empty expressions are skipped.
func New(cfg config.Transform) (*Transformer, error) {
	t := &Transformer{}
	var err error
	if cfg.Headers != "" {
		if t.headers, err = jsonata.Compile(cfg.Headers); err != nil {
			return nil, fmt.Errorf("headers transform: %w", err)
		}
	}
	if cfg.Payload != "" {
		if t.payload, err = jsonata.Compile(cfg.Payload); err != nil {
			return nil, fmt.Errorf("payload transform: %w", err)
		}
	}
	return t, nil
}

// Enabled reports whether any expression is configured.
func (t *Transformer) Enabled() bool {
	return t != nil && (t.headers != nil || t.payload != nil)
}

// Input builds the expression input for a message. The payload is decoded as
// JSON when possible; otherwise it is exposed as a string.
func Input(m *message.Message) map[string]any {
	var payload any
	if len(bytes.TrimSpace(m.Payload)) > 0 {
		if err := json.Unmarshal(m.Payload, &payload); err != nil {
			payload = string(m.Payload)
		}
	}
	return map[string]any{
		"headers": m.FlatHeaders(),
		"payload": payload,
		"subject": m.Subject,
	}
}

// Apply evaluates the configured expressions and returns a new message.
// A payload expression replaces the payload with its JSON encoding and sets
// Content-Type to application/json. A headers expression must return an
// object; each value is stringified.
func (t *Transformer) Apply(m *message.Message) (*message.Message, error) {
	if !t.Enabled() {
		return m, nil
	}
	in := Input(m)
	out := &message.Message{Subject: m.Subject, Headers: m.Headers.Clone(), Payload: m.Payload}
	if t.payload != nil {
		res, err := t.payload.Eval(in)
		if err != nil && !errors.Is(err, jsonata.ErrUndefined) {
			return nil, fmt.Errorf("payload transform: %w", err)
		}
		enc, err := json.Marshal(res)
		if err != nil {
			return nil, fmt.Errorf("payload transform: encode result: %w", err)
		}
		out.Payload = enc
		out.Headers.Set("Content-Type", "application/json")
	}
	if t.headers != nil {
		res, err := t.headers.Eval(in)
		if err != nil && !errors.Is(err, jsonata.ErrUndefined) {
			return nil, fmt.Errorf("headers transform: %w", err)
		}
		hdrs, err := toHeaders(res)
		if err != nil {
			return nil, fmt.Errorf("headers transform: %w", err)
		}
		out.Headers = hdrs
	}
	return out, nil
}

func toHeaders(v any) (http.Header, error) {
	h := http.Header{}
	if v == nil {
		return h, nil
	}
	obj, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("result must be an object, got %T", v)
	}
	for k, raw := range obj {
		switch val := raw.(type) {
		case nil:
			continue
		case string:
			h.Set(k, val)
		case []any:
			for _, item := range val {
				h.Add(k, stringify(item))
			}
		default:
			h.Set(k, stringify(val))
		}
	}
	return h, nil
}

func stringify(v any) string {
	switch val := v.(type) {
	case string:
		return val
	case float64:
		return strconv.FormatFloat(val, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(val)
	default:
		b, _ := json.Marshal(val)
		return string(b)
	}
}
