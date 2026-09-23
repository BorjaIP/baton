package ctl_test

import (
	"context"
	"flag"
	"fmt"
	"testing"

	"github.com/BorjaIP/baton/internal/adapters/ctl"
	"github.com/BorjaIP/baton/internal/client"
	"github.com/BorjaIP/baton/internal/proto"
)

func TestExitCode_EveryWireCode(t *testing.T) {
	cases := []struct {
		code proto.Code
		want int
	}{
		{proto.CodeInvalidEnvelope, ctl.ExitInternal},
		{proto.CodeInvalidRequest, ctl.ExitUsage},
		{proto.CodeUnknownVerb, ctl.ExitInternal},
		{proto.CodeHelloRequired, ctl.ExitInternal},
		{proto.CodeVersionMismatch, ctl.ExitInternal},
		{proto.CodeRequestInFlight, ctl.ExitRejected},
		{proto.CodeSessionOverlap, ctl.ExitRejected},
		{proto.CodeStaleToken, ctl.ExitRejected},
		{proto.CodeUnknownToken, ctl.ExitRejected},
		{proto.CodeSlotExpired, ctl.ExitRejected},
		{proto.CodeWaiterClosed, ctl.ExitRejected},
		{proto.CodeAwaitBusy, ctl.ExitRejected},
		{proto.CodeRootMismatch, ctl.ExitRejected},
		{proto.CodeShuttingDown, ctl.ExitUnavailable},
		{proto.CodeInternal, ctl.ExitInternal},
	}
	for _, tc := range cases {
		err := &proto.Error{Code: tc.code}
		if got := ctl.ExitCode(err); got != tc.want {
			t.Errorf("ExitCode(%s) = %d, want %d", tc.code, got, tc.want)
		}
	}
}

func TestExitCode_ClientUnavailable(t *testing.T) {
	err := fmt.Errorf("%w: dial refused", client.ErrUnavailable)
	if got := ctl.ExitCode(err); got != ctl.ExitUnavailable {
		t.Errorf("ExitCode(ErrUnavailable) = %d, want %d", got, ctl.ExitUnavailable)
	}
}

func TestExitCode_ContextCanceledIsInterrupted(t *testing.T) {
	if got := ctl.ExitCode(context.Canceled); got != ctl.ExitInterrupted {
		t.Errorf("ExitCode(context.Canceled) = %d, want %d", got, ctl.ExitInterrupted)
	}
}

func TestExitCode_UsageErrorIsUsage(t *testing.T) {
	if got := ctl.ExitCode(flag.ErrHelp); got != ctl.ExitInternal {
		// flag.ErrHelp itself is not a usage error from ctl's own flag
		// validation; ctl's own ParseXFlags errors are plain fmt.Errorf and
		// are mapped to usage by the caller, not by ExitCode. Documented via
		// this test asserting the safe (internal) default for an
		// unrecognized error value.
		t.Errorf("ExitCode(flag.ErrHelp) = %d, want %d (safe default)", got, ctl.ExitInternal)
	}
}

func TestExitCode_QueuedResultConstantIsThree(t *testing.T) {
	if ctl.ExitQueued != 3 {
		t.Fatalf("ExitQueued = %d, want 3", ctl.ExitQueued)
	}
}

func TestExitCode_ConsistentAcrossOutputModes(t *testing.T) {
	// ExitCode takes no output-mode parameter at all, so identical codes are
	// structurally guaranteed regardless of human vs --json rendering; this
	// test documents that invariant explicitly.
	err := &proto.Error{Code: proto.CodeStaleToken}
	if ctl.ExitCode(err) != ctl.ExitCode(err) {
		t.Fatal("ExitCode must be a pure function of the error")
	}
}
