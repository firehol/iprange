//go:build !windows

// The creator-only proof tests cover the POSIX ownership commitment
// (security.go is the !windows surface); the Windows stub has no
// creator identity and is covered by the cross-compile builds.
package security

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// freshFile returns one new regular file in a private directory.
func freshFile(t *testing.T) *os.File {
	t.Helper()
	dir := t.TempDir()
	f, err := os.OpenFile(filepath.Join(dir, "artifact"), os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatalf("create test artifact: %v", err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}

// TestCommitmentVector pins the exact SHA-256 commitment domain (Rust
// security posix.rs commitment): "IPR4PSEC" || uid_le32 || 0600_le32.
func TestCommitmentVector(t *testing.T) {
	profile, err := Capture()
	if err != nil {
		t.Fatalf("capture: %v", err)
	}
	var want [32]byte
	h := sha256.New()
	h.Write([]byte("IPR4PSEC"))
	var leUID, leMode [4]byte
	binary.LittleEndian.PutUint32(leUID[:], uint32(unix.Geteuid()))
	binary.LittleEndian.PutUint32(leMode[:], CreatorMode)
	h.Write(leUID[:])
	h.Write(leMode[:])
	copy(want[:], h.Sum(nil))
	if got := profile.Commitment(); got != want {
		t.Fatalf("commitment = %x, want %x", got, want)
	}
}

// TestCommitmentKnownAnswer pins the commitment bytes for uid 0 with
// the hardcoded SHA-256 of "IPR4PSEC" || 00000000_le32 || 0600_le32
// (0x00000180_le32), so an algorithm-level drift in the domain or
// layout fails even when the current uid changes between machines.
func TestCommitmentKnownAnswer(t *testing.T) {
	const wantHex = "dbf2a75f0986fa25a62c4306119252ba80d02d1e8b5991dc4f408069456c33e6"
	want, err := hex.DecodeString(wantHex)
	if err != nil {
		t.Fatalf("decode vector: %v", err)
	}
	if got := commitment(0); got != [32]byte(want) {
		t.Fatalf("commitment(0) = %x, want %x", got, want)
	}
}

// TestSecureCreatorOnlyProvesTheArtifact mirrors Rust
// private_creation_is_exclusive_nofollow_and_creator_only: a fresh
// artifact proves, a chmod-0644 artifact fails the policy, and the
// proof re-applies mode 0600.
func TestSecureCreatorOnlyProvesTheArtifact(t *testing.T) {
	if !CreatorOnlySupported() {
		t.Skip("the creator-only xattr machine is linux-only in pure Go")
	}
	profile, err := Capture()
	if err != nil {
		t.Fatalf("capture: %v", err)
	}
	f := freshFile(t)
	if err := f.Chmod(0o644); err != nil {
		t.Fatalf("chmod 0644: %v", err)
	}
	_, err = CreatorOnlyCommitment(f)
	if !isCode(err, format.CodeAccessPolicyUnsupported) {
		t.Fatalf("commitment over a 0644 artifact = %v, want CodeAccessPolicyUnsupported", err)
	}
	if err := SecureCreatorOnly(f, profile); err != nil {
		t.Fatalf("secure creator-only: %v", err)
	}
	got, err := CreatorOnlyCommitment(f)
	if err != nil {
		t.Fatalf("commitment after proof: %v", err)
	}
	if got != profile.Commitment() {
		t.Fatalf("commitment = %x, want %x", got, profile.Commitment())
	}
	var stat unix.Stat_t
	if err := unix.Fstat(int(f.Fd()), &stat); err != nil {
		t.Fatalf("fstat: %v", err)
	}
	if stat.Mode&0o7777 != 0o600 {
		t.Fatalf("mode after proof = %o, want 600", stat.Mode&0o7777)
	}
	if stat.Uid != uint32(unix.Geteuid()) {
		t.Fatalf("uid after proof = %d, want %d", stat.Uid, unix.Geteuid())
	}
}

// TestSecureCreatorOnlyRejectsANonRegularArtifact pins the single-link
// regular-file rule: a directory cannot prove the policy.
func TestSecureCreatorOnlyRejectsANonRegularArtifact(t *testing.T) {
	if !CreatorOnlySupported() {
		t.Skip("the creator-only xattr machine is linux-only in pure Go")
	}
	profile, err := Capture()
	if err != nil {
		t.Fatalf("capture: %v", err)
	}
	dir := t.TempDir()
	f, err := os.Open(dir)
	if err != nil {
		t.Fatalf("open directory: %v", err)
	}
	defer f.Close()
	if err := SecureCreatorOnly(f, profile); !isCode(err, format.CodeAccessPolicyUnsupported) {
		t.Fatalf("directory proof = %v, want CodeAccessPolicyUnsupported", err)
	}
}

// TestMultiLinkArtifactFailsThePolicy mirrors the Rust single-link
// rule: a second hard link fails the ownership proof.
func TestMultiLinkArtifactFailsThePolicy(t *testing.T) {
	if !CreatorOnlySupported() {
		t.Skip("the creator-only xattr machine is linux-only in pure Go")
	}
	profile, err := Capture()
	if err != nil {
		t.Fatalf("capture: %v", err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "artifact")
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatalf("create artifact: %v", err)
	}
	defer f.Close()
	if err := os.Link(path, filepath.Join(dir, "hardlink")); err != nil {
		t.Fatalf("hard link: %v", err)
	}
	if err := SecureCreatorOnly(f, profile); !isCode(err, format.CodeAccessPolicyUnsupported) {
		t.Fatalf("multi-link proof = %v, want CodeAccessPolicyUnsupported", err)
	}
}

func isCode(err error, code format.ErrorCode) bool {
	var fe *format.Error
	return errors.As(err, &fe) && fe.Code == code
}
