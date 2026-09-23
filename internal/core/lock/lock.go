// Package lock provides a pure, in-memory, path-pattern lock table for
// Baton's coordination layer. It supports exclusive and shared holders,
// strict FIFO fairness, fencing tokens for stale-operation rejection,
// TTL-based expiry for both grants and queued waiter slots, and
// conservative literal-prefix overlap detection between path patterns.
//
// The package has no I/O, no sockets, no persistence, and spawns no
// package-owned background goroutines. All time-based behavior is driven by
// caller-supplied durations and the standard library time/context packages.
package lock

import "time"

// SessionID identifies the agent session that owns a request or grant.
type SessionID string

// Mode describes whether a lock request is exclusive or shared. The zero
// value is intentionally invalid so a missing Mode is rejected explicitly.
type Mode uint8

const (
	// Exclusive requests a single, mutually-exclusive holder.
	Exclusive Mode = iota + 1
	// Shared allows multiple concurrent holders of the same pattern.
	Shared
)

// String implements fmt.Stringer for Mode.
func (m Mode) String() string {
	switch m {
	case Exclusive:
		return "exclusive"
	case Shared:
		return "shared"
	default:
		return "invalid"
	}
}

// Request describes a caller's lock request submitted to Enqueue.
type Request struct {
	Session SessionID
	Pattern string
	Mode    Mode
	TTL     time.Duration
	SlotTTL time.Duration
}

// Grant is a read-only view of an active grant.
type Grant struct {
	Token   uint64
	Session SessionID
	Pattern string
	Mode    Mode
	Expires time.Time
}

// Result is the sealed outcome of a request: either Granted or Queued.
type Result interface{ isResult() }

// Granted reports that a request was granted a fencing token.
type Granted struct {
	Token   uint64
	Expires time.Time
}

// Queued reports that a request remains queued at the given FIFO position.
type Queued struct {
	Position int
}

func (Granted) isResult() {}
func (Queued) isResult()  {}

// Pending is a read-only view of a queued waiter.
type Pending struct {
	Session     SessionID
	Pattern     string
	Mode        Mode
	Position    int
	SlotExpires time.Time // zero while an Await is in progress
}

// Snapshot is a read-only, point-in-time view of the table state.
type Snapshot struct {
	Grants []Grant   // sorted by Token ascending
	Queue  []Pending // FIFO order
}
