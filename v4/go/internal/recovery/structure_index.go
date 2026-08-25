package recovery

// Recovery of authoritative structured payloads by source-local ID
// (Rust recovery/structure_index.rs): the dense table-tree scan over
// the page-ownership set counts and recovers the structure records,
// the ID conflicts reconcile, and the outcomes fold into the report.

import (
	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/live"
	"github.com/firehol/iprange/v4/go/internal/mapping"
	"github.com/firehol/iprange/v4/go/internal/validation"
)

// structureTableEvents is one structure table scan sink (Rust
// TableEvents).
type structureTableEvents interface {
	pageAccepted() error
	pageRejected(ioUnreadable bool) error
	unknown(reason validation.ValidationReason, page *uint32) error
	leaf(page uint32, expectedID uint64, cell []byte) error
}

// countStructureRecords counts the recovery-readable structure records
// of the network-enrichment kind (Rust structure_index::count: the
// table scan with the id bound and the payload digest proof).
func countStructureRecords(m *mapping.Mapping, meta format.Meta, pages *pageSet, check func() error) (uint64, error) {
	events := &structureCountEvents{meta: meta}
	if err := scanStructureTable(m, meta, meta.StructureIDRoot, pages, check, events); err != nil {
		return 0, err
	}
	return events.count, nil
}

// structureCountEvents counts the readable structure records (Rust
// Counter).
type structureCountEvents struct {
	meta  format.Meta
	count uint64
}

func (e *structureCountEvents) pageAccepted() error { return nil }
func (e *structureCountEvents) pageRejected(ioUnreadable bool) error {
	return nil
}
func (e *structureCountEvents) unknown(reason validation.ValidationReason, page *uint32) error {
	return nil
}
func (e *structureCountEvents) leaf(page uint32, expectedID uint64, cell []byte) error {
	record, err := format.DecodeStructureRecord(cell)
	if err != nil || uint64(record.ID) != expectedID || expectedID >= e.meta.StructureIDLimit {
		return nil
	}
	digest, err := format.StructurePayloadDigest(format.StructureKindNetworkEnrichmentV1, record.Payload)
	if err != nil {
		return err
	}
	if digest != record.Digest {
		return nil
	}
	next := e.count + 1
	if next == 0 {
		return overflowError("recovery structure count")
	}
	e.count = next
	return nil
}

// recoverStructureRecords reconciles the structure dictionary of one
// source (Rust structure_index::recover: the kind proof, the table
// scan with the membership dependence, the ID conflict reconciliation,
// and the outcome report).
func recoverStructureRecords(m *mapping.Mapping, meta format.Meta, memberships *membershipIndex, pages *pageSet, tables *tableStore, check func() error, rep *reporter) (*structureIndex, error) {
	if meta.StructureKind != format.StructureKindNetworkEnrichmentV1 {
		return nil, &format.Error{Code: format.CodeUnsupportedStructure, Detail: "recovery structure kind is unsupported"}
	}
	entries := newStructureIndex(tables, format.StructureKindNetworkEnrichmentV1)
	events := &structureRecoverEvents{
		meta:        meta,
		rep:         rep,
		memberships: memberships,
		entries:     entries,
		tables:      tables,
	}
	if err := scanStructureTable(m, meta, meta.StructureIDRoot, pages, check, events); err != nil {
		return nil, err
	}
	if err := reconcileStructureIDs(entries, tables, rep); err != nil {
		return nil, err
	}
	return entries, reportStructureOutcomes(entries, tables, rep)
}

// structureRecoverEvents wires the structure table scan into the
// reporter and the locator table (Rust Events: every leaf cell counts
// one examined record, undecodable records reject, the id bound and
// the payload digest reject with their exact classes, and the
// membership dependence rejects with the membership-invalid class).
type structureRecoverEvents struct {
	meta        format.Meta
	rep         *reporter
	memberships *membershipIndex
	entries     *structureIndex
	tables      *tableStore
}

func (e *structureRecoverEvents) pageAccepted() error {
	return e.rep.pageAccepted()
}

func (e *structureRecoverEvents) pageRejected(ioUnreadable bool) error {
	return e.rep.pageRejected(ioUnreadable)
}

func (e *structureRecoverEvents) unknown(reason validation.ValidationReason, page *uint32) error {
	return e.rep.emitPageUnknown(reason, validation.ObjectStructureDictionary, page)
}

func (e *structureRecoverEvents) leaf(page uint32, expectedID uint64, cell []byte) error {
	if err := e.rep.structureExamined(); err != nil {
		return err
	}
	record, err := format.DecodeStructureRecord(cell)
	if err != nil {
		return e.rep.structureRejected(1)
	}
	// The implied-slot id compare is its own refusal with the
	// StructureInvalid envelope (Rust structure_index.rs Events::leaf:
	// decode_record, then the id and limit proof).
	if uint64(record.ID) != expectedID || expectedID >= e.meta.StructureIDLimit {
		if err := e.rep.structureRejected(1); err != nil {
			return err
		}
		return e.unknown(validation.ReasonStructureInvalid, &page)
	}
	digest, err := format.StructurePayloadDigest(format.StructureKindNetworkEnrichmentV1, record.Payload)
	if err != nil {
		return err
	}
	if digest != record.Digest {
		if err := e.rep.structureRejected(1); err != nil {
			return err
		}
		return e.unknown(validation.ReasonStructureHashInvalid, &page)
	}
	membershipID := format.U32(record.Payload[24:28])
	rejected := false
	if membershipID != 0 {
		_, found, err := e.memberships.get(e.tables, membershipID)
		if err != nil {
			return err
		}
		rejected = !found
		if rejected {
			if err := e.unknown(validation.ReasonStructureMembershipInvalid, &page); err != nil {
				return err
			}
		}
	}
	var payload [format.NetworkEnrichmentV1PayloadSize]byte
	copy(payload[:], record.Payload)
	return e.entries.push(e.tables, structureLocator{
		id:           record.ID,
		membershipID: membershipID,
		leafPage:     page,
		payload:      payload,
		payloadLen:   format.NetworkEnrichmentV1PayloadSize,
		rejected:     rejected,
	})
}

// reconcileStructureIDs folds the source-ID registration conflicts
// (Rust reconcile_ids: both records of one conflicting id reject, and
// the first conflict streams the StructureInvalid envelope).
func reconcileStructureIDs(entries *structureIndex, tables *tableStore, rep *reporter) error {
	for index := uint64(0); index < entries.recordsLen(); index++ {
		entry, err := entries.record(tables, index)
		if err != nil {
			return err
		}
		insert, err := entries.insertID(tables, entry.id, index)
		if err != nil {
			return err
		}
		if !insert.duplicate {
			continue
		}
		if err := entries.reject(tables, insert.first); err != nil {
			return err
		}
		if err := entries.reject(tables, index); err != nil {
			return err
		}
		if insert.newlyConflicted {
			if err := rep.emitPageUnknown(validation.ReasonStructureInvalid, validation.ObjectStructureDictionary, &entry.leafPage); err != nil {
				return err
			}
		}
	}
	return nil
}

// reportStructureOutcomes folds the structure proof (Rust
// report_outcomes: the accepted and rejected counts).
func reportStructureOutcomes(entries *structureIndex, tables *tableStore, rep *reporter) error {
	var accepted, rejected uint64
	for index := uint64(0); index < entries.recordsLen(); index++ {
		entry, err := entries.record(tables, index)
		if err != nil {
			return err
		}
		if entry.rejected {
			rejected++
		} else {
			accepted++
		}
	}
	if err := rep.structureAccepted(accepted); err != nil {
		return err
	}
	return rep.structureRejected(rejected)
}

// scanStructureTable walks one dense structure table tree (Rust
// structure_index::scan: the root level from the id limit, then the
// node walk).
func scanStructureTable(m *mapping.Mapping, meta format.Meta, root uint32, pages *pageSet, check func() error, events structureTableEvents) error {
	if root == 0 {
		return nil
	}
	level, ok := format.StructureRootLevel(meta.StructureIDLimit)
	if !ok {
		return corruptError("recovery structure id limit is invalid")
	}
	var path [format.StructureTableMaxLevel + 1]uint32
	return scanStructureNode(m, meta, root, uint16(level), 0, &path, 0, pages, check, events)
}

// scanStructureNode walks one structure table node (Rust scan_node:
// the claim, the checked page, the dense header, the reserved-zero
// proof, and the leaf or directory walk).
func scanStructureNode(m *mapping.Mapping, meta format.Meta, pageNumber uint32, expectedLevel uint16, base uint64, path *[format.StructureTableMaxLevel + 1]uint32, depth int, pages *pageSet, check func() error, events structureTableEvents) error {
	if err := live.Checkpoint(check); err != nil {
		return err
	}
	claimed, reason, err := pages.claim(pageNumber, meta.PageCount, path[:], depth)
	if err != nil {
		return err
	}
	if !claimed {
		pageCopy := pageNumber
		return events.unknown(reason, &pageCopy)
	}
	page, problem := checkedPage(m, pageNumber, meta.PageCount)
	if problem != nil {
		return rejectStructurePage(events, pageNumber, problem.reason, problem.ioUnreadable)
	}
	header, headerProblem := format.InspectStructureTableHeader(page, meta.TxnID, uint32(format.StructureKindNetworkEnrichmentV1), &expectedLevel)
	if headerProblem != format.TreeHeaderProblemNone {
		return rejectStructurePage(events, pageNumber, structureHeaderReason(headerProblem), false)
	}
	end := uint16(format.StructureLeafEnd)
	if header.Level != 0 {
		end = uint16(format.StructureBranchEnd)
	}
	if !format.AllZero(page[end:]) {
		return rejectStructurePage(events, pageNumber, validation.ReasonPageReservedNonzero, false)
	}
	if err := events.pageAccepted(); err != nil {
		return err
	}
	if header.Level == 0 {
		return scanStructureLeaf(m, meta, page, header, pageNumber, base, pages, check, events)
	}
	return scanStructureBranch(m, meta, page, header, pageNumber, base, path, depth, pages, check, events)
}

// rejectStructurePage streams one refused structure page (Rust
// reject_page: the page-rejected count then the unknown envelope).
func rejectStructurePage(events structureTableEvents, pageNumber uint32, reason validation.ValidationReason, ioUnreadable bool) error {
	if err := events.pageRejected(ioUnreadable); err != nil {
		return err
	}
	return events.unknown(reason, &pageNumber)
}

// structureHeaderReason maps one dense header problem to its envelope
// class (Rust header_reason).
func structureHeaderReason(problem format.TreeHeaderProblem) validation.ValidationReason {
	switch problem {
	case format.TreeHeaderProblemBorn:
		return validation.ReasonPageBornTxnInvalid
	case format.TreeHeaderProblemType:
		return validation.ReasonPageTypeMismatch
	case format.TreeHeaderProblemLevel:
		return validation.ReasonTreeLevelInvalid
	default:
		return validation.ReasonPageHeaderInvalid
	}
}

// scanStructureLeaf walks one record page (Rust scan_leaf: the fixed
// record array, the all-zero skip, the slot-implied id, and the
// item-count proof).
func scanStructureLeaf(m *mapping.Mapping, meta format.Meta, page []byte, header format.PageHeader, pageNumber uint32, base uint64, pages *pageSet, check func() error, events structureTableEvents) error {
	found := 0
	for slot := 0; slot < format.StructureRecordSlots; slot++ {
		if err := live.Checkpoint(check); err != nil {
			return err
		}
		at := 32 + slot*format.StructureRecordSize
		cell := page[at : at+format.StructureRecordSize]
		if format.AllZero(cell) {
			continue
		}
		found++
		if err := events.leaf(pageNumber, base+uint64(slot), cell); err != nil {
			return err
		}
	}
	if found != int(header.ItemCount) {
		pageCopy := pageNumber
		return events.unknown(validation.ReasonPageHeaderInvalid, &pageCopy)
	}
	return nil
}

// scanStructureBranch walks one directory page (Rust scan_branch: the
// fixed child array, the coverage-scaled child base, and the
// item-count proof).
func scanStructureBranch(m *mapping.Mapping, meta format.Meta, page []byte, header format.PageHeader, pageNumber uint32, base uint64, path *[format.StructureTableMaxLevel + 1]uint32, depth int, pages *pageSet, check func() error, events structureTableEvents) error {
	span, ok := format.StructureSpanOfLevel(uint32(header.Level) - 1)
	if !ok {
		return overflowError("recovery structure coverage")
	}
	found := 0
	for index := 0; index < format.StructureDirectoryChildCount; index++ {
		if err := live.Checkpoint(check); err != nil {
			return err
		}
		child := format.U32(page[32+index*4:])
		if child == 0 {
			continue
		}
		found++
		if child < 2 || uint64(child) >= meta.PageCount {
			if err := events.unknown(validation.ReasonPageOutOfBounds, &child); err != nil {
				return err
			}
			continue
		}
		scaled, ok := checkedMul(span, uint64(index))
		if !ok {
			return overflowError("recovery structure coverage")
		}
		childBase, ok := checkedAdd(base, scaled)
		if !ok {
			return overflowError("recovery structure coverage")
		}
		if err := scanStructureNode(m, meta, child, header.Level-1, childBase, path, depth+1, pages, check, events); err != nil {
			return err
		}
	}
	if found != int(header.ItemCount) {
		pageCopy := pageNumber
		return events.unknown(validation.ReasonPageHeaderInvalid, &pageCopy)
	}
	return nil
}
