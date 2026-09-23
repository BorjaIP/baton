package proto

// Code is a stable, documented wire error code. Every relevant
// internal/core/lock sentinel error maps to exactly one distinct Code; see
// docs/protocol.md for the full table.
type Code string

// Wire error codes for Phase 2.
const (
	CodeInvalidEnvelope Code = "invalid_envelope"
	CodeInvalidRequest  Code = "invalid_request" // core ErrInvalidRequest, body decode, scope validation
	CodeUnknownVerb     Code = "unknown_verb"
	CodeHelloRequired   Code = "hello_required"
	CodeVersionMismatch Code = "version_mismatch"
	CodeRequestInFlight Code = "request_in_flight"
	CodeSessionOverlap  Code = "session_overlap" // core ErrSessionOverlap
	CodeStaleToken      Code = "stale_token"     // core ErrStaleToken
	CodeUnknownToken    Code = "unknown_token"   // core ErrUnknownToken
	CodeSlotExpired     Code = "slot_expired"    // core ErrSlotExpired
	CodeWaiterClosed    Code = "waiter_closed"   // core ErrWaiterClosed
	CodeAwaitBusy       Code = "await_busy"      // core ErrAwaitBusy
	CodeRootMismatch    Code = "root_mismatch"
	CodeShuttingDown    Code = "shutting_down"
	CodeInternal        Code = "internal"
)

// Error is a typed, wire-transmissible error carried in a response
// envelope's Err field.
type Error struct {
	Code    Code              `msgpack:"code"`
	Message string            `msgpack:"message"`
	Detail  map[string]string `msgpack:"detail,omitempty"`
}

// Error implements the error interface.
func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	return string(e.Code) + ": " + e.Message
}

// Is reports whether target is a non-nil *Error with the same Code,
// regardless of Message or Detail. This lets callers match wire errors with
// errors.Is against either a decoded *Error or one of the Err* sentinels
// below.
func (e *Error) Is(target error) bool {
	t, ok := target.(*Error)
	if !ok || t == nil || e == nil {
		return false
	}
	return e.Code == t.Code
}

// Sentinel errors, one per Code, for errors.Is matching against a decoded
// wire error without constructing one by hand.
var (
	ErrCodeInvalidEnvelope = &Error{Code: CodeInvalidEnvelope}
	ErrCodeInvalidRequest  = &Error{Code: CodeInvalidRequest}
	ErrCodeUnknownVerb     = &Error{Code: CodeUnknownVerb}
	ErrCodeHelloRequired   = &Error{Code: CodeHelloRequired}
	ErrCodeVersionMismatch = &Error{Code: CodeVersionMismatch}
	ErrCodeRequestInFlight = &Error{Code: CodeRequestInFlight}
	ErrCodeSessionOverlap  = &Error{Code: CodeSessionOverlap}
	ErrCodeStaleToken      = &Error{Code: CodeStaleToken}
	ErrCodeUnknownToken    = &Error{Code: CodeUnknownToken}
	ErrCodeSlotExpired     = &Error{Code: CodeSlotExpired}
	ErrCodeWaiterClosed    = &Error{Code: CodeWaiterClosed}
	ErrCodeAwaitBusy       = &Error{Code: CodeAwaitBusy}
	ErrCodeRootMismatch    = &Error{Code: CodeRootMismatch}
	ErrCodeShuttingDown    = &Error{Code: CodeShuttingDown}
	ErrCodeInternal        = &Error{Code: CodeInternal}
)
