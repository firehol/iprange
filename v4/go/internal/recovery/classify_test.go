package recovery

// Classification tests ported from the Rust recovery/classify_tests.
// The meta-page builder mirrors the bootstrap test helper over the
// format codecs; the cases pin the generation-order proof and the
// exact candidate projection rules.

import (
	"testing"

	"github.com/firehol/iprange/v4/go/internal/bootstrap"
	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/publication"
	"github.com/firehol/iprange/v4/go/internal/validation"
)

// directMetaPage builds one valid direct-v4 meta page image.
func directMetaPage(txn uint64, mutate func(*format.Meta)) []byte {
	page := make([]byte, format.PageSize)
	copy(page[0:8], format.MainMagic[:])
	format.PutU16(page[8:10], format.MetaSize)
	page[10] = format.PageShift
	page[11] = format.AddressFamilyIPv4
	page[12] = format.ValueKindDirect
	format.PutU64(page[32:40], 1) // database id
	format.PutU64(page[48:56], txn)
	for i := 56; i < 72; i++ {
		page[i] = 0x11
	}
	format.PutU64(page[72:80], 2) // page count
	format.PutU32(page[252:256], format.MetaCRC32C(page))
	meta, ok := format.ParseIdentity(page)
	if !ok {
		panic("test meta page not identity-readable")
	}
	if mutate != nil {
		mutate(&meta)
		// Re-encode the mutated scalars and re-seal.
		format.PutU64(page[48:56], meta.TxnID)
		copy(page[56:72], meta.CommitNonce[:])
		format.PutU64(page[80:88], meta.RangeRecordCount)
		format.PutU32(page[252:256], format.MetaCRC32C(page))
	}
	return page
}

// classifyPage wraps one raw page into its classified state.
func classifyPage(page []byte) (bootstrap.RecoveryMetaState, bool) {
	return bootstrap.ClassifyRecoveryMeta(page), true
}

// testIdentity is the portable identity used by the candidate
// projections (any non-zero identity: the tokens carry it verbatim).
func testIdentity() publication.LocalFileIdentity {
	return publication.LocalFileIdentityFromDeviceInode(7, 7)
}

func TestEqualCreationMetasExposeOneDeterministicNewestCandidate(t *testing.T) {
	page := directMetaPage(1, nil)
	s0, h0 := classifyPage(page)
	s1, h1 := classifyPage(page)
	classified := classifyMetas([2]bootstrap.RecoveryMetaState{s0, s1}, [2]bool{h0, h1})
	if !classified.order.proven || classified.order.current != 1 || classified.order.hasPrevious {
		t.Fatalf("order %+v, want proven current 1", classified.order)
	}
	candidates := classified.candidates(testIdentity())
	if candidates[0] == nil || candidates[0].Label != CandidateNewest || candidates[0].MetaPage != 1 {
		t.Fatalf("candidate 0 %+v", candidates[0])
	}
	if candidates[1] != nil {
		t.Fatalf("candidate 1 %+v, want none", candidates[1])
	}
}

func TestAdjacentMetasExposeNewestThenPrevious(t *testing.T) {
	oldPage := directMetaPage(1, nil)
	newPage := directMetaPage(2, func(m *format.Meta) {
		m.CommitNonce = [16]byte{3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3}
	})
	// Rust: page0 = new (txn 2), page1 = old (txn 1).
	s0, h0 := classifyPage(newPage)
	s1, h1 := classifyPage(oldPage)
	classified := classifyMetas([2]bootstrap.RecoveryMetaState{s0, s1}, [2]bool{h0, h1})
	if !classified.order.proven || classified.order.current != 0 || !classified.order.hasPrevious || classified.order.previous != 1 {
		t.Fatalf("order %+v, want proven current 0 previous 1", classified.order)
	}
	candidates := classified.candidates(testIdentity())
	if candidates[0] == nil || candidates[0].Label != CandidateNewest ||
		candidates[1] == nil || candidates[1].Label != CandidatePrevious {
		t.Fatalf("candidates %+v %+v", candidates[0], candidates[1])
	}
}

func TestSwappedAdjacentMetasAreUnorderedNotCurrent(t *testing.T) {
	oldPage := directMetaPage(1, nil)
	newPage := directMetaPage(2, func(m *format.Meta) {
		m.CommitNonce = [16]byte{3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3}
	})
	// Rust: page0 = old (txn 1), page1 = new (txn 2): the newer meta
	// sits on the wrong parity page.
	s0, h0 := classifyPage(oldPage)
	s1, h1 := classifyPage(newPage)
	classified := classifyMetas([2]bootstrap.RecoveryMetaState{s0, s1}, [2]bool{h0, h1})
	if classified.order.proven {
		t.Fatalf("order %+v, want unproven", classified.order)
	}
	candidates := classified.candidates(testIdentity())
	if candidates[0] == nil || candidates[0].Label != CandidateUnorderedMeta0 ||
		candidates[1] == nil || candidates[1].Label != CandidateUnorderedMeta1 {
		t.Fatalf("candidates %+v %+v", candidates[0], candidates[1])
	}
	progress, err := classified.progress()
	if err != nil {
		t.Fatal(err)
	}
	if progress.FindingsFor(validation.ReasonMetaInvalid) != 1 {
		t.Fatalf("progress %+v, want one MetaInvalid", progress)
	}
}

func TestUnreadableCurrentDoesNotPromoteThePreviousMeta(t *testing.T) {
	oldPage := directMetaPage(1, nil)
	currentPage := directMetaPage(2, func(m *format.Meta) {
		m.CommitNonce = [16]byte{3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3}
		m.RangeRecordCount = 1 // recovery-invalid: records without a root
	})
	s0, h0 := classifyPage(currentPage)
	s1, h1 := classifyPage(oldPage)
	classified := classifyMetas([2]bootstrap.RecoveryMetaState{s0, s1}, [2]bool{h0, h1})
	if !classified.order.proven || classified.order.current != 0 || !classified.order.hasPrevious {
		t.Fatalf("order %+v, want proven current 0 previous 1", classified.order)
	}
	if _, ok := classified.currentRecoveryMeta(); ok {
		t.Fatal("recovery-invalid current reported recoverable")
	}
	candidates := classified.candidates(testIdentity())
	if candidates[0] == nil || candidates[0].Label != CandidatePrevious {
		t.Fatalf("candidate 0 %+v, want Previous", candidates[0])
	}
	if candidates[1] != nil {
		t.Fatalf("candidate 1 %+v, want none", candidates[1])
	}
}
