package server

import (
	"sync"

	"github.com/BorjaIP/baton/internal/core/lock"
)

// regKey identifies one registry slot: a session and the scope key
// (scopeKey(root, pattern)) it is acquiring.
type regKey struct {
	session lock.SessionID
	scope   string
}

// regEntry is the registry's record for one in-flight or granted request.
type regEntry struct {
	w         *lock.Waiter
	mode      lock.Mode
	connID    uint64
	ephemeral bool
	delivered bool
	token     uint64
}

// registry maps (session, scope) to the *lock.Waiter handle core/lock
// returned for that pair, implementing stable-session resume, idempotent
// re-acquire after grant (G2), terminal drop plus re-enqueue, and
// ephemeral-session cancellation on disconnect (G1). registry.mu is always
// released before any call to Waiter.Await (design ADR-3): the lock order is
// registry -> table, and the table never calls back into the registry.
type registry struct {
	mu sync.Mutex
	m  map[regKey]*regEntry
}

// obtain looks up or creates the waiter for k. If a live entry already
// exists with a matching mode, it is resumed (connID is updated to the
// caller's, and resumed=true is returned) instead of calling t.Enqueue
// again. If a live entry exists with a different mode, core/lock's
// self-overlap rejection is surfaced unchanged. Otherwise t.Enqueue is
// called fresh and the new waiter is registered.
//
// obtain first prunes registry entries that a fresh Snapshot shows are no
// longer present in the table (i.e., their waiter reached a terminal
// state), so a terminal entry is always discarded and replaced by this
// fresh-enqueue path rather than being resumed.
func (r *registry) obtain(t *lock.Table, k regKey, req lock.Request, connID uint64, ephemeral bool) (w *lock.Waiter, resumed bool, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.pruneLocked(t.Snapshot())

	if e, ok := r.m[k]; ok {
		if e.mode != req.Mode {
			return nil, false, lock.ErrSessionOverlap
		}
		e.connID = connID
		return e.w, true, nil
	}

	w, err = t.Enqueue(req)
	if err != nil {
		return nil, false, err
	}

	r.m[k] = &regEntry{w: w, mode: req.Mode, connID: connID, ephemeral: ephemeral}
	return w, false, nil
}

// markDelivered records that k's waiter's grant (identified by token) has
// been successfully written to its connection (design ADR-4). Only a
// delivered entry survives ephemeral-connection cancellation on disconnect.
func (r *registry) markDelivered(k regKey, w *lock.Waiter, token uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if e, ok := r.m[k]; ok && e.w == w {
		e.delivered = true
		e.token = token
	}
}

// drop removes k's registry entry, but only if it still refers to w (design
// invariant I4: a stale drop never removes a newer entry that has since
// replaced it).
func (r *registry) drop(k regKey, w *lock.Waiter) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if e, ok := r.m[k]; ok && e.w == w {
		delete(r.m, k)
	}
}

// forgetToken removes the registry entry recording token, after a
// successful release or renew.
func (r *registry) forgetToken(token uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for k, e := range r.m {
		if e.delivered && e.token == token {
			delete(r.m, k)
			return
		}
	}
}

// closeConn runs on connection teardown. For an ephemeral connection, every
// entry it owns that was not yet delivered is Cancel()ed and removed (design
// decision G1): a pending queue slot is dropped, and a grant that raced a
// dead connection is released. A delivered grant, and every entry belonging
// to a stable (non-ephemeral) connection, is left untouched.
func (r *registry) closeConn(connID uint64, ephemeral bool) {
	if !ephemeral {
		return
	}

	r.mu.Lock()
	var toCancel []*lock.Waiter
	for k, e := range r.m {
		if e.connID != connID || e.delivered {
			continue
		}
		toCancel = append(toCancel, e.w)
		delete(r.m, k)
	}
	r.mu.Unlock()

	for _, w := range toCancel {
		w.Cancel()
	}
}

// pruneLocked drops every registry entry whose (session, scope) pair is
// absent from s (a fresh Table.Snapshot()), meaning its waiter has reached a
// terminal state core/lock no longer reports. Callers must hold r.mu.
func (r *registry) pruneLocked(s lock.Snapshot) {
	present := make(map[regKey]struct{}, len(s.Grants)+len(s.Queue))
	for _, g := range s.Grants {
		present[regKey{session: g.Session, scope: g.Pattern}] = struct{}{}
	}
	for _, p := range s.Queue {
		present[regKey{session: p.Session, scope: p.Pattern}] = struct{}{}
	}

	for k := range r.m {
		if _, ok := present[k]; !ok {
			delete(r.m, k)
		}
	}
}
