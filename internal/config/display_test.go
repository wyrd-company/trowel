package config

import (
	"fmt"
	"strings"
	"testing"
)

func displayOf(t *testing.T, url, show string) string {
	t.Helper()
	raw := fmt.Sprintf("subjects:\n  - name: s\n    subject: s\n    destinations:\n      - name: d\n        http:\n          url: %q\n%s", url, show)
	cfg, err := Parse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	return cfg.Subjects[0].Destinations[0].HTTP.DisplayURL
}

func TestDisplayURL(t *testing.T) {
	t.Setenv("TEST_HOOK", "s3cret-hook")
	t.Setenv("TEST_SIG", "s3cret-sig")
	t.Setenv("TEST_HOST", "api.example.test")
	t.Setenv("TEST_CREDS", "user:s3cret-pass")
	cases := []struct {
		name, url, show, want string
	}{
		{"reference kept", "https://hooks.example.test/services/${TEST_HOOK}", "", "https://hooks.example.test/services/${TEST_HOOK}"},
		{"shown reference expanded", "https://${TEST_HOST}/in?sig=${TEST_SIG}", "          show: [TEST_HOST]\n", "https://api.example.test/in?sig=${TEST_SIG}"},
		{"no references", "https://api.example.test/in?sig=literal", "", ""},
		{"literal userinfo", "https://user:literal-pass@${TEST_HOST}/x", "", "https://REDACTED:REDACTED@${TEST_HOST}/x"},
		{"shown userinfo", "https://${TEST_CREDS}@api.example.test/${TEST_HOOK}", "          show: [TEST_CREDS]\n", "https://REDACTED:REDACTED@api.example.test/${TEST_HOOK}"},
		{"at sign in path", "https://${TEST_HOST}/a@b", "          show: [TEST_HOST]\n", "https://api.example.test/a@b"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := displayOf(t, c.url, c.show)
			if got != c.want {
				t.Errorf("display %q, want %q", got, c.want)
			}
			if strings.Contains(got, "s3cret") {
				t.Errorf("display %q carries a secret", got)
			}
		})
	}
}

func TestShowMustNameAReference(t *testing.T) {
	t.Setenv("TEST_HOST", "api.example.test")
	raw := "subjects:\n  - name: s\n    subject: s\n    destinations:\n      - name: d\n        http:\n          url: https://${TEST_HOST}/in\n          show: [TEST_OTHER]\n"
	if _, err := Parse([]byte(raw)); err == nil || !strings.Contains(err.Error(), "TEST_OTHER") {
		t.Errorf("err %v, want show rejected", err)
	}
}

// TestDisplayURLFollowsDecoding covers YAML shapes where the written
// document and the decoded config differ: null entries, which decoding
// skips, and anchors, aliases, merge keys, and flow style.
func TestDisplayURLFollowsDecoding(t *testing.T) {
	t.Setenv("TEST_HOST", "api.example.test")
	t.Setenv("TEST_TOKEN", "s3cret-token")
	const want = "https://api.example.test/${TEST_TOKEN}"
	cases := map[string]string{
		"null subject": `subjects:
  - null
  - name: s
    subject: s
    destinations: [{name: d, http: {url: "https://${TEST_HOST}/${TEST_TOKEN}", show: [TEST_HOST]}}]
`,
		"null destination": `subjects:
  - name: s
    subject: s
    destinations:
      - null
      - {name: d, http: {url: "https://${TEST_HOST}/${TEST_TOKEN}", show: [TEST_HOST]}}
`,
		"http alias": `subjects:
  - name: s
    subject: s
    destinations:
      - {name: a, http: &endpoint {url: "https://${TEST_HOST}/${TEST_TOKEN}", show: [TEST_HOST]}}
      - {name: d, http: *endpoint}
`,
		"merge key": `subjects:
  - name: s
    subject: s
    destinations:
      - {name: a, http: &endpoint {url: "https://${TEST_HOST}/${TEST_TOKEN}", show: [TEST_HOST]}}
      - name: d
        http:
          <<: *endpoint
          timeout: 5s
`,
		"scalar alias": `subjects:
  - name: s
    subject: s
    destinations:
      - {name: a, http: {url: &u "https://${TEST_HOST}/${TEST_TOKEN}", show: [TEST_HOST]}}
      - {name: d, http: {url: *u, show: [TEST_HOST]}}
`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			cfg, err := Parse([]byte(raw))
			if err != nil {
				t.Fatal(err)
			}
			dests := cfg.Subjects[len(cfg.Subjects)-1].Destinations
			if got := dests[len(dests)-1].HTTP.DisplayURL; got != want {
				t.Errorf("display %q, want %q", got, want)
			}
		})
	}
}

// TestDisplayURLNeverShowsUserinfo builds URLs whose userinfo the display
// template cannot locate, and expects full redaction instead.
func TestDisplayURLNeverShowsUserinfo(t *testing.T) {
	t.Setenv("TEST_PREFIX", "http://")
	t.Setenv("TEST_SLASHES", "//")
	t.Setenv("TEST_CREDS", "generic-user:s3cret-pass")
	cases := map[string]struct{ url, show string }{
		"shown creds after unshown scheme":    {"${TEST_PREFIX}${TEST_CREDS}@example.test/path", "          show: [TEST_CREDS]\n"},
		"literal creds after unshown scheme":  {"${TEST_PREFIX}generic-user:s3cret-pass@example.test/path", ""},
		"literal creds after unshown slashes": {"https:${TEST_SLASHES}generic-user:s3cret-pass@example.test/path", ""},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			got := displayOf(t, c.url, c.show)
			if strings.Contains(got, "s3cret") || strings.Contains(got, "generic-user") {
				t.Errorf("display %q shows userinfo", got)
			}
		})
	}
}
