package main

import "runtime/debug"

// init resolves version before any command runs. Package variables are
// initialised before init, so the ldflags value (if any) is already set.
func init() {
	info, ok := debug.ReadBuildInfo()
	version = resolveVersion(version, info, ok)
}

// resolveVersion picks the reported version (D-05/D-18): a release build's
// ldflags value wins; otherwise a `go install module@vX` build reports the
// module version from its build info; a local or devel build reports "dev".
func resolveVersion(ld string, info *debug.BuildInfo, ok bool) string {
	if ld != "" && ld != "dev" {
		return ld
	}
	if ok && info != nil {
		if v := info.Main.Version; v != "" && v != "(devel)" {
			return v
		}
	}
	return "dev"
}
