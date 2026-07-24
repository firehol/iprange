package exactv4

import "fmt"

// rangeRootTransactionProof is private evidence that a replacement range root
// cannot bypass retirement of the selected committed range tree. It remains
// detached from allocator ownership and target metadata until terminal
// composition consumes it.
type rangeRootTransactionProof struct {
	selected     rangeRootTransactionIdentity
	materialized rangeTreeMaterializedResult
	rangePages   []privateWriterProducedTerminalPage
	seed         *pageNumberIndex
	first        *pageNumberIndex
	second       *pageNumberIndex
	candidate    pageNumberIndexFixedPointCandidate
	protectedLen uint64
	seal         uint64
}

type rangeRootTransactionIdentity struct {
	txnID            uint64
	pageCount        uint64
	rangeRoot        uint32
	rangeRecordCount uint64
	addressFamily    AddressFamily
	valueKind        ValueKind
}

type rangeRootTransactionProofErrorCode uint8

const (
	rangeRootTransactionProofErrInvalidArgument rangeRootTransactionProofErrorCode = iota + 1
	rangeRootTransactionProofErrSelectedIdentity
	rangeRootTransactionProofErrRangeJournal
	rangeRootTransactionProofErrRangeRoot
	rangeRootTransactionProofErrOwnership
	rangeRootTransactionProofErrFixedPoint
	rangeRootTransactionProofErrProtectedOverlap
	rangeRootTransactionProofErrStale
)

type rangeRootTransactionProofError struct {
	code  rangeRootTransactionProofErrorCode
	page  uint32
	cause error
}

func (e *rangeRootTransactionProofError) Error() string {
	if e == nil {
		return "<nil>"
	}
	return fmt.Sprintf("exact v4 range-root transaction proof: error %d", e.code)
}

func (e *rangeRootTransactionProofError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func rangeRootTransactionIdentityFromMeta(
	selected Meta,
) (rangeRootTransactionIdentity, error) {
	if selected.TxnID == 0 || selected.PageCount < 2 ||
		(selected.RangeRoot == 0 && selected.RangeRecordCount != 0) ||
		(selected.RangeRoot != 0 &&
			(selected.RangeRoot < 2 || uint64(selected.RangeRoot) >= selected.PageCount)) {
		return rangeRootTransactionIdentity{}, &rangeRootTransactionProofError{
			code: rangeRootTransactionProofErrSelectedIdentity,
		}
	}
	return rangeRootTransactionIdentity{
		txnID:            selected.TxnID,
		pageCount:        selected.PageCount,
		rangeRoot:        selected.RangeRoot,
		rangeRecordCount: selected.RangeRecordCount,
		addressFamily:    selected.AddressFamily,
		valueKind:        selected.ValueKind,
	}, nil
}

func validateRangeRootTransactionJournal(
	materialized rangeTreeMaterializedResult,
	rangePages []privateWriterProducedTerminalPage,
) error {
	if materialized.pageCount < 0 || materialized.pageCount != len(rangePages) ||
		(materialized.rootPage == 0 &&
			(materialized.pageCount != 0 || materialized.recordCount != 0)) ||
		(materialized.rootPage != 0 && materialized.rootPage < 2) ||
		(materialized.recordCount != 0 && materialized.rootPage == 0) {
		return &rangeRootTransactionProofError{code: rangeRootTransactionProofErrRangeJournal}
	}
	if problem := validatePrivateWriterTerminalJournalSource(0, rangePages); problem.failed() {
		return &rangeRootTransactionProofError{
			code: rangeRootTransactionProofErrRangeJournal,
			page: problem.page,
		}
	}
	foundRoot := materialized.rootPage == 0
	for _, page := range rangePages {
		if page.owner != privatePageOwnerRange || page.origin != privatePageRange {
			return &rangeRootTransactionProofError{
				code: rangeRootTransactionProofErrRangeJournal,
				page: page.pageNumber,
			}
		}
		if page.pageNumber == materialized.rootPage {
			foundRoot = true
		}
	}
	if !foundRoot {
		return &rangeRootTransactionProofError{
			code: rangeRootTransactionProofErrRangeRoot,
			page: materialized.rootPage,
		}
	}
	return nil
}

func rangeRootTransactionProofCandidate(
	candidate pageNumberIndexFixedPointCandidate,
	first, second *pageNumberIndex,
) *pageNumberIndex {
	switch candidate {
	case pageNumberIndexFixedPointFirst:
		return first
	case pageNumberIndexFixedPointSecond:
		return second
	default:
		return nil
	}
}

func rangeRootTransactionProofOtherCandidate(
	candidate pageNumberIndexFixedPointCandidate,
	first, second *pageNumberIndex,
) *pageNumberIndex {
	switch candidate {
	case pageNumberIndexFixedPointFirst:
		return second
	case pageNumberIndexFixedPointSecond:
		return first
	default:
		return nil
	}
}

func rangeRootTransactionProofDisjoint(
	rangePages []privateWriterProducedTerminalPage,
	protected *pageNumberIndex,
) error {
	cursor, err := newPageNumberIndexCursor(protected)
	if err != nil {
		return &rangeRootTransactionProofError{
			code:  rangeRootTransactionProofErrFixedPoint,
			cause: err,
		}
	}
	protectedPage, available, err := cursor.next()
	if err != nil {
		return &rangeRootTransactionProofError{
			code:  rangeRootTransactionProofErrFixedPoint,
			cause: err,
		}
	}
	for _, rangePage := range rangePages {
		for available && protectedPage < rangePage.pageNumber {
			protectedPage, available, err = cursor.next()
			if err != nil {
				return &rangeRootTransactionProofError{
					code:  rangeRootTransactionProofErrFixedPoint,
					cause: err,
				}
			}
		}
		if available && protectedPage == rangePage.pageNumber {
			return &rangeRootTransactionProofError{
				code: rangeRootTransactionProofErrProtectedOverlap,
				page: rangePage.pageNumber,
			}
		}
	}
	return nil
}

func rangeRootTransactionProofHashIndex(
	hash uint64,
	index *pageNumberIndex,
) (uint64, error) {
	cursor, err := newPageNumberIndexCursor(index)
	if err != nil {
		return 0, err
	}
	for {
		page, available, nextErr := cursor.next()
		if nextErr != nil {
			return 0, nextErr
		}
		if !available {
			return hash, nil
		}
		hash = privateWriterAggregateHashWord(hash, uint64(page))
	}
}

func sealRangeRootTransactionProof(
	selected rangeRootTransactionIdentity,
	materialized rangeTreeMaterializedResult,
	rangePages []privateWriterProducedTerminalPage,
	seed, protected *pageNumberIndex,
	candidate pageNumberIndexFixedPointCandidate,
) (uint64, error) {
	hash := privateWriterAggregateHashSeed ^ 0x98f0_4adf_c3e2_719b
	for _, value := range [...]uint64{
		selected.txnID,
		selected.pageCount,
		uint64(selected.rangeRoot),
		selected.rangeRecordCount,
		uint64(selected.addressFamily),
		uint64(selected.valueKind),
		uint64(materialized.rootPage),
		uint64(materialized.rootLevel),
		materialized.recordCount,
		uint64(materialized.pageCount),
		uint64(candidate),
		uint64(len(rangePages)),
	} {
		hash = privateWriterAggregateHashWord(hash, value)
	}
	for _, page := range rangePages {
		hash = privateWriterProducedPageHash(hash, page)
	}
	if hash, err := rangeRootTransactionProofHashIndex(hash, seed); err != nil {
		return 0, err
	} else {
		return rangeRootTransactionProofHashIndex(hash, protected)
	}
}

func (proof *rangeRootTransactionProof) protectedIndex() (*pageNumberIndex, error) {
	if proof == nil || proof.seed == nil || proof.first == nil || proof.second == nil ||
		proof.seed == proof.first || proof.seed == proof.second || proof.first == proof.second {
		return nil, &rangeRootTransactionProofError{code: rangeRootTransactionProofErrStale}
	}
	protected := rangeRootTransactionProofCandidate(proof.candidate, proof.first, proof.second)
	other := rangeRootTransactionProofOtherCandidate(proof.candidate, proof.first, proof.second)
	if protected == nil || other == nil || !other.isEmptyAndClean() ||
		protected.len() != proof.protectedLen ||
		validatePageNumberIndexCommittedRange(proof.seed, proof.selected.pageCount) != nil ||
		validatePageNumberIndexCommittedRange(protected, proof.selected.pageCount) != nil ||
		validateRangeRootTransactionJournal(proof.materialized, proof.rangePages) != nil ||
		rangeRootTransactionProofDisjoint(proof.rangePages, protected) != nil {
		return nil, &rangeRootTransactionProofError{code: rangeRootTransactionProofErrStale}
	}
	seal, err := sealRangeRootTransactionProof(
		proof.selected, proof.materialized, proof.rangePages, proof.seed, protected, proof.candidate,
	)
	if err != nil || seal != proof.seal {
		return nil, &rangeRootTransactionProofError{code: rangeRootTransactionProofErrStale, cause: err}
	}
	return protected, nil
}

func (proof *rangeRootTransactionProof) discardAfterAbort() {
	if proof == nil {
		return
	}
	proof.seed.discardAfterAbort()
	proof.first.discardAfterAbort()
	proof.second.discardAfterAbort()
	*proof = rangeRootTransactionProof{}
}

// prepareRangeRootTransactionProof collects the selected old range tree,
// converges every prospective committed replacement page, and retains only
// private evidence. It deliberately does not create retirement output, bind a
// terminal journal, change target metadata, or publish a root.
func prepareRangeRootTransactionProof[K rangeKey[K], S committedPageSource](
	source S,
	selected Meta,
	materialized rangeTreeMaterializedResult,
	rangePages []privateWriterProducedTerminalPage,
	seed, first, second *pageNumberIndex,
	ownershipScratch *rangeTreeOwnershipScratch,
	maxRangeWork uint64,
	maxIterations int,
	preview pageNumberIndexFixedPointPreview,
) (rangeRootTransactionProof, error) {
	identity, err := rangeRootTransactionIdentityFromMeta(selected)
	if err != nil {
		return rangeRootTransactionProof{}, err
	}
	if seed == nil || first == nil || second == nil || ownershipScratch == nil ||
		seed == first || seed == second || first == second ||
		seed.workspace == nil || first.workspace == nil || second.workspace == nil ||
		seed.workspace == first.workspace || seed.workspace == second.workspace || first.workspace == second.workspace ||
		!seed.isEmptyAndClean() || !first.isEmptyAndClean() || !second.isEmptyAndClean() ||
		preview == nil || maxIterations <= 0 {
		return rangeRootTransactionProof{}, &rangeRootTransactionProofError{
			code: rangeRootTransactionProofErrInvalidArgument,
		}
	}
	if err = validateRangeRootTransactionJournal(materialized, rangePages); err != nil {
		return rangeRootTransactionProof{}, err
	}
	completed := false
	defer func() {
		if !completed {
			seed.discardAfterAbort()
			first.discardAfterAbort()
			second.discardAfterAbort()
		}
	}()
	if _, err = collectRangeTreeOwnership[K](source, selected, seed, ownershipScratch, maxRangeWork); err != nil {
		return rangeRootTransactionProof{}, &rangeRootTransactionProofError{
			code:  rangeRootTransactionProofErrOwnership,
			cause: err,
		}
	}
	candidate, err := convergePageNumberIndex(
		seed, first, second, identity.pageCount, maxIterations, preview,
	)
	if err != nil {
		return rangeRootTransactionProof{}, &rangeRootTransactionProofError{
			code:  rangeRootTransactionProofErrFixedPoint,
			cause: err,
		}
	}
	protected := rangeRootTransactionProofCandidate(candidate, first, second)
	other := rangeRootTransactionProofOtherCandidate(candidate, first, second)
	if protected == nil || other == nil || !other.isEmptyAndClean() ||
		validatePageNumberIndexCommittedRange(protected, identity.pageCount) != nil {
		return rangeRootTransactionProof{}, &rangeRootTransactionProofError{
			code: rangeRootTransactionProofErrFixedPoint,
		}
	}
	if err = rangeRootTransactionProofDisjoint(rangePages, protected); err != nil {
		return rangeRootTransactionProof{}, err
	}
	seal, err := sealRangeRootTransactionProof(identity, materialized, rangePages, seed, protected, candidate)
	if err != nil {
		return rangeRootTransactionProof{}, &rangeRootTransactionProofError{
			code:  rangeRootTransactionProofErrFixedPoint,
			cause: err,
		}
	}
	completed = true
	return rangeRootTransactionProof{
		selected: identity, materialized: materialized, rangePages: rangePages,
		seed: seed, first: first, second: second, candidate: candidate,
		protectedLen: protected.len(), seal: seal,
	}, nil
}
