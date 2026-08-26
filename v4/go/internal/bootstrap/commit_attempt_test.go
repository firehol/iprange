package bootstrap

import (
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// commitMetaPage builds one valid direct-v4 meta page with an explicit
// transaction id, commit nonce, and declared page count.
func commitMetaPage(txn uint64, nonce [16]byte, pageCount uint64) []byte {
	page := make([]byte, format.PageSize)
	copy(page[0:8], format.MainMagic[:])
	format.PutU16(page[8:10], format.MetaSize)
	page[10] = format.PageShift
	page[11] = format.AddressFamilyIPv4
	page[12] = format.ValueKindDirect
	copy(page[16:32], "direct\x00")
	copy(page[32:48], testDBID[:])
	format.PutU64(page[48:56], txn)
	copy(page[56:72], nonce[:])
	format.PutU64(page[72:80], pageCount)
	format.PutU32(page[252:256], format.MetaCRC32C(page))
	return page
}

// TestResolveCommitAttemptUsesBothExactMetaIdentities ports the Rust
// bootstrap_tests commit_resolution_uses_both_exact_meta_identities
// test: the classification reads the exact transaction and nonce of
// both meta pages, not just the selected page.
func TestResolveCommitAttemptUsesBothExactMetaIdentities(t *testing.T) {
	old := commitMetaPage(1, testNonce, 2)
	current := commitMetaPage(2, [16]byte{3}, 2)
	bytes := make([]byte, 0, 2*format.PageSize)
	bytes = append(bytes, current...)
	bytes = append(bytes, old...)
	p0, p1 := bytes[:format.PageSize], bytes[format.PageSize:]
	physical := uint64(len(bytes))

	resolution, err := ResolveCommitAttempt(p0, p1, physical, testDBID, 2, [16]byte{3})
	if err != nil {
		t.Fatal(err)
	}
	if resolution != CommitAttemptCommitted {
		t.Fatalf("exact current attempt = %v, want Committed", resolution)
	}
	resolution, err = ResolveCommitAttempt(p0, p1, physical, testDBID, 2, [16]byte{9})
	if err != nil {
		t.Fatal(err)
	}
	if resolution != CommitAttemptNotCommitted {
		t.Fatalf("same-transaction wrong nonce = %v, want NotCommitted", resolution)
	}
	resolution, err = ResolveCommitAttempt(p0, p1, physical, testDBID, 3, [16]byte{9})
	if err != nil {
		t.Fatal(err)
	}
	if resolution != CommitAttemptNotCommitted {
		t.Fatalf("future transaction = %v, want NotCommitted", resolution)
	}

	later := commitMetaPage(3, [16]byte{4}, 2)
	advanced := make([]byte, 0, 2*format.PageSize)
	advanced = append(advanced, current...)
	advanced = append(advanced, later...)
	a0, a1 := advanced[:format.PageSize], advanced[format.PageSize:]
	resolution, err = ResolveCommitAttempt(a0, a1, uint64(len(advanced)), testDBID, 1, [16]byte{9})
	if err != nil {
		t.Fatal(err)
	}
	if resolution != CommitAttemptSupersededUnknown {
		t.Fatalf("superseded old attempt = %v, want SupersededUnknown", resolution)
	}

	// A different database id is refused with the static identity class.
	if _, err := ResolveCommitAttempt(p0, p1, physical, [16]byte{0xaa}, 2, [16]byte{3}); err == nil {
		t.Fatal("different database id resolved without error")
	} else if problem, ok := AsProblem(err); !ok || problem.Kind != ProblemStaticIdentityMismatch {
		t.Fatalf("different database id error = %v, want static identity mismatch", err)
	}
}
