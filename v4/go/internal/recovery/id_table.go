package recovery

// Shared fixed-record recovery storage and source-ID lookup (Rust
// recovery/id_table.rs): the record region and the ID slot region of
// one record class, the push/read/write/reject terminals, and the ID
// probe with the conflict classes.

import "github.com/firehol/iprange/v4/go/internal/format"

const (
	idSlotSize       = uint64(16)
	idSlotOccupied   = 0
	idSlotConflict   = 1
	idSlotID         = 4
	idSlotRecord     = 8
	idSlotRecordSize = 16
)

// idInsert is the ID-slot outcome of one registration (Rust Insert).
type idInsert struct {
	first           uint64 // first record of a conflicting slot
	duplicate       bool
	newlyConflicted bool
}

// recordCodec adapts one record class to the shared id table (Rust
// RecordCodec).
type recordCodec[R any] struct {
	width         int
	invalidRecord string
	full          string
	regions       func(layout tableLayout) (records, ids tableRegion)
	encode        func(record R, output []byte)
	decode        func(input []byte) (R, error)
	isRejected    func(record R) bool
	reject        func(record *R)
}

// idIndex is the shared record and ID-slot index of one recovery table
// (Rust IdIndex).
type idIndex[R any] struct {
	codec recordCodec[R]
	table rawIDTable
}

// newIDIndex builds one index over the store layout (Rust IdIndex::new).
func newIDIndex[R any](tables *tableStore, codec recordCodec[R]) *idIndex[R] {
	records, ids := codec.regions(tables.layout)
	return &idIndex[R]{codec: codec, table: rawIDTable{records: records, ids: ids, invalidRecord: codec.invalidRecord, full: codec.full}}
}

// push appends one record (Rust IdIndex::push: the record bound is the
// candidate-changed class).
func (c *idIndex[R]) push(tables *tableStore, record R) error {
	bytes := make([]byte, c.codec.width)
	c.codec.encode(record, bytes)
	return c.table.push(tables, bytes)
}

// record reads and decodes one record (Rust IdIndex::record).
func (c *idIndex[R]) record(tables *tableStore, index uint64) (R, error) {
	var out R
	bytes := make([]byte, c.codec.width)
	if err := c.table.read(tables, index, bytes); err != nil {
		return out, err
	}
	return c.codec.decode(bytes)
}

// reject marks one record rejected (Rust IdIndex::reject).
func (c *idIndex[R]) reject(tables *tableStore, index uint64) error {
	record, err := c.record(tables, index)
	if err != nil {
		return err
	}
	c.codec.reject(&record)
	bytes := make([]byte, c.codec.width)
	c.codec.encode(record, bytes)
	return c.table.write(tables, index, bytes)
}

// insertID registers one record under its ID (Rust IdIndex::insert_id).
func (c *idIndex[R]) insertID(tables *tableStore, id uint32, recordIndex uint64) (idInsert, error) {
	return c.table.insertID(tables, id, recordIndex)
}

// get returns the un-rejected record of one ID (Rust IdIndex::get).
func (c *idIndex[R]) get(tables *tableStore, id uint32) (R, bool, error) {
	var out R
	index, found, err := c.table.get(tables, id)
	if err != nil || !found {
		return out, false, err
	}
	record, err := c.record(tables, index)
	if err != nil {
		return out, false, err
	}
	return record, !c.codec.isRejected(record), nil
}

// recordsLen returns the record count.
func (c *idIndex[R]) recordsLen() uint64 {
	return c.table.recordsLen
}

// rawIDTable is the region-level record and ID table (Rust
// RawIdTable).
type rawIDTable struct {
	records       tableRegion
	ids           tableRegion
	recordsLen    uint64
	invalidRecord string
	full          string
}

// push appends one encoded record.
func (t *rawIDTable) push(tables *tableStore, record []byte) error {
	if t.recordsLen == t.records.slots {
		return candidateChangedError()
	}
	if err := tables.write(t.records, t.recordsLen, record); err != nil {
		return err
	}
	t.recordsLen++
	return nil
}

// read reads one record with the record-index proof (Rust RawIdTable::
// read).
func (t *rawIDTable) read(tables *tableStore, index uint64, output []byte) error {
	if err := t.requireRecord(index); err != nil {
		return err
	}
	return tables.read(t.records, index, output)
}

// write rewrites one record.
func (t *rawIDTable) write(tables *tableStore, index uint64, input []byte) error {
	if err := t.requireRecord(index); err != nil {
		return err
	}
	return tables.write(t.records, index, input)
}

// insertID registers one ID slot (Rust RawIdTable::insert_id: the
// first registration, or the duplicate with the first-record proof and
// the new-conflict envelope fact).
func (t *rawIDTable) insertID(tables *tableStore, id uint32, recordIndex uint64) (idInsert, error) {
	if err := t.requireRecord(recordIndex); err != nil {
		return idInsert{}, err
	}
	slotIndex, slot, found, err := t.probe(tables, id, false)
	if err != nil {
		return idInsert{}, err
	}
	if !found {
		slotIndex, slot, found, err = t.probe(tables, id, true)
		if err != nil {
			return idInsert{}, err
		}
		if !found {
			return idInsert{}, corruptError(t.full)
		}
	}
	if slot[idSlotOccupied] == 0 {
		slot[idSlotOccupied] = 1
		format.PutU32(slot[idSlotID:idSlotRecord], id)
		format.PutU64(slot[idSlotRecord:idSlotRecordSize], recordIndex)
		if err := tables.write(t.ids, slotIndex, slot[:]); err != nil {
			return idInsert{}, err
		}
		return idInsert{}, nil
	}
	newlyConflicted := slot[idSlotConflict] == 0
	slot[idSlotConflict] = 1
	if err := tables.write(t.ids, slotIndex, slot[:]); err != nil {
		return idInsert{}, err
	}
	return idInsert{
		duplicate:       true,
		first:           format.U64(slot[idSlotRecord:idSlotRecordSize]),
		newlyConflicted: newlyConflicted,
	}, nil
}

// get resolves one un-conflicted ID to its record index (Rust
// RawIdTable::get).
func (t *rawIDTable) get(tables *tableStore, id uint32) (uint64, bool, error) {
	_, slot, found, err := t.probe(tables, id, false)
	if err != nil || !found {
		return 0, false, err
	}
	if slot[idSlotConflict] != 0 {
		return 0, false, nil
	}
	index := format.U64(slot[idSlotRecord:idSlotRecordSize])
	if err := t.requireRecord(index); err != nil {
		return 0, false, err
	}
	return index, true, nil
}

// requireRecord proves the record index (Rust require_record: the
// Corrupt class).
func (t *rawIDTable) requireRecord(index uint64) error {
	if index >= t.recordsLen {
		return corruptError(t.invalidRecord)
	}
	return nil
}

// probe probes one ID slot (Rust RawIdTable::probe: the
// occupied-or-match acceptance; empty probes return the empty slot).
func (t *rawIDTable) probe(tables *tableStore, id uint32, empty bool) (uint64, [idSlotRecordSize]byte, bool, error) {
	var slot [idSlotRecordSize]byte
	if t.ids.slots == 0 {
		return 0, slot, false, nil
	}
	mask := t.ids.slots - 1
	index := hashU32(id) & mask
	for probe := uint64(0); probe < t.ids.slots; probe++ {
		if err := tables.read(t.ids, index, slot[:]); err != nil {
			return 0, slot, false, err
		}
		if slot[idSlotOccupied] == 0 {
			if empty {
				return index, slot, true, nil
			}
			return 0, slot, false, nil
		}
		if format.U32(slot[idSlotID:idSlotRecord]) == id {
			if !empty {
				return index, slot, true, nil
			}
			return 0, slot, false, nil
		}
		index = (index + 1) & mask
	}
	return 0, slot, false, nil
}
