// Package subject expands {headers.Name} and {payload.a.b[0]} references in
// a NATS subject template and sanitises the result into valid tokens.
package subject

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/wyrd-company/nats-relay/internal/message"
	"github.com/wyrd-company/nats-relay/internal/transform"
)

var ref = regexp.MustCompile(`\{([^{}]+)\}`)

// Template is a parsed subject template.
type Template struct {
	raw   string
	paths []string
}

// Parse validates the template syntax. Each reference must start with
// `headers.` or `payload.`.
func Parse(raw string) (*Template, error) {
	t := &Template{raw: raw}
	for _, m := range ref.FindAllStringSubmatch(raw, -1) {
		p := strings.TrimSpace(m[1])
		if !strings.HasPrefix(p, "headers.") && !strings.HasPrefix(p, "payload.") {
			return nil, fmt.Errorf("subject %q: reference {%s} must start with headers. or payload.", raw, p)
		}
		t.paths = append(t.paths, p)
	}
	return t, nil
}

// Static reports whether the template has no references.
func (t *Template) Static() bool { return len(t.paths) == 0 }

// Expand resolves every reference against the message. A reference that
// resolves to nothing is an error so a message is never published to a
// truncated subject.
func (t *Template) Expand(m *message.Message) (string, error) {
	if t.Static() {
		return t.raw, nil
	}
	in := transform.Input(m)
	var firstErr error
	out := ref.ReplaceAllStringFunc(t.raw, func(match string) string {
		path := strings.TrimSpace(match[1 : len(match)-1])
		val, ok := lookup(in, path)
		if !ok || val == "" {
			if firstErr == nil {
				firstErr = fmt.Errorf("subject %q: {%s} resolved to nothing", t.raw, path)
			}
			return ""
		}
		return Sanitize(val)
	})
	if firstErr != nil {
		return "", firstErr
	}
	return out, nil
}

// lookup walks a dotted path with optional [n] indexes. Header names match
// case-insensitively.
func lookup(root map[string]any, path string) (string, bool) {
	segs := strings.Split(path, ".")
	var cur any = root
	for i, seg := range segs {
		key, idx, hasIdx := splitIndex(seg)
		obj, ok := cur.(map[string]any)
		if !ok {
			return "", false
		}
		next, found := obj[key]
		if !found && i == 1 && segs[0] == "headers" {
			next, found = lookupFold(obj, key)
		}
		if !found {
			return "", false
		}
		cur = next
		if hasIdx {
			arr, ok := cur.([]any)
			if !ok || idx < 0 || idx >= len(arr) {
				return "", false
			}
			cur = arr[idx]
		}
	}
	switch v := cur.(type) {
	case string:
		return v, true
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64), true
	case bool:
		return strconv.FormatBool(v), true
	case nil:
		return "", false
	}
	return "", false
}

func lookupFold(obj map[string]any, key string) (any, bool) {
	for k, v := range obj {
		if strings.EqualFold(k, key) {
			return v, true
		}
	}
	return nil, false
}

func splitIndex(seg string) (string, int, bool) {
	open := strings.IndexByte(seg, '[')
	if open < 0 || !strings.HasSuffix(seg, "]") {
		return seg, 0, false
	}
	n, err := strconv.Atoi(seg[open+1 : len(seg)-1])
	if err != nil {
		return seg, 0, false
	}
	return seg[:open], n, true
}

// Sanitize replaces characters that are not valid inside a single NATS
// subject token with '_'.
func Sanitize(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == '.', r == '*', r == '>', r == ' ', r == '\t', r == '\r', r == '\n', r < 0x20, r == 0x7f:
			b.WriteByte('_')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
