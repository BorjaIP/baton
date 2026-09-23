package lock_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/BorjaIP/baton/internal/core/lock"
)

// req builds a valid Request with generous default TTL/SlotTTL, overridable
// by the caller by mutating the returned value before use.
func req(session, pattern string, mode lock.Mode) lock.Request {
	return lock.Request{
		Session: lock.SessionID(session),
		Pattern: pattern,
		Mode:    mode,
		TTL:     5 * time.Second,
		SlotTTL: 5 * time.Second,
	}
}

// mustEnqueue enqueues r and fails the test immediately on error.
func mustEnqueue(t *testing.T, tbl *lock.Table, r lock.Request) *lock.Waiter {
	t.Helper()
	w, err := tbl.Enqueue(r)
	if err != nil {
		t.Fatalf("Enqueue(%+v) unexpected error: %v", r, err)
	}
	return w
}

// grantByToken finds the active grant held by session want in snap.
func grantByToken(t *testing.T, snap lock.Snapshot, want lock.SessionID) (lock.Grant, bool) {
	t.Helper()
	for _, g := range snap.Grants {
		if g.Session == want {
			return g, true
		}
	}
	return lock.Grant{}, false
}

// awaitOutcome carries the result of an asynchronous Await call back to the
// test goroutine.
type awaitOutcome struct {
	result lock.Result
	err    error
}

// awaitAsync runs w.Await(ctx, timeout) on its own goroutine and returns a
// channel that receives exactly one outcome. It exists so synctest-bubble
// tests can exercise a blocked Await from a separate goroutine while the
// main test goroutine drives simulated time and assertions; callers should
// call synctest.Wait() before assuming the goroutine has reached its blocking
// point.
func awaitAsync(w *lock.Waiter, ctx context.Context, timeout time.Duration) <-chan awaitOutcome {
	ch := make(chan awaitOutcome, 1)
	go func() {
		res, err := w.Await(ctx, timeout)
		ch <- awaitOutcome{result: res, err: err}
	}()
	return ch
}

// --- Invariant checking over a Snapshot (design §7 I2, I3, I4, I5) ---
//
// These mirror overlap.go's unexported literal-prefix comparison so tests
// can verify invariants using only the exported Snapshot API, without
// reaching into Table internals.

const wildcardChars = `*?[{\`

func literalPrefix(p string) string {
	if idx := strings.IndexAny(p, wildcardChars); idx != -1 {
		return p[:idx]
	}
	return p
}

func patternsOverlap(a, b string) bool {
	pa, pb := literalPrefix(a), literalPrefix(b)
	return strings.HasPrefix(pa, pb) || strings.HasPrefix(pb, pa)
}

func requestsConflict(patternA string, modeA lock.Mode, patternB string, modeB lock.Mode) bool {
	return patternsOverlap(patternA, patternB) && (modeA == lock.Exclusive || modeB == lock.Exclusive)
}

// checkInvariants asserts, from a single Snapshot, the invariants that a
// point-in-time read can observe:
//
//   - I2: no two active grants conflict.
//   - I3: every queued waiter conflicts with an active grant or an earlier
//     queued waiter (the queue is stable — nothing pending is skippable).
//   - I4: tokens are unique and strictly positive.
//   - I5 (partial, snapshot-observable slice): every queued Position is >= 1.
func checkInvariants(t *testing.T, snap lock.Snapshot) {
	t.Helper()

	seenTokens := make(map[uint64]bool)
	for i, g := range snap.Grants {
		if g.Token == 0 {
			t.Errorf("invariant I4 violated: grant %+v has zero token", g)
		}
		if seenTokens[g.Token] {
			t.Errorf("invariant I4 violated: duplicate token %d", g.Token)
		}
		seenTokens[g.Token] = true

		for j, other := range snap.Grants {
			if i == j {
				continue
			}
			if requestsConflict(g.Pattern, g.Mode, other.Pattern, other.Mode) {
				t.Errorf("invariant I2 violated: grants %+v and %+v conflict but are both active", g, other)
			}
		}
	}

	for i, p := range snap.Queue {
		if p.Position < 1 {
			t.Errorf("invariant I5 violated: queued entry %+v has Position < 1", p)
		}

		blocked := false
		for _, g := range snap.Grants {
			if requestsConflict(p.Pattern, p.Mode, g.Pattern, g.Mode) {
				blocked = true
				break
			}
		}
		if !blocked {
			for j, earlier := range snap.Queue {
				if j >= i {
					break
				}
				if requestsConflict(p.Pattern, p.Mode, earlier.Pattern, earlier.Mode) {
					blocked = true
					break
				}
			}
		}
		if !blocked {
			t.Errorf("invariant I3 violated: queued entry %+v is not blocked by any grant or earlier queued waiter", p)
		}
	}
}
