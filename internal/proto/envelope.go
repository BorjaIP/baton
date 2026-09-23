package proto

import (
	"errors"
	"fmt"
	"io"

	"github.com/vmihailenco/msgpack/v5"
)

// Kind identifies whether an Envelope is a request, a response, or an
// unsolicited server push.
type Kind string

// Envelope kinds.
const (
	KindRequest  Kind = "request"
	KindResponse Kind = "response"
	KindPush     Kind = "push"
)

// Verbs defined for Phase 2.
const (
	VerbHello       = "hello"
	VerbStatus      = "status"
	VerbLockAcquire = "lock.acquire"
	VerbLockRelease = "lock.release"
	VerbLockRenew   = "lock.renew"

	// PushLockGranted is reserved for future use: it is defined here so the
	// envelope's Kind/Verb enumeration includes it, but Phase 2 servers never
	// emit it (design decision G4).
	PushLockGranted = "lock.granted"
)

// Envelope-level sentinel errors.
var (
	// ErrMalformedPayload is returned when a complete frame's payload bytes
	// do not decode as valid msgpack.
	ErrMalformedPayload = errors.New("proto: malformed payload")

	// ErrInvalidEnvelope is returned when a payload decodes as valid msgpack
	// but is missing a field required by its Kind, or carries an
	// unrecognized Kind.
	ErrInvalidEnvelope = errors.New("proto: invalid envelope")
)

// Envelope is the single message type carried by every frame. A response's
// ID matches the ID of the request it answers. A push carries a
// server-assigned ID distinct from any pending request's ID and expects no
// reply.
type Envelope struct {
	ID   string             `msgpack:"id"`
	Kind Kind               `msgpack:"kind"`
	Verb string             `msgpack:"verb,omitempty"` // required for request/push; echoed on response
	Body msgpack.RawMessage `msgpack:"body,omitempty"`
	Err  *Error             `msgpack:"error,omitempty"` // response only; Body empty when set
}

// EncodeEnvelope marshals e to msgpack bytes.
func EncodeEnvelope(e *Envelope) ([]byte, error) {
	b, err := msgpack.Marshal(e)
	if err != nil {
		return nil, fmt.Errorf("proto: encode envelope: %w", err)
	}
	return b, nil
}

// DecodeEnvelope unmarshals b into an Envelope, validating that it carries a
// recognized Kind and every field that Kind requires: request needs ID and
// Verb, response needs ID, push needs Verb.
func DecodeEnvelope(b []byte) (*Envelope, error) {
	var e Envelope
	if err := msgpack.Unmarshal(b, &e); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrMalformedPayload, err)
	}

	switch e.Kind {
	case KindRequest:
		if e.ID == "" {
			return nil, fmt.Errorf("%w: request missing required field %q", ErrInvalidEnvelope, "id")
		}
		if e.Verb == "" {
			return nil, fmt.Errorf("%w: request missing required field %q", ErrInvalidEnvelope, "verb")
		}
	case KindResponse:
		if e.ID == "" {
			return nil, fmt.Errorf("%w: response missing required field %q", ErrInvalidEnvelope, "id")
		}
	case KindPush:
		if e.Verb == "" {
			return nil, fmt.Errorf("%w: push missing required field %q", ErrInvalidEnvelope, "verb")
		}
	default:
		return nil, fmt.Errorf("%w: unrecognized kind %q", ErrInvalidEnvelope, e.Kind)
	}

	return &e, nil
}

// WriteEnvelope encodes e and writes it as a single frame.
func WriteEnvelope(w io.Writer, e *Envelope) error {
	b, err := EncodeEnvelope(e)
	if err != nil {
		return err
	}
	return WriteFrame(w, b)
}

// ReadEnvelope reads a single frame from r and decodes it as an Envelope.
func ReadEnvelope(r io.Reader) (*Envelope, error) {
	b, err := ReadFrame(r, MaxFrameSize)
	if err != nil {
		return nil, err
	}
	return DecodeEnvelope(b)
}

// EncodeBody marshals v (a verb-specific body struct) to a
// msgpack.RawMessage suitable for Envelope.Body.
func EncodeBody(v any) (msgpack.RawMessage, error) {
	b, err := msgpack.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("proto: encode body: %w", err)
	}
	return msgpack.RawMessage(b), nil
}

// DecodeBody unmarshals raw into v (a pointer to a verb-specific body
// struct).
func DecodeBody(raw msgpack.RawMessage, v any) error {
	if err := msgpack.Unmarshal(raw, v); err != nil {
		return fmt.Errorf("%w: %v", ErrMalformedPayload, err)
	}
	return nil
}
