// Feed-catalog write codecs and atomic dual-index maintenance (Rust
// feed_catalog/codec.rs + mutation.rs). Both indexes share one record
// wire format: u16 record length @0, u16 zero @2, u32 feed index (or
// child page in name branches) @4, u8 name length @8, three zero bytes
// @9, then the name @12.

package writer

import (
	"bytes"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/tree"
	"github.com/firehol/iprange/v4/go/internal/work"
)

const (
	catalogRecordBase  = 12
	catalogMinRecord   = catalogRecordBase + 1
	catalogMaxRecord   = catalogRecordBase + format.MaxFeedNameLen
	catalogIndexBranch = 8
	catalogNameOffset  = 12
	catalogIndexOffset = 4
	catalogNameLenOff  = 8
)

// encodeCatalogRecord writes one name record into scratch and returns
// the encoded length (Rust feed_catalog::codec::encode: len + index +
// name). The scratch is the caller's bounded encode target, sized at
// least catalogMaxRecord; callers keep it for the lifetime of the
// draft or output so repeated inserts never allocate (the Go generic
// tree interface makes stack encodes escape). The name must already
// satisfy the v4 feed-name grammar; the encoder re-checks it so a
// caller bug cannot plant an invalid key. The name is the caller's
// string (never a mapped view), so no owned copy of page bytes can
// happen here.
func encodeCatalogRecord(name string, index uint32, scratch []byte) (int, error) {
	if len(scratch) < catalogMaxRecord {
		return 0, corrupt("feed catalog record scratch is too small")
	}
	if !format.FeedNameValidString(name) {
		return 0, corrupt("feed catalog name is invalid")
	}
	length := catalogRecordBase + len(name)
	putU16 := func(offset int, v uint16) { format.PutU16(scratch[offset:], v) }
	putU32 := func(offset int, v uint32) { format.PutU32(scratch[offset:], v) }
	putU16(0, uint16(length))
	putU16(2, 0)
	putU32(catalogIndexOffset, index)
	scratch[catalogNameLenOff] = byte(len(name))
	scratch[9] = 0
	scratch[10] = 0
	scratch[11] = 0
	copy(scratch[catalogNameOffset:length], name)
	return length, nil
}

// catalogDecodeName extracts the name from one catalog record (Rust
// decode_entry): the returned slice aliases the input record and lives
// only as long as it. Zero copies on the hot codec path; keys are only
// compared inside the tree operation that read them.
func catalogDecodeName(record []byte) ([]byte, uint32, error) {
	if len(record) < catalogMinRecord {
		return nil, 0, corrupt("feed catalog record is malformed")
	}
	nameLen := int(record[catalogNameLenOff])
	if int(format.U16(record[0:2])) != catalogRecordBase+nameLen ||
		format.U16(record[2:4]) != 0 ||
		record[9] != 0 || record[10] != 0 || record[11] != 0 {
		return nil, 0, corrupt("feed catalog record is malformed")
	}
	name := record[catalogNameOffset : catalogNameOffset+nameLen]
	if !format.FeedNameValid(name) {
		return nil, 0, corrupt("feed catalog name is invalid")
	}
	return name, format.U32(record[catalogIndexOffset : catalogIndexOffset+4]), nil
}

// nameCodec is the catalog name tree (Rust NameCodec): variable keys (the
// name bytes, lexicographic order) and variable full-record branch cells
// that carry the child in the index slot.
type nameCodec struct{}

func (nameCodec) BranchType() format.PageType { return format.PageTypeCatalogNameBranch }
func (nameCodec) LeafType() format.PageType   { return format.PageTypeCatalogNameLeaf }
func (nameCodec) Aux() uint32                 { return 0 }
func (nameCodec) KeySize() int                { return 0 }
func (nameCodec) LeafSize() int               { return 0 }
func (nameCodec) MaxBranchCell() int          { return catalogMaxRecord }
func (nameCodec) MaxLeafCell() int            { return catalogMaxRecord }

func (nameCodec) LeafRecordBounds() (int, int) {
	return catalogMinRecord, catalogMaxRecord
}

func (nameCodec) BranchRecordBounds() (int, int) {
	return catalogMinRecord, catalogMaxRecord
}

func (nameCodec) WriteBranch(key tree.Key, child uint32, output []byte) (int, error) {
	length := catalogRecordBase + len(key.Bytes())
	if length > len(output) || length > catalogMaxRecord {
		return 0, corrupt("feed catalog record buffer is too small")
	}
	// The record header fields are written one by one (Rust
	// write_branch): the length prefix, the zero word, the child, the
	// name length, and the three zero bytes.
	format.PutU16(output[0:2], uint16(length))
	format.PutU16(output[2:4], 0)
	format.PutU32(output[catalogIndexOffset:catalogIndexOffset+4], child)
	output[catalogNameLenOff] = byte(len(key.Bytes()))
	output[9] = 0
	output[10] = 0
	output[11] = 0
	copy(output[catalogNameOffset:length], key.Bytes())
	return length, nil
}

func (nameCodec) ReadBranchChild(cell []byte) (uint32, error) {
	if len(cell) < catalogMinRecord {
		return 0, corrupt("feed catalog record is malformed")
	}
	child := format.U32(cell[catalogIndexOffset : catalogIndexOffset+4])
	if !format.PageNumberValid(child, format.MaxPageCount) {
		return 0, corrupt("catalog child out of range")
	}
	return child, nil
}

func (nameCodec) ReadKey(cell []byte, _ uint16) (tree.Key, error) {
	name, _, err := catalogDecodeName(cell)
	if err != nil {
		return tree.Key{}, err
	}
	return tree.VarKey(name), nil
}

// CompareKey compares one variable catalog name key without building a
// Key (Rust NameCodec + byte-string Ord). The name is decoded as a view
// of the caller's cell, so a probe never allocates.
func (nameCodec) CompareKey(cell []byte, _ uint16, target tree.Key) (int, error) {
	name, _, err := catalogDecodeName(cell)
	if err != nil {
		return 0, err
	}
	return bytes.Compare(name, target.Bytes()), nil
}

func (nameCodec) ReadLeaf(cell []byte) (format.CatalogNameRecord, error) {
	name, index, err := catalogDecodeName(cell)
	if err != nil {
		return format.CatalogNameRecord{}, err
	}
	return format.CatalogNameRecord{FeedIndex: index, Name: name}, nil
}

func (nameCodec) WriteKey(key tree.Key, output []byte) {
	copy(output, key.Bytes())
}

// indexCodec is the catalog index tree (Rust IndexCodec): fixed u32 keys,
// variable leaf records, fixed 8-byte branch cells.
type indexCodec struct{}

func (indexCodec) BranchType() format.PageType { return format.PageTypeCatalogIndexBranch }
func (indexCodec) LeafType() format.PageType   { return format.PageTypeCatalogIndexLeaf }
func (indexCodec) Aux() uint32                 { return 0 }
func (indexCodec) KeySize() int                { return 4 }
func (indexCodec) LeafSize() int               { return 0 }
func (indexCodec) MaxBranchCell() int          { return catalogIndexBranch }
func (indexCodec) MaxLeafCell() int            { return catalogMaxRecord }

func (indexCodec) LeafRecordBounds() (int, int) {
	return catalogMinRecord, catalogMaxRecord
}

// BranchRecordBounds reports the fixed 8-byte index branch cells; the
// tree core only consults it for KeySize == 0 codecs, so the value is
// the branch bound for completeness (Rust codecs implement the whole
// trait).
func (indexCodec) BranchRecordBounds() (int, int) {
	return catalogIndexBranch, catalogIndexBranch
}

func (indexCodec) WriteBranch(key tree.Key, child uint32, output []byte) (int, error) {
	format.PutU32(output, uint32(key.Hi))
	format.PutU32(output[4:], child)
	return catalogIndexBranch, nil
}

func (indexCodec) ReadBranchChild(cell []byte) (uint32, error) {
	if len(cell) < catalogIndexBranch {
		return 0, corrupt("feed index branch record is malformed")
	}
	return format.U32(cell[4:]), nil
}

func (indexCodec) ReadKey(cell []byte, level uint16) (tree.Key, error) {
	if level == 0 {
		if len(cell) < catalogMinRecord {
			return tree.Key{}, corrupt("feed catalog record is malformed")
		}
		return tree.Key{Hi: uint64(format.U32(cell[catalogIndexOffset : catalogIndexOffset+4]))}, nil
	}
	if len(cell) < catalogIndexBranch {
		return tree.Key{}, corrupt("feed index branch record is malformed")
	}
	return tree.Key{Hi: uint64(format.U32(cell[0:4]))}, nil
}

// CompareKey compares one cell key without materializing a Key (Rust
// IndexCodec + u32 Ord). The codec has variable leaf records, so the
// level-0 key lives at catalogIndexOffset inside the record; branch
// cells carry the u32 key prefix.
func (indexCodec) CompareKey(cell []byte, level uint16, target tree.Key) (int, error) {
	if level == 0 {
		if len(cell) < catalogMinRecord {
			return 0, corrupt("feed catalog record is malformed")
		}
		return cmpU32(format.U32(cell[catalogIndexOffset:catalogIndexOffset+4]), uint32(target.Hi)), nil
	}
	if len(cell) < catalogIndexBranch {
		return 0, corrupt("feed index branch record is malformed")
	}
	return cmpU32(format.U32(cell[0:4]), uint32(target.Hi)), nil
}

func (indexCodec) ReadLeaf(cell []byte) (format.CatalogNameRecord, error) {
	name, index, err := catalogDecodeName(cell)
	if err != nil {
		return format.CatalogNameRecord{}, err
	}
	return format.CatalogNameRecord{FeedIndex: index, Name: name}, nil
}

func (indexCodec) WriteKey(key tree.Key, output []byte) {
	format.PutU32(output, uint32(key.Hi))
}

// insertCatalogEntry inserts one entry into both catalog indexes, name
// first, then index; either duplicate is a structural corruption (Rust
// feed_catalog::mutation::insert). Every committed page COW-ed by the
// inserts is retired through the store; on a fresh output store no page is
// committed yet, so retirement stays empty. The name root and index root
// always move together on success.
func insertCatalogEntry(store tree.RetiringStore, scratch []byte, nameRoot, indexRoot *uint32, name string, index uint32) error {
	length, err := encodeCatalogRecord(name, index, scratch)
	if err != nil {
		return err
	}
	var retired tree.RetiredPages
	var changed bool
	retired, changed, err = tree.Insert(nameCodec{}, store, nameRoot, scratch[:length], retired)
	if err != nil {
		return err
	}
	if !changed {
		return corrupt("feed name already exists")
	}
	if err := store.RetirePages(retired); err != nil {
		return err
	}
	retired = tree.RetiredPages{}
	retired, changed, err = tree.Insert(indexCodec{}, store, indexRoot, scratch[:length], retired)
	if err != nil {
		return err
	}
	if !changed {
		return corrupt("feed index already exists")
	}
	if err := store.RetirePages(retired); err != nil {
		return err
	}
	work.CatalogIntern(1)
	return nil
}

// deleteCatalogEntry removes one feed from both catalog indexes, name
// first, then index; either deletion is structural because the entry was
// proven current (Rust feed_catalog::mutation::delete). Every committed
// page COW-ed by the deletions is retired through the store.
func deleteCatalogEntry(store tree.RetiringStore, nameRoot, indexRoot *uint32, entry FeedEntry) error {
	var retired tree.RetiredPages
	var err error
	retired, err = tree.DeleteExisting(nameCodec{}, store, nameRoot, tree.VarKey([]byte(entry.Name)), retired)
	if err != nil {
		return err
	}
	if err := store.RetirePages(retired); err != nil {
		return err
	}
	retired = tree.RetiredPages{}
	retired, err = tree.DeleteExisting(indexCodec{}, store, indexRoot, tree.Key{Hi: uint64(entry.Index)}, retired)
	if err != nil {
		return err
	}
	return store.RetirePages(retired)
}

// renameCatalogEntry renames the name record of one current feed,
// keeping the index record at the same index key (Rust
// feed_catalog::mutation::rename): the old name is deleted, the renamed
// record is inserted under the new name, and the index tree must already
// hold the record (the rename never moves the index).
func renameCatalogEntry(store tree.RetiringStore, scratch []byte, nameRoot, indexRoot *uint32, old FeedEntry, newName string) error {
	var retired tree.RetiredPages
	var err error
	retired, err = tree.DeleteExisting(nameCodec{}, store, nameRoot, tree.VarKey([]byte(old.Name)), retired)
	if err != nil {
		return err
	}
	if err := store.RetirePages(retired); err != nil {
		return err
	}
	renamed := FeedEntry{Name: newName, Index: old.Index}
	length, err := encodeCatalogRecord(renamed.Name, renamed.Index, scratch)
	if err != nil {
		return err
	}
	retired = tree.RetiredPages{}
	retired, changed, err := tree.Insert(nameCodec{}, store, nameRoot, scratch[:length], retired)
	if err != nil {
		return err
	}
	if !changed {
		return corrupt("renamed feed name already exists")
	}
	if err := store.RetirePages(retired); err != nil {
		return err
	}
	retired = tree.RetiredPages{}
	retired, changed, err = tree.Insert(indexCodec{}, store, indexRoot, scratch[:length], retired)
	if err != nil {
		return err
	}
	if changed {
		return corrupt("renamed feed index was missing")
	}
	return store.RetirePages(retired)
}
