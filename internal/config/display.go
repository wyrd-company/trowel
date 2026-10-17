package config

import (
	"fmt"
	"os"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// urlTemplates returns the text of every subjects[i].destinations[j].http.url
// value as written, before ${NAME} expansion, keyed by [i, j].
func urlTemplates(doc *yaml.Node) map[[2]int]string {
	out := map[[2]int]string{}
	root := doc
	if root.Kind == yaml.DocumentNode && len(root.Content) == 1 {
		root = root.Content[0]
	}
	subjects := mapValue(root, "subjects")
	if subjects == nil || subjects.Kind != yaml.SequenceNode {
		return out
	}
	for i, s := range subjects.Content {
		dests := mapValue(s, "destinations")
		if dests == nil || dests.Kind != yaml.SequenceNode {
			continue
		}
		for j, d := range dests.Content {
			if u := mapValue(mapValue(d, "http"), "url"); u != nil && u.Kind == yaml.ScalarNode {
				out[[2]int{i, j}] = u.Value
			}
		}
	}
	return out
}

// mapValue returns the value under key in a mapping node, or nil.
func mapValue(n *yaml.Node, key string) *yaml.Node {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i+1]
		}
	}
	return nil
}

// displayURL names an HTTP destination in logs, dead-letter headers, and
// spans. It is the URL as written, with each ${NAME} that show lists
// expanded and every other reference left as written, and its userinfo
// replaced. A URL without references has no display form, so the caller
// redacts it fully.
func displayURL(template string, show []string) string {
	if !envRef.MatchString(template) {
		return ""
	}
	shown := envRef.ReplaceAllStringFunc(template, func(ref string) string {
		name := envRef.FindStringSubmatch(ref)[1]
		if slices.Contains(show, name) {
			return os.Getenv(name)
		}
		return ref
	})
	return redactUserinfo(shown)
}

// redactUserinfo replaces anything between the scheme and an @ in the
// authority with REDACTED:REDACTED.
func redactUserinfo(u string) string {
	scheme, rest, ok := strings.Cut(u, "://")
	if !ok {
		return u
	}
	end := strings.IndexAny(rest, "/?#")
	if end < 0 {
		end = len(rest)
	}
	at := strings.LastIndex(rest[:end], "@")
	if at < 0 {
		return u
	}
	return scheme + "://REDACTED:REDACTED@" + rest[at+1:]
}

// validateShow checks that every name in show is referenced by template.
func validateShow(where, template string, show []string) error {
	refs := map[string]bool{}
	for _, m := range envRef.FindAllStringSubmatch(template, -1) {
		refs[m[1]] = true
	}
	for _, name := range show {
		if !refs[name] {
			return fmt.Errorf("%s.http.show: %q is not referenced by url", where, name)
		}
	}
	return nil
}
