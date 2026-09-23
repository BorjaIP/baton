package lock

import "strings"

// wildcardChars are the doublestar-style wildcard characters that terminate
// a pattern's literal prefix. A backslash is included because it introduces
// an escape sequence; treating it as a wildcard boundary only ever shortens
// the computed prefix, which stays conservative (see design ADR-13).
const wildcardChars = `*?[{\`

// literalPrefix returns the substring of p before its first wildcard
// character. A pattern with no wildcard is its own full literal prefix.
func literalPrefix(p string) string {
	if idx := strings.IndexAny(p, wildcardChars); idx != -1 {
		return p[:idx]
	}
	return p
}

// overlaps reports whether two path patterns are conflict candidates using a
// conservative literal-prefix comparison: true if either pattern's literal
// prefix is a prefix of the other's, including exact equality. This
// deliberately prefers false positives over false negatives (design ADR-13).
func overlaps(a, b string) bool {
	pa, pb := literalPrefix(a), literalPrefix(b)
	return strings.HasPrefix(pa, pb) || strings.HasPrefix(pb, pa)
}

// conflicts reports whether two requests contend for the same resource: their
// patterns overlap and at least one of them is Exclusive.
func conflicts(a, b Request) bool {
	return overlaps(a.Pattern, b.Pattern) && (a.Mode == Exclusive || b.Mode == Exclusive)
}
