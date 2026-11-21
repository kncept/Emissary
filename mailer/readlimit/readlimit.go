// Package readlimit provides one bounded-read primitive for untrusted readers.
// It reads at most limit bytes plus a single lookahead byte, so an oversized
// input is detected without ever materialising it in memory.
package readlimit

import (
	"errors"
	"fmt"
	"io"
)

// ErrTooLarge reports that a reader produced more than the allowed limit.
var ErrTooLarge = errors.New("input exceeds size limit")

// ReadAll reads r up to limit bytes. It returns ErrTooLarge when a further
// byte is available. Reader errors are returned unchanged.
func ReadAll(r io.Reader, limit int64) ([]byte, error) {
	if r == nil {
		return nil, nil
	}
	if limit < 0 {
		return nil, fmt.Errorf("negative read limit %d", limit)
	}
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%w: %d bytes", ErrTooLarge, limit)
	}
	return data, nil
}
