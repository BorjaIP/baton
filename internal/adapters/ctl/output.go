package ctl

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"time"

	"github.com/BorjaIP/baton/internal/messages"
	"github.com/BorjaIP/baton/internal/proto"
)

// jsonErrorEnvelope is the shared --json error shape for every command.
type jsonErrorEnvelope struct {
	OK    bool `json:"ok"`
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func writeJSON(w io.Writer, v any) {
	enc := json.NewEncoder(w)
	_ = enc.Encode(v)
}

// RenderError writes an error result: a single {"ok":false,...} JSON object
// in --json mode, or "baton: <message>" human text.
func RenderError(w io.Writer, err error, jsonMode bool) {
	code, message := errorCodeAndMessage(err)
	if jsonMode {
		var env jsonErrorEnvelope
		env.Error.Code = code
		env.Error.Message = message
		writeJSON(w, env)
		return
	}
	fmt.Fprintf(w, "%s: %s\n", messages.Program, message)
}

func errorCodeAndMessage(err error) (code, message string) {
	var wireErr *proto.Error
	if e, ok := err.(*proto.Error); ok {
		wireErr = e
	}
	if wireErr != nil {
		msg := wireErr.Message
		if msg == "" {
			msg = messages.ErrorText(string(wireErr.Code))
		}
		return string(wireErr.Code), msg
	}
	return "internal", err.Error()
}

// jsonAcquireResult is the --json shape for a lock.acquire result.
type jsonAcquireResult struct {
	OK        bool   `json:"ok"`
	Status    string `json:"status"`
	Token     uint64 `json:"token,omitempty"`
	Expires   string `json:"expires,omitempty"`
	Position  int    `json:"position,omitempty"`
	Root      string `json:"root"`
	Pattern   string `json:"pattern"`
	Mode      string `json:"mode"`
	Session   string `json:"session"`
	Ephemeral bool   `json:"ephemeral"`
	Resumed   bool   `json:"resumed"`
}

// RenderAcquire writes a lock.acquire success result.
func RenderAcquire(w io.Writer, resp proto.AcquireResponse, jsonMode bool, now time.Time) {
	if jsonMode {
		out := jsonAcquireResult{
			OK: true, Status: resp.Status, Token: resp.Token, Position: resp.Position,
			Root: resp.Root, Pattern: resp.Pattern, Mode: resp.Mode,
			Session: resp.SessionID, Ephemeral: resp.Ephemeral, Resumed: resp.Resumed,
		}
		if resp.Status == "granted" {
			out.Expires = time.UnixMilli(resp.ExpiresMs).UTC().Format(time.RFC3339Nano)
		}
		writeJSON(w, out)
		return
	}

	if resp.Status == "granted" {
		fmt.Fprintln(w, messages.Granted(resp.Pattern, resp.Mode, resp.Token, time.UnixMilli(resp.ExpiresMs), now))
		return
	}
	fmt.Fprintln(w, messages.Queued(resp.Pattern, resp.Mode, resp.Position))
}

// RenderAcquireWithHint calls RenderAcquire on stdout, and additionally
// writes messages.QueuedEphemeralHint to stderr when the result is a queued
// acquire from an ephemeral session.
func RenderAcquireWithHint(stdout, stderr io.Writer, resp proto.AcquireResponse, jsonMode bool) {
	RenderAcquire(stdout, resp, jsonMode, time.Now())
	if resp.Status == "queued" && resp.Ephemeral {
		fmt.Fprintln(stderr, messages.QueuedEphemeralHint)
	}
}

// RenderRelease writes a lock.release success result.
func RenderRelease(w io.Writer, resp proto.ReleaseResponse, jsonMode bool) {
	if jsonMode {
		writeJSON(w, struct {
			OK    bool   `json:"ok"`
			Token uint64 `json:"token"`
		}{true, resp.Token})
		return
	}
	fmt.Fprintln(w, messages.Released(resp.Token))
}

// RenderRenew writes a lock.renew success result.
func RenderRenew(w io.Writer, resp proto.RenewResponse, jsonMode bool, now time.Time) {
	if jsonMode {
		writeJSON(w, struct {
			OK      bool   `json:"ok"`
			Token   uint64 `json:"token"`
			Expires string `json:"expires"`
		}{true, resp.Token, time.UnixMilli(resp.ExpiresMs).UTC().Format(time.RFC3339Nano)})
		return
	}
	fmt.Fprintln(w, messages.Renewed(resp.Token, time.UnixMilli(resp.ExpiresMs), now))
}

// jsonStatusResult is the --json shape for a status result.
type jsonStatusResult struct {
	OK     bool `json:"ok"`
	Daemon struct {
		PID   int    `json:"pid"`
		Proto uint16 `json:"proto"`
	} `json:"daemon"`
	Grants []jsonGrant   `json:"grants"`
	Queue  []jsonPending `json:"queue"`
}

type jsonGrant struct {
	Token   uint64 `json:"token"`
	Session string `json:"session"`
	Root    string `json:"root"`
	Pattern string `json:"pattern"`
	Mode    string `json:"mode"`
	Expires string `json:"expires"`
}

type jsonPending struct {
	Session     string  `json:"session"`
	Root        string  `json:"root"`
	Pattern     string  `json:"pattern"`
	Mode        string  `json:"mode"`
	Position    int     `json:"position"`
	Awaiting    bool    `json:"awaiting"`
	SlotExpires *string `json:"slot_expires"`
}

// RenderStatus writes a status result, grouped by root when all is set.
func RenderStatus(w io.Writer, resp proto.StatusResponse, jsonMode, all bool, now time.Time) {
	if jsonMode {
		out := jsonStatusResult{OK: true}
		out.Daemon.PID = resp.ServerPID
		out.Daemon.Proto = resp.ProtoVersion
		for _, g := range resp.Grants {
			out.Grants = append(out.Grants, jsonGrant{
				Token: g.Token, Session: g.SessionID, Root: g.Root, Pattern: g.Pattern, Mode: g.Mode,
				Expires: time.UnixMilli(g.ExpiresMs).UTC().Format(time.RFC3339Nano),
			})
		}
		for _, p := range resp.Queue {
			jp := jsonPending{Session: p.SessionID, Root: p.Root, Pattern: p.Pattern, Mode: p.Mode, Position: p.Position, Awaiting: p.Awaiting}
			if !p.Awaiting && p.SlotExpiresMs != 0 {
				s := time.UnixMilli(p.SlotExpiresMs).UTC().Format(time.RFC3339Nano)
				jp.SlotExpires = &s
			}
			out.Queue = append(out.Queue, jp)
		}
		writeJSON(w, out)
		return
	}

	if len(resp.Grants) == 0 && len(resp.Queue) == 0 {
		fmt.Fprintln(w, messages.NoLocks)
		return
	}

	if all {
		renderStatusGroupedByRoot(w, resp, now)
		return
	}
	renderStatusSection(w, resp.Grants, resp.Queue, now)
}

func renderStatusGroupedByRoot(w io.Writer, resp proto.StatusResponse, now time.Time) {
	roots := map[string]bool{}
	for _, g := range resp.Grants {
		roots[g.Root] = true
	}
	for _, p := range resp.Queue {
		roots[p.Root] = true
	}
	sorted := make([]string, 0, len(roots))
	for r := range roots {
		sorted = append(sorted, r)
	}
	sort.Strings(sorted)

	for _, root := range sorted {
		fmt.Fprintf(w, "%s:\n", root)
		var grants []proto.GrantInfo
		for _, g := range resp.Grants {
			if g.Root == root {
				grants = append(grants, g)
			}
		}
		var queue []proto.PendingInfo
		for _, p := range resp.Queue {
			if p.Root == root {
				queue = append(queue, p)
			}
		}
		renderStatusSection(w, grants, queue, now)
	}
}

func renderStatusSection(w io.Writer, grants []proto.GrantInfo, queue []proto.PendingInfo, now time.Time) {
	fmt.Fprintln(w, "HELD:")
	if len(grants) == 0 {
		fmt.Fprintln(w, "  (none)")
	}
	for _, g := range grants {
		fmt.Fprintf(w, "  token %d  %s (%s)  session=%s  expires %s\n",
			g.Token, g.Pattern, g.Mode, g.SessionID, time.UnixMilli(g.ExpiresMs).Format(time.RFC3339))
	}
	fmt.Fprintln(w, "QUEUED:")
	if len(queue) == 0 {
		fmt.Fprintln(w, "  (none)")
	}
	for _, p := range queue {
		fmt.Fprintf(w, "  position %d  %s (%s)  session=%s  awaiting=%t\n",
			p.Position, p.Pattern, p.Mode, p.SessionID, p.Awaiting)
	}
}
