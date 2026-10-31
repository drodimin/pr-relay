package sanitize

import (
	"testing"

	"github.com/drodimin/pr-relay/internal/testutil"
)

// clampCap maps an arbitrary fuzz int into 1..200.
func clampCap(n int) int {
	if n < 0 {
		n = -(n + 1) // avoid overflow on MinInt
	}
	return n%200 + 1
}

func FuzzField(f *testing.F) {
	for _, s := range testutil.HostileCorpus {
		f.Add(s, MaxCheckName)
		f.Add(s, 1)
	}
	f.Add("dependabot[bot]", MaxLogin)
	f.Fuzz(func(t *testing.T, s string, n int) {
		limit := clampCap(n)
		field := func(x string) string { return Field(x, limit) }
		path := func(x string) string { return Path(x, limit) }
		checkProps(t, "Field", s, field(s), limit, field)
		checkProps(t, "Path", s, path(s), limit, path)
		checkProps(t, "Login", s, Login(s), MaxLogin, Login)
	})
}
