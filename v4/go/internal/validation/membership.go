package validation

// Membership validation (Rust validation/membership.rs): the ID and
// hash tree walks over the membership dictionary with the record
// bitmap scan (word content, active-feed window, trailing word, and
// digest proofs), the reverse-index collision compare, the used-bitmap
// count, and the slot refcount/reverse/used finish. The reverse tables
// come from the bounded value-kind tables charged in the context.

import (
	"crypto/sha256"
	stdhash "hash"

	"github.com/firehol/iprange/v4/go/internal/bitmap"
	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/reader"
	"github.com/firehol/iprange/v4/go/internal/tree"
)

// validateMembership runs the membership validators (Rust
// membership::validate): only membership and structured databases carry
// a dictionary; every other value kind is empty.
func validateMembership(ctx *context) error {
	if ctx.meta.ValueKind != format.ValueKindMembership && ctx.meta.ValueKind != format.ValueKindStructured {
		return nil
	}
	feeds := newBitmapWordCache(ctx.meta.FeedUsedRoot, ctx.meta.FeedIndexLimit, bitmap.KindFeed)
	maximumID := uint32(0)
	idResult, err := walkTree(ctx, ctx.meta.MembershipIDRoot, ObjectMembershipDictionary, membershipIDCodec(), func(ctx *context, pageNumber uint32, cell []byte) error {
		return validateMembershipRecord(ctx, pageNumber, cell, &feeds, &maximumID)
	})
	if err != nil {
		return err
	}
	if idResult.records != ctx.meta.MembershipEntryCount {
		if err := membershipCountMismatch(ctx, ObjectMembershipDictionary); err != nil {
			return err
		}
	}
	var previous format.MembershipHashKey
	hasPrevious := false
	catalog := reader.NewImmutable(ctx.mapping, ctx.meta)
	hashResult, err := walkTree(ctx, ctx.meta.MembershipHashRoot, ObjectMembershipReverseIndex, membershipHashCodec(), func(ctx *context, pageNumber uint32, cell []byte) error {
		return validateMembershipHash(ctx, pageNumber, cell, &previous, &hasPrevious, catalog)
	})
	if err != nil {
		return err
	}
	if hashResult.records != ctx.meta.MembershipEntryCount {
		if err := membershipCountMismatch(ctx, ObjectMembershipReverseIndex); err != nil {
			return err
		}
	}
	used, err := validateBitmap(ctx, ctx.meta.MembershipUsedRoot, ctx.meta.MembershipIDLimit, bitmap.KindMembership)
	if err != nil {
		return err
	}
	return finishMembership(ctx, idResult.records, used, maximumID)
}

// membershipIDCodec is the Codec of the ID tree (Rust IdCodec: fixed
// branch entries, variable records on the leaves, and the bitmap class
// for undecodable leaves).
func membershipIDCodec() treeCodec {
	return treeCodec{
		branchType:    byte(format.PageTypeMembershipIDBranch),
		leafType:      byte(format.PageTypeMembershipIDLeaf),
		aux:           0,
		branchLayout:  format.FixedLayout(format.MembershipIDBranchSize),
		leafLayout:    format.VariableLayout(format.MembershipIDRecordMin, format.MaxMembershipIDRecord),
		branchInvalid: ReasonTreeOrderInvalid,
		leafInvalid:   ReasonMembershipBitmapInvalid,
		branchKey: func(cell []byte) (tree.Key, bool) {
			firstID, _, err := format.DecodeMembershipIDBranchFields(cell)
			if err != nil {
				return tree.Key{}, false
			}
			return tree.Key{Lo: uint64(firstID)}, true
		},
		branchChild: func(cell []byte) (uint32, bool) {
			_, child, err := format.DecodeMembershipIDBranchFields(cell)
			if err != nil {
				return 0, false
			}
			return child, true
		},
		leafKey: func(cell []byte) (tree.Key, bool) {
			record, err := format.DecodeMembershipRecord(cell)
			if err != nil {
				return tree.Key{}, false
			}
			return tree.Key{Lo: uint64(record.ID)}, true
		},
	}
}

// membershipHashCodec is the Codec of the reverse-index tree (Rust
// HashCodec: fixed 40-byte hash leaves, fixed 44-byte branch entries,
// and the hash class for both levels).
func membershipHashCodec() treeCodec {
	return treeCodec{
		branchType:    byte(format.PageTypeMembershipHashBranch),
		leafType:      byte(format.PageTypeMembershipHashLeaf),
		aux:           0,
		branchLayout:  format.FixedLayout(format.MembershipHashBranchSize),
		leafLayout:    format.FixedLayout(format.MembershipHashKeySize),
		branchInvalid: ReasonMembershipHashInvalid,
		leafInvalid:   ReasonMembershipHashInvalid,
		branchKey: func(cell []byte) (tree.Key, bool) {
			key, _, err := format.DecodeMembershipHashBranchFields(cell)
			if err != nil {
				return tree.Key{}, false
			}
			return membershipHashTreeKey(key), true
		},
		branchChild: func(cell []byte) (uint32, bool) {
			_, child, err := format.DecodeMembershipHashBranchFields(cell)
			if err != nil {
				return 0, false
			}
			return child, true
		},
		leafKey: func(cell []byte) (tree.Key, bool) {
			key, err := format.DecodeMembershipHashKey(cell)
			if err != nil {
				return tree.Key{}, false
			}
			return membershipHashTreeKey(key), true
		},
	}
}

// membershipHashTreeKey builds the ordered key of one hash record (Rust
// HashKey Ord: digest bytes, then word count, then id; the little-endian
// counts keep that byte order).
func membershipHashTreeKey(key format.MembershipHashKey) tree.Key {
	var raw [40]byte
	copy(raw[:32], key.Digest[:])
	format.PutU32(raw[32:36], key.WordCount)
	format.PutU32(raw[36:40], key.ID)
	return tree.RawKey(raw[:])
}

// validateMembershipRecord checks one ID-tree leaf record (Rust
// validate_record): the id-limit window, the nonzero refcount, the
// single define, and the record bitmap scan.
func validateMembershipRecord(ctx *context, pageNumber uint32, cell []byte, feeds *bitmapWordCache, maximumID *uint32) error {
	record, err := format.DecodeMembershipRecord(cell)
	if err != nil {
		// The codec leaf key already reported the undecodable class.
		return nil
	}
	if record.ID > *maximumID {
		*maximumID = record.ID
	}
	if uint64(record.ID) >= ctx.meta.MembershipIDLimit {
		if err := membershipBitmapFinding(ctx, &pageNumber); err != nil {
			return err
		}
	}
	if record.Refcount == 0 {
		if err := membershipRefcountFinding(ctx, &pageNumber); err != nil {
			return err
		}
	}
	result, err := ctx.defineMembership(record.ID, record.Refcount, record.WordCount, record.Digest)
	if err != nil {
		return err
	}
	if result != InsertInserted {
		if err := membershipBitmapFinding(ctx, &pageNumber); err != nil {
			return err
		}
	}
	return validateMembershipRecordBitmap(ctx, pageNumber, cell, record, feeds)
}

// validateMembershipRecordBitmap scans the record bitmap words (Rust
// validate_record_bitmap: inline records consume their proved inline
// span, blob records scan their blob tree) and reports the shape,
// active-feed, and digest classes.
func validateMembershipRecordBitmap(ctx *context, pageNumber uint32, cell []byte, record format.MembershipRecord, feeds *bitmapWordCache) error {
	scan := newMembershipBitmapScan(feeds)
	var complete bool
	switch record.Storage {
	case format.MembershipStorageInline:
		bytes := cell[format.MembershipIDRecordMin : format.MembershipIDRecordMin+int(record.WordCount)*8]
		if err := scan.consume(ctx, bytes); err != nil {
			return err
		}
		complete = true
	case format.MembershipStorageBlob:
		var err error
		complete, err = scanMembership(ctx, record.BlobRoot, uint64(record.WordCount)*8, func(ctx *context, bytes []byte) error {
			return scan.consume(ctx, bytes)
		})
		if err != nil {
			return err
		}
	}
	lengthMatches := scan.words == record.WordCount
	if !lengthMatches || scan.lastWord == 0 {
		if err := membershipBitmapFinding(ctx, &pageNumber); err != nil {
			return err
		}
	}
	if scan.activeInvalid {
		if err := ctx.emit(ReasonMembershipActiveFeedInvalid, ObjectMembershipDictionary, &pageNumber, nil, nil); err != nil {
			return err
		}
	}
	if complete && lengthMatches && scan.finishDigest() != record.Digest {
		if err := membershipDigestFinding(ctx, &pageNumber); err != nil {
			return err
		}
	}
	return nil
}

// membershipBitmapScan accumulates the word stream of one record bitmap
// (Rust BitmapScan): the digest over the words, the word count, the
// last word, and the active-feed window check through the feed cache.
type membershipBitmapScan struct {
	feeds            *bitmapWordCache
	hasher           stdhash.Hash
	words            uint32
	lastWord         uint64
	activeInvalid    bool
	feedReaderFailed bool
}

func newMembershipBitmapScan(feeds *bitmapWordCache) *membershipBitmapScan {
	return &membershipBitmapScan{feeds: feeds, hasher: sha256.New()}
}

// consume feeds one byte slice of the bitmap stream (Rust
// BitmapScan::consume: word aligned, checkpointed, and hashed in the
// wire byte order).
func (s *membershipBitmapScan) consume(ctx *context, bytes []byte) error {
	if err := ctx.checkpoint(); err != nil {
		return err
	}
	if len(bytes)%8 != 0 {
		return &format.Error{Code: format.CodeFormatInvalid, Detail: "validated membership chunk is not word aligned"}
	}
	for offset := 0; offset < len(bytes); offset += 8 {
		value := format.U64(bytes[offset : offset+8])
		s.hasher.Write(bytes[offset : offset+8])
		if !s.feedReaderFailed {
			s.checkActive(ctx, value)
		}
		s.lastWord = value
		if s.words == ^uint32(0) {
			return &format.Error{Code: format.CodeArithmeticOverflow, Detail: "validation membership word count"}
		}
		s.words++
	}
	return nil
}

// checkActive folds one word against the feed used bitmap (Rust
// BitmapScan::check_active; a feed-read defect poisons the rest of the
// scan).
func (s *membershipBitmapScan) checkActive(ctx *context, value uint64) {
	active, err := s.feeds.word(ctx, s.words)
	if err != nil {
		s.activeInvalid = true
		s.feedReaderFailed = true
		return
	}
	s.activeInvalid = s.activeInvalid || value&^active != 0
}

// finishDigest returns the sha256 over the consumed words (Rust
// BitmapScan::finish_digest).
func (s *membershipBitmapScan) finishDigest() [32]byte {
	var digest [32]byte
	copy(digest[:], s.hasher.Sum(nil))
	return digest
}

// validateMembershipHash checks one reverse-index leaf record (Rust
// validate_hash): the id-limit window, the reverse mark, and the
// adjacent same-digest collision compare.
func validateMembershipHash(ctx *context, pageNumber uint32, cell []byte, previous *format.MembershipHashKey, hasPrevious *bool, catalog *reader.ImmutableReader) error {
	key, err := format.DecodeMembershipHashKey(cell)
	if err != nil {
		// The codec leaf key already reported the undecodable class.
		return nil
	}
	marked, err := ctx.markMembershipReverse(key.ID, key.WordCount, key.Digest)
	if err != nil {
		return err
	}
	if uint64(key.ID) >= ctx.meta.MembershipIDLimit || !marked {
		if err := membershipReverseFinding(ctx, &pageNumber); err != nil {
			return err
		}
	}
	if *hasPrevious && previous.Digest == key.Digest && previous.WordCount == key.WordCount {
		if err := compareMembershipCollision(ctx, pageNumber, previous.ID, key.ID, key.WordCount, catalog); err != nil {
			return err
		}
	}
	*previous = key
	*hasPrevious = true
	return nil
}

// compareMembershipCollision compares the two adjacent records of one
// digest (Rust compare_collision: equal bitmaps are the hash class,
// unequal are clean, and any read defect is the reverse class).
func compareMembershipCollision(ctx *context, pageNumber uint32, left, right, wordCount uint32, catalog *reader.ImmutableReader) error {
	equal, err := equalMemberships(ctx, catalog, left, right, wordCount)
	if err != nil {
		return membershipReverseFinding(ctx, &pageNumber)
	}
	if equal {
		return membershipHashFinding(ctx, &pageNumber)
	}
	return nil
}

// equalMemberships proves two records identical over their bitmap words
// (Rust equal_memberships).
func equalMemberships(ctx *context, catalog *reader.ImmutableReader, left, right, wordCount uint32) (bool, error) {
	leftView, err := catalog.LookupMembershipID(left)
	if err != nil {
		return false, err
	}
	rightView, err := catalog.LookupMembershipID(right)
	if err != nil {
		return false, err
	}
	if leftView.WordCount() != wordCount || rightView.WordCount() != wordCount {
		return false, nil
	}
	return compareMembershipWords(ctx, leftView, rightView, wordCount)
}

// membershipCompareWords is the bounded chunk of one word comparison
// (Rust COMPARE_WORDS).
const membershipCompareWords = 64

// compareMembershipWords compares the two bitmaps chunk by chunk (Rust
// compare_words).
func compareMembershipWords(ctx *context, left, right reader.MembershipView, wordCount uint32) (bool, error) {
	var leftWords [membershipCompareWords]uint64
	var rightWords [membershipCompareWords]uint64
	start := uint32(0)
	for start < wordCount {
		if err := ctx.checkpoint(); err != nil {
			return false, err
		}
		count := wordCount - start
		if count > membershipCompareWords {
			count = membershipCompareWords
		}
		if _, err := left.ReadWords(start, leftWords[:count]); err != nil {
			return false, err
		}
		if _, err := right.ReadWords(start, rightWords[:count]); err != nil {
			return false, err
		}
		for i := 0; i < int(count); i++ {
			if leftWords[i] != rightWords[i] {
				return false, nil
			}
		}
		start += count
	}
	return true, nil
}

// finishMembership proves the dictionary totals (Rust finish: the slot
// defined count, the used-bitmap bits, and the exact id limit against
// the maximum observed id).
func finishMembership(ctx *context, dictionaryRecords, usedBits uint64, maximumID uint32) error {
	defined, err := validateMembershipSlots(ctx)
	if err != nil {
		return err
	}
	expectedLimit := uint64(1)
	if maximumID != 0 {
		expectedLimit = uint64(maximumID) + 1
	}
	if defined != dictionaryRecords || defined != ctx.meta.MembershipEntryCount ||
		usedBits != defined || ctx.meta.MembershipIDLimit != expectedLimit {
		return membershipBitmapFinding(ctx, nil)
	}
	return nil
}

// validateMembershipSlots walks every table entry (Rust validate_slots).
func validateMembershipSlots(ctx *context) (uint64, error) {
	defined := uint64(0)
	used := newBitmapWordCache(ctx.meta.MembershipUsedRoot, ctx.meta.MembershipIDLimit, bitmap.KindMembership)
	slots, err := ctx.membershipSlots()
	if err != nil {
		return 0, err
	}
	for index := 0; index < slots; index++ {
		if err := ctx.checkpoint(); err != nil {
			return 0, err
		}
		slot, ok, err := ctx.membershipSlot(index)
		if err != nil {
			return 0, err
		}
		if !ok {
			continue
		}
		if slot.Defined {
			defined++
		}
		if err := validateMembershipSlot(ctx, slot, &used); err != nil {
			return 0, err
		}
	}
	return defined, nil
}

// validateMembershipSlot checks one occupied table entry (Rust
// validate_slot: refcount, reverse mark, and used bit).
func validateMembershipSlot(ctx *context, slot Slot, used *bitmapWordCache) error {
	if err := validateMembershipSlotRefcount(ctx, slot); err != nil {
		return err
	}
	if err := validateMembershipSlotReverse(ctx, slot); err != nil {
		return err
	}
	return validateMembershipSlotUsed(ctx, slot, used)
}

func validateMembershipSlotRefcount(ctx *context, slot Slot) error {
	// The Rust arm fires for every occupied slot that is not defined by
	// the dictionary too: a range-counted id without a dictionary
	// record is as invalid as a stored/counted mismatch.
	if !slot.Defined || slot.StoredRefcnt == 0 || slot.StoredRefcnt != slot.RangeCount {
		return membershipRefcountFinding(ctx, nil)
	}
	return nil
}

func validateMembershipSlotReverse(ctx *context, slot Slot) error {
	if slot.Defined && !slot.ReverseSeen {
		return membershipReverseFinding(ctx, nil)
	}
	return nil
}

func validateMembershipSlotUsed(ctx *context, slot Slot, used *bitmapWordCache) error {
	if slot.Defined {
		word, err := used.word(ctx, slot.ID/64)
		if err != nil {
			word = 0
		}
		if word&(uint64(1)<<(slot.ID%64)) == 0 {
			return membershipBitmapFinding(ctx, nil)
		}
	}
	return nil
}

func membershipCountMismatch(ctx *context, object ValidationObject) error {
	return ctx.emit(ReasonRootCountInvalid, object, nil, nil, nil)
}

func membershipBitmapFinding(ctx *context, page *uint32) error {
	return ctx.emit(ReasonMembershipBitmapInvalid, ObjectMembershipDictionary, page, nil, nil)
}

func membershipReverseFinding(ctx *context, page *uint32) error {
	return ctx.emit(ReasonMembershipReverseIndexInvalid, ObjectMembershipReverseIndex, page, nil, nil)
}

func membershipRefcountFinding(ctx *context, page *uint32) error {
	return ctx.emit(ReasonMembershipRefcountInvalid, ObjectMembershipDictionary, page, nil, nil)
}

// membershipHashFinding streams the hash class of the reverse-index
// tree object (Rust compare_collision; the hash record walk itself
// reports the same class on the reverse-index object).
func membershipHashFinding(ctx *context, page *uint32) error {
	return ctx.emit(ReasonMembershipHashInvalid, ObjectMembershipReverseIndex, page, nil, nil)
}

// membershipDigestFinding streams the hash class of a dictionary record
// whose stored digest disagrees with its bitmap (Rust report_digest).
func membershipDigestFinding(ctx *context, page *uint32) error {
	return ctx.emit(ReasonMembershipHashInvalid, ObjectMembershipDictionary, page, nil, nil)
}
