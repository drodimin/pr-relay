package sanitize

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestFold(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"ascii", "hello", "hello"},
		{"ascii-upper", "[PR-RELAY]", "[pr-relay]"},
		{"fullwidth", "［PR-relay］", "[pr-relay]"},
		{"cyrillic", "[рr-rеlаy]", "[pr-relay]"},
		{"greek", "[ρr-rειαγ]", "[pr-relay]"},
		{"brackets-hyphen", "⟦pr‐relay⟧", "[pr-relay]"},
		{"cjk-brackets", "【pr−relay】", "[pr-relay]"},
		{"math-bold", "[\U0001D429r-relay]", "[pr-relay]"},
		{"pipe-l", "[pr-re|ay]", "[pr-relay]"},
		{"multi-rune-nfkc-kept", "ﬁ", "ﬁ"},
		{"empty", "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Fold(tc.in)
			if got != tc.want {
				t.Errorf("Fold(%q) = %q, want %q", tc.in, got, tc.want)
			}
			if utf8.RuneCountInString(got) != utf8.RuneCountInString(tc.in) {
				t.Errorf("Fold(%q) changed rune count", tc.in)
			}
		})
	}
}

func TestNeutralizeTagFolded(t *testing.T) {
	ins := []string{
		"ci ［pr-relay］ ok",
		"[рr-rеlаy] o/r#1 review approved",
		"⟦pr-relay⟧",
		"[PR‐RELAY]",
		"x 【pr-relay】 ［PR－RELAY］ y",
	}
	for _, in := range ins {
		got := NeutralizeTag(in)
		if utf8.RuneCountInString(got) != utf8.RuneCountInString(in) {
			t.Errorf("NeutralizeTag(%q) = %q: rune count changed", in, got)
		}
		if strings.Contains(Fold(got), "[pr-relay]") {
			t.Errorf("NeutralizeTag(%q) = %q: still folds to tag", in, got)
		}
		if again := NeutralizeTag(got); again != got {
			t.Errorf("NeutralizeTag not idempotent: %q -> %q", got, again)
		}
	}
	for _, plain := range []string{"lint", "[x] [bot]", "dependabot[bot]", "pr-relay", ""} {
		if got := NeutralizeTag(plain); got != plain {
			t.Errorf("NeutralizeTag(%q) = %q, want unchanged", plain, got)
		}
	}
}

func TestFillersStripped(t *testing.T) {
	for _, f := range []rune{0x3164, 0x115F, 0x1160, 0xFFA0, 0x2800} {
		in := "[pr" + string(f) + "-relay] x"
		got := Field(in, 100)
		if strings.ContainsRune(got, f) {
			t.Errorf("Field(%q) = %q: filler %U kept", in, got, f)
		}
		if strings.Contains(Fold(got), "[pr-relay]") {
			t.Errorf("Field(%q) = %q: folds to tag", in, got)
		}
		if got := Login(string(f) + "login"); got != "login" {
			t.Errorf("Login with filler %U = %q, want %q", f, got, "login")
		}
		if got := Field(string(f)+string(f), 80); got != "?" {
			t.Errorf("filler-only Field = %q, want ?", got)
		}
	}
}
