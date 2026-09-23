package lock_test

import (
	"errors"
	"testing"
	"time"

	"github.com/BorjaIP/baton/internal/core/lock"
)

func TestEnqueueMultipleSharedHolders(t *testing.T) {
	tbl := lock.New()

	mustEnqueue(t, tbl, req("A", "src/auth/**", lock.Shared))
	mustEnqueue(t, tbl, req("B", "src/auth/**", lock.Shared))

	snap := tbl.Snapshot()
	if len(snap.Grants) != 2 {
		t.Fatalf("expected 2 concurrent shared grants, got %d: %+v", len(snap.Grants), snap.Grants)
	}
	if len(snap.Queue) != 0 {
		t.Fatalf("expected empty queue, got %+v", snap.Queue)
	}
	ga, aok := grantByToken(t, snap, "A")
	gb, bok := grantByToken(t, snap, "B")
	if !aok || !bok {
		t.Fatalf("expected both A and B to hold grants, got %+v", snap.Grants)
	}
	if ga.Token == gb.Token {
		t.Fatalf("expected distinct fencing tokens, both got %d", ga.Token)
	}
}

func TestEnqueueExclusiveBlocksOnSharedHolder(t *testing.T) {
	tbl := lock.New()
	mustEnqueue(t, tbl, req("A", "src/auth/**", lock.Shared))
	mustEnqueue(t, tbl, req("B", "src/auth/**", lock.Exclusive))

	snap := tbl.Snapshot()
	if len(snap.Grants) != 1 {
		t.Fatalf("expected only A granted, got %+v", snap.Grants)
	}
	if len(snap.Queue) != 1 || snap.Queue[0].Session != "B" {
		t.Fatalf("expected B queued, got %+v", snap.Queue)
	}
}

func TestEnqueueSharedBlocksOnExclusiveHolder(t *testing.T) {
	tbl := lock.New()
	mustEnqueue(t, tbl, req("A", "src/auth/**", lock.Exclusive))
	mustEnqueue(t, tbl, req("B", "src/auth/**", lock.Shared))

	snap := tbl.Snapshot()
	if len(snap.Grants) != 1 {
		t.Fatalf("expected only A granted, got %+v", snap.Grants)
	}
	if len(snap.Queue) != 1 || snap.Queue[0].Session != "B" {
		t.Fatalf("expected B queued, got %+v", snap.Queue)
	}
}

func TestFIFONoWriterStarvation(t *testing.T) {
	tbl := lock.New()
	mustEnqueue(t, tbl, req("A", "src/auth/**", lock.Shared))
	mustEnqueue(t, tbl, req("B", "src/auth/**", lock.Exclusive))
	mustEnqueue(t, tbl, req("C", "src/auth/**", lock.Shared))

	snap := tbl.Snapshot()
	if len(snap.Grants) != 1 {
		t.Fatalf("expected only A granted, got %+v", snap.Grants)
	}
	if len(snap.Queue) != 2 || snap.Queue[0].Session != "B" || snap.Queue[1].Session != "C" {
		t.Fatalf("expected B then C queued in FIFO order, got %+v", snap.Queue)
	}

	// Release A: B should be granted next, C remains queued behind B.
	ga, _ := grantByToken(t, snap, "A")
	if err := tbl.Release(ga.Token); err != nil {
		t.Fatalf("Release(A) unexpected error: %v", err)
	}
	snap = tbl.Snapshot()
	gb, bok := grantByToken(t, snap, "B")
	if !bok {
		t.Fatalf("expected B granted after A released, got %+v", snap)
	}
	if len(snap.Queue) != 1 || snap.Queue[0].Session != "C" {
		t.Fatalf("expected C still queued behind B, got %+v", snap.Queue)
	}

	// Release B: C should be granted next.
	if err := tbl.Release(gb.Token); err != nil {
		t.Fatalf("Release(B) unexpected error: %v", err)
	}
	snap = tbl.Snapshot()
	if _, cok := grantByToken(t, snap, "C"); !cok {
		t.Fatalf("expected C granted after B released, got %+v", snap)
	}
	if len(snap.Queue) != 0 {
		t.Fatalf("expected empty queue, got %+v", snap.Queue)
	}
	checkInvariants(t, snap)
}

func TestFIFOOrderPreservedAcrossReleases(t *testing.T) {
	tbl := lock.New()
	wD1 := mustEnqueue(t, tbl, req("D1", "pattern1", lock.Exclusive))
	mustEnqueue(t, tbl, req("D2", "pattern1", lock.Exclusive))
	mustEnqueue(t, tbl, req("D3", "pattern1", lock.Exclusive))
	_ = wD1

	snap := tbl.Snapshot()
	gd1, ok := grantByToken(t, snap, "D1")
	if !ok {
		t.Fatalf("expected D1 granted, got %+v", snap)
	}

	if err := tbl.Release(gd1.Token); err != nil {
		t.Fatalf("Release(D1) unexpected error: %v", err)
	}
	snap = tbl.Snapshot()
	gd2, ok := grantByToken(t, snap, "D2")
	if !ok {
		t.Fatalf("expected D2 granted next, got %+v", snap)
	}

	if err := tbl.Release(gd2.Token); err != nil {
		t.Fatalf("Release(D2) unexpected error: %v", err)
	}
	snap = tbl.Snapshot()
	if _, ok := grantByToken(t, snap, "D3"); !ok {
		t.Fatalf("expected D3 granted last, got %+v", snap)
	}
}

func TestReleaseGrantsMultipleConsecutiveCompatibleWaiters(t *testing.T) {
	tbl := lock.New()
	wA := mustEnqueue(t, tbl, req("A", "pattern1", lock.Exclusive))
	_ = wA
	mustEnqueue(t, tbl, req("B", "pattern1", lock.Shared))
	mustEnqueue(t, tbl, req("C", "pattern1", lock.Shared))
	mustEnqueue(t, tbl, req("D", "pattern1", lock.Shared))

	snap := tbl.Snapshot()
	ga, _ := grantByToken(t, snap, "A")
	if err := tbl.Release(ga.Token); err != nil {
		t.Fatalf("Release(A) unexpected error: %v", err)
	}

	snap = tbl.Snapshot()
	if len(snap.Grants) != 3 {
		t.Fatalf("expected B, C, D all granted together, got %+v", snap.Grants)
	}
	gb, _ := grantByToken(t, snap, "B")
	gc, _ := grantByToken(t, snap, "C")
	gd, _ := grantByToken(t, snap, "D")
	if !(gb.Token < gc.Token && gc.Token < gd.Token) {
		t.Fatalf("expected strictly increasing tokens B<C<D, got B=%d C=%d D=%d", gb.Token, gc.Token, gd.Token)
	}
	if len(snap.Queue) != 0 {
		t.Fatalf("expected empty queue, got %+v", snap.Queue)
	}
}

func TestReleaseStopsGrantingAtNextIncompatibleRequest(t *testing.T) {
	tbl := lock.New()
	wA := mustEnqueue(t, tbl, req("A", "pattern1", lock.Exclusive))
	_ = wA
	mustEnqueue(t, tbl, req("B", "pattern1", lock.Shared))
	mustEnqueue(t, tbl, req("C", "pattern1", lock.Shared))
	mustEnqueue(t, tbl, req("E", "pattern1", lock.Exclusive))
	mustEnqueue(t, tbl, req("F", "pattern1", lock.Shared))

	snap := tbl.Snapshot()
	ga, _ := grantByToken(t, snap, "A")
	if err := tbl.Release(ga.Token); err != nil {
		t.Fatalf("Release(A) unexpected error: %v", err)
	}

	snap = tbl.Snapshot()
	if len(snap.Grants) != 2 {
		t.Fatalf("expected B and C granted, got %+v", snap.Grants)
	}
	if _, ok := grantByToken(t, snap, "B"); !ok {
		t.Fatalf("expected B granted, got %+v", snap.Grants)
	}
	if _, ok := grantByToken(t, snap, "C"); !ok {
		t.Fatalf("expected C granted, got %+v", snap.Grants)
	}
	if len(snap.Queue) != 2 || snap.Queue[0].Session != "E" || snap.Queue[1].Session != "F" {
		t.Fatalf("expected E then F still queued in order, got %+v", snap.Queue)
	}
	checkInvariants(t, snap)
}

func TestSelfOverlapRejectedWhileHolding(t *testing.T) {
	tbl := lock.New()
	mustEnqueue(t, tbl, req("A", "src/auth/**", lock.Shared))

	_, err := tbl.Enqueue(req("A", "src/auth/**", lock.Exclusive))
	if err != lock.ErrSessionOverlap {
		t.Fatalf("expected ErrSessionOverlap, got %v", err)
	}

	snap := tbl.Snapshot()
	if len(snap.Grants) != 1 {
		t.Fatalf("expected A's original grant unaffected, got %+v", snap.Grants)
	}
}

func TestSelfOverlapRejectedWhileQueued(t *testing.T) {
	tbl := lock.New()
	mustEnqueue(t, tbl, req("Z", "pattern1", lock.Exclusive))
	mustEnqueue(t, tbl, req("A", "pattern1", lock.Exclusive))

	_, err := tbl.Enqueue(req("A", "pattern1", lock.Shared))
	if err != lock.ErrSessionOverlap {
		t.Fatalf("expected ErrSessionOverlap, got %v", err)
	}

	snap := tbl.Snapshot()
	if len(snap.Queue) != 1 || snap.Queue[0].Session != "A" {
		t.Fatalf("expected A's original queued request unaffected, got %+v", snap.Queue)
	}
}

func TestSelfOverlapSharedSharedAlsoRejected(t *testing.T) {
	tbl := lock.New()
	mustEnqueue(t, tbl, req("A", "src/auth/**", lock.Shared))

	_, err := tbl.Enqueue(req("A", "src/auth/**", lock.Shared))
	if err != lock.ErrSessionOverlap {
		t.Fatalf("expected ErrSessionOverlap for same-session shared+shared overlap, got %v", err)
	}
}

func TestSameSessionMayEnqueueNonOverlappingPattern(t *testing.T) {
	tbl := lock.New()
	mustEnqueue(t, tbl, req("A", "src/auth/**", lock.Exclusive))

	w, err := tbl.Enqueue(req("A", "src/billing/**", lock.Exclusive))
	if err != nil {
		t.Fatalf("expected non-overlapping request to succeed, got %v", err)
	}
	if w == nil {
		t.Fatalf("expected a non-nil waiter")
	}

	snap := tbl.Snapshot()
	if len(snap.Grants) != 2 {
		t.Fatalf("expected both A grants active, got %+v", snap.Grants)
	}
}

func TestRequestBlockedByOneOfSeveralOverlappingHolders(t *testing.T) {
	tbl := lock.New()
	mustEnqueue(t, tbl, req("A", "src/**", lock.Shared))
	mustEnqueue(t, tbl, req("B", "src/auth/session/**", lock.Exclusive))

	mustEnqueue(t, tbl, req("C", "src/auth/session/**", lock.Shared))

	snap := tbl.Snapshot()
	if _, ok := grantByToken(t, snap, "C"); ok {
		t.Fatalf("expected C NOT granted (blocked by B's exclusive grant), got %+v", snap.Grants)
	}
	found := false
	for _, p := range snap.Queue {
		if p.Session == "C" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected C queued, got %+v", snap.Queue)
	}
}

func TestRequestGrantedOnlyWhenAllOverlappingConstraintsClear(t *testing.T) {
	tbl := lock.New()
	mustEnqueue(t, tbl, req("A", "src/**", lock.Shared))
	mustEnqueue(t, tbl, req("B", "src/auth/**", lock.Shared))

	mustEnqueue(t, tbl, req("C", "src/auth/**", lock.Shared))

	snap := tbl.Snapshot()
	if _, ok := grantByToken(t, snap, "C"); !ok {
		t.Fatalf("expected C granted immediately (compatible with A and B), got %+v", snap)
	}
}

func TestEnqueueRejectsMissingTTL(t *testing.T) {
	tbl := lock.New()
	bad := req("A", "pattern1", lock.Exclusive)
	bad.TTL = 0

	_, err := tbl.Enqueue(bad)
	if !errors.Is(err, lock.ErrInvalidRequest) {
		t.Fatalf("expected ErrInvalidRequest for zero TTL, got %v", err)
	}
}

func TestEnqueueRejectsMissingSlotTTL(t *testing.T) {
	tbl := lock.New()
	bad := req("A", "pattern1", lock.Exclusive)
	bad.SlotTTL = 0

	_, err := tbl.Enqueue(bad)
	if !errors.Is(err, lock.ErrInvalidRequest) {
		t.Fatalf("expected ErrInvalidRequest for zero SlotTTL, got %v", err)
	}
}

func TestEnqueueRejectsInvalidMode(t *testing.T) {
	tbl := lock.New()
	bad := req("A", "pattern1", lock.Mode(0))

	_, err := tbl.Enqueue(bad)
	if !errors.Is(err, lock.ErrInvalidRequest) {
		t.Fatalf("expected ErrInvalidRequest for invalid mode, got %v", err)
	}
}

func TestEnqueueRejectsEmptySessionOrPattern(t *testing.T) {
	tbl := lock.New()

	if _, err := tbl.Enqueue(req("", "pattern1", lock.Exclusive)); !errors.Is(err, lock.ErrInvalidRequest) {
		t.Fatalf("expected ErrInvalidRequest for empty session, got %v", err)
	}
	if _, err := tbl.Enqueue(req("A", "", lock.Exclusive)); !errors.Is(err, lock.ErrInvalidRequest) {
		t.Fatalf("expected ErrInvalidRequest for empty pattern, got %v", err)
	}
}

func TestSnapshotReflectsGrantsAndQueue(t *testing.T) {
	tbl := lock.New()
	mustEnqueue(t, tbl, req("A", "pattern1", lock.Exclusive))
	mustEnqueue(t, tbl, req("B", "pattern1", lock.Exclusive))

	snap := tbl.Snapshot()
	ga, aok := grantByToken(t, snap, "A")
	if !aok {
		t.Fatalf("expected A's grant in snapshot, got %+v", snap.Grants)
	}
	if ga.Pattern != "pattern1" || ga.Token == 0 || ga.Expires.IsZero() {
		t.Fatalf("expected A's grant to have pattern/token/expires populated, got %+v", ga)
	}
	if len(snap.Queue) != 1 || snap.Queue[0].Session != "B" || snap.Queue[0].Pattern != "pattern1" {
		t.Fatalf("expected B's queued entry populated, got %+v", snap.Queue)
	}
}

// --- Phase 3: fencing tokens, Renew, ReleaseSession ---

func TestSequentialGrantsReceiveStrictlyIncreasingTokens(t *testing.T) {
	tbl := lock.New()
	mustEnqueue(t, tbl, req("A", "pattern1", lock.Exclusive))
	mustEnqueue(t, tbl, req("B", "pattern2", lock.Shared))

	snap := tbl.Snapshot()
	ga, _ := grantByToken(t, snap, "A")
	gb, _ := grantByToken(t, snap, "B")
	if !(gb.Token > ga.Token) {
		t.Fatalf("expected B's token > A's token, got A=%d B=%d", ga.Token, gb.Token)
	}
}

func TestStaleTokenRejectedOnReleaseAfterRegrant(t *testing.T) {
	tbl := lock.New()
	mustEnqueue(t, tbl, req("A", "pattern1", lock.Exclusive))
	snap := tbl.Snapshot()
	ga, _ := grantByToken(t, snap, "A")
	t1 := ga.Token

	if err := tbl.Release(t1); err != nil {
		t.Fatalf("Release(T1) unexpected error: %v", err)
	}
	mustEnqueue(t, tbl, req("A", "pattern1", lock.Exclusive))
	snap = tbl.Snapshot()
	ga2, _ := grantByToken(t, snap, "A")
	t2 := ga2.Token

	if err := tbl.Release(t1); !errors.Is(err, lock.ErrStaleToken) {
		t.Fatalf("expected ErrStaleToken releasing superseded T1, got %v", err)
	}

	snap = tbl.Snapshot()
	if _, ok := grantByToken(t, snap, "A"); !ok {
		t.Fatalf("expected the grant under T2 to remain active, got %+v", snap.Grants)
	}
	if _, err := tbl.Validate(t2); err != nil {
		t.Fatalf("expected T2 to remain valid, got %v", err)
	}
}

func TestUnknownTokenRejected(t *testing.T) {
	tbl := lock.New()

	if err := tbl.Release(999999); !errors.Is(err, lock.ErrUnknownToken) {
		t.Fatalf("expected ErrUnknownToken releasing an unissued token, got %v", err)
	}
	if _, err := tbl.Renew(999999, time.Second); !errors.Is(err, lock.ErrUnknownToken) {
		t.Fatalf("expected ErrUnknownToken renewing an unissued token, got %v", err)
	}
	if _, err := tbl.Validate(0); !errors.Is(err, lock.ErrUnknownToken) {
		t.Fatalf("expected ErrUnknownToken validating token 0, got %v", err)
	}
}

func TestRenewExtendsExpiresAndPreservesToken(t *testing.T) {
	tbl := lock.New()
	r := req("A", "pattern1", lock.Exclusive)
	r.TTL = 5 * time.Second
	mustEnqueue(t, tbl, r)

	snap := tbl.Snapshot()
	ga, _ := grantByToken(t, snap, "A")

	newExpires, err := tbl.Renew(ga.Token, 20*time.Second)
	if err != nil {
		t.Fatalf("Renew unexpected error: %v", err)
	}
	if !newExpires.After(ga.Expires) {
		t.Fatalf("expected renewed Expires (%v) after original (%v)", newExpires, ga.Expires)
	}

	snap = tbl.Snapshot()
	ga2, ok := grantByToken(t, snap, "A")
	if !ok {
		t.Fatalf("expected A still granted after renew, got %+v", snap.Grants)
	}
	if ga2.Token != ga.Token {
		t.Fatalf("expected token to remain %d after renew, got %d", ga.Token, ga2.Token)
	}
}

func TestRenewWithStaleOrUnknownTokenFailsWithoutSideEffects(t *testing.T) {
	tbl := lock.New()
	mustEnqueue(t, tbl, req("A", "pattern1", lock.Exclusive))
	snap := tbl.Snapshot()
	ga, _ := grantByToken(t, snap, "A")
	t1 := ga.Token

	if err := tbl.Release(t1); err != nil {
		t.Fatalf("Release(T1) unexpected error: %v", err)
	}
	mustEnqueue(t, tbl, req("A", "pattern1", lock.Exclusive))
	snap = tbl.Snapshot()
	ga2, _ := grantByToken(t, snap, "A")
	t2 := ga2.Token

	if _, err := tbl.Renew(t1, 5*time.Second); !errors.Is(err, lock.ErrStaleToken) {
		t.Fatalf("expected ErrStaleToken renewing superseded T1, got %v", err)
	}

	snap = tbl.Snapshot()
	ga3, ok := grantByToken(t, snap, "A")
	if !ok || ga3.Token != t2 || ga3.Expires != ga2.Expires {
		t.Fatalf("expected T2's grant unaffected by the failed renew, got %+v (want token=%d expires=%v)", ga3, t2, ga2.Expires)
	}
}

func TestReleaseSessionFreesGrantsAndQueuedSlots(t *testing.T) {
	tbl := lock.New()
	mustEnqueue(t, tbl, req("A", "pattern1", lock.Exclusive))
	mustEnqueue(t, tbl, req("A", "pattern2", lock.Shared))
	mustEnqueue(t, tbl, req("B", "pattern2", lock.Exclusive))

	tbl.ReleaseSession("A")

	snap := tbl.Snapshot()
	if _, ok := grantByToken(t, snap, "A"); ok {
		t.Fatalf("expected A's grant released, got %+v", snap.Grants)
	}
	found := false
	for _, p := range snap.Queue {
		if p.Session == "A" {
			found = true
		}
	}
	if found {
		t.Fatalf("expected A's queued request removed, got %+v", snap.Queue)
	}
	if _, ok := grantByToken(t, snap, "B"); !ok {
		t.Fatalf("expected B granted after A's session released pattern2, got %+v", snap.Grants)
	}
}

func TestReleaseSessionOnSessionWithNoLocksIsNoOp(t *testing.T) {
	tbl := lock.New()
	mustEnqueue(t, tbl, req("A", "pattern1", lock.Exclusive))

	tbl.ReleaseSession("Z")

	snap := tbl.Snapshot()
	if _, ok := grantByToken(t, snap, "A"); !ok {
		t.Fatalf("expected A's grant unaffected by no-op ReleaseSession, got %+v", snap.Grants)
	}
}
