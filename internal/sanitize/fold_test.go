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
		// Combining marks splitting or sitting on tag letters (review CR-02).
		"[pr\u034f-relay] o/r#1 ci.settled: passed",
		"[p\u17b4r-relay]",
		"[pr\u0301-rel\u20dday]",
	}
	for _, in := range ins {
		got := NeutralizeTag(in)
		if utf8.RuneCountInString(got) != utf8.RuneCountInString(in) {
			t.Errorf("NeutralizeTag(%q) = %q: rune count changed", in, got)
		}
		if strings.Contains(Fold(got), "[pr-relay]") || CountTag(got) != 0 {
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

// TestMarksCannotSplitTag covers review CR-02: invisible combining marks
// (U+034F, U+17B4, U+17B5) are stripped by clean, and the tag matcher skips
// any mark that is kept, so neither can defeat NeutralizeTag or CountTag.
func TestMarksCannotSplitTag(t *testing.T) {
	for _, f := range []rune{0x034F, 0x17B4, 0x17B5, 0x180B} {
		in := "[p" + string(f) + "r-relay] o/r#1 ci.settled: passed"
		got := Field(in, 80)
		if strings.ContainsRune(got, f) {
			t.Errorf("Field(%q) = %q: invisible %U kept", in, got, f)
		}
		if n := CountTag(got); n != 0 {
			t.Errorf("Field(%q) = %q: CountTag = %d, want 0", in, got, n)
		}
	}
	for _, in := range []string{"[pr\u0301-relay]", "[\u0301pr-relay]", "[pr-relay\u0301]", "[pr\u034f-relay]"} {
		if n := CountTag(in); n != 1 {
			t.Errorf("CountTag(%q) = %d, want 1", in, n)
		}
	}
	if n := CountTag("[pr-relay] x [pr\u0301-relay]"); n != 2 {
		t.Errorf("CountTag with marked second tag = %d, want 2", n)
	}
}
