//go:build linux && amd64

// Wire primitive and shared-codec unit tests (Rust worker/wire.rs): the
// little-endian scalar codecs, the path and sized-bytes envelopes, the
// optional value codecs (u32, interval, fence), the identity and
// candidate tokens, the progress and cardinality envelopes, the error
// codec, and the inspection request/result messages. The Rust
// authority has no wire.rs unit tests, so the parity vectors pin the
// tricky encodings: u128 pairs (fence), byte ranges, optional enums,
// truncation classes, and the exact overflow details.

package worker

import (
	"errors"
	"math"
	"syscall"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/recovery"
	"github.com/firehol/iprange/v4/go/internal/validation"
)

// encodePayload runs one writer closure against a fresh control payload
// and returns the sealed control (Rust Writer::new + finish).
func encodePayload(t *testing.T, build func(w *WireWriter) error) *Control {
	t.Helper()
	c, err := CreateParent()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	w := NewWireWriter(c)
	if err := build(w); err != nil {
		t.Fatalf("encode: %v", err)
	}
	if err := w.Finish(); err != nil {
		t.Fatalf("seal: %v", err)
	}
	return c
}

// decodePayload runs one reader closure over the sealed payload and
// requires the message to be fully consumed (Rust Reader::new +
// finish).
func decodePayload(t *testing.T, c *Control, consume func(r *WireReader) error) {
	t.Helper()
	r, err := NewWireReader(c)
	if err != nil {
		t.Fatal("open reader:", err)
	}
	if err := consume(r); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if err := r.Finish(); err != nil {
		t.Fatalf("finish: %v", err)
	}
}

func TestWireScalarRoundTrips(t *testing.T) {
	c := encodePayload(t, func(w *WireWriter) error {
		if err := w.Byte(0xab); err != nil {
			return err
		}
		if err := w.Bool(false); err != nil {
			return err
		}
		if err := w.Bool(true); err != nil {
			return err
		}
		if err := w.U16(0x1234); err != nil {
			return err
		}
		if err := w.U32(0xdeadbeef); err != nil {
			return err
		}
		if err := w.I32(-123456); err != nil {
			return err
		}
		if err := w.U64(0x0123456789abcdef); err != nil {
			return err
		}
		return w.Bytes([]byte{1, 2, 3})
	})
	decodePayload(t, c, func(r *WireReader) error {
		if value, err := r.Byte(); err != nil || value != 0xab {
			return errOrValue(err, "byte")
		}
		if value, err := r.Bool(); err != nil || value {
			return errOrValue(err, "bool false")
		}
		if value, err := r.Bool(); err != nil || !value {
			return errOrValue(err, "bool true")
		}
		if value, err := r.U16(); err != nil || value != 0x1234 {
			return errOrValue(err, "u16")
		}
		if value, err := r.U32(); err != nil || value != 0xdeadbeef {
			return errOrValue(err, "u32")
		}
		if value, err := r.I32(); err != nil || value != -123456 {
			return errOrValue(err, "i32")
		}
		if value, err := r.U64(); err != nil || value != 0x0123456789abcdef {
			return errOrValue(err, "u64")
		}
		value, err := r.Array(3)
		if err != nil {
			return err
		}
		if len(value) != 3 || value[0] != 1 || value[1] != 2 || value[2] != 3 {
			return errors.New("bytes mismatch")
		}
		return nil
	})
}

// errOrValue collapses a failed scalar check into the reader error
// surface.
func errOrValue(err error, label string) error {
	if err != nil {
		return err
	}
	return errors.New(label + " mismatch")
}

func TestWireScalarLiteralVectors(t *testing.T) {
	c := encodePayload(t, func(w *WireWriter) error {
		if err := w.U16(0x1234); err != nil {
			return err
		}
		if err := w.U32(0xdeadbeef); err != nil {
			return err
		}
		if err := w.U64(0x0102030405060708); err != nil {
			return err
		}
		return nil
	})
	want := []byte{0x34, 0x12, 0xef, 0xbe, 0xad, 0xde, 0x08, 0x07, 0x06, 0x05, 0x04, 0x03, 0x02, 0x01}
	for index := range want {
		value, ok := c.PayloadByte(index)
		if !ok || value != want[index] {
			t.Fatalf("literal byte %d = %v %v, want %x", index, value, ok, want[index])
		}
	}
}

func TestWireBoolRejectsInvalid(t *testing.T) {
	c := encodePayload(t, func(w *WireWriter) error { return w.Byte(2) })
	r, err := NewWireReader(c)
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.Bool()
	wantCode(t, err, format.CodeFormatInvalid)
}

func TestWireTruncation(t *testing.T) {
	c := encodePayload(t, func(w *WireWriter) error { return w.U32(1) })
	r, err := NewWireReader(c)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.U64(); err == nil {
		t.Fatal("truncated u64 accepted")
	} else {
		wantCode(t, err, format.CodeFormatInvalid)
	}
}

func TestWireTrailingBytesRejected(t *testing.T) {
	c := encodePayload(t, func(w *WireWriter) error {
		if err := w.Byte(1); err != nil {
			return err
		}
		return w.Byte(2)
	})
	r, err := NewWireReader(c)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Byte(); err != nil {
		t.Fatal(err)
	}
	err = r.Finish()
	wantCode(t, err, format.CodeFormatInvalid)
}

func TestWirePathRoundTrip(t *testing.T) {
	paths := []*string{nil, strp("/var/tmp/iprange/source.v4"), strp("")}
	for _, path := range paths {
		c := encodePayload(t, func(w *WireWriter) error { return w.OptionalPath(path) })
		decodePayload(t, c, func(r *WireReader) error {
			value, err := r.OptionalPath()
			if err != nil {
				return err
			}
			if path == nil && value != nil {
				return errors.New("optional path present")
			}
			if path != nil && (value == nil || *value != *path) {
				return errors.New("optional path mismatch")
			}
			return nil
		})
	}
}

// strp builds a string pointer for the optional codecs.
func strp(value string) *string { return &value }

func TestWireSizedBytesRoundTrip(t *testing.T) {
	c := encodePayload(t, func(w *WireWriter) error {
		if err := w.SizedBytes([]byte("basename.v4")); err != nil {
			return err
		}
		return w.SizedBytes(nil)
	})
	decodePayload(t, c, func(r *WireReader) error {
		value, err := r.BoxedBytes()
		if err != nil {
			return err
		}
		if string(value) != "basename.v4" {
			return errors.New("sized bytes mismatch")
		}
		empty, err := r.BoxedBytes()
		if err != nil {
			return err
		}
		if len(empty) != 0 {
			return errors.New("empty sized bytes mismatch")
		}
		return nil
	})
}

func TestWireIdentityRoundTrip(t *testing.T) {
	identity := testIdentity(11, 12)
	c := encodePayload(t, func(w *WireWriter) error { return writeIdentity(w, identity) })
	decodePayload(t, c, func(r *WireReader) error {
		value, err := readIdentity(r)
		if err != nil {
			return err
		}
		if value != identity {
			return errors.New("identity mismatch")
		}
		return nil
	})
}

func TestWireCardinalityRoundTrip(t *testing.T) {
	values := []format.Cardinality129{
		format.CardinalityZero(),
		format.FullIPv6Space(),
		format.CardinalityFromUint64(42),
		format.CardinalityFromUint128(0x0102030405060708, 0x1122334455667788),
	}
	for _, value := range values {
		c := encodePayload(t, func(w *WireWriter) error { return writeCardinality(w, value) })
		decodePayload(t, c, func(r *WireReader) error {
			got, err := readCardinality(r)
			if err != nil {
				return err
			}
			if got.Compare(value) != 0 {
				return errors.New("cardinality mismatch")
			}
			return nil
		})
	}
	// A bit-128 limb above 1 is corruption (Rust
	// Cardinality129::try_new).
	c := encodePayload(t, func(w *WireWriter) error {
		if err := w.Byte(2); err != nil {
			return err
		}
		if err := w.U64(0); err != nil {
			return err
		}
		return w.U64(0)
	})
	r, err := NewWireReader(c)
	if err != nil {
		t.Fatal(err)
	}
	_, err = readCardinality(r)
	wantCode(t, err, format.CodeFormatInvalid)
}

func TestWireOptionalU32(t *testing.T) {
	for _, value := range []*uint32{nil, u32p(7)} {
		c := encodePayload(t, func(w *WireWriter) error { return writeOptionalU32(w, value) })
		decodePayload(t, c, func(r *WireReader) error {
			got, err := readOptionalU32(r)
			if err != nil {
				return err
			}
			if value == nil && got != nil {
				return errors.New("optional u32 present")
			}
			if value != nil && (got == nil || *got != *value) {
				return errors.New("optional u32 mismatch")
			}
			return nil
		})
	}
}

func u32p(value uint32) *uint32 { return &value }

func TestWireOptionalInterval(t *testing.T) {
	values := []*validation.PhysicalByteInterval{
		nil,
		{Start: 4096, EndExclusive: 8192},
		{Start: 0, EndExclusive: 0},
	}
	for _, value := range values {
		c := encodePayload(t, func(w *WireWriter) error { return writeOptionalInterval(w, value) })
		decodePayload(t, c, func(r *WireReader) error {
			got, err := readOptionalInterval(r)
			if err != nil {
				return err
			}
			if value == nil && got != nil {
				return errors.New("optional interval present")
			}
			if value != nil && (got == nil || *got != *value) {
				return errors.New("optional interval mismatch")
			}
			return nil
		})
	}
}

// TestWireOptionalFence covers the u128 pair encoding of the IPv6 arm:
// tag 2, then from.hi, from.lo, to.hi, to.lo (Rust
// ValidationAddressFence::Ipv6), with each 16-byte key in the v4
// range-record numeric order (format.U128/PutU128).
func TestWireOptionalFence(t *testing.T) {
	fromV6 := [16]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
	toV6 := [16]byte{0xff, 0xee, 0xdd, 0xcc, 0xbb, 0xaa, 0x99, 0x88, 0x77, 0x66, 0x55, 0x44, 0x33, 0x22, 0x11, 0x00}
	values := []*validation.ValidationAddressFence{
		nil,
		{IPv4: true, From: 0x0a000001, To: 0x0a0000ff},
		{IPv4: false, FromV6: fromV6, ToV6: toV6},
	}
	for _, value := range values {
		c := encodePayload(t, func(w *WireWriter) error { return writeOptionalFence(w, value) })
		decodePayload(t, c, func(r *WireReader) error {
			got, err := readOptionalFence(r, "worker validation fence is invalid")
			if err != nil {
				return err
			}
			if value == nil && got != nil {
				return errors.New("optional fence present")
			}
			if value == nil {
				return nil
			}
			if got == nil || got.IPv4 != value.IPv4 || got.From != value.From || got.To != value.To ||
				got.FromV6 != value.FromV6 || got.ToV6 != value.ToV6 {
				return errors.New("optional fence mismatch")
			}
			return nil
		})
	}
	// The v6 hi limb must be the first u64 of the wire pair.
	hi, lo := format.U128(fromV6[:])
	c := encodePayload(t, func(w *WireWriter) error {
		if err := w.Byte(2); err != nil {
			return err
		}
		if err := w.U64(hi); err != nil {
			return err
		}
		if err := w.U64(lo); err != nil {
			return err
		}
		if err := w.U64(hi); err != nil {
			return err
		}
		return w.U64(lo)
	})
	decodePayload(t, c, func(r *WireReader) error {
		got, err := readOptionalFence(r, "worker validation fence is invalid")
		if err != nil {
			return err
		}
		if got == nil || got.FromV6 != fromV6 {
			return errors.New("v6 fence limbs out of order")
		}
		return nil
	})
	// An unknown tag is corruption with the caller's detail.
	c2 := encodePayload(t, func(w *WireWriter) error { return w.Byte(9) })
	r, err := NewWireReader(c2)
	if err != nil {
		t.Fatal(err)
	}
	_, err = readOptionalFence(r, "worker validation fence is invalid")
	wantCode(t, err, format.CodeFormatInvalid)
}

func TestWireRecoveryCandidateRoundTrip(t *testing.T) {
	candidate := &recovery.RecoveryCandidate{
		Label:          recovery.CandidateNewest,
		MetaPage:       1,
		SourceIdentity: testIdentity(3, 4),
		DatabaseID:     [16]byte{0x11},
		TransactionID:  9,
		CommitNonce:    [16]byte{0x22},
	}
	c := encodePayload(t, func(w *WireWriter) error { return writeRecoveryCandidate(w, candidate) })
	decodePayload(t, c, func(r *WireReader) error {
		got, err := readRecoveryCandidate(r)
		if err != nil {
			return err
		}
		if *got != *candidate {
			return errors.New("candidate mismatch")
		}
		return nil
	})
	for _, label := range []recovery.RecoveryCandidateLabel{
		recovery.CandidatePrevious, recovery.CandidateUnorderedMeta0, recovery.CandidateUnorderedMeta1,
	} {
		candidate.Label = label
		c := encodePayload(t, func(w *WireWriter) error { return writeRecoveryCandidate(w, candidate) })
		decodePayload(t, c, func(r *WireReader) error {
			got, err := readRecoveryCandidate(r)
			if err != nil {
				return err
			}
			if got.Label != label {
				return errors.New("candidate label mismatch")
			}
			return nil
		})
	}
	// An invalid label tag is corruption.
	c2 := encodePayload(t, func(w *WireWriter) error { return w.Byte(0) })
	r, err := NewWireReader(c2)
	if err != nil {
		t.Fatal(err)
	}
	_, err = readRecoveryCandidate(r)
	wantCode(t, err, format.CodeFormatInvalid)
}

func TestWireErrorRoundTrip(t *testing.T) {
	// A plain format.Error keeps its class and loses nothing else (the
	// wire carries code plus errno only, exactly like Rust).
	c := encodePayload(t, func(w *WireWriter) error {
		return encodeWorkerError(w, &format.Error{Code: format.CodeConflict, Detail: "exact detail"})
	})
	decodePayload(t, c, func(r *WireReader) error {
		value, err := readWorkerError(r)
		if err != nil {
			return err
		}
		var wire *WireError
		if !errors.As(value, &wire) || wire.Code != format.CodeConflict {
			return errors.New("conflict round trip mismatch")
		}
		return nil
	})
	// An errno chain encodes as Io plus the raw errno and decodes back
	// to the raw errno (Rust Error::Io arm).
	errno := syscall.ENOENT
	wrapped := &osPathError{Err: errno}
	c2 := encodePayload(t, func(w *WireWriter) error { return encodeWorkerError(w, wrapped) })
	decodePayload(t, c2, func(r *WireReader) error {
		value, err := readWorkerError(r)
		if err != nil {
			return err
		}
		var raw syscall.Errno
		if !errors.As(value, &raw) || raw != errno {
			return errors.New("errno round trip mismatch")
		}
		return nil
	})
	// The specific constant variants decode as plain format.Errors.
	c3 := encodePayload(t, func(w *WireWriter) error {
		return encodeWorkerError(w, &format.Error{Code: format.CodeNameExists, Detail: "unit variant"})
	})
	decodePayload(t, c3, func(r *WireReader) error {
		value, err := readWorkerError(r)
		if err != nil {
			return err
		}
		var formatted *format.Error
		if !errors.As(value, &formatted) || formatted.Code != format.CodeNameExists {
			return errors.New("unit variant round trip mismatch")
		}
		return nil
	})
	// An unregistered wire code is corruption.
	c4 := encodePayload(t, func(w *WireWriter) error { return w.U32(70000) })
	r, err := NewWireReader(c4)
	if err != nil {
		t.Fatal(err)
	}
	_, err = readWorkerError(r)
	wantCode(t, err, format.CodeFormatInvalid)
}

// osPathError wraps a raw errno through the standard unwrap chain.
type osPathError struct {
	Err syscall.Errno
}

func (e *osPathError) Error() string { return "worker io: " + e.Err.Error() }
func (e *osPathError) Unwrap() error { return e.Err }

func TestWriteReadWorkerErrorEnvelope(t *testing.T) {
	c, err := CreateParent()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	cause := &format.Error{Code: format.CodeCancelled, Detail: "parent cancelled"}
	if err := WriteWorkerError(c, cause); err != nil {
		t.Fatal("write worker error:", err)
	}
	value, err := ReadWorkerError(c)
	if err != nil {
		t.Fatal("read worker error:", err)
	}
	// CodeCancelled is one of the specific constant variants of the
	// Rust read_error arm: it decodes as a plain format.Error.
	var formatted *format.Error
	if !errors.As(value, &formatted) || formatted.Code != format.CodeCancelled {
		t.Fatalf("worker error = %v", value)
	}
}

// TestWireProgressRoundTrip pins the fixed array order of the progress
// envelope: the reason counts run before the object counts (Rust
// wire::progress over the reason/object chains).
func TestWireProgressRoundTrip(t *testing.T) {
	progress := ProgressWire{
		CheckedUniquePages:           11,
		FindingCount:                 2,
		UntraversableSubgraphs:       1,
		BoundedPossibleSpanAddresses: format.CardinalityFromUint128(0, 29),
		HasUnboundedUnknown:          false,
	}
	progress.ReasonCounts[validation.ReasonPageCrcMismatch] = 2
	progress.ObjectCounts[validation.ObjectRangeTree] = 11
	c := encodePayload(t, func(w *WireWriter) error { return writeProgress(w, &progress) })
	decodePayload(t, c, func(r *WireReader) error {
		got, err := readProgress(r)
		if err != nil {
			return err
		}
		if got != progress {
			return errors.New("progress mismatch")
		}
		return nil
	})
}

func TestProgressWireOfDomain(t *testing.T) {
	progress := validation.NewProgress()
	progress.CheckedUniquePages = 5
	progress.BoundedPossibleSpanAddresses = format.CardinalityFromUint64(9)
	if err := validation.CountFinding(&progress, validation.ReasonPageCrcMismatch); err != nil {
		t.Fatal(err)
	}
	if err := validation.CountFinding(&progress, validation.ReasonPageCrcMismatch); err != nil {
		t.Fatal(err)
	}
	if err := validation.MarkUntraversable(&progress, true); err != nil {
		t.Fatal(err)
	}
	wire := ProgressWireOf(&progress)
	if wire.CheckedUniquePages != 5 || wire.FindingCount != 2 || wire.UntraversableSubgraphs != 1 {
		t.Fatalf("wire progress = %+v", wire)
	}
	if wire.ReasonCounts[validation.ReasonPageCrcMismatch] != 2 {
		t.Fatalf("reason counts = %+v", wire.ReasonCounts)
	}
	if !wire.HasUnboundedUnknown {
		t.Fatal("unbounded flag lost")
	}
}

func TestWireBudgetRoundTrip(t *testing.T) {
	budget := &validation.ValidationBudget{
		MaxHeapBytes:     1 << 20,
		MaxOpenFiles:     4,
		MaxScratchBytes:  1 << 18,
		MaxScratchFiles:  2,
		ScratchDirectory: "/tmp/scratch",
	}
	c := encodePayload(t, func(w *WireWriter) error { return writeValidationBudget(w, budget) })
	decodePayload(t, c, func(r *WireReader) error {
		got, err := readValidationBudget(r)
		if err != nil {
			return err
		}
		if got != *budget {
			return errors.New("budget mismatch")
		}
		return nil
	})
	// The absent scratch directory encodes as the false optional arm.
	budget.ScratchDirectory = ""
	c = encodePayload(t, func(w *WireWriter) error { return writeValidationBudget(w, budget) })
	decodePayload(t, c, func(r *WireReader) error {
		got, err := readValidationBudget(r)
		if err != nil {
			return err
		}
		if got.ScratchDirectory != "" {
			return errors.New("absent scratch directory mismatch")
		}
		return nil
	})
}

func TestWireU32ListHeapAccounting(t *testing.T) {
	c := encodePayload(t, func(w *WireWriter) error {
		return writeU32List(w, []uint32{3, 1, 4}, errors.New("unused overflow"))
	})
	r, err := NewWireReader(c)
	if err != nil {
		t.Fatal(err)
	}
	heap := uint64(12)
	values, err := readU32List(r, &heap)
	if err != nil {
		t.Fatal(err)
	}
	if len(values) != 3 || values[0] != 3 || values[1] != 1 || values[2] != 4 {
		t.Fatalf("values = %v", values)
	}
	if heap != 0 {
		t.Fatalf("heap after list = %d, want 0", heap)
	}
	// A list that exceeds the remaining heap is the budget class.
	c2 := encodePayload(t, func(w *WireWriter) error {
		return writeU32List(w, []uint32{1, 2}, errors.New("unused overflow"))
	})
	r2, err := NewWireReader(c2)
	if err != nil {
		t.Fatal(err)
	}
	small := uint64(4)
	_, err = readU32List(r2, &small)
	wantCode(t, err, format.CodeInsufficientResourceBudget)
}

func TestWriteInspectionRequestRoundTrip(t *testing.T) {
	path := "/var/tmp/source.v4"
	pages := []uint32{3, 9}
	for _, mode := range []recovery.RecoveryInspectionMode{
		recovery.RecoveryInspectionImmutable, recovery.RecoveryInspectionLive, recovery.RecoveryInspectionOffline,
	} {
		c, err := CreateParent()
		if err != nil {
			t.Fatal(err)
		}
		budget := &validation.ValidationBudget{MaxHeapBytes: 1 << 20, MaxOpenFiles: 2}
		if err := WriteInspectionRequest(c, path, mode, budget, pages); err != nil {
			t.Fatalf("write inspection %v: %v", mode, err)
		}
		request, err := ReadInspectionRequest(c)
		if err != nil {
			t.Fatalf("read inspection %v: %v", mode, err)
		}
		if request.Path != path || request.Mode != mode || len(request.UnreadablePages) != 2 ||
			request.UnreadablePages[0] != 3 || request.UnreadablePages[1] != 9 {
			t.Fatalf("request = %+v", request)
		}
		if request.Budget.MaxHeapBytes != 1<<20-8 {
			t.Fatalf("heap after list = %d", request.Budget.MaxHeapBytes)
		}
		c.Close()
	}
}

func TestWriteInspectionResultRoundTrip(t *testing.T) {
	inspection := &InspectionWire{
		SourceIdentity: testIdentity(3, 4),
		Progress: ProgressWire{
			CheckedUniquePages: 7,
			FindingCount:       1,
		},
	}
	inspection.Candidates[0] = &recovery.RecoveryCandidate{
		Label:          recovery.CandidateNewest,
		MetaPage:       1,
		SourceIdentity: testIdentity(3, 4),
		DatabaseID:     [16]byte{1},
		TransactionID:  2,
		CommitNonce:    [16]byte{3},
	}
	c, err := CreateParent()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := WriteInspectionResult(c, inspection, nil); err != nil {
		t.Fatal("write result:", err)
	}
	got, err := ReadInspectionResult(c)
	if err != nil {
		t.Fatal("read result:", err)
	}
	if got.SourceIdentity != inspection.SourceIdentity || got.CandidateCount() != 1 ||
		*got.Candidates[0] != *inspection.Candidates[0] || got.Progress != inspection.Progress {
		t.Fatalf("inspection = %+v", got)
	}
	// More than two candidates is corruption.
	c2, err := CreateParent()
	if err != nil {
		t.Fatal(err)
	}
	defer c2.Close()
	w := NewWireWriter(c2)
	if err := w.Byte(0); err != nil {
		t.Fatal(err)
	}
	if err := writeIdentity(w, testIdentity(1, 1)); err != nil {
		t.Fatal(err)
	}
	if err := writeProgress(w, &ProgressWire{}); err != nil {
		t.Fatal(err)
	}
	if err := w.Byte(3); err != nil {
		t.Fatal(err)
	}
	if err := w.Finish(); err != nil {
		t.Fatal(err)
	}
	_, err = ReadInspectionResult(c2)
	wantCode(t, err, format.CodeFormatInvalid)
	// The error arm carries an encoded worker error.
	c3, err := CreateParent()
	if err != nil {
		t.Fatal(err)
	}
	defer c3.Close()
	if err := WriteInspectionResult(c3, nil, &format.Error{Code: format.CodeCleanupConflict, Detail: "exact"}); err != nil {
		t.Fatal("write error result:", err)
	}
	_, err = ReadInspectionResult(c3)
	var wire *WireError
	if !errors.As(err, &wire) || wire.Code != format.CodeCleanupConflict {
		t.Fatalf("inspection error = %v", err)
	}
}

// Guard the implicit usize cap of the Go int used for offsets and
// lengths on 32-bit hosts; the worker proof targets amd64.
func TestWireIntCapacity(t *testing.T) {
	if payloadCapacity > math.MaxInt {
		t.Fatal("payload capacity exceeds the platform int")
	}
}
