package exactv4

import (
	"fmt"
	"os"
	"syscall"
	"testing"
)

type controlledPageSource struct {
	base      immutableSlicePageSource
	access    *pageSourceError
	readError *pageSourceError
	reads     int
	mutate    func(uint32, *[PageSize]byte)
}

func (source *controlledPageSource) checkAccessStatus() pageSourceStatus {
	return source.access.status()
}

func (source *controlledPageSource) readPageStatus(
	pageNumber uint32,
	destination *[PageSize]byte,
) pageSourceStatus {
	source.reads++
	if source.readError != nil {
		return source.readError.status()
	}
	if status := source.base.readPageStatus(pageNumber, destination); status.failed() {
		return status
	}
	if source.mutate != nil {
		source.mutate(pageNumber, destination)
	}
	return pageSourceStatus{}
}

func TestPageIOEvidenceRetainsStableKindAndRawOSCode(t *testing.T) {
	evidence := pageIOEvidenceFromError(fmt.Errorf("wrapped read: %w", syscall.EIO))
	if evidence.kind != pageIOOther || !evidence.hasRawOSCode || evidence.rawOSCode != uint64(syscall.EIO) {
		t.Fatalf("I/O evidence = %+v, want other + raw EIO", evidence)
	}

	evidence = pageIOEvidenceFromError(fmt.Errorf("wrapped interrupt: %w", syscall.EINTR))
	if evidence.kind != pageIOInterrupted || !evidence.hasRawOSCode || evidence.rawOSCode != uint64(syscall.EINTR) {
		t.Fatalf("interrupt evidence = %+v, want interrupted + raw EINTR", evidence)
	}
}

func TestImmutablePageSourceReportsExactPositionalShortRead(t *testing.T) {
	data := make([]byte, 2*PageSize+17)
	source := newImmutableSlicePageSource(data, 3)
	var page [PageSize]byte
	err := source.readPage(2, &page)
	if err == nil || err.code != pageSourceErrShortRead ||
		err.page != 2 || err.offset != 2*PageSize ||
		err.expected != PageSize || err.actual != 17 {
		t.Fatalf("short read = %+v", err)
	}
}

func TestFilePageSourcePreservesOSAndCreatorPIDEvidence(t *testing.T) {
	file, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	defer writer.Close()
	var page [PageSize]byte
	readErr := readFilePageAt(file, 0, &page)
	if readErr == nil || readErr.code != pageSourceErrIO ||
		!readErr.evidence.hasRawOSCode || readErr.evidence.rawOSCode != uint64(syscall.ESPIPE) {
		t.Fatalf("closed-file evidence = code=%d evidence=%+v cause=%T/%v", readErr.code, readErr.evidence, readErr.cause, readErr.cause)
	}

	forked := newFilePageRead(nil, os.Getpid()+1)
	if accessErr := forked.checkPageAccess(); accessErr == nil || accessErr.code != pageSourceErrForkedHandle {
		t.Fatalf("creator check = %+v", accessErr)
	}
	if accessErr := forked.readPageAt(0, &page); accessErr == nil || accessErr.code != pageSourceErrForkedHandle {
		t.Fatalf("read creator check = %+v", accessErr)
	}
}
