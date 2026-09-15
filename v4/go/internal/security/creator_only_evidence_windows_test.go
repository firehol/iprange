//go:build windows

package security

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// TestPlainWriteDoesNotInstallTheCreatorOnlyPolicy pins the measured
// descriptor fact behind the wave-19.25 Windows maintenance failures: a file
// created by os.WriteFile with mode 0600 inherits the parent directory's
// access entries, so its descriptor is not SE_DACL_PROTECTED and carries more
// than the single creator ACE. The creator-only proof therefore refuses it
// with the access-policy class, and residue a publisher really left behind
// must be created through CreatePrivate — the way the product creates every
// private artifact — rather than by a mode the platform ignores.
func TestPlainWriteDoesNotInstallTheCreatorOnlyPolicy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "inherited.tmp")
	if err := os.WriteFile(path, []byte("partial"), 0o600); err != nil {
		t.Fatalf("seed the residue: %v", err)
	}
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("open the residue: %v", err)
	}
	defer file.Close()

	var control windows.SECURITY_DESCRIPTOR_CONTROL
	var aces uint16
	control, aces = readDescriptorFacts(t, file)
	if control&seDaclProtected != 0 {
		t.Fatalf("an inherited descriptor reported SE_DACL_PROTECTED (control=%#x)", control)
	}
	if aces < 2 {
		t.Fatalf("an inherited descriptor reported %d access entries, expected the inherited set", aces)
	}
	_, commitErr := CreatorOnlyCommitment(file)
	var failure *format.Error
	if !errors.As(commitErr, &failure) || failure.Code != format.CodeAccessPolicyUnsupported {
		t.Fatalf("CreatorOnlyCommitment of inherited residue = %v, want the access-policy class", commitErr)
	}
}

// TestCreatePrivateInstallsTheCreatorOnlyPolicy pins the other half: the SDK
// creation yields exactly the protected single-ACE descriptor the retirement
// and the GC resolver prove, and its commitment is the captured profile's.
func TestCreatePrivateInstallsTheCreatorOnlyPolicy(t *testing.T) {
	profile, err := Capture()
	if err != nil {
		t.Fatalf("capture the creator profile: %v", err)
	}
	path := filepath.Join(t.TempDir(), "created.tmp")
	file, err := CreatePrivate(path, profile, true)
	if err != nil {
		t.Fatalf("CreatePrivate: %v", err)
	}
	defer file.Close()

	var control windows.SECURITY_DESCRIPTOR_CONTROL
	var aces uint16
	control, aces = readDescriptorFacts(t, file)
	if control&seDaclProtected == 0 {
		t.Fatalf("the created descriptor is not SE_DACL_PROTECTED (control=%#x)", control)
	}
	if aces != 1 {
		t.Fatalf("the created descriptor carries %d access entries, want the single creator ACE", aces)
	}
	commitment, err := CreatorOnlyCommitment(file)
	if err != nil {
		t.Fatalf("CreatorOnlyCommitment of the created file: %v", err)
	}
	var want [32]byte
	if want = profile.Commitment(); commitment != want {
		t.Fatalf("commitment = %x, want the captured profile commitment %x", commitment, want)
	}
}

// readDescriptorFacts returns the control flags and the access-entry count of
// one open file's security descriptor, the same facts the proof reads.
func readDescriptorFacts(t *testing.T, file *os.File) (windows.SECURITY_DESCRIPTOR_CONTROL, uint16) {
	t.Helper()
	sd, err := windows.GetSecurityInfo(windows.Handle(file.Fd()), windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatalf("GetSecurityInfo: %v", err)
	}
	control, _, err := sd.Control()
	if err != nil {
		t.Fatalf("descriptor control: %v", err)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		t.Fatalf("descriptor DACL: %v", err)
	}
	return control, dacl.AceCount
}
