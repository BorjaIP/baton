package proto

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// EnvSocket is the environment variable that overrides the default socket
// path.
const EnvSocket = "BATON_SOCK"

// MaxSocketPath is the maximum byte length accepted for a resolved socket
// path. It is chosen to stay portable across platforms: macOS's sockaddr_un
// sun_path is 104 bytes including the trailing NUL, Linux's is 108.
const MaxSocketPath = 103

// Paths holds every filesystem location derived from the resolved socket
// path: the containing directory, the socket itself, the single-instance
// flock file, and the autostart log file.
type Paths struct {
	Dir    string
	Socket string
	Lock   string
	Log    string
}

// ResolvePaths resolves the daemon's socket path from getenv(EnvSocket) if
// set, otherwise filepath.Join(home, ".baton", "baton.sock"). It creates the
// containing directory with mode 0700 if it does not already exist, and
// derives the lock and log file paths from the socket's base name.
func ResolvePaths(getenv func(string) string, home string) (Paths, error) {
	socket := getenv(EnvSocket)
	if socket == "" {
		socket = filepath.Join(home, ".baton", "baton.sock")
	} else if !filepath.IsAbs(socket) {
		return Paths{}, fmt.Errorf("proto: %s must be an absolute path, got %q", EnvSocket, socket)
	}

	if len(socket) > MaxSocketPath {
		return Paths{}, fmt.Errorf("proto: socket path %q exceeds maximum length %d", socket, MaxSocketPath)
	}

	dir := filepath.Dir(socket)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return Paths{}, fmt.Errorf("proto: create socket directory %q: %w", dir, err)
	}

	base := strings.TrimSuffix(socket, ".sock")
	return Paths{
		Dir:    dir,
		Socket: socket,
		Lock:   base + ".lock",
		Log:    base + ".log",
	}, nil
}
