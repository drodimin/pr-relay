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
const pluginMissingMsg = "pr-relay not found on PATH; install via go install github.com/drodimin/pr-relay/cmd/pr-relay@latest, or a release download"

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
	Panes []struct {
		ID        string   `toml:"id"`
		Title     string   `toml:"title"`
		Placement string   `toml:"placement"`
		Command   []string `toml:"command"`
	} `toml:"panes"`
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
		"status":        {"--open", "status-view"},
		"list-all":      {"--open", "list-view"},
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

	// Action stdout only reaches herdr's plugin log (05-07 spike), so status
	// and list-all open these popup panes instead.
	wantPanes := map[string][]string{
		"status-view": {"sh", "run.sh", "--hold", "status"},
		"list-view":   {"sh", "run.sh", "--hold", "ls", "--all"},
	}
	if len(m.Panes) != len(wantPanes) {
		t.Fatalf("panes = %d, want %d", len(m.Panes), len(wantPanes))
	}
	gotPanes := map[string][]string{}
	for _, p := range m.Panes {
		if strings.Contains(p.ID, ".") {
			t.Errorf("pane id %q must not contain '.'", p.ID)
		}
		if p.Title == "" {
			t.Errorf("pane %q has empty title", p.ID)
		}
		if p.Placement != "popup" {
			t.Errorf("pane %q placement = %q, want popup", p.ID, p.Placement)
		}
		gotPanes[p.ID] = p.Command
	}
	if !reflect.DeepEqual(gotPanes, wantPanes) {
		t.Errorf("panes = %v, want %v", gotPanes, wantPanes)
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
	return runPluginShEnv(t, []string{"PATH=" + path}, "", args...)
}

// runPluginShEnv is runPluginSh with an explicit full env and stdin.
func runPluginShEnv(t *testing.T, env []string, stdin string, args ...string) (stdout, stderr string, code int, workDir string) {
	t.Helper()
	runSh, err := filepath.Abs(pluginRunShPath)
	if err != nil {
		t.Fatalf("abs run.sh: %v", err)
	}
	workDir = t.TempDir()
	cmd := exec.Command("/bin/sh", append([]string{runSh}, args...)...)
	cmd.Env = env
	cmd.Dir = workDir
	cmd.Stdin = strings.NewReader(stdin)
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

	t.Run("hold", func(t *testing.T) {
		bin := t.TempDir()
		writeFakePrRelay(t, bin, `printf '%s\n' "$@"`)
		stdout, stderr, code, _ := runPluginShEnv(t, []string{"PATH=" + bin + ":/usr/bin:/bin"}, "\n", "--hold", "ls", "--all")
		if code != 0 {
			t.Fatalf("exit code = %d, stderr = %q", code, stderr)
		}
		if want := "ls\n--all\npress Enter to close\n"; stdout != want {
			t.Errorf("stdout = %q, want %q", stdout, want)
		}
	})

	t.Run("hold-missing", func(t *testing.T) {
		empty := t.TempDir()
		stdout, stderr, code, _ := runPluginShEnv(t, []string{"PATH=" + empty}, "\n", "--hold", "status")
		if code != 0 {
			t.Errorf("exit code = %d, want 0", code)
		}
		if stderr != pluginMissingMsg+"\n" {
			t.Errorf("stderr = %q, want %q", stderr, pluginMissingMsg+"\n")
		}
		if !strings.HasSuffix(stdout, "press Enter to close\n") {
			t.Errorf("stdout = %q, want suffix %q", stdout, "press Enter to close\n")
		}
	})

	t.Run("open", func(t *testing.T) {
		bin := t.TempDir()
		fake := filepath.Join(bin, "fake-herdr")
		if err := os.WriteFile(fake, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\"\n"), 0o755); err != nil {
			t.Fatalf("write fake herdr: %v", err)
		}
		env := []string{"PATH=/usr/bin:/bin", "HERDR_BIN_PATH=" + fake}
		stdout, stderr, code, _ := runPluginShEnv(t, env, "", "--open", "status-view")
		if code != 0 {
			t.Fatalf("exit code = %d, stderr = %q", code, stderr)
		}
		if want := "plugin\npane\nopen\n--plugin\npr-relay\n--entrypoint\nstatus-view\n"; stdout != want {
			t.Errorf("stdout = %q, want %q", stdout, want)
		}
	})

	t.Run("open-fallback", func(t *testing.T) {
		bin := t.TempDir()
		if err := os.WriteFile(filepath.Join(bin, "herdr"), []byte("#!/bin/sh\necho fallback \"$@\"\n"), 0o755); err != nil {
			t.Fatalf("write fake herdr: %v", err)
		}
		stdout, stderr, code, _ := runPluginShEnv(t, []string{"PATH=" + bin + ":/usr/bin:/bin"}, "", "--open", "list-view")
		if code != 0 {
			t.Fatalf("exit code = %d, stderr = %q", code, stderr)
		}
		if want := "fallback plugin pane open --plugin pr-relay --entrypoint list-view\n"; stdout != want {
			t.Errorf("stdout = %q, want %q", stdout, want)
		}
	})
}
