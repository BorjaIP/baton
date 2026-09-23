package server

import (
	"context"
	"errors"
	"net"
	"os"
	"testing"
	"time"

	"github.com/BorjaIP/baton/internal/proto"
)

func TestServer_ListenThenSecondInstanceGetsErrAlreadyRunning(t *testing.T) {
	dir := shortTempDir(t)
	paths, err := proto.ResolvePaths(func(string) string { return "" }, dir)
	if err != nil {
		t.Fatalf("ResolvePaths: %v", err)
	}
	paths.Socket = dir + "/b.sock"
	paths.Lock = dir + "/b.lock"

	first := New(Config{Paths: paths})
	if err := first.Listen(); err != nil {
		t.Fatalf("first Listen: %v", err)
	}
	defer func() {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_ = first.Serve(ctx)
	}()

	before, err := os.Lstat(paths.Socket)
	if err != nil {
		t.Fatalf("Lstat socket before second Listen: %v", err)
	}

	second := New(Config{Paths: paths})
	err = second.Listen()
	if !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("second Listen error = %v, want ErrAlreadyRunning", err)
	}

	after, err := os.Lstat(paths.Socket)
	if err != nil {
		t.Fatalf("Lstat socket after second Listen: %v", err)
	}
	if before.ModTime() != after.ModTime() {
		t.Fatal("the losing instance modified the winner's socket file")
	}
}

func TestServer_ServeAcceptsConnectionsUntilShutdown(t *testing.T) {
	d := startTestDaemon(t, Config{})

	nc, err := net.DialTimeout("unix", d.srv.cfg.Paths.Socket, 2*time.Second)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	nc.Close()
}

func TestServer_ShutdownRemovesSocketAndReleasesFlock(t *testing.T) {
	dir := shortTempDir(t)
	paths, err := proto.ResolvePaths(func(string) string { return "" }, dir)
	if err != nil {
		t.Fatalf("ResolvePaths: %v", err)
	}
	paths.Socket = dir + "/b.sock"
	paths.Lock = dir + "/b.lock"

	srv := New(Config{Paths: paths})
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

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return after context cancellation")
	}

	if _, err := os.Lstat(paths.Socket); !os.IsNotExist(err) {
		t.Fatalf("expected socket file removed after shutdown, Lstat err = %v", err)
	}

	// A fresh instance must be able to acquire the flock immediately.
	again := New(Config{Paths: paths})
	if err := again.Listen(); err != nil {
		t.Fatalf("re-Listen after shutdown: %v", err)
	}
	ctx2, cancel2 := context.WithCancel(context.Background())
	cancel2()
	_ = again.Serve(ctx2)
}

func TestServer_ShutdownUnblocksBlockedAcquireWait(t *testing.T) {
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

	waiter := d.dial(t)
	doHello(t, waiter, "waiter")
	sendRequest(t, waiter, "2", proto.VerbLockAcquire, proto.AcquireRequest{
		Root: "/repo", Pattern: "src/**", Mode: "exclusive",
		TTLMs: int64(5 * time.Minute / time.Millisecond), SlotTTLMs: int64(30 * time.Second / time.Millisecond),
		WaitMs: int64(30 * time.Second / time.Millisecond),
	})

	// Trigger shutdown; the blocked waiter must get a prompt response
	// (shutting_down), not block for the full 30s wait budget.
	d.cnl()

	waiter.SetReadDeadline(time.Now().Add(4 * time.Second))
	env, err := proto.ReadEnvelope(waiter)
	if err != nil {
		t.Fatalf("expected a shutting_down response before the wait budget elapsed: %v", err)
	}
	if env.Err == nil || env.Err.Code != proto.CodeShuttingDown {
		t.Fatalf("blocked acquire on shutdown: err = %v, want shutting_down", env.Err)
	}
}
