package exactv4

import (
	"reflect"
	"sync/atomic"
)

type privateWriterWorkspaceErrorCode uint8

const (
	privateWriterWorkspaceErrInvalidBudget privateWriterWorkspaceErrorCode = iota + 1
	privateWriterWorkspaceErrExhausted
	privateWriterWorkspaceErrInvalidState
)

type privateWriterWorkspaceError struct {
	code     privateWriterWorkspaceErrorCode
	required uint64
	actual   uint64
}

func (e privateWriterWorkspaceError) failed() bool { return e.code != 0 }

type privateWriterWorkspaceBudget struct {
	maxBytes            uint64
	privatePages        int
	records             int
	preparedSlots       int
	scratchWordsPerSlot int
}

type privateWriterWorkspaceVersions struct {
	layout   uint64
	ledger   uint64
	slotMap  uint64
	records  uint64
	scratch  uint64
	prepared uint64
}

const privateWriterWorkspaceComponentCount = 9

type privateWriterWorkspaceLayout struct {
	offsets [privateWriterWorkspaceComponentCount]uint64
	bytes   uint64
}

type privateWriterWorkspace struct {
	self *privateWriterWorkspace

	layout           privateWriterWorkspaceLayout
	partitionBytes   uint64
	writerHeapBudget uint64

	poolSlots      []privatePagePoolSlot
	poolValidation []uint32
	records        []privateWriterSealedBitmapWorkUnitRecord
	slotRecords    []int
	preparedSlots  []privateWriterFixedPointPreparedWork
	scratch        []uint64

	recordGenerations   []uint64
	preparedGenerations []uint64
	scratchGenerations  []uint64
	scratchWordsPerSlot int

	layoutGeneration uint64
	ledgerVersion    uint64
	slotMapVersion   uint64
	recordVersion    uint64
	scratchVersion   uint64
	preparedVersion  uint64

	callbackActive         bool
	nextPreparedSlot       int
	scopePreflightVisits   int
	transactionResetCount  uint64
	transactionResetVisits uint64
}

var privateWriterWorkspaceGeneration atomic.Uint64

func nextPrivateWriterWorkspaceGeneration() (uint64, bool) {
	for {
		current := privateWriterWorkspaceGeneration.Load()
		if current == ^uint64(0) {
			return 0, false
		}
		if privateWriterWorkspaceGeneration.CompareAndSwap(current, current+1) {
			return current + 1, true
		}
	}
}

func privateWriterWorkspaceCheckedAdd(
	offset uint64,
	count int,
	element reflect.Type,
) (uint64, uint64, bool) {
	if count <= 0 {
		return 0, 0, false
	}
	align := uint64(element.Align())
	padding := (align - offset%align) % align
	if padding > ^uint64(0)-offset {
		return 0, 0, false
	}
	start := offset + padding
	size := uint64(element.Size())
	items := uint64(count)
	if size != 0 && items > ^uint64(0)/size {
		return 0, 0, false
	}
	bytes := items * size
	// A count can fit int while count*element-size cannot fit the native
	// address space. Reject that budget before make can panic on 32-bit hosts.
	nativeLimit := uint64(^uint(0) >> 1)
	if uintptrLimit := uint64(^uintptr(0)); uintptrLimit < nativeLimit {
		nativeLimit = uintptrLimit
	}
	if bytes > nativeLimit {
		return 0, 0, false
	}
	if bytes > ^uint64(0)-start {
		return 0, 0, false
	}
	next := start + bytes
	if next > nativeLimit {
		return 0, 0, false
	}
	return start, next, true
}

func privateWriterWorkspaceLayoutFor(
	budget privateWriterWorkspaceBudget,
) (privateWriterWorkspaceLayout, bool) {
	if budget.maxBytes == 0 || budget.privatePages <= 0 || budget.records <= 0 ||
		budget.preparedSlots <= 0 || budget.scratchWordsPerSlot <= 0 ||
		budget.scratchWordsPerSlot > int(^uint(0)>>1)/budget.preparedSlots {
		return privateWriterWorkspaceLayout{}, false
	}
	scratchWords := budget.preparedSlots * budget.scratchWordsPerSlot
	components := [...]struct {
		count int
		type_ reflect.Type
	}{
		{budget.privatePages, reflect.TypeOf(privatePagePoolSlot{})},
		{budget.privatePages, reflect.TypeOf(uint32(0))},
		{budget.records, reflect.TypeOf(privateWriterSealedBitmapWorkUnitRecord{})},
		{budget.privatePages, reflect.TypeOf(int(0))},
		{budget.preparedSlots, reflect.TypeOf(privateWriterFixedPointPreparedWork{})},
		{scratchWords, reflect.TypeOf(uint64(0))},
		{budget.records, reflect.TypeOf(uint64(0))},
		{budget.preparedSlots, reflect.TypeOf(uint64(0))},
		{budget.preparedSlots, reflect.TypeOf(uint64(0))},
	}
	layout := privateWriterWorkspaceLayout{}
	offset := uint64(reflect.TypeOf(privateWriterWorkspace{}).Size())
	nativeLimit := uint64(^uint(0) >> 1)
	if uintptrLimit := uint64(^uintptr(0)); uintptrLimit < nativeLimit {
		nativeLimit = uintptrLimit
	}
	if offset > nativeLimit {
		return privateWriterWorkspaceLayout{}, false
	}
	for index, component := range components {
		start, next, ok := privateWriterWorkspaceCheckedAdd(
			offset, component.count, component.type_,
		)
		if !ok {
			return privateWriterWorkspaceLayout{}, false
		}
		layout.offsets[index] = start
		offset = next
	}
	// bytes is the exact retained capacity of the SDK-owned logical
	// partition: its descriptor, alignment gaps, and every typed backing
	// array at its full capacity.
	layout.bytes = offset
	return layout, layout.bytes <= budget.maxBytes
}

func newPrivateWriterWorkspace(
	budget privateWriterWorkspaceBudget,
	writerBudget privateWriterResourceBudget,
) (*privateWriterWorkspace, privateWriterWorkspaceError) {
	layout, ok := privateWriterWorkspaceLayoutFor(budget)
	if !ok || layout.bytes > writerBudget.maxHeapBytes ||
		uint64(budget.privatePages) > writerBudget.maxPrivatePages {
		required := layout.bytes
		if required == 0 {
			required = budget.maxBytes
		}
		return nil, privateWriterWorkspaceError{
			code:     privateWriterWorkspaceErrInvalidBudget,
			required: required, actual: writerBudget.maxHeapBytes,
		}
	}
	generation, generationOK := nextPrivateWriterWorkspaceGeneration()
	if !generationOK {
		return nil, privateWriterWorkspaceError{code: privateWriterWorkspaceErrExhausted}
	}
	scratchWords := budget.preparedSlots * budget.scratchWordsPerSlot
	workspace := &privateWriterWorkspace{
		layout:              layout,
		partitionBytes:      layout.bytes,
		writerHeapBudget:    writerBudget.maxHeapBytes,
		poolSlots:           make([]privatePagePoolSlot, budget.privatePages),
		poolValidation:      make([]uint32, budget.privatePages),
		records:             make([]privateWriterSealedBitmapWorkUnitRecord, budget.records),
		slotRecords:         make([]int, budget.privatePages),
		preparedSlots:       make([]privateWriterFixedPointPreparedWork, budget.preparedSlots),
		scratch:             make([]uint64, scratchWords),
		recordGenerations:   make([]uint64, budget.records),
		preparedGenerations: make([]uint64, budget.preparedSlots),
		scratchGenerations:  make([]uint64, budget.preparedSlots),
		scratchWordsPerSlot: budget.scratchWordsPerSlot,
		layoutGeneration:    generation,
	}
	workspace.self = workspace
	return workspace, privateWriterWorkspaceError{}
}

func (w *privateWriterWorkspace) versions() privateWriterWorkspaceVersions {
	if w == nil || w.self != w {
		return privateWriterWorkspaceVersions{}
	}
	return privateWriterWorkspaceVersions{
		layout: w.layoutGeneration, ledger: w.ledgerVersion,
		slotMap: w.slotMapVersion, records: w.recordVersion,
		scratch: w.scratchVersion, prepared: w.preparedVersion,
	}
}

func (w *privateWriterWorkspace) resetForTransaction() privateWriterWorkspaceError {
	if w == nil || w.self != w || w.layout.bytes == 0 ||
		w.partitionBytes != w.layout.bytes ||
		w.layoutGeneration == 0 || w.callbackActive {
		return privateWriterWorkspaceError{code: privateWriterWorkspaceErrInvalidState}
	}
	if w.ledgerVersion == ^uint64(0) || w.slotMapVersion == ^uint64(0) ||
		w.recordVersion == ^uint64(0) || w.scratchVersion == ^uint64(0) ||
		w.preparedVersion == ^uint64(0) {
		return privateWriterWorkspaceError{code: privateWriterWorkspaceErrExhausted}
	}
	// Pool initialization overwrites every slot. Clearing it here as well
	// doubles the dominant reset cost for large workspaces.
	clear(w.poolValidation)
	clear(w.records)
	clear(w.slotRecords)
	clear(w.preparedSlots)
	clear(w.scratch)
	clear(w.recordGenerations)
	clear(w.preparedGenerations)
	clear(w.scratchGenerations)
	w.ledgerVersion++
	w.slotMapVersion++
	w.recordVersion++
	w.scratchVersion++
	w.preparedVersion++
	w.nextPreparedSlot = 0
	w.scopePreflightVisits = 0
	w.transactionResetCount++
	w.transactionResetVisits += uint64(
		len(w.poolValidation) +
			len(w.records) +
			len(w.slotRecords) +
			len(w.preparedSlots) +
			len(w.scratch) +
			len(w.recordGenerations) +
			len(w.preparedGenerations) +
			len(w.scratchGenerations),
	)
	return privateWriterWorkspaceError{}
}
