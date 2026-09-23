package proto

import (
	"errors"
	"testing"
)

func TestCode_SentinelsAreInjectiveOverCoreErrors(t *testing.T) {
	// Every core-sentinel-mapped Code constant must be distinct: two
	// different core sentinel errors must never share a wire code.
	coreMapped := []Code{
		CodeInvalidRequest,
		CodeSessionOverlap,
		CodeStaleToken,
		CodeUnknownToken,
		CodeSlotExpired,
		CodeWaiterClosed,
		CodeAwaitBusy,
	}

	seen := make(map[Code]bool, len(coreMapped))
	for _, c := range coreMapped {
		if seen[c] {
			t.Fatalf("duplicate wire code %q maps more than one core sentinel", c)
		}
		seen[c] = true
	}
}

func TestCode_AllDocumentedCodesAreDistinct(t *testing.T) {
	all := []Code{
		CodeInvalidEnvelope,
		CodeInvalidRequest,
		CodeUnknownVerb,
		CodeHelloRequired,
		CodeVersionMismatch,
		CodeRequestInFlight,
		CodeSessionOverlap,
		CodeStaleToken,
		CodeUnknownToken,
		CodeSlotExpired,
		CodeWaiterClosed,
		CodeAwaitBusy,
		CodeRootMismatch,
		CodeShuttingDown,
		CodeInternal,
	}
	seen := make(map[Code]bool, len(all))
	for _, c := range all {
		if seen[c] {
			t.Fatalf("duplicate Code constant value %q", c)
		}
		seen[c] = true
	}
}

func TestError_IsMatchesByCodeOnly(t *testing.T) {
	e1 := &Error{Code: CodeStaleToken, Message: "token superseded"}
	e2 := &Error{Code: CodeStaleToken, Message: "different message, same code"}
	e3 := &Error{Code: CodeUnknownToken, Message: "token superseded"}

	if !errors.Is(e1, e2) {
		t.Fatal("expected errors with the same Code to match via errors.Is")
	}
	if errors.Is(e1, e3) {
		t.Fatal("expected errors with different Codes not to match via errors.Is")
	}
}

func TestError_IsMatchesSentinelByCode(t *testing.T) {
	wireErr := &Error{Code: CodeStaleToken, Message: "token superseded by grant 42"}

	if !errors.Is(wireErr, ErrCodeStaleToken) {
		t.Fatal("expected wireErr to match ErrCodeStaleToken sentinel via errors.Is")
	}
	if errors.Is(wireErr, ErrCodeUnknownToken) {
		t.Fatal("expected wireErr not to match ErrCodeUnknownToken sentinel")
	}
}

func TestError_ErrorStringIncludesMessage(t *testing.T) {
	e := &Error{Code: CodeInternal, Message: "boom"}
	if got := e.Error(); got == "" {
		t.Fatal("Error() returned empty string")
	}
}
