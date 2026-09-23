package server

import (
	"context"
	"errors"
	"net"
	"sync"
	"time"

	"github.com/BorjaIP/baton/internal/core/lock"
	"github.com/BorjaIP/baton/internal/proto"
)

// unknownRequestID is used for a response envelope reporting an
// envelope-level error (malformed payload, unrecognized kind) whose request
// id could not be parsed at all. proto.DecodeEnvelope requires every
// response to carry a non-empty id, so a literal empty id (as design ADR-8's
// prose describes) is not a valid envelope; this sentinel is the closest
// legitimate substitute and can never collide with a real client id, since
// clients use positive decimal counters starting at 1.
const unknownRequestID = "0"

// errConnClosed is the cancellation cause for a connection's context when
// its own read loop ends (peer closed, read error, or hello timeout).
// errShutdown is the cause used when the server's base context is
// cancelled by Shutdown. handler.acquire's caller distinguishes the two so a
// blocked wait that loses its connection responds with nothing, while one
// caught by a server-wide shutdown gets an explicit shutting_down response.
var (
	errConnClosed = errors.New("connection closed")
	errShutdown   = errors.New("server shutting down")
)

// conn is one accepted connection's server-side state. It implements the
// connState interface handler.go depends on.
type conn struct {
	id  uint64
	nc  net.Conn
	wmu sync.Mutex

	ctx    context.Context
	cancel context.CancelCauseFunc

	session   lock.SessionID
	ephemeral bool
	helloDone bool
	proto     uint16

	inflight chan struct{} // cap 1: at most one request dispatched at a time
	hwg      sync.WaitGroup
}

func (c *conn) connID() uint64            { return c.id }
func (c *conn) sessionID() lock.SessionID { return c.session }
func (c *conn) isEphemeral() bool         { return c.ephemeral }
func (c *conn) setSession(id lock.SessionID, ephemeral bool) {
	c.session = id
	c.ephemeral = ephemeral
}

// dispatchResult is the outcome of dispatching one request envelope: the
// response envelope to write (nil means write nothing, per the
// connection-closed shutdown case), and an optional ack to run only after
// that response is successfully written (design ADR-4).
type dispatchResult struct {
	resp *proto.Envelope
	ack  func()
}

// serveConn runs one connection's read loop: a single reader goroutine that
// always calls ReadEnvelope, so a peer disconnect or read error is detected
// promptly even while a request is blocked in Await elsewhere (design
// ADR-5). Before hello completes, reads carry a bounded deadline
// (HelloTimeout); hello itself is always handled inline. After hello, at
// most one request runs at a time, dispatched to its own goroutine tracked
// by c.hwg so the reader keeps reading.
func (s *Server) serveConn(c *conn) {
	for {
		if !c.helloDone {
			_ = c.nc.SetReadDeadline(time.Now().Add(s.cfg.HelloTimeout))
		} else {
			_ = c.nc.SetReadDeadline(time.Time{})
		}

		env, err := proto.ReadEnvelope(c.nc)
		if err != nil {
			if errors.Is(err, proto.ErrMalformedPayload) || errors.Is(err, proto.ErrInvalidEnvelope) {
				// The frame boundary was intact; only the payload was bad.
				// Respond and keep the connection open (design ADR-8).
				s.writeEnvelope(c, errorResponse(unknownRequestID, &proto.Error{
					Code:    proto.CodeInvalidEnvelope,
					Message: err.Error(),
				}))
				continue
			}
			if errors.Is(err, proto.ErrFrameTooLarge) {
				s.cfg.Logger.Warn("oversize frame, closing connection", "conn", c.id, "err", err)
			}
			break
		}

		if env.Kind != proto.KindRequest {
			s.writeEnvelope(c, errorResponse(unknownRequestID, &proto.Error{
				Code:    proto.CodeInvalidEnvelope,
				Message: "expected a request envelope",
			}))
			continue
		}

		if !c.helloDone {
			s.handleHello(c, env)
			continue
		}

		if env.Verb == proto.VerbHello {
			s.writeEnvelope(c, errorResponse(env.ID, invalidRequest(errors.New("hello already completed on this connection"))))
			continue
		}

		select {
		case c.inflight <- struct{}{}:
			reqEnv := env
			c.hwg.Go(func() {
				defer func() { <-c.inflight }()
				res := s.dispatch(c.ctx, c, reqEnv)
				if res.resp == nil {
					return
				}
				if writeErr := s.writeEnvelope(c, res.resp); writeErr == nil && res.ack != nil {
					res.ack()
				}
			})
		default:
			s.writeEnvelope(c, errorResponse(env.ID, &proto.Error{
				Code:    proto.CodeRequestInFlight,
				Message: "a request is already in flight on this connection",
			}))
		}
	}

	c.cancel(errConnClosed)
	c.hwg.Wait()
	s.reg.closeConn(c.id, c.isEphemeral())
	_ = c.nc.Close()
}

// handleHello processes the mandatory first request inline, in the reader
// goroutine itself: it is never dispatched to c.hwg, so the connection's
// session is fully established before any concurrent handler goroutine can
// observe it.
func (s *Server) handleHello(c *conn, env *proto.Envelope) {
	if env.Verb != proto.VerbHello {
		s.writeEnvelope(c, errorResponse(env.ID, &proto.Error{
			Code:    proto.CodeHelloRequired,
			Message: "hello must be the first request on a connection",
		}))
		return
	}

	var req proto.HelloRequest
	if err := proto.DecodeBody(env.Body, &req); err != nil {
		s.writeEnvelope(c, errorResponse(env.ID, invalidRequest(err)))
		return
	}

	resp, perr := s.h.hello(c, req)
	if perr != nil {
		// The connection stays open on a version mismatch (design ADR-7);
		// the client decides whether to disconnect.
		s.writeEnvelope(c, errorResponse(env.ID, perr))
		return
	}

	c.helloDone = true
	c.proto = resp.ProtoVersion
	s.writeEnvelope(c, bodyResponse(env.ID, proto.VerbHello, resp))
}

// dispatch routes one post-hello request to its handler method and builds
// the response envelope, translating the connection-scoped context's
// cancellation cause into the right wire behavior for lock.acquire (design:
// shutdown gets an explicit shutting_down response; a connection that lost
// its own peer gets no response written at all).
func (s *Server) dispatch(ctx context.Context, c *conn, env *proto.Envelope) dispatchResult {
	switch env.Verb {
	case proto.VerbStatus:
		var req proto.StatusRequest
		if err := proto.DecodeBody(env.Body, &req); err != nil {
			return dispatchResult{resp: errorResponse(env.ID, invalidRequest(err))}
		}
		resp := s.h.status(req)
		return dispatchResult{resp: bodyResponse(env.ID, proto.VerbStatus, resp)}

	case proto.VerbLockAcquire:
		var req proto.AcquireRequest
		if err := proto.DecodeBody(env.Body, &req); err != nil {
			return dispatchResult{resp: errorResponse(env.ID, invalidRequest(err))}
		}
		resp, ack, perr := s.h.acquire(ctx, c, req)
		if perr != nil {
			if ctx.Err() != nil {
				switch context.Cause(ctx) {
				case errShutdown:
					return dispatchResult{resp: errorResponse(env.ID, &proto.Error{
						Code:    proto.CodeShuttingDown,
						Message: "server is shutting down",
					})}
				case errConnClosed:
					return dispatchResult{}
				}
			}
			return dispatchResult{resp: errorResponse(env.ID, perr)}
		}
		return dispatchResult{resp: bodyResponse(env.ID, proto.VerbLockAcquire, resp), ack: ack}

	case proto.VerbLockRelease:
		var req proto.ReleaseRequest
		if err := proto.DecodeBody(env.Body, &req); err != nil {
			return dispatchResult{resp: errorResponse(env.ID, invalidRequest(err))}
		}
		resp, perr := s.h.release(req)
		if perr != nil {
			return dispatchResult{resp: errorResponse(env.ID, perr)}
		}
		return dispatchResult{resp: bodyResponse(env.ID, proto.VerbLockRelease, resp)}

	case proto.VerbLockRenew:
		var req proto.RenewRequest
		if err := proto.DecodeBody(env.Body, &req); err != nil {
			return dispatchResult{resp: errorResponse(env.ID, invalidRequest(err))}
		}
		resp, perr := s.h.renew(req)
		if perr != nil {
			return dispatchResult{resp: errorResponse(env.ID, perr)}
		}
		return dispatchResult{resp: bodyResponse(env.ID, proto.VerbLockRenew, resp)}

	default:
		return dispatchResult{resp: errorResponse(env.ID, &proto.Error{
			Code:    proto.CodeUnknownVerb,
			Message: "unrecognized verb " + env.Verb,
		})}
	}
}

// writeEnvelope serializes writes to c under c.wmu with a bounded deadline.
// A nil env (the connection-closed shutdown case) is a no-op. Write errors
// are swallowed here: the reader loop's next ReadEnvelope call is what
// detects and reacts to a broken connection.
func (s *Server) writeEnvelope(c *conn, env *proto.Envelope) error {
	if env == nil {
		return nil
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	_ = c.nc.SetWriteDeadline(time.Now().Add(s.cfg.WriteTimeout))
	return proto.WriteEnvelope(c.nc, env)
}

func errorResponse(id string, perr *proto.Error) *proto.Envelope {
	return &proto.Envelope{ID: id, Kind: proto.KindResponse, Err: perr}
}

func bodyResponse(id, verb string, body any) *proto.Envelope {
	raw, err := proto.EncodeBody(body)
	if err != nil {
		return errorResponse(id, &proto.Error{Code: proto.CodeInternal, Message: "internal error"})
	}
	return &proto.Envelope{ID: id, Kind: proto.KindResponse, Verb: verb, Body: raw}
}
