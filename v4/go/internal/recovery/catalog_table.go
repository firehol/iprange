package recovery

// Fixed-record catalog reconciliation (Rust recovery/catalog_table.rs):
// the record table and the name and index slot tables inside the
// recovery backing store, the reconciliation of redundant entries with
// the conflict envelopes, and the accepted-records proof. The name and
// index slots use the exact wire offsets of the Rust slots.

import (
	"bytes"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/validation"
)

const (
	slotOccupiedOffset     = 0
	slotConflictOffset     = 1
	slotValueOffset        = 4
	slotRecordOffset       = 8
	nameOccurrencesOffset  = 16
	recordNameLengthOffset = 0
	recordIndexOffset      = 4
	recordNameOffset       = 8
)

// catalog is one recovered feed catalog (Rust Catalog): the store
// regions and the examined record count.
type catalog struct {
	records    tableRegion
	names      tableRegion
	indexes    tableRegion
	recordsLen uint64
}

// catalogFeed is one recovered catalog entry (Rust FeedEntry): the
// name view and the feed index. The name view aliases its source
// (the scanned source cell or the table store arena), never a
// per-record buffer, so decoding one record allocates nothing.
type catalogFeed struct {
	name  []byte
	index uint32
}

// catalogBuilder reconciles the feed catalog of one source (Rust
// catalog_table::Builder).
type catalogBuilder struct {
	tables  *tableStore
	catalog catalog
}

// newCatalogBuilder starts one catalog builder over the store layout
// (Rust Builder::new).
func newCatalogBuilder(tables *tableStore) *catalogBuilder {
	layout := tables.layout
	return &catalogBuilder{
		tables: tables,
		catalog: catalog{
			records: layout.catalogRecords,
			names:   layout.catalogNames,
			indexes: layout.catalogIndexes,
		},
	}
}

// push stores one catalog entry and its slots (Rust Builder::push: the
// record bound is the candidate-changed class).
func (b *catalogBuilder) push(entry catalogFeed, rep *reporter) error {
	if b.catalog.recordsLen == b.catalog.records.slots {
		return candidateChangedError()
	}
	var record [catalogRecordSize]byte
	encodeCatalogRecord(entry, record[:])
	recordIndex := b.catalog.recordsLen
	if err := b.tables.write(b.catalog.records, recordIndex, record[:]); err != nil {
		return err
	}
	if err := b.insertName(entry, recordIndex, rep); err != nil {
		return err
	}
	if err := b.insertIndex(entry, recordIndex, rep); err != nil {
		return err
	}
	b.catalog.recordsLen++
	return nil
}

// finish folds the accepted and rejected record counts (Rust
// Builder::finish).
func (b *catalogBuilder) finish(rep *reporter) (*catalog, error) {
	accepted, err := b.catalog.acceptedSourceRecords(b.tables)
	if err != nil {
		return nil, err
	}
	if err := rep.catalogRejected(b.catalog.recordsLen - accepted); err != nil {
		return nil, err
	}
	if err := rep.catalogAccepted(accepted); err != nil {
		return nil, err
	}
	return &b.catalog, nil
}

// insertName stores the name slot of one entry (Rust
// Builder::insert_name: the occurrence count, the first-record proof,
// and the name conflict envelope).
func (b *catalogBuilder) insertName(entry catalogFeed, recordIndex uint64, rep *reporter) error {
	slotIndex, slot, err := b.catalog.nameSlot(b.tables, entry.name)
	if err != nil {
		return err
	}
	if slot[slotOccupiedOffset] == 0 {
		slot[slotOccupiedOffset] = 1
		format.PutU64(slot[slotRecordOffset:nameOccurrencesOffset], recordIndex)
		format.PutU64(slot[nameOccurrencesOffset:catalogNameSlotSize], 1)
	} else {
		occurrences, ok := checkedAdd(format.U64(slot[nameOccurrencesOffset:catalogNameSlotSize]), 1)
		if !ok {
			return overflowError("recovery catalog occurrences")
		}
		format.PutU64(slot[nameOccurrencesOffset:catalogNameSlotSize], occurrences)
		first, err := b.catalog.record(b.tables, format.U64(slot[slotRecordOffset:slotRecordOffset+8]))
		if err != nil {
			return err
		}
		if first.index != entry.index && slot[slotConflictOffset] == 0 {
			slot[slotConflictOffset] = 1
			if err := rep.emitPageUnknown(validation.ReasonCatalogInvalid, validation.ObjectCatalogNameTree, nil); err != nil {
				return err
			}
		}
	}
	return b.tables.write(b.catalog.names, slotIndex, slot[:])
}

// insertIndex stores the index slot of one entry (Rust
// Builder::insert_index: the index value proof and the index conflict
// envelope).
func (b *catalogBuilder) insertIndex(entry catalogFeed, recordIndex uint64, rep *reporter) error {
	slotIndex, slot, err := b.catalog.indexSlot(b.tables, entry.index)
	if err != nil {
		return err
	}
	if slot[slotOccupiedOffset] == 0 {
		slot[slotOccupiedOffset] = 1
		format.PutU32(slot[slotValueOffset:slotRecordOffset], entry.index)
		format.PutU64(slot[slotRecordOffset:catalogIndexSlotSize], recordIndex)
	} else {
		first, err := b.catalog.record(b.tables, format.U64(slot[slotRecordOffset:slotRecordOffset+8]))
		if err != nil {
			return err
		}
		if !bytes.Equal(first.name, entry.name) && slot[slotConflictOffset] == 0 {
			slot[slotConflictOffset] = 1
			if err := rep.emitPageUnknown(validation.ReasonCatalogInvalid, validation.ObjectCatalogIndexTree, nil); err != nil {
				return err
			}
		}
	}
	return b.tables.write(b.catalog.indexes, slotIndex, slot[:])
}

// nameSlot returns the existing or empty name slot of one name (Rust
// name_slot: a full table is the Corrupt class).
func (c *catalog) nameSlot(tables *tableStore, name []byte) (uint64, [catalogNameSlotSize]byte, error) {
	if index, slot, found, err := probeName(tables, c, name, false); err != nil || found {
		return index, slot, err
	}
	index, slot, found, err := probeName(tables, c, name, true)
	if err != nil {
		return 0, slot, err
	}
	if !found {
		return 0, slot, corruptError("recovery catalog name table is full")
	}
	return index, slot, nil
}

// indexSlot returns the existing or empty index slot of one index
// (Rust index_slot).
func (c *catalog) indexSlot(tables *tableStore, value uint32) (uint64, [catalogIndexSlotSize]byte, error) {
	if index, slot, found, err := probeIndex(tables, c, value, false); err != nil || found {
		return index, slot, err
	}
	index, slot, found, err := probeIndex(tables, c, value, true)
	if err != nil {
		return 0, slot, err
	}
	if !found {
		return 0, slot, corruptError("recovery catalog index table is full")
	}
	return index, slot, nil
}

// probeName probes one name slot (Rust probe_name: the occupied-or-
// match acceptance; empty probes return the empty slot).
func probeName(tables *tableStore, catalog *catalog, name []byte, empty bool) (uint64, [catalogNameSlotSize]byte, bool, error) {
	var slot [catalogNameSlotSize]byte
	if catalog.names.slots == 0 {
		return 0, slot, false, nil
	}
	mask := catalog.names.slots - 1
	index := hashBytes(name) & mask
	for probe := uint64(0); probe < catalog.names.slots; probe++ {
		if err := tables.read(catalog.names, index, slot[:]); err != nil {
			return 0, slot, false, err
		}
		if slot[slotOccupiedOffset] == 0 {
			if empty {
				return index, slot, true, nil
			}
			return 0, slot, false, nil
		}
		entry, err := catalog.record(tables, format.U64(slot[slotRecordOffset:slotRecordOffset+8]))
		if err != nil {
			return 0, slot, false, err
		}
		if bytes.Equal(entry.name, name) {
			if !empty {
				return index, slot, true, nil
			}
			return 0, slot, false, nil
		}
		index = (index + 1) & mask
	}
	return 0, slot, false, nil
}

// probeIndex probes one index slot (Rust probe_index).
func probeIndex(tables *tableStore, catalog *catalog, value uint32, empty bool) (uint64, [catalogIndexSlotSize]byte, bool, error) {
	var slot [catalogIndexSlotSize]byte
	if catalog.indexes.slots == 0 {
		return 0, slot, false, nil
	}
	mask := catalog.indexes.slots - 1
	index := hashU32(value) & mask
	for probe := uint64(0); probe < catalog.indexes.slots; probe++ {
		if err := tables.read(catalog.indexes, index, slot[:]); err != nil {
			return 0, slot, false, err
		}
		if slot[slotOccupiedOffset] == 0 {
			if empty {
				return index, slot, true, nil
			}
			return 0, slot, false, nil
		}
		if format.U32(slot[slotValueOffset:slotRecordOffset]) == value {
			if !empty {
				return index, slot, true, nil
			}
			return 0, slot, false, nil
		}
		index = (index + 1) & mask
	}
	return 0, slot, false, nil
}

// forEach streams every accepted source record (Rust Catalog::for_each:
// the name-slot first-record proof then the accepted-slot proof).
func (c *catalog) forEach(tables *tableStore, emit func(catalogFeed) error) error {
	for recordIndex := uint64(0); recordIndex < c.recordsLen; recordIndex++ {
		entry, err := c.record(tables, recordIndex)
		if err != nil {
			return err
		}
		_, slot, found, err := probeName(tables, c, entry.name, false)
		if err != nil {
			return err
		}
		if !found {
			continue
		}
		if format.U64(slot[slotRecordOffset:slotRecordOffset+8]) != recordIndex {
			continue
		}
		accepted, _, ok, err := c.acceptedNameSlot(tables, &slot)
		if err != nil {
			return err
		}
		if ok {
			if err := emit(accepted); err != nil {
				return err
			}
		}
	}
	return nil
}

// contains proves one index through both slot tables (Rust
// Catalog::contains: an accepted chain from the index slot to the name
// slot).
func (c *catalog) contains(tables *tableStore, index uint32) (bool, error) {
	_, slot, found, err := probeIndex(tables, c, index, false)
	if err != nil || !found {
		return false, err
	}
	if slot[slotConflictOffset] != 0 {
		return false, nil
	}
	entry, err := c.record(tables, format.U64(slot[slotRecordOffset:slotRecordOffset+8]))
	if err != nil {
		return false, err
	}
	_, nameSlot, found, err := probeName(tables, c, entry.name, false)
	if err != nil || !found {
		return false, err
	}
	_, _, ok, err := c.acceptedNameSlot(tables, &nameSlot)
	return ok, err
}

// acceptedSourceRecords sums the accepted records over the name slots
// (Rust accepted_source_records: the occurrence counts of the accepted
// slots).
func (c *catalog) acceptedSourceRecords(tables *tableStore) (uint64, error) {
	var accepted uint64
	var slot [catalogNameSlotSize]byte
	for slotIndex := uint64(0); slotIndex < c.names.slots; slotIndex++ {
		if err := tables.read(c.names, slotIndex, slot[:]); err != nil {
			return 0, err
		}
		_, occurrences, ok, err := c.acceptedNameSlot(tables, &slot)
		if err != nil {
			return 0, err
		}
		if !ok {
			continue
		}
		next, ok := checkedAdd(accepted, occurrences)
		if !ok {
			return 0, overflowError("accepted recovery catalog records")
		}
		accepted = next
	}
	return accepted, nil
}

// acceptedNameSlot proves one name slot (Rust accepted_name_slot: the
// occupied and conflict flags, the index-slot proof, and the record
// equality).
func (c *catalog) acceptedNameSlot(tables *tableStore, slot *[catalogNameSlotSize]byte) (catalogFeed, uint64, bool, error) {
	if slot[slotOccupiedOffset] == 0 || slot[slotConflictOffset] != 0 {
		return catalogFeed{}, 0, false, nil
	}
	entry, err := c.record(tables, format.U64(slot[slotRecordOffset:slotRecordOffset+8]))
	if err != nil {
		return catalogFeed{}, 0, false, err
	}
	_, indexSlot, found, err := probeIndex(tables, c, entry.index, false)
	if err != nil || !found {
		return catalogFeed{}, 0, false, err
	}
	if indexSlot[slotConflictOffset] != 0 {
		return catalogFeed{}, 0, false, nil
	}
	indexed, err := c.record(tables, format.U64(indexSlot[slotRecordOffset:slotRecordOffset+8]))
	if err != nil {
		return catalogFeed{}, 0, false, err
	}
	if !equalCatalogFeed(indexed, entry) {
		return catalogFeed{}, 0, false, nil
	}
	return entry, format.U64(slot[nameOccurrencesOffset:catalogNameSlotSize]), true, nil
}

// record reads and decodes one catalog record (Rust Catalog::record:
// the record index bound is the Corrupt class). The record bytes are
// a view of the store arena, so the returned name aliases the tables
// heap instead of a per-call buffer; the caller copies the name only
// at the boundary that outlives the tables (Rust FeedName owns the
// copy inside the accessor; the arena view is the Go equivalent).
func (c *catalog) record(tables *tableStore, index uint64) (catalogFeed, error) {
	if index >= c.recordsLen {
		return catalogFeed{}, corruptError("recovery catalog record index is invalid")
	}
	bytes, err := tables.view(c.records, index)
	if err != nil {
		return catalogFeed{}, err
	}
	return decodeCatalogRecord(bytes)
}

// view returns one region entry as a slice of the store arena (Rust
// Tables::read over the entry slice): the same offset proof as read,
// but the caller decodes the arena bytes directly, so a per-record
// catalog read copies no record into a local buffer.
func (t *tableStore) view(region tableRegion, index uint64) ([]byte, error) {
	offset, err := t.offset(region, index, int(region.width))
	if err != nil {
		return nil, err
	}
	return t.bytes[offset : offset+region.width], nil
}

// encodeCatalogRecord encodes one record into the fixed table shape
// (Rust encode_record).
func encodeCatalogRecord(entry catalogFeed, output []byte) {
	output[recordNameLengthOffset] = byte(len(entry.name))
	format.PutU32(output[recordIndexOffset:recordNameOffset], entry.index)
	copy(output[recordNameOffset:recordNameOffset+len(entry.name)], entry.name)
}

// decodeCatalogRecord decodes one table record (Rust decode_record: the
// stored name grammar proof; every refusal is the Corrupt class).
func decodeCatalogRecord(bytes []byte) (catalogFeed, error) {
	length := int(bytes[recordNameLengthOffset])
	if length < 1 || length > format.MaxFeedNameLen || recordNameOffset+length > len(bytes) {
		return catalogFeed{}, corruptError("recovery catalog name length is invalid")
	}
	name := bytes[recordNameOffset : recordNameOffset+length]
	if !format.FeedNameValid(name) {
		return catalogFeed{}, corruptError("recovery catalog name is invalid")
	}
	return catalogFeed{name: name, index: format.U32(bytes[recordIndexOffset:recordNameOffset])}, nil
}

// equalCatalogFeed compares two catalog entries (Rust FeedEntry
// equality).
func equalCatalogFeed(a, b catalogFeed) bool {
	return a.index == b.index && bytes.Equal(a.name, b.name)
}

// hashBytes is the name probe hash of the catalog table (Rust
// hash_bytes: FNV-1a 64 over the name bytes).
func hashBytes(bytes []byte) uint64 {
	hash := uint64(0xcbf29ce484222325)
	for _, b := range bytes {
		hash ^= uint64(b)
		hash *= 0x00000100000001b3
	}
	return hash
}
