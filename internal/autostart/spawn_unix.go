//go:build unix

package autostart

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// errSpawnSandbox is the message for EPERM/EACCES from starting the daemon
// (Pitfall 12).
const errSpawnSandbox = "cannot start the pr-relay daemon (operation not permitted); your agent sandbox may block process spawning"

// spawn starts exe args as a detached daemon in its own session and waits up
// to timeout for its ready line. The child gets /dev/null for stdin, stdout
// and stderr, never the CLI's own streams: an agent tool that captures the
// CLI's output would otherwise hang until the daemon exits (Pitfall 1). The
// only inherited fd is the ready pipe; every other Go fd is CLOEXEC. spawn
// never waits for the child, which outlives the CLI.
func spawn(ctx context.Context, exe string, args []string, env []string, timeout time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	devnull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("autostart: open %s: %w", os.DevNull, err)
	}
	defer devnull.Close()
	r, w, err := os.Pipe()
	if err != nil {
		return fmt.Errorf("autostart: ready pipe: %w", err)
	}
	defer r.Close()

	cmd := exec.Command(exe, args...)
	cmd.Dir = "/"
	cmd.Stdin = devnull
	cmd.Stdout = devnull
	cmd.Stderr = devnull
	cmd.ExtraFiles = []*os.File{w}
	cmd.Env = append(withoutReadyFD(env), EnvReadyFD+"=3")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	err = cmd.Start()
	_ = w.Close()
	if err != nil {
		if errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES) {
			return errors.New(errSpawnSandbox)
		}
		return fmt.Errorf("autostart: start daemon: %w", err)
	}
	defer func() { _ = cmd.Process.Release() }()

	if dl, ok := ctx.Deadline(); ok {
		if left := time.Until(dl); left < timeout {
			timeout = max(left, time.Millisecond)
		}
	}
	return readReady(r, timeout)
}

// withoutReadyFD returns env minus any EnvReadyFD entry.
func withoutReadyFD(env []string) []string {
	out := make([]string, 0, len(env)+1)
	for _, kv := range env {
		if strings.HasPrefix(kv, EnvReadyFD+"=") {
			continue
		}
		out = append(out, kv)
	}
	return out
}
