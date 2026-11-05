package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

// pluginManifestVersion is the single place to bump the herdr plugin
// manifest version; TestPluginManifest fails until both agree.
const pluginManifestVersion = "0.1.0"

// pluginMissingMsg must equal the D-10 line printed by herdr-plugin/run.sh.
const pluginMissingMsg = "pr-relay not found on PATH; install via brew install drodimin/tap/pr-relay, go install github.com/drodimin/pr-relay/cmd/pr-relay@latest, or a release download"

const (
	pluginManifestPath = "../../herdr-plugin/herdr-plugin.toml"
	pluginRunShPath    = "../../herdr-plugin/run.sh"
)

type pluginManifest struct {
	ID              string   `toml:"id"`
	Name            string   `toml:"name"`
	Version         string   `toml:"version"`
	MinHerdrVersion string   `toml:"min_herdr_version"`
	Description     string   `toml:"description"`
	Platforms       []string `toml:"platforms"`
	Startup         []struct {
		Command []string `toml:"command"`
	} `toml:"startup"`
	Actions []struct {
		ID       string   `toml:"id"`
		Title    string   `toml:"title"`
		Contexts []string `toml:"contexts"`
		Command  []string `toml:"command"`
	} `toml:"actions"`
}

func TestPluginManifest(t *testing.T) {
	var m pluginManifest
	md, err := toml.DecodeFile(pluginManifestPath, &m)
	if err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	// Strict: any key the struct does not model (e.g. a [[build]] table, D-06) fails.
	if und := md.Undecoded(); len(und) != 0 {
		t.Fatalf("manifest has unexpected keys: %v", und)
	}
	if m.ID != "pr-relay" {
		t.Errorf("id = %q, want pr-relay", m.ID)
	}
	if m.Name == "" || m.Description == "" {
		t.Errorf("name and description must be set: name=%q description=%q", m.Name, m.Description)
	}
	if m.MinHerdrVersion != "0.9.5" {
		t.Errorf("min_herdr_version = %q, want 0.9.5", m.MinHerdrVersion)
	}
	if m.Version != pluginManifestVersion {
		t.Errorf("version = %q, want %q", m.Version, pluginManifestVersion)
	}
	if !reflect.DeepEqual(m.Platforms, []string{"linux", "macos"}) {
		t.Errorf("platforms = %v, want [linux macos]", m.Platforms)
	}

	wantPrefix := []string{"sh", "run.sh"}
	if len(m.Startup) != 1 {
		t.Fatalf("startup entries = %d, want 1", len(m.Startup))
	}
	if want := []string{"sh", "run.sh", "daemon", "--ensure"}; !reflect.DeepEqual(m.Startup[0].Command, want) {
		t.Errorf("startup command = %q, want %q", m.Startup[0].Command, want)
	}

	wantActions := map[string][]string{
		"status":        {"status"},
		"list-all":      {"ls", "--all"},
		"restart":       {"daemon", "restart"},
		"install-skill": {"skill", "install"},
	}
	if len(m.Actions) != len(wantActions) {
		t.Fatalf("actions = %d, want %d", len(m.Actions), len(wantActions))
	}
	got := map[string][]string{}
	for _, a := range m.Actions {
		if strings.Contains(a.ID, ".") {
			t.Errorf("action id %q must not contain '.'", a.ID)
		}
		if a.Title == "" {
			t.Errorf("action %q has empty title", a.ID)
		}
		if !reflect.DeepEqual(a.Contexts, []string{"global"}) {
			t.Errorf("action %q contexts = %v, want [global]", a.ID, a.Contexts)
		}
		if len(a.Command) < 2 || !reflect.DeepEqual(a.Command[:2], wantPrefix) {
			t.Errorf("action %q command %q must start with %q", a.ID, a.Command, wantPrefix)
			continue
		}
		if _, dup := got[a.ID]; dup {
			t.Errorf("duplicate action id %q", a.ID)
		}
		got[a.ID] = a.Command[2:]
	}
	if !reflect.DeepEqual(got, wantActions) {
		t.Errorf("actions = %v, want %v", got, wantActions)
	}
}

// writeFakePrRelay writes an executable pr-relay script with the given body.
func writeFakePrRelay(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "pr-relay"), []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatalf("write fake pr-relay: %v", err)
	}
}

// runPluginSh runs run.sh via /bin/sh with an explicit env (never the
// inherited one, so no HERDR_* leaks) and a temp working dir.
func runPluginSh(t *testing.T, path string, args ...string) (stdout, stderr string, code int, workDir string) {
	t.Helper()
	runSh, err := filepath.Abs(pluginRunShPath)
	if err != nil {
		t.Fatalf("abs run.sh: %v", err)
	}
	workDir = t.TempDir()
	cmd := exec.Command("/bin/sh", append([]string{runSh}, args...)...)
	cmd.Env = []string{"PATH=" + path}
	cmd.Dir = workDir
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	err = cmd.Run()
	var ee *exec.ExitError
	switch {
	case err == nil:
		code = 0
	case errors.As(err, &ee):
		code = ee.ExitCode()
	default:
		t.Fatalf("run run.sh: %v", err)
	}
	return out.String(), errb.String(), code, workDir
}

func TestPluginRunSh(t *testing.T) {
	t.Run("missing", func(t *testing.T) {
		empty := t.TempDir()
		stdout, stderr, code, _ := runPluginSh(t, empty, "daemon", "--ensure")
		if code != 0 {
			t.Errorf("exit code = %d, want 0", code)
		}
		if stdout != "" {
			t.Errorf("stdout = %q, want empty", stdout)
		}
		if stderr != pluginMissingMsg+"\n" {
			t.Errorf("stderr = %q, want %q", stderr, pluginMissingMsg+"\n")
		}
	})

	t.Run("passthrough", func(t *testing.T) {
		bin := t.TempDir()
		writeFakePrRelay(t, bin, `printf '%s\n' "$@"`)
		stdout, stderr, code, workDir := runPluginSh(t, bin+":/usr/bin:/bin", "ls", "--all", "a b", "$(touch pwned)")
		if code != 0 {
			t.Fatalf("exit code = %d, stderr = %q", code, stderr)
		}
		if want := "ls\n--all\na b\n$(touch pwned)\n"; stdout != want {
			t.Errorf("stdout = %q, want %q", stdout, want)
		}
		for _, dir := range []string{workDir, bin} {
			if _, err := os.Stat(filepath.Join(dir, "pwned")); err == nil {
				t.Errorf("shell injection: pwned created in %s", dir)
			}
		}
	})

	t.Run("exitcode", func(t *testing.T) {
		bin := t.TempDir()
		writeFakePrRelay(t, bin, "exit 3")
		_, _, code, _ := runPluginSh(t, bin+":/usr/bin:/bin", "status")
		if code != 3 {
			t.Errorf("exit code = %d, want 3", code)
		}
	})
}
