package recovery

// Recovery of the authoritative membership-ID records and bitmap bytes
// (Rust recovery/membership_index.rs): the ID tree count and recover
// passes, the locator validation (inline bitmap or blob scan against
// the digest, the feed-bit catalog proofs), and the accepted/rejected
// membership proof.

import (
	"crypto/sha256"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/live"
	"github.com/firehol/iprange/v4/go/internal/mapping"
	"github.com/firehol/iprange/v4/go/internal/validation"
)

// membershipCount counts the recovery-readable membership records
// (Rust membership_index::count).
func membershipCount(m *mapping.Mapping, meta format.Meta, pages *pageSet, check func() error) (uint64, error) {
	counter := &leafCounter{meta: meta, overflowDetail: "recovery membership count", accept: membershipCountAccept}
	if err := scanTree(membershipIDCodec{}, m, meta, meta.MembershipIDRoot, pages, check, counter); err != nil {
		return 0, err
	}
	return counter.count, nil
}

func membershipCountAccept(meta format.Meta, cell []byte) bool {
	record, err := format.DecodeMembershipRecord(cell)
	return err == nil && membershipRecordFieldsValid(record, meta)
}

// membershipRecordFieldsValid proves the raw record fields (Rust
// record_fields_valid: the ID bound, the word-count bound from the
// feed limit, and the storage proof).
func membershipRecordFieldsValid(record format.MembershipRecord, meta format.Meta) bool {
	maximumWords := meta.FeedIndexLimit
	if maximumWords < ^uint64(0)-63 {
		maximumWords += 63
	} else {
		maximumWords = ^uint64(0)
	}
	maximumWords /= 64
	if uint64(record.ID) >= meta.MembershipIDLimit || uint64(record.WordCount) > maximumWords {
		return false
	}
	switch record.Storage {
	case format.MembershipStorageInline:
		return true
	case format.MembershipStorageBlob:
		return uint64(record.BlobRoot) < meta.PageCount
	default:
		return false
	}
}

// recoverMemberships reconciles the membership dictionary of one
// source (Rust membership_index::recover: the ID tree scan into the
// locator table, the per-record validation with the ID registration,
// and the finish proof).
func recoverMemberships(m *mapping.Mapping, meta format.Meta, catalog *catalog, pages *pageSet, tables *tableStore, check func() error, rep *reporter) (*membershipIndex, error) {
	entries := newMembershipIndex(tables)
	events := &membershipEvents{meta: meta, rep: rep, entries: entries, tables: tables}
	if err := scanTree(membershipIDCodec{}, m, meta, meta.MembershipIDRoot, pages, check, events); err != nil {
		return nil, err
	}
	validation := &membershipValidation{m: m, meta: meta, catalog: catalog, pages: pages, tables: tables, check: check, rep: rep}
	if err := validation.entries(entries); err != nil {
		return nil, err
	}
	return finishMemberships(entries, tables, rep)
}

// membershipIDCodec is the ID-tree scan codec (Rust IdCodec: the fixed
// branch cells and the variable membership records keyed by the ID).
type membershipIDCodec struct{}

func (membershipIDCodec) object() validation.ValidationObject {
	return validation.ObjectMembershipDictionary
}
func (membershipIDCodec) branchType() format.PageType { return format.PageTypeMembershipIDBranch }
func (membershipIDCodec) leafType() format.PageType   { return format.PageTypeMembershipIDLeaf }
func (membershipIDCodec) aux() uint32                 { return 0 }
func (membershipIDCodec) branchLayout() format.CellLayout {
	return format.FixedLayout(format.MembershipIDBranchSize)
}
func (membershipIDCodec) leafLayout() format.CellLayout {
	return format.VariableLayout(format.MembershipIDRecordMin, format.MaxMembershipIDRecord)
}
func (membershipIDCodec) branchInvalid() validation.ValidationReason {
	return validation.ReasonMembershipInvalid
}
func (membershipIDCodec) leafInvalid() validation.ValidationReason {
	return validation.ReasonMembershipInvalid
}
func (membershipIDCodec) decodeBranch(cell []byte) (treeKey, uint32, bool) {
	record, err := format.DecodeMembershipIDBranch(cell)
	if err != nil {
		return nil, 0, false
	}
	return record.FirstID, record.Child, true
}
func (membershipIDCodec) decodeLeafKey(cell []byte) (treeKey, bool) {
	id, err := format.MembershipIDLeafKey(cell)
	if err != nil {
		return nil, false
	}
	return id, true
}
func (membershipIDCodec) less(a, b treeKey) bool  { return a.(uint32) < b.(uint32) }
func (membershipIDCodec) equal(a, b treeKey) bool { return a.(uint32) == b.(uint32) }

// membershipEvents wires the ID tree scan into the reporter and the
// locator table (Rust membership_index::Events: every leaf counts one
// examined record, undecodable or invalid records reject with the
// exact classes, readable records push their locator).
type membershipEvents struct {
	meta    format.Meta
	rep     *reporter
	entries *membershipIndex
	tables  *tableStore
}

func (e *membershipEvents) pageAccepted() error {
	return e.rep.pageAccepted()
}

func (e *membershipEvents) pageRejected(ioUnreadable bool) error {
	return e.rep.pageRejected(ioUnreadable)
}

func (e *membershipEvents) unknown(reason validation.ValidationReason, object validation.ValidationObject, page *uint32) error {
	return e.rep.emitPageUnknown(reason, object, page)
}

func (e *membershipEvents) leaf(page uint32, index int, cell []byte, ok bool) error {
	if err := e.rep.membershipExamined(); err != nil {
		return err
	}
	record, err := format.DecodeMembershipRecord(cell)
	if !ok || err != nil {
		return e.rep.membershipRejected(1)
	}
	if !membershipRecordFieldsValid(record, e.meta) {
		if err := e.rep.membershipRejected(1); err != nil {
			return err
		}
		pageNumber := page
		return e.rep.emitPageUnknown(validation.ReasonMembershipInvalid, validation.ObjectMembershipDictionary, &pageNumber)
	}
	if index > int(^uint16(0)) {
		return corruptError("membership leaf index exceeds its page")
	}
	return e.entries.push(e.tables, membershipLocator{
		id:        record.ID,
		wordCount: record.WordCount,
		digest:    record.Digest,
		leafPage:  page,
		leafIndex: uint16(index),
		storage:   record.Storage,
		blobRoot:  record.BlobRoot,
	})
}

// membershipValidation proves every recovered locator (Rust
// membership_index::Validation).
type membershipValidation struct {
	m       *mapping.Mapping
	meta    format.Meta
	catalog *catalog
	pages   *pageSet
	tables  *tableStore
	check   func() error
	rep     *reporter
}

func (v *membershipValidation) entries(entries *membershipIndex) error {
	for index := uint64(0); index < entries.recordsLen(); index++ {
		if err := v.one(entries, index); err != nil {
			return err
		}
	}
	return nil
}

func (v *membershipValidation) one(entries *membershipIndex, index uint64) error {
	if err := live.Checkpoint(v.check); err != nil {
		return err
	}
	entry, err := entries.record(v.tables, index)
	if err != nil {
		return err
	}
	valid, err := v.entry(entry)
	if err != nil {
		return err
	}
	if !valid {
		if err := entries.reject(v.tables, index); err != nil {
			return err
		}
		pageNumber := entry.leafPage
		if err := v.rep.emitPageUnknown(validation.ReasonMembershipInvalid, validation.ObjectMembershipDictionary, &pageNumber); err != nil {
			return err
		}
	}
	return v.registerID(entries, entry.id, index)
}

func (v *membershipValidation) registerID(entries *membershipIndex, id uint32, index uint64) error {
	insert, err := entries.insertID(v.tables, id, index)
	if err != nil {
		return err
	}
	if !insert.duplicate {
		return nil
	}
	if err := entries.reject(v.tables, insert.first); err != nil {
		return err
	}
	if err := entries.reject(v.tables, index); err != nil {
		return err
	}
	if insert.newlyConflicted {
		if err := v.rep.emitPageUnknown(validation.ReasonMembershipInvalid, validation.ObjectMembershipDictionary, nil); err != nil {
			return err
		}
	}
	return nil
}

func (v *membershipValidation) entry(entry membershipLocator) (bool, error) {
	bitmap := newBitmapCheck(v.catalog, v.tables)
	var complete bool
	var err error
	switch entry.storage {
	case format.MembershipStorageInline:
		var bytes []byte
		bytes, err = readInlineBitmap(v.m, v.meta, entry)
		if err != nil {
			return false, err
		}
		if err := bitmap.consume(bytes); err != nil {
			return false, err
		}
		complete = true
	case format.MembershipStorageBlob:
		complete, err = scanMembershipBlob(v.m, v.meta, entry.blobRoot, entry.wordCount, v.pages, v.check, v.rep, func(bytes []byte) error {
			return bitmap.consume(bytes)
		})
		if err != nil {
			return false, err
		}
	default:
		return false, corruptError("recovery membership storage is invalid")
	}
	return complete && bitmap.valid(entry.wordCount, entry.digest), nil
}

// bitmapCheck proves one recovered bitmap (Rust BitmapCheck: the
// running SHA-256 over the little-endian words, the feed-bit catalog
// proofs, the nonzero final word, and the exact word count).
type bitmapCheck struct {
	catalog  *catalog
	tables   *tableStore
	hasher   sha256Hasher
	words    uint32
	last     uint64
	inactive bool
}

type sha256Hasher = interface {
	Write([]byte) (int, error)
	Sum([]byte) []byte
}

func newBitmapCheck(catalog *catalog, tables *tableStore) *bitmapCheck {
	return &bitmapCheck{catalog: catalog, tables: tables, hasher: sha256.New()}
}

func (b *bitmapCheck) consume(bytes []byte) error {
	if len(bytes)%8 != 0 {
		return corruptError("recovery membership bytes are not word aligned")
	}
	for offset := 0; offset < len(bytes); offset += 8 {
		value := format.U64(bytes[offset : offset+8])
		var word [8]byte
		format.PutU64(word[:], value)
		if _, err := b.hasher.Write(word[:]); err != nil {
			return err
		}
		remaining := value
		for remaining != 0 {
			bit := trailingZeros(remaining)
			index := uint64(b.words)*64 + bit
			if index > uint64(^uint32(0)) {
				return corruptError("recovery membership feed bit is invalid")
			}
			contained, err := b.catalog.contains(b.tables, uint32(index))
			if err != nil {
				return err
			}
			b.inactive = b.inactive || !contained
			remaining &= remaining - 1
		}
		b.last = value
		next := b.words + 1
		if next == 0 {
			return overflowError("recovery membership words")
		}
		b.words = next
	}
	return nil
}

func (b *bitmapCheck) valid(expectedWords uint32, expectedDigest [32]byte) bool {
	var digest [32]byte
	copy(digest[:], b.hasher.Sum(nil))
	return b.words == expectedWords &&
		b.last != 0 &&
		!b.inactive &&
		digest == expectedDigest
}

// trailingZeros counts the trailing zero bits of one word (Rust
// trailing_zeros).
func trailingZeros(value uint64) uint64 {
	bit := uint64(0)
	for value&1 == 0 {
		value >>= 1
		bit++
	}
	return bit
}

// finishMemberships folds the membership proof (Rust finish: the
// rejected locators, then the accepted remainder).
func finishMemberships(entries *membershipIndex, tables *tableStore, rep *reporter) (*membershipIndex, error) {
	var rejected uint64
	for index := uint64(0); index < entries.recordsLen(); index++ {
		record, err := entries.record(tables, index)
		if err != nil {
			return nil, err
		}
		if record.rejected {
			rejected++
		}
	}
	if err := rep.membershipRejected(rejected); err != nil {
		return nil, err
	}
	if err := rep.membershipAccepted(entries.recordsLen() - rejected); err != nil {
		return nil, err
	}
	return entries, nil
}
