// Package client is the single typed Go client used by
// internal/adapters/ctl (and later phases) to dial the baton lock daemon,
// perform the hello handshake, invoke each Phase 2 verb, decode responses
// and errors, and autostart the daemon on demand. Per design §4 layering,
// this is the only package besides internal/server that understands
// wire-protocol framing and dialing details.
package client

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/BorjaIP/baton/internal/proto"
)

// ErrUnavailable is returned when dialing (and, if enabled, autostarting)
// the daemon fails, or when an established connection is lost mid-call.
var ErrUnavailable = errors.New("client: daemon unavailable")

// Options configures Dial.
type Options struct {
	Paths        proto.Paths // zero => proto.ResolvePaths(os.Getenv, home)
	SessionID    string
	Ephemeral    bool
	Agent        string
	Vendor       string
	Autostart    bool
	StartTimeout time.Duration             // default 3s total dial-retry budget after spawn
	CallTimeout  time.Duration             // default 10s; LockAcquire adds its wait_ms on top
	Spawn        func(p proto.Paths) error // nil => spawnDaemon (unix only)
}

func (o Options) withDefaults() (Options, error) {
	if o.Paths.Socket == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return o, fmt.Errorf("client: resolve home directory: %w", err)
		}
		paths, err := proto.ResolvePaths(os.Getenv, home)
		if err != nil {
			return o, err
		}
		o.Paths = paths
	}
	if o.StartTimeout <= 0 {
		o.StartTimeout = 3 * time.Second
	}
	if o.CallTimeout <= 0 {
		o.CallTimeout = 10 * time.Second
	}
	if o.Spawn == nil {
		o.Spawn = spawnDaemon
	}
	return o, nil
}

// Client is one dialed, hello-completed connection to the daemon. It is not
// safe for concurrent calls: internal/adapters/ctl uses one Client per
// short-lived invocation.
type Client struct {
	nc          net.Conn
	mu          sync.Mutex
	next        uint64
	hello       proto.HelloResponse
	broken      bool
	callTimeout time.Duration
}

// Dial resolves the socket path, dials it, and completes hello. If dialing
// fails for a reason that indicates the daemon simply is not running (no
// socket file, connection refused, or a dial timeout) and o.Autostart is
// set, Dial spawns the daemon exactly once via o.Spawn and retries dialing
// with bounded backoff (10ms doubling, capped at 200ms) until
// o.StartTimeout elapses. Any other dial error, or a hello error such as
// version_mismatch, fails immediately without autostarting.
func Dial(ctx context.Context, o Options) (*Client, error) {
	o, err := o.withDefaults()
	if err != nil {
		return nil, err
	}

	c, err := dialOnce(o)
	if err == nil {
		return c, nil
	}
	if !isNotRunning(err) || !o.Autostart {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}

	if spawnErr := o.Spawn(o.Paths); spawnErr != nil {
		return nil, fmt.Errorf("%w: spawn: %v", ErrUnavailable, spawnErr)
	}

	deadline := time.Now().Add(o.StartTimeout)
	backoff := 10 * time.Millisecond
	lastErr := err
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(backoff):
		}

		c, lastErr = dialOnce(o)
		if lastErr == nil {
			return c, nil
		}
		if !isNotRunning(lastErr) {
			return nil, fmt.Errorf("%w: %v", ErrUnavailable, lastErr)
		}

		backoff *= 2
		if backoff > 200*time.Millisecond {
			backoff = 200 * time.Millisecond
		}
	}
	return nil, fmt.Errorf("%w: %v", ErrUnavailable, lastErr)
}

// dialOnce dials the socket and performs hello, with no autostart and no
// retry.
func dialOnce(o Options) (*Client, error) {
	nc, err := net.DialTimeout("unix", o.Paths.Socket, 500*time.Millisecond)
	if err != nil {
		return nil, err
	}

	c := &Client{nc: nc, callTimeout: o.CallTimeout}
	if err := c.doHello(o); err != nil {
		_ = nc.Close()
		return nil, err
	}
	return c, nil
}

func (c *Client) doHello(o Options) error {
	_ = c.nc.SetDeadline(time.Now().Add(2 * time.Second))
	defer func() { _ = c.nc.SetDeadline(time.Time{}) }()

	req := proto.HelloRequest{
		ProtoVersion: proto.Version,
		ProtoMin:     proto.MinVersion,
		Agent:        o.Agent,
		Vendor:       o.Vendor,
		SessionID:    o.SessionID,
		Ephemeral:    o.Ephemeral,
		PID:          os.Getpid(),
	}
	body, err := proto.EncodeBody(req)
	if err != nil {
		return err
	}

	c.next++
	id := strconv.FormatUint(c.next, 10)
	if err := proto.WriteEnvelope(c.nc, &proto.Envelope{ID: id, Kind: proto.KindRequest, Verb: proto.VerbHello, Body: body}); err != nil {
		return err
	}

	env, err := proto.ReadEnvelope(c.nc)
	if err != nil {
		return err
	}
	if env.Err != nil {
		return env.Err
	}

	var resp proto.HelloResponse
	if err := proto.DecodeBody(env.Body, &resp); err != nil {
		return err
	}
	c.hello = resp
	return nil
}

// isNotRunning reports whether err from a dial attempt indicates simply
// that no daemon is currently listening (as opposed to a permission or
// path problem, which should fail fast without autostarting).
func isNotRunning(err error) bool {
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ECONNREFUSED) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	return false
}

// Hello returns the HelloResponse recorded when this connection completed
// its handshake.
func (c *Client) Hello() proto.HelloResponse {
	return c.hello
}

// Status calls the status verb.
func (c *Client) Status(ctx context.Context, r proto.StatusRequest) (proto.StatusResponse, error) {
	var resp proto.StatusResponse
	err := c.call(ctx, proto.VerbStatus, r, &resp, 0)
	return resp, err
}

// LockAcquire calls the lock.acquire verb. The call's own deadline budget
// includes r.WaitMs on top of the configured CallTimeout, since a blocking
// acquire is expected to take that long.
func (c *Client) LockAcquire(ctx context.Context, r proto.AcquireRequest) (proto.AcquireResponse, error) {
	var resp proto.AcquireResponse
	err := c.call(ctx, proto.VerbLockAcquire, r, &resp, time.Duration(r.WaitMs)*time.Millisecond)
	return resp, err
}

// LockRelease calls the lock.release verb.
func (c *Client) LockRelease(ctx context.Context, r proto.ReleaseRequest) (proto.ReleaseResponse, error) {
	var resp proto.ReleaseResponse
	err := c.call(ctx, proto.VerbLockRelease, r, &resp, 0)
	return resp, err
}

// LockRenew calls the lock.renew verb.
func (c *Client) LockRenew(ctx context.Context, r proto.RenewRequest) (proto.RenewResponse, error) {
	var resp proto.RenewResponse
	err := c.call(ctx, proto.VerbLockRenew, r, &resp, 0)
	return resp, err
}

// Close closes the underlying connection.
func (c *Client) Close() error {
	return c.nc.Close()
}

// call sends one request and waits for its matching response, tolerating
// (and ignoring) any push envelope or response with a different id that
// arrives first, per the wire-protocol's push tolerance requirement.
func (c *Client) call(ctx context.Context, verb string, req, resp any, wait time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.broken {
		return fmt.Errorf("%w: connection broken", ErrUnavailable)
	}

	c.next++
	id := strconv.FormatUint(c.next, 10)

	body, err := proto.EncodeBody(req)
	if err != nil {
		return err
	}

	deadline := time.Now().Add(c.callTimeout + wait)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = c.nc.SetDeadline(deadline)
	defer func() { _ = c.nc.SetDeadline(time.Time{}) }()

	// AfterFunc runs no goroutine unless ctx is later cancelled, keeping
	// goleak clean on the common path.
	stop := context.AfterFunc(ctx, func() {
		_ = c.nc.SetDeadline(time.Unix(1, 0))
	})
	defer stop()

	if err := proto.WriteEnvelope(c.nc, &proto.Envelope{ID: id, Kind: proto.KindRequest, Verb: verb, Body: body}); err != nil {
		return c.fail(ctx, err)
	}

	for {
		env, err := proto.ReadEnvelope(c.nc)
		if err != nil {
			return c.fail(ctx, err)
		}
		if env.Kind == proto.KindPush || env.ID != id {
			continue
		}
		if env.Err != nil {
			return env.Err
		}
		if resp == nil {
			return nil
		}
		return proto.DecodeBody(env.Body, resp)
	}
}

func (c *Client) fail(ctx context.Context, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	c.broken = true
	return fmt.Errorf("%w: %v", ErrUnavailable, err)
}
