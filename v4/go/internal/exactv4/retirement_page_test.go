package exactv4

import (
	"encoding/binary"
	"errors"
	"testing"
)

func putRetirementHeader(
	t *testing.T,
	page []byte,
	pageType PageType,
	count uint16,
	level uint16,
	lower uint16,
) {
	t.Helper()
	clear(page)
	header := PageHeader{
		PageType:  pageType,
		BornTxn:   1,
		ItemCount: count,
		Level:     level,
		Lower:     lower,
		Upper:     PageSize,
		Aux:       0,
	}
	if err := header.EncodeInto(page); err != nil {
		t.Fatal(err)
	}
}

func putRetirementBranchPage(
	t *testing.T,
	page []byte,
	level uint16,
	entries []retirementBranchEntry,
) {
	t.Helper()
	lower := int(PageHeaderSize) + len(entries)*retirementBranchEntrySize
	putRetirementHeader(t, page, PageTypeRetirementBranch, uint16(len(entries)), level, uint16(lower))
	for index, entry := range entries {
		at := int(PageHeaderSize) + index*retirementBranchEntrySize
		binary.LittleEndian.PutUint64(page[at:at+8], entry.maxRetiredByTxn)
		binary.LittleEndian.PutUint32(page[at+8:at+12], entry.childPage)
	}
	if _, err := WritePageCRC32C(page); err != nil {
		t.Fatal(err)
	}
}

func putRetirementLeafPage(t *testing.T, page []byte, batches []retirementBatch) {
	t.Helper()
	lower := int(PageHeaderSize) + len(batches)*retirementLeafRecordSize
	putRetirementHeader(t, page, PageTypeRetirementLeaf, uint16(len(batches)), 0, uint16(lower))
	for index, batch := range batches {
		at := int(PageHeaderSize) + index*retirementLeafRecordSize
		binary.LittleEndian.PutUint64(page[at+8:at+16], batch.retiredByTxn)
		binary.LittleEndian.PutUint64(page[at+16:at+24], batch.pageCount)
		binary.LittleEndian.PutUint32(page[at+24:at+28], batch.pageListBlobRoot)
	}
	if _, err := WritePageCRC32C(page); err != nil {
		t.Fatal(err)
	}
}

func requireRetirementPageCode(
	t *testing.T,
	err error,
	want retirementPageErrorCode,
) *retirementPageError {
	t.Helper()
	if err == nil {
		t.Fatalf("expected retirement-page error %d", want)
	}
	var got *retirementPageError
	if !errors.As(err, &got) {
		t.Fatalf("error type = %T, want *retirementPageError: %v", err, err)
	}
	if got.code != want {
		t.Fatalf("retirement-page code = %d, want %d", got.code, want)
	}
	return got
}

func TestRetirementBranchExactLayoutOrderAndCRC(t *testing.T) {
	page := make([]byte, PageSize)
	putRetirementBranchPage(t, page, 2, []retirementBranchEntry{
		{maxRetiredByTxn: 2, childPage: 3},
		{maxRetiredByTxn: 7, childPage: 4},
	})
	if got := binary.LittleEndian.Uint64(page[32:40]); got != 2 {
		t.Fatalf("first maximum = %d, want 2", got)
	}
	if got := binary.LittleEndian.Uint32(page[40:44]); got != 3 {
		t.Fatalf("first child = %d, want 3", got)
	}
	if got := binary.LittleEndian.Uint32(page[44:48]); got != 0 {
		t.Fatalf("first reserved = %d, want 0", got)
	}

	branch, err := openRetirementBranch(page, 8, 10)
	if err != nil {
		t.Fatal(err)
	}
	if branch.len() != 2 || branch.level != 2 {
		t.Fatalf("branch count/level = %d/%d, want 2/2", branch.len(), branch.level)
	}
	entry, err := branch.entry(1)
	if err != nil || entry.maxRetiredByTxn != 7 || entry.childPage != 4 {
		t.Fatalf("entry 1 = %+v/%v", entry, err)
	}
	maximum, err := branch.maximumKey()
	if err != nil || maximum != 7 {
		t.Fatalf("maximum = %d/%v, want 7/nil", maximum, err)
	}
	if err := branch.verifyCRC(); err != nil {
		t.Fatal(err)
	}

	page[PageCRCOffset] ^= 1
	branch, err = openRetirementBranch(page, 8, 10)
	if err != nil {
		t.Fatalf("ordinary open checked CRC: %v", err)
	}
	requireRetirementPageCode(t, branch.verifyCRC(), retirementPageErrChecksum)
}

func TestRetirementBranchRejectsTypeAuxGeometryReservedBoundsAndKeys(t *testing.T) {
	page := make([]byte, PageSize)
	putRetirementBranchPage(t, page, 1, []retirementBranchEntry{{maxRetiredByTxn: 2, childPage: 3}})
	page[4] = byte(PageTypeRangeBranch)
	_, err := openRetirementBranch(page, 8, 10)
	requireRetirementPageCode(t, err, retirementPageErrWrongPageType)

	putRetirementBranchPage(t, page, 1, []retirementBranchEntry{{maxRetiredByTxn: 2, childPage: 3}})
	binary.LittleEndian.PutUint32(page[24:28], 1)
	_, err = openRetirementBranch(page, 8, 10)
	requireRetirementPageCode(t, err, retirementPageErrWrongAux)

	putRetirementHeader(t, page, PageTypeRetirementBranch, 0, 1, PageHeaderSize)
	_, err = openRetirementBranch(page, 8, 10)
	requireRetirementPageCode(t, err, retirementPageErrEmptyPage)

	putRetirementBranchPage(t, page, 1, []retirementBranchEntry{{maxRetiredByTxn: 2, childPage: 3}})
	binary.LittleEndian.PutUint16(page[20:22], PageHeaderSize)
	_, err = openRetirementBranch(page, 8, 10)
	requireRetirementPageCode(t, err, retirementPageErrFixedGeometry)

	putRetirementBranchPage(t, page, 1, []retirementBranchEntry{{maxRetiredByTxn: 2, childPage: 3}})
	page[48] = 1
	_, err = openRetirementBranch(page, 8, 10)
	requireRetirementPageCode(t, err, retirementPageErrReservedNonzero)

	putRetirementBranchPage(t, page, 1, []retirementBranchEntry{{maxRetiredByTxn: 2, childPage: 10}})
	_, err = openRetirementBranch(page, 8, 10)
	pageErr := requireRetirementPageCode(t, err, retirementPageErrChildOutOfBounds)
	if pageErr.childPage != 10 {
		t.Fatalf("bad child = %d, want 10", pageErr.childPage)
	}

	putRetirementBranchPage(t, page, 1, []retirementBranchEntry{
		{maxRetiredByTxn: 2, childPage: 3},
		{maxRetiredByTxn: 2, childPage: 4},
	})
	_, err = openRetirementBranch(page, 8, 10)
	requireRetirementPageCode(t, err, retirementPageErrKeysNotStrict)

	putRetirementBranchPage(t, page, 1, []retirementBranchEntry{{maxRetiredByTxn: 9, childPage: 3}})
	_, err = openRetirementBranch(page, 8, 10)
	requireRetirementPageCode(t, err, retirementPageErrRetiredTransactionOutOfRange)

	putRetirementBranchPage(t, page, 1, []retirementBranchEntry{{maxRetiredByTxn: 2, childPage: 3}})
	binary.LittleEndian.PutUint32(page[44:48], 1)
	_, err = openRetirementBranch(page, 8, 10)
	requireRetirementPageCode(t, err, retirementPageErrReservedNonzero)
}

func TestRetirementLeafExactLayoutBatchLimitsAndCRC(t *testing.T) {
	page := make([]byte, PageSize)
	putRetirementLeafPage(t, page, []retirementBatch{
		{retiredByTxn: 2, pageCount: 7, pageListBlobRoot: 3},
		{retiredByTxn: 7, pageCount: maxRetirementBatchPages, pageListBlobRoot: 4},
	})
	if got := binary.LittleEndian.Uint64(page[32:40]); got != 0 {
		t.Fatalf("first reserved = %d, want 0", got)
	}
	if got := binary.LittleEndian.Uint64(page[40:48]); got != 2 {
		t.Fatalf("first transaction = %d, want 2", got)
	}
	if got := binary.LittleEndian.Uint64(page[48:56]); got != 7 {
		t.Fatalf("first page count = %d, want 7", got)
	}
	if got := binary.LittleEndian.Uint32(page[56:60]); got != 3 {
		t.Fatalf("first blob root = %d, want 3", got)
	}

	leaf, err := openRetirementLeaf(page, 8, 10)
	if err != nil {
		t.Fatal(err)
	}
	batch, err := leaf.batch(1)
	if err != nil || batch.pageCount != maxRetirementBatchPages || batch.pageListBlobRoot != 4 {
		t.Fatalf("batch 1 = %+v/%v", batch, err)
	}
	maximum, err := leaf.maximumKey()
	if err != nil || maximum != 7 {
		t.Fatalf("maximum = %d/%v, want 7/nil", maximum, err)
	}
	if err := leaf.verifyCRC(); err != nil {
		t.Fatal(err)
	}
}

func TestRetirementLeafRejectsEmptyGeometryReservedOrderAndBatchFields(t *testing.T) {
	page := make([]byte, PageSize)
	valid := retirementBatch{retiredByTxn: 2, pageCount: 1, pageListBlobRoot: 3}

	putRetirementHeader(t, page, PageTypeRetirementLeaf, 0, 0, PageHeaderSize)
	_, err := openRetirementLeaf(page, 8, 10)
	requireRetirementPageCode(t, err, retirementPageErrEmptyPage)

	putRetirementLeafPage(t, page, []retirementBatch{valid})
	binary.LittleEndian.PutUint16(page[20:22], PageHeaderSize)
	_, err = openRetirementLeaf(page, 8, 10)
	requireRetirementPageCode(t, err, retirementPageErrFixedGeometry)

	putRetirementLeafPage(t, page, []retirementBatch{valid})
	page[64] = 1
	_, err = openRetirementLeaf(page, 8, 10)
	requireRetirementPageCode(t, err, retirementPageErrReservedNonzero)

	putRetirementLeafPage(t, page, []retirementBatch{valid})
	page[32] = 1
	_, err = openRetirementLeaf(page, 8, 10)
	requireRetirementPageCode(t, err, retirementPageErrReservedNonzero)

	putRetirementLeafPage(t, page, []retirementBatch{valid})
	page[60] = 1
	_, err = openRetirementLeaf(page, 8, 10)
	requireRetirementPageCode(t, err, retirementPageErrReservedNonzero)

	putRetirementLeafPage(t, page, []retirementBatch{valid, valid})
	_, err = openRetirementLeaf(page, 8, 10)
	requireRetirementPageCode(t, err, retirementPageErrKeysNotStrict)

	for _, batch := range []retirementBatch{
		{retiredByTxn: 1, pageCount: 1, pageListBlobRoot: 3},
		{retiredByTxn: 9, pageCount: 1, pageListBlobRoot: 3},
	} {
		putRetirementLeafPage(t, page, []retirementBatch{batch})
		_, err = openRetirementLeaf(page, 8, 10)
		requireRetirementPageCode(t, err, retirementPageErrRetiredTransactionOutOfRange)
	}
	for _, count := range []uint64{0, maxRetirementBatchPages + 1} {
		putRetirementLeafPage(t, page, []retirementBatch{{retiredByTxn: 2, pageCount: count, pageListBlobRoot: 3}})
		_, err = openRetirementLeaf(page, 8, 10)
		requireRetirementPageCode(t, err, retirementPageErrBatchPageCountOutOfRange)
	}
	for _, root := range []uint32{0, 1, 10} {
		putRetirementLeafPage(t, page, []retirementBatch{{retiredByTxn: 2, pageCount: 1, pageListBlobRoot: root}})
		_, err = openRetirementLeaf(page, 8, 10)
		requireRetirementPageCode(t, err, retirementPageErrBlobRootOutOfBounds)
	}
}
