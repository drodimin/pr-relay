//go:build unix

// Command setsid-probe answers spike S5: does a child that the parent
// re-execs with Setsid survive `herdr session stop` on the session that
// launched it?
//
// With no arguments it re-execs itself as `setsid-probe child` in a new
// session (stdin from /dev/null, stdout/stderr to spikes/out/setsid-probe.log),
// prints the child PID and exits. In child mode it appends
//
//	heartbeat <unix-ts> pid=<pid> ppid=<ppid> sid=<sid>
//
// to spikes/out/setsid-heartbeat.txt once a second for at most 15 minutes.
//
// The output directory is resolved relative to the built binary
// (spikes/setsid-probe/setsid-probe -> spikes/out), so build it with
// `go build -o spikes/setsid-probe/setsid-probe ./spikes/setsid-probe`
// rather than using `go run`.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

const maxLifetime = 15 * time.Minute

func main() {
	outDir, err := outputDir()
	if err != nil {
		fmt.Fprintln(os.Stderr, "setsid-probe:", err)
		os.Exit(1)
	}
	if len(os.Args) > 1 && os.Args[1] == "child" {
		if err := runChild(outDir); err != nil {
			fmt.Fprintln(os.Stderr, "setsid-probe child:", err)
			os.Exit(1)
		}
		return
	}
	if err := spawn(outDir); err != nil {
		fmt.Fprintln(os.Stderr, "setsid-probe:", err)
		os.Exit(1)
	}
}

// outputDir returns <dir of executable>/../out, creating it if needed.
func outputDir() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("resolve executable: %w", err)
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return "", fmt.Errorf("resolve executable symlinks: %w", err)
	}
	dir := filepath.Join(filepath.Dir(exe), "..", "out")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create output dir: %w", err)
	}
	return filepath.Clean(dir), nil
}

func spawn(outDir string) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve executable: %w", err)
	}
	devNull, err := os.Open(os.DevNull)
	if err != nil {
		return fmt.Errorf("open %s: %w", os.DevNull, err)
	}
	defer func() { _ = devNull.Close() }()

	logPath := filepath.Join(outDir, "setsid-probe.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("open log: %w", err)
	}
	defer func() { _ = logFile.Close() }()

	cmd := exec.Command(exe, "child")
	cmd.Stdin = devNull
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start child: %w", err)
	}
	fmt.Printf("child pid=%d (parent pid=%d sid=%d)\n", cmd.Process.Pid, os.Getpid(), sid())
	fmt.Printf("heartbeats: %s\n", filepath.Join(outDir, "setsid-heartbeat.txt"))
	// Do not Wait: the parent exits and the child is reparented.
	return cmd.Process.Release()
}

func runChild(outDir string) error {
	path := filepath.Join(outDir, "setsid-heartbeat.txt")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("open heartbeat file: %w", err)
	}
	defer func() { _ = f.Close() }()

	deadline := time.Now().Add(maxLifetime)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		line := fmt.Sprintf("heartbeat %d pid=%d ppid=%d sid=%d\n",
			time.Now().Unix(), os.Getpid(), os.Getppid(), sid())
		if _, err := f.WriteString(line); err != nil {
			return fmt.Errorf("write heartbeat: %w", err)
		}
		if time.Now().After(deadline) {
			_, _ = fmt.Fprintf(f, "exit %d max lifetime reached\n", time.Now().Unix())
			return nil
		}
		<-ticker.C
	}
}

func sid() int {
	s, err := syscall.Getsid(0)
	if err != nil {
		return -1
	}
	return s
}
