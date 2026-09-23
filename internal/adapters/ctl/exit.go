package ctl

import (
	"context"
	"errors"

	"github.com/BorjaIP/baton/internal/client"
	"github.com/BorjaIP/baton/internal/proto"
)

// Exit codes per design ADR-15 / spec "Exit Code Contract".
const (
	ExitOK          = 0
	ExitInternal    = 1
	ExitUsage       = 2
	ExitQueued      = 3
	ExitRejected    = 4
	ExitUnavailable = 5
	ExitInterrupted = 130
)

// rejectedCodes are the wire codes that mean "rejected by lock rules":
// overlap, stale/unknown token, slot expired, waiter closed/cancelled,
// root mismatch, or a request already in flight.
var rejectedCodes = map[proto.Code]bool{
	proto.CodeSessionOverlap:  true,
	proto.CodeStaleToken:      true,
	proto.CodeUnknownToken:    true,
	proto.CodeSlotExpired:     true,
	proto.CodeWaiterClosed:    true,
	proto.CodeAwaitBusy:       true,
	proto.CodeRootMismatch:    true,
	proto.CodeRequestInFlight: true,
}

// ExitCode maps an error returned from a daemon call (or ctx.Err()) to the
// documented ctl exit code. The result is identical regardless of whether
// the caller renders human or --json output.
func ExitCode(err error) int {
	if err == nil {
		return ExitOK
	}
	if errors.Is(err, context.Canceled) {
		return ExitInterrupted
	}
	if errors.Is(err, client.ErrUnavailable) {
		return ExitUnavailable
	}

	var wireErr *proto.Error
	if errors.As(err, &wireErr) {
		switch {
		case wireErr.Code == proto.CodeInvalidRequest:
			return ExitUsage
		case wireErr.Code == proto.CodeShuttingDown:
			return ExitUnavailable
		case rejectedCodes[wireErr.Code]:
			return ExitRejected
		default:
			return ExitInternal
		}
	}

	return ExitInternal
}
