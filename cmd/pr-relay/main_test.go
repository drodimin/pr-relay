package main

import "testing"

func TestVersionString(t *testing.T) {
	if got, want := versionString(), "pr-relay dev"; got != want {
		t.Errorf("versionString() = %q, want %q", got, want)
	}
}
