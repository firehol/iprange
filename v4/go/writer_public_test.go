package iprangedb

// Public live-writer facade tests (SOW-0025 chunk-6 design record D3):
// Create -> OpenWriter -> BeginDirect -> Assign/Clear/SetMetadataJSON ->
// Commit/Abort -> Close, verified through the public immutable reader.

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// isPubCode reports whether err carries code. The facade returns the
// internal format.Error value directly; ErrorCode is the same numeric
// table re-exported publicly, so the internal type carries the public
// classification (errors.go is the name authority, format/codes.go the
// value authority).
func isPubCode(err error, code ErrorCode) bool {
	var fe *format.Error
	return errors.As(err, &fe) && ErrorCode(fe.Code) == code
}

// pubCreate writes one fresh direct database through the public facade.
func pubCreate(t *testing.T, family AddressFamily, tag ValueTag) (string, CreateResult) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "pub.iprdb")
	created, err := Create(path, family, ValueKindDirect, StructureKindNone, tag)
	if err != nil {
		t.Fatal(err)
	}
	if created.DatabaseID == [16]byte{} || created.CommitNonce == [16]byte{} ||
		created.State != CreationStateCreated || created.SidecarID != [16]byte{} ||
		created.ReaderCapacity != 0 || created.DirectoryIdentity != nil ||
		created.MainIdentity != nil || created.SidecarIdentity != nil {
		t.Fatalf("created identity = %+v, want Created state with drawn ids and no live-pair surface", created)
	}
	return path, created
}

// TestPublicCreateWriteCommitReadBack runs the full public workflow and
// verifies ranges and metadata through the public reader.
func TestPublicCreateWriteCommitReadBack(t *testing.T) {
	requireFileCreation(t)
	tag, err := NewValueTag([]byte("go-public"))
	if err != nil {
		t.Fatal(err)
	}
	path, created := pubCreate(t, AddressFamilyIPv4, tag)
	w, err := OpenWriter(path, DefaultBudget())
	if err != nil {
		t.Fatal(err)
	}
	info, err := w.Info()
	if err != nil {
		t.Fatal(err)
	}
	if info.Family != AddressFamilyIPv4 || info.ValueKind != ValueKindDirect || info.TransactionID != 1 {
		t.Fatalf("writer info = family %d kind %d txn %d, want 4/1/1", info.Family, info.ValueKind, info.TransactionID)
	}
	if info.DatabaseID != created.DatabaseID || info.CommitNonce != created.CommitNonce {
		t.Fatal("writer info identity does not match the created identity")
	}

	tx, err := w.BeginDirect()
	if err != nil {
		t.Fatal(err)
	}
	if changed, err := tx.AssignV4(IPv4(10), IPv4(20), 123); err != nil || !changed {
		t.Fatalf("assign = changed %v err %v", changed, err)
	}
	meta := []byte(`{"fixture":"go-public-facade","producer":"go"}`)
	if changed, err := tx.SetMetadataJSON(meta); err != nil || !changed {
		t.Fatalf("metadata set = changed %v err %v", changed, err)
	}
	if _, err := tx.AssignV4(IPv4(30), IPv4(40), 7); !isPubCode(err, ErrorWrongState) {
		t.Fatalf("assign after metadata stage err = %v, want ErrorWrongState", err)
	}
	res, err := tx.Commit()
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != CommitCommitted || res.TransactionID != 2 || res.Err != nil {
		t.Fatalf("commit result = %+v err %v, want committed txn 2", res, err)
	}
	if res.DatabaseID != created.DatabaseID || res.CommitNonce == [16]byte{} {
		t.Fatalf("commit identity = %x/%x", res.DatabaseID, res.CommitNonce)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	// Every later writer call reports WrongState and a second Close is
	// idempotent success (Rust State::Closed parity).
	if _, err := w.Info(); !isPubCode(err, ErrorWrongState) {
		t.Fatalf("Info after close err = %v, want ErrorWrongState", err)
	}
	if _, err := w.BeginDirect(); !isPubCode(err, ErrorWrongState) {
		t.Fatalf("BeginDirect after close err = %v, want ErrorWrongState", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("second Close err = %v, want nil (idempotent)", err)
	}

	r, err := OpenImmutable(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if v, ok, err := r.LookupDirectV4(IPv4(15)); err != nil || !ok || v != 123 {
		t.Fatalf("lookup 15 = (%d, %v, %v), want (123, true, nil)", v, ok, err)
	}
	if v, ok, err := r.LookupDirectV4(IPv4(25)); err != nil || ok {
		t.Fatalf("lookup 25 = (%d, %v, %v), want absent", v, ok, err)
	}
	got, present, err := r.MetadataJSON()
	if err != nil || !present || !bytes.Equal(got, meta) {
		t.Fatalf("metadata = %q present %v err %v", got, present, err)
	}
}

// TestPublicAbortDiscards verifies Abort drops the draft: no range, no
// metadata, and the writer stays usable for a fresh transaction.
func TestPublicAbortDiscards(t *testing.T) {
	requireFileCreation(t)
	path, _ := pubCreate(t, AddressFamilyIPv4, ValueTag{})
	w, err := OpenWriter(path, DefaultBudget())
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	tx, err := w.BeginDirect()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.AssignV4(IPv4(1), IPv4(100), 9); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.SetMetadataJSON([]byte(`{"aborted":true}`)); err != nil {
		t.Fatal(err)
	}
	if err := tx.Abort(); err != nil {
		t.Fatal(err)
	}
	// Abort is idempotent-spent: the draft is gone, so Commit reports
	// ErrorNoPendingTransaction (Rust commit_attempt parity).
	if _, err := tx.Commit(); !isPubCode(err, ErrorNoPendingTransaction) {
		t.Fatalf("commit after abort err = %v, want ErrorNoPendingTransaction", err)
	}
	if err := tx.Abort(); !isPubCode(err, ErrorNoPendingTransaction) {
		t.Fatalf("abort after abort err = %v, want ErrorNoPendingTransaction", err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	r, err := OpenImmutable(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if _, ok, err := r.LookupDirectV4(IPv4(50)); err != nil || ok {
		t.Fatalf("aborted range visible: ok %v err %v", ok, err)
	}
	if _, present, err := r.MetadataJSON(); err != nil || present {
		t.Fatalf("aborted metadata visible: present %v err %v", present, err)
	}
}

// TestPublicCommitNoPending verifies an unchanged transaction reports
// ErrorNoPendingTransaction at commit (Rust commit_attempt parity).
func TestPublicCommitNoPending(t *testing.T) {
	requireFileCreation(t)
	path, _ := pubCreate(t, AddressFamilyIPv4, ValueTag{})
	w, err := OpenWriter(path, DefaultBudget())
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	tx, err := w.BeginDirect()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Commit(); !isPubCode(err, ErrorNoPendingTransaction) {
		t.Fatalf("unchanged commit err = %v, want ErrorNoPendingTransaction", err)
	}
}

// TestPublicBeginDirectRequiresDirect verifies a membership database
// refuses direct transactions with ErrorWrongValueKind.
func TestPublicBeginDirectRequiresDirect(t *testing.T) {
	requireFileCreation(t)
	path := filepath.Join(t.TempDir(), "membership.iprdb")
	if _, err := Create(path, AddressFamilyIPv4, ValueKindMembership, StructureKindNone, ValueTag{}); err != nil {
		t.Fatal(err)
	}
	w, err := OpenWriter(path, DefaultBudget())
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if _, err := w.BeginDirect(); !isPubCode(err, ErrorWrongValueKind) {
		t.Fatalf("BeginDirect on membership err = %v, want ErrorWrongValueKind", err)
	}
}

// TestPublicFamilyAndOrderGates verifies the mutation preconditions:
// wrong family and reversed ranges refuse with the exact codes.
func TestPublicFamilyAndOrderGates(t *testing.T) {
	requireFileCreation(t)
	path, _ := pubCreate(t, AddressFamilyIPv4, ValueTag{})
	w, err := OpenWriter(path, DefaultBudget())
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	tx, err := w.BeginDirect()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.AssignV6(IPv6FromHalves(0, 1), IPv6FromHalves(0, 2), 1); !isPubCode(err, ErrorWrongAddressFamily) {
		t.Fatalf("v6 assign on v4 db err = %v, want ErrorWrongAddressFamily", err)
	}
	if _, err := tx.AssignV4(IPv4(20), IPv4(10), 1); !isPubCode(err, ErrorInvalidArgument) {
		t.Fatalf("reversed v4 assign err = %v, want ErrorInvalidArgument", err)
	}
	if err := tx.Abort(); err != nil {
		t.Fatal(err)
	}
}

// TestPublicV6Workflow runs one IPv6 assignment through the full facade
// with an empty metadata payload staged in the same transaction.
func TestPublicV6Workflow(t *testing.T) {
	requireFileCreation(t)
	path, _ := pubCreate(t, AddressFamilyIPv6, ValueTag{})
	w, err := OpenWriter(path, DefaultBudget())
	if err != nil {
		t.Fatal(err)
	}
	tx, err := w.BeginDirect()
	if err != nil {
		t.Fatal(err)
	}
	from := IPv6FromHalves(0x2001_0db8, 0)
	to := IPv6FromHalves(0x2001_0db8, 0xffff)
	if changed, err := tx.AssignV6(from, to, 42); err != nil || !changed {
		t.Fatalf("v6 assign = changed %v err %v", changed, err)
	}
	if changed, err := tx.SetMetadataJSON([]byte{}); err != nil || !changed {
		t.Fatalf("empty metadata set = changed %v err %v", changed, err)
	}
	res, err := tx.Commit()
	if err != nil || res.Status != CommitCommitted {
		t.Fatalf("v6 commit = %+v err %v", res, err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	r, err := OpenImmutable(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	v, ok, err := r.LookupDirectV6(IPv6FromHalves(0x2001_0db8, 0x8000))
	if err != nil || !ok || v != 42 {
		t.Fatalf("v6 lookup = (%d, %v, %v), want (42, true, nil)", v, ok, err)
	}
	got, present, err := r.MetadataJSON()
	if err != nil || !present || len(got) != 0 {
		t.Fatalf("empty metadata readback = %q present %v err %v", got, present, err)
	}
}

// TestPublicCreateRefusesExisting verifies Create never clobbers: the
// second Create on an existing path returns ErrorNameExists and the
// original file survives byte-for-byte.
func TestPublicCreateRefusesExisting(t *testing.T) {
	requireFileCreation(t)
	tag, err := NewValueTag([]byte("keep-me"))
	if err != nil {
		t.Fatal(err)
	}
	path, _ := pubCreate(t, AddressFamilyIPv4, tag)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Create(path, AddressFamilyIPv4, ValueKindDirect, StructureKindNone, tag); !isPubCode(err, ErrorNameExists) {
		t.Fatalf("second Create err = %v, want ErrorNameExists", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("refused Create modified the existing database")
	}
}

// TestPublicClearMetadataJSON stages absence in a second transaction.
func TestPublicClearMetadataJSON(t *testing.T) {
	requireFileCreation(t)
	path, _ := pubCreate(t, AddressFamilyIPv4, ValueTag{})
	w, err := OpenWriter(path, DefaultBudget())
	if err != nil {
		t.Fatal(err)
	}
	tx, err := w.BeginDirect()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.SetMetadataJSON([]byte(`{"v":1}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	w, err = OpenWriter(path, DefaultBudget())
	if err != nil {
		t.Fatal(err)
	}
	tx, err = w.BeginDirect()
	if err != nil {
		t.Fatal(err)
	}
	if changed, err := tx.ClearMetadataJSON(); err != nil || !changed {
		t.Fatalf("clear = changed %v err %v", changed, err)
	}
	if _, err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	r, err := OpenImmutable(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if _, present, err := r.MetadataJSON(); err != nil || present {
		t.Fatalf("cleared metadata present %v err %v", present, err)
	}
}

// TestPublicDirectOpFailureAbortsDraft pins the Rust mutate
// abort_after contract on the immutable-mode direct transaction: a
// failed store op must discard the draft and spend the transaction, so
// a later Commit can never publish the partial mutation the failed op
// left behind (Rust DirectState ops route every store error through
// LiveWriter::mutate -> abort_after).
func TestPublicDirectOpFailureAbortsDraft(t *testing.T) {
	requireFileCreation(t)
	path, _ := pubCreate(t, AddressFamilyIPv4, ValueTag{})
	budget := DefaultBudget()
	budget.MaxHeapBytes = 0
	w, err := OpenWriter(path, budget)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	tx, err := w.BeginDirect()
	if err != nil {
		t.Fatal(err)
	}
	if changed, err := tx.AssignV4(IPv4(10), IPv4(20), 5); err != nil || !changed {
		t.Fatalf("assign: changed=%v err=%v", changed, err)
	}
	// The metadata compression heap charge fails inside the store (Rust
	// metadata.rs compress): the op must abort the draft and report the
	// TransactionAborted class wrapping the cause.
	if _, err := tx.SetMetadataJSON([]byte("x")); err == nil {
		t.Fatal("metadata stage succeeded under a zero heap budget")
	} else if !isPubCode(err, ErrorTransactionAborted) {
		t.Fatalf("failed op = %v, want TransactionAborted", err)
	}
	// The draft is gone: Commit and Abort report the Rust
	// NoPendingTransaction class (commit_attempt/abort have no nonce
	// gate on a draft-less core), never WrongState.
	if _, err := tx.Commit(); err == nil {
		t.Fatal("commit succeeded after the aborted op")
	} else if !isPubCode(err, ErrorNoPendingTransaction) {
		t.Fatalf("commit after aborted op = %v, want NoPendingTransaction", err)
	}
	if _, err := tx.Commit(); err == nil {
		t.Fatal("second commit succeeded on the spent transaction")
	} else if !isPubCode(err, ErrorNoPendingTransaction) {
		t.Fatalf("spent commit = %v, want NoPendingTransaction", err)
	}
	if err := tx.Abort(); err == nil {
		t.Fatal("abort succeeded after the aborted op")
	} else if !isPubCode(err, ErrorNoPendingTransaction) {
		t.Fatalf("abort after aborted op = %v, want NoPendingTransaction", err)
	}
	// The failure class is not fatal (budget exhaustion): the writer
	// stays healthy and a fresh transaction commits normally.
	tx2, err := w.BeginDirect()
	if err != nil {
		t.Fatalf("BeginDirect after aborted op: %v", err)
	}
	if changed, err := tx2.AssignV4(IPv4(30), IPv4(40), 7); err != nil || !changed {
		t.Fatalf("post-abort assign: changed=%v err=%v", changed, err)
	}
	result, err := tx2.Commit()
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != CommitCommitted {
		t.Fatalf("post-abort status = %v, want committed (cause %v)", result.Status, result.Err)
	}
	// The failed op's partial mutation never published: the immutable
	// reader (after the writer releases its exclusive lifetime lock)
	// sees only the post-abort value, and the first assign's value is
	// absent because the whole draft was discarded.
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	r, err := OpenImmutable(path)
	if err != nil {
		t.Fatalf("OpenImmutable: %v", err)
	}
	defer r.Close()
	if _, ok, err := r.LookupDirectV4(IPv4(15)); err != nil || ok {
		t.Fatalf("LookupDirectV4(15) = (ok %v, err %v), want none after the aborted draft", ok, err)
	}
	if got, ok, err := r.LookupDirectV4(IPv4(35)); err != nil || !ok || got != 7 {
		t.Fatalf("LookupDirectV4(35) = (%d, %v, %v), want (7, true)", got, ok, err)
	}
}

// TestPublicDirectOversizedMetadataKeepsDraft pins the Rust
// stage_metadata_json position on the immutable-mode direct
// transaction: an oversized payload refuses with ErrorInvalidArgument
// before the store, so the draft survives and the transaction can still
// commit its already-staged ranges.
func TestPublicDirectOversizedMetadataKeepsDraft(t *testing.T) {
	requireFileCreation(t)
	path, _ := pubCreate(t, AddressFamilyIPv4, ValueTag{})
	w, err := OpenWriter(path, DefaultBudget())
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	tx, err := w.BeginDirect()
	if err != nil {
		t.Fatal(err)
	}
	if changed, err := tx.AssignV4(IPv4(1), IPv4(9), 3); err != nil || !changed {
		t.Fatalf("assign: changed=%v err=%v", changed, err)
	}
	oversized := make([]byte, 20<<20+1)
	if _, err := tx.SetMetadataJSON(oversized); err == nil {
		t.Fatal("oversized metadata stage succeeded")
	} else if !isPubCode(err, ErrorInvalidArgument) {
		t.Fatalf("oversized metadata = %v, want InvalidArgument", err)
	}
	// The draft survived the refusal: the staged range commits.
	result, err := tx.Commit()
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != CommitCommitted {
		t.Fatalf("status = %v, want committed (cause %v)", result.Status, result.Err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	r, err := OpenImmutable(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if got, ok, err := r.LookupDirectV4(IPv4(5)); err != nil || !ok || got != 3 {
		t.Fatalf("LookupDirectV4(5) = (%d, %v, %v), want (3, true)", got, ok, err)
	}
}
