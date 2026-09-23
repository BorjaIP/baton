package lock_test

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/BorjaIP/baton/internal/core/lock"
)

func TestAwaitGrantsImmediatelyOnFreePattern(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		tbl := lock.New()
		w := mustEnqueue(t, tbl, req("A", "pattern1", lock.Exclusive))

		res, err := w.Await(context.Background(), 0)
		if err != nil {
			t.Fatalf("Await unexpected error: %v", err)
		}
		g, ok := res.(lock.Granted)
		if !ok {
			t.Fatalf("expected Granted result, got %#v", res)
		}
		if g.Token == 0 {
			t.Fatalf("expected a non-zero token, got %+v", g)
		}
	})
}

func TestAwaitTimesOutAndReportsPositionKeepingSlot(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		tbl := lock.New()
		mustEnqueue(t, tbl, req("D1", "pattern1", lock.Exclusive))
		mustEnqueue(t, tbl, req("D2", "pattern1", lock.Exclusive))
		wB := mustEnqueue(t, tbl, req("B", "pattern1", lock.Exclusive))

		res, err := wB.Await(context.Background(), 100*time.Millisecond)
		if err != nil {
			t.Fatalf("Await unexpected error: %v", err)
		}
		q, ok := res.(lock.Queued)
		if !ok || q.Position != 2 {
			t.Fatalf("expected Queued{Position:2}, got %#v", res)
		}

		snap := tbl.Snapshot()
		found := false
		for _, p := range snap.Queue {
			if p.Session == "B" {
				found = true
			}
		}
		if !found {
			t.Fatalf("expected B's slot kept in the queue after the timeout, got %+v", snap.Queue)
		}
	})
}

func TestReCallAwaitAfterTimeoutGrantsWhenGrantable(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		tbl := lock.New()
		mustEnqueue(t, tbl, req("A", "pattern1", lock.Exclusive))
		wB := mustEnqueue(t, tbl, req("B", "pattern1", lock.Exclusive))

		res, err := wB.Await(context.Background(), 50*time.Millisecond)
		if err != nil {
			t.Fatalf("first Await unexpected error: %v", err)
		}
		if _, ok := res.(lock.Queued); !ok {
			t.Fatalf("expected Queued on the first Await, got %#v", res)
		}

		snap := tbl.Snapshot()
		ga, _ := grantByToken(t, snap, "A")
		if err := tbl.Release(ga.Token); err != nil {
			t.Fatalf("Release(A) unexpected error: %v", err)
		}

		res2, err := wB.Await(context.Background(), 50*time.Millisecond)
		if err != nil {
			t.Fatalf("second Await unexpected error: %v", err)
		}
		if _, ok := res2.(lock.Granted); !ok {
			t.Fatalf("expected Granted on the second Await after A released, got %#v", res2)
		}
	})
}

func TestAwaitRefreshesQueueSlotTTL(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		tbl := lock.New()
		a := req("A", "pattern1", lock.Exclusive)
		a.TTL = time.Hour
		mustEnqueue(t, tbl, a)

		b := req("B", "pattern1", lock.Exclusive)
		b.SlotTTL = 2 * time.Second
		wB := mustEnqueue(t, tbl, b)

		time.Sleep(1500 * time.Millisecond)

		if _, err := wB.Await(context.Background(), 10*time.Millisecond); err != nil {
			t.Fatalf("Await unexpected error: %v", err)
		}

		// Total elapsed since Enqueue is now ~3s, past the original 2s
		// SlotTTL; B's slot must have survived because the Await call above
		// refreshed it forward from its own exit time.
		time.Sleep(1500 * time.Millisecond)

		snap := tbl.Snapshot()
		found := false
		for _, p := range snap.Queue {
			if p.Session == "B" {
				found = true
			}
		}
		if !found {
			t.Fatalf("expected B's queue slot refreshed by Await, kept alive past the original SlotTTL, got %+v", snap.Queue)
		}
	})
}

func TestContextCancellationDuringAwaitPreservesPosition(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		tbl := lock.New()
		mustEnqueue(t, tbl, req("A", "pattern1", lock.Exclusive))
		mustEnqueue(t, tbl, req("D", "pattern1", lock.Exclusive))
		wB := mustEnqueue(t, tbl, req("B", "pattern1", lock.Exclusive))

		ctx, cancel := context.WithCancel(context.Background())
		ch := awaitAsync(wB, ctx, 10*time.Second)
		synctest.Wait()
		cancel()

		out := <-ch
		if !errors.Is(out.err, context.Canceled) {
			t.Fatalf("expected context.Canceled, got %v (result=%#v)", out.err, out.result)
		}

		snap := tbl.Snapshot()
		found := false
		for _, p := range snap.Queue {
			if p.Session == "B" {
				found = true
				if p.Position != 2 {
					t.Fatalf("expected B still at position 2, got %+v", p)
				}
			}
		}
		if !found {
			t.Fatalf("expected B's slot kept after ctx cancellation, got %+v", snap.Queue)
		}
	})
}

func TestAwaitWakesOnRelease(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		tbl := lock.New()
		mustEnqueue(t, tbl, req("A", "pattern1", lock.Exclusive))
		wB := mustEnqueue(t, tbl, req("B", "pattern1", lock.Exclusive))

		ch := awaitAsync(wB, context.Background(), 10*time.Second)
		synctest.Wait()

		snap := tbl.Snapshot()
		ga, _ := grantByToken(t, snap, "A")
		if err := tbl.Release(ga.Token); err != nil {
			t.Fatalf("Release(A) unexpected error: %v", err)
		}

		out := <-ch
		if out.err != nil {
			t.Fatalf("expected B granted after A released, got err %v", out.err)
		}
		if _, ok := out.result.(lock.Granted); !ok {
			t.Fatalf("expected Granted, got %#v", out.result)
		}
	})
}

func TestGrantTTLExpiryWakesBlockedAwait(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		tbl := lock.New()
		a := req("A", "pattern1", lock.Exclusive)
		a.TTL = 2 * time.Second
		mustEnqueue(t, tbl, a)

		wB := mustEnqueue(t, tbl, req("B", "pattern1", lock.Exclusive))
		ch := awaitAsync(wB, context.Background(), 10*time.Second)
		synctest.Wait()

		time.Sleep(3 * time.Second)

		out := <-ch
		if out.err != nil {
			t.Fatalf("expected B granted once A's grant TTL expired, got err %v", out.err)
		}
		if _, ok := out.result.(lock.Granted); !ok {
			t.Fatalf("expected Granted, got %#v", out.result)
		}
	})
}

func TestSlotDoesNotExpireDuringLongAwait(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		tbl := lock.New()
		a := req("A", "pattern1", lock.Exclusive)
		a.TTL = 5 * time.Second
		mustEnqueue(t, tbl, a)

		b := req("B", "pattern1", lock.Exclusive)
		b.SlotTTL = 1 * time.Second // would lapse at 1s if not suspended by Await
		wB := mustEnqueue(t, tbl, b)

		ch := awaitAsync(wB, context.Background(), 10*time.Second)
		synctest.Wait()

		out := <-ch // blocks until A's 5s grant TTL expires, well past B's 1s SlotTTL
		if out.err != nil {
			t.Fatalf("expected B granted once A's grant expired (slot TTL suspended during Await), got err %v", out.err)
		}
		if _, ok := out.result.(lock.Granted); !ok {
			t.Fatalf("expected Granted, got %#v", out.result)
		}
	})
}

func TestSlotTTLLapsesWithoutAwait(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		tbl := lock.New()
		mustEnqueue(t, tbl, req("A", "pattern1", lock.Exclusive))

		b := req("B", "pattern1", lock.Exclusive)
		b.SlotTTL = 1 * time.Second
		wB := mustEnqueue(t, tbl, b)
		mustEnqueue(t, tbl, req("C", "pattern1", lock.Exclusive))

		time.Sleep(2 * time.Second)

		res, err := wB.Await(context.Background(), 0)
		if !errors.Is(err, lock.ErrSlotExpired) {
			t.Fatalf("expected ErrSlotExpired, got err=%v result=%#v", err, res)
		}

		snap := tbl.Snapshot()
		for _, p := range snap.Queue {
			if p.Session == "C" && p.Position != 1 {
				t.Fatalf("expected C promoted to position 1 after B's slot expired, got %+v", p)
			}
		}
	})
}

func TestGrantBeatsCancelledContext(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		tbl := lock.New()
		wA := mustEnqueue(t, tbl, req("A", "pattern1", lock.Exclusive))
		if _, err := wA.Await(context.Background(), 0); err != nil {
			t.Fatalf("unexpected error granting A: %v", err)
		}

		ctx, cancel := context.WithCancel(context.Background())
		cancel() // already cancelled before Await starts

		res, err := wA.Await(ctx, 0)
		if err != nil {
			t.Fatalf("expected an active grant to beat an already-cancelled context, got err %v (result=%#v)", err, res)
		}
		if _, ok := res.(lock.Granted); !ok {
			t.Fatalf("expected Granted despite the cancelled context, got %#v", res)
		}
	})
}

func TestCancelRemovesQueuedWaiterAndUnblocksNext(t *testing.T) {
	tbl := lock.New()
	mustEnqueue(t, tbl, req("A", "pattern1", lock.Exclusive))
	wB := mustEnqueue(t, tbl, req("B", "pattern1", lock.Exclusive))
	mustEnqueue(t, tbl, req("C", "pattern1", lock.Exclusive))

	wB.Cancel()

	snap := tbl.Snapshot()
	for _, p := range snap.Queue {
		if p.Session == "B" {
			t.Fatalf("expected B removed from the queue after Cancel, got %+v", snap.Queue)
		}
	}

	ga, _ := grantByToken(t, snap, "A")
	if err := tbl.Release(ga.Token); err != nil {
		t.Fatalf("Release(A) unexpected error: %v", err)
	}
	snap = tbl.Snapshot()
	if _, ok := grantByToken(t, snap, "C"); !ok {
		t.Fatalf("expected C granted next, no longer blocked by B, got %+v", snap)
	}
	checkInvariants(t, snap)
}

func TestAwaitReturnsClosedAfterCancel(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		tbl := lock.New()
		mustEnqueue(t, tbl, req("A", "pattern1", lock.Exclusive))
		wB := mustEnqueue(t, tbl, req("B", "pattern1", lock.Exclusive))

		ch := awaitAsync(wB, context.Background(), 10*time.Second)
		synctest.Wait()

		wB.Cancel()

		out := <-ch
		if !errors.Is(out.err, lock.ErrWaiterClosed) {
			t.Fatalf("expected ErrWaiterClosed after a concurrent Cancel, got %v (result=%#v)", out.err, out.result)
		}
	})
}

func TestCancelOnGrantedReleasesGrant(t *testing.T) {
	tbl := lock.New()
	wA := mustEnqueue(t, tbl, req("A", "pattern1", lock.Exclusive))
	mustEnqueue(t, tbl, req("B", "pattern1", lock.Exclusive))

	wA.Cancel()

	snap := tbl.Snapshot()
	if _, ok := grantByToken(t, snap, "A"); ok {
		t.Fatalf("expected A's grant released by Cancel, got %+v", snap.Grants)
	}
	if _, ok := grantByToken(t, snap, "B"); !ok {
		t.Fatalf("expected B granted after A's grant was released via Cancel, got %+v", snap)
	}
}

func TestCancelIsIdempotent(t *testing.T) {
	tbl := lock.New()
	wA := mustEnqueue(t, tbl, req("A", "pattern1", lock.Exclusive))

	wA.Cancel()
	wA.Cancel() // must not panic or resurrect any state

	snap := tbl.Snapshot()
	if len(snap.Grants) != 0 || len(snap.Queue) != 0 {
		t.Fatalf("expected no residual state after a double Cancel, got %+v", snap)
	}
}

func TestConcurrentAwaitReturnsErrAwaitBusy(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		tbl := lock.New()
		mustEnqueue(t, tbl, req("A", "pattern1", lock.Exclusive))
		wB := mustEnqueue(t, tbl, req("B", "pattern1", lock.Exclusive))

		ch := awaitAsync(wB, context.Background(), 10*time.Second)
		synctest.Wait()

		if _, err := wB.Await(context.Background(), 0); !errors.Is(err, lock.ErrAwaitBusy) {
			t.Fatalf("expected ErrAwaitBusy for a concurrent Await, got %v", err)
		}

		snap := tbl.Snapshot()
		ga, _ := grantByToken(t, snap, "A")
		if err := tbl.Release(ga.Token); err != nil {
			t.Fatalf("Release(A) unexpected error: %v", err)
		}

		out := <-ch
		if out.err != nil {
			t.Fatalf("expected the original blocked Await to still complete normally, got err %v", out.err)
		}
		if _, ok := out.result.(lock.Granted); !ok {
			t.Fatalf("expected Granted, got %#v", out.result)
		}
	})
}

func TestSnapshotSlotExpiresZeroDuringAwait(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		tbl := lock.New()
		mustEnqueue(t, tbl, req("A", "pattern1", lock.Exclusive))
		wB := mustEnqueue(t, tbl, req("B", "pattern1", lock.Exclusive))

		ch := awaitAsync(wB, context.Background(), 10*time.Second)
		synctest.Wait()

		snap := tbl.Snapshot()
		found := false
		for _, p := range snap.Queue {
			if p.Session == "B" {
				found = true
				if !p.SlotExpires.IsZero() {
					t.Fatalf("expected B's SlotExpires zero while an Await is in progress, got %+v", p)
				}
			}
		}
		if !found {
			t.Fatalf("expected B queued, got %+v", snap.Queue)
		}

		ga, _ := grantByToken(t, snap, "A")
		if err := tbl.Release(ga.Token); err != nil {
			t.Fatalf("Release(A) unexpected error: %v", err)
		}
		<-ch
	})
}
