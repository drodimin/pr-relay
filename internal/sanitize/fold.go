package sanitize

import (
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// skeleton maps confusables of the tag characters ("[pr-relay]") to their
// ASCII form (CR-02). It runs after NFKC and lowercasing, so it lists only
// lowercase forms that NFKC leaves alone. It is deliberately limited to the
// tag letters: the folded render guard rejects anything it misses that still
// folds to a second tag.
var skeleton = map[rune]rune{
	// p
	0x0440: 'p', // Cyrillic er
	0x03C1: 'p', // Greek rho
	// r
	0x0433: 'r', // Cyrillic ghe
	0x1D26: 'r', // small capital Greek gamma
	// e
	0x0435: 'e', // Cyrillic ie
	0x04BD: 'e', // Cyrillic abkhasian che
	0x03B5: 'e', // Greek epsilon
	// l
	0x04CF: 'l', // Cyrillic palochka
	0x0456: 'l', // Cyrillic byelorussian-ukrainian i (and U+0406 via ToLower)
	0x03B9: 'l', // Greek iota
	0x01C0: 'l', // Latin dental click
	0x2223: 'l', // divides
	'|':    'l',
	// a
	0x0430: 'a', // Cyrillic a
	0x03B1: 'a', // Greek alpha
	// y
	0x0443: 'y', // Cyrillic u
	0x04AF: 'y', // Cyrillic straight u
	0x03B3: 'y', // Greek gamma
	// -
	0x2010: '-', 0x2011: '-', 0x2012: '-', 0x2013: '-', 0x2014: '-', 0x2015: '-',
	0x2212: '-', // minus sign
	0xFE63: '-', // small hyphen-minus (NFKC already maps it; kept for clarity)
	0x00AD: '-', // soft hyphen (normally stripped earlier as Cf)
	// [
	0x27E6: '[', 0x3010: '[', 0x3014: '[', 0x301A: '[', 0x2045: '[', 0xFE5D: '[',
	// ]
	0x27E7: ']', 0x3011: ']', 0x3015: ']', 0x301B: ']', 0x2046: ']', 0xFE5E: ']',
}

// foldRune maps r to its comparison form: NFKC (when that yields exactly one
// rune), then lowercase, then the tag skeleton.
func foldRune(r rune) rune {
	if r >= utf8.RuneSelf {
		n := norm.NFKC.String(string(r))
		if c, size := utf8.DecodeRuneInString(n); size == len(n) && c != utf8.RuneError {
			r = c
		}
	}
	r = unicode.ToLower(r)
	if s, ok := skeleton[r]; ok {
		return s
	}
	return r
}

// Fold returns the comparison form of s: foldRune applied to every rune. It
// preserves the rune count, so an index into Fold(s) by rune is an index into
// s. It is for matching only; its output is never rendered.
func Fold(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		out = append(out, foldRune(r))
	}
	return string(out)
}
