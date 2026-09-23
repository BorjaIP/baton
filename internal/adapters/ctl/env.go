// Package ctl implements "baton ctl": the human- and script-facing lock CLI
// surface. It normalizes patterns, resolves the repository root, applies
// documented defaults, formats human and --json output, and defines the
// exit-code contract. Per design §4 layering, it imports only
// internal/client, internal/proto, and internal/messages (plus stdlib and
// oklog/ulid/v2) — never internal/server or internal/core/lock directly.
package ctl

import (
	"context"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/BorjaIP/baton/internal/client"
	"github.com/BorjaIP/baton/internal/proto"
)

// API is the subset of *client.Client that ctl depends on, so tests can
// substitute a fake daemon without a real socket.
type API interface {
	Status(context.Context, proto.StatusRequest) (proto.StatusResponse, error)
	LockAcquire(context.Context, proto.AcquireRequest) (proto.AcquireResponse, error)
	LockRelease(context.Context, proto.ReleaseRequest) (proto.ReleaseResponse, error)
	LockRenew(context.Context, proto.RenewRequest) (proto.RenewResponse, error)
	Close() error
}

// Env is ctl's injected environment: everything that touches the OS, git,
// or the daemon connection, so Run and its helpers stay testable.
type Env struct {
	Stdout, Stderr io.Writer
	Getenv         func(string) string
	Getwd          func() (string, error)
	GitTopLevel    func(ctx context.Context, dir string) (string, error) // nil => exec git
	Dial           func(context.Context, client.Options) (API, error)    // nil => client.Dial (Autostart:true)
}

// DefaultEnv returns an Env wired to the real OS, git, and daemon dial.
func DefaultEnv(stdout, stderr io.Writer) Env {
	return Env{
		Stdout:      stdout,
		Stderr:      stderr,
		Getenv:      os.Getenv,
		Getwd:       os.Getwd,
		GitTopLevel: execGitTopLevel,
		Dial:        dialAPI,
	}
}

// dialAPI adapts client.Dial to the API interface expected by ctl.
func dialAPI(ctx context.Context, o client.Options) (API, error) {
	return client.Dial(ctx, o)
}

// execGitTopLevel runs `git rev-parse --show-toplevel` with a fixed argv, no
// shell, cmd.Dir = dir, and a 2s timeout, per design ADR-12 / the
// threat-matrix "Git repository selection" boundary.
func execGitTopLevel(ctx context.Context, dir string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--show-toplevel")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
