// Package paths resolves every file location Askgate uses. Nothing else
// in the codebase may build these paths by hand: tests point ASKGATE_HOME
// at a temp dir and must never touch the real home directory.
package paths

import (
	"os"
	"path/filepath"
)

// Home is the Askgate data directory: $ASKGATE_HOME, else ~/.askgate.
func Home() string {
	if h := os.Getenv("ASKGATE_HOME"); h != "" {
		return h
	}
	u, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "askgate")
	}
	return filepath.Join(u, ".askgate")
}

// UserHome is the user's home directory, used to find agent settings
// (~/.claude, ~/.codex). $ASKGATE_USER_HOME overrides it for tests.
func UserHome() string {
	if h := os.Getenv("ASKGATE_USER_HOME"); h != "" {
		return h
	}
	u, err := os.UserHomeDir()
	if err != nil {
		return os.TempDir()
	}
	return u
}

func Config() string       { return filepath.Join(Home(), "config.yaml") }
func Policy() string       { return filepath.Join(Home(), "policy.yaml") }
func DB() string           { return filepath.Join(Home(), "askgate.db") }
func Keys() string         { return filepath.Join(Home(), "keys") } // VAPID + daemon keys, 0700
func Logs() string         { return filepath.Join(Home(), "logs") }
func TrustedRepos() string { return filepath.Join(Home(), "trusted-repos.yaml") }

// Run is the directory for sockets. It is kept short because Unix socket
// paths are limited to ~104 bytes on macOS.
func Run() string {
	if d := os.Getenv("ASKGATE_RUN_DIR"); d != "" {
		return d
	}
	return filepath.Join(Home(), "run")
}

// Socket is the daemon's Unix socket: the only path for local decisions.
func Socket() string { return filepath.Join(Run(), "askgate.sock") }

// HolderSocket is the socket of one `askgate run` holder.
func HolderSocket(sessionID string) string {
	return filepath.Join(Run(), "h-"+sessionID+".sock")
}

// Ensure creates the data directories with private permissions.
func Ensure() error {
	for _, d := range []string{Home(), Run(), Keys(), Logs()} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return err
		}
		if err := os.Chmod(d, 0o700); err != nil {
			return err
		}
	}
	return nil
}
