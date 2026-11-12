package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeFiles writes name → content under dir, creating parents.
func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func sourcePaths(srcs []Source) []string {
	out := make([]string, len(srcs))
	for i, s := range srcs {
		out[i] = filepath.Base(s.Path)
	}
	return out
}

func TestReadOrdersAndFiltersSnippets(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"config.yml":               "",
		"conf.d/20-b.yaml":         "",
		"conf.d/10-a.yml":          "",
		"conf.d/.hidden.yml":       "",
		"conf.d/README.md":         "",
		"conf.d/30-c.yml.bak":      "",
		"conf.d/sub.yml/inner.yml": "",
		"elsewhere/40-d.yml":       "",
	})
	if err := os.Symlink(filepath.Join(dir, "elsewhere/40-d.yml"), filepath.Join(dir, "conf.d/40-d.yml")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "elsewhere"), filepath.Join(dir, "conf.d/50-dir.yml")); err != nil {
		t.Fatal(err)
	}
	srcs, err := Read(filepath.Join(dir, "config.yml"))
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(sourcePaths(srcs), " ")
	if want := "config.yml 10-a.yml 20-b.yaml 40-d.yml"; got != want {
		t.Errorf("sources %q, want %q", got, want)
	}
}

func TestReadWithoutSnippetDir(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{"config.yml": "x: 1"})
	srcs, err := Read(filepath.Join(dir, "config.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(srcs) != 1 || string(srcs[0].Raw) != "x: 1" {
		t.Errorf("sources %+v", srcs)
	}
}

func TestReadFailsOnBrokenSnippetLink(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{"config.yml": ""})
	if err := os.Mkdir(filepath.Join(dir, "conf.d"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "missing.yml"), filepath.Join(dir, "conf.d/a.yml")); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(filepath.Join(dir, "config.yml")); err == nil || !strings.Contains(err.Error(), "a.yml") {
		t.Errorf("error %v does not name the broken snippet", err)
	}
}

func TestReadFailsWhenSnippetDirIsAFile(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{"config.yml": "", "conf.d": ""})
	if _, err := Read(filepath.Join(dir, "config.yml")); err == nil {
		t.Error("a conf.d file was accepted as an empty snippet directory")
	}
}

func TestParseSourcesJoinsListsInOrder(t *testing.T) {
	t.Setenv("TEST_SNIPPET_HOST", "snippet.example")
	cfg, err := ParseSources([]Source{
		{Path: "/etc/trowel/config.yml", Raw: []byte("server:\n  listen: \":9000\"\nwebhooks:\n  - path: /a\n    subject: a\n")},
		{Path: "/etc/trowel/conf.d/10.yml", Raw: []byte("webhooks:\n  - path: /b\n    subject: b\nsubjects:\n  - name: s\n    subject: x\n    destinations:\n      - name: d\n        http:\n          url: https://${TEST_SNIPPET_HOST}/x\n")},
		{Path: "/etc/trowel/conf.d/20.yml", Raw: []byte("# comments only\n")},
		{Path: "/etc/trowel/conf.d/30.yml", Raw: []byte("webhooks:\n  - path: /c\n    subject: c\nnats:\n  url: nats://n:4222\n")},
	})
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, w := range cfg.Webhooks {
		paths = append(paths, w.Path)
	}
	if got := strings.Join(paths, " "); got != "/a /b /c" {
		t.Errorf("webhooks %q", got)
	}
	if cfg.Server.Listen != ":9000" || cfg.NATS.URL != "nats://n:4222" {
		t.Errorf("sections not taken from their files: %+v %+v", cfg.Server, cfg.NATS)
	}
	h := cfg.Subjects[0].Destinations[0].HTTP
	if h.URL != "https://snippet.example/x" || h.Template != "https://${TEST_SNIPPET_HOST}/x" {
		t.Errorf("snippet expansion or template lost: url %q template %q", h.URL, h.Template)
	}
}

func TestParseSourcesErrors(t *testing.T) {
	main := Source{Path: "/etc/trowel/config.yml", Raw: []byte("nats:\n  url: nats://a:4222\nwebhooks:\n  - path: /a\n    subject: a\n")}
	cases := map[string]struct {
		snippet string
		want    []string
	}{
		"section set twice": {"nats:\n  name: other\n", []string{"conf.d/10.yml", "nats is already set in /etc/trowel/config.yml"}},
		"unknown field":     {"webhooks:\n  - path: /b\n    subjekt: b\n", []string{"conf.d/10.yml", "subjekt"}},
		"unknown section":   {"extra: 1\n", []string{"conf.d/10.yml", "extra"}},
		"unparseable":       {"webhooks: [", []string{"conf.d/10.yml", "parse config"}},
		"not a mapping":     {"- a\n", []string{"conf.d/10.yml"}},
		"unset env":         {"webhooks:\n  - path: /b\n    subject: ${TEST_SNIPPET_UNSET}\n", []string{"conf.d/10.yml", "TEST_SNIPPET_UNSET"}},
		"merge key":         {"base: &b {}\n<<: *b\n", []string{"conf.d/10.yml"}},
		"duplicate path":    {"webhooks:\n  - path: /a\n    subject: b\n", []string{"duplicate path"}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ParseSources([]Source{main, {Path: "/etc/trowel/conf.d/10.yml", Raw: []byte(c.snippet)}})
			if err == nil {
				t.Fatal("accepted")
			}
			for _, w := range c.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error %q does not contain %q", err, w)
				}
			}
		})
	}
}

func TestParseSourcesJoinsAnchoredList(t *testing.T) {
	cfg, err := ParseSources([]Source{
		{Raw: []byte("webhooks:\n  - path: /a\n    subject: a\nsubjects: []\n")},
		{Path: "s.yml", Raw: []byte("webhooks: &h\n  - path: /b\n    subject: b\n")},
	})
	if err != nil || len(cfg.Webhooks) != 2 {
		t.Fatalf("webhooks %+v, err %v", cfg, err)
	}
}

func TestParseSourcesKeepsAliasesInTheirFile(t *testing.T) {
	relay := func(name, subject string) string {
		return "  - name: " + name + "\n    subject: " + subject + "\n    destinations:\n      - name: d\n        command:\n          argv: [\"true\"]\n"
	}
	// The snippet's webhooks come before its subjects, the main file's
	// after, so joining the lists puts the snippet's alias ahead of its
	// anchor and after the main file's anchor of the same name.
	snippet := Source{Path: "s.yml", Raw: []byte("webhooks:\n  - path: /b\n    subject: &event events.b\nsubjects:\n" + relay("from-snippet", "*event"))}
	cases := map[string]Source{
		"anchor name reused": {Raw: []byte("subjects:\n" + relay("from-main", "&event events.a") + "webhooks:\n  - path: /a\n    subject: a\n")},
		"anchor only here":   {Raw: []byte("subjects:\n" + relay("from-main", "events.a") + "webhooks:\n  - path: /a\n    subject: a\n")},
	}
	for name, main := range cases {
		t.Run(name, func(t *testing.T) {
			cfg, err := ParseSources([]Source{main, snippet})
			if err != nil {
				t.Fatal(err)
			}
			if got := cfg.Subjects[1].Subject; got != "events.b" {
				t.Errorf("snippet relay subscribes to %q, want events.b", got)
			}
		})
	}
}

func TestParseSourcesKeepsAliasedURLTemplate(t *testing.T) {
	t.Setenv("TEST_ALIAS_HOST", "alias.example")
	cfg, err := ParseSources([]Source{
		{Raw: []byte("subjects:\n  - name: m\n    subject: m\n    destinations:\n      - name: d\n        http:\n          url: &u https://main.example/${TEST_ALIAS_HOST}\n")},
		{Path: "s.yml", Raw: []byte("webhooks:\n  - path: /b\n    subject: &u b\nsubjects:\n  - name: s\n    subject: s\n    destinations:\n      - name: d\n        http:\n          url: &v https://${TEST_ALIAS_HOST}/x\n      - name: e\n        http:\n          url: *v\n")},
	})
	if err != nil {
		t.Fatal(err)
	}
	e := cfg.Subjects[1].Destinations[1].HTTP
	if e.URL != "https://alias.example/x" || e.Template != "https://${TEST_ALIAS_HOST}/x" {
		t.Errorf("aliased url %q template %q", e.URL, e.Template)
	}
}

func TestParseSourcesTreatsEmptySectionAsUnset(t *testing.T) {
	cfg, err := ParseSources([]Source{
		{Raw: []byte("nats:\nwebhooks:\n")},
		{Path: "s.yml", Raw: []byte("nats:\n  url: nats://n:4222\nwebhooks:\n  - path: /a\n    subject: a\n")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.NATS.URL != "nats://n:4222" || len(cfg.Webhooks) != 1 {
		t.Errorf("nats %+v, webhooks %+v", cfg.NATS, cfg.Webhooks)
	}
}

func TestDigestCoversNamesAndContents(t *testing.T) {
	base := []Source{{Path: "c.yml", Raw: []byte("a")}, {Path: "conf.d/1.yml", Raw: []byte("b")}}
	same := []Source{{Path: "c.yml", Raw: []byte("a")}, {Path: "conf.d/1.yml", Raw: []byte("b")}}
	if Digest(base) != Digest(same) {
		t.Error("identical sources differ")
	}
	for name, other := range map[string][]Source{
		"content":  {{Path: "c.yml", Raw: []byte("a")}, {Path: "conf.d/1.yml", Raw: []byte("c")}},
		"name":     {{Path: "c.yml", Raw: []byte("a")}, {Path: "conf.d/2.yml", Raw: []byte("b")}},
		"added":    append(append([]Source{}, base...), Source{Path: "conf.d/3.yml"}),
		"boundary": {{Path: "c.ymla", Raw: nil}, {Path: "conf.d/1.yml", Raw: []byte("b")}},
	} {
		if Digest(base) == Digest(other) {
			t.Errorf("%s change does not change the digest", name)
		}
	}
}
