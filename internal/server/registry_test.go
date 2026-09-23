package server

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/BorjaIP/baton/internal/core/lock"
)

func newReq(session lock.SessionID, key string, mode lock.Mode) lock.Request {
	return lock.Request{
		Session: session,
		Pattern: key,
		Mode:    mode,
		TTL:     5 * time.Minute,
		SlotTTL: 30 * time.Second,
	}
}

func TestRegistry_StableSessionResumesPendingWaiter(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		table := lock.New()
		reg := &registry{m: make(map[regKey]*regEntry)}

		key := scopeKey("/repo", "src/auth/**")
		rk := regKey{session: "S1", scope: key}

		// First acquire: session S1 holds nothing yet, so it gets a fresh
		// enqueue and (since nothing else holds the pattern) an immediate
		// grant, leaving the registry entry present but delivered=false.
		w1, resumed1, err := reg.obtain(table, rk, newReq("S1", key, lock.Exclusive), 1, false)
		if err != nil {
			t.Fatalf("obtain #1: %v", err)
		}
		if resumed1 {
			t.Fatal("first obtain should not be resumed")
		}

		// A second session queues behind S1 by holding the same key with a
		// blocking session (simulate by having S1 hold and S2 queue).
		key2 := scopeKey("/repo", "src/other/**")
		rk2 := regKey{session: "S2", scope: key2}
		w2, resumed2, err := reg.obtain(table, rk2, newReq("S2", key2, lock.Exclusive), 2, false)
		if err != nil {
			t.Fatalf("obtain S2: %v", err)
		}
		if resumed2 {
			t.Fatal("S2's first obtain should not be resumed")
		}
		_ = w2

		// Now S1 re-acquires the SAME key+mode from a new connection. It
		// must resume the existing waiter, not enqueue again.
		w1b, resumed1b, err := reg.obtain(table, rk, newReq("S1", key, lock.Exclusive), 3, false)
		if err != nil {
			t.Fatalf("obtain #2 (resume): %v", err)
		}
		if !resumed1b {
			t.Fatal("expected resume=true on second obtain for the same session+scope+mode")
		}
		if w1b != w1 {
			t.Fatal("resumed obtain must return the SAME *lock.Waiter handle")
		}
	})
}

func TestRegistry_TerminalWaiterDiscardedAndReplaced(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		table := lock.New()
		reg := &registry{m: make(map[regKey]*regEntry)}

		key := scopeKey("/repo", "src/auth/**")
		rk := regKey{session: "S1", scope: key}

		req := newReq("S1", key, lock.Exclusive)
		req.TTL = 1 * time.Second
		req.SlotTTL = 1 * time.Second

		// Grant it to S1, then let a competing session queue and let the
		// original grant expire so the waiter transitions to a terminal
		// state we can observe via a fresh obtain.
		w1, _, err := reg.obtain(table, rk, req, 1, false)
		if err != nil {
			t.Fatalf("obtain: %v", err)
		}

		// Force S1's grant to expire by advancing time past its TTL, using a
		// direct Await which also triggers the table's lazy sweep.
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if _, err := w1.Await(ctx, 0); err != nil {
			t.Fatalf("Await (initial grant check): %v", err)
		}

		time.Sleep(2 * time.Second)
		synctest.Wait()

		// A second session takes the pattern once it's free, forcing S1's
		// waiter to be observed as ended (stale) on next lookup.
		key2 := scopeKey("/repo", "src/auth/**") // same key as S1's expired grant
		rk2 := regKey{session: "S2", scope: key2}
		req2 := newReq("S2", key2, lock.Exclusive)
		if _, _, err := reg.obtain(table, rk2, req2, 2, false); err != nil {
			t.Fatalf("obtain S2 after S1 expiry: %v", err)
		}

		// S1 re-acquires the same pattern: its old entry is terminal
		// (ended), so this must discard it and enqueue fresh rather than
		// resuming a dead waiter.
		w1new, resumed, err := reg.obtain(table, rk, newReq("S1", key, lock.Exclusive), 3, false)
		if err != nil {
			t.Fatalf("obtain S1 fresh: %v", err)
		}
		if resumed {
			t.Fatal("expected a fresh enqueue (resumed=false) after the old waiter went terminal")
		}
		if w1new == w1 {
			t.Fatal("expected a NEW waiter handle after discarding the terminal one")
		}
	})
}

func TestRegistry_DifferentModeNotResumed(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		table := lock.New()
		reg := &registry{m: make(map[regKey]*regEntry)}

		key := scopeKey("/repo", "src/auth/**")
		rk := regKey{session: "S1", scope: key}

		if _, _, err := reg.obtain(table, rk, newReq("S1", key, lock.Shared), 1, false); err != nil {
			t.Fatalf("obtain shared: %v", err)
		}

		_, _, err := reg.obtain(table, rk, newReq("S1", key, lock.Exclusive), 1, false)
		if !errors.Is(err, lock.ErrSessionOverlap) {
			t.Fatalf("err = %v, want ErrSessionOverlap for a mode mismatch on the same pattern", err)
		}
	})
}

func TestRegistry_IdempotentReacquireAfterGrant(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		table := lock.New()
		reg := &registry{m: make(map[regKey]*regEntry)}

		key := scopeKey("/repo", "src/auth/**")
		rk := regKey{session: "S1", scope: key}

		w, resumed, err := reg.obtain(table, rk, newReq("S1", key, lock.Exclusive), 1, false)
		if err != nil {
			t.Fatalf("obtain: %v", err)
		}
		if resumed {
			t.Fatal("first obtain should not be resumed")
		}

		res, err := w.Await(context.Background(), 0)
		if err != nil {
			t.Fatalf("Await: %v", err)
		}
		granted, ok := res.(lock.Granted)
		if !ok {
			t.Fatalf("result = %T, want lock.Granted", res)
		}
		reg.markDelivered(rk, w, granted.Token)

		w2, resumed2, err := reg.obtain(table, rk, newReq("S1", key, lock.Exclusive), 2, false)
		if err != nil {
			t.Fatalf("re-obtain after grant: %v", err)
		}
		if !resumed2 {
			t.Fatal("expected resumed=true when re-acquiring an already-granted waiter")
		}
		if w2 != w {
			t.Fatal("expected the SAME waiter handle for idempotent re-acquire")
		}

		res2, err := w2.Await(context.Background(), 0)
		if err != nil {
			t.Fatalf("Await on resumed waiter: %v", err)
		}
		granted2, ok := res2.(lock.Granted)
		if !ok {
			t.Fatalf("result = %T, want lock.Granted", res2)
		}
		if granted2.Token != granted.Token {
			t.Fatalf("token = %d, want the same existing token %d", granted2.Token, granted.Token)
		}
	})
}

func TestRegistry_CloseConnEphemeralCancelsUndelivered(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		table := lock.New()
		reg := &registry{m: make(map[regKey]*regEntry)}

		key := scopeKey("/repo", "src/auth/**")
		rk := regKey{session: "S1", scope: key}
		connID := uint64(1)

		w, _, err := reg.obtain(table, rk, newReq("S1", key, lock.Exclusive), connID, true)
		if err != nil {
			t.Fatalf("obtain: %v", err)
		}
		// Grant it but do NOT mark delivered, simulating a grant that raced
		// a dead connection (design ADR-4).
		if _, err := w.Await(context.Background(), 0); err != nil {
			t.Fatalf("Await: %v", err)
		}

		reg.closeConn(connID, true)

		// The waiter must now be closed: a further Await returns
		// ErrWaiterClosed.
		if _, err := w.Await(context.Background(), 0); !errors.Is(err, lock.ErrWaiterClosed) {
			t.Fatalf("Await after ephemeral closeConn: err = %v, want ErrWaiterClosed", err)
		}
	})
}

func TestRegistry_CloseConnEphemeralKeepsDelivered(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		table := lock.New()
		reg := &registry{m: make(map[regKey]*regEntry)}

		key := scopeKey("/repo", "src/auth/**")
		rk := regKey{session: "S1", scope: key}
		connID := uint64(1)

		w, _, err := reg.obtain(table, rk, newReq("S1", key, lock.Exclusive), connID, true)
		if err != nil {
			t.Fatalf("obtain: %v", err)
		}
		res, err := w.Await(context.Background(), 0)
		if err != nil {
			t.Fatalf("Await: %v", err)
		}
		granted := res.(lock.Granted)
		reg.markDelivered(rk, w, granted.Token)

		reg.closeConn(connID, true)

		// The grant must remain active: Table.Validate still resolves it.
		if _, err := table.Validate(granted.Token); err != nil {
			t.Fatalf("Validate(%d) after ephemeral closeConn with delivered grant: %v", granted.Token, err)
		}
	})
}

func TestRegistry_StableCloseConnIsNoOp(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		table := lock.New()
		reg := &registry{m: make(map[regKey]*regEntry)}

		key := scopeKey("/repo", "src/auth/**")
		rk := regKey{session: "S1", scope: key}
		connID := uint64(1)

		w, _, err := reg.obtain(table, rk, newReq("S1", key, lock.Exclusive), connID, false)
		if err != nil {
			t.Fatalf("obtain: %v", err)
		}

		reg.closeConn(connID, false)

		// The waiter must be unaffected: still resumable.
		w2, resumed, err := reg.obtain(table, rk, newReq("S1", key, lock.Exclusive), 2, false)
		if err != nil {
			t.Fatalf("re-obtain after stable closeConn: %v", err)
		}
		if !resumed || w2 != w {
			t.Fatalf("expected the stable session's waiter to remain resumable after closeConn, resumed=%v w2==w1:%v", resumed, w2 == w)
		}
	})
}

func TestRegistry_ForgetTokenRemovesEntry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		table := lock.New()
		reg := &registry{m: make(map[regKey]*regEntry)}

		key := scopeKey("/repo", "src/auth/**")
		rk := regKey{session: "S1", scope: key}

		w, _, err := reg.obtain(table, rk, newReq("S1", key, lock.Exclusive), 1, false)
		if err != nil {
			t.Fatalf("obtain: %v", err)
		}
		res, err := w.Await(context.Background(), 0)
		if err != nil {
			t.Fatalf("Await: %v", err)
		}
		granted := res.(lock.Granted)
		reg.markDelivered(rk, w, granted.Token)

		reg.forgetToken(granted.Token)

		if _, ok := reg.m[rk]; ok {
			t.Fatal("expected forgetToken to remove the registry entry for this token")
		}
	})
}

func TestRegistry_PruneLockedDropsAbsentEntries(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		table := lock.New()
		reg := &registry{m: make(map[regKey]*regEntry)}

		key := scopeKey("/repo", "src/auth/**")
		rk := regKey{session: "S1", scope: key}

		req := newReq("S1", key, lock.Exclusive)
		req.TTL = 1 * time.Second
		w, _, err := reg.obtain(table, rk, req, 1, false)
		if err != nil {
			t.Fatalf("obtain: %v", err)
		}
		if _, err := w.Await(context.Background(), 0); err != nil {
			t.Fatalf("Await: %v", err)
		}

		time.Sleep(2 * time.Second)
		synctest.Wait()

		reg.pruneLocked(table.Snapshot())

		if _, ok := reg.m[rk]; ok {
			t.Fatal("expected pruneLocked to drop an entry absent from a fresh Snapshot")
		}
	})
}
