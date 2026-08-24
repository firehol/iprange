package recovery

// Membership locator table (Rust recovery/membership_table.rs): the
// fixed 56-byte locator records of the recovered membership
// dictionary, with the exact wire offsets of the Rust records.

import "github.com/firehol/iprange/v4/go/internal/format"

const (
	membershipRecordPresentOffset = 0
	membershipRecordStorageOffset = 1
	membershipRecordLeafIndexOff  = 2
	membershipRecordIDOffset      = 4
	membershipRecordWordCountOff  = 8
	membershipRecordLeafPageOff   = 12
	membershipRecordBlobRootOff   = 16
	membershipRecordRejectedOff   = 20
	membershipRecordDigestOffset  = 24
	membershipRecordDigestEnd     = 56
)

// membershipLocator is one recovered membership dictionary record
// (Rust recovery membership_index::Locator): the source facts needed
// to verify and rebuild one bitmap.
type membershipLocator struct {
	id        uint32
	wordCount uint32
	digest    [32]byte
	leafPage  uint32
	leafIndex uint16
	blobRoot  uint32
	storage   format.MembershipStorage
	rejected  bool
}

// membershipCodec adapts the locator to the shared id table (Rust
// MembershipCodec).
func membershipCodec() recordCodec[membershipLocator] {
	return recordCodec[membershipLocator]{
		width:         int(membershipRecordSize),
		invalidRecord: "recovery membership record index is invalid",
		full:          "recovery membership ID table is full",
		regions: func(layout tableLayout) (tableRegion, tableRegion) {
			return layout.membershipRecords, layout.membershipIDs
		},
		encode:     encodeMembershipLocator,
		decode:     decodeMembershipLocator,
		isRejected: func(record membershipLocator) bool { return record.rejected },
		reject:     func(record *membershipLocator) { record.rejected = true },
	}
}

// encodeMembershipLocator encodes one locator (Rust membership_table::
// encode: the present flag, the storage flag with the inline root
// zero, and every wire field).
func encodeMembershipLocator(locator membershipLocator, output []byte) {
	for index := range output {
		output[index] = 0
	}
	output[membershipRecordPresentOffset] = 1
	root := uint32(0)
	if locator.storage == format.MembershipStorageBlob {
		output[membershipRecordStorageOffset] = 1
		root = locator.blobRoot
	}
	format.PutU16(output[membershipRecordLeafIndexOff:membershipRecordIDOffset], locator.leafIndex)
	format.PutU32(output[membershipRecordIDOffset:membershipRecordWordCountOff], locator.id)
	format.PutU32(output[membershipRecordWordCountOff:membershipRecordLeafPageOff], locator.wordCount)
	format.PutU32(output[membershipRecordLeafPageOff:membershipRecordBlobRootOff], locator.leafPage)
	format.PutU32(output[membershipRecordBlobRootOff:membershipRecordRejectedOff], root)
	output[membershipRecordRejectedOff] = 0
	if locator.rejected {
		output[membershipRecordRejectedOff] = 1
	}
	copy(output[membershipRecordDigestOffset:membershipRecordDigestEnd], locator.digest[:])
}

// decodeMembershipLocator decodes one locator (Rust membership_table::
// decode: the present flag, the storage proof, and every wire field;
// every refusal is the Corrupt class).
func decodeMembershipLocator(bytes []byte) (membershipLocator, error) {
	if len(bytes) != int(membershipRecordSize) {
		return membershipLocator{}, corruptError("recovery membership locator has wrong size")
	}
	if bytes[membershipRecordPresentOffset] != 1 || bytes[membershipRecordStorageOffset] > 1 {
		return membershipLocator{}, corruptError("recovery membership locator is malformed")
	}
	root := format.U32(bytes[membershipRecordBlobRootOff:membershipRecordRejectedOff])
	var storage format.MembershipStorage
	switch {
	case bytes[membershipRecordStorageOffset] == 0 && root == 0:
		storage = format.MembershipStorageInline
	case bytes[membershipRecordStorageOffset] == 1 && root >= 2:
		storage = format.MembershipStorageBlob
	default:
		return membershipLocator{}, corruptError("recovery membership locator storage is malformed")
	}
	var locator membershipLocator
	copy(locator.digest[:], bytes[membershipRecordDigestOffset:membershipRecordDigestEnd])
	locator.id = format.U32(bytes[membershipRecordIDOffset:membershipRecordWordCountOff])
	locator.wordCount = format.U32(bytes[membershipRecordWordCountOff:membershipRecordLeafPageOff])
	locator.leafPage = format.U32(bytes[membershipRecordLeafPageOff:membershipRecordBlobRootOff])
	locator.leafIndex = format.U16(bytes[membershipRecordLeafIndexOff:membershipRecordIDOffset])
	locator.storage = storage
	locator.blobRoot = root
	locator.rejected = bytes[membershipRecordRejectedOff] != 0
	return locator, nil
}

// membershipIndex is the recovered membership locator table (Rust
// MembershipIndex = IdIndex<MembershipCodec>).
type membershipIndex = idIndex[membershipLocator]

// newMembershipIndex builds the membership locator table over the
// store layout (Rust MembershipIndex::new).
func newMembershipIndex(tables *tableStore) *membershipIndex {
	return newIDIndex(tables, membershipCodec())
}
