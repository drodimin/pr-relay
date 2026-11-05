// Package daemon hosts pr-relay's single per-user background process: the
// poller engine, the delivery manager, the lifecycle ticker and the control
// server, all in one process.
//
// The daemon runs with stdio on /dev/null, so its diagnostics go to a
// rotating file log (OpenLog) and, for panics and fatal runtime errors, to a
// crash file (SetCrash). Both files are mode 0600 in a 0700 directory. The
// daemon never logs tokens or PR text, only keys and pane IDs (T-04-16).
package daemon

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime/debug"
	"sync"

	"gopkg.in/natefinch/lumberjack.v2"
)

const (
	logFileName   = "daemon.log"
	crashFileName = "crash.log"
	maxBackups    = 3
)

// maxSizeMB is the rotation threshold in megabytes. It is a variable only so
// tests can lower it.
var maxSizeMB = 5

// Log is the daemon's rotating file logger.
type Log struct {
	path   string
	w      *lumberjack.Logger
	logger *slog.Logger
	once   sync.Once
	err    error
}

// OpenLog creates dir (tightening it to 0700), opens dir/daemon.log with mode
// 0600 and returns a logger that rotates the file at 5 MB, keeping 3 backups.
func OpenLog(dir string) (*Log, error) {
	if err := ensureDir(dir); err != nil {
		return nil, fmt.Errorf("daemon: open log: %w", err)
	}
	path := filepath.Join(dir, logFileName)

	// lumberjack copies the mode of the current file onto each new file it
	// rotates to, so the active file must already be 0600. Pre-create it and
	// tighten it plus any backups left by an older run.
	if err := touch0600(path); err != nil {
		return nil, fmt.Errorf("daemon: open log: %w", err)
	}
	backups, _ := filepath.Glob(filepath.Join(dir, "daemon-*.log"))
	for _, b := range backups {
		_ = os.Chmod(b, 0o600)
	}

	w := &lumberjack.Logger{
		Filename:   path,
		MaxSize:    maxSizeMB,
		MaxBackups: maxBackups,
		Compress:   false,
	}
	l := &Log{
		path:   path,
		w:      w,
		logger: slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: slog.LevelInfo})),
	}
	l.logger.Info("daemon log opened", "path", path, "pid", os.Getpid())
	return l, nil
}

// Logger returns the slog logger writing to the rotating file.
func (l *Log) Logger() *slog.Logger { return l.logger }

// Path returns the active log file path (dir/daemon.log).
func (l *Log) Path() string { return l.path }

// Close closes the log file. It is safe to call more than once.
func (l *Log) Close() error {
	l.once.Do(func() { l.err = l.w.Close() })
	return l.err
}

// SetCrash creates dir (0700), opens dir/crash.log (0600, append) and directs
// the Go runtime's panic and fatal-error output to it with
// debug.SetCrashOutput. The caller keeps the file open for the life of the
// process. The runtime duplicates the descriptor, so closing it is harmless.
func SetCrash(dir string) (*os.File, error) {
	if err := ensureDir(dir); err != nil {
		return nil, fmt.Errorf("daemon: crash file: %w", err)
	}
	path := filepath.Join(dir, crashFileName)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("daemon: crash file: open: %w", err)
	}
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("daemon: crash file: chmod: %w", err)
	}
	if err := debug.SetCrashOutput(f, debug.CrashOptions{}); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("daemon: crash file: set output: %w", err)
	}
	return f, nil
}

func ensureDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("chmod dir: %w", err)
	}
	return nil
}

func touch0600(path string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("create: %w", err)
	}
	cerr := f.Chmod(0o600)
	if err := f.Close(); err != nil && cerr == nil {
		cerr = err
	}
	if cerr != nil {
		return fmt.Errorf("chmod: %w", cerr)
	}
	return nil
}
