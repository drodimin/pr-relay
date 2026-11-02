// Package sanitize neutralizes PR-derived strings (check names, logins, paths,
// URLs, SHAs) before they are interpolated into a message typed into an
// agent's pane (DLV-06). It is the security boundary for every third-party
// string: output is always a single printable line, capped in length, and can
// never spoof the "[pr-relay]" envelope tag.
//
// Resolved choices (Claude's Discretion, phase 01 CONTEXT):
//   - Caps: login 40 runes (GitHub max is 39), check name 80, path 100.
//   - Strip set: invalid UTF-8 (utf8.RuneError), every rune with
//     !unicode.IsPrint (Cc incl. ESC and C1, Cf incl. bidi controls,
//     zero-width chars, BOM and tag chars, Co private use, Cs, unassigned),
//     and unicode.Variation_Selector (VS1-VS256).
//   - Whitespace: every unicode.IsSpace rune collapses with its neighbours
//     into one ASCII space; leading and trailing spaces are trimmed.
//   - Combining marks (Mn, Me): at most 2 per base rune; the rest are dropped.
//   - The ellipsis "…" (U+2026) counts inside the cap.
//   - Empty output becomes the "?" placeholder.
//   - Tag neutralization runs last, after stripping, so invisible characters
//     cannot be used to split and later reassemble the tag.
//   - '[' and ']' are kept in general, because bot logins render as name[bot].
//   - Confusable fold (CR-02): tag matching compares Fold(s), not s. Fold
//     applies NFKC per rune, lowercases, and maps Cyrillic/Greek lookalikes
//     of the tag letters and Unicode brackets/hyphens to ASCII, so
//     "［pr-relay］", "[рr-rеlаy]" and "⟦pr‐relay⟧" are all neutralized. The
//     reader of relay lines is an LLM, which reads such lookalikes as the tag.
//   - Invisible fillers (IN-01): U+3164, U+115F, U+1160, U+FFA0 and U+2800
//     are printable per unicode.IsPrint but render blank, so they are
//     stripped explicitly; they cannot split a tag or blank a login.
//
// Every function is pure, deterministic and idempotent.
package sanitize

import (
	"unicode"
	"unicode/utf8"
)

// Rune caps for interpolated fields.
const (
	MaxLogin     = 40
	MaxCheckName = 80
	MaxPath      = 100
)

const (
	ellipsis        = '…'
	placeholder     = "?"
	maxMarksPerBase = 2
)

// clean returns the kept runes of s: stripped, whitespace-collapsed,
// combining-limited and trimmed. If limit > 0 it stops once limit runes have
// been collected (callers pass cap+1 to detect truncation cheaply).
func clean(s string, limit int) []rune {
	out := make([]rune, 0, min(len(s), 256))
	pendingSpace := false
	marks := 0
	for _, r := range s {
		switch {
		case r == utf8.RuneError:
			continue
		case unicode.IsSpace(r):
			pendingSpace = true
			continue
		case !unicode.IsPrint(r), unicode.Is(unicode.Variation_Selector, r), isFiller(r):
			continue
		}
		isMark := unicode.In(r, unicode.Mn, unicode.Me)
		if isMark && marks >= maxMarksPerBase && !pendingSpace {
			continue
		}
		if pendingSpace && len(out) > 0 {
			if limit > 0 && len(out) >= limit {
				return out
			}
			out = append(out, ' ')
			marks = 0
		}
		pendingSpace = false
		if isMark {
			marks++
		} else {
			marks = 0
		}
		if limit > 0 && len(out) >= limit {
			return out
		}
		out = append(out, r)
	}
	return out
}

// isFiller reports whether r is a printable-but-invisible filler (IN-01).
func isFiller(r rune) bool {
	switch r {
	case 0x3164, // U+3164 Hangul filler
		0x115F, // Hangul choseong filler
		0x1160, // Hangul jungseong filler
		0xFFA0, // halfwidth Hangul filler
		0x2800: // braille pattern blank
		return true
	}
	return false
}

func trimSpaceRunes(rs []rune) []rune {
	for len(rs) > 0 && rs[0] == ' ' {
		rs = rs[1:]
	}
	for len(rs) > 0 && rs[len(rs)-1] == ' ' {
		rs = rs[:len(rs)-1]
	}
	return rs
}

func finish(rs []rune) string {
	if len(rs) == 0 {
		return placeholder
	}
	return NeutralizeTag(string(rs))
}

// Field neutralizes s for single-line interpolation and caps it at maxRunes
// runes (minimum 1). When truncated, the output is the head of the cleaned
// string followed by "…". An empty result becomes "?".
func Field(s string, maxRunes int) string {
	maxRunes = max(maxRunes, 1)
	rs := clean(s, maxRunes+1)
	if len(rs) > maxRunes {
		rs = append(trimSpaceRunes(rs[:maxRunes-1]), ellipsis)
	}
	return finish(rs)
}

// Login neutralizes a GitHub login (bot logins keep their "[bot]" suffix).
func Login(s string) string { return Field(s, MaxLogin) }

// Path neutralizes a file path like Field, but keeps the tail when truncating
// (the filename matters most): "…" followed by the last maxRunes-1 runes.
func Path(s string, maxRunes int) string {
	maxRunes = max(maxRunes, 1)
	rs := clean(s, 0)
	if len(rs) > maxRunes {
		tail := trimSpaceRunes(rs[len(rs)-(maxRunes-1):])
		rs = append([]rune{ellipsis}, tail...)
	}
	return finish(rs)
}

var tagRunes = []rune("[pr-relay]")

// NeutralizeTag replaces every run of runes in s that folds to "[pr-relay]"
// (see Fold: case, NFKC and lookalike insensitive) by turning its first rune
// into '(' and its last into ')', keeping the inner runes as they were. It
// preserves the rune count.
func NeutralizeTag(s string) string {
	if !hasOpenBracket(s) {
		return s
	}
	rs := []rune(s)
	changed := false
	for i := 0; i+len(tagRunes) <= len(rs); i++ {
		match := true
		for j, want := range tagRunes {
			if foldRune(rs[i+j]) != want {
				match = false
				break
			}
		}
		if match {
			rs[i] = '('
			rs[i+len(tagRunes)-1] = ')'
			changed = true
			i += len(tagRunes) - 1
		}
	}
	if !changed {
		return s
	}
	return string(rs)
}

// hasOpenBracket reports whether any rune of s folds to '['.
func hasOpenBracket(s string) bool {
	for _, r := range s {
		if r == '[' || (r >= utf8.RuneSelf && foldRune(r) == '[') {
			return true
		}
	}
	return false
}
