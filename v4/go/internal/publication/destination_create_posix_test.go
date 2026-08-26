//go:build linux || freebsd || darwin

// Private creation facts (Rust namespace_tests.rs
// private_creation_is_exclusive_nofollow_and_creator_only). The test
// runs where the pure-Go creator-only proof exists (linux xattr,
// freebsd, and darwin raw-syscall ACL machines); the remaining
// platforms refuse honestly in the security package.

package publication

import (
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/firehol/iprange/v4/go/internal/live"
)

func TestDestinationCreateIsExclusiveNofollowAndCreatorOnly(t *testing.T) {
	dir := t.TempDir()
	d, err := bindDestination(filepath.Join(dir, "output.v4"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.dir.Close()
	name, err := d.outputName(sixteen(1))
	if err != nil {
		t.Fatal(err)
	}
	f, err := d.create(name)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	// A foreign mode fails the creator-only proof before the policy is
	// reapplied (Rust set_permissions(0o644) -> AccessPolicy).
	if err := f.Chmod(0o644); err != nil {
		t.Fatal(err)
	}
	if err := d.verifyCreated(f); !isNamespace(err, live.NamespaceAccessPolicy) {
		t.Fatalf("verifyCreated(0644) = %v, want AccessPolicy", err)
	}
	if err := d.secureCreated(f); err != nil {
		t.Fatal(err)
	}
	var st unix.Stat_t
	if err := unix.Fstat(int(f.Fd()), &st); err != nil {
		t.Fatal(err)
	}
	if st.Mode&0o7777 != 0o600 {
		t.Fatalf("mode = %o, want 0600", st.Mode&0o7777)
	}
	if st.Uid != uint32(unix.Geteuid()) {
		t.Fatalf("uid = %d, want euid %d", st.Uid, unix.Geteuid())
	}
	if err := d.verifyCreated(f); err != nil {
		t.Fatalf("verifyCreated after secureCreated = %v", err)
	}
	// The private name is exclusive.
	second, err := d.create(name)
	if err == nil {
		second.Close()
		t.Fatal("second create succeeded on an existing private name")
	}
	if !isNamespace(err, live.NamespaceExists) {
		t.Fatalf("second create = %v, want Exists", err)
	}
}
