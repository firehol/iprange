//go:build linux || darwin || freebsd || windows

// Worker wire codecs (Rust worker/wire.rs): the little-endian
// Writer/Reader primitives over the two mapped payload buffers (the
// session payload at offPayload and the callback payload at
// offCallbackPayload) and the shared per-mode envelopes (identity,
// progress, cardinality, optional values, recovery candidates, errors,
// inspection requests and results, budget and unreadable-page lists).
// The field order and every overflow/truncation class mirror the Rust
// authority exactly; the error detail strings are verbatim Rust. The
// worker and its client both live on linux/amd64 (the fault isolation
// proof), so only the unix path codec exists here.

package worker

import (
	"errors"
	"math"
	"strconv"
	"syscall"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/publication"
	"github.com/firehol/iprange/v4/go/internal/recovery"
	"github.com/firehol/iprange/v4/go/internal/validation"
)

// wireBuffer selects the mapped payload buffer of one wire session
// (Rust Writer/Reader Buffer).
type wireBuffer uint8

const (
	bufferPayload wireBuffer = iota
	bufferCallbackCheckpoint
)

// WireWriter writes one little-endian message into a mapped control
// payload buffer (Rust worker/wire.rs Writer). Every write is bounds
// checked against the buffer capacity by the control owner; finish
// seals the buffer length.
type WireWriter struct {
	control *Control
	buffer  wireBuffer
	at      int
}

// NewWireWriter starts a session-payload writer (Rust Writer::new).
func NewWireWriter(control *Control) *WireWriter {
	return &WireWriter{control: control, buffer: bufferPayload}
}

// NewWireCallbackWriter starts a callback-payload writer (Rust
// Writer::new_callback_checkpoint).
func NewWireCallbackWriter(control *Control) *WireWriter {
	return &WireWriter{control: control, buffer: bufferCallbackCheckpoint}
}

// Finish seals the written length (Rust Writer::finish).
func (w *WireWriter) Finish() error {
	switch w.buffer {
	case bufferPayload:
		return w.control.SetPayloadLen(w.at)
	case bufferCallbackCheckpoint:
		return w.control.SetCallbackPayloadLen(w.at)
	}
	panic("unreachable wire buffer")
}

// Byte writes one byte (Rust Writer::byte).
func (w *WireWriter) Byte(value byte) error {
	return w.Bytes([]byte{value})
}

// Bool writes one boolean as 0/1 (Rust Writer::bool).
func (w *WireWriter) Bool(value bool) error {
	if value {
		return w.Byte(1)
	}
	return w.Byte(0)
}

// U16 writes one little-endian u16 (Rust Writer::u16).
func (w *WireWriter) U16(value uint16) error { return w.Bytes([]byte{byte(value), byte(value >> 8)}) }

// U32 writes one little-endian u32 (Rust Writer::u32).
func (w *WireWriter) U32(value uint32) error {
	return w.Bytes([]byte{byte(value), byte(value >> 8), byte(value >> 16), byte(value >> 24)})
}

// I32 writes one little-endian i32 (Rust Writer::i32).
func (w *WireWriter) I32(value int32) error { return w.U32(uint32(value)) }

// U64 writes one little-endian u64 (Rust Writer::u64).
func (w *WireWriter) U64(value uint64) error {
	return w.Bytes([]byte{
		byte(value), byte(value >> 8), byte(value >> 16), byte(value >> 24),
		byte(value >> 32), byte(value >> 40), byte(value >> 48), byte(value >> 56),
	})
}

// Bytes writes the raw bytes and advances the offset (Rust
// Writer::bytes: the control rejects any write past the buffer
// capacity, and the offset advance is overflow checked).
func (w *WireWriter) Bytes(value []byte) error {
	switch w.buffer {
	case bufferPayload:
		if err := w.control.WritePayload(w.at, value); err != nil {
			return err
		}
	case bufferCallbackCheckpoint:
		if err := w.control.WriteCallbackPayload(w.at, value); err != nil {
			return err
		}
	}
	if len(value) > math.MaxInt-w.at {
		return &format.Error{Code: format.CodeArithmeticOverflow, Detail: "worker payload offset"}
	}
	w.at += len(value)
	return nil
}

// Path writes one unix path as a u32 length followed by its raw bytes
// (Rust os_string on unix; the worker boundary never runs on Windows,
// so the wide-char arm is intentionally absent).
func (w *WireWriter) Path(value string) error {
	if len(value) > math.MaxUint32 {
		return &format.Error{Code: format.CodeInvalidArgument, Detail: "path is too long"}
	}
	if err := w.U32(uint32(len(value))); err != nil {
		return err
	}
	return w.Bytes([]byte(value))
}

// OptionalPath writes one optional path (Rust Writer::optional_path: a
// present bit followed by the path).
func (w *WireWriter) OptionalPath(value *string) error {
	if value == nil {
		return w.Bool(false)
	}
	if err := w.Bool(true); err != nil {
		return err
	}
	return w.Path(*value)
}

// SizedBytes writes one length-prefixed byte string (Rust
// Writer::sized_bytes).
func (w *WireWriter) SizedBytes(value []byte) error {
	if len(value) > math.MaxUint32 {
		return &format.Error{Code: format.CodeInsufficientResourceBudget, Detail: "worker byte string"}
	}
	if err := w.U32(uint32(len(value))); err != nil {
		return err
	}
	return w.Bytes(value)
}

// WireReader reads one little-endian message from a sealed mapped
// payload buffer (Rust worker/wire.rs Reader). The constructor
// snapshots the sealed length; every read stays inside it.
type WireReader struct {
	region []byte
	at     int
	length int
}

// NewWireReader starts a session-payload reader (Rust Reader::new).
func NewWireReader(control *Control) (*WireReader, error) {
	length, err := control.PayloadLen()
	if err != nil {
		return nil, err
	}
	return &WireReader{
		region: control.data[offPayload : offPayload+payloadCapacity],
		length: length,
	}, nil
}

// NewWireCallbackReader starts a callback-payload reader (Rust
// Reader::new_callback_checkpoint).
func NewWireCallbackReader(control *Control) (*WireReader, error) {
	length, err := control.CallbackPayloadLen()
	if err != nil {
		return nil, err
	}
	return &WireReader{
		region: control.data[offCallbackPayload : offCallbackPayload+callbackPayCapacity],
		length: length,
	}, nil
}

// Finish requires the whole sealed message to have been consumed (Rust
// Reader::finish: trailing bytes are corruption).
func (r *WireReader) Finish() error {
	if r.at == r.length {
		return nil
	}
	return &format.Error{Code: format.CodeFormatInvalid, Detail: "worker payload has trailing bytes"}
}

// Byte reads one byte (Rust Reader::byte).
func (r *WireReader) Byte() (byte, error) {
	if r.at >= r.length {
		return 0, &format.Error{Code: format.CodeFormatInvalid, Detail: "worker payload is truncated"}
	}
	value := r.region[r.at]
	r.at++
	return value, nil
}

// Bool reads one boolean (Rust Reader::bool: any byte other than 0/1 is
// corruption).
func (r *WireReader) Bool() (bool, error) {
	switch value, err := r.Byte(); value {
	case 0:
		return false, err
	case 1:
		return true, err
	default:
		if err != nil {
			return false, err
		}
		return false, &format.Error{Code: format.CodeFormatInvalid, Detail: "worker boolean is invalid"}
	}
}

// U16 reads one little-endian u16 (Rust Reader::u16).
func (r *WireReader) U16() (uint16, error) {
	value, err := r.Array(2)
	if err != nil {
		return 0, err
	}
	return format.U16(value), nil
}

// U32 reads one little-endian u32 (Rust Reader::u32).
func (r *WireReader) U32() (uint32, error) {
	value, err := r.Array(4)
	if err != nil {
		return 0, err
	}
	return format.U32(value), nil
}

// I32 reads one little-endian i32 (Rust Reader::i32).
func (r *WireReader) I32() (int32, error) {
	value, err := r.U32()
	if err != nil {
		return 0, err
	}
	return int32(value), nil
}

// U64 reads one little-endian u64 (Rust Reader::u64).
func (r *WireReader) U64() (uint64, error) {
	value, err := r.Array(8)
	if err != nil {
		return 0, err
	}
	return format.U64(value), nil
}

// Array reads n raw bytes as a fresh copy (Rust
// Reader::array<const N>; the bounds proof runs before the copy, so a
// truncated message is rejected whole).
func (r *WireReader) Array(n int) ([]byte, error) {
	if n < 0 || r.at > r.length || n > r.length-r.at {
		return nil, &format.Error{Code: format.CodeFormatInvalid, Detail: "worker payload is truncated"}
	}
	value := make([]byte, n)
	copy(value, r.region[r.at:r.at+n])
	r.at += n
	return value, nil
}

// Array16 reads one fixed 16-byte array (Rust Reader::array).
func (r *WireReader) Array16() ([16]byte, error) {
	value, err := r.Array(16)
	if err != nil {
		return [16]byte{}, err
	}
	return [16]byte(value), nil
}

// Array32 reads one fixed 32-byte array (Rust Reader::array).
func (r *WireReader) Array32() ([32]byte, error) {
	value, err := r.Array(32)
	if err != nil {
		return [32]byte{}, err
	}
	return [32]byte(value), nil
}

// Array64 reads one fixed 64-byte array (Rust Reader::array).
func (r *WireReader) Array64() ([64]byte, error) {
	value, err := r.Array(64)
	if err != nil {
		return [64]byte{}, err
	}
	return [64]byte(value), nil
}

// Path reads one unix path (Rust read_os_string on unix: a u32 length
// followed by the raw bytes).
func (r *WireReader) Path() (string, error) {
	length, err := r.U32()
	if err != nil {
		return "", err
	}
	if int(length) > r.length-r.at {
		return "", &format.Error{Code: format.CodeFormatInvalid, Detail: "worker payload is truncated"}
	}
	value := make([]byte, int(length))
	copy(value, r.region[r.at:r.at+int(length)])
	r.at += int(length)
	return string(value), nil
}

// OptionalPath reads one optional path (Rust Reader::optional_path).
func (r *WireReader) OptionalPath() (*string, error) {
	present, err := r.Bool()
	if err != nil {
		return nil, err
	}
	if !present {
		return nil, nil
	}
	value, err := r.Path()
	if err != nil {
		return nil, err
	}
	return &value, nil
}

// BoxedBytes reads one length-prefixed byte string (Rust
// Reader::boxed_bytes: the length must fit the remaining message).
func (r *WireReader) BoxedBytes() ([]byte, error) {
	length, err := r.U32()
	if err != nil {
		return nil, err
	}
	if int(length) > r.length-r.at {
		return nil, &format.Error{Code: format.CodeFormatInvalid, Detail: "worker byte string is truncated"}
	}
	value := make([]byte, int(length))
	copy(value, r.region[r.at:r.at+int(length)])
	r.at += int(length)
	return value, nil
}

// writeValidationBudget encodes one validation budget (Rust
// wire::validation_budget): heap bytes, open files, scratch bytes and
// files, and the optional scratch directory.
func writeValidationBudget(w *WireWriter, value *validation.ValidationBudget) error {
	if err := w.U64(value.MaxHeapBytes); err != nil {
		return err
	}
	if err := w.U32(value.MaxOpenFiles); err != nil {
		return err
	}
	if err := w.U64(value.MaxScratchBytes); err != nil {
		return err
	}
	if err := w.U32(value.MaxScratchFiles); err != nil {
		return err
	}
	var directory *string
	if value.ScratchDirectory != "" {
		directory = &value.ScratchDirectory
	}
	return w.OptionalPath(directory)
}

// readValidationBudget decodes one validation budget (Rust
// wire::read_validation_budget).
func readValidationBudget(r *WireReader) (validation.ValidationBudget, error) {
	budget := validation.ValidationBudget{}
	var err error
	if budget.MaxHeapBytes, err = r.U64(); err != nil {
		return budget, err
	}
	if budget.MaxOpenFiles, err = r.U32(); err != nil {
		return budget, err
	}
	if budget.MaxScratchBytes, err = r.U64(); err != nil {
		return budget, err
	}
	if budget.MaxScratchFiles, err = r.U32(); err != nil {
		return budget, err
	}
	var directory *string
	if directory, err = r.OptionalPath(); err != nil {
		return budget, err
	}
	if directory != nil {
		budget.ScratchDirectory = *directory
	}
	return budget, nil
}

// writeU32List encodes a length-prefixed u32 list (Rust wire::u32_list;
// overflow carries the caller-selected error class).
func writeU32List(w *WireWriter, values []uint32, overflow error) error {
	if len(values) > math.MaxUint32 {
		return overflow
	}
	if err := w.U32(uint32(len(values))); err != nil {
		return err
	}
	for _, value := range values {
		if err := w.U32(value); err != nil {
			return err
		}
	}
	return nil
}

// readU32List decodes a length-prefixed u32 list and charges its exact
// byte footprint against the caller's heap budget (Rust
// wire::read_u32_list: the checked charge underflows to the budget
// class).
func readU32List(r *WireReader, heapBytes *uint64) ([]uint32, error) {
	count, err := r.U32()
	if err != nil {
		return nil, err
	}
	bytes := uint64(count) * 4
	if count > 0 && bytes/4 != uint64(count) {
		return nil, &format.Error{Code: format.CodeArithmeticOverflow, Detail: "unreadable source-page list"}
	}
	if bytes > *heapBytes {
		return nil, &format.Error{Code: format.CodeInsufficientResourceBudget, Detail: "unreadable source-page list"}
	}
	*heapBytes -= bytes
	// The preallocation is capped by the sealed message length: the
	// count field alone must never force a ~16 GiB allocation for a
	// corrupt control (Rust try_reserve_exact folds the allocator
	// failure into BudgetExceeded; Go cannot fail make(), so the cap
	// is the reader's own byte bound and an over-count fails on
	// truncation inside the loop with the same corrupt class).
	remaining := r.length - r.at
	capacity := int(count)
	if capacity < 0 || uint64(capacity)*4 > uint64(remaining) {
		capacity = remaining / 4
	}
	if capacity < 0 {
		capacity = 0
	}
	values := make([]uint32, 0, capacity)
	for i := uint32(0); i < count; i++ {
		value, err := r.U32()
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, nil
}

// writeIdentity encodes one portable identity (Rust wire::identity:
// one u16 kind followed by the fixed 32-byte payload).
func writeIdentity(w *WireWriter, value publication.LocalFileIdentity) error {
	if err := w.U16(value.Kind); err != nil {
		return err
	}
	return w.Bytes(value.Bytes[:])
}

// readIdentity decodes one portable identity (Rust wire::read_identity).
func readIdentity(r *WireReader) (publication.LocalFileIdentity, error) {
	kind, err := r.U16()
	if err != nil {
		return publication.LocalFileIdentity{}, err
	}
	bytes, err := r.Array32()
	if err != nil {
		return publication.LocalFileIdentity{}, err
	}
	return publication.LocalFileIdentity{Kind: kind, Bytes: bytes}, nil
}

// ProgressWire is the wire form of the validation progress counters
// (Rust validation::ValidationProgress; the Go validation package keeps
// its per-reason and per-object count arrays private, so the worker
// boundary owns the full arrays and converts through the exported
// accessors). ProgressWireOf reads a domain progress; the decoded wire
// progress serves the client slices directly.
type ProgressWire struct {
	CheckedUniquePages           uint64
	FindingCount                 uint64
	UntraversableSubgraphs       uint64
	BoundedPossibleSpanAddresses format.Cardinality129
	HasUnboundedUnknown          bool
	ReasonCounts                 [validation.ValidationReasonCount]uint64
	ObjectCounts                 [validation.ValidationObjectCount]uint64
}

// ProgressWireOf converts one domain progress to its wire form (Rust
// wire::progress parity: the per-reason and per-object counters are the
// exported accessors of the Go validation package).
func ProgressWireOf(value *validation.ValidationProgress) ProgressWire {
	out := ProgressWire{
		CheckedUniquePages:           value.CheckedUniquePages,
		FindingCount:                 value.FindingCount,
		UntraversableSubgraphs:       value.UntraversableSubgraphs,
		BoundedPossibleSpanAddresses: value.BoundedPossibleSpanAddresses,
		HasUnboundedUnknown:          value.HasUnboundedUnknown,
	}
	for reason := validation.ValidationReason(0); reason < validation.ValidationReasonCount; reason++ {
		out.ReasonCounts[reason] = value.FindingsFor(reason)
	}
	for object := validation.ValidationObject(0); object < validation.ValidationObjectCount; object++ {
		out.ObjectCounts[object] = value.ExaminedFor(object)
	}
	return out
}

// writeProgress encodes one progress envelope (Rust wire::progress: the
// five scalars, the bounded cardinality, and the fixed reason and
// object count arrays in declaration order).
func writeProgress(w *WireWriter, value *ProgressWire) error {
	if err := w.U64(value.CheckedUniquePages); err != nil {
		return err
	}
	if err := w.U64(value.FindingCount); err != nil {
		return err
	}
	if err := w.U64(value.UntraversableSubgraphs); err != nil {
		return err
	}
	if err := writeCardinality(w, value.BoundedPossibleSpanAddresses); err != nil {
		return err
	}
	if err := w.Bool(value.HasUnboundedUnknown); err != nil {
		return err
	}
	for _, count := range value.ReasonCounts {
		if err := w.U64(count); err != nil {
			return err
		}
	}
	for _, count := range value.ObjectCounts {
		if err := w.U64(count); err != nil {
			return err
		}
	}
	return nil
}

// readProgress decodes one progress envelope (Rust wire::read_progress).
func readProgress(r *WireReader) (ProgressWire, error) {
	value := ProgressWire{}
	var err error
	if value.CheckedUniquePages, err = r.U64(); err != nil {
		return value, err
	}
	if value.FindingCount, err = r.U64(); err != nil {
		return value, err
	}
	if value.UntraversableSubgraphs, err = r.U64(); err != nil {
		return value, err
	}
	if value.BoundedPossibleSpanAddresses, err = readCardinality(r); err != nil {
		return value, err
	}
	if value.HasUnboundedUnknown, err = r.Bool(); err != nil {
		return value, err
	}
	for index := range value.ReasonCounts {
		if value.ReasonCounts[index], err = r.U64(); err != nil {
			return value, err
		}
	}
	for index := range value.ObjectCounts {
		if value.ObjectCounts[index], err = r.U64(); err != nil {
			return value, err
		}
	}
	return value, nil
}

// writeCardinality encodes one exact 129-bit cardinality (Rust
// wire::cardinality: the bit-128 limb byte, then the hi and lo limbs).
func writeCardinality(w *WireWriter, value format.Cardinality129) error {
	if err := w.Byte(value.Bit128()); err != nil {
		return err
	}
	if err := w.U64(value.Hi()); err != nil {
		return err
	}
	return w.U64(value.Lo())
}

// readCardinality decodes one exact 129-bit cardinality (Rust
// wire::read_cardinality: a limb above 1 is corruption).
func readCardinality(r *WireReader) (format.Cardinality129, error) {
	bit128, err := r.Byte()
	if err != nil {
		return format.Cardinality129{}, err
	}
	hi, err := r.U64()
	if err != nil {
		return format.Cardinality129{}, err
	}
	lo, err := r.U64()
	if err != nil {
		return format.Cardinality129{}, err
	}
	value, err := format.NewCardinality129(bit128, hi, lo)
	if err != nil {
		return format.Cardinality129{}, &format.Error{Code: format.CodeFormatInvalid, Detail: "worker cardinality is invalid"}
	}
	return value, nil
}

// writeOptionalU32 encodes one optional u32 (Rust wire::optional_u32).
func writeOptionalU32(w *WireWriter, value *uint32) error {
	if value == nil {
		return w.Bool(false)
	}
	if err := w.Bool(true); err != nil {
		return err
	}
	return w.U32(*value)
}

// readOptionalU32 decodes one optional u32 (Rust wire::read_optional_u32).
func readOptionalU32(r *WireReader) (*uint32, error) {
	present, err := r.Bool()
	if err != nil {
		return nil, err
	}
	if !present {
		return nil, nil
	}
	value, err := r.U32()
	if err != nil {
		return nil, err
	}
	return &value, nil
}

// writeOptionalInterval encodes one optional physical byte interval
// (Rust wire::optional_interval).
func writeOptionalInterval(w *WireWriter, value *validation.PhysicalByteInterval) error {
	if value == nil {
		return w.Bool(false)
	}
	if err := w.Bool(true); err != nil {
		return err
	}
	if err := w.U64(value.Start); err != nil {
		return err
	}
	return w.U64(value.EndExclusive)
}

// readOptionalInterval decodes one optional physical byte interval
// (Rust wire::read_optional_interval).
func readOptionalInterval(r *WireReader) (*validation.PhysicalByteInterval, error) {
	present, err := r.Bool()
	if err != nil {
		return nil, err
	}
	if !present {
		return nil, nil
	}
	start, err := r.U64()
	if err != nil {
		return nil, err
	}
	end, err := r.U64()
	if err != nil {
		return nil, err
	}
	return &validation.PhysicalByteInterval{Start: start, EndExclusive: end}, nil
}

// fenceV6HiLo splits one 16-byte v6 key into its numeric hi/lo limbs in
// the v4 range-record key order (format.U128: the low limb lives at
// bytes 0..8, the high limb at bytes 8..16).
func fenceV6HiLo(key [16]byte) (hi, lo uint64) {
	return format.U128(key[:])
}

// fenceV6Key rebuilds one 16-byte v6 key from its numeric limbs
// (format.PutU128: the low limb at bytes 0..8, the high limb at bytes
// 8..16).
func fenceV6Key(hi, lo uint64) [16]byte {
	var key [16]byte
	format.PutU128(key[:], hi, lo)
	return key
}

// writeOptionalFence encodes one optional logical address fence (Rust
// wire::optional_fence): tag 0 absent, tag 1 an IPv4 pair, tag 2 an
// IPv6 pair with each key as hi then lo.
func writeOptionalFence(w *WireWriter, value *validation.ValidationAddressFence) error {
	if value == nil {
		return w.Byte(0)
	}
	if value.IPv4 {
		if err := w.Byte(1); err != nil {
			return err
		}
		if err := w.U32(uint32(value.From)); err != nil {
			return err
		}
		return w.U32(uint32(value.To))
	}
	if err := w.Byte(2); err != nil {
		return err
	}
	fromHi, fromLo := fenceV6HiLo(value.FromV6)
	if err := w.U64(fromHi); err != nil {
		return err
	}
	if err := w.U64(fromLo); err != nil {
		return err
	}
	toHi, toLo := fenceV6HiLo(value.ToV6)
	if err := w.U64(toHi); err != nil {
		return err
	}
	return w.U64(toLo)
}

// readOptionalFence decodes one optional logical address fence (Rust
// wire::read_optional_fence; invalid carries the caller's corrupt
// detail).
func readOptionalFence(r *WireReader, invalid string) (*validation.ValidationAddressFence, error) {
	tag, err := r.Byte()
	if err != nil {
		return nil, err
	}
	switch tag {
	case 0:
		return nil, nil
	case 1:
		from, err := r.U32()
		if err != nil {
			return nil, err
		}
		to, err := r.U32()
		if err != nil {
			return nil, err
		}
		return &validation.ValidationAddressFence{IPv4: true, From: uint64(from), To: uint64(to)}, nil
	case 2:
		fromHi, err := r.U64()
		if err != nil {
			return nil, err
		}
		fromLo, err := r.U64()
		if err != nil {
			return nil, err
		}
		toHi, err := r.U64()
		if err != nil {
			return nil, err
		}
		toLo, err := r.U64()
		if err != nil {
			return nil, err
		}
		return &validation.ValidationAddressFence{
			IPv4:   false,
			FromV6: fenceV6Key(fromHi, fromLo),
			ToV6:   fenceV6Key(toHi, toLo),
		}, nil
	default:
		return nil, &format.Error{Code: format.CodeFormatInvalid, Detail: invalid}
	}
}

// writeRecoveryCandidate encodes one recovery candidate token (Rust
// wire::recovery_candidate: label tag, meta page, source identity,
// database id, transaction id, commit nonce).
func writeRecoveryCandidate(w *WireWriter, value *recovery.RecoveryCandidate) error {
	label, ok := recoveryCandidateLabelTag(value.Label)
	if !ok {
		return &format.Error{Code: format.CodeInvalidArgument, Detail: "worker recovery label is invalid"}
	}
	if err := w.Byte(label); err != nil {
		return err
	}
	if err := w.Byte(value.MetaPage); err != nil {
		return err
	}
	if err := writeIdentity(w, value.SourceIdentity); err != nil {
		return err
	}
	if err := w.Bytes(value.DatabaseID[:]); err != nil {
		return err
	}
	if err := w.U64(value.TransactionID); err != nil {
		return err
	}
	return w.Bytes(value.CommitNonce[:])
}

// recoveryCandidateLabelTag maps one candidate label to its wire tag
// (Rust wire.rs recovery_candidate: Newest 1, Previous 2,
// UnorderedMeta0 3, UnorderedMeta1 4).
func recoveryCandidateLabelTag(label recovery.RecoveryCandidateLabel) (byte, bool) {
	switch label {
	case recovery.CandidateNewest:
		return 1, true
	case recovery.CandidatePrevious:
		return 2, true
	case recovery.CandidateUnorderedMeta0:
		return 3, true
	case recovery.CandidateUnorderedMeta1:
		return 4, true
	}
	return 0, false
}

// recoveryCandidateLabelFromTag maps one wire tag back to a candidate
// label.
func recoveryCandidateLabelFromTag(tag byte) (recovery.RecoveryCandidateLabel, bool) {
	switch tag {
	case 1:
		return recovery.CandidateNewest, true
	case 2:
		return recovery.CandidatePrevious, true
	case 3:
		return recovery.CandidateUnorderedMeta0, true
	case 4:
		return recovery.CandidateUnorderedMeta1, true
	}
	return 0, false
}

// readRecoveryCandidate decodes one recovery candidate token (Rust
// wire::read_recovery_candidate).
func readRecoveryCandidate(r *WireReader) (*recovery.RecoveryCandidate, error) {
	tag, err := r.Byte()
	if err != nil {
		return nil, err
	}
	label, ok := recoveryCandidateLabelFromTag(tag)
	if !ok {
		return nil, &format.Error{Code: format.CodeFormatInvalid, Detail: "worker recovery label is invalid"}
	}
	metaPage, err := r.Byte()
	if err != nil {
		return nil, err
	}
	sourceIdentity, err := readIdentity(r)
	if err != nil {
		return nil, err
	}
	databaseID, err := r.Array16()
	if err != nil {
		return nil, err
	}
	transactionID, err := r.U64()
	if err != nil {
		return nil, err
	}
	commitNonce, err := r.Array16()
	if err != nil {
		return nil, err
	}
	return &recovery.RecoveryCandidate{
		Label:          label,
		MetaPage:       metaPage,
		SourceIdentity: sourceIdentity,
		DatabaseID:     databaseID,
		TransactionID:  transactionID,
		CommitNonce:    commitNonce,
	}, nil
}

// errorCodeFromWire maps one wire error code (Rust
// ErrorCode::from_wire; the Go format codes are the identical 1..69
// numeric table).
func errorCodeFromWire(value uint32) (format.ErrorCode, bool) {
	if value >= 1 && value <= uint32(format.CodeStructureIdExhausted) {
		return format.ErrorCode(value), true
	}
	return 0, false
}

// WireError is the reconstructed worker error of an unregistered wire
// pair (Rust Error::WorkerOperation): the stable code and the optional
// errno. The specific Rust constant variants decode as plain
// format.Errors; every other pair keeps its os code here.
type WireError struct {
	Code   format.ErrorCode
	OSCode *int32
}

func (e *WireError) Error() string {
	if e.OSCode != nil {
		return "iprange v4 worker operation " + strconv.FormatUint(uint64(e.Code), 10) + " errno " + strconv.FormatInt(int64(*e.OSCode), 10)
	}
	return "iprange v4 worker operation " + strconv.FormatUint(uint64(e.Code), 10)
}

// errnoOf unwraps the OS errno of one error (Rust
// io::Error::raw_os_error parity: PathError/SyscallError chains unwrap
// to the raw errno).
func errnoOf(err error) (*int32, bool) {
	var errno syscall.Errno
	if errors.As(err, &errno) {
		value := int32(errno)
		return &value, true
	}
	return nil, false
}

// errorWireParts reduces one error to its wire pair (Rust Error::code
// plus the raw os code): a format.Error reports its class, an errno
// chain the Io class, a worker error its recorded pair, and any other
// error the fixed Conflict class of an unknown failure.
func errorWireParts(err error) (format.ErrorCode, *int32) {
	var workerError *WireError
	if errors.As(err, &workerError) {
		return workerError.Code, workerError.OSCode
	}
	var formatted *format.Error
	if errors.As(err, &formatted) {
		return formatted.Code, nil
	}
	if osCode, ok := errnoOf(err); ok {
		return format.CodeIO, osCode
	}
	return format.CodeConflict, nil
}

// encodeWorkerError writes one error as its code and optional errno
// (Rust wire::encode_error; the detail is deliberately not transmitted,
// exactly like Rust).
func encodeWorkerError(w *WireWriter, value error) error {
	code, osCode := errorWireParts(value)
	if err := w.U32(uint32(code)); err != nil {
		return err
	}
	if err := w.Bool(osCode != nil); err != nil {
		return err
	}
	if osCode != nil {
		return w.I32(*osCode)
	}
	return nil
}

// readWorkerError decodes one error (Rust wire::read_error: known
// constant variants fold to plain format.Errors, an Io class with an
// errno becomes the raw errno, and everything else keeps its wire
// pair).
func readWorkerError(r *WireReader) (error, error) {
	rawCode, err := r.U32()
	if err != nil {
		return nil, err
	}
	code, ok := errorCodeFromWire(rawCode)
	if !ok {
		return nil, &format.Error{Code: format.CodeFormatInvalid, Detail: "worker error code is invalid"}
	}
	hasOS, err := r.Bool()
	if err != nil {
		return nil, err
	}
	var osCode *int32
	if hasOS {
		value, err := r.I32()
		if err != nil {
			return nil, err
		}
		osCode = &value
	}
	switch code {
	case format.CodeIO:
		if osCode != nil {
			return syscall.Errno(*osCode), nil
		}
	case format.CodeNameInvalid, format.CodeNameExists, format.CodeNameNotFound,
		format.CodeStaleReference, format.CodeForeignReference, format.CodePageSpaceExhausted,
		format.CodeFeedIndexExhausted, format.CodeMembershipIdExhausted, format.CodeCancelled,
		format.CodeWriterBusy, format.CodeReaderCapacityExhausted, format.CodeNoPendingTransaction,
		format.CodeStoppedBySink, format.CodeLiveRecoveryCurrentGenerationUnprovable,
		format.CodeLiveRecoveryCurrentGenerationUnreadable, format.CodeRecoveryCandidateChanged,
		format.CodeDirectoryIdentityMismatch, format.CodeForkedHandle:
		return &format.Error{Code: code}, nil
	}
	return &WireError{Code: code, OSCode: osCode}, nil
}

// WriteWorkerError writes one error into a fresh session payload (Rust
// wire::write_worker_error: the failure response of a callback or
// terminal).
func WriteWorkerError(control *Control, value error) error {
	w := NewWireWriter(control)
	if err := encodeWorkerError(w, value); err != nil {
		return err
	}
	return w.Finish()
}

// ReadWorkerError reads one error from the sealed session payload (Rust
// wire::read_worker_error).
func ReadWorkerError(control *Control) (error, error) {
	r, err := NewWireReader(control)
	if err != nil {
		return nil, err
	}
	value, err := readWorkerError(r)
	if err != nil {
		return nil, err
	}
	if err := r.Finish(); err != nil {
		return nil, err
	}
	return value, nil
}

// inspectionModeTag maps one inspection mode to its wire tag (Rust
// wire.rs write_inspection_request: Immutable 1, Live 2, Offline 3).
func inspectionModeTag(mode recovery.RecoveryInspectionMode) (byte, bool) {
	switch mode {
	case recovery.RecoveryInspectionImmutable:
		return 1, true
	case recovery.RecoveryInspectionLive:
		return 2, true
	case recovery.RecoveryInspectionOffline:
		return 3, true
	}
	return 0, false
}

// inspectionModeFromTag maps one wire tag back to an inspection mode.
func inspectionModeFromTag(tag byte) (recovery.RecoveryInspectionMode, bool) {
	switch tag {
	case 1:
		return recovery.RecoveryInspectionImmutable, true
	case 2:
		return recovery.RecoveryInspectionLive, true
	case 3:
		return recovery.RecoveryInspectionOffline, true
	}
	return 0, false
}

// WriteInspectionRequest writes one recovery-candidate inspection
// request (Rust wire::write_inspection_request): path, mode, budget,
// and the unreadable source-page list.
func WriteInspectionRequest(control *Control, path string, mode recovery.RecoveryInspectionMode, budget *validation.ValidationBudget, unreadablePages []uint32) error {
	tag, ok := inspectionModeTag(mode)
	if !ok {
		return &format.Error{Code: format.CodeInvalidArgument, Detail: "worker inspection mode is invalid"}
	}
	w := NewWireWriter(control)
	if err := w.Path(path); err != nil {
		return err
	}
	if err := w.Byte(tag); err != nil {
		return err
	}
	if err := writeValidationBudget(w, budget); err != nil {
		return err
	}
	if err := writeU32List(w, unreadablePages, &format.Error{Code: format.CodeInvalidArgument, Detail: "too many unreadable source pages"}); err != nil {
		return err
	}
	return w.Finish()
}

// InspectionRequest is the decoded inspection request (Rust
// wire.rs read_inspection_request tuple).
type InspectionRequest struct {
	Path            string
	Mode            recovery.RecoveryInspectionMode
	Budget          validation.ValidationBudget
	UnreadablePages []uint32
}

// ReadInspectionRequest decodes one inspection request (Rust
// wire::read_inspection_request; the unreadable-page list charges its
// byte footprint against the budget's heap allowance).
func ReadInspectionRequest(control *Control) (*InspectionRequest, error) {
	r, err := NewWireReader(control)
	if err != nil {
		return nil, err
	}
	request := &InspectionRequest{}
	if request.Path, err = r.Path(); err != nil {
		return nil, err
	}
	tag, err := r.Byte()
	if err != nil {
		return nil, err
	}
	mode, ok := inspectionModeFromTag(tag)
	if !ok {
		return nil, &format.Error{Code: format.CodeFormatInvalid, Detail: "worker inspection mode is invalid"}
	}
	request.Mode = mode
	if request.Budget, err = readValidationBudget(r); err != nil {
		return nil, err
	}
	if request.UnreadablePages, err = readU32List(r, &request.Budget.MaxHeapBytes); err != nil {
		return nil, err
	}
	if err := r.Finish(); err != nil {
		return nil, err
	}
	return request, nil
}

// InspectionWire is the wire form of one recovery-candidate inspection
// result (Rust wire.rs write_inspection_result over
// recovery::RecoveryCandidateInspection; the Go recovery package keeps
// its candidate slots private, so the worker boundary owns the wire
// shape and reads the domain via the exported accessors). On the wire
// the result is tag 0 (facts) or tag 1 (an encoded error).
type InspectionWire struct {
	SourceIdentity publication.LocalFileIdentity
	Progress       ProgressWire
	Candidates     [2]*recovery.RecoveryCandidate
}

// CandidateCount reports the number of present candidate tokens.
func (i *InspectionWire) CandidateCount() int {
	count := 0
	for _, candidate := range i.Candidates {
		if candidate != nil {
			count++
		}
	}
	return count
}

// WireInspectionOf converts one domain inspection to its wire form.
func WireInspectionOf(value *recovery.RecoveryCandidateInspection) InspectionWire {
	out := InspectionWire{SourceIdentity: value.SourceIdentity, Progress: ProgressWireOf(&value.Progress)}
	for index := 0; index < value.CandidateCount() && index < len(out.Candidates); index++ {
		out.Candidates[index] = value.Candidate(index)
	}
	return out
}

// WriteInspectionResult writes one inspection result (Rust
// wire::write_inspection_result): a non-nil inspection encodes the
// facts arm, a nil inspection the error arm.
func WriteInspectionResult(control *Control, inspection *InspectionWire, failure error) error {
	w := NewWireWriter(control)
	if inspection != nil {
		if err := w.Byte(0); err != nil {
			return err
		}
		if err := writeIdentity(w, inspection.SourceIdentity); err != nil {
			return err
		}
		if err := writeProgress(w, &inspection.Progress); err != nil {
			return err
		}
		if err := w.Byte(uint8(inspection.CandidateCount())); err != nil {
			return err
		}
		for _, candidate := range inspection.Candidates {
			if candidate == nil {
				continue
			}
			if err := writeRecoveryCandidate(w, candidate); err != nil {
				return err
			}
		}
		return w.Finish()
	}
	if err := w.Byte(1); err != nil {
		return err
	}
	if err := encodeWorkerError(w, failure); err != nil {
		return err
	}
	return w.Finish()
}

// ReadInspectionResult decodes one inspection result (Rust
// wire::read_inspection_result; more than two candidates is
// corruption).
func ReadInspectionResult(control *Control) (*InspectionWire, error) {
	r, err := NewWireReader(control)
	if err != nil {
		return nil, err
	}
	tag, err := r.Byte()
	if err != nil {
		return nil, err
	}
	if tag == 1 {
		failure, err := readWorkerError(r)
		if err != nil {
			return nil, err
		}
		if err := r.Finish(); err != nil {
			return nil, err
		}
		return nil, failure
	}
	// Rust wire.rs read_inspection_result:332-361 has no tag check;
	// any tag other than the failure tag decodes as facts. The Go
	// grammar must not reject a foreign tag the Rust grammar accepts.
	inspection := &InspectionWire{}
	if inspection.SourceIdentity, err = readIdentity(r); err != nil {
		return nil, err
	}
	if inspection.Progress, err = readProgress(r); err != nil {
		return nil, err
	}
	count, err := r.Byte()
	if err != nil {
		return nil, err
	}
	if count > 2 {
		return nil, &format.Error{Code: format.CodeFormatInvalid, Detail: "worker recovery candidate count is invalid"}
	}
	for index := 0; index < int(count); index++ {
		candidate, err := readRecoveryCandidate(r)
		if err != nil {
			return nil, err
		}
		inspection.Candidates[index] = candidate
	}
	if err := r.Finish(); err != nil {
		return nil, err
	}
	return inspection, nil
}
