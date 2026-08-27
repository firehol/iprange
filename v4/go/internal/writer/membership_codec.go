// Membership dictionary write codecs (Rust
// membership_dictionary/codec.rs): the ID tree (variable leaf records:
// 64-byte fixed head + inline bitmap words, or a blob-tree root) and the
// hash tree (fixed 40-byte keys/leaves, 44-byte branches). The hash key
// order is the raw record bytes (digest, little-endian word count,
// little-endian id), which is exactly the Rust derived Ord of HashKey.

package writer

import (
	"crypto/sha256"
	"encoding/binary"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/tree"
)

const (
	membershipIDBase       = 64
	membershipMaxIDRecord  = format.PageSize - 32 - 2
	membershipMaxWordCount = format.MaxMembershipWordCount
	membershipRecordLimit  = 512
	membershipInlineWords  = (membershipRecordLimit - membershipIDBase) / 8
	// membershipChunkWords is the maximum word batch of one membership
	// source read (Rust HASH_WORDS).
	membershipChunkWords = 64
)

const (
	membershipLengthOffset    = 0
	membershipStorageOffset   = 2
	membershipIDOffset        = 4
	membershipRefcountOffset  = 8
	membershipWordCountOffset = 16
	membershipBitmapLenOffset = 20
	membershipBlobRootOffset  = 24
	membershipReservedOffset  = 28
	membershipDigestOffset    = 32
	hashDigestOffset          = 0
	hashWordCountOffset       = 32
	hashIDOffset              = 36
	membershipIDBranchSize    = 8
	membershipHashKeySize     = 40
	membershipHashBranchSize  = 44
)

// membershipStorage selects inline versus blob bitmap storage (Rust
// Storage).
type membershipStorage uint8

const (
	membershipStorageInline membershipStorage = 0
	membershipStorageBlob   membershipStorage = 1
)

// membershipRecord is one decoded ID-tree leaf (Rust Record).
type membershipRecord struct {
	id        uint32
	refcount  uint64
	wordCount uint32
	digest    [32]byte
	storage   membershipStorage
	blobRoot  uint32
}

// membershipEncoded is one encoded ID-tree leaf record (Rust Encoded):
// a view over the draft state's bounded scratch holding the fixed
// 64-byte head; inline bitmap words are appended by the caller up to the
// inline limit. The scratch is writer-owned heap, so encoding one record
// never allocates (writer allocations are bounded, never per record).
type membershipEncoded struct {
	bytes []byte
	len   int
}

// newMembershipEncoded builds the record head for one membership entry
// into scratch (Rust Encoded::new; the caller owns the scratch and must
// size it to membershipRecordLimit). blobRoot == 0 selects inline
// storage; a nonzero root selects blob storage with the fixed 64-byte
// head.
func newMembershipEncoded(id, wordCount uint32, digest [32]byte, blobRoot uint32, scratch []byte) (membershipEncoded, error) {
	bitmapLen := uint64(wordCount) * 8
	length := int(membershipIDBase) + int(bitmapLen)
	storage := membershipStorageInline
	if blobRoot != 0 {
		storage = membershipStorageBlob
		length = membershipIDBase
		// Rust codec::Encoded::new matches Blob(root >= 2) and returns
		// Corrupt for a blob root below the page namespace.
		if blobRoot < 2 {
			return membershipEncoded{}, corrupt("membership blob root is invalid")
		}
	}
	if id == 0 || wordCount == 0 || wordCount > membershipMaxWordCount ||
		length > membershipRecordLimit {
		return membershipEncoded{}, invalid("membership record fields are outside the v4 limit")
	}
	if len(scratch) < membershipRecordLimit {
		return membershipEncoded{}, corrupt("membership record scratch is too small")
	}
	encoded := membershipEncoded{bytes: scratch, len: length}
	putU16 := func(offset int, v uint16) { binary.LittleEndian.PutUint16(encoded.bytes[offset:], v) }
	putU32 := func(offset int, v uint32) { binary.LittleEndian.PutUint32(encoded.bytes[offset:], v) }
	putU64 := func(offset int, v uint64) { binary.LittleEndian.PutUint64(encoded.bytes[offset:], v) }
	putU16(membershipLengthOffset, uint16(length))
	encoded.bytes[membershipStorageOffset] = byte(storage)
	encoded.bytes[3] = 0
	putU32(membershipIDOffset, id)
	putU64(membershipRefcountOffset, 0)
	putU32(membershipWordCountOffset, wordCount)
	putU32(membershipBitmapLenOffset, uint32(bitmapLen))
	putU32(membershipBlobRootOffset, blobRoot)
	putU32(membershipReservedOffset, 0)
	copy(encoded.bytes[membershipDigestOffset:], digest[:])
	return encoded, nil
}

// putInlineWord writes one bitmap word of an inline record (Rust
// Encoded::put_inline_word). The caller must have sized the record for the
// full inline bitmap.
func (e *membershipEncoded) putInlineWord(index int, value uint64) error {
	at := membershipIDBase + index*8
	if at+8 > e.len {
		return corrupt("membership inline bitmap exceeds its record")
	}
	binary.LittleEndian.PutUint64(e.bytes[at:], value)
	return nil
}

// Len reports the encoded record length.
func (e *membershipEncoded) Len() int { return e.len }

// Slice returns the encoded record bytes.
func (e *membershipEncoded) Slice() []byte { return e.bytes[:e.len] }

// decodeMembershipRecord validates one ID-tree leaf record and returns
// the canonical view (Rust decode_canonical: the inline bitmap's last
// word must be nonzero).
func decodeMembershipRecord(cell []byte) (membershipRecord, error) {
	record, err := decodeMembershipRecordRaw(cell)
	if err != nil {
		return membershipRecord{}, err
	}
	if record.storage == membershipStorageInline {
		last := int(record.wordCount)*8 - 8
		if binary.LittleEndian.Uint64(cell[membershipIDBase+last:]) == 0 {
			return membershipRecord{}, corrupt("membership bitmap is not canonical")
		}
	}
	return record, nil
}

func decodeMembershipRecordRaw(cell []byte) (membershipRecord, error) {
	if len(cell) < membershipIDBase {
		return membershipRecord{}, corrupt("membership dictionary record is malformed")
	}
	if int(binary.LittleEndian.Uint16(cell)) != len(cell) || cell[3] != 0 {
		return membershipRecord{}, corrupt("membership dictionary record is malformed")
	}
	record := membershipRecord{
		id:        binary.LittleEndian.Uint32(cell[membershipIDOffset:]),
		refcount:  binary.LittleEndian.Uint64(cell[membershipRefcountOffset:]),
		wordCount: binary.LittleEndian.Uint32(cell[membershipWordCountOffset:]),
		blobRoot:  binary.LittleEndian.Uint32(cell[membershipBlobRootOffset:]),
	}
	copy(record.digest[:], cell[membershipDigestOffset:])
	bitmapLen := binary.LittleEndian.Uint32(cell[membershipBitmapLenOffset:])
	if record.id == 0 || record.wordCount == 0 || record.wordCount > membershipMaxWordCount ||
		uint64(bitmapLen) != uint64(record.wordCount)*8 ||
		binary.LittleEndian.Uint32(cell[membershipReservedOffset:]) != 0 {
		return membershipRecord{}, corrupt("membership dictionary fields are malformed")
	}
	switch cell[membershipStorageOffset] {
	case 0:
		if record.blobRoot != 0 || len(cell) != membershipIDBase+int(bitmapLen) {
			return membershipRecord{}, corrupt("membership dictionary storage is malformed")
		}
		record.storage = membershipStorageInline
	case 1:
		if record.blobRoot < 2 || len(cell) != membershipIDBase {
			return membershipRecord{}, corrupt("membership dictionary storage is malformed")
		}
		record.storage = membershipStorageBlob
	default:
		return membershipRecord{}, corrupt("membership dictionary storage is malformed")
	}
	return record, nil
}

// idCodec is the membership ID tree (Rust IdCodec): fixed u32 keys,
// variable leaf records (64-byte head + inline bitmap or blob head),
// fixed 8-byte branch cells.
type idCodec struct{}

func (idCodec) BranchType() format.PageType { return format.PageTypeMembershipIDBranch }
func (idCodec) LeafType() format.PageType   { return format.PageTypeMembershipIDLeaf }
func (idCodec) Aux() uint32                 { return 0 }
func (idCodec) KeySize() int                { return 4 }
func (idCodec) LeafSize() int               { return 0 }
func (idCodec) MaxBranchCell() int          { return membershipIDBranchSize }
func (idCodec) MaxLeafCell() int            { return membershipMaxIDRecord }

func (idCodec) LeafRecordBounds() (int, int) {
	return membershipIDBase, membershipMaxIDRecord
}

// BranchRecordBounds reports the fixed 8-byte ID branch cells; the tree
// core only consults it for KeySize == 0 codecs, so the value is the
// branch bound for completeness (Rust codecs implement the whole trait).
func (idCodec) BranchRecordBounds() (int, int) {
	return membershipIDBranchSize, membershipIDBranchSize
}

func (idCodec) WriteBranch(key tree.Key, child uint32, output []byte) (int, error) {
	binary.LittleEndian.PutUint32(output, uint32(key.Hi))
	binary.LittleEndian.PutUint32(output[4:], child)
	return membershipIDBranchSize, nil
}

func (idCodec) ReadBranchChild(cell []byte) (uint32, error) {
	if len(cell) < membershipIDBranchSize {
		return 0, corrupt("membership ID branch record is malformed")
	}
	return binary.LittleEndian.Uint32(cell[4:]), nil
}

func (idCodec) ReadKey(cell []byte, level uint16) (tree.Key, error) {
	if level == 0 {
		if len(cell) < membershipIDBase {
			return tree.Key{}, corrupt("membership dictionary record is malformed")
		}
		return tree.Key{Hi: uint64(binary.LittleEndian.Uint32(cell[membershipIDOffset:]))}, nil
	}
	if len(cell) < membershipIDBranchSize {
		return tree.Key{}, corrupt("membership ID branch record is malformed")
	}
	return tree.Key{Hi: uint64(binary.LittleEndian.Uint32(cell[0:4]))}, nil
}

// CompareKey compares one cell key without materializing a Key (Rust
// IdCodec read_key + u32 Ord). The codec has variable leaf records, so
// the level-0 key lives at membershipIDOffset inside the record head;
// branch cells carry the u32 key prefix.
func (idCodec) CompareKey(cell []byte, level uint16, target tree.Key) (int, error) {
	if level == 0 {
		if len(cell) < membershipIDBase {
			return 0, corrupt("membership dictionary record is malformed")
		}
		return cmpU32(binary.LittleEndian.Uint32(cell[membershipIDOffset:]), uint32(target.Hi)), nil
	}
	if len(cell) < membershipIDBranchSize {
		return 0, corrupt("membership ID branch record is malformed")
	}
	return cmpU32(binary.LittleEndian.Uint32(cell[0:4]), uint32(target.Hi)), nil
}

func (idCodec) ReadLeaf(cell []byte) (membershipRecord, error) {
	return decodeMembershipRecord(cell)
}

func (idCodec) WriteKey(key tree.Key, output []byte) {
	binary.LittleEndian.PutUint32(output, uint32(key.Hi))
}

// writeHashKey encodes one hash-tree LEAF record into dst (Rust
// encode_hash): the wire bytes are the digest, the little-endian word
// count, and the little-endian id. The tree orders keys by the typed
// Rust HashKey Ord (digest bytes, then the numeric word count and id),
// so the tree Key keeps word count and id big-endian (hashProbe) while
// the cells on disk stay exactly the Rust wire layout.
func writeHashKey(dst []byte, digest [32]byte, wordCount, id uint32) {
	copy(dst[hashDigestOffset:], digest[:])
	binary.LittleEndian.PutUint32(dst[hashWordCountOffset:], wordCount)
	binary.LittleEndian.PutUint32(dst[hashIDOffset:], id)
}

// hashKey encodes one hash-tree LEAF record as a value (Rust
// encode_hash; test and wire-build convenience over writeHashKey).
func hashKey(digest [32]byte, wordCount, id uint32) [membershipHashKeySize]byte {
	var key [membershipHashKeySize]byte
	writeHashKey(key[:], digest, wordCount, id)
	return key
}

// hashProbe encodes one hash-tree search key in the numeric total order
// of the Rust HashKey Ord: the digest bytes followed by the big-endian
// word count and id. The tree compares these probe bytes with the keys
// it decodes from wire cells (ReadKey normalizes to the same
// orientation), so (digest, 255) < (digest, 256) exactly like the Rust
// derived Ord.
func hashProbe(digest [32]byte, wordCount, id uint32) [membershipHashKeySize]byte {
	var key [membershipHashKeySize]byte
	copy(key[hashDigestOffset:], digest[:])
	binary.BigEndian.PutUint32(key[hashWordCountOffset:], wordCount)
	binary.BigEndian.PutUint32(key[hashIDOffset:], id)
	return key
}

// decodeHashKey parses one hash-tree key or leaf (Rust decode_hash).
func decodeHashKey(cell []byte) (digest [32]byte, wordCount uint32, id uint32, err error) {
	if len(cell) < membershipHashKeySize {
		err = corrupt("membership hash record is too short")
		return
	}
	copy(digest[:], cell[hashDigestOffset:])
	wordCount = binary.LittleEndian.Uint32(cell[hashWordCountOffset:])
	id = binary.LittleEndian.Uint32(cell[hashIDOffset:])
	if wordCount == 0 || wordCount > membershipMaxWordCount || id == 0 {
		err = corrupt("membership hash record is malformed")
	}
	return
}

// hashCodec is the membership hash tree (Rust HashCodec): fully fixed
// 40-byte keys and leaves, 44-byte branch cells. The key is the raw
// record bytes, so byte Comparison is the Rust derived Ord.
type hashCodec struct{}

func (hashCodec) BranchType() format.PageType { return format.PageTypeMembershipHashBranch }
func (hashCodec) LeafType() format.PageType   { return format.PageTypeMembershipHashLeaf }
func (hashCodec) Aux() uint32                 { return 0 }
func (hashCodec) KeySize() int                { return membershipHashKeySize }
func (hashCodec) LeafSize() int               { return membershipHashKeySize }

func (hashCodec) ReadKey(cell []byte, level uint16) (tree.Key, error) {
	digest, wordCount, id, err := decodeHashKey(cell)
	if err != nil {
		return tree.Key{}, err
	}
	probe := hashProbe(digest, wordCount, id)
	return tree.RawKey(probe[:]), nil
}

// PrefixKeyProbe opts the codec into the inline raw probe (digest plus
// little-endian u32 suffix words, 40-byte fixed keys and leaves).
func (hashCodec) PrefixKeyProbe() {}

// CompareKey compares one cell key without materializing a Key (Rust
// HashKey Ord; never called on the hot path, which uses the raw prefix
// probe): the cell keeps the wire layout (little-endian suffix), so the
// suffix words compare numerically against the big-endian probe words.
func (hashCodec) CompareKey(cell []byte, _ uint16, target tree.Key) (int, error) {
	if _, _, _, err := decodeHashKey(cell); err != nil {
		return 0, err
	}
	return tree.CompareRawKey(cell, membershipHashKeySize, &target.Raw)
}

func (hashCodec) ReadLeaf(cell []byte) (membershipHashRecord, error) {
	digest, wordCount, id, err := decodeHashKey(cell)
	if err != nil {
		return membershipHashRecord{}, err
	}
	return membershipHashRecord{digest: digest, wordCount: wordCount, id: id}, nil
}

func (hashCodec) WriteKey(key tree.Key, output []byte) {
	// Branch cells carry the wire layout (digest, little-endian word
	// count and id); the tree Key is the numeric orientation, so the
	// trailing eight bytes reverse. The probe bytes are the key's raw
	// inline field, never a slice of a local.
	raw := key.Raw
	copy(output[hashDigestOffset:hashWordCountOffset], raw[:hashWordCountOffset])
	output[hashWordCountOffset] = raw[hashWordCountOffset+3]
	output[hashWordCountOffset+1] = raw[hashWordCountOffset+2]
	output[hashWordCountOffset+2] = raw[hashWordCountOffset+1]
	output[hashWordCountOffset+3] = raw[hashWordCountOffset]
	output[hashIDOffset] = raw[hashIDOffset+3]
	output[hashIDOffset+1] = raw[hashIDOffset+2]
	output[hashIDOffset+2] = raw[hashIDOffset+1]
	output[hashIDOffset+3] = raw[hashIDOffset]
}

// membershipHashRecord is one decoded hash-tree leaf (Rust HashKey).
type membershipHashRecord struct {
	digest    [32]byte
	wordCount uint32
	id        uint32
}

// digestWords computes the SHA-256 digest of one membership bitmap over
// little-endian u64 words in at most 64-word chunks (Rust hash_words).
func digestWords[W membershipWords](words W) ([32]byte, error) {
	hasher := sha256.New()
	var start uint32
	total := words.WordCount()
	for start < total {
		chunk, count, err := words.ReadChunk(start)
		if err != nil {
			return [32]byte{}, err
		}
		for _, word := range chunk[:count] {
			var encoded [8]byte
			binary.LittleEndian.PutUint64(encoded[:], word)
			hasher.Write(encoded[:])
		}
		start += count
	}
	var digest [32]byte
	hasher.Sum(digest[:0])
	return digest, nil
}
