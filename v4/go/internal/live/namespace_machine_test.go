// Focused tests of the retained-directory machine (Rust
// publication/namespace/unix.rs Directory) and its live namespace
// wiring: the durability filesystem/name_max preconditions, the
// no-symlink parent rule, the single-component parent semantics, and
// the security-failure class mapping of the live path.

package live

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
)

func testAuthority() cleanupAuthority {
	return cleanupAuthority{
		attemptID:     [16]byte{1},
		ordinal:       0,
		kind:          ArtifactOwnedMain,
		directoryRole: DirectoryRoleMainFile,
	}
}

// TestCreatePrivateRefusesOverlongBasename pins the Rust InvalidName
// class: a basename longer than the directory name_max fails before
// any syscall (Directory::create require_name_lengths), even though
// LocalBasename allows up to 512 bytes.
func TestCreatePrivateRefusesOverlongBasename(t *testing.T) {
	dir := t.TempDir()
	name := strings.Repeat("x", 300)
	created, failure := createPrivate(filepath.Join(dir, name), testAuthority())
	if created.file != nil {
		t.Fatal("overlong basename created an artifact")
	}
	expectCode(t, failure.cause, format.CodeNameInvalid)
}

// TestCreatePrivateRefusesSymlinkedParent pins the no-symlink parent
// rule: Directory::open uses O_NOFOLLOW, so a parent reached through a
// symlink reports the Io class (Rust maps the ELOOP open failure to
// Io, not to a wrong-mode class).
func TestCreatePrivateRefusesSymlinkedParent(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real")
	if err := os.Mkdir(real, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	created, failure := createPrivate(filepath.Join(link, "artifact"), testAuthority())
	if created.file != nil {
		t.Fatal("symlinked parent created an artifact")
	}
	expectCode(t, failure.cause, format.CodeIO)
}

// TestCreateLiveRelativeSingleComponentRefuses pins the Rust
// Path::parent semantics of bind_path: a bare relative path has the
// empty parent, whose open reports Missing; parent_identity folds that
// to the Io(NotFound) class before any artifact is created.
func TestCreateLiveRelativeSingleComponentRefuses(t *testing.T) {
	dir := t.TempDir()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.Chdir(wd); err != nil {
			t.Fatal(err)
		}
	}()
	result, err := CreateLive("feed.v4", format.AddressFamilyIPv4, format.ValueKindDirect, format.StructureKindNone, [16]byte{}, 1, neverCheck)
	if err != nil {
		t.Fatal(err)
	}
	if result.State != CreationStateNotCreated {
		t.Fatalf("state = %v, want NotCreated", result.State)
	}
	expectCode(t, result.Cause, format.CodeIO)
	if _, err := os.Lstat("feed.v4"); !os.IsNotExist(err) {
		t.Fatalf("main exists after refused create: %v", err)
	}
	if _, err := os.Lstat("feed.v4.readers"); !os.IsNotExist(err) {
		t.Fatalf("sidecar exists after refused create: %v", err)
	}
}

// TestLiveSecurityErrorMapsToLiveClasses pins the live-path security
// mapping (Rust create_private folds security errors through
// namespace_error): an access-policy proof failure surfaces as the
// WrongState ownership class, never as the resolver's
// CodeAccessPolicyUnsupported class.
func TestLiveSecurityErrorMapsToLiveClasses(t *testing.T) {
	err := liveSecurityError(&format.Error{Code: format.CodeAccessPolicyUnsupported, Detail: "creator-only access policy is not proved"})
	expectCode(t, err, format.CodeWrongState)
	if err.Error() != "iprange v4 error 11: live file ownership changed" {
		t.Fatalf("detail = %q, want the Rust ownership-changed detail", err.Error())
	}
}
