package server

import "strings"

// wildcardEscapeChars are the doublestar-style wildcard characters that
// terminate core/lock's literalPrefix computation. escapeRoot percent-encodes
// every occurrence of these (plus '%' itself) so an absolute repository root
// containing one of them can never shorten the literal prefix of a scoped
// key (design ADR-2).
const wildcardEscapeChars = `%*?[{\`

// escapeRoot percent-encodes every byte in root that would otherwise
// terminate core/lock's literal-prefix computation, making the escaped root
// fully literal. Percent-encoding is injective, so escapeRoot(a) ==
// escapeRoot(b) implies a == b.
func escapeRoot(root string) string {
	var b strings.Builder
	b.Grow(len(root))
	for i := 0; i < len(root); i++ {
		c := root[i]
		if strings.IndexByte(wildcardEscapeChars, c) >= 0 {
			b.WriteByte('%')
			const hex = "0123456789ABCDEF"
			b.WriteByte(hex[c>>4])
			b.WriteByte(hex[c&0x0f])
			continue
		}
		b.WriteByte(c)
	}
	return b.String()
}

// unescapeRoot reverses escapeRoot.
func unescapeRoot(esc string) (string, bool) {
	var b strings.Builder
	b.Grow(len(esc))
	for i := 0; i < len(esc); i++ {
		if esc[i] != '%' {
			b.WriteByte(esc[i])
			continue
		}
		if i+2 >= len(esc) {
			return "", false
		}
		hi, ok1 := hexVal(esc[i+1])
		lo, ok2 := hexVal(esc[i+2])
		if !ok1 || !ok2 {
			return "", false
		}
		b.WriteByte(hi<<4 | lo)
		i += 2
	}
	return b.String(), true
}

func hexVal(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	default:
		return 0, false
	}
}

// scopeKey encodes an absolute repository root and a repo-relative pattern
// into the single string core/lock uses as its Request.Pattern (design
// ADR-1/ADR-2). The root is percent-escaped so it is fully literal, then
// joined to pattern with a NUL byte, which cannot appear in either an
// escaped root or a validated (proto.ValidateScope) pattern.
func scopeKey(root, pattern string) string {
	return escapeRoot(root) + "\x00" + pattern
}

// splitKey reverses scopeKey. It reports ok=false if key does not contain
// exactly the expected NUL separator or the escaped root cannot be decoded.
func splitKey(key string) (root, pattern string, ok bool) {
	idx := strings.IndexByte(key, 0)
	if idx < 0 {
		return "", "", false
	}
	root, ok = unescapeRoot(key[:idx])
	if !ok {
		return "", "", false
	}
	return root, key[idx+1:], true
}
