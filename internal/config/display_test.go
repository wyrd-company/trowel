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
