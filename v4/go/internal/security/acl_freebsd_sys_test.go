//go:build freebsd

// Live FreeBSD ACL machine tests: the kernel round trip through
// __acl_get_fd/__acl_set_fd on the host filesystem, mirroring the
// Rust freebsd ACL machine suite. The NFSv4 arm runs the full strip
// flow on ZFS (the host brand); the POSIX.1e arm pins the masked-ACL
// behavior of libc acl_strip_np on hosts without NFSv4 ACLs (a
// recalculated mask entry keeps the ACL at four entries, so the
// creator-only proof refuses before and after the strip, exactly like
// the Rust machine).

package security

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// freshFreeBSDFile returns one new regular file on the host filesystem.
func freshFreeBSDFile(t *testing.T) *os.File {
	t.Helper()
	dir := t.TempDir()
	f, err := os.OpenFile(filepath.Join(dir, "artifact"), os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatalf("create test artifact: %v", err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}

// TestFreeBSDACLMachineLiveProveOrStrip runs both machine surfaces on
// the host: a fresh 0600 file proves immediately (its ACL is the
// trivial form of the brand), and a file whose ACL carries an extended
// entry fails the proof, strips toward the trivial form, and behaves
// exactly like the Rust machine.
func TestFreeBSDACLMachineLiveProveOrStrip(t *testing.T) {
	profile, err := Capture()
	if err != nil {
		t.Fatalf("capture: %v", err)
	}

	f := freshFreeBSDFile(t)
	if err := f.Chmod(0o600); err != nil {
		t.Fatalf("chmod 0600: %v", err)
	}
	if err := requireTrivialACL(f); err != nil {
		t.Fatalf("fresh 0600 file must be trivial: %v", err)
	}
	if err := SecureCreatorOnly(f, profile); err != nil {
		t.Fatalf("secure creator-only on a fresh file: %v", err)
	}
	got, err := CreatorOnlyCommitment(f)
	if err != nil {
		t.Fatalf("commitment: %v", err)
	}
	if got != profile.Commitment() {
		t.Fatalf("commitment = %x, want %x", got, profile.Commitment())
	}

	var liveACL fbsdACL
	brand, err := fbsdGetACL(f, &liveACL)
	if err != nil {
		t.Fatalf("get ACL: %v", err)
	}
	acl := &liveACL
	switch brand {
	case fbsdBrandNFS4:
		// One named-user deny entry makes the NFSv4 ACL nontrivial; the
		// proof fails with the access-policy class, removeInheritedACL
		// rebuilds the trivial PSARC form of the expressed mode, and
		// the proof passes again (Rust remove_inherited on ZFS).
		acl.Entries[acl.Cnt] = fbsdACLEntry{Tag: fbsdTagUser, ID: 1337, Perm: fbsdPermReadData, EntryType: fbsdEntryTypeDeny}
		acl.Cnt++
		if err := fbsdSetACL(f, acl, brand); err != nil {
			t.Fatalf("set nontrivial ACL: %v", err)
		}
		if err := requireTrivialACL(f); !isCode(err, format.CodeAccessPolicyUnsupported) {
			t.Fatalf("requireTrivialACL over a named entry = %v, want CodeAccessPolicyUnsupported", err)
		}
		if err := removeInheritedACL(f); err != nil {
			t.Fatalf("removeInheritedACL: %v", err)
		}
		if err := requireTrivialACL(f); err != nil {
			t.Fatalf("NFSv4 ACL after strip must be trivial: %v", err)
		}
		if err := SecureCreatorOnly(f, profile); err != nil {
			t.Fatalf("secure creator-only after strip: %v", err)
		}
	case fbsdBrandPOSIX:
		// A POSIX.1e ACL with a mask entry is nontrivial (four
		// entries), and libc acl_strip_np keeps the recalculated mask,
		// so the proof refuses before and after the strip: the Rust
		// machine behaves identically on POSIX hosts.
		acl.Entries[acl.Cnt] = fbsdACLEntry{Tag: fbsdTagMask, ID: fbsdUndefinedID, Perm: fbsdPermRead | fbsdPermWrite, EntryType: fbsdEntryTypeAllow}
		acl.Cnt++
		if err := fbsdSetACL(f, acl, brand); err != nil {
			t.Fatalf("set masked ACL: %v", err)
		}
		if err := requireTrivialACL(f); !isCode(err, format.CodeAccessPolicyUnsupported) {
			t.Fatalf("requireTrivialACL over a mask entry = %v, want CodeAccessPolicyUnsupported", err)
		}
		if err := removeInheritedACL(f); err != nil {
			t.Fatalf("removeInheritedACL: %v", err)
		}
		if err := requireTrivialACL(f); !isCode(err, format.CodeAccessPolicyUnsupported) {
			t.Fatalf("POSIX ACL after strip must keep the mask refusal, got %v", err)
		}
	default:
		t.Fatalf("unexpected host brand %v", brand)
	}

	// The 0644 mode fails the ownership proof with the access-policy
	// class on every brand (Rust creator_only_metadata).
	if err := f.Chmod(0o644); err != nil {
		t.Fatalf("chmod 0644: %v", err)
	}
	if _, err := CreatorOnlyCommitment(f); !isCode(err, format.CodeAccessPolicyUnsupported) {
		t.Fatalf("commitment over 0644 = %v, want CodeAccessPolicyUnsupported", err)
	}
}

// TestFreeBSDACLGetReportsUnsupportedOnDevfs pins the get arm's
// EOPNOTSUPP class (Rust maps it to NamespaceError::Unsupported,
// transported as CodeDurabilityUnsupported) on a filesystem without
// ACL operations; devfs on the host is one such filesystem. The test
// skips when the host's devfs unexpectedly carries ACL support.
func TestFreeBSDACLGetReportsUnsupportedOnDevfs(t *testing.T) {
	f, err := os.Open("/dev/null")
	if err != nil {
		t.Skipf("open /dev/null: %v", err)
	}
	defer f.Close()
	var devACL fbsdACL
	_, err = fbsdGetACL(f, &devACL)
	if err == nil {
		t.Skip("devfs unexpectedly supports access ACLs on this host")
	}
	var fe *format.Error
	if !errors.As(err, &fe) || fe.Code != format.CodeDurabilityUnsupported {
		t.Fatalf("get ACL on devfs = %v, want CodeDurabilityUnsupported", err)
	}
}
