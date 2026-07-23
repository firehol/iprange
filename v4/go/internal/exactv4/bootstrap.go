package exactv4

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"
)

// OpenMode makes immutable and coordinated live semantics explicit.
type OpenMode uint8

const (
	OpenImmutableReader OpenMode = 1
	OpenLiveReader      OpenMode = 2
	OpenWriter          OpenMode = 3
)

// MetaSelection reports what the two physical meta pages actually prove.
type MetaSelection uint8

const (
	SelectionProvenCurrent MetaSelection = 1
	SelectionSoleMeta0     MetaSelection = 2
	SelectionSoleMeta1     MetaSelection = 3
)

// MetaProblem classifies why one physical meta is not bootstrap-valid.
type MetaProblem uint8

const (
	MetaProblemMagic MetaProblem = iota + 1
	MetaProblemFixedValue
	MetaProblemReserved
	MetaProblemTag
	MetaProblemDatabaseID
	MetaProblemChecksum
	MetaProblemCommitNonce
	MetaProblemTransaction
	MetaProblemPageCount
	MetaProblemPhysicalLength
	MetaProblemRootBounds
	MetaProblemKindInvariant
	MetaProblemCountInvariant
	MetaProblemMetadataInvariant
	MetaProblemRetirementInvariant
)

// BootstrapErrorCode is the stable high-level bootstrap failure class.
type BootstrapErrorCode uint8

const (
	BootstrapErrFileTooShort BootstrapErrorCode = iota + 1
	BootstrapErrFileUnaligned
	BootstrapErrHostAddressability
	BootstrapErrStaticIdentityMismatch
	BootstrapErrNoBootstrapMeta
	BootstrapErrTransactionGap
	BootstrapErrPhysicalParity
	BootstrapErrEqualTransactionDisagreement
	BootstrapErrCurrentGenerationUnprovable
	BootstrapErrImmutableLengthMismatch
	BootstrapErrInvalidOpenMode
)

// BootstrapError retains both physical-meta findings when neither is usable.
type BootstrapError struct {
	Code  BootstrapErrorCode
	Meta0 MetaProblem
	Meta1 MetaProblem
}

func (e *BootstrapError) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Code == BootstrapErrNoBootstrapMeta {
		return fmt.Sprintf("exact v4 bootstrap: no valid meta (meta0=%d, meta1=%d)", e.Meta0, e.Meta1)
	}
	return fmt.Sprintf("exact v4 bootstrap: error %d", e.Code)
}

// Bootstrap is the complete O(1) result needed before any page-graph access.
type Bootstrap struct {
	Meta             Meta
	Selection        MetaSelection
	SelectedMetaPage uint8
	CommittedBytes   uint64
	PhysicalBytes    uint64
}

// UnpublishedTailBytes is zero for exact-length inputs and otherwise reports
// the aligned live tail that an OS-backed writer must truncate after leasing.
func (b Bootstrap) UnpublishedTailBytes() uint64 {
	return b.PhysicalBytes - b.CommittedBytes
}

type identityReadable struct {
	meta Meta
}

type metaCandidate struct {
	meta    Meta
	problem MetaProblem
	valid   bool
}

// Open classifies and selects the two exact-v4 meta pages without walking any
// non-meta page graph. For OpenWriter, successful return only classifies an
// aligned unpublished tail; the OS-backed opener remains responsible for
// truncating that tail after it owns the writer lease.
func Open(data []byte, mode OpenMode) (Bootstrap, error) {
	if err := validateBootstrapGeometry(uint64(len(data)), mode); err != nil {
		return Bootstrap{}, err
	}
	page0 := (*[PageSize]byte)(data[:PageSize])
	page1 := (*[PageSize]byte)(data[PageSize : 2*PageSize])
	return openMetaPages(page0, page1, uint64(len(data)), mode)
}

func openMetaPages(
	page0 *[PageSize]byte,
	page1 *[PageSize]byte,
	physicalBytes uint64,
	mode OpenMode,
) (Bootstrap, error) {
	if err := validateBootstrapGeometry(physicalBytes, mode); err != nil {
		return Bootstrap{}, err
	}

	identity0, problem0 := readIdentity(page0[:])
	identity1, problem1 := readIdentity(page1[:])

	if problem0 == 0 && problem1 == 0 && !identity0.meta.staticIdentityEqual(identity1.meta) {
		return Bootstrap{}, &BootstrapError{Code: BootstrapErrStaticIdentityMismatch}
	}

	candidate0 := validateBootstrap(identity0, problem0, physicalBytes)
	candidate1 := validateBootstrap(identity1, problem1, physicalBytes)

	var (
		meta         Meta
		selection    MetaSelection
		selectedPage uint8
	)
	switch {
	case !candidate0.valid && !candidate1.valid:
		return Bootstrap{}, &BootstrapError{
			Code:  BootstrapErrNoBootstrapMeta,
			Meta0: candidate0.problem,
			Meta1: candidate1.problem,
		}
	case candidate0.valid && !candidate1.valid:
		meta, selection, selectedPage = candidate0.meta, SelectionSoleMeta0, 0
	case !candidate0.valid && candidate1.valid:
		meta, selection, selectedPage = candidate1.meta, SelectionSoleMeta1, 1
	default:
		var err error
		meta, selection, selectedPage, err = selectPair(
			page0[:],
			page1[:],
			candidate0.meta,
			candidate1.meta,
		)
		if err != nil {
			return Bootstrap{}, err
		}
	}

	if mode != OpenImmutableReader && selection != SelectionProvenCurrent {
		return Bootstrap{}, &BootstrapError{Code: BootstrapErrCurrentGenerationUnprovable}
	}
	committedBytes, ok := checkedMul(meta.PageCount, PageSize)
	if !ok {
		return Bootstrap{}, &BootstrapError{Code: BootstrapErrHostAddressability}
	}
	if mode == OpenImmutableReader && committedBytes != physicalBytes {
		return Bootstrap{}, &BootstrapError{Code: BootstrapErrImmutableLengthMismatch}
	}

	return Bootstrap{
		Meta:             meta,
		Selection:        selection,
		SelectedMetaPage: selectedPage,
		CommittedBytes:   committedBytes,
		PhysicalBytes:    physicalBytes,
	}, nil
}

func validateBootstrapGeometry(physicalBytes uint64, mode OpenMode) error {
	if mode != OpenImmutableReader && mode != OpenLiveReader && mode != OpenWriter {
		return &BootstrapError{Code: BootstrapErrInvalidOpenMode}
	}
	if physicalBytes < 2*PageSize {
		return &BootstrapError{Code: BootstrapErrFileTooShort}
	}
	if physicalBytes%PageSize != 0 {
		return &BootstrapError{Code: BootstrapErrFileUnaligned}
	}
	addressable := int(physicalBytes)
	if addressable < 0 || uint64(addressable) != physicalBytes {
		return &BootstrapError{Code: BootstrapErrHostAddressability}
	}
	return nil
}

func readIdentity(page []byte) (identityReadable, MetaProblem) {
	if string(page[0:8]) != MetaMagic {
		return identityReadable{}, MetaProblemMagic
	}
	if binary.LittleEndian.Uint16(page[8:10]) != MetaSize || page[10] != PageShift {
		return identityReadable{}, MetaProblemFixedValue
	}
	if anyNonzero(page[13:16]) || anyNonzero(page[184:252]) || anyNonzero(page[256:]) {
		return identityReadable{}, MetaProblemReserved
	}
	storedCRC := binary.LittleEndian.Uint32(page[MetaCRCOffset : MetaCRCOffset+4])
	if metaCRC(page) != storedCRC {
		return identityReadable{}, MetaProblemChecksum
	}
	if !validAddressFamily(AddressFamily(page[11])) || !validValueKind(ValueKind(page[12])) {
		return identityReadable{}, MetaProblemFixedValue
	}
	meta, ok := decodeMetaUnchecked(page)
	if !ok {
		return identityReadable{}, MetaProblemTag
	}
	if meta.DatabaseID == [16]byte{} {
		return identityReadable{}, MetaProblemDatabaseID
	}
	return identityReadable{meta: meta}, 0
}

func validateBootstrap(identity identityReadable, prior MetaProblem, physicalBytes uint64) metaCandidate {
	if prior != 0 {
		return metaCandidate{problem: prior}
	}
	meta := identity.meta
	invalid := func(problem MetaProblem) metaCandidate {
		return metaCandidate{problem: problem}
	}

	if meta.TxnID == 0 {
		return invalid(MetaProblemTransaction)
	}
	if meta.CommitNonce == [16]byte{} {
		return invalid(MetaProblemCommitNonce)
	}
	if meta.PageCount < 2 || meta.PageCount > MaxPageCount {
		return invalid(MetaProblemPageCount)
	}
	committedBytes, ok := checkedMul(meta.PageCount, PageSize)
	if !ok {
		return invalid(MetaProblemPageCount)
	}
	if physicalBytes < committedBytes {
		return invalid(MetaProblemPhysicalLength)
	}

	for _, root := range metaRoots(meta) {
		if root != 0 && (root < 2 || uint64(root) >= meta.PageCount) {
			return invalid(MetaProblemRootBounds)
		}
	}
	if meta.RangeRecordCount != 0 && meta.RangeRoot == 0 {
		return invalid(MetaProblemCountInvariant)
	}
	leafCapacity := uint64((PageSize - int(PageHeaderSize)) / 12)
	if meta.AddressFamily == AddressFamilyIPv6 {
		leafCapacity = uint64((PageSize - int(PageHeaderSize)) / 36)
	}
	maximumRangeRecords, ok := checkedMul(meta.PageCount-2, leafCapacity)
	if !ok || meta.RangeRecordCount > maximumRangeRecords {
		return invalid(MetaProblemCountInvariant)
	}
	if meta.RetirementBatchCount > meta.TxnID-1 ||
		(meta.RetirementBatchCount == 0) != (meta.RetirementRoot == 0) {
		return invalid(MetaProblemRetirementInvariant)
	}
	if meta.MetadataRoot == 0 {
		if meta.MetadataUncompressedLen != 0 || meta.MetadataCompressedLen != 0 {
			return invalid(MetaProblemMetadataInvariant)
		}
	} else if !validMetadataLengths(meta) {
		return invalid(MetaProblemMetadataInvariant)
	}

	var problem MetaProblem
	if meta.ValueKind == ValueKindDirect {
		problem = validateDirect(meta)
	} else {
		problem = validateMembership(meta)
	}
	if problem != 0 {
		return invalid(problem)
	}
	return metaCandidate{meta: meta, valid: true}
}

func validMetadataLengths(meta Meta) bool {
	plus, ok := checkedAdd(meta.MetadataUncompressedLen, 65_534)
	if !ok {
		return false
	}
	blocks := plus / 65_535
	if blocks == 0 {
		blocks = 1
	}
	overhead, ok := checkedMul(5, blocks)
	if !ok {
		return false
	}
	maximumZlibBytes, ok := checkedAdd(meta.MetadataUncompressedLen, overhead)
	if !ok {
		return false
	}
	maximumZlibBytes, ok = checkedAdd(maximumZlibBytes, 6)
	if !ok {
		return false
	}
	maximumCompressedBytes, ok := checkedMul(meta.PageCount-2, 4048)
	if !ok {
		return false
	}
	return meta.MetadataCompressedLen != 0 &&
		meta.MetadataCompressedLen <= maximumCompressedBytes &&
		meta.MetadataCompressedLen <= maximumZlibBytes &&
		meta.MetadataUncompressedLen <= MaxMetadataUncompressed
}

func validateDirect(meta Meta) MetaProblem {
	if meta.ActiveFeedCount != 0 ||
		meta.FeedIndexLimit != 0 ||
		meta.MembershipEntryCount != 0 ||
		meta.MembershipIDLimit != 0 ||
		meta.CatalogNameRoot != 0 ||
		meta.CatalogIndexRoot != 0 ||
		meta.FeedUsedRoot != 0 ||
		meta.MembershipIDRoot != 0 ||
		meta.MembershipHashRoot != 0 ||
		meta.MembershipUsedRoot != 0 {
		return MetaProblemKindInvariant
	}
	return 0
}

func validateMembership(meta Meta) MetaProblem {
	if meta.FeedIndexLimit > MaxPageCount ||
		meta.ActiveFeedCount > meta.FeedIndexLimit ||
		meta.MembershipEntryCount > math.MaxUint32 ||
		meta.MembershipIDLimit < 1 ||
		meta.MembershipIDLimit > MaxPageCount ||
		meta.MembershipEntryCount >= meta.MembershipIDLimit ||
		meta.MembershipEntryCount > meta.RangeRecordCount {
		return MetaProblemCountInvariant
	}
	if meta.ActiveFeedCount == 0 &&
		(meta.MembershipEntryCount != 0 || meta.RangeRecordCount != 0) {
		return MetaProblemCountInvariant
	}
	if meta.MembershipEntryCount == 0 && meta.RangeRecordCount != 0 {
		return MetaProblemCountInvariant
	}
	if meta.ActiveFeedCount == 0 {
		if meta.CatalogNameRoot != 0 || meta.CatalogIndexRoot != 0 || meta.FeedUsedRoot != 0 {
			return MetaProblemKindInvariant
		}
	} else if meta.CatalogNameRoot == 0 || meta.CatalogIndexRoot == 0 || meta.FeedUsedRoot == 0 {
		return MetaProblemKindInvariant
	}
	if meta.MembershipEntryCount == 0 {
		if meta.MembershipIDRoot != 0 ||
			meta.MembershipHashRoot != 0 ||
			meta.MembershipUsedRoot != 0 ||
			meta.MembershipIDLimit != 1 {
			return MetaProblemKindInvariant
		}
	} else if meta.MembershipIDRoot == 0 || meta.MembershipHashRoot == 0 || meta.MembershipUsedRoot == 0 {
		return MetaProblemKindInvariant
	}
	return 0
}

func metaRoots(meta Meta) [10]uint32 {
	return [10]uint32{
		meta.RangeRoot,
		meta.CatalogNameRoot,
		meta.CatalogIndexRoot,
		meta.FeedUsedRoot,
		meta.MembershipIDRoot,
		meta.MembershipHashRoot,
		meta.MembershipUsedRoot,
		meta.MetadataRoot,
		meta.FreeBitmapRoot,
		meta.RetirementRoot,
	}
}

func selectPair(page0, page1 []byte, meta0, meta1 Meta) (Meta, MetaSelection, uint8, error) {
	if meta0.TxnID == meta1.TxnID {
		if !bytes.Equal(page0[:MetaSize], page1[:MetaSize]) {
			return Meta{}, 0, 0, &BootstrapError{Code: BootstrapErrEqualTransactionDisagreement}
		}
		selected := uint8(meta0.TxnID & 1)
		return meta0, SelectionProvenCurrent, selected, nil
	}

	lower, higher, higherPage := meta0, meta1, uint8(1)
	if meta1.TxnID < meta0.TxnID {
		lower, higher, higherPage = meta1, meta0, 0
	}
	if higher.TxnID-lower.TxnID != 1 {
		return Meta{}, 0, 0, &BootstrapError{Code: BootstrapErrTransactionGap}
	}
	if uint8(higher.TxnID&1) != higherPage {
		return Meta{}, 0, 0, &BootstrapError{Code: BootstrapErrPhysicalParity}
	}
	return higher, SelectionProvenCurrent, higherPage, nil
}

func anyNonzero(value []byte) bool {
	for _, b := range value {
		if b != 0 {
			return true
		}
	}
	return false
}

func checkedAdd(left, right uint64) (uint64, bool) {
	if left > math.MaxUint64-right {
		return 0, false
	}
	return left + right, true
}

func checkedMul(left, right uint64) (uint64, bool) {
	if left != 0 && right > math.MaxUint64/left {
		return 0, false
	}
	return left * right, true
}
