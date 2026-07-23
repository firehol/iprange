package exactv4

import "testing"

func initPrivateWriterResourceTestLedger(
	t *testing.T,
	ledger *privateWriterResourceLedger,
	budget privateWriterResourceBudget,
) {
	t.Helper()
	if problem := initPrivateWriterResourceLedger(ledger, budget); problem.failed() {
		t.Fatal(problem)
	}
}

func TestPrivateWriterResourceLedgerExactLimitsAndOneOver(t *testing.T) {
	budget := privateWriterResourceBudget{
		maxHeapBytes: 10, maxPrivatePages: 8, maxFileGrowthPages: 5, maxOpenFiles: 3,
	}
	var ledger privateWriterResourceLedger
	initPrivateWriterResourceTestLedger(t, &ledger, budget)
	exact := privateWriterResourceUsage{
		heapBytes: 10, privatePages: 8, fileGrowthPages: 5, openFiles: 3,
	}
	if problem := ledger.acquire(exact); problem.failed() || ledger.usage != exact {
		t.Fatalf("exact acquire = usage %#v error %+v", ledger.usage, problem)
	}
	if problem := ledger.release(exact); problem.failed() || !ledger.empty() {
		t.Fatalf("exact release = usage %#v error %+v", ledger.usage, problem)
	}

	tests := []struct {
		name      string
		change    privateWriterResourceUsage
		dimension privateWriterResourceDimension
	}{
		{"heap", privateWriterResourceUsage{heapBytes: 11}, privateWriterResourceHeap},
		{"private", privateWriterResourceUsage{privatePages: 9}, privateWriterResourcePrivatePages},
		{
			"growth",
			privateWriterResourceUsage{privatePages: 6, fileGrowthPages: 6},
			privateWriterResourceFileGrowthPages,
		},
		{"open", privateWriterResourceUsage{openFiles: 4}, privateWriterResourceOpenFiles},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var ledger privateWriterResourceLedger
			initPrivateWriterResourceTestLedger(t, &ledger, budget)
			before := ledger.usage
			problem := ledger.acquire(test.change)
			if problem.code != privateWriterResourceErrInsufficientBudget ||
				problem.dimension != test.dimension {
				t.Fatalf("one-over = %+v", problem)
			}
			if ledger.usage != before {
				t.Fatalf("one-over changed usage: got %#v want %#v", ledger.usage, before)
			}
		})
	}
}

func TestPrivateWriterResourceLedgerOverflowUnderflowAndGrowthInvariantAreAtomic(t *testing.T) {
	maxBudget := privateWriterResourceBudget{
		maxHeapBytes: ^uint64(0), maxPrivatePages: ^uint64(0),
		maxFileGrowthPages: ^uint64(0), maxOpenFiles: ^uint32(0),
	}
	overflows := []struct {
		name      string
		base      privateWriterResourceUsage
		change    privateWriterResourceUsage
		dimension privateWriterResourceDimension
	}{
		{
			"heap",
			privateWriterResourceUsage{heapBytes: ^uint64(0) - 1},
			privateWriterResourceUsage{heapBytes: 2},
			privateWriterResourceHeap,
		},
		{
			"private",
			privateWriterResourceUsage{privatePages: ^uint64(0) - 1},
			privateWriterResourceUsage{privatePages: 2},
			privateWriterResourcePrivatePages,
		},
		{
			"growth",
			privateWriterResourceUsage{
				privatePages: ^uint64(0), fileGrowthPages: ^uint64(0) - 1,
			},
			privateWriterResourceUsage{fileGrowthPages: 2},
			privateWriterResourceFileGrowthPages,
		},
		{
			"open",
			privateWriterResourceUsage{openFiles: ^uint32(0)},
			privateWriterResourceUsage{openFiles: 1},
			privateWriterResourceOpenFiles,
		},
	}
	for _, test := range overflows {
		t.Run(test.name, func(t *testing.T) {
			var ledger privateWriterResourceLedger
			initPrivateWriterResourceTestLedger(t, &ledger, maxBudget)
			if problem := ledger.acquire(test.base); problem.failed() {
				t.Fatal(problem)
			}
			before := ledger.usage
			if problem := ledger.acquire(test.change); problem.code != privateWriterResourceErrArithmeticOverflow ||
				problem.dimension != test.dimension {
				t.Fatalf("overflow = %+v", problem)
			}
			if ledger.usage != before {
				t.Fatalf("overflow changed usage: got %#v want %#v", ledger.usage, before)
			}
		})
	}

	var releaseLedger privateWriterResourceLedger
	initPrivateWriterResourceTestLedger(t, &releaseLedger, privateWriterResourceBudget{
		maxHeapBytes: 10, maxPrivatePages: 10, maxFileGrowthPages: 10, maxOpenFiles: 10,
	})
	base := privateWriterResourceUsage{
		heapBytes: 5, privatePages: 5, fileGrowthPages: 3, openFiles: 2,
	}
	if problem := releaseLedger.acquire(base); problem.failed() {
		t.Fatal(problem)
	}
	underflows := []struct {
		change    privateWriterResourceUsage
		dimension privateWriterResourceDimension
	}{
		{privateWriterResourceUsage{heapBytes: 6}, privateWriterResourceHeap},
		{privateWriterResourceUsage{privatePages: 6}, privateWriterResourcePrivatePages},
		{privateWriterResourceUsage{fileGrowthPages: 4}, privateWriterResourceFileGrowthPages},
		{privateWriterResourceUsage{openFiles: 3}, privateWriterResourceOpenFiles},
	}
	for _, test := range underflows {
		before := releaseLedger.usage
		problem := releaseLedger.release(test.change)
		if problem.code != privateWriterResourceErrUnderflow ||
			problem.dimension != test.dimension {
			t.Fatalf("underflow = %+v", problem)
		}
		if releaseLedger.usage != before {
			t.Fatalf("underflow changed usage: got %#v want %#v", releaseLedger.usage, before)
		}
	}
	before := releaseLedger.usage
	if problem := releaseLedger.release(privateWriterResourceUsage{
		privatePages: 3,
	}); problem.code != privateWriterResourceErrGrowthExceedsPrivate ||
		problem.dimension != privateWriterResourcePrivatePages {
		t.Fatalf("growth/private release = %+v", problem)
	}
	if releaseLedger.usage != before {
		t.Fatalf("growth/private rejection changed usage: got %#v want %#v", releaseLedger.usage, before)
	}

	var acquireLedger privateWriterResourceLedger
	initPrivateWriterResourceTestLedger(t, &acquireLedger, privateWriterResourceBudget{
		maxPrivatePages: 10, maxFileGrowthPages: 10,
	})
	if problem := acquireLedger.acquire(privateWriterResourceUsage{
		fileGrowthPages: 1,
	}); problem.code != privateWriterResourceErrGrowthExceedsPrivate ||
		acquireLedger.usage != (privateWriterResourceUsage{}) {
		t.Fatalf("growth/private acquire = usage %#v error %+v", acquireLedger.usage, problem)
	}
}

func TestPrivateWriterResourceLedgerMultiDimensionFailureIsAtomicAndCumulative(t *testing.T) {
	var ledger privateWriterResourceLedger
	initPrivateWriterResourceTestLedger(t, &ledger, privateWriterResourceBudget{
		maxHeapBytes: 10, maxPrivatePages: 10, maxFileGrowthPages: 5, maxOpenFiles: 3,
	})
	if problem := ledger.acquire(privateWriterResourceUsage{
		heapBytes: 4, privatePages: 4, fileGrowthPages: 2, openFiles: 1,
	}); problem.failed() {
		t.Fatal(problem)
	}
	if problem := ledger.acquire(privateWriterResourceUsage{
		heapBytes: 4, privatePages: 2, fileGrowthPages: 1, openFiles: 1,
	}); problem.failed() {
		t.Fatal(problem)
	}
	want := privateWriterResourceUsage{
		heapBytes: 8, privatePages: 6, fileGrowthPages: 3, openFiles: 2,
	}
	if ledger.usage != want {
		t.Fatalf("cumulative usage = got %#v want %#v", ledger.usage, want)
	}

	before := ledger.usage
	problem := ledger.acquire(privateWriterResourceUsage{
		heapBytes: 1, privatePages: 1, fileGrowthPages: 1, openFiles: 2,
	})
	if problem.code != privateWriterResourceErrInsufficientBudget ||
		problem.dimension != privateWriterResourceOpenFiles {
		t.Fatalf("multi-dimensional failure = %+v", problem)
	}
	if ledger.usage != before {
		t.Fatalf("multi-dimensional failure changed usage: got %#v want %#v", ledger.usage, before)
	}
	if problem = ledger.acquire(privateWriterResourceUsage{
		heapBytes: 3,
	}); problem.code != privateWriterResourceErrInsufficientBudget ||
		ledger.usage != before {
		t.Fatalf("per-call reset bypassed cumulative use: usage %#v error %+v", ledger.usage, problem)
	}
}

func TestPrivateWriterResourceLedgerCannotBeReinitializedToResetUsageOrBudget(t *testing.T) {
	var ledger privateWriterResourceLedger
	originalBudget := privateWriterResourceBudget{
		maxHeapBytes: 10, maxPrivatePages: 10, maxFileGrowthPages: 5, maxOpenFiles: 2,
	}
	initPrivateWriterResourceTestLedger(t, &ledger, originalBudget)
	if problem := ledger.acquire(privateWriterResourceUsage{
		heapBytes: 7, privatePages: 4, fileGrowthPages: 2, openFiles: 1,
	}); problem.failed() {
		t.Fatal(problem)
	}
	beforeUsage := ledger.usage
	if problem := initPrivateWriterResourceLedger(
		&ledger,
		privateWriterResourceBudget{
			maxHeapBytes: ^uint64(0), maxPrivatePages: ^uint64(0),
			maxFileGrowthPages: ^uint64(0), maxOpenFiles: ^uint32(0),
		},
	); problem.code != privateWriterResourceErrInvalidState {
		t.Fatalf("active ledger reinit = %+v", problem)
	}
	if ledger.budget != originalBudget || ledger.usage != beforeUsage {
		t.Fatalf("reinit changed ledger: budget %#v usage %#v", ledger.budget, ledger.usage)
	}
}

func TestPrivateWriterResourceLedgerPreparedOperationsAllocateNothing(t *testing.T) {
	var ledger privateWriterResourceLedger
	budget := privateWriterResourceBudget{
		maxHeapBytes: 8, maxPrivatePages: 8, maxFileGrowthPages: 4, maxOpenFiles: 2,
	}
	change := privateWriterResourceUsage{
		heapBytes: 8, privatePages: 8, fileGrowthPages: 4, openFiles: 2,
	}
	if problem := initPrivateWriterResourceLedger(&ledger, budget); problem.failed() {
		t.Fatal(problem)
	}
	allocations := testing.AllocsPerRun(1000, func() {
		if problem := ledger.acquire(change); problem.failed() {
			panic(problem)
		}
		if problem := ledger.release(change); problem.failed() {
			panic(problem)
		}
	})
	if allocations != 0 {
		t.Fatalf("resource ledger allocations = %f", allocations)
	}
}

func TestPrivateWriterResourceLedgerFailurePathsAllocateNothing(t *testing.T) {
	assertZero := func(name string, operation func()) {
		t.Helper()
		if allocations := testing.AllocsPerRun(1000, operation); allocations != 0 {
			t.Fatalf("%s allocations = %f", name, allocations)
		}
	}
	budget := privateWriterResourceBudget{
		maxHeapBytes: 8, maxPrivatePages: 8, maxFileGrowthPages: 8, maxOpenFiles: 2,
	}

	var overBudget privateWriterResourceLedger
	initPrivateWriterResourceTestLedger(t, &overBudget, budget)
	if problem := overBudget.acquire(privateWriterResourceUsage{heapBytes: 8}); problem.failed() {
		t.Fatal(problem)
	}
	var overBudgetProblem privateWriterResourceError
	assertZero("over budget", func() {
		overBudgetProblem = overBudget.acquire(privateWriterResourceUsage{heapBytes: 1})
	})
	if overBudgetProblem.code != privateWriterResourceErrInsufficientBudget {
		t.Fatalf("over-budget result = %+v", overBudgetProblem)
	}

	var openOverflow privateWriterResourceLedger
	initPrivateWriterResourceTestLedger(t, &openOverflow, privateWriterResourceBudget{
		maxOpenFiles: ^uint32(0),
	})
	if problem := openOverflow.acquire(privateWriterResourceUsage{
		openFiles: ^uint32(0),
	}); problem.failed() {
		t.Fatal(problem)
	}
	openOverflowBefore := openOverflow.usage
	var openOverflowProblem privateWriterResourceError
	assertZero("open-file native overflow", func() {
		openOverflowProblem = openOverflow.acquire(privateWriterResourceUsage{openFiles: 1})
	})
	if openOverflowProblem.code != privateWriterResourceErrArithmeticOverflow ||
		openOverflowProblem.dimension != privateWriterResourceOpenFiles ||
		openOverflow.usage != openOverflowBefore {
		t.Fatalf(
			"open-file overflow = usage %#v error %+v",
			openOverflow.usage, openOverflowProblem,
		)
	}

	var underflow privateWriterResourceLedger
	initPrivateWriterResourceTestLedger(t, &underflow, budget)
	var underflowProblem privateWriterResourceError
	assertZero("underflow", func() {
		underflowProblem = underflow.release(privateWriterResourceUsage{heapBytes: 1})
	})
	if underflowProblem.code != privateWriterResourceErrUnderflow {
		t.Fatalf("underflow result = %+v", underflowProblem)
	}

	var growthInvariant privateWriterResourceLedger
	initPrivateWriterResourceTestLedger(t, &growthInvariant, budget)
	if problem := growthInvariant.acquire(privateWriterResourceUsage{
		privatePages: 4, fileGrowthPages: 4,
	}); problem.failed() {
		t.Fatal(problem)
	}
	var growthProblem privateWriterResourceError
	assertZero("growth invariant", func() {
		growthProblem = growthInvariant.release(privateWriterResourceUsage{privatePages: 1})
	})
	if growthProblem.code != privateWriterResourceErrGrowthExceedsPrivate {
		t.Fatalf("growth-invariant result = %+v", growthProblem)
	}

	var replacementBudgetProblem privateWriterResourceError
	assertZero("reinitialization rejection", func() {
		replacementBudgetProblem = initPrivateWriterResourceLedger(
			&growthInvariant,
			privateWriterResourceBudget{
				maxHeapBytes: ^uint64(0), maxPrivatePages: ^uint64(0),
				maxFileGrowthPages: ^uint64(0), maxOpenFiles: ^uint32(0),
			},
		)
	})
	if replacementBudgetProblem.code != privateWriterResourceErrInvalidState {
		t.Fatalf("reinitialization result = %+v", replacementBudgetProblem)
	}
}
