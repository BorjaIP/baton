package ctl_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/BorjaIP/baton/internal/adapters/ctl"
)

func testEnv(t *testing.T, cwd string, gitTopLevel func(ctx context.Context, dir string) (string, error)) ctl.Env {
	t.Helper()
	return ctl.Env{
		Getenv:      func(string) string { return "" },
		Getwd:       func() (string, error) { return cwd, nil },
		GitTopLevel: gitTopLevel,
	}
}

func TestResolveRoot_GitTopLevelWins(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "repoA")
	if err := os.MkdirAll(filepath.Join(root, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	env := testEnv(t, filepath.Join(root, "src"), func(_ context.Context, gitDir string) (string, error) {
		return root, nil
	})

	got, err := ctl.ResolveRoot(context.Background(), env)
	if err != nil {
		t.Fatal(err)
	}
	if got != root {
		t.Fatalf("ResolveRoot = %q, want %q", got, root)
	}
}

func TestResolveRoot_FallsBackToDotGitAncestorWhenGitFails(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "repoB")
	sub := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	env := testEnv(t, sub, func(_ context.Context, gitDir string) (string, error) {
		return "", errors.New("git not found")
	})

	got, err := ctl.ResolveRoot(context.Background(), env)
	if err != nil {
		t.Fatal(err)
	}
	if got != root {
		t.Fatalf("ResolveRoot = %q, want %q", got, root)
	}
}

func TestResolveRoot_DotGitAsFileCoversWorktrees(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "worktree")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".git"), []byte("gitdir: /somewhere/else"), 0o644); err != nil {
		t.Fatal(err)
	}
	env := testEnv(t, root, func(_ context.Context, gitDir string) (string, error) {
		return "", errors.New("no git")
	})

	got, err := ctl.ResolveRoot(context.Background(), env)
	if err != nil {
		t.Fatal(err)
	}
	if got != root {
		t.Fatalf("ResolveRoot = %q, want %q", got, root)
	}
}

func TestResolveRoot_NoRepoAnywhereFallsBackToCwd(t *testing.T) {
	dir := t.TempDir()
	env := testEnv(t, dir, func(_ context.Context, gitDir string) (string, error) {
		return "", errors.New("no git")
	})

	got, err := ctl.ResolveRoot(context.Background(), env)
	if err != nil {
		t.Fatal(err)
	}
	if got != dir {
		t.Fatalf("ResolveRoot = %q, want %q", got, dir)
	}
}

func TestResolveRoot_GarbageGitOutputFallsBack(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "repoC")
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	env := testEnv(t, root, func(_ context.Context, gitDir string) (string, error) {
		return "not-an-absolute-path", nil
	})

	got, err := ctl.ResolveRoot(context.Background(), env)
	if err != nil {
		t.Fatal(err)
	}
	if got != root {
		t.Fatalf("ResolveRoot = %q, want %q (garbage git output must fall back)", got, root)
	}
}

func TestResolveRoot_SymlinkedCwdResolvesConsistently(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}

	env := testEnv(t, link, func(_ context.Context, gitDir string) (string, error) {
		if gitDir != real {
			t.Errorf("git invoked with unresolved dir %q, want resolved %q", gitDir, real)
		}
		return "", errors.New("no git")
	})

	got, err := ctl.ResolveRoot(context.Background(), env)
	if err != nil {
		t.Fatal(err)
	}
	if got != real {
		t.Fatalf("ResolveRoot = %q, want resolved %q", got, real)
	}
}

func TestNormalizePattern(t *testing.T) {
	root := string(filepath.Separator) + filepath.Join("home", "user", "repoA")

	cases := []struct {
		name    string
		raw     string
		want    string
		wantErr bool
	}{
		{name: "leading dot slash stripped", raw: "./src/auth/**", want: "src/auth/**"},
		{name: "os separator form normalized", raw: filepath.Join("src", "auth", "x"), want: "src/auth/x"},
		{name: "absolute inside root becomes relative", raw: filepath.Join(root, "src", "auth", "**"), want: "src/auth/**"},
		{name: "absolute outside root escapes", raw: string(filepath.Separator) + filepath.Join("etc", "**"), wantErr: true},
		{name: "dot rejected", raw: ".", wantErr: true},
		{name: "dot dot rejected", raw: "..", wantErr: true},
		{name: "dot dot prefix rejected", raw: "../x", wantErr: true},
		{name: "empty rejected", raw: "", wantErr: true},
		{name: "NUL rejected", raw: "src/\x00x", wantErr: true},
		{name: "wildcards preserved", raw: "src/**", want: "src/**"},
		{name: "clean quirk accepted", raw: "src/**/../x", want: "src/x"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ctl.NormalizePattern(root, tc.raw)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("NormalizePattern(%q) = %q, want error", tc.raw, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("NormalizePattern(%q) unexpected error: %v", tc.raw, err)
			}
			if got != tc.want {
				t.Fatalf("NormalizePattern(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}
