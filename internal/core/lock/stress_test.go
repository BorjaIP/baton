package lock_test

import (
	"context"
	"fmt"
	"math/rand"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/BorjaIP/baton/internal/core/lock"
)

// TestStressConcurrentEnqueueAwaitRelease runs many goroutines concurrently
// enqueueing, awaiting (with a bounded timeout/ctx deadline), and either
// releasing or cancelling their request against overlapping patterns, inside
// one synctest bubble. It exercises the design §7 invariants under -race and
// under goleak's no-leak check (main_test.go's TestMain), matching design §8
// scenario 27.
func TestStressConcurrentEnqueueAwaitRelease(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		tbl := lock.New()
		patterns := []string{"src/a/**", "src/b/**", "src/**"}
		modes := []lock.Mode{lock.Exclusive, lock.Shared}

		const goroutines = 30
		var wg sync.WaitGroup
		wg.Add(goroutines)

		for i := 0; i < goroutines; i++ {
			go func(i int) {
				defer wg.Done()
				rnd := rand.New(rand.NewSource(int64(i) + 1))

				r := lock.Request{
					Session: lock.SessionID(fmt.Sprintf("S%d", i)),
					Pattern: patterns[rnd.Intn(len(patterns))],
					Mode:    modes[rnd.Intn(len(modes))],
					TTL:     time.Duration(1+rnd.Intn(3)) * time.Second,
					SlotTTL: time.Duration(1+rnd.Intn(3)) * time.Second,
				}

				w, err := tbl.Enqueue(r)
				if err != nil {
					// Each goroutine uses a distinct session against a small
					// fixed pattern set, so no self-overlap is expected.
					t.Errorf("Enqueue(%+v) unexpected error: %v", r, err)
					return
				}

				ctx, cancel := context.WithTimeout(context.Background(), time.Duration(1+rnd.Intn(4))*time.Second)
				defer cancel()

				res, err := w.Await(ctx, time.Duration(rnd.Intn(3))*time.Second)
				if err != nil {
					// Timeout kept the slot; ctx deadline kept the slot;
					// ErrSlotExpired reached a terminal state. Abandon
					// cleanly via Cancel either way (idempotent, safe on any
					// state).
					w.Cancel()
					return
				}
				if g, ok := res.(lock.Granted); ok {
					time.Sleep(time.Duration(rnd.Intn(2)) * time.Second)
					_ = tbl.Release(g.Token)
					return
				}
				// Queued (timed out before deadline elapsed further): abandon.
				w.Cancel()
			}(i)
		}

		wg.Wait()
		checkInvariants(t, tbl.Snapshot())
	})
}
