package ctl

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/BorjaIP/baton/internal/proto"
)

// ResolveRoot resolves the repository root per design ADR-12: symlink-
// resolved cwd, then `git rev-parse --show-toplevel` (accepted only if its
// output is an absolute, existing, already-clean directory), then the
// nearest ancestor containing a .git entry (directory or file, covering
// worktrees and submodules), then cwd itself.
func ResolveRoot(ctx context.Context, env Env) (string, error) {
	cwd, err := env.Getwd()
	if err != nil {
		return "", fmt.Errorf("ctl: resolve cwd: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(cwd); err == nil {
		cwd = resolved
	}

	if env.GitTopLevel != nil {
		if out, err := env.GitTopLevel(ctx, cwd); err == nil {
			out = strings.TrimSpace(out)
			if filepath.IsAbs(out) && filepath.Clean(out) == out {
				if info, statErr := os.Stat(out); statErr == nil && info.IsDir() {
					return out, nil
				}
			}
		}
	}

	dir := cwd
	for {
		if _, err := os.Lstat(filepath.Join(dir, ".git")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}

	return cwd, nil
}

// NormalizePattern normalizes a raw, user-supplied pattern relative to root
// per design ADR-11/ADR-13, then validates it with proto.ValidateScope.
func NormalizePattern(root, raw string) (string, error) {
	if raw == "" {
		return "", fmt.Errorf("ctl: pattern must not be empty")
	}
	if strings.ContainsRune(raw, 0) {
		return "", fmt.Errorf("ctl: pattern must not contain a NUL byte")
	}

	p := filepath.ToSlash(raw)

	if filepath.IsAbs(raw) {
		rel, err := filepath.Rel(root, filepath.Clean(raw))
		if err != nil {
			return "", fmt.Errorf("ctl: pattern %q is not relative to root %q: %w", raw, root, err)
		}
		p = filepath.ToSlash(rel)
	}

	for strings.HasPrefix(p, "./") {
		p = strings.TrimPrefix(p, "./")
	}
	p = path.Clean(p)

	if p == "." {
		return "", fmt.Errorf("ctl: pattern must not be \".\"; use \"**\" to match the whole repository")
	}
	if p == ".." || strings.HasPrefix(p, "../") {
		return "", fmt.Errorf("ctl: pattern %q escapes the repository root", raw)
	}

	if err := proto.ValidateScope(root, p); err != nil {
		return "", fmt.Errorf("ctl: %w", err)
	}

	return p, nil
}
