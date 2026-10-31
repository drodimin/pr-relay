package sanitize

import (
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"github.com/drodimin/pr-relay/internal/testutil"
)

// checkProps asserts the DLV-06 properties of one sanitizer output.
func checkProps(t *testing.T, name, in, out string, limit int, again func(string) string) {
	t.Helper()
	if !utf8.ValidString(out) {
		t.Errorf("%s(%q) = %q: invalid UTF-8", name, in, out)
	}
	for _, r := range out {
		if r != ' ' && !unicode.IsPrint(r) {
			t.Errorf("%s(%q) = %q: non-printable rune %U", name, in, out, r)
		}
	}
	if strings.ContainsAny(out, "\n\r") {
		t.Errorf("%s(%q) = %q: contains line break", name, in, out)
	}
	if n := utf8.RuneCountInString(out); n > limit {
		t.Errorf("%s(%q): %d runes > cap %d", name, in, n, limit)
	}
	if n := utf8.RuneCountInString(out); n == 0 {
		t.Errorf("%s(%q): empty output", name, in)
	}
	if strings.Contains(strings.ToLower(out), "[pr-relay]") {
		t.Errorf("%s(%q) = %q: contains tag", name, in, out)
	}
	if got := again(out); got != out {
		t.Errorf("%s not idempotent: %q -> %q", name, out, got)
	}
}

func TestHostile(t *testing.T) {
	if len(testutil.HostileCorpus) < 30 {
		t.Fatalf("corpus has %d entries, want >= 30", len(testutil.HostileCorpus))
	}
	for _, in := range testutil.HostileCorpus {
		field := func(s string) string { return Field(s, MaxCheckName) }
		path := func(s string) string { return Path(s, MaxPath) }

		out := field(in)
		if out != field(in) {
			t.Errorf("Field(%q) not deterministic", in)
		}
		checkProps(t, "Field", in, out, MaxCheckName, field)

		out = Login(in)
		if out != Login(in) {
			t.Errorf("Login(%q) not deterministic", in)
		}
		checkProps(t, "Login", in, out, MaxLogin, Login)

		out = path(in)
		if out != path(in) {
			t.Errorf("Path(%q) not deterministic", in)
		}
		checkProps(t, "Path", in, out, MaxPath, path)
	}
}
