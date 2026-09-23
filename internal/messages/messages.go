// Package messages is the single text catalog shared by internal/server
// (which fills proto.Error.Message from ErrorText) and
// internal/adapters/ctl (which renders usage, human output, and error
// text). It has no dependency beyond the standard library and
// internal/proto's Code type name, keeping it a leaf package per design §4.
package messages

import (
	"fmt"
	"time"
)

// Program is the fixed program name used in every usage/help string,
// regardless of how the binary was invoked (including through a symlink).
const Program = "baton"

// Usage/help strings. Every string here MUST reference Program and MUST NOT
// reference any other invocation name.
const (
	Usage = "Usage: " + Program + " <command> [arguments]\n\n" +
		"Commands:\n" +
		"  serve   run the lock daemon\n" +
		"  ctl     control the lock daemon\n\n" +
		"Run '" + Program + " ctl help' for details on ctl subcommands."

	ServeUsage = "Usage: " + Program + " serve [-h]\n\n" +
		"Runs the lock daemon in the foreground, listening on the Unix socket\n" +
		"resolved from $BATON_SOCK or ~/.baton/baton.sock."

	CtlUsage = "Usage: " + Program + " ctl <command> [arguments]\n\n" +
		"Commands:\n" +
		"  status          show current locks\n" +
		"  lock acquire    acquire a lock\n" +
		"  lock release    release a lock\n" +
		"  lock renew      renew a lock's TTL\n\n" +
		"Run '" + Program + " ctl <command> -h' for flags on a specific command."

	StatusUsage = "Usage: " + Program + " ctl status [--session S] [--all] [--json]"

	AcquireUsage = "Usage: " + Program + " ctl lock acquire <pattern> [--mode exclusive|shared] " +
		"[--wait D] [--ttl D] [--slot-ttl D] [--session S] [--json]"

	ReleaseUsage = "Usage: " + Program + " ctl lock release --token T [--json]"

	RenewUsage = "Usage: " + Program + " ctl lock renew --token T [--ttl D] [--json]"
)

// QueuedEphemeralHint is written to stderr alongside a "queued" human
// result when the invocation used an ephemeral session (no --session and
// no $BATON_SESSION), warning the user their place in the queue will not
// survive process exit.
const QueuedEphemeralHint = "note: no session set; your place was not kept (use --session or BATON_SESSION)"

// NoLocks is printed by "status" when there are no grants and no queued
// waiters to report.
const NoLocks = "no locks"

// Granted renders a human-readable message for a successful lock.acquire
// grant.
func Granted(pattern, mode string, token uint64, expires, now time.Time) string {
	return fmt.Sprintf("granted token %d on %s (%s), expires %s", token, pattern, mode, relative(expires, now))
}

// Queued renders a human-readable message for a lock.acquire that returned
// queued rather than granted.
func Queued(pattern, mode string, position int) string {
	return fmt.Sprintf("queued at position %d on %s (%s)", position, pattern, mode)
}

// Released renders a human-readable message for a successful lock.release.
func Released(token uint64) string {
	return fmt.Sprintf("released token %d", token)
}

// Renewed renders a human-readable message for a successful lock.renew.
func Renewed(token uint64, expires, now time.Time) string {
	return fmt.Sprintf("renewed token %d, expires %s", token, relative(expires, now))
}

// relative renders t as RFC3339 plus a human-friendly relative suffix
// ("in 4m59s" or "4m59s ago") measured against now.
func relative(t, now time.Time) string {
	d := t.Sub(now)
	if d >= 0 {
		return fmt.Sprintf("%s (in %s)", t.Format(time.RFC3339), d.Round(time.Second))
	}
	return fmt.Sprintf("%s (%s ago)", t.Format(time.RFC3339), (-d).Round(time.Second))
}

// errorTexts maps every wire error code to a distinct, non-empty
// human-readable default message. internal/server uses this to fill
// proto.Error.Message; internal/adapters/ctl uses it to render human-mode
// error output.
var errorTexts = map[string]string{
	"invalid_envelope":  "malformed or invalid message envelope",
	"invalid_request":   "invalid request parameters",
	"unknown_verb":      "unknown verb",
	"hello_required":    "hello handshake required before any other request",
	"version_mismatch":  "daemon protocol version incompatible; restart it",
	"request_in_flight": "a request is already in flight on this connection",
	"session_overlap":   "this session already holds or is queued for an overlapping pattern",
	"stale_token":       "token has been superseded by a later grant",
	"unknown_token":     "token is not recognized",
	"slot_expired":      "queued slot expired before it could be granted",
	"waiter_closed":     "queued wait was cancelled",
	"await_busy":        "another wait is already in progress for this waiter",
	"root_mismatch":     "token belongs to a different repository root",
	"shutting_down":     "daemon is shutting down",
	"internal":          "internal server error",
}

// ErrorText returns the default human-readable text for a wire error code.
// An unrecognized code still returns a non-empty, generic fallback.
func ErrorText(code string) string {
	if text, ok := errorTexts[code]; ok {
		return text
	}
	return fmt.Sprintf("unrecognized error (%s)", code)
}
