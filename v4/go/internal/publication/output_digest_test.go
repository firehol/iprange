//go:build !windows

// Fixed-memory SHA-512 digest pass (Rust output_digest.rs tests): the
// byte-visit order and count, the known-answer digest over a mapped
// file, cancellation, and the out-of-range error.

package publication

import (
	"crypto/sha512"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/mapping"
)

func TestDigestWithVisitsEveryByteOnceInOrder(t *testing.T) {
	bytes := make([]byte, 2*digestBufferSize+17)
	for i := range bytes {
		bytes[i] = 0x5a
	}
	expectedOffset := 0
	calls := 0
	digested, err := digestWith(uint64(len(bytes)), func(offset uint64, output []byte) error {
		if int(offset) != expectedOffset {
			t.Fatalf("offset %d, want %d", offset, expectedOffset)
		}
		end := expectedOffset + len(output)
		copy(output, bytes[expectedOffset:end])
		expectedOffset = end
		calls++
		return nil
	})
	if err != nil {
		t.Fatalf("digestWith: %v", err)
	}
	if expectedOffset != len(bytes) {
		t.Fatalf("visited %d bytes, want %d", expectedOffset, len(bytes))
	}
	if calls != 3 {
		t.Fatalf("chunk calls %d, want 3", calls)
	}
	expected := sha512.Sum512(bytes)
	if digested != expected {
		t.Fatal("digest does not match the known-answer SHA-512")
	}
}

func TestDigestCancellableOverMappedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "digest")
	content := make([]byte, 3000)
	for i := range content {
		content[i] = byte(i * 7)
	}
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { file.Close() })
	mapped, err := mapping.MapFile(file, uint64(len(content)), false)
	if err != nil {
		t.Fatalf("map: %v", err)
	}
	t.Cleanup(func() { mapped.Close() })

	expected := sha512.Sum512(content)
	digested, err := digestCancellable(mapped, uint64(len(content)), nil)
	if err != nil {
		t.Fatalf("digestCancellable: %v", err)
	}
	if digested != expected {
		t.Fatal("digest does not match the file bytes")
	}

	want := errors.New("cancel")
	_, err = digestCancellable(mapped, uint64(len(content)), func() error { return want })
	if !errors.Is(err, want) {
		t.Fatalf("cancellation error %v, got %v", want, err)
	}
}

func TestDigestWithOutOfRangeReadFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "short")
	if err := os.WriteFile(path, []byte("short"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { file.Close() })
	mapped, err := mapping.MapFile(file, 5, false)
	if err != nil {
		t.Fatalf("map: %v", err)
	}
	t.Cleanup(func() { mapped.Close() })

	if _, err := digest(mapped, 6); err == nil {
		t.Fatal("digest past the mapped extent must fail")
	}
}
