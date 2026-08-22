// Variable-codec tree tests: variable leaf records (fixed-key codecs like
// the catalog index tree and membership ID tree) and variable keys plus
// full-record branch cells (like the catalog name tree). The cell layout
// mirrors feed_catalog/codec.rs: a u16 record length prefix, a u32 key
// (or the child page in branch records) and a payload, with the branch
// child carried inside the record.

package tree

import (
	"bytes"
	"fmt"
	"sort"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
)

// varRecordCodec is a fixed-key variable-record codec (catalog index tree
// and membership ID tree shape): 4-byte keys, records with a u16 length
// prefix, the u32 key at offset 2, and a payload. Branches are fixed 8B
// cells.
type varRecordCodec struct{}

func (varRecordCodec) BranchType() format.PageType { return format.PageTypeRangeBranch }
func (varRecordCodec) LeafType() format.PageType   { return format.PageTypeRangeLeaf }
func (varRecordCodec) Aux() uint32                 { return 0 }
func (varRecordCodec) KeySize() int                { return 4 }
func (varRecordCodec) LeafSize() int               { return 0 }
func (varRecordCodec) MaxBranchCell() int          { return 8 }
func (varRecordCodec) MaxLeafCell() int            { return 512 }

func (varRecordCodec) LeafRecordBounds() (int, int) {
	return 7, 512
}

func (varRecordCodec) BranchRecordBounds() (int, int) {
	return 8, 8
}

func (varRecordCodec) WriteBranch(key Key, child uint32, output []byte) (int, error) {
	format.PutU32(output, uint32(key.Hi))
	format.PutU32(output[4:], child)
	return 8, nil
}

func (varRecordCodec) ReadBranchChild(cell []byte) (uint32, error) {
	if len(cell) < 8 {
		return 0, corrupt("test branch record is truncated")
	}
	return format.U32(cell[4:]), nil
}

func (varRecordCodec) ReadKey(cell []byte, level uint16) (Key, error) {
	if level == 0 {
		if len(cell) < 6 {
			return Key{}, corrupt("test record is truncated")
		}
		return Key{Hi: uint64(format.U32(cell[2:]))}, nil
	}
	if len(cell) < 4 {
		return Key{}, corrupt("test branch record is truncated")
	}
	return Key{Hi: uint64(format.U32(cell))}, nil
}

func (varRecordCodec) ReadLeaf(cell []byte) (string, error) {
	if len(cell) < 7 || int(format.U16(cell)) != len(cell) {
		return "", corrupt("test leaf record is invalid")
	}
	return fmt.Sprintf("%x", cell[6:]), nil
}

func (varRecordCodec) WriteKey(key Key, output []byte) {
	format.PutU32(output, uint32(key.Hi))
}

// varNameCodec is a variable-key variable-record codec (catalog name tree
// shape): records carry a u16 length prefix, the u32 child/index slot at
// offset 2, and the name (the key) at offset 6. Branches are full records
// with the child in the index slot.
type varNameCodec struct{}

func (varNameCodec) BranchType() format.PageType { return format.PageTypeCatalogNameBranch }
func (varNameCodec) LeafType() format.PageType   { return format.PageTypeCatalogNameLeaf }
func (varNameCodec) Aux() uint32                 { return 0 }
func (varNameCodec) KeySize() int                { return 0 }
func (varNameCodec) LeafSize() int               { return 0 }
func (varNameCodec) MaxBranchCell() int          { return 128 }
func (varNameCodec) MaxLeafCell() int            { return 128 }

func (varNameCodec) LeafRecordBounds() (int, int) {
	return 7, 128
}

func (varNameCodec) BranchRecordBounds() (int, int) {
	return 7, 128
}

func (varNameCodec) WriteBranch(key Key, child uint32, output []byte) (int, error) {
	name := key.Bytes()
	length := 6 + len(name)
	if length > 128 {
		return 0, corrupt("test name record is too large")
	}
	format.PutU16(output, uint16(length))
	format.PutU32(output[2:], child)
	copy(output[6:length], name)
	return length, nil
}

func (varNameCodec) ReadBranchChild(cell []byte) (uint32, error) {
	if len(cell) < 6 {
		return 0, corrupt("test name branch record is truncated")
	}
	return format.U32(cell[2:]), nil
}

func (varNameCodec) ReadKey(cell []byte, _ uint16) (Key, error) {
	if len(cell) < 7 {
		return Key{}, corrupt("test name record is truncated")
	}
	if int(format.U16(cell)) != len(cell) {
		return Key{}, corrupt("test name record length is invalid")
	}
	return VarKey(append([]byte(nil), cell[6:]...)), nil
}

func (varNameCodec) ReadLeaf(cell []byte) (string, error) {
	if len(cell) < 7 || int(format.U16(cell)) != len(cell) {
		return "", corrupt("test name leaf record is invalid")
	}
	return "name:" + string(cell[6:]), nil
}

func (varNameCodec) WriteKey(key Key, output []byte) {
	copy(output, key.Bytes())
}

func varRecord(key uint32, payload byte) []byte {
	cell := make([]byte, 6+int(payload))
	format.PutU16(cell, uint16(len(cell)))
	format.PutU32(cell[2:], key)
	for i := 6; i < len(cell); i++ {
		cell[i] = payload
	}
	return cell
}

func nameRecord(name string, child uint32) []byte {
	cell := make([]byte, 6+len(name))
	format.PutU16(cell, uint16(len(cell)))
	format.PutU32(cell[2:], child)
	copy(cell[6:], name)
	return cell
}

// walkTree is the test-only whole-tree enumeration: the visit callback
// receives every leaf record in order, resolved through the store.
func walkTree(codec Codec[string], m *memoryStore, root uint32, visit func(cell []byte, header *Header, index int) error) error {
	if root == 0 {
		return nil
	}
	var descend func(pageNumber uint32, expected *uint16) error
	descend = func(pageNumber uint32, expected *uint16) error {
		page, err := m.Inspect(pageNumber)
		if err != nil {
			return err
		}
		h, err := parse(codec, page, m.TargetTxn(), expected)
		if err != nil {
			return err
		}
		header := &h
		if header.Level != 0 {
			for index := 0; index < int(header.ItemCount); index++ {
				page, err := m.Inspect(pageNumber)
				if err != nil {
					return err
				}
				child, err := branchChild(codec, page, header, index, m.PageLimit())
				if err != nil {
					return err
				}
				next := header.Level - 1
				if err := descend(child, &next); err != nil {
					return err
				}
			}
			return nil
		}
		for index := 0; index < int(header.ItemCount); index++ {
			page, err := m.Inspect(pageNumber)
			if err != nil {
				return err
			}
			cell, err := codecCell(codec, page, header, index)
			if err != nil {
				return err
			}
			if err := visit(cell, header, index); err != nil {
				return err
			}
		}
		return nil
	}
	return descend(root, nil)
}

// TestVariableRecordInsertOrdered pins the fixed-key variable-leaf insert
// path across many splits: the tree core must route leaf cells through the
// codec record accessor and keep the index tree ordering.
func TestVariableRecordInsertOrdered(t *testing.T) {
	m := newMemoryStore()
	codec := varRecordCodec{}
	root := uint32(0)
	const count = 200
	for i := 0; i < count; i++ {
		retired, changed, err := Insert(codec, m, &root, varRecord(uint32(i), byte(8+i%40)), RetiredPages{})
		if err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
		if retired.Len() != 0 {
			t.Fatalf("insert %d retired pages", i)
		}
		if !changed {
			t.Fatalf("insert %d reported no change", i)
		}
	}
	if root == 0 {
		t.Fatal("no root built")
	}
	// Enumerate in order and verify every record is present exactly once.
	seen := 0
	last := -1
	err := walkTree(codec, m, root, func(cell []byte, _ *Header, _ int) error {
		if len(cell) < 6 || int(format.U16(cell)) != len(cell) {
			return corrupt("test record is invalid")
		}
		key := int(format.U32(cell[2:]))
		if key <= last {
			t.Fatalf("records out of order: %d after %d", key, last)
		}
		last = key
		seen++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if seen != count {
		t.Fatalf("walked %d records, want %d", seen, count)
	}
}

// TestVariableNameInsertOrdered pins the variable-key path with
// full-record branch cells: names insert in lexicographic order, and every
// split keeps branch records (name + child in the index slot) valid.
func TestVariableNameInsertOrdered(t *testing.T) {
	m := newMemoryStore()
	codec := varNameCodec{}
	root := uint32(0)
	retired := RetiredPages{}
	names := make([]string, 0, 120)
	for i := 0; i < 120; i++ {
		names = append(names, fmt.Sprintf("feed-%03d", i))
	}
	sort.Strings(names)
	for _, name := range names {
		next, changed, err := Insert(codec, m, &root, nameRecord(name, 0), RetiredPages{})
		if err != nil {
			t.Fatalf("insert %q: %v", name, err)
		}
		if next.Len() != 0 {
			t.Fatalf("insert %q retired pages", name)
		}
		if !changed {
			t.Fatalf("insert %q reported no change", name)
		}
	}
	for _, name := range names {
		key := VarKey([]byte(name))
		leaf, _, err := PrivatePath(codec, m, &root, key, RetiredPages{})
		if err != nil {
			t.Fatalf("path %q: %v", name, err)
		}
		if !leaf.Exists {
			t.Fatalf("name %q not found", name)
		}
		if retired.Len() != 0 {
			t.Fatalf("read-only descent retired %d pages", retired.Len())
		}
	}
	// Enumerate in lexicographic order.
	last := ""
	err := walkTree(codec, m, root, func(cell []byte, _ *Header, _ int) error {
		name := string(cell[6:])
		if bytes.Compare([]byte(name), []byte(last)) <= 0 {
			t.Fatalf("names out of order: %q after %q", name, last)
		}
		last = name
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestVariableNameInsertSplitFence pins first-fence propagation on the
// variable path: inserting a name before every existing first key walks
// the branch fences up through full-record branch cells.
func TestVariableNameInsertSplitFence(t *testing.T) {
	m := newMemoryStore()
	codec := varNameCodec{}
	root := uint32(0)
	retired := RetiredPages{}
	// Insert a descending series so every insert lands at index 0 and
	// rewrites the root fence.
	for i := 199; i >= 0; i-- {
		name := fmt.Sprintf("feed-%03d", i)
		next, changed, err := Insert(codec, m, &root, nameRecord(name, 0), retired)
		if err != nil {
			t.Fatalf("insert %q: %v", name, err)
		}
		retired = next
		if !changed {
			t.Fatalf("insert %q reported no change", name)
		}
	}
	first, err := FirstKey(codec, m, root, 0)
	if err != nil {
		t.Fatal(err)
	}
	if string(first.Bytes()) != "feed-000" {
		t.Fatalf("first key = %q, want feed-000", first.Bytes())
	}
	seen := 0
	if err := walkTree(codec, m, root, func(cell []byte, _ *Header, _ int) error {
		seen++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if seen != 200 {
		t.Fatalf("walked %d leaves, want 200", seen)
	}
}

// TestVariableRecordReplacement pins requireReplacement and the
// replacement-edit path over variable leaf records.
func TestVariableRecordReplacement(t *testing.T) {
	m := newMemoryStore()
	codec := varRecordCodec{}
	root := uint32(0)
	retired := RetiredPages{}
	if _, _, err := Insert(codec, m, &root, varRecord(7, 3), retired); err != nil {
		t.Fatal(err)
	}
	// The replacement keeps the target key in its first cell (Rust
	// require_replacement: the first cell's key must equal the replaced
	// key) and adds a second, later key.
	if _, err := ReplaceLeafWith(codec, m, &root, Key{Hi: 7}, [][]byte{varRecord(7, 3), varRecord(9, 5)}, retired); err != nil {
		t.Fatal(err)
	}
	keys := []uint32{}
	err := walkTree(codec, m, root, func(cell []byte, _ *Header, _ int) error {
		keys = append(keys, format.U32(cell[2:]))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 2 || keys[0] != 7 || keys[1] != 9 {
		t.Fatalf("replacement keys = %v, want [7 9]", keys)
	}
}

// TestVariableNameAtOrAfter pins the ordered cursor over variable keys:
// Predecessor/AtOrAfter use lowerBoundBy with byte-key comparison on the
// variable path.
func TestVariableNameAtOrAfter(t *testing.T) {
	m := newMemoryStore()
	codec := varNameCodec{}
	root := uint32(0)
	retired := RetiredPages{}
	names := []string{"alpha", "bravo", "charlie", "delta"}
	for _, name := range names {
		if _, _, err := Insert(codec, m, &root, nameRecord(name, 0), retired); err != nil {
			t.Fatal(err)
		}
	}
	value, found, err := AtOrAfter(codec, m, root, VarKey([]byte("charl")))
	if err != nil {
		t.Fatal(err)
	}
	if !found || value != "name:charlie" {
		t.Fatalf("AtOrAfter(charl) = %v, want name:charlie", value)
	}
	// Predecessor returns the key itself when it exists (Rust
	// predecessor: at-or-below, otherwise strictly below).
	value, found, err = Predecessor(codec, m, root, VarKey([]byte("bravo")))
	if err != nil {
		t.Fatal(err)
	}
	if !found || value != "name:bravo" {
		t.Fatalf("Predecessor(bravo) = %v, want name:bravo", value)
	}
	value, found, err = Predecessor(codec, m, root, VarKey([]byte("brian")))
	if err != nil {
		t.Fatal(err)
	}
	if !found || value != "name:bravo" {
		t.Fatalf("Predecessor(brian) = %v, want name:bravo", value)
	}
}
