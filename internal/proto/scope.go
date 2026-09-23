package proto

import (
	"fmt"
	"path"
	"path/filepath"
	"strings"
)

// ValidateScope validates a repo-scoped lock request's root and pattern.
// root must be a non-empty, absolute, already-clean path with no NUL byte.
// pattern must be non-empty, NUL-free, not absolute, already path.Clean, and
// must not be "." or ".." or escape the root via a "../" prefix. Wildcard
// characters are accepted verbatim; ValidateScope performs no further
// transformation of either argument. It is called both by ctl (before
// sending) and by the server (on every request, as defense in depth).
func ValidateScope(root, pattern string) error {
	if root == "" {
		return fmt.Errorf("proto: root must not be empty")
	}
	if strings.ContainsRune(root, 0) {
		return fmt.Errorf("proto: root must not contain a NUL byte")
	}
	if !filepath.IsAbs(root) {
		return fmt.Errorf("proto: root %q must be an absolute path", root)
	}
	if filepath.Clean(root) != root {
		return fmt.Errorf("proto: root %q must already be filepath.Clean", root)
	}

	if pattern == "" {
		return fmt.Errorf("proto: pattern must not be empty")
	}
	if strings.ContainsRune(pattern, 0) {
		return fmt.Errorf("proto: pattern must not contain a NUL byte")
	}
	if path.IsAbs(pattern) {
		return fmt.Errorf("proto: pattern %q must be repo-relative, not absolute", pattern)
	}
	if path.Clean(pattern) != pattern {
		return fmt.Errorf("proto: pattern %q must already be path.Clean", pattern)
	}
	if pattern == "." {
		return fmt.Errorf("proto: pattern must not be \".\"")
	}
	if pattern == ".." || strings.HasPrefix(pattern, "../") {
		return fmt.Errorf("proto: pattern %q must not escape the repository root", pattern)
	}

	return nil
}
