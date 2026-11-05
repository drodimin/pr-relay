package main

import (
	"runtime/debug"
	"testing"
)

func TestResolveVersion(t *testing.T) {
	info := func(v string) *debug.BuildInfo {
		return &debug.BuildInfo{Main: debug.Module{Path: "github.com/drodimin/pr-relay", Version: v}}
	}
	const pseudo = "v0.0.0-20261105120000-abcdef123456+dirty"
	tests := []struct {
		name string
		ld   string
		info *debug.BuildInfo
		ok   bool
		want string
	}{
		{"ldflags wins over build info", "v0.1.0", info("v0.2.0"), true, "v0.1.0"},
		{"ldflags without build info", "v0.1.0", nil, false, "v0.1.0"},
		{"go install module version", "dev", info("v0.1.0"), true, "v0.1.0"},
		{"empty ldflags uses build info", "", info("v0.1.0"), true, "v0.1.0"},
		{"devel build", "dev", info("(devel)"), true, "dev"},
		{"empty module version", "dev", info(""), true, "dev"},
		{"no build info", "dev", nil, false, "dev"},
		{"pseudo-version kept", "dev", info(pseudo), true, pseudo},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolveVersion(tt.ld, tt.info, tt.ok); got != tt.want {
				t.Errorf("resolveVersion(%q, ..., %v) = %q, want %q", tt.ld, tt.ok, got, tt.want)
			}
		})
	}
}
