//go:build linux || darwin

package live

// Refusal-class pins for the candidate-bound live source open (Rust
// recovery/source_guard/live.rs open_file + bind_candidate). The
// identity capture and the first path proof run over the
// lifetime-locked descriptor ahead of the reader mapping, so:
//
//   - a hard-linked main keeps the wrong-state class of the single-link
//     rule instead of becoming a coordination failure;
//   - a main too short to hold two meta pages is classified and refused
//     as candidate-changed instead of refused as a format problem by the
//     mapping geometry.

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/bootstrap"
	"github.com/firehol/iprange/v4/go/internal/format"
)

// tokenForPath builds one newest-candidate token bound to the identity
// of path, so the selection reaches the pair classification instead of
// stopping at the token comparison.
func tokenForPath(t *testing.T, path string) bootstrap.RecoveryCandidateToken {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	identity, err := IdentityAnyLink(f)
	if err != nil {
		t.Fatal(err)
	}
	return bootstrap.RecoveryCandidateToken{
		MetaPage: 1, Device: identity.device, Inode: identity.inode, TransactionID: 3,
	}
}

func createLiveDatabaseForClassTest(t *testing.T, dir string) string {
	t.Helper()
	main := filepath.Join(dir, "db.iprdb")
	if _, err := CreateLive(main, format.AddressFamilyIPv4, format.ValueKindDirect,
		format.StructureKindNone, [16]byte{}, 2, nil); err != nil {
		t.Fatalf("CreateLive: %v", err)
	}
	return main
}

// A hard-linked live main is refused by the single-link identity capture
// (Rust live_namespace::identity over retained_regular_identity
// with require_single_link = true): the namespace wrong-state class, not
// a coordination failure that the caller would be told to retry.
func TestOpenLiveSourceCandidateHardLinkedMainIsWrongState(t *testing.T) {
	main := createLiveDatabaseForClassTest(t, t.TempDir())
	link := main + ".link"
	if err := os.Link(main, link); err != nil {
		t.Skipf("hard links are unavailable: %v", err)
	}
	_, err := OpenLiveSourceCandidate(link, tokenForPath(t, link), nil)
	if err == nil {
		t.Fatal("a hard-linked live main was accepted")
	}
	var fe *format.Error
	if !errors.As(err, &fe) {
		t.Fatalf("%v is not a typed v4 error", err)
	}
	if fe.Code != format.CodeWrongState {
		t.Fatalf("code = %v (%v), want %v", fe.Code, fe.Detail, format.CodeWrongState)
	}
}

// A main too short to hold the two meta pages is untrusted recovery
// input: the pair cannot be classified, so the answer is the
// candidate-changed class of bind_candidate, not the format-invalid
// class of the reader-mapping geometry refusal.
func TestOpenLiveSourceCandidateShortMainIsCandidateChanged(t *testing.T) {
	dir := t.TempDir()
	short := filepath.Join(dir, "short.iprdb")
	if err := os.WriteFile(short, make([]byte, 5000), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := OpenLiveSourceCandidate(short, tokenForPath(t, short), nil)
	if err == nil {
		t.Fatal("a short junk live main was accepted")
	}
	var fe *format.Error
	if !errors.As(err, &fe) {
		t.Fatalf("%v is not a typed v4 error", err)
	}
	if fe.Code != format.CodeRecoveryCandidateChanged {
		t.Fatalf("code = %v (%v), want %v", fe.Code, fe.Detail, format.CodeRecoveryCandidateChanged)
	}
}

// A live main truncated to zero bytes still has a reader sidecar, so the
// arm reaches the pair classification over an empty bounded view. No
// committed generation can be proven from no bytes, and the answer is
// the candidate-changed class of bind_candidate — never the io class a
// zero-length mmap would have reported (Rust mapping.rs map_nonempty
// answers Ok(None) for the empty extent).
func TestOpenLiveSourceCandidateZeroLengthMainIsCandidateChanged(t *testing.T) {
	dir := t.TempDir()
	main := createLiveDatabaseForClassTest(t, dir)
	if err := os.Truncate(main, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(main + sidecarSuffix); err != nil {
		t.Skipf("the live database has no reader sidecar to make the shape meaningful: %v", err)
	}
	_, err := OpenLiveSourceCandidate(main, tokenForPath(t, main), nil)
	if err == nil {
		t.Fatal("a zero-length live main was accepted")
	}
	var fe *format.Error
	if !errors.As(err, &fe) {
		t.Fatalf("%v is not a typed v4 error", err)
	}
	if fe.Code != format.CodeRecoveryCandidateChanged {
		t.Fatalf("code = %v (%v), want %v", fe.Code, fe.Detail, format.CodeRecoveryCandidateChanged)
	}
}
