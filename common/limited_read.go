package common

import (
	"errors"
	"fmt"
	"io"
)

// ErrBodyTooLarge is returned when a bounded read observes more than the
// permitted number of bytes. Callers must read max+1 bytes so an exact-limit
// payload remains distinguishable from a truncated oversized payload.
var ErrBodyTooLarge = errors.New("body exceeds configured limit")

// ReadAllLimited reads at most maxBytes and fails when additional data exists.
// It is intended for upstream and local response bodies that must be buffered
// before parsing. Streaming relay paths use their own incremental limits.
func ReadAllLimited(reader io.Reader, maxBytes int64) ([]byte, error) {
	if reader == nil {
		return nil, errors.New("reader is nil")
	}
	if maxBytes < 1 {
		return nil, fmt.Errorf("invalid body limit %d", maxBytes)
	}
	data, err := io.ReadAll(io.LimitReader(reader, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("%w: maximum is %d bytes", ErrBodyTooLarge, maxBytes)
	}
	return data, nil
}
