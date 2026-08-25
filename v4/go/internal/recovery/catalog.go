package recovery

// Reconciliation of the redundant recovery-readable feed catalogs
// (Rust recovery/catalog.rs): one count pass sizes the record table
// from both catalog trees, one recover pass rebuilds the reconciled
// catalog (name and index slots) with the exact counter and envelope
// classes, and the family codecs mirror the Rust feed_catalog codecs.

import (
	"bytes"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/mapping"
	"github.com/firehol/iprange/v4/go/internal/validation"
)

// catalogCount counts the recovery-readable catalog entries (Rust
// catalog::count: both trees into one counter, the acceptance rule is
// the decoded index below the feed limit).
func catalogCount(m *mapping.Mapping, meta format.Meta, pages *pageSet, check func() error) (uint64, error) {
	counter := &leafCounter{meta: meta, overflowDetail: "recovery catalog count", accept: catalogCountAccept}
	if err := scanTree(catalogNameCodec{}, m, meta, meta.CatalogNameRoot, pages, check, counter); err != nil {
		return 0, err
	}
	if err := scanTree(catalogIndexCodec{}, m, meta, meta.CatalogIndexRoot, pages, check, counter); err != nil {
		return 0, err
	}
	return counter.count, nil
}

func catalogCountAccept(meta format.Meta, cell []byte) bool {
	index, _, err := format.DecodeCatalogEntry(cell)
	return err == nil && uint64(index) < meta.FeedIndexLimit
}

// recoverCatalog reconciles the feed catalogs of one source (Rust
// catalog::recover: the name tree then the index tree through one
// builder; the finish folds the accepted and rejected counts).
func recoverCatalog(m *mapping.Mapping, meta format.Meta, pages *pageSet, tables *tableStore, check func() error, rep *reporter) (*catalog, error) {
	builder := newCatalogBuilder(tables)
	events := &catalogEvents{meta: meta, rep: rep, builder: builder, object: catalogNameCodec{}.object()}
	if err := scanTree(catalogNameCodec{}, m, meta, meta.CatalogNameRoot, pages, check, events); err != nil {
		return nil, err
	}
	events.object = catalogIndexCodec{}.object()
	if err := scanTree(catalogIndexCodec{}, m, meta, meta.CatalogIndexRoot, pages, check, events); err != nil {
		return nil, err
	}
	return builder.finish(rep)
}

// catalogNameCodec is the name-tree scan codec (Rust NameCodec: the
// variable name records, the branch record's index field is the child
// page).
type catalogNameCodec struct{}

func (catalogNameCodec) object() validation.ValidationObject { return validation.ObjectCatalogNameTree }
func (catalogNameCodec) branchType() format.PageType         { return format.PageTypeCatalogNameBranch }
func (catalogNameCodec) leafType() format.PageType           { return format.PageTypeCatalogNameLeaf }
func (catalogNameCodec) aux() uint32                         { return 0 }
func (catalogNameCodec) branchLayout() format.CellLayout {
	return format.VariableLayout(format.MinCatalogNameRecord, format.MaxCatalogNameRecord)
}
func (catalogNameCodec) leafLayout() format.CellLayout {
	return format.VariableLayout(format.MinCatalogNameRecord, format.MaxCatalogNameRecord)
}
func (catalogNameCodec) branchInvalid() validation.ValidationReason {
	return validation.ReasonCatalogInvalid
}
func (catalogNameCodec) leafInvalid() validation.ValidationReason {
	return validation.ReasonCatalogInvalid
}
func (catalogNameCodec) decodeBranch(cell []byte) ([]byte, uint32, bool) {
	child, name, err := format.DecodeCatalogEntry(cell)
	if err != nil {
		return nil, 0, false
	}
	return name, child, true
}
func (catalogNameCodec) decodeLeafKey(cell []byte) ([]byte, bool) {
	_, name, err := format.DecodeCatalogEntry(cell)
	if err != nil {
		return nil, false
	}
	return name, true
}

// lessTreeName orders one name key like the Rust FeedName Ord: valid
// names are NUL-free, so the fixed-array order equals the byte order.
func lessTreeName(a, b []byte) bool {
	return bytes.Compare(a, b) < 0
}

func equalTreeName(a, b []byte) bool {
	return bytes.Equal(a, b)
}

func (catalogNameCodec) less(a, b []byte) bool  { return lessTreeName(a, b) }
func (catalogNameCodec) equal(a, b []byte) bool { return equalTreeName(a, b) }

// catalogIndexCodec is the index-tree scan codec (Rust IndexCodec: the
// fixed 8-byte branch cells and the variable name-record leaves keyed
// by the feed index).
type catalogIndexCodec struct{}

func (catalogIndexCodec) object() validation.ValidationObject {
	return validation.ObjectCatalogIndexTree
}
func (catalogIndexCodec) branchType() format.PageType { return format.PageTypeCatalogIndexBranch }
func (catalogIndexCodec) leafType() format.PageType   { return format.PageTypeCatalogIndexLeaf }
func (catalogIndexCodec) aux() uint32                 { return 0 }
func (catalogIndexCodec) branchLayout() format.CellLayout {
	return format.FixedLayout(format.CatalogIndexBranchSize)
}
func (catalogIndexCodec) leafLayout() format.CellLayout {
	return format.VariableLayout(format.MinCatalogNameRecord, format.MaxCatalogNameRecord)
}
func (catalogIndexCodec) branchInvalid() validation.ValidationReason {
	return validation.ReasonCatalogInvalid
}
func (catalogIndexCodec) leafInvalid() validation.ValidationReason {
	return validation.ReasonCatalogInvalid
}
func (catalogIndexCodec) decodeBranch(cell []byte) (uint32, uint32, bool) {
	first, child, err := format.DecodeCatalogIndexBranchFields(cell)
	if err != nil {
		return 0, 0, false
	}
	return first, child, true
}
func (catalogIndexCodec) decodeLeafKey(cell []byte) (uint32, bool) {
	index, _, err := format.DecodeCatalogEntry(cell)
	if err != nil {
		return 0, false
	}
	return index, true
}
func (catalogIndexCodec) less(a, b uint32) bool  { return a < b }
func (catalogIndexCodec) equal(a, b uint32) bool { return a == b }

// catalogEvents wires one catalog tree scan into the reporter and the
// table builder (Rust catalog::Events: the page and envelope events
// stream, every leaf counts one examined entry, an undecodable entry or
// an index at or above the feed limit rejects with the exact classes).
type catalogEvents struct {
	meta    format.Meta
	rep     *reporter
	builder *catalogBuilder
	object  validation.ValidationObject
}

func (e *catalogEvents) pageAccepted() error {
	return e.rep.pageAccepted()
}

func (e *catalogEvents) pageRejected(ioUnreadable bool) error {
	return e.rep.pageRejected(ioUnreadable)
}

func (e *catalogEvents) unknown(reason validation.ValidationReason, object validation.ValidationObject, page *uint32) error {
	return e.rep.emitPageUnknown(reason, object, page)
}

func (e *catalogEvents) leaf(page uint32, index int, cell []byte, ok bool) error {
	if err := e.rep.catalogExamined(); err != nil {
		return err
	}
	if !ok {
		return e.rep.catalogRejected(1)
	}
	entry, err := decodeCatalogCell(cell)
	if err != nil {
		return e.rep.catalogRejected(1)
	}
	if uint64(entry.index) >= e.meta.FeedIndexLimit {
		if err := e.rep.catalogRejected(1); err != nil {
			return err
		}
		return e.rep.emitPageUnknown(validation.ReasonCatalogInvalid, e.object, &page)
	}
	return e.builder.push(entry, e.rep)
}

// decodeCatalogCell parses one recovered catalog entry (Rust
// feed_catalog::decode_entry: the record shape and grammar; the index
// field is raw).
func decodeCatalogCell(cell []byte) (catalogFeed, error) {
	index, name, err := format.DecodeCatalogEntry(cell)
	if err != nil {
		return catalogFeed{}, err
	}
	return catalogFeed{name: name, index: index}, nil
}
