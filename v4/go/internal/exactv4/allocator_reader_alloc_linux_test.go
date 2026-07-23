//go:build linux

package exactv4

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func realPinnedPageSource(
	t *testing.T,
	data []byte,
	pageCount uint64,
) (*retainedRegular, pinnedPageSource, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "main.iprdb")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	retainedDir, component, openErr := openRetainedParent(path)
	if openErr != nil {
		t.Fatal(openErr)
	}
	t.Cleanup(func() { _ = retainedDir.file.Close() })
	retained, openErr := retainedDir.openRegular(component, false)
	if openErr != nil {
		t.Fatal(openErr)
	}
	t.Cleanup(func() { _ = retained.file.Close() })
	source, sourceErr := retained.pinnedPageSource(Bootstrap{
		Meta:           Meta{PageCount: pageCount},
		CommittedBytes: pageCount * PageSize,
	})
	if sourceErr != nil {
		t.Fatal(sourceErr)
	}
	return retained, source, path
}

func requireZeroAllocations(t *testing.T, operation func()) {
	t.Helper()
	if allocations := testing.AllocsPerRun(100, operation); allocations != 0 {
		t.Fatalf("allocations = %v, want 0", allocations)
	}
}

func TestLinuxRealPinnedPageSourceFailuresAllocateNothing(t *testing.T) {
	data := make([]byte, 4*PageSize)
	for index := 2 * PageSize; index < 3*PageSize; index++ {
		data[index] = 0x5a
	}

	t.Run("pid", func(t *testing.T) {
		_, source, _ := realPinnedPageSource(t, data, 4)
		access := source.source.access.(*processPageAccess)
		access.creatorPID++
		var page [PageSize]byte
		var status pageSourceStatus
		requireZeroAllocations(t, func() { status = source.readPageStatus(2, &page) })
		if status.code != pageSourceErrForkedHandle {
			t.Fatalf("status = %+v", status)
		}
	})

	t.Run("closed-fd", func(t *testing.T) {
		retained, source, _ := realPinnedPageSource(t, data, 4)
		if err := retained.file.Close(); err != nil {
			t.Fatal(err)
		}
		var page [PageSize]byte
		var status pageSourceStatus
		requireZeroAllocations(t, func() { status = source.readPageStatus(2, &page) })
		if status.code != pageSourceErrIO || !status.hasRaw || status.rawOSCode != uint64(unix.EBADF) {
			t.Fatalf("status = %+v", status)
		}
	})

	t.Run("truncated", func(t *testing.T) {
		_, source, path := realPinnedPageSource(t, data, 4)
		if err := os.Truncate(path, 2*PageSize+17); err != nil {
			t.Fatal(err)
		}
		var page [PageSize]byte
		var status pageSourceStatus
		requireZeroAllocations(t, func() { status = source.readPageStatus(2, &page) })
		if status.code != pageSourceErrShortRead || status.offset != 2*PageSize ||
			status.expected != PageSize || status.actual != 17 || status.hasRaw {
			t.Fatalf("status = %+v", status)
		}
	})
}

func TestLinuxRealPinnedFreeBitmapCOWFailuresAllocateNothing(t *testing.T) {
	data := make([]byte, 4*PageSize)
	putBitmapLeaf(t, data[2*PageSize:3*PageSize], bitmapKindFreePages, map[int]uint64{0: 1 << 3})

	for _, test := range []struct {
		name     string
		sabotage func(*testing.T, *retainedRegular, *pinnedPageSource, string)
		check    func(*testing.T, pageSourceStatus)
	}{
		{
			name: "pid",
			sabotage: func(_ *testing.T, _ *retainedRegular, source *pinnedPageSource, _ string) {
				source.source.access.(*processPageAccess).creatorPID++
			},
			check: func(t *testing.T, status pageSourceStatus) {
				if status.code != pageSourceErrForkedHandle {
					t.Fatalf("status = %+v", status)
				}
			},
		},
		{
			name: "closed-fd",
			sabotage: func(t *testing.T, retained *retainedRegular, _ *pinnedPageSource, _ string) {
				if err := retained.file.Close(); err != nil {
					t.Fatal(err)
				}
			},
			check: func(t *testing.T, status pageSourceStatus) {
				if status.code != pageSourceErrIO || !status.hasRaw || status.rawOSCode != uint64(unix.EBADF) {
					t.Fatalf("status = %+v", status)
				}
			},
		},
		{
			name: "truncated",
			sabotage: func(t *testing.T, _ *retainedRegular, _ *pinnedPageSource, path string) {
				if err := os.Truncate(path, 2*PageSize+17); err != nil {
					t.Fatal(err)
				}
			},
			check: func(t *testing.T, status pageSourceStatus) {
				if status.code != pageSourceErrShortRead || status.offset != 2*PageSize || status.actual != 17 {
					t.Fatalf("status = %+v", status)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			retained, source, path := realPinnedPageSource(t, data, 4)
			cow, problem := newFreeBitmapCOW(
				&source,
				1,
				4,
				2,
				emptyFreeBitmapCOWLedger(nil, nil, nil),
			)
			if problem.failed() {
				t.Fatal(problem)
			}
			test.sabotage(t, retained, &source, path)
			var status freeBitmapCOWError
			requireZeroAllocations(t, func() {
				_, _, status = cow.removeLowest()
			})
			if status.code != freeBitmapCOWErrSource {
				t.Fatalf("COW status = %+v", status)
			}
			test.check(t, status.source)
		})
	}
}

func TestLinuxRealPinnedBlobFailuresAllocateNothing(t *testing.T) {
	data := twoBlobLeafImage(t, blobLeafCapacity, blobLeafCapacity, 8)
	for _, test := range []struct {
		name     string
		sabotage func(*testing.T, *retainedRegular, *pinnedPageSource, string)
		check    func(*testing.T, blobReadStatus)
	}{
		{
			name: "pid",
			sabotage: func(_ *testing.T, _ *retainedRegular, source *pinnedPageSource, _ string) {
				source.source.access.(*processPageAccess).creatorPID++
			},
			check: func(t *testing.T, status blobReadStatus) {
				if status.code != blobReadErrSource || status.source.code != pageSourceErrForkedHandle {
					t.Fatalf("status = %+v", status)
				}
			},
		},
		{
			name: "closed-fd",
			sabotage: func(t *testing.T, retained *retainedRegular, _ *pinnedPageSource, _ string) {
				if err := retained.file.Close(); err != nil {
					t.Fatal(err)
				}
			},
			check: func(t *testing.T, status blobReadStatus) {
				if status.code != blobReadErrSource || status.source.code != pageSourceErrIO ||
					!status.source.hasRaw || status.source.rawOSCode != uint64(unix.EBADF) {
					t.Fatalf("status = %+v", status)
				}
			},
		},
		{
			name: "truncated",
			sabotage: func(t *testing.T, _ *retainedRegular, _ *pinnedPageSource, path string) {
				if err := os.Truncate(path, 2*PageSize+17); err != nil {
					t.Fatal(err)
				}
			},
			check: func(t *testing.T, status blobReadStatus) {
				if status.code != blobReadErrSource || status.source.code != pageSourceErrShortRead ||
					status.source.offset != 2*PageSize || status.source.actual != 17 {
					t.Fatalf("status = %+v", status)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			retained, source, path := realPinnedPageSource(t, data, 5)
			tree, status := newBlobTreeFromSourceStatus(
				&source,
				1,
				5,
				2,
				blobKindMembershipBitmap,
				blobLeafCapacity+8,
			)
			if status.failed() {
				t.Fatal(status)
			}
			test.sabotage(t, retained, &source, path)
			workspace := blobReadWorkspace[*pinnedPageSource]{}
			requireZeroAllocations(t, func() {
				reader := tree.streamWithWorkspace(blobPageCheckOrdinary, &workspace)
				_, _, status = reader.nextChunkStatus()
			})
			test.check(t, status)
		})
	}
}

func TestLinuxRealPinnedRetirementFailureStagesAllocateNothing(t *testing.T) {
	data := sampleRetirementImage(t)
	identity := testRetirementIdentity(20, 2, 3)

	t.Run("select-pid", func(t *testing.T) {
		_, source, _ := realPinnedPageSource(t, data, identity.pageCount)
		tree, err := newRetirementTreeFromSource(&source, identity)
		if err != nil {
			t.Fatal(err)
		}
		source.source.access.(*processPageAccess).creatorPID++
		workspace := retirementReadWorkspace[*pinnedPageSource]{}
		var status retirementReadStatus
		requireZeroAllocations(t, func() {
			_, _, status = tree.selectOldestEligibleWithWorkspace(4, 2, 3, &workspace)
		})
		if status.code != retirementReadErrSource || status.source.code != pageSourceErrForkedHandle {
			t.Fatalf("status = %+v", status)
		}
	})

	t.Run("verify-closed-fd", func(t *testing.T) {
		retained, source, _ := realPinnedPageSource(t, data, identity.pageCount)
		tree, err := newRetirementTreeFromSource(&source, identity)
		if err != nil {
			t.Fatal(err)
		}
		workspace := retirementReadWorkspace[*pinnedPageSource]{}
		selection, ok, status := tree.selectOldestEligibleWithWorkspace(4, 2, 3, &workspace)
		if status.failed() || !ok {
			t.Fatalf("selection = %t/%+v", ok, status)
		}
		if err := retained.file.Close(); err != nil {
			t.Fatal(err)
		}
		scratch := make([]retirementBatch, 2)
		requireZeroAllocations(t, func() {
			_, status = tree.verifySelectionWithWorkspace(selection, scratch, &workspace)
		})
		if status.code != retirementReadErrSource || status.source.code != pageSourceErrIO ||
			!status.source.hasRaw || status.source.rawOSCode != uint64(unix.EBADF) {
			t.Fatalf("status = %+v", status)
		}
	})

	t.Run("second-pass-truncated", func(t *testing.T) {
		_, source, path := realPinnedPageSource(t, data, identity.pageCount)
		tree, err := newRetirementTreeFromSource(&source, identity)
		if err != nil {
			t.Fatal(err)
		}
		workspace := retirementReadWorkspace[*pinnedPageSource]{}
		selection, ok, status := tree.selectOldestEligibleWithWorkspace(4, 2, 3, &workspace)
		if status.failed() || !ok {
			t.Fatalf("selection = %t/%+v", ok, status)
		}
		scratch := make([]retirementBatch, 2)
		verified, status := tree.verifySelectionWithWorkspace(selection, scratch, &workspace)
		if status.failed() {
			t.Fatal(status)
		}
		if err := os.Truncate(path, 3*PageSize+17); err != nil {
			t.Fatal(err)
		}
		sinkCalls := 0
		sink := retirementPageSink(func(retirementBatch, uint32) retirementSinkStatus {
			sinkCalls++
			return retirementSinkStatus{}
		})
		var passStatus retirementSecondPassStatus
		requireZeroAllocations(t, func() {
			sinkCalls = 0
			_, passStatus = verified.secondPassWithWorkspace(&tree, &workspace, sink)
			if sinkCalls != 0 {
				panic("second pass reached sink")
			}
		})
		if passStatus.code != retirementSecondPassErrRead ||
			passStatus.read.code != retirementReadErrBlob ||
			passStatus.read.blobProblem.source.code != pageSourceErrShortRead ||
			passStatus.read.blobProblem.source.offset != 3*PageSize ||
			passStatus.read.blobProblem.source.actual != 17 {
			t.Fatalf("status = %+v", passStatus)
		}
	})
}

func TestLinuxRealPinnedRetirementWorkDoesNotAllocatePerBatchOrPage(t *testing.T) {
	type fixture struct {
		name      string
		data      []byte
		identity  retirementIdentity
		threshold uint64
		batches   uint64
		pages     uint64
	}

	one := retirementImage(12)
	putRetirementLeafPage(t, retirementImagePage(one, 2), []retirementBatch{{
		retiredByTxn:     2,
		pageCount:        1,
		pageListBlobRoot: 3,
	}})
	putRetirementBlob(t, retirementImagePage(one, 3), []uint32{4})

	many := retirementImage(50)
	putRetirementLeafPage(t, retirementImagePage(many, 2), []retirementBatch{
		{retiredByTxn: 2, pageCount: 10, pageListBlobRoot: 3},
		{retiredByTxn: 4, pageCount: 10, pageListBlobRoot: 4},
		{retiredByTxn: 6, pageCount: 10, pageListBlobRoot: 5},
	})
	first := make([]uint32, 10)
	second := make([]uint32, 10)
	third := make([]uint32, 10)
	for index := range 10 {
		first[index] = uint32(10 + index)
		second[index] = uint32(20 + index)
		third[index] = uint32(30 + index)
	}
	putRetirementBlob(t, retirementImagePage(many, 3), first)
	putRetirementBlob(t, retirementImagePage(many, 4), second)
	putRetirementBlob(t, retirementImagePage(many, 5), third)

	for _, test := range []fixture{
		{name: "one", data: one, identity: testRetirementIdentity(12, 2, 1), threshold: 2, batches: 1, pages: 1},
		{name: "many", data: many, identity: testRetirementIdentity(50, 2, 3), threshold: 6, batches: 3, pages: 30},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, source, _ := realPinnedPageSource(t, test.data, test.identity.pageCount)
			tree, err := newRetirementTreeFromSource(&source, test.identity)
			if err != nil {
				t.Fatal(err)
			}
			workspace := retirementReadWorkspace[*pinnedPageSource]{}
			scratch := make([]retirementBatch, test.batches)
			sinkCalls := uint64(0)
			sink := retirementPageSink(func(retirementBatch, uint32) retirementSinkStatus {
				sinkCalls++
				return retirementSinkStatus{}
			})
			requireZeroAllocations(t, func() {
				sinkCalls = 0
				selection, ok, status := tree.selectOldestEligibleWithWorkspace(
					test.threshold,
					test.batches,
					test.pages,
					&workspace,
				)
				if status.failed() || !ok {
					panic("selection failed")
				}
				verified, status := tree.verifySelectionWithWorkspace(selection, scratch, &workspace)
				if status.failed() {
					panic("verification failed")
				}
				result, passStatus := verified.secondPassWithWorkspace(&tree, &workspace, sink)
				if passStatus.failed() || result.batchCount != test.batches ||
					result.pageCount != test.pages || sinkCalls != test.pages {
					panic("second pass failed")
				}
			})
		})
	}
}
