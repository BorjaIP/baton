package ctl_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/BorjaIP/baton/internal/adapters/ctl"
	"github.com/BorjaIP/baton/internal/proto"
)

func TestRenderAcquireGranted_HumanPrintsTokenPlainly(t *testing.T) {
	var out bytes.Buffer
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	resp := proto.AcquireResponse{Status: "granted", Token: 42, ExpiresMs: now.Add(5 * time.Minute).UnixMilli(), Root: "/r", Pattern: "src/**", Mode: "exclusive"}

	ctl.RenderAcquire(&out, resp, false, now)

	if !strings.Contains(out.String(), "42") {
		t.Fatalf("human output does not contain the plain token 42: %q", out.String())
	}
}

func TestRenderAcquireGranted_JSONIsSingleParseableObject(t *testing.T) {
	var out bytes.Buffer
	now := time.Now()
	resp := proto.AcquireResponse{Status: "granted", Token: 42, ExpiresMs: now.Add(5 * time.Minute).UnixMilli(), Root: "/r", Pattern: "src/**", Mode: "exclusive"}

	ctl.RenderAcquire(&out, resp, true, now)

	var decoded map[string]any
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\n%s", err, out.String())
	}
	if tok, ok := decoded["token"].(float64); !ok || tok != 42 {
		t.Fatalf("decoded token = %v, want 42", decoded["token"])
	}
	if strings.Count(strings.TrimSpace(out.String()), "\n") > 0 {
		// A single JSON object may legitimately be pretty-printed; only
		// assert there is no extra non-JSON text by re-checking the whole
		// buffer parses as exactly one value already done above.
		_ = out.String()
	}
}

func TestRenderError_JSONShape(t *testing.T) {
	var out bytes.Buffer
	err := &proto.Error{Code: proto.CodeStaleToken, Message: "stale"}

	ctl.RenderError(&out, err, true)

	var decoded struct {
		OK    bool `json:"ok"`
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if jsonErr := json.Unmarshal(out.Bytes(), &decoded); jsonErr != nil {
		t.Fatalf("stdout is not valid JSON: %v\n%s", jsonErr, out.String())
	}
	if decoded.OK {
		t.Fatal("ok must be false for an error result")
	}
	if decoded.Error.Code != "stale_token" {
		t.Fatalf("error.code = %q, want stale_token", decoded.Error.Code)
	}
}

func TestRenderStatus_EmptyPrintsNoLocks(t *testing.T) {
	var out bytes.Buffer
	ctl.RenderStatus(&out, proto.StatusResponse{}, false, false, time.Now())
	if !strings.Contains(out.String(), "no locks") {
		t.Fatalf("expected 'no locks', got %q", out.String())
	}
}

func TestRenderStatus_ShowsGrantsAndQueue(t *testing.T) {
	var out bytes.Buffer
	resp := proto.StatusResponse{
		Grants: []proto.GrantInfo{{Token: 1, SessionID: "A", Root: "/r", Pattern: "src/**", Mode: "exclusive", ExpiresMs: time.Now().Add(time.Minute).UnixMilli()}},
		Queue:  []proto.PendingInfo{{SessionID: "B", Root: "/r", Pattern: "src/**", Mode: "exclusive", Position: 1, Awaiting: true}},
	}
	ctl.RenderStatus(&out, resp, false, false, time.Now())
	s := out.String()
	if !strings.Contains(s, "HELD") || !strings.Contains(s, "QUEUED") {
		t.Fatalf("expected HELD and QUEUED sections, got %q", s)
	}
	if !strings.Contains(s, "src/**") {
		t.Fatalf("expected pattern in output, got %q", s)
	}
}

func TestQueuedEphemeralHint_WrittenToStderr(t *testing.T) {
	var stdout, stderr bytes.Buffer
	resp := proto.AcquireResponse{Status: "queued", Position: 1, Root: "/r", Pattern: "src/**", Mode: "exclusive", Ephemeral: true}

	ctl.RenderAcquireWithHint(&stdout, &stderr, resp, false)

	if !strings.Contains(stderr.String(), "ephemeral") && !strings.Contains(stderr.String(), "session") {
		t.Fatalf("expected an ephemeral-session hint on stderr, got %q", stderr.String())
	}
}
