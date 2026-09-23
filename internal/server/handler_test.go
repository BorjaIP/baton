package server

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/BorjaIP/baton/internal/core/lock"
	"github.com/BorjaIP/baton/internal/proto"
)

// fakeConnState is the minimal per-connection state handler.acquire needs,
// standing in for the real conn type (built in Work Unit 3) so registry and
// handler logic can be exercised without any socket.
type fakeConnState struct {
	id        uint64
	session   lock.SessionID
	ephemeral bool
}

func (c *fakeConnState) connID() uint64            { return c.id }
func (c *fakeConnState) sessionID() lock.SessionID { return c.session }
func (c *fakeConnState) isEphemeral() bool         { return c.ephemeral }
func (c *fakeConnState) setSession(id lock.SessionID, ephemeral bool) {
	c.session = id
	c.ephemeral = ephemeral
}

func newHandler() *handler {
	return &handler{
		table:   lock.New(),
		reg:     &registry{m: make(map[regKey]*regEntry)},
		pid:     4242,
		version: "v1",
	}
}

func validAcquireReq(root, pattern string) proto.AcquireRequest {
	return proto.AcquireRequest{
		Root:      root,
		Pattern:   pattern,
		Mode:      "exclusive",
		TTLMs:     int64(5 * time.Minute / time.Millisecond),
		SlotTTLMs: int64(30 * time.Second / time.Millisecond),
		WaitMs:    0,
	}
}

func TestHandler_Hello_SetsSessionAndEphemeral(t *testing.T) {
	h := newHandler()
	c := &fakeConnState{id: 1}

	resp, perr := h.hello(c, proto.HelloRequest{ProtoVersion: proto.Version, Agent: "a", Vendor: "v", SessionID: "S1", PID: 99})
	if perr != nil {
		t.Fatalf("hello: %v", perr)
	}
	if resp.SessionID != "S1" || resp.Ephemeral {
		t.Fatalf("hello response = %+v, want SessionID=S1, Ephemeral=false", resp)
	}
	if c.session != "S1" || c.ephemeral {
		t.Fatalf("conn state after hello: session=%q ephemeral=%v, want S1/false", c.session, c.ephemeral)
	}
}

func TestHandler_Hello_EmptySessionIsEphemeral(t *testing.T) {
	h := newHandler()
	c := &fakeConnState{id: 1}

	resp, perr := h.hello(c, proto.HelloRequest{ProtoVersion: proto.Version, Agent: "a", Vendor: "v", PID: 99})
	if perr != nil {
		t.Fatalf("hello: %v", perr)
	}
	if resp.SessionID == "" {
		t.Fatal("expected a generated session id for an ephemeral hello")
	}
	if !resp.Ephemeral || !c.ephemeral {
		t.Fatalf("expected ephemeral=true, got response=%+v conn.ephemeral=%v", resp, c.ephemeral)
	}
}

func TestHandler_Hello_VersionMismatchReturnsError(t *testing.T) {
	h := newHandler()
	c := &fakeConnState{id: 1}

	_, perr := h.hello(c, proto.HelloRequest{ProtoVersion: 99, ProtoMin: 99, Agent: "a", Vendor: "v", SessionID: "S1", PID: 99})
	if perr == nil || perr.Code != proto.CodeVersionMismatch {
		t.Fatalf("hello with unsupported version: perr = %v, want version_mismatch", perr)
	}
	if perr.Detail["client_max"] != "99" || perr.Detail["server_max"] != "1" {
		t.Fatalf("version_mismatch detail = %+v, want client_max=99 server_max=1", perr.Detail)
	}
}

func TestHandler_Acquire_ValidationErrors(t *testing.T) {
	h := newHandler()
	c := &fakeConnState{id: 1, session: "S1"}

	tests := []struct {
		name string
		req  proto.AcquireRequest
	}{
		{"non-positive ttl", func() proto.AcquireRequest { r := validAcquireReq("/repo", "src/**"); r.TTLMs = 0; return r }()},
		{"non-positive slot ttl", func() proto.AcquireRequest { r := validAcquireReq("/repo", "src/**"); r.SlotTTLMs = 0; return r }()},
		{"negative wait", func() proto.AcquireRequest { r := validAcquireReq("/repo", "src/**"); r.WaitMs = -1; return r }()},
		{"malformed root", func() proto.AcquireRequest { r := validAcquireReq("relative/root", "src/**"); return r }()},
		{"malformed pattern", func() proto.AcquireRequest { r := validAcquireReq("/repo", "../escape"); return r }()},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, perr := h.acquire(context.Background(), c, tt.req)
			if perr == nil || perr.Code != proto.CodeInvalidRequest {
				t.Fatalf("acquire(%+v) perr = %v, want invalid_request", tt.req, perr)
			}
		})
	}
}

func TestHandler_Acquire_FreshEnqueueThenResumeOnSecondCall(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHandler()
		c := &fakeConnState{id: 1, session: "S1"}

		resp1, ack1, perr := h.acquire(context.Background(), c, validAcquireReq("/repo", "src/auth/**"))
		if perr != nil {
			t.Fatalf("acquire #1: %v", perr)
		}
		if resp1.Status != "granted" {
			t.Fatalf("resp1.Status = %q, want granted", resp1.Status)
		}
		if ack1 != nil {
			ack1()
		}

		c2 := &fakeConnState{id: 2, session: "S1"}
		resp2, _, perr := h.acquire(context.Background(), c2, validAcquireReq("/repo", "src/auth/**"))
		if perr != nil {
			t.Fatalf("acquire #2 (resume): %v", perr)
		}
		if !resp2.Resumed {
			t.Fatal("expected Resumed=true on second call for the same session+scope+mode")
		}
		if resp2.Token != resp1.Token {
			t.Fatalf("resp2.Token = %d, want same token %d (idempotent re-acquire)", resp2.Token, resp1.Token)
		}
	})
}

func TestHandler_Status_FiltersAndDoesNotMutate(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHandler()
		c1 := &fakeConnState{id: 1, session: "S1"}
		c2 := &fakeConnState{id: 2, session: "S2"}

		if _, _, perr := h.acquire(context.Background(), c1, validAcquireReq("/repoA", "src/**")); perr != nil {
			t.Fatalf("acquire S1: %v", perr)
		}
		if _, _, perr := h.acquire(context.Background(), c2, validAcquireReq("/repoB", "src/**")); perr != nil {
			t.Fatalf("acquire S2: %v", perr)
		}

		resp := h.status(proto.StatusRequest{Root: "/repoA"})
		if len(resp.Grants) != 1 || resp.Grants[0].SessionID != "S1" || resp.Grants[0].Root != "/repoA" {
			t.Fatalf("status(root=/repoA) = %+v, want exactly S1's grant", resp)
		}

		respAll := h.status(proto.StatusRequest{})
		if len(respAll.Grants) != 2 {
			t.Fatalf("status(no filter) grants = %d, want 2", len(respAll.Grants))
		}
	})
}

func TestHandler_Release_RootMismatch(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHandler()
		c := &fakeConnState{id: 1, session: "S1"}

		resp, _, perr := h.acquire(context.Background(), c, validAcquireReq("/repoA", "src/**"))
		if perr != nil {
			t.Fatalf("acquire: %v", perr)
		}

		_, perr = h.release(proto.ReleaseRequest{Token: resp.Token, Root: "/repoB"})
		if perr == nil || perr.Code != proto.CodeRootMismatch {
			t.Fatalf("release with wrong root: perr = %v, want root_mismatch", perr)
		}

		// Correct root releases successfully, proving root_mismatch above
		// wasn't a false negative from some other error.
		_, perr = h.release(proto.ReleaseRequest{Token: resp.Token, Root: "/repoA"})
		if perr != nil {
			t.Fatalf("release with correct root: %v", perr)
		}
	})
}

func TestHandler_Release_StaleTokenMapsToDistinctCode(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHandler()
		c := &fakeConnState{id: 1, session: "S1"}

		resp, _, perr := h.acquire(context.Background(), c, validAcquireReq("/repoA", "src/**"))
		if perr != nil {
			t.Fatalf("acquire: %v", perr)
		}
		if _, perr := h.release(proto.ReleaseRequest{Token: resp.Token}); perr != nil {
			t.Fatalf("release: %v", perr)
		}

		_, perr = h.release(proto.ReleaseRequest{Token: resp.Token})
		if perr == nil || perr.Code != proto.CodeStaleToken {
			t.Fatalf("second release: perr = %v, want stale_token", perr)
		}
	})
}

func TestHandler_Renew_ExtendsExpiryKeepsToken(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHandler()
		c := &fakeConnState{id: 1, session: "S1"}

		resp, _, perr := h.acquire(context.Background(), c, validAcquireReq("/repoA", "src/**"))
		if perr != nil {
			t.Fatalf("acquire: %v", perr)
		}

		renewResp, perr := h.renew(proto.RenewRequest{Token: resp.Token, TTLMs: int64(10 * time.Minute / time.Millisecond), Root: "/repoA"})
		if perr != nil {
			t.Fatalf("renew: %v", perr)
		}
		if renewResp.Token != resp.Token {
			t.Fatalf("renew token = %d, want unchanged %d", renewResp.Token, resp.Token)
		}
		if renewResp.ExpiresMs <= resp.ExpiresMs {
			t.Fatalf("renewResp.ExpiresMs = %d, want > original %d", renewResp.ExpiresMs, resp.ExpiresMs)
		}
	})
}

func TestCodeFor_MapsEveryCoreSentinelInjectively(t *testing.T) {
	tests := []struct {
		err  error
		want proto.Code
	}{
		{lock.ErrInvalidRequest, proto.CodeInvalidRequest},
		{lock.ErrSessionOverlap, proto.CodeSessionOverlap},
		{lock.ErrStaleToken, proto.CodeStaleToken},
		{lock.ErrUnknownToken, proto.CodeUnknownToken},
		{lock.ErrSlotExpired, proto.CodeSlotExpired},
		{lock.ErrWaiterClosed, proto.CodeWaiterClosed},
		{lock.ErrAwaitBusy, proto.CodeAwaitBusy},
		{errors.New("some other unmapped error"), proto.CodeInternal},
	}

	seen := make(map[proto.Code]bool)
	for _, tt := range tests {
		got := codeFor(tt.err)
		if got != tt.want {
			t.Fatalf("codeFor(%v) = %q, want %q", tt.err, got, tt.want)
		}
		if tt.want != proto.CodeInternal {
			if seen[got] {
				t.Fatalf("code %q mapped from more than one distinct core sentinel", got)
			}
			seen[got] = true
		}
	}
}
