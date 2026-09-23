package messages_test

import (
	"strings"
	"testing"

	"github.com/BorjaIP/baton/internal/messages"
	"github.com/BorjaIP/baton/internal/proto"
)

// usageStrings returns every usage/help string messages exports, labeled by
// name, so tests can assert a property over all of them without repeating
// the list.
func usageStrings() map[string]string {
	return map[string]string{
		"Usage":        messages.Usage,
		"ServeUsage":   messages.ServeUsage,
		"CtlUsage":     messages.CtlUsage,
		"StatusUsage":  messages.StatusUsage,
		"AcquireUsage": messages.AcquireUsage,
		"ReleaseUsage": messages.ReleaseUsage,
		"RenewUsage":   messages.RenewUsage,
	}
}

func TestUsageStringsReferenceFixedProgramName(t *testing.T) {
	for name, s := range usageStrings() {
		if !strings.Contains(s, messages.Program) {
			t.Errorf("%s does not mention program name %q: %q", name, messages.Program, s)
		}
	}
}

func TestUsageStringsNeverMentionAnyOtherName(t *testing.T) {
	// "bt" is the historical alternate symlink name; usage/help text must
	// never say anything but "baton" per the argv[0]-agnostic contract.
	const decoyName = "bt"
	for name, s := range usageStrings() {
		if strings.Contains(strings.ToLower(s), decoyName) && !strings.Contains(s, messages.Program) {
			t.Errorf("%s unexpectedly mentions %q without also mentioning %q", name, decoyName, messages.Program)
		}
		// Never contain a standalone "bt" token that isn't part of "baton".
		for _, word := range strings.Fields(s) {
			trimmed := strings.Trim(word, "`.,:'\"")
			if trimmed == decoyName {
				t.Errorf("%s contains the standalone decoy token %q: %q", name, decoyName, s)
			}
		}
	}
}

func TestProgramIsBaton(t *testing.T) {
	if messages.Program != "baton" {
		t.Fatalf("Program = %q, want %q", messages.Program, "baton")
	}
}

func allCodes() []proto.Code {
	return []proto.Code{
		proto.CodeInvalidEnvelope,
		proto.CodeInvalidRequest,
		proto.CodeUnknownVerb,
		proto.CodeHelloRequired,
		proto.CodeVersionMismatch,
		proto.CodeRequestInFlight,
		proto.CodeSessionOverlap,
		proto.CodeStaleToken,
		proto.CodeUnknownToken,
		proto.CodeSlotExpired,
		proto.CodeWaiterClosed,
		proto.CodeAwaitBusy,
		proto.CodeRootMismatch,
		proto.CodeShuttingDown,
		proto.CodeInternal,
	}
}

func TestErrorTextIsNonEmptyAndDistinctPerCode(t *testing.T) {
	seen := make(map[string]proto.Code)
	for _, code := range allCodes() {
		text := messages.ErrorText(string(code))
		if text == "" {
			t.Errorf("ErrorText(%q) is empty", code)
			continue
		}
		if other, ok := seen[text]; ok {
			t.Errorf("ErrorText(%q) and ErrorText(%q) return the same text %q", code, other, text)
		}
		seen[text] = code
	}
}

func TestErrorTextUnknownCodeIsNonEmpty(t *testing.T) {
	if messages.ErrorText("something_undefined") == "" {
		t.Fatal("ErrorText for an unknown code must still return a non-empty fallback")
	}
}
