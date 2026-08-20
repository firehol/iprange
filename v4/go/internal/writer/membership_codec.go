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
// the caller-owned bounded buffer holds the fixed 64-byte head; inline
// bitmap words are appended by the caller up to the inline limit.
type membershipEncoded struct {
	bytes [membershipRecordLimit]byte
	len   int
}

// newMembershipEncoded builds the record head for one membership entry
// (Rust Encoded::new). blobRoot == 0 selects inline storage; a nonzero
// root selects blob storage with the fixed 64-byte head.
func newMembershipEncoded(id, wordCount uint32, digest [32]byte, blobRoot uint32) (membershipEncoded, error) {
	var encoded membershipEncoded
	bitmapLen := uint64(wordCount) * 8
	length := int(membershipIDBase) + int(bitmapLen)
	storage := membershipStorageInline
	if blobRoot != 0 {
		storage = membershipStorageBlob
		length = membershipIDBase
	}
	if id == 0 || wordCount == 0 || wordCount > membershipMaxWordCount ||
		length > membershipRecordLimit || (blobRoot != 0 && blobRoot < 2) {
		return membershipEncoded{}, invalid("membership record fields are outside the v4 limit")
	}
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
	if blobRoot != 0 {
		putU32(membershipBlobRootOffset, blobRoot)
	}
	putU32(membershipReservedOffset, 0)
	copy(encoded.bytes[membershipDigestOffset:], digest[:])
	encoded.len = length
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

func (idCodec) ReadLeaf(cell []byte) (any, error) {
	record, err := decodeMembershipRecord(cell)
	if err != nil {
		return nil, err
	}
	return record, nil
}

func (idCodec) WriteKey(key tree.Key, output []byte) {
	binary.LittleEndian.PutUint32(output, uint32(key.Hi))
}

// hashKey encodes one hash-tree key (Rust encode_hash): digest bytes,
// word count, id; the raw bytes are the total order.
func hashKey(digest [32]byte, wordCount, id uint32) [membershipHashKeySize]byte {
	var key [membershipHashKeySize]byte
	copy(key[hashDigestOffset:], digest[:])
	binary.LittleEndian.PutUint32(key[hashWordCountOffset:], wordCount)
	binary.LittleEndian.PutUint32(key[hashIDOffset:], id)
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
// record bytes, so byte comparison is the Rust derived Ord.
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
	key := hashKey(digest, wordCount, id)
	return tree.VarKey(key[:]), nil
}

func (hashCodec) ReadLeaf(cell []byte) (any, error) {
	digest, wordCount, id, err := decodeHashKey(cell)
	if err != nil {
		return nil, err
	}
	return membershipHashRecord{digest: digest, wordCount: wordCount, id: id}, nil
}

func (hashCodec) WriteKey(key tree.Key, output []byte) {
	copy(output, key.Bytes())
}

// membershipHashRecord is one decoded hash-tree leaf (Rust HashKey).
type membershipHashRecord struct {
	digest    [32]byte
	wordCount uint32
	id        uint32
}

// digestWords computes the SHA-256 digest of one membership bitmap over
// little-endian u64 words in at most 64-word chunks (Rust hash_words).
func digestWords(words OutputWords) ([32]byte, error) {
	var hasher = sha256.New()
	var buffer [64]uint64
	var start uint32
	total := words.WordCount()
	for start < total {
		count := total - start
		if count > 64 {
			count = 64
		}
		if err := words.ReadWords(start, buffer[:count]); err != nil {
			return [32]byte{}, err
		}
		for _, word := range buffer[:count] {
			var encoded [8]byte
			binary.LittleEndian.PutUint64(encoded[:], word)
			hasher.Write(encoded[:])
		}
		start += count
	}
	var digest [32]byte
	copy(digest[:], hasher.Sum(nil))
	return digest, nil
}
