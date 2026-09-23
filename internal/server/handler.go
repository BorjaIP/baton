package server

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/BorjaIP/baton/internal/core/lock"
	"github.com/BorjaIP/baton/internal/proto"
	"github.com/oklog/ulid/v2"
)

// connState is the minimal per-connection state handler needs: identity for
// registry bookkeeping and the session established by hello. The real
// connection type (built alongside the socket listener) implements this
// interface; tests exercise handler against a lightweight fake with no
// socket at all.
type connState interface {
	connID() uint64
	sessionID() lock.SessionID
	isEphemeral() bool
	setSession(id lock.SessionID, ephemeral bool)
}

// handler dispatches each recognized verb to the shared *lock.Table,
// translating every core/lock sentinel error into the wire-protocol error
// code mapping before responding.
type handler struct {
	table   *lock.Table
	reg     *registry
	pid     int
	version string
}

// hello processes the mandatory first request on a connection: it
// negotiates the protocol version, establishes the connection's session
// (generating a fresh ULID and marking the connection ephemeral when the
// client supplied none), and records both on c.
func (h *handler) hello(c connState, req proto.HelloRequest) (proto.HelloResponse, *proto.Error) {
	cMax := req.ProtoVersion
	cMin := req.ProtoMin
	if cMin == 0 {
		cMin = cMax
	}

	negotiated, ok := proto.Negotiate(cMin, cMax, proto.MinVersion, proto.Version)
	if !ok {
		return proto.HelloResponse{}, &proto.Error{
			Code:    proto.CodeVersionMismatch,
			Message: fmt.Sprintf("client supports [%d,%d], server supports [%d,%d]", cMin, cMax, proto.MinVersion, proto.Version),
			Detail: map[string]string{
				"client_min": strconv.FormatUint(uint64(cMin), 10),
				"client_max": strconv.FormatUint(uint64(cMax), 10),
				"server_min": strconv.FormatUint(uint64(proto.MinVersion), 10),
				"server_max": strconv.FormatUint(uint64(proto.Version), 10),
			},
		}
	}

	session := req.SessionID
	ephemeral := req.Ephemeral || session == ""
	if session == "" {
		session = ulid.Make().String()
		ephemeral = true
	}
	c.setSession(lock.SessionID(session), ephemeral)

	return proto.HelloResponse{
		ProtoVersion:  negotiated,
		SessionID:     session,
		Ephemeral:     ephemeral,
		ServerPID:     h.pid,
		ServerVersion: h.version,
	}, nil
}

// status returns a read-only snapshot of the table (per core/lock's
// Snapshot), filtered by req.Root and req.SessionID when set, with the
// internal scope key split back into its root and repo-relative pattern.
func (h *handler) status(req proto.StatusRequest) proto.StatusResponse {
	h.reg.mu.Lock()
	snap := h.table.Snapshot()
	h.reg.pruneLocked(snap)
	h.reg.mu.Unlock()

	resp := proto.StatusResponse{ServerPID: h.pid, ProtoVersion: proto.Version}

	for _, g := range snap.Grants {
		root, pattern, ok := splitKey(g.Pattern)
		if !ok {
			continue
		}
		if req.Root != "" && req.Root != root {
			continue
		}
		if req.SessionID != "" && string(g.Session) != req.SessionID {
			continue
		}
		resp.Grants = append(resp.Grants, proto.GrantInfo{
			Token:     g.Token,
			SessionID: string(g.Session),
			Root:      root,
			Pattern:   pattern,
			Mode:      g.Mode.String(),
			ExpiresMs: g.Expires.UnixMilli(),
		})
	}

	for _, p := range snap.Queue {
		root, pattern, ok := splitKey(p.Pattern)
		if !ok {
			continue
		}
		if req.Root != "" && req.Root != root {
			continue
		}
		if req.SessionID != "" && string(p.Session) != req.SessionID {
			continue
		}
		awaiting := p.SlotExpires.IsZero()
		var slotExpiresMs int64
		if !awaiting {
			slotExpiresMs = p.SlotExpires.UnixMilli()
		}
		resp.Queue = append(resp.Queue, proto.PendingInfo{
			SessionID:     string(p.Session),
			Root:          root,
			Pattern:       pattern,
			Mode:          p.Mode.String(),
			Position:      p.Position,
			Awaiting:      awaiting,
			SlotExpiresMs: slotExpiresMs,
		})
	}

	return resp
}

// acquire implements the full lock.acquire algorithm: validation, registry
// obtain (fresh enqueue, resume, or idempotent re-acquire), Await, one
// terminal-waiter retry, and response construction. ack is non-nil only on a
// granted result; the caller (the connection loop) must call it only after
// the response has been successfully written, so a grant only counts as
// delivered once the client could actually have seen it (design ADR-4).
func (h *handler) acquire(ctx context.Context, c connState, req proto.AcquireRequest) (resp proto.AcquireResponse, ack func(), perr *proto.Error) {
	mode, err := parseMode(req.Mode)
	if err != nil {
		return proto.AcquireResponse{}, nil, invalidRequest(err)
	}
	if err := proto.ValidateScope(req.Root, req.Pattern); err != nil {
		return proto.AcquireResponse{}, nil, invalidRequest(err)
	}
	if req.TTLMs <= 0 {
		return proto.AcquireResponse{}, nil, invalidRequest(errors.New("ttl_ms must be > 0"))
	}
	if req.SlotTTLMs <= 0 {
		return proto.AcquireResponse{}, nil, invalidRequest(errors.New("slot_ttl_ms must be > 0"))
	}
	if req.WaitMs < 0 {
		return proto.AcquireResponse{}, nil, invalidRequest(errors.New("wait_ms must be >= 0"))
	}

	key := scopeKey(req.Root, req.Pattern)
	rk := regKey{session: c.sessionID(), scope: key}
	lreq := lock.Request{
		Session: c.sessionID(),
		Pattern: key,
		Mode:    mode,
		TTL:     time.Duration(req.TTLMs) * time.Millisecond,
		SlotTTL: time.Duration(req.SlotTTLMs) * time.Millisecond,
	}
	wait := time.Duration(req.WaitMs) * time.Millisecond

	w, resumed, err := h.reg.obtain(h.table, rk, lreq, c.connID(), c.isEphemeral())
	if err != nil {
		return proto.AcquireResponse{}, nil, wireError(err)
	}

	res, err := w.Await(ctx, wait)
	if err != nil && resumed && isTerminalWaiterErr(err) {
		// The resumed waiter turned out to be terminal (e.g. its slot
		// expired between the registry lookup and Await). Discard it and
		// re-enqueue fresh exactly once.
		h.reg.drop(rk, w)
		w, _, err = h.reg.obtain(h.table, rk, lreq, c.connID(), c.isEphemeral())
		if err != nil {
			return proto.AcquireResponse{}, nil, wireError(err)
		}
		resumed = false
		res, err = w.Await(ctx, wait)
	}
	if err != nil {
		return proto.AcquireResponse{}, nil, wireError(err)
	}

	resp = proto.AcquireResponse{
		Root:      req.Root,
		Pattern:   req.Pattern,
		Mode:      req.Mode,
		SessionID: string(c.sessionID()),
		Ephemeral: c.isEphemeral(),
		Resumed:   resumed,
	}

	switch v := res.(type) {
	case lock.Granted:
		resp.Status = "granted"
		resp.Token = v.Token
		resp.ExpiresMs = v.Expires.UnixMilli()
		token := v.Token
		waiter := w
		ack = func() { h.reg.markDelivered(rk, waiter, token) }
	case lock.Queued:
		resp.Status = "queued"
		resp.Position = v.Position
	}

	return resp, ack, nil
}

// release validates an optional root against the token's stored scope
// before delegating to Table.Release, per design ADR-14.
func (h *handler) release(req proto.ReleaseRequest) (proto.ReleaseResponse, *proto.Error) {
	if req.Root != "" {
		grant, err := h.table.Validate(req.Token)
		if err != nil {
			return proto.ReleaseResponse{}, wireError(err)
		}
		if root, _, ok := splitKey(grant.Pattern); !ok || root != req.Root {
			return proto.ReleaseResponse{}, rootMismatch()
		}
	}

	if err := h.table.Release(req.Token); err != nil {
		return proto.ReleaseResponse{}, wireError(err)
	}
	h.reg.forgetToken(req.Token)

	return proto.ReleaseResponse{Token: req.Token}, nil
}

// renew validates an optional root the same way release does, then
// delegates to Table.Renew. A stale or unknown token also forgets the
// registry entry, mirroring release's cleanup.
func (h *handler) renew(req proto.RenewRequest) (proto.RenewResponse, *proto.Error) {
	if req.TTLMs <= 0 {
		return proto.RenewResponse{}, invalidRequest(errors.New("ttl_ms must be > 0"))
	}

	if req.Root != "" {
		grant, err := h.table.Validate(req.Token)
		if err != nil {
			return proto.RenewResponse{}, wireError(err)
		}
		if root, _, ok := splitKey(grant.Pattern); !ok || root != req.Root {
			return proto.RenewResponse{}, rootMismatch()
		}
	}

	expires, err := h.table.Renew(req.Token, time.Duration(req.TTLMs)*time.Millisecond)
	if err != nil {
		if errors.Is(err, lock.ErrStaleToken) || errors.Is(err, lock.ErrUnknownToken) {
			h.reg.forgetToken(req.Token)
		}
		return proto.RenewResponse{}, wireError(err)
	}

	return proto.RenewResponse{Token: req.Token, ExpiresMs: expires.UnixMilli()}, nil
}

// codeFor maps a core/lock sentinel error to its designated wire code. Two
// different core sentinels never map to the same code. Any error that does
// not match a known sentinel maps to CodeInternal, and its raw message is
// never exposed on the wire (callers building an *proto.Error from it should
// use a generic message).
func codeFor(err error) proto.Code {
	switch {
	case errors.Is(err, lock.ErrInvalidRequest):
		return proto.CodeInvalidRequest
	case errors.Is(err, lock.ErrSessionOverlap):
		return proto.CodeSessionOverlap
	case errors.Is(err, lock.ErrStaleToken):
		return proto.CodeStaleToken
	case errors.Is(err, lock.ErrUnknownToken):
		return proto.CodeUnknownToken
	case errors.Is(err, lock.ErrSlotExpired):
		return proto.CodeSlotExpired
	case errors.Is(err, lock.ErrWaiterClosed):
		return proto.CodeWaiterClosed
	case errors.Is(err, lock.ErrAwaitBusy):
		return proto.CodeAwaitBusy
	default:
		return proto.CodeInternal
	}
}

func isTerminalWaiterErr(err error) bool {
	return errors.Is(err, lock.ErrStaleToken) || errors.Is(err, lock.ErrSlotExpired) || errors.Is(err, lock.ErrWaiterClosed)
}

func wireError(err error) *proto.Error {
	code := codeFor(err)
	msg := err.Error()
	if code == proto.CodeInternal {
		msg = "internal error"
	}
	return &proto.Error{Code: code, Message: msg}
}

func invalidRequest(err error) *proto.Error {
	return &proto.Error{Code: proto.CodeInvalidRequest, Message: err.Error()}
}

func rootMismatch() *proto.Error {
	return &proto.Error{Code: proto.CodeRootMismatch, Message: "token belongs to a different repository root"}
}

func parseMode(m string) (lock.Mode, error) {
	switch m {
	case "exclusive":
		return lock.Exclusive, nil
	case "shared":
		return lock.Shared, nil
	default:
		return 0, fmt.Errorf("mode must be \"exclusive\" or \"shared\", got %q", m)
	}
}
