package proto

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolvePaths_DefaultWhenEnvUnset(t *testing.T) {
	home := t.TempDir()
	getenv := func(string) string { return "" }

	paths, err := ResolvePaths(getenv, home)
	if err != nil {
		t.Fatalf("ResolvePaths returned error: %v", err)
	}

	wantSocket := filepath.Join(home, ".baton", "baton.sock")
	if paths.Socket != wantSocket {
		t.Fatalf("Socket = %q, want %q", paths.Socket, wantSocket)
	}

	info, err := os.Stat(paths.Dir)
	if err != nil {
		t.Fatalf("Dir was not created: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("Dir %q is not a directory", paths.Dir)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Fatalf("Dir mode = %o, want 0700", perm)
	}
}

func TestResolvePaths_EnvOverrideHonored(t *testing.T) {
	base := t.TempDir()
	custom := filepath.Join(base, "custom-dir", "baton.sock")
	getenv := func(k string) string {
		if k == EnvSocket {
			return custom
		}
		return ""
	}

	paths, err := ResolvePaths(getenv, "/should/not/be/used")
	if err != nil {
		t.Fatalf("ResolvePaths returned error: %v", err)
	}
	if paths.Socket != custom {
		t.Fatalf("Socket = %q, want %q", paths.Socket, custom)
	}

	info, err := os.Stat(filepath.Dir(custom))
	if err != nil {
		t.Fatalf("custom dir was not created: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Fatalf("custom dir mode = %o, want 0700", perm)
	}
}

func TestResolvePaths_DerivesLockAndLog(t *testing.T) {
	home := t.TempDir()
	paths, err := ResolvePaths(func(string) string { return "" }, home)
	if err != nil {
		t.Fatalf("ResolvePaths returned error: %v", err)
	}

	base := strings.TrimSuffix(paths.Socket, ".sock")
	if paths.Lock != base+".lock" {
		t.Fatalf("Lock = %q, want %q", paths.Lock, base+".lock")
	}
	if paths.Log != base+".log" {
		t.Fatalf("Log = %q, want %q", paths.Log, base+".log")
	}
}

func TestResolvePaths_RelativeSocketRejected(t *testing.T) {
	getenv := func(k string) string {
		if k == EnvSocket {
			return "relative/baton.sock"
		}
		return ""
	}

	if _, err := ResolvePaths(getenv, t.TempDir()); err == nil {
		t.Fatal("expected error for relative BATON_SOCK, got nil")
	}
}

func TestResolvePaths_TooLongSocketRejected(t *testing.T) {
	long := "/" + strings.Repeat("a", MaxSocketPath) + "/baton.sock"
	getenv := func(k string) string {
		if k == EnvSocket {
			return long
		}
		return ""
	}

	if _, err := ResolvePaths(getenv, t.TempDir()); err == nil {
		t.Fatal("expected error for over-long BATON_SOCK, got nil")
	}
}
