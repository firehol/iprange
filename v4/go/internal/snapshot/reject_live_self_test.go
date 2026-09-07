//go:build !windows

package snapshot

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/live"
	"github.com/firehol/iprange/v4/go/internal/publication"
	"github.com/firehol/iprange/v4/go/internal/reader"
)

// probeSource is a snapshot source whose identity the test controls,
// so rejectLiveSelf can be exercised without a live database.
type probeSource struct {
	device uint64
	inode  uint64
}

func (s probeSource) Meta() format.Meta                     { return format.Meta{} }
func (s probeSource) FileIdentity() (uint64, uint64, error) { return s.device, s.inode, nil }
func (s probeSource) Core() *reader.ImmutableReader         { return nil }
func (s probeSource) finishCurrent(func() error) sourceEnd  { return sourceEnd{} }

// TestRejectLiveSelfBindsDestination mirrors the Rust reference: the
// self-replacement check binds the destination parent and main name
// and opens the bound spelling relative to the parent, so valid
// destination spellings that carry a trailing separator or a trailing
// "." component after the main name probe the same file the publish
// will replace instead of failing with ENOTDIR on the raw string.
func TestRejectLiveSelfBindsDestination(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "archive.iprange")
	data := make([]byte, 512)
	if err := os.WriteFile(main, data, 0o600); err != nil {
		t.Fatal(err)
	}
	var st os.FileInfo
	st, err := os.Stat(main)
	if err != nil {
		t.Fatal(err)
	}
	sysstat, ok := st.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatalf("unexpected file stat type %T", st.Sys())
	}
	identity := probeSource{device: uint64(sysstat.Dev), inode: uint64(sysstat.Ino)}
	other := probeSource{device: uint64(sysstat.Dev) + 1, inode: uint64(sysstat.Ino) + 1}

	// The destination must stay a plain regular file on disk for the
	// open-after-bind probe; the raw spellings below do not exist as
	// directory paths.
	for _, spelling := range []string{
		main,         // exact
		main + "/",   // trailing separator
		main + "/.",  // trailing dot
		main + "//",  // doubled trailing separator
		main + "/./", // trailing dot reduced
	} {
		// Same identity: refuse (self replacement), whatever the
		// spelling, exactly like Rust.
		err := rejectLiveSelf(identity, SourceLive, spelling, publication.PolicyReplaceExisting)
		if err == nil {
			t.Errorf("rejectLiveSelf(%q) accepted self replacement", spelling)
		} else if fe, ok := err.(*format.Error); !ok || fe.Code != format.CodeInvalidArgument {
			t.Errorf("rejectLiveSelf(%q) = %v, want InvalidArgument self-replacement", spelling, err)
		}
		// Different identity: no refusal, the bound probe succeeds.
		if err := rejectLiveSelf(other, SourceLive, spelling, publication.PolicyReplaceExisting); err != nil {
			t.Errorf("rejectLiveSelf(%q) with a different source = %v, want nil", spelling, err)
		}
	}
	// Non-live sources skip the check altogether.
	if err := rejectLiveSelf(identity, SourceImmutable, main, publication.PolicyReplaceExisting); err != nil {
		t.Errorf("immutable mode unexpectedly refused: %v", err)
	}
	// A missing destination is not a refusal (the attempt creation
	// reports it), whatever its spelling.
	missing := filepath.Join(dir, "missing.iprange") + "/."
	if err := rejectLiveSelf(other, SourceLive, missing, publication.PolicyReplaceExisting); err != nil {
		t.Errorf("rejectLiveSelf(%q) missing destination = %v, want nil", missing, err)
	}
}

// rejectLiveSelf needs live.FileName to be exercised at least once on
// the reference surface; pin the spelling split used by the binding.
func TestRejectLiveSelfBindingSpelling(t *testing.T) {
	for path, want := range map[string]string{
		"dir/a.iprange":      "a.iprange",
		"dir/a.iprange/":     "a.iprange",
		"dir/a.iprange/.":    "a.iprange",
		"dir/a.iprange//":    "a.iprange",
		"dir/a/../b.iprange": "b.iprange",
	} {
		if got, ok := live.FileName(path); !ok || got != want {
			t.Errorf("FileName(%q) = (%q, %v), want (%q, true)", path, got, ok, want)
		}
	}
}
