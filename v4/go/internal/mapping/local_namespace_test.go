//go:build linux || darwin || freebsd

package mapping

// The containing-directory bind of the mapping owner must prove the
// local-filesystem durability contract, as Rust's bind_path does through
// Directory::open before it opens the name (publication::namespace::unix
// require_local_filesystem). Two arms are pinned: the read-write open,
// where the proof runs ahead of the name open so the class is the
// durability refusal rather than the errno the open of the name happens
// to report, and the read-only open, where the same proof is the
// namespace probe the reader arms run ahead of the geometry decision.
//
// procfs is the shape that separates the two answers: a node there is
// readable and looks regular, so without the proof the read-only arm
// reports the geometry class and the read-write arm reports EACCES as
// io. tmpfs, procfs, sysfs and network mounts are all outside the
// whitelist; a name under one of them can never carry the live contract.

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// nonLocalPath is a regular, readable file whose containing directory is
// not a filesystem the durability proof accepts; empty when the platform
// has no such shape.
func nonLocalPath() string {
	// procfs entries report st_size 0 and are read on demand, which is
	// exactly the shape that separates the two answers: the mapping open
	// cannot decide anything about them from the extent.
	for _, candidate := range []string{"/proc/self/net/route", "/proc/uptime", "/proc/mounts"} {
		if st, err := os.Stat(candidate); err == nil && st.Mode().IsRegular() {
			return candidate
		}
	}
	return ""
}

func codeOf(t *testing.T, err error) format.ErrorCode {
	t.Helper()
	if err == nil {
		t.Fatal("expected a refusal")
	}
	var fe *format.Error
	if !errors.As(err, &fe) {
		t.Fatalf("%v is not a typed v4 error", err)
	}
	return fe.Code
}

func TestOpenMutableUnderNonLocalFilesystemIsDurabilityUnsupported(t *testing.T) {
	path := nonLocalPath()
	if path == "" {
		t.Skip("no procfs shape available")
	}
	_, err := OpenMutable(path, nil)
	if code := codeOf(t, err); code != format.CodeDurabilityUnsupported {
		t.Fatalf("code = %v, want %v", code, format.CodeDurabilityUnsupported)
	}
}

func TestOpenImmutableUnderNonLocalFilesystemIsDurabilityUnsupported(t *testing.T) {
	path := nonLocalPath()
	if path == "" {
		t.Skip("no procfs shape available")
	}
	probe := func(f *os.File) error { return VerifyPathAgainstFile(path, f) }
	_, err := OpenImmutableChecked(path, nil, probe)
	if code := codeOf(t, err); code != format.CodeDurabilityUnsupported {
		t.Fatalf("code = %v, want %v", code, format.CodeDurabilityUnsupported)
	}
}

// The proof must accept the filesystem the suite runs on: a junk file
// under the test directory is refused for its content, never for the
// durability of its containing filesystem. Without this guard the proof
// could pass by refusing every name.
func TestOpenMutableOnLocalFilesystemIsNotDurabilityRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "junk.iprange")
	if err := os.WriteFile(path, make([]byte, 4096), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := OpenMutable(path, nil)
	if err == nil {
		return // a platform that accepted the junk content is fine too
	}
	if code := codeOf(t, err); code == format.CodeDurabilityUnsupported {
		t.Fatalf("the test filesystem was refused the durability contract: %v", err)
	}
}
