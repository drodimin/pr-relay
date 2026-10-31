package sanitize

import (
	"net/url"
	"regexp"
	"strings"
)

// MaxURLBytes is the longest URL that may be interpolated. Longer URLs are
// replaced by the fallback, never truncated.
const MaxURLBytes = 300

const urlPrefix = "https://github.com/"

// URL returns raw unchanged if it is an allowed GitHub web URL, otherwise
// fallback. Allowed means: at most MaxURLBytes, printable ASCII only (0x21-0x7E,
// so no spaces or controls), scheme exactly "https", host exactly lowercase
// "github.com" (no port, no subdomain), no userinfo and not opaque.
// The result is always raw or fallback byte-for-byte; URLs are never
// truncated. The caller must build fallback from validated parts.
func URL(raw, fallback string) string {
	if len(raw) > MaxURLBytes || !strings.HasPrefix(raw, urlPrefix) {
		return fallback
	}
	for i := 0; i < len(raw); i++ {
		if c := raw[i]; c < 0x21 || c > 0x7E {
			return fallback
		}
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host != "github.com" || u.User != nil || u.Opaque != "" {
		return fallback
	}
	return raw
}

var shaRE = regexp.MustCompile(`^[0-9a-f]{7,40}$`)

// ShortSHA returns the first 7 lowercase hex characters of a 7-40 character
// hex commit SHA, or "?" for anything else.
func ShortSHA(s string) string {
	s = strings.ToLower(s)
	if !shaRE.MatchString(s) {
		return placeholder
	}
	return s[:7]
}
