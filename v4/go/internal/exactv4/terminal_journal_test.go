package exactv4

import "testing"

func terminalJournalPage(
	pageNumber uint32,
	owner privatePageOwner,
	origin privatePageOrigin,
) privateWriterProducedTerminalPage {
	return privateWriterProducedTerminalPage{
		pageNumber: pageNumber, authorization: privatePageCommittedFree,
		owner: owner, origin: origin,
	}
}

func TestPrivateWriterTerminalJournalMergeOrdersThreeSourcesWithoutAllocation(t *testing.T) {
	rangePages := []privateWriterProducedTerminalPage{
		terminalJournalPage(2, privatePageOwnerRange, privatePageRange),
		terminalJournalPage(8, privatePageOwnerRange, privatePageRange),
	}
	bitmapPages := []privateWriterProducedTerminalPage{
		terminalJournalPage(3, privatePageOwnerBitmap, privatePageBitmap),
		terminalJournalPage(9, privatePageOwnerBitmap, privatePageBitmap),
	}
	retirementPages := []privateWriterProducedTerminalPage{
		terminalJournalPage(4, privatePageOwnerRetirement, privatePageRetirementTree),
		terminalJournalPage(10, privatePageOwnerRetirement, privatePageRetirementBlob),
	}
	var output [6]privateWriterProducedTerminalPage
	sources := [3][]privateWriterProducedTerminalPage{rangePages, bitmapPages, retirementPages}
	var problem privateWriterTerminalJournalError
	allocations := testing.AllocsPerRun(100, func() {
		clear(output[:])
		problem = mergePrivateWriterTerminalJournals(sources, output[:])
	})
	if problem.failed() || allocations != 0 {
		t.Fatalf("merge problem=%+v allocations=%v", problem, allocations)
	}
	for index, want := range [...]uint32{2, 3, 4, 8, 9, 10} {
		if output[index].pageNumber != want {
			t.Fatalf("page %d = %d, want %d", index, output[index].pageNumber, want)
		}
	}
}

func TestPrivateWriterTerminalJournalMergeRejectsBeforeOutputMutation(t *testing.T) {
	rangePages := []privateWriterProducedTerminalPage{
		terminalJournalPage(4, privatePageOwnerRange, privatePageRange),
	}
	bitmapDuplicate := []privateWriterProducedTerminalPage{
		terminalJournalPage(4, privatePageOwnerBitmap, privatePageBitmap),
	}
	var output [2]privateWriterProducedTerminalPage
	before := output
	problem := mergePrivateWriterTerminalJournals(
		[3][]privateWriterProducedTerminalPage{rangePages, bitmapDuplicate, nil}, output[:],
	)
	if problem.code != privateWriterTerminalJournalErrDuplicatePage || problem.page != 4 {
		t.Fatalf("duplicate problem=%+v", problem)
	}
	if output != before {
		t.Fatal("duplicate merge changed output")
	}

	bitmapPages := []privateWriterProducedTerminalPage{
		terminalJournalPage(5, privatePageOwnerBitmap, privatePageBitmap),
	}
	var shortOutput [1]privateWriterProducedTerminalPage
	beforeShort := shortOutput
	problem = mergePrivateWriterTerminalJournals(
		[3][]privateWriterProducedTerminalPage{rangePages, bitmapPages, nil}, shortOutput[:],
	)
	if problem.code != privateWriterTerminalJournalErrOutputLength ||
		problem.required != 2 || problem.actual != 1 {
		t.Fatalf("length problem=%+v", problem)
	}
	if shortOutput != beforeShort {
		t.Fatal("wrong-sized merge changed output")
	}

	output[0] = terminalJournalPage(99, privatePageOwnerBitmap, privatePageBitmap)
	before = output
	problem = mergePrivateWriterTerminalJournals(
		[3][]privateWriterProducedTerminalPage{rangePages, bitmapPages, nil}, output[:],
	)
	if problem.code != privateWriterTerminalJournalErrOutputDirty {
		t.Fatalf("dirty problem=%+v", problem)
	}
	if output != before {
		t.Fatal("dirty merge changed output")
	}

	unordered := []privateWriterProducedTerminalPage{
		terminalJournalPage(7, privatePageOwnerBitmap, privatePageBitmap),
		terminalJournalPage(6, privatePageOwnerBitmap, privatePageBitmap),
	}
	var exactOutput [3]privateWriterProducedTerminalPage
	beforeExact := exactOutput
	problem = mergePrivateWriterTerminalJournals(
		[3][]privateWriterProducedTerminalPage{rangePages, unordered, nil}, exactOutput[:],
	)
	if problem.code != privateWriterTerminalJournalErrSourceOrder ||
		problem.source != 1 || problem.previous != 7 || problem.page != 6 {
		t.Fatalf("order problem=%+v", problem)
	}
	if exactOutput != beforeExact {
		t.Fatal("unordered merge changed output")
	}
}
