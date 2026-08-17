package bootstrap

import (
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
)

var testDBID = [16]byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10}
var testNonce = [16]byte{0x11, 0x12, 0x13, 0x14, 0x15, 0x16, 0x17, 0x18, 0x19, 0x1a, 0x1b, 0x1c, 0x1d, 0x1e, 0x1f, 0x20}

// metaPage builds one valid direct-v4 meta page with the given transaction
// id and declared page count.
func metaPage(txn, pageCount uint64) []byte {
	page := make([]byte, format.PageSize)
	copy(page[0:8], format.MainMagic[:])
	format.PutU16(page[8:10], format.MetaSize)
	page[10] = format.PageShift
	page[11] = format.AddressFamilyIPv4
	page[12] = format.ValueKindDirect
	copy(page[16:32], "direct\x00")
	copy(page[32:48], testDBID[:])
	format.PutU64(page[48:56], txn)
	copy(page[56:72], testNonce[:])
	format.PutU64(page[72:80], pageCount)
	format.PutU32(page[252:256], format.MetaCRC32C(page))
	return page
}

// metaStructured builds a valid empty structured meta page with the given
// structure kind code (nonzero), transaction id, and page count: the empty
// membership/structured dictionary relations (id limits 1, zero roots).
func metaStructured(kind uint8, txn, pageCount uint64) []byte {
	page := make([]byte, format.PageSize)
	copy(page[0:8], format.MainMagic[:])
	format.PutU16(page[8:10], format.MetaSize)
	page[10] = format.PageShift
	page[11] = format.AddressFamilyIPv4
	page[12] = format.ValueKindStructured
	page[13] = kind
	copy(page[16:32], "struct\x00")
	copy(page[32:48], testDBID[:])
	format.PutU64(page[48:56], txn)
	copy(page[56:72], testNonce[:])
	format.PutU64(page[72:80], pageCount)
	format.PutU64(page[112:120], 1) // membership_id_limit
	format.PutU64(page[208:216], 1) // structure_id_limit
	format.PutU32(page[252:256], format.MetaCRC32C(page))
	return page
}

// mustFormatInvalid asserts the open failure carries the FormatInvalid code.
func mustFormatInvalid(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	ferr, ok := err.(*format.Error)
	if !ok {
		t.Fatalf("expected *format.Error, got %T", err)
	}
	if ferr.Code != format.CodeFormatInvalid {
		t.Fatalf("code %v want FormatInvalid", ferr.Code)
	}
}

func TestOpenEqualTransactionPair(t *testing.T) {
	p0 := metaPage(1, 4)
	p1 := metaPage(1, 4)
	res, err := Open(p0, p1, 4*format.PageSize, ModeImmutableReader)
	if err != nil {
		t.Fatal(err)
	}
	if res.Selection != SelectionProvenCurrent || res.SelectedMetaPage != 1 {
		t.Fatalf("selection %v page %d, want ProvenCurrent/1", res.Selection, res.SelectedMetaPage)
	}
	if res.CommittedBytes != 4*format.PageSize || res.PhysicalBytes != 4*format.PageSize {
		t.Fatalf("committed %d physical %d, want 4 pages", res.CommittedBytes, res.PhysicalBytes)
	}
	// Writer mode accepts the same provable pair.
	if _, err := Open(p0, p1, 4*format.PageSize, ModeWriter); err != nil {
		t.Fatal("writer open:", err)
	}
}

func TestOpenAdjacentTransactionParity(t *testing.T) {
	// p0 txn=2 (even, page 0) -> p1 txn=1: higher is p0 with parity 0.
	res, err := Open(metaPage(2, 4), metaPage(1, 4), 4*format.PageSize, ModeWriter)
	if err != nil {
		t.Fatal(err)
	}
	if res.SelectedMetaPage != 0 || res.Meta.TxnID != 2 {
		t.Fatalf("selected page %d txn %d, want 0/2", res.SelectedMetaPage, res.Meta.TxnID)
	}
	// p0 txn=1 -> p1 txn=2: higher is p1 with even txn on page 1: swapped.
	mustFormatInvalid(t, mustErr(Open(metaPage(1, 4), metaPage(2, 4), 4*format.PageSize, ModeWriter)))
}

func TestOpenTransactionGap(t *testing.T) {
	mustFormatInvalid(t, mustErr(Open(metaPage(1, 4), metaPage(3, 4), 4*format.PageSize, ModeWriter)))
}

func TestOpenEqualTransactionDisagreement(t *testing.T) {
	p0 := metaPage(1, 4)
	p1 := metaPage(1, 4)
	format.PutU64(p1[48:56], 1) // same txn, different nonce below
	copy(p1[56:72], []byte{0x99})
	format.PutU32(p1[252:256], format.MetaCRC32C(p1))
	mustFormatInvalid(t, mustErr(Open(p0, p1, 4*format.PageSize, ModeImmutableReader)))
}

func TestOpenSoleMeta(t *testing.T) {
	p0 := metaPage(1, 4)
	p0[0] = 'X' // damaged magic: not identity-readable
	p1 := metaPage(1, 4)
	res, err := Open(p0, p1, 4*format.PageSize, ModeImmutableReader)
	if err != nil {
		t.Fatal(err)
	}
	if res.Selection != SelectionSoleMeta1 || res.SelectedMetaPage != 1 {
		t.Fatalf("selection %v page %d, want SoleMeta1/1", res.Selection, res.SelectedMetaPage)
	}
	// A writer cannot prove the current generation from a sole meta.
	_, err = Open(p0, p1, 4*format.PageSize, ModeWriter)
	if err == nil {
		t.Fatal("writer accepted a sole meta")
	}
	ferr, ok := err.(*format.Error)
	if !ok || ferr.Code != format.CodeFormatInvalid {
		t.Fatalf("writer sole-meta error %v, want FormatInvalid", err)
	}
}

func TestOpenNoBootstrapMeta(t *testing.T) {
	p0 := metaPage(1, 4)
	p1 := metaPage(1, 4)
	p0[0] = 'X'
	p1[0] = 'X'
	mustFormatInvalid(t, mustErr(Open(p0, p1, 4*format.PageSize, ModeImmutableReader)))
}

func TestOpenIdentityMismatch(t *testing.T) {
	p0 := metaPage(1, 4)
	p1 := metaPage(1, 4)
	copy(p1[32:48], []byte{0x99})
	format.PutU32(p1[252:256], format.MetaCRC32C(p1))
	mustFormatInvalid(t, mustErr(Open(p0, p1, 4*format.PageSize, ModeImmutableReader)))
}

func TestOpenImmutableRequiresExactLength(t *testing.T) {
	p0 := metaPage(1, 4)
	p1 := metaPage(1, 4)
	// A 6-page physical file with a 4-page committed generation: the
	// immutable reader refuses, the writer accepts (tail to be trimmed).
	_, err := Open(p0, p1, 6*format.PageSize, ModeImmutableReader)
	ferr, ok := err.(*format.Error)
	if !ok || ferr.Code != format.CodeFormatInvalid {
		t.Fatalf("immutable tail error %v, want FormatInvalid", err)
	}
	res, err := Open(p0, p1, 6*format.PageSize, ModeWriter)
	if err != nil {
		t.Fatal("writer with tail:", err)
	}
	if res.CommittedBytes != 4*format.PageSize || res.PhysicalBytes != 6*format.PageSize {
		t.Fatalf("committed %d physical %d, want 4/6 pages", res.CommittedBytes, res.PhysicalBytes)
	}
}

func TestOpenStructuredUnknownKind(t *testing.T) {
	// Empty structured file with an unknown nonzero kind: count/root
	// validation passes, the open reports UnsupportedStructure after pair
	// selection (Rust finish_open).
	p0 := metaStructured(2, 1, 4)
	p1 := metaStructured(2, 1, 4)
	_, err := Open(p0, p1, 4*format.PageSize, ModeImmutableReader)
	if err == nil {
		t.Fatal("expected error for unknown structure kind")
	}
	ferr, ok := err.(*format.Error)
	if !ok || ferr.Code != format.CodeUnsupportedStructure {
		t.Fatalf("error %v, want UnsupportedStructure", err)
	}
	// Zero structure kind on a structured file is a validation failure.
	p0 = metaStructured(0, 1, 4)
	p1 = metaStructured(0, 1, 4)
	mustFormatInvalid(t, mustErr(Open(p0, p1, 4*format.PageSize, ModeImmutableReader)))
}

func TestOpenGeometry(t *testing.T) {
	p0 := metaPage(1, 4)
	p1 := metaPage(1, 4)
	mustFormatInvalid(t, mustErr(Open(p0, p1, 1*format.PageSize, ModeImmutableReader)))
	mustFormatInvalid(t, mustErr(Open(p0, p1, 2*format.PageSize+100, ModeImmutableReader)))
	mustFormatInvalid(t, mustErr(Open(p0[:100], p1, 4*format.PageSize, ModeImmutableReader)))
}

func TestOpenPerMetaPhysicalLength(t *testing.T) {
	// pageCount 5 on a 4-page physical file: the meta is not
	// bootstrap-valid, so both candidates fail and no meta selects.
	p0 := metaPage(1, 5)
	p1 := metaPage(1, 5)
	mustFormatInvalid(t, mustErr(Open(p0, p1, 4*format.PageSize, ModeImmutableReader)))
	// With one valid candidate, immutable selection falls to the sole
	// valid meta (Rust select_candidates); writer refuses.
	p0 = metaPage(1, 5)
	p1 = metaPage(2, 4)
	res, err := Open(p0, p1, 4*format.PageSize, ModeImmutableReader)
	if err != nil {
		t.Fatal(err)
	}
	if res.Selection != SelectionSoleMeta1 || res.Meta.TxnID != 2 {
		t.Fatalf("selection %v txn %d, want SoleMeta1/2", res.Selection, res.Meta.TxnID)
	}
	_, err = Open(p0, p1, 4*format.PageSize, ModeWriter)
	if err == nil {
		t.Fatal("writer accepted a sole valid meta")
	}
}

// mustErr returns the error half of an Open call.
func mustErr(_ *Result, err error) error { return err }
