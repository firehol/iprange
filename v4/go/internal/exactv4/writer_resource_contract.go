package exactv4

// privateWriterResourceBudget is copied into one writer resource ledger and
// remains fixed for that ledger's lifetime.
type privateWriterResourceBudget struct {
	maxHeapBytes       uint64
	maxPrivatePages    uint64
	maxFileGrowthPages uint64
	maxOpenFiles       uint32
}

type privateWriterResourceUsage struct {
	heapBytes       uint64
	privatePages    uint64
	fileGrowthPages uint64
	openFiles       uint32
}

type privateWriterResourceDimension uint8

const (
	privateWriterResourceHeap privateWriterResourceDimension = iota + 1
	privateWriterResourcePrivatePages
	privateWriterResourceFileGrowthPages
	privateWriterResourceOpenFiles
)

type privateWriterResourceErrorCode uint8

const (
	privateWriterResourceErrInvalidState privateWriterResourceErrorCode = iota + 1
	privateWriterResourceErrArithmeticOverflow
	privateWriterResourceErrInsufficientBudget
	privateWriterResourceErrUnderflow
	privateWriterResourceErrGrowthExceedsPrivate
)

type privateWriterResourceError struct {
	code      privateWriterResourceErrorCode
	dimension privateWriterResourceDimension
	current   uint64
	change    uint64
	limit     uint64
}

func (e privateWriterResourceError) failed() bool { return e.code != 0 }

// privateWriterResourceLedger tracks simultaneously retained transaction
// resources. Acquire and release preflight every dimension before changing any.
type privateWriterResourceLedger struct {
	self   *privateWriterResourceLedger
	budget privateWriterResourceBudget
	usage  privateWriterResourceUsage
}

func initPrivateWriterResourceLedger(
	ledger *privateWriterResourceLedger,
	budget privateWriterResourceBudget,
) privateWriterResourceError {
	if ledger == nil || *ledger != (privateWriterResourceLedger{}) {
		return privateWriterResourceError{code: privateWriterResourceErrInvalidState}
	}
	*ledger = privateWriterResourceLedger{self: ledger, budget: budget}
	return privateWriterResourceError{}
}

func (ledger *privateWriterResourceLedger) acquire(
	change privateWriterResourceUsage,
) privateWriterResourceError {
	if ledger == nil || ledger.self != ledger {
		return privateWriterResourceError{code: privateWriterResourceErrInvalidState}
	}
	nextHeap, problem := checkedWriterResourceAcquire(
		ledger.usage.heapBytes, change.heapBytes, ledger.budget.maxHeapBytes,
		privateWriterResourceHeap,
	)
	if problem.failed() {
		return problem
	}
	nextPrivate, problem := checkedWriterResourceAcquire(
		ledger.usage.privatePages, change.privatePages, ledger.budget.maxPrivatePages,
		privateWriterResourcePrivatePages,
	)
	if problem.failed() {
		return problem
	}
	nextGrowth, problem := checkedWriterResourceAcquire(
		ledger.usage.fileGrowthPages, change.fileGrowthPages, ledger.budget.maxFileGrowthPages,
		privateWriterResourceFileGrowthPages,
	)
	if problem.failed() {
		return problem
	}
	nextOpen, problem := checkedWriterOpenFilesAcquire(
		ledger.usage.openFiles, change.openFiles, ledger.budget.maxOpenFiles,
	)
	if problem.failed() {
		return problem
	}
	if nextGrowth > nextPrivate {
		return privateWriterResourceError{
			code: privateWriterResourceErrGrowthExceedsPrivate, dimension: privateWriterResourceFileGrowthPages,
			current: ledger.usage.fileGrowthPages, change: change.fileGrowthPages, limit: nextPrivate,
		}
	}
	ledger.usage = privateWriterResourceUsage{
		heapBytes: nextHeap, privatePages: nextPrivate,
		fileGrowthPages: nextGrowth, openFiles: nextOpen,
	}
	return privateWriterResourceError{}
}

func (ledger *privateWriterResourceLedger) release(
	change privateWriterResourceUsage,
) privateWriterResourceError {
	if ledger == nil || ledger.self != ledger {
		return privateWriterResourceError{code: privateWriterResourceErrInvalidState}
	}
	nextHeap, problem := checkedWriterResourceRelease(
		ledger.usage.heapBytes, change.heapBytes, privateWriterResourceHeap,
	)
	if problem.failed() {
		return problem
	}
	nextPrivate, problem := checkedWriterResourceRelease(
		ledger.usage.privatePages, change.privatePages, privateWriterResourcePrivatePages,
	)
	if problem.failed() {
		return problem
	}
	nextGrowth, problem := checkedWriterResourceRelease(
		ledger.usage.fileGrowthPages, change.fileGrowthPages, privateWriterResourceFileGrowthPages,
	)
	if problem.failed() {
		return problem
	}
	nextOpen, problem := checkedWriterResourceRelease(
		uint64(ledger.usage.openFiles), uint64(change.openFiles), privateWriterResourceOpenFiles,
	)
	if problem.failed() {
		return problem
	}
	if nextGrowth > nextPrivate {
		return privateWriterResourceError{
			code: privateWriterResourceErrGrowthExceedsPrivate, dimension: privateWriterResourcePrivatePages,
			current: ledger.usage.privatePages, change: change.privatePages, limit: nextGrowth,
		}
	}
	ledger.usage = privateWriterResourceUsage{
		heapBytes: nextHeap, privatePages: nextPrivate,
		fileGrowthPages: nextGrowth, openFiles: uint32(nextOpen),
	}
	return privateWriterResourceError{}
}

func (ledger *privateWriterResourceLedger) empty() bool {
	return ledger != nil && ledger.self == ledger && ledger.usage == (privateWriterResourceUsage{})
}

func checkedWriterResourceAcquire(
	current uint64,
	change uint64,
	limit uint64,
	dimension privateWriterResourceDimension,
) (uint64, privateWriterResourceError) {
	if change > ^uint64(0)-current {
		return 0, privateWriterResourceError{
			code: privateWriterResourceErrArithmeticOverflow, dimension: dimension,
			current: current, change: change, limit: limit,
		}
	}
	next := current + change
	if next > limit {
		return 0, privateWriterResourceError{
			code: privateWriterResourceErrInsufficientBudget, dimension: dimension,
			current: current, change: change, limit: limit,
		}
	}
	return next, privateWriterResourceError{}
}

func checkedWriterOpenFilesAcquire(
	current uint32,
	change uint32,
	limit uint32,
) (uint32, privateWriterResourceError) {
	if change > ^uint32(0)-current {
		return 0, privateWriterResourceError{
			code: privateWriterResourceErrArithmeticOverflow, dimension: privateWriterResourceOpenFiles,
			current: uint64(current), change: uint64(change), limit: uint64(limit),
		}
	}
	next := current + change
	if next > limit {
		return 0, privateWriterResourceError{
			code: privateWriterResourceErrInsufficientBudget, dimension: privateWriterResourceOpenFiles,
			current: uint64(current), change: uint64(change), limit: uint64(limit),
		}
	}
	return next, privateWriterResourceError{}
}

func checkedWriterResourceRelease(
	current uint64,
	change uint64,
	dimension privateWriterResourceDimension,
) (uint64, privateWriterResourceError) {
	if change > current {
		return 0, privateWriterResourceError{
			code: privateWriterResourceErrUnderflow, dimension: dimension,
			current: current, change: change,
		}
	}
	return current - change, privateWriterResourceError{}
}
