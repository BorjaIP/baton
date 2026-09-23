package lock

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain ensures no goroutine leaks across the package's test suite,
// enforcing the "no table-owned goroutines" requirement.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
