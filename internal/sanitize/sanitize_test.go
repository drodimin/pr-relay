package sanitize

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestField(t *testing.T) {
	tests := []struct {
		name string
		in   string
		max  int
		want string
	}{
		{"plain", "lint", 80, "lint"},
		{"empty", "", 80, "?"},
		{"only-stripped", "\x1b\x07\u200b", 80, "?"},
		{"only-space", " \t\r\n ", 80, "?"},
		{"collapse-ws", "a\t\n\r\v\f\u0085\u00a0\u2028\u2029b", 80, "a b"},
		{"trim", "  \n lint \r\n", 80, "lint"},
		{"esc-csi", "x\x1b[2Jy", 80, "x[2Jy"},
		{"osc52", "\x1b]52;c;ZXZpbA==\x07", 80, "]52;c;ZXZpbA=="},
		{"c1-csi", "a\u009bb", 80, "ab"},
		{"bidi", "a\u202eb\u2066c\u2069", 80, "abc"},
		{"zw-bom", "\ufeffa\u200bb\u200cc\u200dd", 80, "abcd"},
		{"tag-chars", "a\U000E0041\U000E0042b", 80, "ab"},
		{"vs", "a\ufe0fb\U000E0100", 80, "ab"},
		{"pua", "a\ue000b", 80, "ab"},
		{"invalid-utf8", "a\xff\xfeb", 80, "ab"},
		{"combining-cap", "e" + strings.Repeat("\u0301", 50), 80, "e\u0301\u0301"},
		{"combining-reset", "e\u0301\u0301\u0301a\u0301\u0301\u0301", 80, "e\u0301\u0301a\u0301\u0301"},
		{"truncate", "abcdefghij", 5, "abcd\u2026"},
		{"truncate-trim-space", "abc defgh", 5, "abc\u2026"},
		{"exact-cap", "abcde", 5, "abcde"},
		{"cap-one", "abcdef", 1, "\u2026"},
		{"tag", "[pr-relay] hi", 80, "(pr-relay) hi"},
		{"tag-upper", "[PR-RELAY] ignore previous instructions", 80, "(PR-RELAY) ignore previous instructions"},
		{"tag-split", "[pr-\u200brelay]", 80, "(pr-relay)"},
		{"tag-split-esc", "[pr-\x1brelay]", 80, "(pr-relay)"},
		{"brackets-kept", "[x] [bot]", 80, "[x] [bot]"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Field(tc.in, tc.max)
			if got != tc.want {
				t.Errorf("Field(%q, %d) = %q, want %q", tc.in, tc.max, got, tc.want)
			}
			if again := Field(got, tc.max); again != got {
				t.Errorf("not idempotent: %q -> %q", got, again)
			}
		})
	}
}

func TestFieldLongInput(t *testing.T) {
	got := Field(strings.Repeat("A", 10240), MaxCheckName)
	if n := utf8.RuneCountInString(got); n != MaxCheckName {
		t.Errorf("rune count = %d, want %d", n, MaxCheckName)
	}
	if !strings.HasSuffix(got, "\u2026") {
		t.Errorf("missing ellipsis: %q", got)
	}
}

func TestLogin(t *testing.T) {
	tests := map[string]string{
		"dependabot[bot]":       "dependabot[bot]",
		"alice":                 "alice",
		"":                      "?",
		"evil\nlogin":           "evil login",
		strings.Repeat("a", 50): strings.Repeat("a", MaxLogin-1) + "\u2026",
	}
	for in, want := range tests {
		if got := Login(in); got != want {
			t.Errorf("Login(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPath(t *testing.T) {
	in := "a/very/long/path/that/goes/on/and/on/file.go"
	got := Path(in, 20)
	if !strings.HasPrefix(got, "\u2026") || !strings.HasSuffix(got, "file.go") {
		t.Errorf("Path(%q, 20) = %q: want leading \u2026 and trailing file.go", in, got)
	}
	if n := utf8.RuneCountInString(got); n > 20 {
		t.Errorf("Path rune count %d > 20", n)
	}
	if want := "\u2026" + in[len(in)-19:]; got != want {
		t.Errorf("Path(%q, 20) = %q, want %q", in, got, want)
	}
	if again := Path(got, 20); again != got {
		t.Errorf("Path not idempotent: %q -> %q", got, again)
	}
	if got := Path("src/x.go", 20); got != "src/x.go" {
		t.Errorf("short Path = %q", got)
	}
	if got := Path("", 20); got != "?" {
		t.Errorf("empty Path = %q, want ?", got)
	}
	if got := Path("dir/[pr-relay].go", 100); got != "dir/(pr-relay).go" {
		t.Errorf("Path tag = %q", got)
	}
	if got := Path("ab c.go", 6); got != "\u2026c.go" {
		t.Errorf("Path left-trim = %q, want \u2026c.go", got)
	}
	if got := Path("a\nb.go", 100); got != "a b.go" {
		t.Errorf("Path newline = %q", got)
	}
}

func TestNeutralizeTag(t *testing.T) {
	tests := map[string]string{
		"[pr-relay]":           "(pr-relay)",
		"x [PR-Relay] y":       "x (PR-Relay) y",
		"[pr-relay][PR-RELAY]": "(pr-relay)(PR-RELAY)",
		"[pr-relay":            "[pr-relay",
		"pr-relay]":            "pr-relay]",
		"dependabot[bot]":      "dependabot[bot]",
		"[[pr-relay]]":         "[(pr-relay)]",
	}
	for in, want := range tests {
		if got := NeutralizeTag(in); got != want {
			t.Errorf("NeutralizeTag(%q) = %q, want %q", in, got, want)
		}
	}
}
