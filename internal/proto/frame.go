package proto

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// MaxFrameSize is the largest payload (excluding the 4-byte length header)
// this package will read or write in a single frame.
const MaxFrameSize = 1 << 20

// Frame-level sentinel errors.
var (
	// ErrFrameTooLarge is returned when a frame's declared length exceeds the
	// configured maximum. It is returned before any buffer sized to the
	// declared length is allocated.
	ErrFrameTooLarge = errors.New("proto: frame too large")

	// ErrFrameTruncated is returned when fewer than the declared number of
	// payload bytes are available before the connection closes or times out.
	ErrFrameTruncated = errors.New("proto: truncated frame")

	// ErrFrameEmpty is returned when a frame declares a length of 0.
	ErrFrameEmpty = errors.New("proto: empty frame")
)

// WriteFrame writes payload as a single frame: a 4-byte big-endian length
// prefix followed by payload, assembled into one buffer and written with a
// single Write call.
func WriteFrame(w io.Writer, payload []byte) error {
	buf := make([]byte, 4+len(payload))
	binary.BigEndian.PutUint32(buf[:4], uint32(len(payload)))
	copy(buf[4:], payload)

	if _, err := w.Write(buf); err != nil {
		return fmt.Errorf("proto: write frame: %w", err)
	}
	return nil
}

// ReadFrame reads a single frame from r: a 4-byte big-endian length prefix
// followed by exactly that many payload bytes. max bounds the accepted
// declared length; a frame declaring more is rejected with ErrFrameTooLarge
// before any payload-sized buffer is allocated. A declared length of 0
// yields ErrFrameEmpty. Fewer than the declared number of payload bytes
// before EOF yields ErrFrameTruncated. A clean EOF before any header byte is
// read is returned as io.EOF unmodified.
func ReadFrame(r io.Reader, max int) ([]byte, error) {
	header := make([]byte, 4)
	if _, err := io.ReadFull(r, header); err != nil {
		if errors.Is(err, io.ErrUnexpectedEOF) {
			return nil, fmt.Errorf("proto: read frame header: %w", ErrFrameTruncated)
		}
		return nil, err // clean io.EOF before any header byte, or another read error
	}

	length := binary.BigEndian.Uint32(header)
	if length == 0 {
		return nil, ErrFrameEmpty
	}
	if int(length) > max {
		return nil, fmt.Errorf("%w: declared length %d exceeds maximum %d", ErrFrameTooLarge, length, max)
	}

	payload := make([]byte, length)
	if _, err := io.ReadFull(r, payload); err != nil {
		return nil, fmt.Errorf("proto: read frame payload: %w", ErrFrameTruncated)
	}
	return payload, nil
}
