//go:build linux

package reader

import (
	"errors"
	"os"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/mapping"
)

// The immutable reader is Rust ReaderCore::open_immutable, which proves
// the retained identity of the opened descriptor and re-proves the
// pathname through a bound parent directory before map_reader. The
// parent open is O_DIRECTORY | O_NOFOLLOW, so a regular file whose
// parent is a magic symlink (/proc/self is the shape) is refused with
// the io class of that open, not with the format-invalid class of the
// two-page geometry check that would otherwise run first.
func TestOpenImmutableRefusesUnbindableParentAsIO(t *testing.T) {
	_, err := OpenImmutable("/proc/self/cmdline")
	if err == nil {
		t.Fatal("OpenImmutable accepted a procfs-shaped path")
	}
	var fe *format.Error
	if !errors.As(err, &fe) {
		t.Fatalf("%v is not a typed v4 error", err)
	}
	if fe.Code != format.CodeIO {
		t.Fatalf("code = %v (%v), want %v", fe.Code, fe.Detail, format.CodeIO)
	}
}

// The bound-parent proof accepts an ordinary database path: adding it
// must not turn the immutable open into a blanket refusal.
func TestVerifyPathAgainstFileAcceptsOrdinaryPath(t *testing.T) {
	path := buildBlobDatabase(t)
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := mapping.VerifyPathAgainstFile(path, f); err != nil {
		t.Fatalf("VerifyPathAgainstFile(%s) = %v", path, err)
	}
	if _, err := OpenImmutable(path); err != nil {
		t.Fatalf("OpenImmutable(%s) = %v", path, err)
	}
}
