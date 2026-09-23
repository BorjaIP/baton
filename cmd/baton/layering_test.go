//go:build unix

// Layering test: enforces the dependency-direction rules from design §4 by
// inspecting the actual (non-test) import graph via `go list`, rather than
// trusting hand-written comments to stay accurate.
package main

import (
	"bytes"
	"os/exec"
	"strings"
	"testing"
)

const module = "github.com/BorjaIP/baton"

// goList runs `go list <args...>` from the module root (two directories up
// from cmd/baton, where go.mod lives) and returns its trimmed stdout.
func goList(t *testing.T, args ...string) string {
	t.Helper()
	cmd := exec.Command(goBinary(), args...)
	cmd.Dir = "../.."
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		t.Fatalf("go list %v: %v\n%s", args, err, errb.String())
	}
	return strings.TrimSpace(out.String())
}

// listDeps returns pkg's non-test transitive import set (via `go list
// -deps`), excluding pkg itself. `go list -deps` includes the queried
// package in its own output, which is never what "does X import Y" means.
func listDeps(t *testing.T, pkg string) map[string]bool {
	t.Helper()
	out := goList(t, "list", "-deps", pkg)
	set := map[string]bool{}
	for _, l := range strings.Fields(out) {
		if l == pkg {
			continue
		}
		set[l] = true
	}
	return set
}

// allPackages lists every package under ./internal/... and ./cmd/....
func allPackages(t *testing.T) []string {
	t.Helper()
	out := goList(t, "list", "./internal/...", "./cmd/...")
	return strings.Fields(out)
}

// directImports returns pkg's direct (non-transitive), non-test import set.
func directImports(t *testing.T, pkg string) map[string]bool {
	t.Helper()
	out := goList(t, "list", "-f", "{{join .Imports \"\\n\"}}", pkg)
	set := map[string]bool{}
	for _, l := range strings.Split(out, "\n") {
		l = strings.TrimSpace(l)
		if l != "" {
			set[l] = true
		}
	}
	return set
}

// directImportersOf returns every package (among allPackages) that directly
// (non-transitively) imports target.
func directImportersOf(t *testing.T, target string) []string {
	t.Helper()
	var result []string
	for _, p := range allPackages(t) {
		if directImports(t, p)[target] {
			result = append(result, p)
		}
	}
	return result
}

func assertOnlyDirectImporter(t *testing.T, target, wantOnly string) {
	t.Helper()
	got := directImportersOf(t, target)
	if len(got) != 1 || got[0] != wantOnly {
		t.Fatalf("expected only %s to directly import %s, got importers=%v", wantOnly, target, got)
	}
}

func assertNoImportOf(t *testing.T, fromPkg string, forbidden ...string) {
	t.Helper()
	deps := listDeps(t, fromPkg)
	for _, f := range forbidden {
		if deps[f] {
			t.Fatalf("%s must not import %s (layering violation)", fromPkg, f)
		}
	}
}

func TestLayering_OnlyCmdBatonImportsServer(t *testing.T) {
	assertOnlyDirectImporter(t, module+"/internal/server", module+"/cmd/baton")
}

func TestLayering_ServerIsSoleNonTestImporterOfCoreLock(t *testing.T) {
	assertOnlyDirectImporter(t, module+"/internal/core/lock", module+"/internal/server")
}

func TestLayering_CtlImportsOnlyClientProtoMessages(t *testing.T) {
	assertNoImportOf(t, module+"/internal/adapters/ctl",
		module+"/internal/server",
		module+"/internal/core/lock",
	)
}

func TestLayering_ClientImportsOnlyProto(t *testing.T) {
	assertNoImportOf(t, module+"/internal/client",
		module+"/internal/server",
		module+"/internal/core/lock",
		module+"/internal/adapters/ctl",
		module+"/internal/messages",
	)
}

func TestLayering_MessagesAndProtoImportNoInternalPackage(t *testing.T) {
	for _, pkg := range []string{module + "/internal/messages", module + "/internal/proto"} {
		deps := listDeps(t, pkg)
		for d := range deps {
			if strings.HasPrefix(d, module+"/internal/") && d != pkg {
				t.Fatalf("%s must not import internal package %s", pkg, d)
			}
		}
	}
}

func TestLayering_CoreLockImportGraphUnchanged(t *testing.T) {
	deps := listDeps(t, module+"/internal/core/lock")
	for d := range deps {
		if strings.HasPrefix(d, module+"/") {
			t.Fatalf("internal/core/lock must not import any other module package, got %s", d)
		}
	}
}
