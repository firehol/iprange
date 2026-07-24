package exactv4

import "fmt"

// rangeTreePoolSinkErrorCode identifies failures at the private builder/pool
// boundary. It is intentionally separate from range-tree input errors.
type rangeTreePoolSinkErrorCode uint8

const (
	rangeTreePoolSinkErrPendingTransaction rangeTreePoolSinkErrorCode = iota + 1
	rangeTreePoolSinkErrPool
)

type rangeTreePoolSinkError struct {
	code        rangeTreePoolSinkErrorCode
	requested   uint64
	poolPending uint64
	poolProblem privatePagePoolError
}

func (e *rangeTreePoolSinkError) Error() string {
	if e == nil {
		return "<nil>"
	}
	return fmt.Sprintf("exact v4 range tree pool sink: error %d", e.code)
}

// rangeTreePoolSink gives one ordered range-tree build its actual
// transaction-private page destination. The surrounding checkpoint owns all
// cleanup: this sink never keeps a parallel journal or tries partial release.
type rangeTreePoolSink struct {
	pool       *privatePagePool
	checkpoint privatePagePoolCheckpoint
	bornTxn    uint64
}

func newRangeTreePoolSink(
	pool *privatePagePool,
	checkpoint privatePagePoolCheckpoint,
	bornTxn uint64,
) (rangeTreePoolSink, error) {
	if pool == nil || pool.self != pool {
		return rangeTreePoolSink{}, &rangeTreePoolSinkError{
			code:        rangeTreePoolSinkErrPool,
			poolProblem: privatePagePoolError{code: privatePagePoolErrCrossPool},
		}
	}
	if bornTxn != pool.pendingTxn {
		return rangeTreePoolSink{}, &rangeTreePoolSinkError{
			code:        rangeTreePoolSinkErrPendingTransaction,
			requested:   bornTxn,
			poolPending: pool.pendingTxn,
		}
	}
	if problem := pool.validateCheckpoint(checkpoint); problem.failed() {
		return rangeTreePoolSink{}, &rangeTreePoolSinkError{
			code: rangeTreePoolSinkErrPool, poolProblem: problem,
		}
	}
	return rangeTreePoolSink{pool: pool, checkpoint: checkpoint, bornTxn: bornTxn}, nil
}

func (s *rangeTreePoolSink) writeRangePage(page *[PageSize]byte) (uint32, error) {
	if s == nil || s.pool == nil {
		return 0, &rangeTreePoolSinkError{
			code:        rangeTreePoolSinkErrPool,
			poolProblem: privatePagePoolError{code: privatePagePoolErrCrossPool},
		}
	}
	// Check before claiming so a stale or foreign checkpoint cannot strand a
	// page. This is a constant-time capability check, not file validation.
	if problem := s.pool.validateCheckpoint(s.checkpoint); problem.failed() {
		return 0, &rangeTreePoolSinkError{code: rangeTreePoolSinkErrPool, poolProblem: problem}
	}
	token, problem := s.pool.claimLowest(
		s.checkpoint, privatePageOwnerRange, privatePageRange,
	)
	if problem.failed() {
		return 0, &rangeTreePoolSinkError{code: rangeTreePoolSinkErrPool, poolProblem: problem}
	}
	pageNumber := s.pool.slots[token.slot].pageNumber
	if problem = s.pool.writePage(token, page); problem.failed() {
		return 0, &rangeTreePoolSinkError{code: rangeTreePoolSinkErrPool, poolProblem: problem}
	}
	return pageNumber, nil
}
