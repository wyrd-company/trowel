package config

import (
	"fmt"
	"net/url"
	"os"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// urlShadow mirrors the path to every HTTP destination URL. Decoding the
// expanded document with it applies the same null, alias, and merge rules
// as decoding Config, and records the node each URL came from.
type urlShadow struct {
	Subjects []struct {
		Destinations []struct {
			HTTP *struct {
				URL urlNode `yaml:"url"`
			} `yaml:"http"`
		} `yaml:"destinations"`
	} `yaml:"subjects"`
}

// urlNode records the node a value was decoded from, without decoding it,
// so it accepts whatever the Config decode accepts.
type urlNode struct{ node *yaml.Node }

func (u *urlNode) UnmarshalYAML(n *yaml.Node) error {
	u.node = n
	return nil
}

// setTemplates sets the URL as written on every HTTP destination whose
// URL had ${NAME} references, using written from expandNode. A shape that
// does not line up with c sets nothing, which leaves those URLs fully
// redacted.
func (c *Config) setTemplates(doc *yaml.Node, written map[*yaml.Node]string) {
	var s urlShadow
	if err := doc.Decode(&s); err != nil || len(s.Subjects) != len(c.Subjects) {
		return
	}
	for i, sub := range s.Subjects {
		dests := c.Subjects[i].Destinations
		if len(sub.Destinations) != len(dests) {
			continue
		}
		for j, d := range sub.Destinations {
			if d.HTTP == nil || dests[j].HTTP == nil || d.HTTP.URL.node == nil {
				continue
			}
			if t, ok := written[d.HTTP.URL.node]; ok {
				dests[j].HTTP.Template = t
			}
		}
	}
}

// displayURL names an HTTP destination in logs, dead-letter headers, and
// spans. It is the URL as written, with each ${NAME} that show lists
// expanded and every other reference left as written. It is empty, so the
// caller redacts the URL fully, when the URL has no references, when
// expanded does not parse, or when expanded has userinfo that the written
// form does not locate unambiguously.
func displayURL(template, expanded string, show []string) string {
	if !envRef.MatchString(template) {
		return ""
	}
	u, err := url.Parse(expanded)
	if err != nil {
		return ""
	}
	if u.User != nil {
		var ok bool
		if template, ok = redactUserinfo(template); !ok {
			return ""
		}
	}
	return envRef.ReplaceAllStringFunc(template, func(ref string) string {
		name := envRef.FindStringSubmatch(ref)[1]
		if slices.Contains(show, name) {
			return os.Getenv(name)
		}
		return ref
	})
}

// redactUserinfo replaces the userinfo of a URL as written with
// REDACTED:REDACTED. It reports false unless the written form fixes where
// the userinfo is: the scheme and :// are literal, the authority holds a
// literal @, and no reference in the authority expands to a delimiter that
// could move its bounds.
func redactUserinfo(template string) (string, bool) {
	i := strings.Index(template, "://")
	if i < 0 || envRef.MatchString(template[:i]) {
		return "", false
	}
	rest := template[i+3:]
	end := strings.IndexAny(rest, "/?#")
	if end < 0 {
		end = len(rest)
	}
	authority := rest[:end]
	for _, m := range envRef.FindAllStringSubmatch(authority, -1) {
		if strings.ContainsAny(os.Getenv(m[1]), "@/?#") {
			return "", false
		}
	}
	at := strings.LastIndex(authority, "@")
	if at < 0 {
		return "", false
	}
	return template[:i+3] + "REDACTED:REDACTED" + rest[at:], true
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
