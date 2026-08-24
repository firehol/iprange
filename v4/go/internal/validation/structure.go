package validation

// Structure validation (Rust validation/structure.rs): the dense ID
// table walk over the structure dictionary, the reverse-index hash tree
// walk with the adjacent same-digest collision compare, the membership
// ownership count, the used-bitmap count, and the bounded slot finish
// (refcount, reverse, and used arms). The reverse tables come from the
// bounded value-kind tables charged in the context.

import (
	"github.com/firehol/iprange/v4/go/internal/bitmap"
	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/reader"
	"github.com/firehol/iprange/v4/go/internal/tree"
)

// validateStructure runs the structure table validators (Rust
// structure::validate): only structured databases carry a dictionary;
// an absent or unknown structure kind is its own invalid class.
func validateStructure(ctx *context) error {
	if ctx.meta.ValueKind != format.ValueKindStructured {
		return nil
	}
	switch ctx.meta.StructureKind {
	case format.StructureKindNetworkEnrichmentV1:
		return validateStructureKind(ctx)
	default:
		return ctx.emit(ReasonStructureInvalid, ObjectStructureDictionary, nil, nil, nil)
	}
}

// validateStructureKind runs the walks of one supported structure kind
// (Rust validate_kind).
func validateStructureKind(ctx *context) error {
	maximumID := uint32(0)
	catalog := reader.NewImmutable(ctx.mapping, ctx.meta)
	idResult, err := walkStructureTable(ctx, ctx.meta.StructureIDRoot, func(ctx *context, pageNumber uint32, expectedID uint64, cell []byte) error {
		return validateStructureRecord(ctx, pageNumber, expectedID, cell, &maximumID, catalog)
	})
	if err != nil {
		return err
	}
	if idResult.records != ctx.meta.StructureEntryCount {
		if err := structureCountMismatch(ctx, ObjectStructureDictionary); err != nil {
			return err
		}
	}
	var previous format.StructureHashKey
	hasPrevious := false
	hashResult, err := walkTree(ctx, ctx.meta.StructureHashRoot, ObjectStructureReverseIndex, structureHashCodec(ctx.meta.StructureKind), func(ctx *context, pageNumber uint32, cell []byte) error {
		return validateStructureHash(ctx, pageNumber, cell, &previous, &hasPrevious, catalog)
	})
	if err != nil {
		return err
	}
	if hashResult.records != ctx.meta.StructureEntryCount {
		if err := structureCountMismatch(ctx, ObjectStructureReverseIndex); err != nil {
			return err
		}
	}
	used, err := validateBitmap(ctx, ctx.meta.StructureUsedRoot, ctx.meta.StructureIDLimit, bitmap.KindStructure)
	if err != nil {
		return err
	}
	return finishStructure(ctx, idResult.records, used, maximumID)
}

// structureHashCodec is the Codec of the reverse-index tree (Rust
// HashCodec: fixed 36-byte hash leaves, fixed 40-byte branch entries,
// the structure kind as aux, and the hash class for both levels).
func structureHashCodec(kind uint8) treeCodec {
	return treeCodec{
		branchType:    byte(format.PageTypeStructureHashBranch),
		leafType:      byte(format.PageTypeStructureHashLeaf),
		aux:           uint32(kind),
		branchLayout:  format.FixedLayout(format.StructureHashBranchSize),
		leafLayout:    format.FixedLayout(format.StructureHashKeySize),
		branchInvalid: ReasonStructureHashInvalid,
		leafInvalid:   ReasonStructureHashInvalid,
		branchKey: func(cell []byte) (tree.Key, bool) {
			key, _, err := format.DecodeStructureHashBranchFields(cell)
			if err != nil {
				return tree.Key{}, false
			}
			return structureHashTreeKey(key), true
		},
		branchChild: func(cell []byte) (uint32, bool) {
			_, child, err := format.DecodeStructureHashBranchFields(cell)
			if err != nil {
				return 0, false
			}
			return child, true
		},
		leafKey: func(cell []byte) (tree.Key, bool) {
			key, err := format.DecodeStructureHashKey(cell)
			if err != nil {
				return tree.Key{}, false
			}
			return structureHashTreeKey(key), true
		},
	}
}

// structureHashTreeKey builds the ordered key of one hash record (Rust
// HashKey Ord: digest bytes, then id; the little-endian id keeps that
// byte order).
func structureHashTreeKey(key format.StructureHashKey) tree.Key {
	var raw [format.StructureHashKeySize]byte
	copy(raw[:32], key.Digest[:])
	format.PutU32(raw[32:36], key.ID)
	return tree.RawKey(raw[:])
}

// validateStructureRecord checks one dense-table record (Rust
// validate_record): the envelope and payload proof, the id-limit and
// slot windows, the nonzero refcount, the payload digest, the single
// define, and the membership ownership count.
func validateStructureRecord(ctx *context, pageNumber uint32, expectedID uint64, cell []byte, maximumID *uint32, catalog *reader.ImmutableReader) error {
	record, err := format.DecodeStructureRecord(cell, expectedID)
	if err != nil {
		return structurePayloadFinding(ctx, &pageNumber)
	}
	if record.ID > *maximumID {
		*maximumID = record.ID
	}
	if uint64(record.ID) != expectedID || expectedID >= ctx.meta.StructureIDLimit {
		if err := structureFinding(ctx, &pageNumber); err != nil {
			return err
		}
	}
	if record.Refcount == 0 {
		if err := structureRefcountFinding(ctx, &pageNumber); err != nil {
			return err
		}
	}
	digest, err := format.StructurePayloadDigest(ctx.meta.StructureKind, record.Payload)
	if err != nil {
		return err
	}
	if digest != record.Digest {
		if err := structureHashFinding(ctx, &pageNumber); err != nil {
			return err
		}
	}
	membershipID := format.U32(record.Payload[24:28])
	result, err := ctx.defineStructure(record.ID, record.Refcount, membershipID, record.Digest)
	if err != nil {
		return err
	}
	if result != InsertInserted {
		if err := structureFinding(ctx, &pageNumber); err != nil {
			return err
		}
	}
	if membershipID != 0 {
		switch ctx.countMembershipOwner(membershipID) {
		case CountFull:
			if err := structureMembershipFinding(ctx, &pageNumber); err != nil {
				return err
			}
		case CountCancelled:
			return &format.Error{Code: format.CodeCancelled, Detail: "validation cancelled"}
		case CountUnavailable:
			if err := structureMembershipFinding(ctx, &pageNumber); err != nil {
				return err
			}
		case CountInserted, CountExisting:
		}
		if _, err := catalog.LookupMembershipID(membershipID); err != nil {
			if err := structureMembershipFinding(ctx, &pageNumber); err != nil {
				return err
			}
		}
	}
	return nil
}

// validateStructureHash checks one reverse-index leaf record (Rust
// validate_hash): the id-limit window, the reverse mark, and the
// adjacent same-digest collision compare.
func validateStructureHash(ctx *context, pageNumber uint32, cell []byte, previous *format.StructureHashKey, hasPrevious *bool, catalog *reader.ImmutableReader) error {
	key, err := format.DecodeStructureHashKey(cell)
	if err != nil {
		// The codec leaf key already reported the undecodable class.
		return nil
	}
	marked, err := ctx.markStructureReverse(key.ID, key.Digest)
	if err != nil {
		return err
	}
	if uint64(key.ID) >= ctx.meta.StructureIDLimit || !marked {
		if err := structureReverseFinding(ctx, &pageNumber); err != nil {
			return err
		}
	}
	if *hasPrevious && previous.Digest == key.Digest {
		equal, err := equalStructurePayloads(catalog, previous.ID, key.ID)
		if err != nil || equal {
			if err := structureHashFinding(ctx, &pageNumber); err != nil {
				return err
			}
		}
	}
	*previous = key
	*hasPrevious = true
	return nil
}

// equalStructurePayloads proves two records carry identical payloads
// (Rust equal_payloads: an absent record is unequal, any read defect is
// reported as equal by the caller's unwrap_or(true)).
func equalStructurePayloads(catalog *reader.ImmutableReader, left, right uint32) (bool, error) {
	leftView, leftFound, err := catalog.LookupStructureID(left)
	if err != nil {
		return false, err
	}
	rightView, rightFound, err := catalog.LookupStructureID(right)
	if err != nil {
		return false, err
	}
	if !leftFound || !rightFound {
		return false, nil
	}
	leftValue, err := leftView.Value()
	if err != nil {
		return false, err
	}
	rightValue, err := rightView.Value()
	if err != nil {
		return false, err
	}
	return leftValue == rightValue, nil
}

// finishStructure proves the dictionary totals (Rust finish: the slot
// defined count, the used-bitmap bits, and the exact id limit against
// the maximum observed id).
func finishStructure(ctx *context, dictionaryRecords, usedBits uint64, maximumID uint32) error {
	defined, err := validateStructureSlots(ctx)
	if err != nil {
		return err
	}
	expectedLimit := uint64(1)
	if maximumID != 0 {
		expectedLimit = uint64(maximumID) + 1
	}
	if defined != dictionaryRecords || defined != ctx.meta.StructureEntryCount ||
		usedBits != defined || ctx.meta.StructureIDLimit != expectedLimit {
		return structureFinding(ctx, nil)
	}
	return nil
}

// validateStructureSlots walks every table entry (Rust validate_slots).
func validateStructureSlots(ctx *context) (uint64, error) {
	defined := uint64(0)
	used := newBitmapWordCache(ctx.meta.StructureUsedRoot, ctx.meta.StructureIDLimit, bitmap.KindStructure)
	slots, err := ctx.structureSlots()
	if err != nil {
		return 0, err
	}
	for index := 0; index < slots; index++ {
		if err := ctx.checkpoint(); err != nil {
			return 0, err
		}
		slot, ok, err := ctx.structureSlot(index)
		if err != nil {
			return 0, err
		}
		if !ok {
			continue
		}
		if slot.Defined {
			defined++
		}
		if err := validateStructureSlot(ctx, slot, &used); err != nil {
			return 0, err
		}
	}
	return defined, nil
}

// validateStructureSlot checks one occupied table entry (Rust
// validate_slot: refcount, reverse mark, and used bit; every arm runs
// and reports its own class).
func validateStructureSlot(ctx *context, slot Slot, used *bitmapWordCache) error {
	if err := validateStructureSlotRefcount(ctx, slot); err != nil {
		return err
	}
	if err := validateStructureSlotReverse(ctx, slot); err != nil {
		return err
	}
	return validateStructureSlotUsed(ctx, slot, used)
}

func validateStructureSlotRefcount(ctx *context, slot Slot) error {
	if !slot.Defined || slot.StoredRefcnt == 0 || slot.StoredRefcnt != slot.RangeCount {
		return structureRefcountFinding(ctx, nil)
	}
	return nil
}

func validateStructureSlotReverse(ctx *context, slot Slot) error {
	if slot.Defined && !slot.ReverseSeen {
		return structureReverseFinding(ctx, nil)
	}
	return nil
}

func validateStructureSlotUsed(ctx *context, slot Slot, used *bitmapWordCache) error {
	if slot.Defined {
		word, err := used.word(ctx, slot.ID/64)
		if err != nil {
			word = 0
		}
		if word&(uint64(1)<<(slot.ID%64)) == 0 {
			return structureFinding(ctx, nil)
		}
	}
	return nil
}

func structureCountMismatch(ctx *context, object ValidationObject) error {
	return ctx.emit(ReasonRootCountInvalid, object, nil, nil, nil)
}

// structureFinding streams the invalid class of a dictionary record,
// slot, or totals proof (Rust structure_finding).
func structureFinding(ctx *context, page *uint32) error {
	return ctx.emit(ReasonStructureInvalid, ObjectStructureDictionary, page, nil, nil)
}

// structurePayloadFinding streams the payload class of an undecodable
// dictionary record (Rust payload_finding).
func structurePayloadFinding(ctx *context, page *uint32) error {
	return ctx.emit(ReasonStructurePayloadInvalid, ObjectStructureDictionary, page, nil, nil)
}

// structureHashFinding streams the hash class of a dictionary record
// whose stored digest disagrees with its payload, or of an adjacent
// same-digest pair with equal payloads (Rust hash_finding).
func structureHashFinding(ctx *context, page *uint32) error {
	return ctx.emit(ReasonStructureHashInvalid, ObjectStructureDictionary, page, nil, nil)
}

// structureReverseFinding streams the invalid class of the
// reverse-index object (Rust reverse_finding).
func structureReverseFinding(ctx *context, page *uint32) error {
	return ctx.emit(ReasonStructureReverseIndexInvalid, ObjectStructureReverseIndex, page, nil, nil)
}

// structureRefcountFinding streams the refcount class of a dictionary
// record or slot (Rust refcount_finding).
func structureRefcountFinding(ctx *context, page *uint32) error {
	return ctx.emit(ReasonStructureRefcountInvalid, ObjectStructureDictionary, page, nil, nil)
}

// structureMembershipFinding streams the membership class of a record
// whose payload names an absent or unprovable membership (Rust
// membership_finding).
func structureMembershipFinding(ctx *context, page *uint32) error {
	return ctx.emit(ReasonStructureMembershipInvalid, ObjectStructureDictionary, page, nil, nil)
}
