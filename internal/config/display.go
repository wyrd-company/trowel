package config

import (
	"fmt"
	"net/url"
	"os"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// urlShadow mirrors the path to every HTTP destination URL, so decoding
// the unexpanded document with it applies the same null, alias, and merge
// rules as decoding Config and yields the URLs as written.
type urlShadow struct {
	Subjects []struct {
		Destinations []struct {
			HTTP *struct {
				URL string `yaml:"url"`
			} `yaml:"http"`
		} `yaml:"destinations"`
	} `yaml:"subjects"`
}

// urlTemplates decodes the URLs as written from doc, before ${NAME}
// expansion.
func urlTemplates(doc *yaml.Node) (urlShadow, error) {
	var s urlShadow
	err := doc.Decode(&s)
	return s, err
}

// setTemplates copies the URLs as written onto cfg's HTTP destinations.
// A shape that does not line up with cfg sets nothing, which leaves those
// URLs fully redacted.
func (c *Config) setTemplates(s urlShadow) {
	if len(s.Subjects) != len(c.Subjects) {
		return
	}
	for i, sub := range s.Subjects {
		dests := c.Subjects[i].Destinations
		if len(sub.Destinations) != len(dests) {
			continue
		}
		for j, d := range sub.Destinations {
			if d.HTTP != nil && dests[j].HTTP != nil {
				dests[j].HTTP.Template = d.HTTP.URL
			}
		}
	}
}

// displayURL names an HTTP destination in logs, dead-letter headers, and
// spans. It is the URL as written, with each ${NAME} that show lists
// expanded and every other reference left as written, and its userinfo
// replaced. It is empty, so the caller redacts the URL fully, when the URL
// has no references, when expanded does not parse, or when the userinfo of
// expanded would still show.
func displayURL(template, expanded string, show []string) string {
	if !envRef.MatchString(template) {
		return ""
	}
	u, err := url.Parse(expanded)
	if err != nil {
		return ""
	}
	shown := envRef.ReplaceAllStringFunc(template, func(ref string) string {
		name := envRef.FindStringSubmatch(ref)[1]
		if slices.Contains(show, name) {
			return os.Getenv(name)
		}
		return ref
	})
	shown = redactUserinfo(shown)
	if u.User != nil {
		pass, _ := u.User.Password()
		for _, part := range []string{u.User.Username(), pass} {
			if part != "" && strings.Contains(shown, part) {
				return ""
			}
		}
	}
	return shown
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
