package ghclient

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/cli/go-gh/v2/pkg/auth"
	"github.com/cli/go-gh/v2/pkg/config"
)

// ErrNoToken means no GitHub token could be resolved from env, gh's config
// file or the gh keyring fallback.
var ErrNoToken = errors.New("ghclient: no GitHub token found (run gh auth login)")

// DefaultTokenTimeout bounds the `gh auth token` keyring fallback.
const DefaultTokenTimeout = 10 * time.Second

// ghWaitDelay bounds how long Token waits for the gh subprocess's pipes to
// close after it has been killed (a grandchild may still hold them).
const ghWaitDelay = 500 * time.Millisecond

// TokenSource resolves a GitHub token the way gh does. The zero value is
// ready to use: Host defaults to github.com and Timeout to
// DefaultTokenTimeout. GhPath, when set, overrides GH_PATH and PATH lookup.
type TokenSource struct {
	Host    string
	Timeout time.Duration
	GhPath  string
}

func (t *TokenSource) host() string {
	if t.Host == "" {
		return "github.com"
	}
	return t.Host
}

// Token returns a token and its source (GH_TOKEN, GITHUB_TOKEN, oauth_token
// or gh). It reloads gh's config files first, so a rewritten hosts.yml is
// picked up. It returns an error wrapping ErrNoToken when nothing is found.
// The token value never appears in a returned error.
func (t *TokenSource) Token(ctx context.Context) (string, string, error) {
	host := t.host()
	// A reload failure (for example a malformed hosts.yml) leaves the
	// previously cached config in place; env tokens and the gh fallback
	// still work, so it is not fatal here.
	_ = config.Reload(nil)
	if tok, src := auth.TokenFromEnvOrConfig(host); tok != "" {
		return tok, src, nil
	}

	gh := t.GhPath
	if gh == "" {
		gh = os.Getenv("GH_PATH")
	}
	if gh == "" {
		p, err := exec.LookPath("gh")
		if err != nil {
			return "", "", ErrNoToken
		}
		gh = p
	}

	timeout := t.Timeout
	if timeout <= 0 {
		timeout = DefaultTokenTimeout
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, gh, "auth", "token", "--secure-storage", "--hostname", host)
	cmd.Stdin = nil
	cmd.Stderr = nil // discarded: stderr is never surfaced
	cmd.WaitDelay = ghWaitDelay
	out, err := cmd.Output()
	if err != nil {
		if cctx.Err() != nil {
			return "", "", fmt.Errorf("%w: gh auth token timed out after %v", ErrNoToken, timeout)
		}
		// Wrap only the exec error (exit status or start failure); the
		// command output is never included.
		return "", "", fmt.Errorf("%w: gh auth token: %w", ErrNoToken, sanitizeExecErr(err))
	}
	tok := strings.TrimSpace(string(out))
	if tok == "" {
		return "", "", ErrNoToken
	}
	return tok, "gh", nil
}

// sanitizeExecErr drops any captured output an *exec.ExitError carries, so
// wrapping it can never surface subprocess output.
func sanitizeExecErr(err error) error {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return errors.New(ee.ProcessState.String())
	}
	return err
}
