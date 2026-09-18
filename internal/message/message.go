// Package message is the unit that moves through the relay in both
// directions: a set of headers, a payload, and a subject.
package message

import (
	"net/http"
	"net/textproto"
	"strings"
)

// Message is a relayed unit. Headers are canonicalised on write.
type Message struct {
	Subject string
	Headers http.Header
	Payload []byte
}

// New returns a Message with an empty header set.
func New(subject string, payload []byte) *Message {
	return &Message{Subject: subject, Headers: http.Header{}, Payload: payload}
}

// FlatHeaders returns headers as a single-valued map for expression input.
// Multiple values are joined with ", " as HTTP permits.
func (m *Message) FlatHeaders() map[string]any {
	out := make(map[string]any, len(m.Headers))
	for k, v := range m.Headers {
		out[k] = strings.Join(v, ", ")
	}
	return out
}

// Header returns the first value of a header, matched case-insensitively.
func (m *Message) Header(name string) (string, bool) {
	v, ok := m.Headers[textproto.CanonicalMIMEHeaderKey(name)]
	if !ok || len(v) == 0 {
		return "", false
	}
	return v[0], true
}

// hopByHop lists headers that describe the transport, not the message.
var hopByHop = map[string]bool{
	"Connection":          true,
	"Content-Length":      true,
	"Host":                true,
	"Keep-Alive":          true,
	"Proxy-Authenticate":  true,
	"Proxy-Authorization": true,
	"Proxy-Connection":    true,
	"Te":                  true,
	"Trailer":             true,
	"Transfer-Encoding":   true,
	"Upgrade":             true,
	"Accept-Encoding":     true,
}

// CopyHeaders copies src into a new header set, dropping transport headers
// and any names listed in drop (matched case-insensitively).
func CopyHeaders(src http.Header, drop ...string) http.Header {
	dropSet := make(map[string]bool, len(drop))
	for _, d := range drop {
		dropSet[textproto.CanonicalMIMEHeaderKey(d)] = true
	}
	out := http.Header{}
	for k, vals := range src {
		ck := textproto.CanonicalMIMEHeaderKey(k)
		if hopByHop[ck] || dropSet[ck] || strings.HasPrefix(ck, "Nats-") {
			continue
		}
		for _, v := range vals {
			out.Add(ck, v)
		}
	}
	return out
}
