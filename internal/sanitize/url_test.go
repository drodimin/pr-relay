package sanitize

import (
	"strings"
	"testing"

	"github.com/drodimin/pr-relay/internal/testutil"
)

const fb = "https://github.com/o/r/pull/1"

func TestURLAllowList(t *testing.T) {
	long := "https://github.com/o/r/pull/1#" + strings.Repeat("a", MaxURLBytes-len("https://github.com/o/r/pull/1#"))
	tests := []struct {
		name string
		in   string
		ok   bool
	}{
		{"discussion", "https://github.com/o/r/pull/1#discussion_r5", true},
		{"checks", "https://github.com/o/r/pull/1/checks", true},
		{"check-run", "https://github.com/o/r/actions/runs/1/job/2?pr=3", true},
		{"max-len", long, true},
		{"http", "http://github.com/x", false},
		{"evil-host", "https://evil.example/", false},
		{"javascript", "javascript:alert(1)", false},
		{"suffix-host", "https://github.com.evil.com/x", false},
		{"userinfo", "https://user@github.com/x", false},
		{"userinfo-pass", "https://user:pw@github.com/x", false},
		{"port", "https://github.com:8443/x", false},
		{"upper-host", "https://GITHUB.COM/x", false},
		{"upper-scheme", "HTTPS://github.com/x", false},
		{"subdomain", "https://api.github.com/x", false},
		{"empty", "", false},
		{"too-long", long + "a", false},
		{"space", "https://github.com/o/r pull", false},
		{"tab", "https://github.com/o/r\tpull", false},
		{"newline", "https://github.com/o/r\n", false},
		{"esc", "https://github.com/o/r\x1b[2J", false},
		{"del", "https://github.com/o/r\x7f", false},
		{"non-ascii", "https://github.com/o/r/\u00e9", false},
		{"bidi", "https://github.com/o/r/\u202e", false},
		{"scheme-relative", "//github.com/x", false},
		{"no-path", "https://github.com", false},
		{"backslash-host", "https://github.com\\@evil.com/x", false},
		{"data", "data:text/html,hi", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := URL(tc.in, fb)
			want := fb
			if tc.ok {
				want = tc.in
			}
			if got != want {
				t.Errorf("URL(%q) = %q, want %q", tc.in, got, want)
			}
		})
	}
}

func TestURLHostileCorpus(t *testing.T) {
	for _, in := range testutil.HostileCorpus {
		if got := URL(in, fb); got != fb {
			t.Errorf("URL(%q) = %q, want fallback", in, got)
		}
	}
}

func TestShortSHA(t *testing.T) {
	tests := map[string]string{
		"ABCDEF1234567":         "abcdef1",
		"abcdef1":               "abcdef1",
		strings.Repeat("a", 40): "aaaaaaa",
		strings.Repeat("a", 41): "?",
		"abc123":                "?",
		"xyz1234":               "?",
		"":                      "?",
		"abcdef1\n":             "?",
		"abcd ef1234":           "?",
	}
	for in, want := range tests {
		if got := ShortSHA(in); got != want {
			t.Errorf("ShortSHA(%q) = %q, want %q", in, got, want)
		}
	}
}
