package ghclient

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// isolateAuth clears every env var go-gh consults and points GH_CONFIG_DIR
// at a fresh temp dir, so the developer's real gh login never leaks in.
func isolateAuth(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, k := range []string{"GH_TOKEN", "GITHUB_TOKEN", "GH_ENTERPRISE_TOKEN", "GITHUB_ENTERPRISE_TOKEN", "GH_HOST"} {
		t.Setenv(k, "")
	}
	t.Setenv("GH_CONFIG_DIR", dir)
	t.Setenv("GH_PATH", filepath.Join(dir, "no-such-gh"))
	t.Setenv("PATH", "")
	return dir
}

func writeHosts(t *testing.T, dir, token string) {
	t.Helper()
	body := "github.com:\n    oauth_token: " + token + "\n    user: u\n"
	if err := os.WriteFile(filepath.Join(dir, "hosts.yml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// writeScript writes an executable shell script and returns its path.
func writeScript(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "gh")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return p
}

func assertNoSecret(t *testing.T, err error, secrets ...string) {
	t.Helper()
	if err == nil {
		return
	}
	for _, s := range secrets {
		if strings.Contains(err.Error(), s) {
			t.Fatalf("error %q leaks token %q", err.Error(), s)
		}
	}
}

func TestTokenFromEnv(t *testing.T) {
	isolateAuth(t)
	t.Setenv("GH_TOKEN", "abc")
	tok, src, err := (&TokenSource{}).Token(t.Context())
	if err != nil || tok != "abc" || src != "GH_TOKEN" {
		t.Fatalf("got (%q, %q, %v), want (abc, GH_TOKEN, nil)", tok, src, err)
	}
}

func TestTokenFromGithubTokenEnv(t *testing.T) {
	isolateAuth(t)
	t.Setenv("GITHUB_TOKEN", "def")
	tok, src, err := (&TokenSource{}).Token(t.Context())
	if err != nil || tok != "def" || src != "GITHUB_TOKEN" {
		t.Fatalf("got (%q, %q, %v), want (def, GITHUB_TOKEN, nil)", tok, src, err)
	}
}

func TestTokenFromHostsFile(t *testing.T) {
	dir := isolateAuth(t)
	writeHosts(t, dir, "t1")
	ts := &TokenSource{}
	tok, src, err := ts.Token(t.Context())
	if err != nil || tok != "t1" || src != "oauth_token" {
		t.Fatalf("got (%q, %q, %v), want (t1, oauth_token, nil)", tok, src, err)
	}
	// gh auth login rewrites hosts.yml; the next resolution must see it.
	writeHosts(t, dir, "t2")
	tok, _, err = ts.Token(t.Context())
	if err != nil || tok != "t2" {
		t.Fatalf("after rewrite got (%q, %v), want t2", tok, err)
	}
}

func TestTokenGhFallback(t *testing.T) {
	dir := isolateAuth(t)
	if err := os.WriteFile(filepath.Join(dir, "hosts.yml"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GH_PATH", writeScript(t, `echo t3`))
	tok, src, err := (&TokenSource{}).Token(t.Context())
	if err != nil || tok != "t3" || src != "gh" {
		t.Fatalf("got (%q, %q, %v), want (t3, gh, nil)", tok, src, err)
	}
}

func TestTokenGhPathField(t *testing.T) {
	isolateAuth(t)
	ts := &TokenSource{GhPath: writeScript(t, `echo t4`)}
	tok, src, err := ts.Token(t.Context())
	if err != nil || tok != "t4" || src != "gh" {
		t.Fatalf("got (%q, %q, %v), want (t4, gh, nil)", tok, src, err)
	}
}

func TestTokenGhEmptyOutput(t *testing.T) {
	isolateAuth(t)
	t.Setenv("GH_PATH", writeScript(t, `echo "   "`))
	_, _, err := (&TokenSource{}).Token(t.Context())
	if !errors.Is(err, ErrNoToken) {
		t.Fatalf("err = %v, want ErrNoToken", err)
	}
}

func TestTokenGhFailureDoesNotLeakOutput(t *testing.T) {
	isolateAuth(t)
	t.Setenv("GH_PATH", writeScript(t, "echo secret-out\necho secret-err >&2\nexit 3"))
	_, _, err := (&TokenSource{}).Token(t.Context())
	if !errors.Is(err, ErrNoToken) {
		t.Fatalf("err = %v, want ErrNoToken", err)
	}
	assertNoSecret(t, err, "secret-out", "secret-err")
}

func TestTokenGhTimeout(t *testing.T) {
	isolateAuth(t)
	t.Setenv("GH_PATH", writeScript(t, "/bin/sleep 5\necho late"))
	ts := &TokenSource{Timeout: 200 * time.Millisecond}
	start := time.Now()
	_, _, err := ts.Token(t.Context())
	if !errors.Is(err, ErrNoToken) {
		t.Fatalf("err = %v, want ErrNoToken", err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("Token took %v, want < 2s", d)
	}
}

func TestTokenNone(t *testing.T) {
	isolateAuth(t)
	_, src, err := (&TokenSource{}).Token(context.Background())
	if !errors.Is(err, ErrNoToken) {
		t.Fatalf("err = %v, want ErrNoToken", err)
	}
	if src != "" {
		t.Fatalf("source = %q, want empty", src)
	}
}

func TestTokenEnvNotInError(t *testing.T) {
	// A token from env is returned, never wrapped into an error.
	isolateAuth(t)
	t.Setenv("GH_TOKEN", "ghp_secretvalue")
	_, _, err := (&TokenSource{}).Token(t.Context())
	assertNoSecret(t, err, "ghp_secretvalue")
}
