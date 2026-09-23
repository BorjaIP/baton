package lock

import (
	"fmt"
	"sort"
	"sync"
	"time"
)

// Table is an in-memory lock table protected by a single mutex, since
// overlapping patterns can span different lock keys and must be reasoned
// about together (design ADR-2).
type Table struct {
	mu        sync.Mutex
	lastToken uint64
	grants    map[uint64]*Waiter
	queue     []*Waiter            // pending only, FIFO
	awaiting  map[*Waiter]struct{} // waiters with an Await in progress
}

// New creates an empty Table.
func New() *Table {
	return &Table{
		grants:   make(map[uint64]*Waiter),
		awaiting: make(map[*Waiter]struct{}),
	}
}

// Enqueue either grants req immediately (if grantable per FIFO/overlap
// rules) or places it into the FIFO queue, returning a *Waiter handle for
// that request's slot either way.
func (t *Table) Enqueue(r Request) (*Waiter, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	now := time.Now()
	t.advanceLocked(now)

	if err := validateRequest(r); err != nil {
		return nil, err
	}
	if t.hasSessionOverlapLocked(r) {
		return nil, ErrSessionOverlap
	}

	w := &Waiter{
		t:            t,
		req:          r,
		state:        pending,
		slotDeadline: now.Add(r.SlotTTL),
		wake:         make(chan struct{}, 1),
	}
	t.queue = append(t.queue, w)
	t.promoteLocked(now)
	t.kickLocked()

	return w, nil
}

// Release frees the grant identified by token, per the fencing-token
// validation rules, and re-evaluates the FIFO queue.
func (t *Table) Release(token uint64) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	now := time.Now()
	t.advanceLocked(now)

	w, err := t.validateTokenLocked(token)
	if err != nil {
		return err
	}

	delete(t.grants, token)
	w.state = ended
	t.promoteLocked(now)
	t.kickLocked()
	return nil
}

// Renew validates token and, on success, extends the grant's Expires by ttl
// from now. It never mints a new token.
func (t *Table) Renew(token uint64, ttl time.Duration) (time.Time, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	now := time.Now()
	t.advanceLocked(now)

	if ttl <= 0 {
		return time.Time{}, fmt.Errorf("%w: renew ttl must be > 0", ErrInvalidRequest)
	}

	w, err := t.validateTokenLocked(token)
	if err != nil {
		return time.Time{}, err
	}

	w.expires = now.Add(ttl)
	return w.expires, nil
}

// ReleaseSession releases every active grant held by session and removes
// every queued waiter slot belonging to session, in a single operation. It
// is a no-op if the session holds nothing.
func (t *Table) ReleaseSession(session SessionID) {
	t.mu.Lock()
	defer t.mu.Unlock()

	now := time.Now()
	t.advanceLocked(now)

	for token, w := range t.grants {
		if w.req.Session != session {
			continue
		}
		w.state = closed
		delete(t.grants, token)
	}

	remaining := make([]*Waiter, 0, len(t.queue))
	for _, w := range t.queue {
		if w.req.Session == session {
			w.state = closed
			continue
		}
		remaining = append(remaining, w)
	}
	t.queue = remaining

	t.promoteLocked(now)
	t.kickLocked()
}

// Validate resolves token to its active Grant, applying the same
// stale/unknown token rules as Release and Renew.
func (t *Table) Validate(token uint64) (Grant, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	now := time.Now()
	t.advanceLocked(now)

	w, err := t.validateTokenLocked(token)
	if err != nil {
		return Grant{}, err
	}
	return grantView(w), nil
}

// Snapshot returns a read-only, point-in-time view of the table: all active
// grants and all queued waiters. It performs the standard lazy sweep of
// expired entries but no other mutation.
func (t *Table) Snapshot() Snapshot {
	t.mu.Lock()
	defer t.mu.Unlock()

	now := time.Now()
	t.advanceLocked(now)

	grants := make([]Grant, 0, len(t.grants))
	for _, w := range t.grants {
		grants = append(grants, grantView(w))
	}
	sort.Slice(grants, func(i, j int) bool { return grants[i].Token < grants[j].Token })

	queue := make([]Pending, 0, len(t.queue))
	for _, w := range t.queue {
		var slotExpires time.Time
		if _, awaiting := t.awaiting[w]; !awaiting {
			slotExpires = w.slotDeadline
		}
		queue = append(queue, Pending{
			Session:     w.req.Session,
			Pattern:     w.req.Pattern,
			Mode:        w.req.Mode,
			Position:    t.positionLocked(w),
			SlotExpires: slotExpires,
		})
	}

	return Snapshot{Grants: grants, Queue: queue}
}

func grantView(w *Waiter) Grant {
	return Grant{
		Token:   w.token,
		Session: w.req.Session,
		Pattern: w.req.Pattern,
		Mode:    w.req.Mode,
		Expires: w.expires,
	}
}

// validateRequest applies the caller-supplied-duration and shape rules
// shared by Enqueue: Session and Pattern must be non-empty, Mode must be
// Exclusive or Shared, and TTL/SlotTTL must be positive.
func validateRequest(r Request) error {
	if r.Session == "" {
		return fmt.Errorf("%w: session must not be empty", ErrInvalidRequest)
	}
	if r.Pattern == "" {
		return fmt.Errorf("%w: pattern must not be empty", ErrInvalidRequest)
	}
	if r.Mode != Exclusive && r.Mode != Shared {
		return fmt.Errorf("%w: mode must be Exclusive or Shared", ErrInvalidRequest)
	}
	if r.TTL <= 0 {
		return fmt.Errorf("%w: TTL must be > 0", ErrInvalidRequest)
	}
	if r.SlotTTL <= 0 {
		return fmt.Errorf("%w: SlotTTL must be > 0", ErrInvalidRequest)
	}
	return nil
}

// hasSessionOverlapLocked reports whether r's session already holds a grant
// or has a queued request whose pattern overlaps r.Pattern, in any mode
// combination (design §13 OQ-4: same-session shared+shared is also
// rejected).
func (t *Table) hasSessionOverlapLocked(r Request) bool {
	for _, w := range t.grants {
		if w.req.Session == r.Session && overlaps(w.req.Pattern, r.Pattern) {
			return true
		}
	}
	for _, w := range t.queue {
		if w.req.Session == r.Session && overlaps(w.req.Pattern, r.Pattern) {
			return true
		}
	}
	return false
}

// validateTokenLocked resolves token to its active Waiter, distinguishing
// unknown tokens (never issued, or beyond the highest minted so far) from
// stale tokens (issued but no longer active) per design §13.
func (t *Table) validateTokenLocked(token uint64) (*Waiter, error) {
	if token == 0 || token > t.lastToken {
		return nil, ErrUnknownToken
	}
	w, ok := t.grants[token]
	if !ok {
		return nil, ErrStaleToken
	}
	return w, nil
}

// promoteLocked walks the FIFO queue from the front, granting every waiter
// that conflicts with no active grant and no earlier still-pending waiter
// (design ADR-3). Waiters granted during this pass become visible to later
// waiters in the same pass.
func (t *Table) promoteLocked(at time.Time) {
	if len(t.queue) == 0 {
		return
	}

	ahead := make([]*Waiter, 0, len(t.queue))
	rest := make([]*Waiter, 0, len(t.queue))

	for _, w := range t.queue {
		if t.grantConflictsLocked(w.req) || waiterListConflicts(ahead, w.req) {
			ahead = append(ahead, w)
			rest = append(rest, w)
			continue
		}

		t.lastToken++
		w.token = t.lastToken
		w.expires = at.Add(w.req.TTL)
		w.state = granted
		t.grants[w.token] = w
	}

	t.queue = rest
}

func (t *Table) grantConflictsLocked(r Request) bool {
	for _, g := range t.grants {
		if conflicts(g.req, r) {
			return true
		}
	}
	return false
}

func waiterListConflicts(list []*Waiter, r Request) bool {
	for _, w := range list {
		if conflicts(w.req, r) {
			return true
		}
	}
	return false
}

// positionLocked reports w's FIFO position as 1 + the number of earlier
// still-pending waiters whose requests conflict with w's (design ADR-12,
// confirmed binding in design §13).
func (t *Table) positionLocked(w *Waiter) int {
	pos := 1
	for _, other := range t.queue {
		if other == w {
			break
		}
		if conflicts(other.req, w.req) {
			pos++
		}
	}
	return pos
}

// kickLocked wakes every waiter currently blocked in Await via a
// non-blocking send on its capacity-1 wake channel (design ADR-6).
func (t *Table) kickLocked() {
	for w := range t.awaiting {
		select {
		case w.wake <- struct{}{}:
		default:
		}
	}
}

// nextEventLocked reports the earliest upcoming event the table must react
// to: either a grant's Expires, or the slot deadline of a queued waiter that
// does not currently have an Await in progress (design §6 algorithms). It
// reports ok=false if there are no pending events at all.
func (t *Table) nextEventLocked() (time.Time, bool) {
	var (
		earliest time.Time
		found    bool
	)
	consider := func(candidate time.Time) {
		if !found || candidate.Before(earliest) {
			earliest = candidate
			found = true
		}
	}

	for _, w := range t.grants {
		consider(w.expires)
	}
	for _, w := range t.queue {
		if _, awaiting := t.awaiting[w]; awaiting {
			continue
		}
		consider(w.slotDeadline)
	}

	return earliest, found
}

// applyExpiryLocked evicts every grant and every unpinned queued slot whose
// expiry is at or before at, moving them to their respective terminal
// states. It performs no promotion; callers must call promoteLocked(at)
// afterward.
func (t *Table) applyExpiryLocked(at time.Time) {
	for token, w := range t.grants {
		if !at.Before(w.expires) {
			w.state = ended
			delete(t.grants, token)
		}
	}

	if len(t.queue) > 0 {
		remaining := make([]*Waiter, 0, len(t.queue))
		for _, w := range t.queue {
			if _, awaiting := t.awaiting[w]; !awaiting && !at.Before(w.slotDeadline) {
				w.state = slotExpired
				continue
			}
			remaining = append(remaining, w)
		}
		t.queue = remaining
	}
}

// advanceLocked replays every expiry event at or before now in strict
// chronological order, applying each event and re-promoting the queue at
// that event's own instant, per design ADR-5. This guarantees the same
// observable state as an eager, clock-driven table regardless of when a
// sweep happens to run: a grant expiring before a blocked waiter's slot
// deadline is always observed as expiring first, even if both are already
// in the past by the time this function runs.
//
// The loop always terminates: applyExpiryLocked removes at least one grant
// or queue slot per iteration (the one that produced the returned event
// time), and any waiter freshly granted during the following promoteLocked
// gets an Expires strictly later than at (TTL > 0 is enforced at
// validation), so it cannot re-trigger the same event.
func (t *Table) advanceLocked(now time.Time) bool {
	changed := false
	for {
		at, ok := t.nextEventLocked()
		if !ok || at.After(now) {
			break
		}
		t.applyExpiryLocked(at)
		t.promoteLocked(at)
		changed = true
	}
	return changed
}
