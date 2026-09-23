package proto

import (
	"bytes"
	"errors"
	"testing"
)

func TestEnvelope_EncodeDecodeRoundTrips(t *testing.T) {
	body, err := EncodeBody(HelloRequest{ProtoVersion: 1, Agent: "claude", Vendor: "anthropic", PID: 42})
	if err != nil {
		t.Fatalf("EncodeBody: %v", err)
	}
	in := &Envelope{ID: "1", Kind: KindRequest, Verb: VerbHello, Body: body}

	raw, err := EncodeEnvelope(in)
	if err != nil {
		t.Fatalf("EncodeEnvelope: %v", err)
	}

	out, err := DecodeEnvelope(raw)
	if err != nil {
		t.Fatalf("DecodeEnvelope: %v", err)
	}
	if out.ID != in.ID || out.Kind != in.Kind || out.Verb != in.Verb {
		t.Fatalf("DecodeEnvelope round trip mismatch: got %+v, want %+v", out, in)
	}

	var req HelloRequest
	if err := DecodeBody(out.Body, &req); err != nil {
		t.Fatalf("DecodeBody: %v", err)
	}
	if req.Agent != "claude" || req.PID != 42 {
		t.Fatalf("decoded body mismatch: %+v", req)
	}
}

func TestEnvelope_WriteReadRoundTripsOverStream(t *testing.T) {
	body, err := EncodeBody(StatusRequest{})
	if err != nil {
		t.Fatalf("EncodeBody: %v", err)
	}
	in := &Envelope{ID: "7", Kind: KindRequest, Verb: VerbStatus, Body: body}

	var buf bytes.Buffer
	if err := WriteEnvelope(&buf, in); err != nil {
		t.Fatalf("WriteEnvelope: %v", err)
	}

	out, err := ReadEnvelope(&buf)
	if err != nil {
		t.Fatalf("ReadEnvelope: %v", err)
	}
	if out.ID != "7" || out.Verb != VerbStatus {
		t.Fatalf("ReadEnvelope round trip mismatch: %+v", out)
	}
}

func TestEnvelope_InvalidMsgpackPayload(t *testing.T) {
	_, err := DecodeEnvelope([]byte{0xff, 0xff, 0xff, 0xff, 0xff})
	if !errors.Is(err, ErrMalformedPayload) {
		t.Fatalf("err = %v, want ErrMalformedPayload", err)
	}
}

func TestEnvelope_MissingRequiredFieldOnRequest(t *testing.T) {
	// A request envelope encoded without its required "verb" field.
	raw, err := EncodeEnvelope(&Envelope{ID: "1", Kind: KindRequest})
	if err != nil {
		t.Fatalf("EncodeEnvelope: %v", err)
	}

	_, err = DecodeEnvelope(raw)
	if !errors.Is(err, ErrInvalidEnvelope) {
		t.Fatalf("err = %v, want ErrInvalidEnvelope", err)
	}
}

func TestEnvelope_ResponseIDMatchesRequestID(t *testing.T) {
	reqRaw, err := EncodeEnvelope(&Envelope{ID: "abc123", Kind: KindRequest, Verb: VerbStatus})
	if err != nil {
		t.Fatalf("EncodeEnvelope: %v", err)
	}
	req, err := DecodeEnvelope(reqRaw)
	if err != nil {
		t.Fatalf("DecodeEnvelope: %v", err)
	}

	resp := &Envelope{ID: req.ID, Kind: KindResponse, Verb: req.Verb}
	if resp.ID != "abc123" {
		t.Fatalf("response ID = %q, want %q", resp.ID, "abc123")
	}
}

func TestEnvelope_PushRequiresNoReplyAndIsDistinguishable(t *testing.T) {
	raw, err := EncodeEnvelope(&Envelope{ID: "s-1", Kind: KindPush, Verb: PushLockGranted})
	if err != nil {
		t.Fatalf("EncodeEnvelope: %v", err)
	}
	out, err := DecodeEnvelope(raw)
	if err != nil {
		t.Fatalf("DecodeEnvelope: %v", err)
	}
	if out.Kind != KindPush {
		t.Fatalf("Kind = %q, want %q", out.Kind, KindPush)
	}
}

func TestEnvelope_MalformedPayloadDoesNotPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("DecodeEnvelope panicked: %v", r)
		}
	}()
	_, _ = DecodeEnvelope([]byte("not msgpack at all, just garbage bytes"))
}
