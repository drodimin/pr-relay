// Package autostart is the CLI side of the daemon lifecycle: connect to the
// running daemon, or spawn exactly one (D-01, D-19), restart it once when
// its version differs from the CLI's (D-17, DMN-07), and stop it.
//
// The spawned daemon reports startup success or failure on a ready pipe
// passed as fd 3 (EnvReadyFD), so a startup error such as a newer state
// schema reaches the CLI as the daemon's own message (Pitfall 8).
package autostart

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/drodimin/pr-relay/internal/paths"
)

// EnvReadyFD names the environment variable that tells a spawned daemon the
// ready pipe is open on that fd. Only "3" is ever set.
const EnvReadyFD = "PR_RELAY_READY_FD"

// readyFD is the fd number of the ready pipe in the spawned daemon
// (cmd.ExtraFiles[0]).
const readyFD = 3

// SignalReady is called once by the daemon after it has started (startErr
// nil, listening) or failed to start. It writes "ok" or "err MSG" to the
// ready pipe and closes it. It is a no-op when the daemon was not spawned by
// autostart (EnvReadyFD unset), for example `pr-relay daemon` in a terminal.
func SignalReady(startErr error) {
	if os.Getenv(EnvReadyFD) != "3" {
		return
	}
	// Nothing the daemon spawns later may inherit the variable.
	_ = os.Unsetenv(EnvReadyFD)
	f := os.NewFile(readyFD, "ready")
	if f == nil {
		return
	}
	defer f.Close()
	msg := "ok\n"
	if startErr != nil {
		msg = "err " + oneLine(startErr.Error()) + "\n"
	}
	_, _ = io.WriteString(f, msg)
}

// readReady waits up to timeout for the daemon's ready line on r.
func readReady(r *os.File, timeout time.Duration) error {
	_ = r.SetReadDeadline(time.Now().Add(timeout))
	line, err := bufio.NewReader(io.LimitReader(r, 4096)).ReadString('\n')
	line = strings.TrimRight(line, "\r\n")
	if err != nil && line == "" {
		if errors.Is(err, os.ErrDeadlineExceeded) {
			return fmt.Errorf("daemon did not become ready within %s", timeout)
		}
		return fmt.Errorf("daemon exited during startup; see %s", logHint())
	}
	switch {
	case line == "ok":
		return nil
	case strings.HasPrefix(line, "err "):
		return errors.New(strings.TrimPrefix(line, "err "))
	default:
		return fmt.Errorf("daemon sent an unexpected ready line; see %s", logHint())
	}
}

// logHint names where the daemon log lives, for startup failure messages.
func logHint() string {
	dir, err := paths.LogDir()
	if err != nil {
		return "the pr-relay daemon log"
	}
	return "the daemon log in " + dir
}

func oneLine(s string) string {
	s = strings.ReplaceAll(s, "\r", " ")
	return strings.ReplaceAll(s, "\n", " ")
}
