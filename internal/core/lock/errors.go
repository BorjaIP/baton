package lock

import "errors"

// Sentinel errors returned by Table and Waiter operations. Validation
// failures wrap ErrInvalidRequest with additional detail via
// fmt.Errorf("%w: ...").
var (
	// ErrInvalidRequest is returned when a Request fails validation (empty
	// session or pattern, invalid Mode, or a non-positive TTL/SlotTTL/Renew
	// duration).
	ErrInvalidRequest = errors.New("lock: invalid request")

	// ErrSessionOverlap is returned when a session already holds or has
	// queued a request whose pattern overlaps the newly submitted request,
	// regardless of mode.
	ErrSessionOverlap = errors.New("lock: session already holds or queues an overlapping pattern")

	// ErrStaleToken is returned when a token was issued but is no longer
	// active (released, expired, or superseded by a later grant).
	ErrStaleToken = errors.New("lock: stale token")

	// ErrUnknownToken is returned when a token was never issued by this
	// table (zero, or greater than the highest token ever minted).
	ErrUnknownToken = errors.New("lock: unknown token")

	// ErrSlotExpired is returned by Await when a queued waiter's slot TTL
	// lapsed without being refreshed.
	ErrSlotExpired = errors.New("lock: queue slot expired")

	// ErrWaiterClosed is returned by Await when the waiter was canceled or
	// its session was released.
	ErrWaiterClosed = errors.New("lock: waiter canceled or session released")

	// ErrAwaitBusy is returned when Await is called concurrently on the
	// same Waiter.
	ErrAwaitBusy = errors.New("lock: concurrent Await on the same waiter")
)
