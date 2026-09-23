package lock

import (
	"context"
	"time"
)

// waiterState records where a Waiter currently sits in its lifecycle.
type waiterState uint8

const (
	pending waiterState = iota
	granted
	ended
	slotExpired
	closed
)

// Waiter is the single record for a request, whether it is still pending in
// the FIFO queue or has been granted. Its state field records which.
type Waiter struct {
	t            *Table
	req          Request
	state        waiterState
	token        uint64
	expires      time.Time
	slotDeadline time.Time
	wake         chan struct{} // cap 1
}

// checkLocked reports whether w has reached an outcome Await can return
// without blocking further: a granted or terminal state (checked first, per
// design ADR-10 — a grant beats timeout and context cancellation), or the
// caller's own deadline having elapsed while still pending (reported as
// Queued, keeping the slot). It must be called while t.mu is held.
func (w *Waiter) checkLocked(now, deadline time.Time) (Result, error, bool) {
	switch w.state {
	case granted:
		return Granted{Token: w.token, Expires: w.expires}, nil, true
	case ended:
		return nil, ErrStaleToken, true
	case slotExpired:
		return nil, ErrSlotExpired, true
	case closed:
		return nil, ErrWaiterClosed, true
	}

	if !now.Before(deadline) {
		return Queued{Position: w.t.positionLocked(w)}, nil, true
	}

	return nil, nil, false
}

// finishLocked runs on every Await exit path: it removes w from the
// awaiting set (resuming slot-TTL enforcement), restarts the slot deadline
// from now if w is still pending, and wakes any other waiters that may have
// been affected. It must be called while t.mu is held.
func (t *Table) finishLocked(w *Waiter, now time.Time) {
	delete(t.awaiting, w)
	if w.state == pending {
		w.slotDeadline = now.Add(w.req.SlotTTL)
	}
	t.kickLocked()
}

// Await blocks up to timeout (<=0 means a non-blocking poll that also
// refreshes the queue slot's TTL, per design ADR-8/OQ-5) waiting for w to
// become grantable. It returns Granted once granted, Queued{Position} if
// timeout elapses or ctx is cancelled while still pending (the slot is kept
// in both cases), or one of ErrStaleToken / ErrSlotExpired / ErrWaiterClosed
// if the waiter reached a terminal state. Only one Await may run on a given
// Waiter at a time; a concurrent call returns ErrAwaitBusy.
func (w *Waiter) Await(ctx context.Context, timeout time.Duration) (Result, error) {
	t := w.t

	t.mu.Lock()
	if _, busy := t.awaiting[w]; busy {
		t.mu.Unlock()
		return nil, ErrAwaitBusy
	}

	now := time.Now()
	t.advanceLocked(now)
	deadline := now.Add(timeout)
	t.awaiting[w] = struct{}{}

	for {
		if result, err, done := w.checkLocked(now, deadline); done {
			t.finishLocked(w, now)
			t.mu.Unlock()
			return result, err
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			t.finishLocked(w, now)
			t.mu.Unlock()
			return nil, ctxErr
		}

		wakeAt := deadline
		if next, ok := t.nextEventLocked(); ok && next.Before(wakeAt) {
			wakeAt = next
		}
		t.mu.Unlock()

		timer := time.NewTimer(wakeAt.Sub(now))
		select {
		case <-w.wake:
		case <-timer.C:
		case <-ctx.Done():
		}
		timer.Stop()

		t.mu.Lock()
		now = time.Now()
		t.advanceLocked(now)
	}
}

// Cancel abandons w's request: if pending, it removes the queue slot; if
// granted, it releases the grant exactly like Release(token); if already
// terminal, it does nothing (design ADR-11). In every case w ends as closed,
// and any concurrently blocked or future Await call on w returns
// ErrWaiterClosed. Cancel is idempotent.
func (w *Waiter) Cancel() {
	t := w.t

	t.mu.Lock()
	defer t.mu.Unlock()

	now := time.Now()
	t.advanceLocked(now)

	switch w.state {
	case pending:
		for i, other := range t.queue {
			if other == w {
				t.queue = append(t.queue[:i], t.queue[i+1:]...)
				break
			}
		}
		w.state = closed
	case granted:
		delete(t.grants, w.token)
		w.state = closed
	default:
		// Already terminal (ended, slotExpired, or closed): no-op.
		return
	}

	t.promoteLocked(now)
	t.kickLocked()
}
