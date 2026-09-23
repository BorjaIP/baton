package server

import (
	"errors"
	"fmt"
	"net"
	"os"

	"github.com/gofrs/flock"
)

// ErrAlreadyRunning is returned by Listen when another live daemon already
// holds the single-instance flock for the same socket path.
var ErrAlreadyRunning = errors.New("server: another daemon holds the lock")

// tryLockInstance attempts to acquire fl exclusively without blocking. A
// caller that loses the race gets ErrAlreadyRunning, not a raw flock error,
// so it can exit quietly (design ADR-9) without disturbing the winner's
// socket or lock file.
func tryLockInstance(fl *flock.Flock) error {
	ok, err := fl.TryLock()
	if err != nil {
		return fmt.Errorf("server: acquire instance lock: %w", err)
	}
	if !ok {
		return ErrAlreadyRunning
	}
	return nil
}

// removeStaleSocket removes an existing file at path only if it is a Unix
// domain socket left behind by a crashed prior daemon. A missing file is not
// an error. A regular file or directory at path is rejected and left
// untouched (design ADR-9): only the flock winner ever calls this, and only
// a socket-mode file is ever removed.
func removeStaleSocket(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("server: stat socket path %q: %w", path, err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("server: refusing to remove non-socket file at %q", path)
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("server: remove stale socket %q: %w", path, err)
	}
	return nil
}

// listenUnix binds a Unix domain socket listener at path with
// SetUnlinkOnClose(false) (design ADR-9: only an explicit os.Remove during
// shutdown ever deletes the socket file, never an implicit Close) and mode
// 0600.
func listenUnix(path string) (*net.UnixListener, error) {
	addr, err := net.ResolveUnixAddr("unix", path)
	if err != nil {
		return nil, fmt.Errorf("server: resolve socket address %q: %w", path, err)
	}
	ln, err := net.ListenUnix("unix", addr)
	if err != nil {
		return nil, fmt.Errorf("server: listen on %q: %w", path, err)
	}
	ln.SetUnlinkOnClose(false)
	if err := os.Chmod(path, 0o600); err != nil {
		ln.Close()
		return nil, fmt.Errorf("server: chmod socket %q: %w", path, err)
	}
	return ln, nil
}
