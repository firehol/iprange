//go:build linux

package live

// Live-arm namespace proof pins. Each live arm proves the retained
// identity of the opened descriptor and then re-proves the pathname
// through a bound parent directory before its reader mapping exists. The
// parent open is O_DIRECTORY | O_NOFOLLOW, so a magic symlink such as
// /proc/self cannot be bound as a directory and the refusal is the io
// class of that open (Rust NamespaceError::IoAt -> Error::Io), and a
// multi-link main is refused by the single-link identity capture as the
// namespace wrong-state class.
//
// The two live arms differ in one respect that must stay visible: Rust
// maps the source-guard path proofs through live_coordination
// (recovery/source_guard/live.rs bind_current) while the validation arm
// propagates them unchanged (validation/source.rs bind_live_main).

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/bootstrap"
	"github.com/firehol/iprange/v4/go/internal/format"
)

// procfsShapedPath is one regular file whose parent is a magic symlink,
// so binding the parent fails with ENOTDIR while the open of the file
// itself succeeds.
const procfsShapedPath = "/proc/self/cmdline"

func requireCode(t *testing.T, label string, err error, want format.ErrorCode) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: the request was accepted", label)
	}
	var fe *format.Error
	if !errors.As(err, &fe) {
		t.Fatalf("%s: %v is not a typed v4 error", label, err)
	}
	if fe.Code != want {
		t.Fatalf("%s: code = %v (%v), want %v", label, fe.Code, fe.Detail, want)
	}
}

// The registered live reader is Rust reader_core/live.rs LiveReaderCore,
// whose identity and path proofs are not folded: both keep their own
// namespace classes.
func TestOpenLiveReaderNamespaceProofsAreNotFolded(t *testing.T) {
	_, err := OpenLiveReader(procfsShapedPath, nil)
	requireCode(t, "live reader on a procfs-shaped path", err, format.CodeIO)

	dir := t.TempDir()
	main := createLiveDatabaseForClassTest(t, dir)
	link := filepath.Join(dir, "linked.iprdb")
	if err := os.Link(main, link); err != nil {
		t.Skipf("hard links are unavailable: %v", err)
	}
	_, err = OpenLiveReader(link, nil)
	requireCode(t, "live reader on a hard-linked main", err, format.CodeWrongState)
}

// The immutable-source validation open is Rust validation/source.rs
// LiveSource::open: bind_live_main propagates verify_path unchanged, so
// the unbindable parent is the io class.
func TestOpenLiveValidationSourcePropagatesPathProofClass(t *testing.T) {
	opened, failure := OpenLiveValidationSource(procfsShapedPath, nil)
	if opened != nil {
		t.Fatal("the validation live source accepted a procfs-shaped path")
	}
	if failure == nil {
		t.Fatal("the validation live source reported no failure")
	}
	requireCode(t, "validation live source on a procfs-shaped path", failure.Cause, format.CodeIO)
}

// The source-guard arm of the same registration folds the same proof
// through the coordination class, matching Rust bind_current.
func TestOpenLiveSourceCurrentFoldsPathProofToCoordination(t *testing.T) {
	_, err := OpenLiveSourceCurrent(procfsShapedPath, nil)
	requireCode(t, "snapshot live source on a procfs-shaped path", err,
		format.CodeLiveRecoveryCoordinationUnavailable)

	dir := t.TempDir()
	main := createLiveDatabaseForClassTest(t, dir)
	link := filepath.Join(dir, "linked.iprdb")
	if err := os.Link(main, link); err != nil {
		t.Skipf("hard links are unavailable: %v", err)
	}
	_, err = OpenLiveSourceCurrent(link, nil)
	requireCode(t, "snapshot live source on a hard-linked main", err, format.CodeWrongState)
}

// The recovery candidate arm reports its path proofs as the
// candidate-changed class (Rust bind_candidate maps verify_path through
// candidate_changed), while the identity capture stays the namespace
// wrong-state class.
func TestOpenLiveSourceCandidatePathProofIsCandidateChanged(t *testing.T) {
	dir := t.TempDir()
	unbindable := filepath.Join(dir, "gone.iprdb")
	if err := os.WriteFile(unbindable, []byte("junk"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(unbindable)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := IdentityAnyLink(f)
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(unbindable); err != nil {
		t.Fatal(err)
	}
	token := bootstrap.RecoveryCandidateToken{MetaPage: 1, Device: identity.device,
		Inode: identity.inode, TransactionID: 3}
	source, err := OpenLiveSourceCandidate(procfsShapedPath, token, nil)
	if source != nil {
		t.Fatal("the candidate source accepted a procfs-shaped path")
	}
	// The token identity of a procfs file cannot match, so the
	// candidate-changed class is the first refusal; the proof that the
	// fold is in place is the class itself.
	requireCode(t, "candidate live source on a procfs-shaped path", err,
		format.CodeRecoveryCandidateChanged)
}
