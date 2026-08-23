// Empty transaction-1 main-image operations (Rust database_file.rs
// EmptySpec::live + empty_meta + write_empty). The live create writes
// the identical txn-1 meta on both meta pages through a read-write
// mapping of the created main descriptor, flushes the pair, and syncs
// the file. The codec authority is format.Meta.EncodeMapped; this file
// owns the empty-image driver (writer/create.go mirrors the same Rust
// function over its mapping.Create flow, recorded in the SOW).

package live

import (
	"os"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/mapping"
)

// emptySpec is the txn-1 live empty image (Rust EmptySpec::live:
// transaction 1, zero feed index limit).
type emptySpec struct {
	addressFamily uint8
	valueKind     uint8
	structureKind uint8
	valueTag      [16]byte
	databaseID    [16]byte
	commitNonce   [16]byte
}

// emptyMeta mirrors Rust database_file.rs empty_meta exactly: two
// pages, all roots and counts zero, the membership id limit one for
// membership/structured kinds, and the structure id limit one for the
// structured kind.
func emptyMeta(spec emptySpec) format.Meta {
	m := format.Meta{
		AddressFamily: spec.addressFamily,
		ValueKind:     spec.valueKind,
		StructureKind: spec.structureKind,
		ValueTag:      spec.valueTag,
		DatabaseID:    spec.databaseID,
		TxnID:         1,
		CommitNonce:   spec.commitNonce,
		PageCount:     2,
	}
	if spec.valueKind == format.ValueKindMembership || spec.valueKind == format.ValueKindStructured {
		m.MembershipIDLimit = 1
	}
	if spec.valueKind == format.ValueKindStructured {
		m.StructureIDLimit = 1
	}
	return m
}

// writeEmpty sizes the main file to two pages, encodes the identical
// meta on both meta pages through the mapping, flushes the pair, and
// syncs the file (Rust write_empty: set_len, read-write view,
// encode_mapped x2, flush_range, sync_all).
func writeEmpty(f *os.File, spec emptySpec) error {
	if err := f.Truncate(2 * format.PageSize); err != nil {
		return &format.Error{Code: format.CodeIO, Detail: "main resize: " + err.Error()}
	}
	m, err := mapping.MapFile(f, 2*format.PageSize, true)
	if err != nil {
		return err
	}
	defer m.Close()
	p0, err := m.Page(0)
	if err != nil {
		return err
	}
	p1, err := m.Page(1)
	if err != nil {
		return err
	}
	meta := emptyMeta(spec)
	if err := meta.EncodeMapped(p0); err != nil {
		return err
	}
	if err := meta.EncodeMapped(p1); err != nil {
		return err
	}
	if err := m.FlushRange(0, 2*format.PageSize); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return &format.Error{Code: format.CodeIO, Detail: "main sync: " + err.Error()}
	}
	return nil
}
