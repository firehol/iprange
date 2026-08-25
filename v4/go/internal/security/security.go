//go:build !windows

// Package security implements the creator-only creation and access-policy
// proof for coordination artifacts (Rust publication/security.rs +
// security/posix.rs + the per-OS ACL machines): an artifact is created
// with mode 0600, any inherited access ACL is removed, and the resulting
// single-link regular file must prove the creating user through a
// SHA-256 commitment over the IPR4PSEC domain, the owner uid, and the
// creator mode. This is the single authority for the surface; the live
// lifecycle and the worker control page both compose it. The error
// classes are the Rust NamespaceError classes transported as format
// codes (AccessPolicyUnsupported for the proof failure); callers map
// them to their context exactly like Rust: the live namespace folds
// AccessPolicy to WrongState (live_namespace::namespace_error), the
// worker folds everything to Conflict, and the publication resolver
// (chunk 4-8) keeps the problem.rs classes.

package security

import (
	"crypto/sha256"
	"encoding/binary"
	"os"

	"golang.org/x/sys/unix"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// CreatorMode is the only permitted mode of a creator-only artifact
// (Rust security CREATOR_MODE).
const CreatorMode = 0o600

// commitmentDomain is the exact domain string of the ownership
// commitment (Rust COMMITMENT_DOMAIN).
const commitmentDomain = "IPR4PSEC"

// Profile is the creator identity captured before creation (Rust
// security::Profile): the effective uid and its commitment.
type Profile struct {
	uid        uint32
	commitment [32]byte
}

// Capture records the effective uid and its commitment (Rust
// Profile::capture).
func Capture() (Profile, error) {
	uid := uint32(unix.Geteuid())
	return Profile{uid: uid, commitment: commitment(uid)}, nil
}

// Commitment returns the captured commitment.
func (p *Profile) Commitment() [32]byte {
	return p.commitment
}

// SecureCreatorOnly applies the creator-only policy to one open
// artifact: mode exactly 0600, no inherited access ACL, and the
// single-link regular-file ownership proof against the profile (Rust
// security::secure_creator_only). Failures report the Rust problem
// classes: CodeAccessPolicyUnsupported when the policy cannot be
// proved, CodeDurabilityUnsupported when the filesystem lacks the
// required ACL operations, CodeIO for operation failures.
// CreatorOnlySupported reports whether the secure creator-only
// artifact policy is implemented on this platform. Only the linux
// xattr machine is reachable from pure Go: the darwin filesec and
// other-OS libc ACL machines would need cgo (Decision 2A forbids it),
// so every other target refuses honestly. The predicate drives the
// platform gates of the live and publication test suites.
func CreatorOnlySupported() bool { return aclSupported }

func SecureCreatorOnly(f *os.File, profile Profile) error {
	if err := f.Chmod(CreatorMode); err != nil {
		return ioError("apply creator-only mode", err)
	}
	if err := removeInheritedACL(f); err != nil {
		return err
	}
	meta, err := creatorOnlyMetadata(f)
	if err != nil {
		return err
	}
	if meta.Uid != profile.uid || commitment(meta.Uid) != profile.commitment {
		return accessPolicy()
	}
	return nil
}

// CreatorOnlyCommitment proves the creator-only policy of one open
// artifact and returns the ownership commitment of its current owner
// (Rust security::creator_only_commitment). Production consumers land
// with the publication resolver slice (chunk 4-8, Rust
// reservation_inspection and recovery scratch surfaces); tests cover
// the proof today.
func CreatorOnlyCommitment(f *os.File) ([32]byte, error) {
	meta, err := creatorOnlyMetadata(f)
	if err != nil {
		return [32]byte{}, err
	}
	return commitment(meta.Uid), nil
}

// creatorOnlyMetadata proves the artifact is one regular file with one
// link, carries no inherited access ACL, and has exactly mode 0600
// (Rust creator_only_metadata). The uid of the owning user is returned
// for the commitment comparison.
func creatorOnlyMetadata(f *os.File) (unix.Stat_t, error) {
	// The ACL proof runs first, exactly like Rust creator_only_metadata:
	// a non-ENODATA xattr failure combined with a mode mismatch must
	// report the ACL operation class, not the ownership class.
	if err := requireTrivialACL(f); err != nil {
		return unix.Stat_t{}, err
	}
	var stat unix.Stat_t
	if err := unix.Fstat(int(f.Fd()), &stat); err != nil {
		return stat, ioError("creator-only metadata", err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1 || stat.Mode&0o7777 != CreatorMode {
		return stat, accessPolicy()
	}
	return stat, nil
}

// commitment is the SHA-256 ownership commitment over the exact domain,
// the little-endian uid, and the little-endian creator mode (Rust
// security commitment). Sum256 keeps the digest on the stack, so the
// verify path never allocates.
func commitment(uid uint32) [32]byte {
	var leUID, leMode [4]byte
	binary.LittleEndian.PutUint32(leUID[:], uid)
	binary.LittleEndian.PutUint32(leMode[:], CreatorMode)
	var input [16]byte
	copy(input[0:8], commitmentDomain)
	copy(input[8:12], leUID[:])
	copy(input[12:16], leMode[:])
	return sha256.Sum256(input[:])
}

func accessPolicy() error {
	return &format.Error{Code: format.CodeAccessPolicyUnsupported, Detail: "creator-only access policy is not proved"}
}

func ioError(operation string, cause error) error {
	return &format.Error{Code: format.CodeIO, Detail: operation + ": " + cause.Error()}
}
