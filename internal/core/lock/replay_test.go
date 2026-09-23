package lock_test

import (
	"testing"
	"testing/synctest"
	"time"

	"github.com/BorjaIP/baton/internal/core/lock"
)

// TestAbandonedQueueSlotEvictedOnNextOperation covers the spec's lazy-sweep
// requirement: a queued slot whose SlotTTL lapses without ever being awaited
// or renewed disappears from the queue as observed by a later operation,
// with no package-owned goroutine involved.
func TestAbandonedQueueSlotEvictedOnNextOperation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		tbl := lock.New()
		mustEnqueue(t, tbl, req("A", "pattern1", lock.Exclusive))

		b := req("B", "pattern1", lock.Exclusive)
		b.SlotTTL = 1 * time.Second
		mustEnqueue(t, tbl, b)

		time.Sleep(2 * time.Second)

		snap := tbl.Snapshot()
		for _, p := range snap.Queue {
			if p.Session == "B" {
				t.Fatalf("expected B's abandoned slot evicted by the lazy sweep, got %+v", snap.Queue)
			}
		}
	})
}

// TestEvictedSlotDoesNotBlockOthersOnceSwept covers that once an abandoned
// slot is swept, the waiter behind it is promoted to the freed position and
// can be granted without the abandoned slot ever being explicitly released.
func TestEvictedSlotDoesNotBlockOthersOnceSwept(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		tbl := lock.New()
		mustEnqueue(t, tbl, req("A", "pattern1", lock.Exclusive))

		b := req("B", "pattern1", lock.Exclusive)
		b.SlotTTL = 1 * time.Second
		mustEnqueue(t, tbl, b)
		mustEnqueue(t, tbl, req("C", "pattern1", lock.Exclusive))

		time.Sleep(2 * time.Second)

		snap := tbl.Snapshot()
		if len(snap.Queue) != 1 || snap.Queue[0].Session != "C" {
			t.Fatalf("expected only C left queued after B's slot was swept, got %+v", snap.Queue)
		}
		if snap.Queue[0].Position != 1 {
			t.Fatalf("expected C's position to become 1 after B was evicted, got %+v", snap.Queue[0])
		}

		ga, ok := grantByToken(t, snap, "A")
		if !ok {
			t.Fatalf("expected A still holding the grant, got %+v", snap)
		}
		if err := tbl.Release(ga.Token); err != nil {
			t.Fatalf("Release(A) unexpected error: %v", err)
		}

		snap = tbl.Snapshot()
		if _, ok := grantByToken(t, snap, "C"); !ok {
			t.Fatalf("expected C granted after A released, without B ever being released, got %+v", snap)
		}
		checkInvariants(t, snap)
	})
}

// TestLazyReplayEqualsEagerModel is the ADR-5 worked example: grant G expires
// at T1, strictly before blocked waiter X's slot deadline T2. A sweep that
// only runs later, at T3 > T2, must still observe the chronological order —
// X was granted at T1 — rather than incorrectly evicting X for a slot
// deadline that an eager, clock-driven model would never have reached
// (this is exactly the gap the batch-1 placeholder advanceLocked left open).
func TestLazyReplayEqualsEagerModel(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		start := time.Now()
		tbl := lock.New()

		g := req("G", "pattern1", lock.Exclusive)
		g.TTL = 1 * time.Second // T1 = start + 1s
		mustEnqueue(t, tbl, g)

		x := req("X", "pattern1", lock.Exclusive)
		x.TTL = 5 * time.Second
		x.SlotTTL = 3 * time.Second // T2 = start + 3s
		mustEnqueue(t, tbl, x)

		// Sweep at T3 = start + 5s, after both T1 and T2.
		time.Sleep(5 * time.Second)

		snap := tbl.Snapshot()
		gx, ok := grantByToken(t, snap, "X")
		if !ok {
			t.Fatalf("expected X granted via chronological replay at T1 (before its own slot deadline T2), got %+v", snap)
		}

		wantExpires := start.Add(1 * time.Second).Add(x.TTL)
		if !gx.Expires.Equal(wantExpires) {
			t.Fatalf("expected X's Expires anchored to G's expiry instant T1 (%v), got %v", wantExpires, gx.Expires)
		}
		checkInvariants(t, snap)
	})
}

// TestExpiredGrantObservedViaLazySweepOnNextOperation covers freeing a
// TTL-expired grant purely through the lazy sweep of a later explicit
// operation (no Await involved), for a waiter that was queued but never
// blocked in Await.
func TestExpiredGrantObservedViaLazySweepOnNextOperation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		tbl := lock.New()
		a := req("A", "pattern1", lock.Exclusive)
		a.TTL = 2 * time.Second
		mustEnqueue(t, tbl, a)
		mustEnqueue(t, tbl, req("B", "pattern1", lock.Exclusive))

		time.Sleep(3 * time.Second)

		snap := tbl.Snapshot()
		if _, ok := grantByToken(t, snap, "A"); ok {
			t.Fatalf("expected A's expired grant evicted by the lazy sweep, got %+v", snap.Grants)
		}
		if _, ok := grantByToken(t, snap, "B"); !ok {
			t.Fatalf("expected B granted as of the snapshot operation after A's grant expired, got %+v", snap)
		}
		checkInvariants(t, snap)
	})
}
