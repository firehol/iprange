//go:build linux

package security

import (
	"encoding/binary"
	"errors"
	"os"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// installExtendedACL installs one inherited access ACL on the artifact
// (Rust namespace_tests install_extended_acl: version 2 plus the five
// standard entries). It reports false when the filesystem lacks ACL
// support (EOPNOTSUPP/ENOSYS), skipping the assertion like the Rust
// test, and fails the test on any other error.
func installExtendedACL(t *testing.T, f *os.File) bool {
	t.Helper()
	const (
		userObj  = 0x01
		user     = 0x02
		groupObj = 0x04
		mask     = 0x10
		other    = 0x20
	)
	bytes := make([]byte, 0, 44)
	var version [4]byte
	binary.LittleEndian.PutUint32(version[:], 2)
	bytes = append(bytes, version[:]...)
	for _, entry := range []struct {
		tag  uint16
		perm uint16
		id   uint32
	}{
		{userObj, 6, 0xffffffff},
		{user, 4, uint32(unix.Geteuid()) + 1},
		{groupObj, 0, 0xffffffff},
		{mask, 4, 0xffffffff},
		{other, 0, 0xffffffff},
	} {
		var tag, perm [2]byte
		binary.LittleEndian.PutUint16(tag[:], entry.tag)
		binary.LittleEndian.PutUint16(perm[:], entry.perm)
		var id [4]byte
		binary.LittleEndian.PutUint32(id[:], entry.id)
		bytes = append(bytes, tag[:]...)
		bytes = append(bytes, perm[:]...)
		bytes = append(bytes, id[:]...)
	}
	err := unix.Fsetxattr(int(f.Fd()), accessACL, bytes, 0)
	if err == nil {
		return true
	}
	if errors.Is(err, unix.EOPNOTSUPP) || errors.Is(err, unix.ENOSYS) {
		return false
	}
	t.Fatalf("install test ACL: %v", err)
	return false
}

// TestInheritedExtendedAccessACLIsRemoved mirrors Rust
// inherited_extended_access_acl_is_removed: a file carrying an
// inherited access ACL fails the ownership proof until SecureCreatorOnly
// removes the ACL and proves the policy.
func TestInheritedExtendedAccessACLIsRemoved(t *testing.T) {
	profile, err := Capture()
	if err != nil {
		t.Fatalf("capture: %v", err)
	}
	f := freshFile(t)
	if !installExtendedACL(t, f) {
		t.Skip("filesystem does not support posix access ACLs")
	}
	if _, err := CreatorOnlyCommitment(f); !isCode(err, format.CodeAccessPolicyUnsupported) {
		t.Fatalf("commitment over an ACL artifact = %v, want CodeAccessPolicyUnsupported", err)
	}
	if err := SecureCreatorOnly(f, profile); err != nil {
		t.Fatalf("secure creator-only: %v", err)
	}
	got, err := CreatorOnlyCommitment(f)
	if err != nil {
		t.Fatalf("commitment after ACL removal: %v", err)
	}
	if got != profile.Commitment() {
		t.Fatalf("commitment = %x, want %x", got, profile.Commitment())
	}
}
