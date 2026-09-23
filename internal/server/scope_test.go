package server

import "testing"

func TestScopeKey_SplitRoundTrips(t *testing.T) {
	tests := []struct {
		root, pattern string
	}{
		{"/home/user/repo", "src/auth/**"},
		{"/home/user/repo", "**"},
		{"/", "a/b"},
	}
	for _, tt := range tests {
		key := scopeKey(tt.root, tt.pattern)
		root, pattern, ok := splitKey(key)
		if !ok {
			t.Fatalf("splitKey(%q) ok=false", key)
		}
		if root != tt.root || pattern != tt.pattern {
			t.Fatalf("splitKey(%q) = (%q, %q), want (%q, %q)", key, root, pattern, tt.root, tt.pattern)
		}
	}
}

func TestEscapeRoot_PercentEncodesWildcardChars(t *testing.T) {
	root := `/home/u/proj[1]*?{\`
	esc := escapeRoot(root)

	// escapeRoot must make its output fully literal: core/lock's
	// literalPrefix computation must never truncate it before its end.
	if got := literalPrefixTest(esc); got != esc {
		t.Fatalf("escapeRoot(%q) = %q is not fully literal (literalPrefix = %q)", root, esc, got)
	}

	got, ok := unescapeRoot(esc)
	if !ok || got != root {
		t.Fatalf("unescapeRoot(escapeRoot(%q)) = (%q, %v), want (%q, true)", root, got, ok, root)
	}
}

func TestScopeKey_DifferentRootsWithWildcardCharsDoNotOverlap(t *testing.T) {
	// A root containing wildcard/bracket characters must still isolate
	// correctly from a visually similar sibling root.
	key1 := scopeKey("/home/u/proj[1]", "src/**")
	key2 := scopeKey("/home/u/proj2", "src/**")

	if literalPrefixOverlap(key1, key2) {
		t.Fatalf("keys for different roots %q and %q incorrectly overlap", key1, key2)
	}
}

func TestScopeKey_NestedRepoRootNeverOverlaps(t *testing.T) {
	key1 := scopeKey("/a/repo", "src/**")
	key2 := scopeKey("/a/repo/sub", "src/**")

	if literalPrefixOverlap(key1, key2) {
		t.Fatalf("keys for nested repo roots %q and %q incorrectly overlap", key1, key2)
	}
}

func TestScopeKey_SameRootAndPatternOverlap(t *testing.T) {
	key1 := scopeKey("/a/repo", "src/**")
	key2 := scopeKey("/a/repo", "src/**")

	if !literalPrefixOverlap(key1, key2) {
		t.Fatalf("identical keys %q and %q must overlap", key1, key2)
	}
}

// literalPrefixOverlap mirrors core/lock's conservative literal-prefix
// overlap check (design ADR-2), used here only to assert the scope-key
// encoding's isolation properties without importing core/lock's unexported
// helpers.
func literalPrefixOverlap(a, b string) bool {
	pa, pb := literalPrefixTest(a), literalPrefixTest(b)
	return hasPrefixEither(pa, pb)
}

func literalPrefixTest(p string) string {
	for i := 0; i < len(p); i++ {
		switch p[i] {
		case '*', '?', '[', '{', '\\':
			return p[:i]
		}
	}
	return p
}

func hasPrefixEither(a, b string) bool {
	if len(a) <= len(b) {
		return b[:len(a)] == a
	}
	return a[:len(b)] == b
}
