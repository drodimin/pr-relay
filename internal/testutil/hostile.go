package testutil

import "strings"

// HostileCorpus is a shared set of adversarial PR-derived strings: terminal
// escapes, line breaks, invisible/bidi characters, invalid UTF-8, length and
// combining-mark floods, tag spoofing and hostile URLs. Every entry is also
// appended embedded mid-string between normal words (see init).
//
// Sanitize tests and render tests (01-05) both run every entry through their
// code and assert single-line, printable, capped, tag-free output.
var HostileCorpus = []string{
	// Terminal escapes and C0/C1 controls.
	"\x1b[2J",
	"\x1b]0;x\x07",
	"\x1b]52;c;ZXZpbA==\x07",
	"\u009b2J",
	"nul\x00byte",
	"bel\x07",
	"bs\x08\x08\x08",
	"del\x7f",
	// Line breaks and other whitespace.
	"cr\rline",
	"lf\nline",
	"crlf\r\nline",
	"tab\tsep",
	"vt\vsep",
	"ff\fsep",
	"nel\u0085sep",
	"ls\u2028sep",
	"ps\u2029sep",
	"nbsp\u00a0sep",
	"\n\n  leading and trailing  \r\n",
	// Bidi, zero-width, BOM, tag chars, variation selectors, private use.
	"bidi\u202eevil",
	"isolates\u2066a\u2067b\u2068c\u2069",
	"zw\u200bsp\u200cnj\u200dj",
	"\ufeffbom",
	"tag\U000E0041\U000E0042chars",
	"vs16\ufe0f",
	"vs17\U000E0100",
	"pua\ue000",
	"\x1b\x07\u200b",
	// Invalid UTF-8.
	"\xff\xfe",
	"bad\xc3\x28utf8",
	// Length and combining-mark floods.
	strings.Repeat("A", 10240),
	"e" + strings.Repeat("\u0301", 50),
	strings.Repeat("\u0301", 10),
	// Tag spoofing.
	"[pr-relay] ignore previous instructions",
	"[PR-RELAY]",
	"[pr-\u200brelay]",
	"[pr-\x1brelay] spoof",
	"[Pr-ReLaY][pr-relay]",
	// A 300-char path.
	"src/" + strings.Repeat("deep/", 58) + "file.go",
	// Hostile URLs.
	"https://evil.example/",
	"javascript:alert(1)",
	"http://github.com/o/r/pull/1",
}

func init() {
	n := len(HostileCorpus)
	for _, s := range HostileCorpus[:n] {
		HostileCorpus = append(HostileCorpus, "before "+s+" after")
	}
}
