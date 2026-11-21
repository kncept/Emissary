package readlimit

import (
	"errors"
	"strings"
	"testing"
)

type failingReader struct{ err error }

func (r failingReader) Read([]byte) (int, error) { return 0, r.err }

func TestReadAll(t *testing.T) {
	got, err := ReadAll(strings.NewReader("12345678"), 8)
	if err != nil || string(got) != "12345678" {
		t.Fatalf("ReadAll(exact) = %q, %v; want all 8 bytes", got, err)
	}

	if _, err := ReadAll(strings.NewReader("123456789"), 8); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("ReadAll(over limit) error = %v, want ErrTooLarge", err)
	}

	got, err = ReadAll(nil, 8)
	if err != nil || got != nil {
		t.Fatalf("ReadAll(nil) = %q, %v", got, err)
	}

	wantErr := errors.New("reader failed")
	if _, err := ReadAll(failingReader{err: wantErr}, 8); !errors.Is(err, wantErr) {
		t.Fatalf("ReadAll(reader error) = %v, want %v", err, wantErr)
	}
}
