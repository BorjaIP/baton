package server

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/gofrs/flock"
)

func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "bt")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func TestTryLockInstance_SecondCallerGetsErrAlreadyRunning(t *testing.T) {
	dir := shortTempDir(t)
	lockPath := filepath.Join(dir, "b.lock")

	winner := flock.New(lockPath)
	if err := tryLockInstance(winner); err != nil {
		t.Fatalf("first tryLockInstance: %v", err)
	}
	defer winner.Unlock()

	loser := flock.New(lockPath)
	err := tryLockInstance(loser)
	if !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("second tryLockInstance error = %v, want ErrAlreadyRunning", err)
	}
}

func TestRemoveStaleSocket_RemovesExistingSocketFile(t *testing.T) {
	dir := shortTempDir(t)
	sockPath := filepath.Join(dir, "b.sock")

	addr, err := net.ResolveUnixAddr("unix", sockPath)
	if err != nil {
		t.Fatalf("ResolveUnixAddr: %v", err)
	}
	ln, err := net.ListenUnix("unix", addr)
	if err != nil {
		t.Fatalf("ListenUnix: %v", err)
	}
	ln.SetUnlinkOnClose(false)
	ln.Close()

	if _, err := os.Lstat(sockPath); err != nil {
		t.Fatalf("expected stale socket file to exist before cleanup: %v", err)
	}

	if err := removeStaleSocket(sockPath); err != nil {
		t.Fatalf("removeStaleSocket: %v", err)
	}
	if _, err := os.Lstat(sockPath); !os.IsNotExist(err) {
		t.Fatalf("expected socket file removed, Lstat err = %v", err)
	}
}

func TestRemoveStaleSocket_NoFilePresentIsNotAnError(t *testing.T) {
	dir := shortTempDir(t)
	sockPath := filepath.Join(dir, "missing.sock")

	if err := removeStaleSocket(sockPath); err != nil {
		t.Fatalf("removeStaleSocket on missing file: %v", err)
	}
}

func TestRemoveStaleSocket_RegularFileIsRejectedAndPreserved(t *testing.T) {
	dir := shortTempDir(t)
	path := filepath.Join(dir, "b.sock")

	if err := os.WriteFile(path, []byte("not a socket"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	err := removeStaleSocket(path)
	if err == nil {
		t.Fatal("removeStaleSocket on a regular file: want error, got nil")
	}

	data, statErr := os.ReadFile(path)
	if statErr != nil {
		t.Fatalf("expected regular file preserved, ReadFile err = %v", statErr)
	}
	if string(data) != "not a socket" {
		t.Fatalf("regular file contents changed: %q", data)
	}
}

func TestListenUnix_BindsWithSocketPermission0600(t *testing.T) {
	dir := shortTempDir(t)
	sockPath := filepath.Join(dir, "b.sock")

	ln, err := listenUnix(sockPath)
	if err != nil {
		t.Fatalf("listenUnix: %v", err)
	}
	defer ln.Close()

	info, err := os.Lstat(sockPath)
	if err != nil {
		t.Fatalf("Lstat: %v", err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		t.Fatalf("expected a socket file at %q, mode = %v", sockPath, info.Mode())
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("socket permission = %o, want 0600", perm)
	}
}

func TestListenUnix_SetsUnlinkOnCloseFalse(t *testing.T) {
	dir := shortTempDir(t)
	sockPath := filepath.Join(dir, "b.sock")

	ln, err := listenUnix(sockPath)
	if err != nil {
		t.Fatalf("listenUnix: %v", err)
	}
	ln.Close()

	// SetUnlinkOnClose(false) means the socket file must still exist after
	// Close; only an explicit os.Remove (the flock winner's shutdown path)
	// removes it.
	if _, err := os.Lstat(sockPath); err != nil {
		t.Fatalf("expected socket file to survive listener Close: %v", err)
	}
}
