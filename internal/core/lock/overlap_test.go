package lock

import "testing"

func TestLiteralPrefix(t *testing.T) {
	cases := []struct {
		name    string
		pattern string
		want    string
	}{
		{"no wildcard", "src/auth/config.go", "src/auth/config.go"},
		{"star wildcard", "src/auth/**", "src/auth/"},
		{"root wildcard", "**", ""},
		{"question mark", "src/a?c", "src/a"},
		{"bracket class", "src/[ab]c", "src/"},
		{"brace group", "src/{a,b}/**", "src/"},
		{"escape character", `src/\*x`, "src/"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := literalPrefix(tc.pattern)
			if got != tc.want {
				t.Fatalf("literalPrefix(%q) = %q, want %q", tc.pattern, got, tc.want)
			}
		})
	}
}

func TestOverlaps(t *testing.T) {
	cases := []struct {
		name string
		a, b string
		want bool
	}{
		{"identical patterns", "src/auth/**", "src/auth/**", true},
		{"prefix contains other", "src/**", "src/auth/**", true},
		{"disjoint siblings", "src/auth/**", "src/billing/**", false},
		{"identical no-wildcard literals", "src/auth/config.go", "src/auth/config.go", true},
		{"distinct no-wildcard literals", "src/auth/config.go", "src/auth/other.go", false},
		{"root wildcard vs pattern", "**", "src/anything/**", true},
		{"root wildcard vs literal", "**", "src/auth/config.go", true},
		{"accepted false positive prefix overlap", "src/auth", "src/authz", true},
		{"disjoint top-level dirs", "docs/", "src/", false},
		{"star suffix vs deep literal", "src/*.go", "src/x/y.go", true},
		{"brace group vs sibling literal", "src/{a,b}/**", "src/c", true},
		{"escaped wildcard prefix vs literal", `src/\*x`, "src/q", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := overlaps(tc.a, tc.b)
			if got != tc.want {
				t.Fatalf("overlaps(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
			}
			// overlap must be symmetric.
			if rev := overlaps(tc.b, tc.a); rev != tc.want {
				t.Fatalf("overlaps(%q, %q) = %v, want %v (symmetry check)", tc.b, tc.a, rev, tc.want)
			}
		})
	}
}

func TestConflicts(t *testing.T) {
	base := func(pattern string, mode Mode) Request {
		return Request{Session: "s", Pattern: pattern, Mode: mode}
	}

	cases := []struct {
		name string
		a, b Request
		want bool
	}{
		{
			name: "overlapping exclusive vs shared conflicts",
			a:    base("src/auth/**", Exclusive),
			b:    base("src/auth/**", Shared),
			want: true,
		},
		{
			name: "overlapping shared vs shared does not conflict",
			a:    base("src/auth/**", Shared),
			b:    base("src/auth/**", Shared),
			want: false,
		},
		{
			name: "non-overlapping exclusive vs exclusive does not conflict",
			a:    base("src/a/**", Exclusive),
			b:    base("src/b/**", Exclusive),
			want: false,
		},
		{
			name: "overlapping exclusive vs exclusive conflicts",
			a:    base("src/auth/**", Exclusive),
			b:    base("src/auth/**", Exclusive),
			want: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := conflicts(tc.a, tc.b); got != tc.want {
				t.Fatalf("conflicts(%+v, %+v) = %v, want %v", tc.a, tc.b, got, tc.want)
			}
		})
	}
}
