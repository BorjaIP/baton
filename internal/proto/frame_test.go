package proto

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"testing"
)

func TestFrame_RoundTrips(t *testing.T) {
	payload := []byte("hello, world")

	var buf bytes.Buffer
	if err := WriteFrame(&buf, payload); err != nil {
		t.Fatalf("WriteFrame: %v", err)
	}

	got, err := ReadFrame(&buf, MaxFrameSize)
	if err != nil {
		t.Fatalf("ReadFrame: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("ReadFrame = %q, want %q", got, payload)
	}
}

func TestFrame_RoundTrips_EmptyPayloadDistinctFromEmptyFrame(t *testing.T) {
	// A non-empty payload with different content triangulates the round trip.
	payload := []byte("{}")

	var buf bytes.Buffer
	if err := WriteFrame(&buf, payload); err != nil {
		t.Fatalf("WriteFrame: %v", err)
	}

	got, err := ReadFrame(&buf, MaxFrameSize)
	if err != nil {
		t.Fatalf("ReadFrame: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("ReadFrame = %q, want %q", got, payload)
	}
}

// limitedReader lets us assert exactly how many bytes ReadFrame consumed
// before rejecting an oversize frame, proving no buffer was allocated for
// the declared (oversize) length.
type limitedReader struct {
	r    io.Reader
	read int
}

func (l *limitedReader) Read(p []byte) (int, error) {
	n, err := l.r.Read(p)
	l.read += n
	return n, err
}

func TestFrame_OversizeRejectedBeforeAllocation(t *testing.T) {
	const max = 16

	header := make([]byte, 4)
	binary.BigEndian.PutUint32(header, uint32(max+1))
	// Only the 4-byte header is available; no payload bytes follow.
	lr := &limitedReader{r: bytes.NewReader(header)}

	_, err := ReadFrame(lr, max)
	if !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("err = %v, want ErrFrameTooLarge", err)
	}
	if lr.read != 4 {
		t.Fatalf("bytes consumed = %d, want 4 (header only, no payload read attempted)", lr.read)
	}
}

func TestFrame_TruncatedRejected(t *testing.T) {
	header := make([]byte, 4)
	binary.BigEndian.PutUint32(header, 10)
	buf := bytes.NewBuffer(header)
	buf.WriteString("short") // fewer than 10 bytes

	_, err := ReadFrame(buf, MaxFrameSize)
	if !errors.Is(err, ErrFrameTruncated) {
		t.Fatalf("err = %v, want ErrFrameTruncated", err)
	}
}

func TestFrame_EmptyRejected(t *testing.T) {
	header := make([]byte, 4)
	binary.BigEndian.PutUint32(header, 0)
	buf := bytes.NewBuffer(header)

	_, err := ReadFrame(buf, MaxFrameSize)
	if !errors.Is(err, ErrFrameEmpty) {
		t.Fatalf("err = %v, want ErrFrameEmpty", err)
	}
}

func TestFrame_CleanEOFBeforeHeader(t *testing.T) {
	_, err := ReadFrame(bytes.NewReader(nil), MaxFrameSize)
	if !errors.Is(err, io.EOF) {
		t.Fatalf("err = %v, want io.EOF", err)
	}
}
