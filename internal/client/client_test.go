package client

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/BorjaIP/baton/internal/proto"
)

// fakeHandler is one fake daemon connection's whole scripted behavior. It
// runs in a background goroutine (via fakeDaemon), so it must never call
// testing.T methods directly; it reports its outcome on the channel
// fakeDaemon returns instead.
type fakeHandler func(nc net.Conn) error

func fakeSocketPath(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "bt")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Join(dir, "b.sock")
}

func pathsFor(socket string) proto.Paths {
	base := socket[:len(socket)-len(".sock")]
	return proto.Paths{
		Dir:    filepath.Dir(socket),
		Socket: socket,
		Lock:   base + ".lock",
		Log:    base + ".log",
	}
}

// fakeDaemon accepts exactly one connection at path and runs handle against
// it in a background goroutine, reporting handle's return value (or an
// Accept error) on the returned channel exactly once.
func fakeDaemon(t *testing.T, path string, handle fakeHandler) <-chan error {
	t.Helper()

	addr, err := net.ResolveUnixAddr("unix", path)
	if err != nil {
		t.Fatalf("ResolveUnixAddr: %v", err)
	}
	ln, err := net.ListenUnix("unix", addr)
	if err != nil {
		t.Fatalf("ListenUnix: %v", err)
	}
	t.Cleanup(func() { ln.Close() })

	errCh := make(chan error, 1)
	go func() {
		nc, err := ln.Accept()
		if err != nil {
			errCh <- err
			return
		}
		defer nc.Close()
		errCh <- handle(nc)
	}()
	return errCh
}

func waitFakeDaemon(t *testing.T, errCh <-chan error) {
	t.Helper()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("fake daemon handler: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("fake daemon handler did not complete in time")
	}
}

func fakeRecvRequest(nc net.Conn) (*proto.Envelope, error) {
	_ = nc.SetReadDeadline(time.Now().Add(2 * time.Second))
	return proto.ReadEnvelope(nc)
}

func fakeSend(nc net.Conn, env *proto.Envelope) error {
	_ = nc.SetWriteDeadline(time.Now().Add(2 * time.Second))
	return proto.WriteEnvelope(nc, env)
}

func fakeResponseEnv(id, verb string, body any) (*proto.Envelope, error) {
	raw, err := proto.EncodeBody(body)
	if err != nil {
		return nil, err
	}
	return &proto.Envelope{ID: id, Kind: proto.KindResponse, Verb: verb, Body: raw}, nil
}

func fakeErrorEnv(id string, perr *proto.Error) *proto.Envelope {
	return &proto.Envelope{ID: id, Kind: proto.KindResponse, Err: perr}
}

// helloHandler replies to the mandatory first hello request with resp, then
// hands the connection to next (if any) for the rest of the scripted
// exchange.
func helloHandler(resp proto.HelloResponse, next fakeHandler) fakeHandler {
	return func(nc net.Conn) error {
		env, err := fakeRecvRequest(nc)
		if err != nil {
			return fmt.Errorf("recv hello: %w", err)
		}
		if env.Verb != proto.VerbHello {
			return fmt.Errorf("expected hello, got verb %q", env.Verb)
		}
		respEnv, err := fakeResponseEnv(env.ID, proto.VerbHello, resp)
		if err != nil {
			return err
		}
		if err := fakeSend(nc, respEnv); err != nil {
			return fmt.Errorf("send hello response: %w", err)
		}
		if next == nil {
			return nil
		}
		return next(nc)
	}
}

func TestDial_FailsFastWhenNoDaemonAndAutostartDisabled(t *testing.T) {
	path := fakeSocketPath(t)

	_, err := Dial(context.Background(), Options{Paths: pathsFor(path)})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Dial with nothing listening: err = %v, want ErrUnavailable", err)
	}
}

func TestDial_SuccessfulHelloRoundTrip(t *testing.T) {
	path := fakeSocketPath(t)
	errCh := fakeDaemon(t, path, helloHandler(proto.HelloResponse{
		ProtoVersion: proto.Version, SessionID: "S1", ServerPID: 999, ServerVersion: "v1",
	}, nil))

	c, err := Dial(context.Background(), Options{Paths: pathsFor(path), SessionID: "S1", Agent: "test", Vendor: "test"})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer c.Close()

	if got := c.Hello(); got.SessionID != "S1" || got.ServerPID != 999 {
		t.Fatalf("Hello() = %+v, want SessionID=S1 ServerPID=999", got)
	}
	waitFakeDaemon(t, errCh)
}

func TestClient_StatusRoundTripsTypedResult(t *testing.T) {
	path := fakeSocketPath(t)
	want := proto.StatusResponse{
		ServerPID: 42,
		Grants: []proto.GrantInfo{
			{Token: 7, SessionID: "S1", Root: "/repo", Pattern: "src/**", Mode: "exclusive", ExpiresMs: 123},
		},
	}
	errCh := fakeDaemon(t, path, helloHandler(proto.HelloResponse{ProtoVersion: proto.Version, SessionID: "S1"}, func(nc net.Conn) error {
		env, err := fakeRecvRequest(nc)
		if err != nil {
			return err
		}
		if env.Verb != proto.VerbStatus {
			return fmt.Errorf("expected status, got %q", env.Verb)
		}
		respEnv, err := fakeResponseEnv(env.ID, proto.VerbStatus, want)
		if err != nil {
			return err
		}
		return fakeSend(nc, respEnv)
	}))

	c, err := Dial(context.Background(), Options{Paths: pathsFor(path), SessionID: "S1"})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer c.Close()

	got, err := c.Status(context.Background(), proto.StatusRequest{})
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if len(got.Grants) != 1 || got.Grants[0].Token != 7 || got.Grants[0].Pattern != "src/**" {
		t.Fatalf("Status() = %+v, want one grant token=7 pattern=src/**", got)
	}
	waitFakeDaemon(t, errCh)
}

func TestClient_WireErrorDecodesToTypedClientError(t *testing.T) {
	path := fakeSocketPath(t)
	errCh := fakeDaemon(t, path, helloHandler(proto.HelloResponse{ProtoVersion: proto.Version, SessionID: "S1"}, func(nc net.Conn) error {
		env, err := fakeRecvRequest(nc)
		if err != nil {
			return err
		}
		if env.Verb != proto.VerbLockRelease {
			return fmt.Errorf("expected lock.release, got %q", env.Verb)
		}
		return fakeSend(nc, fakeErrorEnv(env.ID, &proto.Error{Code: proto.CodeStaleToken, Message: "stale token"}))
	}))

	c, err := Dial(context.Background(), Options{Paths: pathsFor(path), SessionID: "S1"})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer c.Close()

	_, err = c.LockRelease(context.Background(), proto.ReleaseRequest{Token: 17})
	if !errors.Is(err, proto.ErrCodeStaleToken) {
		t.Fatalf("LockRelease with stale token: err = %v, want errors.Is match on ErrCodeStaleToken", err)
	}
	waitFakeDaemon(t, errCh)
}

func TestClient_PushBeforeResponseIsTolerated(t *testing.T) {
	path := fakeSocketPath(t)
	errCh := fakeDaemon(t, path, helloHandler(proto.HelloResponse{ProtoVersion: proto.Version, SessionID: "S1"}, func(nc net.Conn) error {
		env, err := fakeRecvRequest(nc)
		if err != nil {
			return err
		}
		if env.Verb != proto.VerbStatus {
			return fmt.Errorf("expected status, got %q", env.Verb)
		}

		pushRaw, err := proto.EncodeBody(proto.GrantedPush{Token: 1, Root: "/repo", Pattern: "src/**"})
		if err != nil {
			return err
		}
		push := &proto.Envelope{ID: "s-1", Kind: proto.KindPush, Verb: proto.PushLockGranted, Body: pushRaw}
		if err := fakeSend(nc, push); err != nil {
			return err
		}

		respEnv, err := fakeResponseEnv(env.ID, proto.VerbStatus, proto.StatusResponse{ServerPID: 1})
		if err != nil {
			return err
		}
		return fakeSend(nc, respEnv)
	}))

	c, err := Dial(context.Background(), Options{Paths: pathsFor(path), SessionID: "S1"})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer c.Close()

	got, err := c.Status(context.Background(), proto.StatusRequest{})
	if err != nil {
		t.Fatalf("Status after an unexpected push: %v", err)
	}
	if got.ServerPID != 1 {
		t.Fatalf("Status() = %+v, want ServerPID=1 (the real response, not the push)", got)
	}
	waitFakeDaemon(t, errCh)
}

func TestClient_NoCoreLockImport(t *testing.T) {
	// Compile-time layering guard is enforced in cmd/baton/layering_test.go
	// (Work Unit 6). This test just documents the intent locally so a
	// reviewer of this package sees it stated once.
	t.Log("internal/client must never import internal/core/lock; see cmd/baton layering test")
}
