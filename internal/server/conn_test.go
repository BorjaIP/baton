package server

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/BorjaIP/baton/internal/proto"
)

// testDaemon wires a *Server to a real Unix socket in a short temp directory
// and tears it down at test end.
type testDaemon struct {
	t   *testing.T
	srv *Server
	ctx context.Context
	cnl context.CancelFunc
}

func startTestDaemon(t *testing.T, cfg Config) *testDaemon {
	t.Helper()

	dir := shortTempDir(t)
	paths, err := proto.ResolvePaths(func(string) string { return "" }, dir)
	if err != nil {
		t.Fatalf("ResolvePaths: %v", err)
	}
	// Force our own socket path under the short temp dir instead of
	// $HOME/.baton, still well under proto.MaxSocketPath.
	paths.Socket = dir + "/b.sock"
	paths.Lock = dir + "/b.lock"
	paths.Log = dir + "/b.log"

	cfg.Paths = paths
	if cfg.Version == "" {
		cfg.Version = "test"
	}

	srv := New(cfg)
	if err := srv.Listen(); err != nil {
		t.Fatalf("Listen: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := srv.Serve(ctx); err != nil {
			t.Errorf("Serve: %v", err)
		}
	}()

	td := &testDaemon{t: t, srv: srv, ctx: ctx, cnl: cancel}
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("Serve did not return after shutdown")
		}
	})
	return td
}

func (d *testDaemon) dial(t *testing.T) net.Conn {
	t.Helper()
	nc, err := net.DialTimeout("unix", d.srv.cfg.Paths.Socket, 2*time.Second)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { nc.Close() })
	return nc
}

func sendRequest(t *testing.T, nc net.Conn, id, verb string, body any) {
	t.Helper()
	raw, err := proto.EncodeBody(body)
	if err != nil {
		t.Fatalf("EncodeBody: %v", err)
	}
	env := &proto.Envelope{ID: id, Kind: proto.KindRequest, Verb: verb, Body: raw}
	if err := proto.WriteEnvelope(nc, env); err != nil {
		t.Fatalf("WriteEnvelope: %v", err)
	}
}

func recvResponse(t *testing.T, nc net.Conn) *proto.Envelope {
	t.Helper()
	nc.SetReadDeadline(time.Now().Add(2 * time.Second))
	env, err := proto.ReadEnvelope(nc)
	if err != nil {
		t.Fatalf("ReadEnvelope: %v", err)
	}
	return env
}

func doHello(t *testing.T, nc net.Conn, session string) proto.HelloResponse {
	t.Helper()
	req := proto.HelloRequest{ProtoVersion: proto.Version, Agent: "test", Vendor: "test", SessionID: session, PID: 1}
	sendRequest(t, nc, "1", proto.VerbHello, req)
	env := recvResponse(t, nc)
	if env.Err != nil {
		t.Fatalf("hello error: %+v", env.Err)
	}
	var resp proto.HelloResponse
	if err := proto.DecodeBody(env.Body, &resp); err != nil {
		t.Fatalf("decode hello response: %v", err)
	}
	return resp
}

func TestConn_HelloRequiredBeforeOtherVerbs(t *testing.T) {
	d := startTestDaemon(t, Config{})
	nc := d.dial(t)

	sendRequest(t, nc, "1", proto.VerbStatus, proto.StatusRequest{})
	env := recvResponse(t, nc)
	if env.Err == nil || env.Err.Code != proto.CodeHelloRequired {
		t.Fatalf("status before hello: err = %v, want hello_required", env.Err)
	}
}

func TestConn_HelloThenStatusRoundTrips(t *testing.T) {
	d := startTestDaemon(t, Config{})
	nc := d.dial(t)

	doHello(t, nc, "S1")

	sendRequest(t, nc, "2", proto.VerbStatus, proto.StatusRequest{})
	env := recvResponse(t, nc)
	if env.Err != nil {
		t.Fatalf("status: %+v", env.Err)
	}
	if env.ID != "2" {
		t.Fatalf("response id = %q, want 2", env.ID)
	}
}

func TestConn_UnknownVerbRejectedExplicitly(t *testing.T) {
	d := startTestDaemon(t, Config{})
	nc := d.dial(t)
	doHello(t, nc, "S1")

	sendRequest(t, nc, "2", "task.claim", struct{}{})
	env := recvResponse(t, nc)
	if env.Err == nil || env.Err.Code != proto.CodeUnknownVerb {
		t.Fatalf("unknown verb: err = %v, want unknown_verb", env.Err)
	}
}

func TestConn_SecondHelloIsInvalidRequest(t *testing.T) {
	d := startTestDaemon(t, Config{})
	nc := d.dial(t)
	doHello(t, nc, "S1")

	sendRequest(t, nc, "2", proto.VerbHello, proto.HelloRequest{ProtoVersion: proto.Version})
	env := recvResponse(t, nc)
	if env.Err == nil || env.Err.Code != proto.CodeInvalidRequest {
		t.Fatalf("second hello: err = %v, want invalid_request", env.Err)
	}
}

func TestConn_VersionMismatchKeepsConnectionOpen(t *testing.T) {
	d := startTestDaemon(t, Config{})
	nc := d.dial(t)

	sendRequest(t, nc, "1", proto.VerbHello, proto.HelloRequest{ProtoVersion: 99, ProtoMin: 99})
	env := recvResponse(t, nc)
	if env.Err == nil || env.Err.Code != proto.CodeVersionMismatch {
		t.Fatalf("version mismatch: err = %v, want version_mismatch", env.Err)
	}

	// Connection must still be usable: a correct hello now succeeds.
	doHello(t, nc, "S1")
}

func TestConn_MalformedEnvelopeGetsErrorAndConnectionContinues(t *testing.T) {
	d := startTestDaemon(t, Config{})
	nc := d.dial(t)
	doHello(t, nc, "S1")

	// A frame whose payload is not valid msgpack.
	if err := proto.WriteFrame(nc, []byte{0xff, 0xff, 0xff}); err != nil {
		t.Fatalf("WriteFrame: %v", err)
	}
	env := recvResponse(t, nc)
	if env.Err == nil || env.Err.Code != proto.CodeInvalidEnvelope {
		t.Fatalf("malformed payload: err = %v, want invalid_envelope", env.Err)
	}
	if env.ID != unknownRequestID {
		t.Fatalf("malformed payload response id = %q, want the unknown-id sentinel %q", env.ID, unknownRequestID)
	}

	// The connection must still be usable afterward.
	sendRequest(t, nc, "5", proto.VerbStatus, proto.StatusRequest{})
	env2 := recvResponse(t, nc)
	if env2.Err != nil {
		t.Fatalf("status after malformed payload: %+v", env2.Err)
	}
}

func TestConn_RequestInFlightRejectsConcurrentRequest(t *testing.T) {
	d := startTestDaemon(t, Config{})
	nc := d.dial(t)
	doHello(t, nc, "S1")

	// Acquire src/** exclusive and block a second connection's wait on it.
	sendRequest(t, nc, "2", proto.VerbLockAcquire, proto.AcquireRequest{
		Root: "/repo", Pattern: "src/**", Mode: "exclusive",
		TTLMs: int64(5 * time.Minute / time.Millisecond), SlotTTLMs: int64(30 * time.Second / time.Millisecond),
	})
	env := recvResponse(t, nc)
	if env.Err != nil {
		t.Fatalf("acquire: %+v", env.Err)
	}

	nc2 := d.dial(t)
	doHello(t, nc2, "S2")

	// Send a blocking acquire (in-flight), then immediately another request
	// on the same connection while the first is still pending.
	sendRequest(t, nc2, "3", proto.VerbLockAcquire, proto.AcquireRequest{
		Root: "/repo", Pattern: "src/**", Mode: "exclusive",
		TTLMs: int64(5 * time.Minute / time.Millisecond), SlotTTLMs: int64(30 * time.Second / time.Millisecond),
		WaitMs: int64(2 * time.Second / time.Millisecond),
	})
	// Give the handler goroutine a moment to pick up the in-flight slot.
	time.Sleep(100 * time.Millisecond)
	sendRequest(t, nc2, "4", proto.VerbStatus, proto.StatusRequest{})

	env4 := recvResponse(t, nc2)
	if env4.Err == nil || env4.Err.Code != proto.CodeRequestInFlight {
		t.Fatalf("second request while first in flight: id=%s err=%v, want request_in_flight", env4.ID, env4.Err)
	}
}

func TestConn_DisconnectCancelsBlockedAcquire(t *testing.T) {
	d := startTestDaemon(t, Config{})
	holder := d.dial(t)
	doHello(t, holder, "holder")

	sendRequest(t, holder, "1", proto.VerbLockAcquire, proto.AcquireRequest{
		Root: "/repo", Pattern: "src/**", Mode: "exclusive",
		TTLMs: int64(5 * time.Minute / time.Millisecond), SlotTTLMs: int64(30 * time.Second / time.Millisecond),
	})
	if env := recvResponse(t, holder); env.Err != nil {
		t.Fatalf("holder acquire: %+v", env.Err)
	}

	waiterConn, err := net.DialTimeout("unix", d.srv.cfg.Paths.Socket, 2*time.Second)
	if err != nil {
		t.Fatalf("dial waiter: %v", err)
	}
	doHello(t, waiterConn, "") // ephemeral

	sendRequest(t, waiterConn, "2", proto.VerbLockAcquire, proto.AcquireRequest{
		Root: "/repo", Pattern: "src/**", Mode: "exclusive",
		TTLMs: int64(5 * time.Minute / time.Millisecond), SlotTTLMs: int64(30 * time.Second / time.Millisecond),
		WaitMs: int64(10 * time.Second / time.Millisecond),
	})
	time.Sleep(150 * time.Millisecond) // let the acquire enqueue and block in Await

	// Kill the waiter connection instead of waiting for the response.
	waiterConn.Close()

	// Confirm status no longer shows the cancelled ephemeral waiter's queue
	// slot promptly (bounded poll, event-driven via repeated status calls) —
	// proving the disconnect actually cancelled its pending wait instead of
	// leaving it queued for the full 10s wait budget.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		sendRequest(t, holder, "4", proto.VerbStatus, proto.StatusRequest{Root: "/repo"})
		env := recvResponse(t, holder)
		if env.Err != nil {
			t.Fatalf("status: %+v", env.Err)
		}
		var resp proto.StatusResponse
		if err := proto.DecodeBody(env.Body, &resp); err != nil {
			t.Fatalf("decode status: %v", err)
		}
		if len(resp.Queue) == 0 {
			return // waiter's cancelled slot is gone: success
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("cancelled ephemeral waiter's slot was never removed from the queue")
}

func TestConn_JSONNotRequired_ResponseIDMatchesRequestID(t *testing.T) {
	d := startTestDaemon(t, Config{})
	nc := d.dial(t)
	doHello(t, nc, "S1")

	sendRequest(t, nc, "abc123", proto.VerbStatus, proto.StatusRequest{})
	env := recvResponse(t, nc)
	if env.ID != "abc123" {
		t.Fatalf("response id = %q, want abc123", env.ID)
	}
}
