package format

// Membership dictionary records (binary-format-v4.md section 9).

// MembershipStorage selects inline versus blob bitmap storage.
type MembershipStorage uint8

const (
	MembershipStorageInline MembershipStorage = 0
	MembershipStorageBlob   MembershipStorage = 1
)

// MembershipIDLeaf is one parsed ID-tree leaf record. The Inline field, when
// storage is inline, aliases the page view; the view that retains it is
// guarded by a live pin, so the slice never outlives the mapping.
type MembershipIDLeaf struct {
	Storage      MembershipStorage
	MembershipID uint32
	OwnerRef     uint64
	WordCount    uint32
	BitmapLen    uint32
	BlobRoot     uint32
	BitmapSHA256 [32]byte
	Inline       []byte
}

const membershipLeafFixed = 64

// MaxInlineBitmapLen is the largest inline bitmap that fits one record page.
const MaxInlineBitmapLen = PageSize - 32 - membershipLeafFixed

// DecodeMembershipIDLeaf parses one ID-tree leaf record.
func DecodeMembershipIDLeaf(b []byte) (MembershipIDLeaf, error) {
	if len(b) < membershipLeafFixed {
		return MembershipIDLeaf{}, headerErr("short membership leaf record %d", len(b))
	}
	recordLen := U16(b[0:2])
	if int(recordLen) > len(b) {
		return MembershipIDLeaf{}, headerErr("membership record length %d", recordLen)
	}
	if b[3] != 0 {
		return MembershipIDLeaf{}, headerErr("membership record reserved byte")
	}
	if U32(b[28:32]) != 0 {
		return MembershipIDLeaf{}, headerErr("membership record reserved field")
	}
	var r MembershipIDLeaf
	switch b[2] {
	case 0:
		r.Storage = MembershipStorageInline
	case 1:
		r.Storage = MembershipStorageBlob
	default:
		return MembershipIDLeaf{}, headerErr("membership storage %d", b[2])
	}
	r.MembershipID = U32(b[4:8])
	if r.MembershipID == 0 {
		return MembershipIDLeaf{}, headerErr("zero membership id")
	}
	r.OwnerRef = U64(b[8:16])
	if r.OwnerRef == 0 {
		return MembershipIDLeaf{}, headerErr("zero membership refcount")
	}
	r.WordCount = U32(b[16:20])
	if r.WordCount < 1 || r.WordCount > MaxMembershipWordCount {
		return MembershipIDLeaf{}, headerErr("membership word count %d", r.WordCount)
	}
	r.BitmapLen = U32(b[20:24])
	if uint64(r.BitmapLen) != uint64(r.WordCount)*8 {
		return MembershipIDLeaf{}, headerErr("membership bitmap length %d vs words %d", r.BitmapLen, r.WordCount)
	}
	r.BlobRoot = U32(b[24:28])
	copy(r.BitmapSHA256[:], b[32:64])
	switch r.Storage {
	case MembershipStorageInline:
		if r.BlobRoot != 0 {
			return MembershipIDLeaf{}, headerErr("inline bitmap with blob root")
		}
		if int(recordLen) != membershipLeafFixed+int(r.BitmapLen) {
			return MembershipIDLeaf{}, headerErr("inline record length %d vs %d", recordLen, membershipLeafFixed+r.BitmapLen)
		}
		r.Inline = b[64 : 64+int(r.BitmapLen)]
	case MembershipStorageBlob:
		if r.BlobRoot == 0 || !PageNumberValid(r.BlobRoot, MaxPageCount) {
			return MembershipIDLeaf{}, headerErr("blob storage without valid root")
		}
		if int(recordLen) != membershipLeafFixed {
			return MembershipIDLeaf{}, headerErr("blob record length %d", recordLen)
		}
	}
	return r, nil
}

// MembershipIDBranchRecord is one fixed 8-byte ID-branch entry.
type MembershipIDBranchRecord struct {
	FirstID uint32
	Child   uint32
}

const MembershipIDBranchSize = 8

// DecodeMembershipIDBranch parses one fixed ID branch entry.
func DecodeMembershipIDBranch(b []byte) (MembershipIDBranchRecord, error) {
	if len(b) < MembershipIDBranchSize {
		return MembershipIDBranchRecord{}, headerErr("short id branch %d", len(b))
	}
	r := MembershipIDBranchRecord{FirstID: U32(b[0:4]), Child: U32(b[4:8])}
	if !PageNumberValid(r.Child, MaxPageCount) {
		return MembershipIDBranchRecord{}, headerErr("id branch child out of range")
	}
	return r, nil
}

// Key-only membership codecs (search.go): probes read the id key only; the
// selected record is decoded and validated once by the caller.

// MembershipIDBranchKey reads the first_id key of one ID-branch entry.
func MembershipIDBranchKey(b []byte) (uint32, error) {
	if len(b) < MembershipIDBranchSize {
		return 0, headerErr("short id branch %d", len(b))
	}
	return U32(b[0:4]), nil
}

// MembershipIDLeafKey reads the membership id key of one ID-leaf record.
// The fixed base length is enforced so the key offset is always inside the
// checked record slice.
func MembershipIDLeafKey(b []byte) (uint32, error) {
	if len(b) < membershipLeafFixed {
		return 0, headerErr("short membership leaf record %d", len(b))
	}
	return U32(b[4:8]), nil
}

// Raw membership record and hash codecs for the validation walkers (Rust
// membership_dictionary::codec::decode/decode_hash/decode_id_branch/
// decode_hash_branch parity): the fields decode without the reader's
// extra refcount and page-range checks so the validators can report
// those classes as findings.

// MembershipRecord is one raw dictionary record (Rust Record).
type MembershipRecord struct {
	ID        uint32
	Refcount  uint64
	WordCount uint32
	Digest    [32]byte
	Storage   MembershipStorage
	BlobRoot  uint32
}

// MembershipIDRecordMin is the fixed head of one membership record
// (Rust ID_BASE).
const MembershipIDRecordMin = membershipLeafFixed

// MaxMembershipIDRecord is the largest membership record the slotted
// layout can carry (Rust MAX_ID_RECORD).
const MaxMembershipIDRecord = PageSize - SlottedHeaderSize - 2

// DecodeMembershipRecord parses one variable membership record (Rust
// codec::decode): the stored record length proves the record span, the
// fields are canonical (nonzero id, in-range word count, bitmap length
// equal to the word count times eight), and the storage flag picks the
// inline or blob layout. Refcount is deliberately not checked: the
// validation walk reports a zero refcount as the refcount finding.
func DecodeMembershipRecord(b []byte) (MembershipRecord, error) {
	if len(b) < membershipLeafFixed {
		return MembershipRecord{}, headerErr("membership record is too short")
	}
	if int(U16(b[0:2])) != len(b) {
		return MembershipRecord{}, headerErr("membership record length mismatch")
	}
	if b[3] != 0 {
		return MembershipRecord{}, headerErr("membership record reserved byte")
	}
	r := MembershipRecord{
		ID:        U32(b[4:8]),
		Refcount:  U64(b[8:16]),
		WordCount: U32(b[16:20]),
		Digest:    [32]byte(b[32:64]),
		BlobRoot:  U32(b[24:28]),
	}
	if U32(b[28:32]) != 0 {
		return MembershipRecord{}, headerErr("membership record reserved field")
	}
	bitmapLen := U32(b[20:24])
	if r.ID == 0 {
		return MembershipRecord{}, headerErr("membership id is zero")
	}
	if r.WordCount < 1 || r.WordCount > MaxMembershipWordCount {
		return MembershipRecord{}, headerErr("membership word count %d", r.WordCount)
	}
	if uint64(bitmapLen) != uint64(r.WordCount)*8 {
		return MembershipRecord{}, headerErr("membership bitmap length %d vs words %d", bitmapLen, r.WordCount)
	}
	switch b[2] {
	case 0:
		if r.BlobRoot != 0 || len(b) != membershipLeafFixed+int(bitmapLen) {
			return MembershipRecord{}, headerErr("membership inline storage is malformed")
		}
		r.Storage = MembershipStorageInline
	case 1:
		if r.BlobRoot < 2 || len(b) != membershipLeafFixed {
			return MembershipRecord{}, headerErr("membership blob storage is malformed")
		}
		r.Storage = MembershipStorageBlob
	default:
		return MembershipRecord{}, headerErr("membership storage %d", b[2])
	}
	return r, nil
}

// MembershipHashKey is one reverse-index hash key (Rust HashKey:
// digest, word count, id; ordered lexicographically by those fields).
type MembershipHashKey struct {
	Digest    [32]byte
	WordCount uint32
	ID        uint32
}

const (
	// MembershipHashKeySize is the fixed hash leaf record size (Rust
	// HASH_KEY_SIZE).
	MembershipHashKeySize = 40
	// MembershipHashBranchSize is the fixed hash branch entry size
	// (Rust HASH_BRANCH_SIZE).
	MembershipHashBranchSize = 44
)

// DecodeMembershipHashKey parses one fixed 40-byte reverse-index key
// (Rust codec::decode_hash).
func DecodeMembershipHashKey(b []byte) (MembershipHashKey, error) {
	if len(b) != MembershipHashKeySize {
		return MembershipHashKey{}, headerErr("membership hash record size %d", len(b))
	}
	key := MembershipHashKey{
		Digest:    [32]byte(b[0:32]),
		WordCount: U32(b[32:36]),
		ID:        U32(b[36:40]),
	}
	if key.WordCount == 0 || key.WordCount > MaxMembershipWordCount || key.ID == 0 {
		return MembershipHashKey{}, headerErr("membership hash record is malformed")
	}
	return key, nil
}

// DecodeMembershipHashBranchFields parses one fixed 44-byte hash branch
// entry without the child-page-number check (Rust codec::
// decode_hash_branch: the key decodes in place and the child is raw).
func DecodeMembershipHashBranchFields(b []byte) (MembershipHashKey, uint32, error) {
	if len(b) != MembershipHashBranchSize {
		return MembershipHashKey{}, 0, headerErr("membership hash branch size %d", len(b))
	}
	key, err := DecodeMembershipHashKey(b[0:MembershipHashKeySize])
	if err != nil {
		return MembershipHashKey{}, 0, err
	}
	return key, U32(b[40:44]), nil
}

// DecodeMembershipIDBranchFields parses one fixed 8-byte ID branch entry
// without the child-page-number check (Rust codec::decode_id_branch).
func DecodeMembershipIDBranchFields(b []byte) (firstID uint32, child uint32, err error) {
	if len(b) != MembershipIDBranchSize {
		return 0, 0, headerErr("membership id branch size %d", len(b))
	}
	return U32(b[0:4]), U32(b[4:8]), nil
}
