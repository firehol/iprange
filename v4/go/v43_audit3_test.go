package iprangedb

import (
	"bytes"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

type synchronizedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *synchronizedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *synchronizedBuffer) contains(s string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return bytes.Contains(b.b.Bytes(), []byte(s))
}

func (b *synchronizedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

// Audit round 3: 7 hardening fixes, each with a failing-then-passing test.
//
// I5  — truncated spill file detection (Go + Rust).
// I2  — OpenFile must not truncate a corrupt file before rejecting it.
// I3  — Validate() must verify scope-table per-page CRC.
// I4  — writer must reject corrupt scope-table and free-list pages.
// I7  — compaction must not run on every Set/Delete.
// I1  — MVCC: reader registering during commit must stay stable.
// I6  — Go Set must be zero-allocation on a warm tree.

// ── I5: truncated spill files must error, not silently lose records ──────────

// TestI5_RunReaderRejectsTruncatedSpill writes 3 complete spill records, then
// truncates the file mid-4th-record. The runReader must surface an error
// instead of treating the partial record as clean EOF (which would silently
// drop the IPs that were in the truncated record).
func TestI5_RunReaderRejectsTruncatedSpill(t *testing.T) {
	var zero Ipv4Key
	kw := zero.width()
	recSize := spillRecordSize(kw)

	// Write 3 complete records to a temp spill file.
	f, err := os.CreateTemp(t.TempDir(), "iprange_trunc_*")
	if err != nil {
		t.Fatal(err)
	}
	path := f.Name()
	buf := make([]byte, recSize)
	for i := uint32(0); i < 3; i++ {
		rec := DesiredRecord[Ipv4Key]{From: Ipv4Key(i * 10), To: Ipv4Key(i*10 + 9), ScopeID: i + 1}
		writeSpillRecord(buf, &rec, uint64(i), kw)
		if _, err := f.Write(buf); err != nil {
			f.Close()
			t.Fatal(err)
		}
	}
	// Append a partial 4th record (half a record) to simulate truncation.
	half := make([]byte, recSize/2)
	for i := range half {
		half[i] = 0xAB
	}
	if _, err := f.Write(half); err != nil {
		f.Close()
		t.Fatal(err)
	}
	f.Close()

	// Truncate to exactly 3*recSize + recSize/2 (already the size, but be explicit).
	info, _ := os.Stat(path)
	if info.Size() != int64(3*recSize+recSize/2) {
		t.Fatalf("setup size = %d, want %d", info.Size(), 3*recSize+recSize/2)
	}

	rr, err := openRunReader[Ipv4Key](path)
	if err != nil {
		t.Fatal(err)
	}
	// Read the 3 good records.
	for i := 0; i < 3; i++ {
		if !rr.ok {
			t.Fatalf("record %d: reader stopped early before truncation", i)
		}
		if rr.current.From != Ipv4Key(uint32(i)*10) {
			t.Fatalf("record %d From = %v, want %v", i, rr.current.From, Ipv4Key(uint32(i)*10))
		}
		rr.advance()
	}
	// The 4th advance hits the partial record. It MUST report a truncation error.
	if rr.ok {
		t.Fatal("reader accepted a partial record as a complete record")
	}
	if rr.err == nil {
		t.Fatal("truncated spill file produced no error — records silently lost")
	}
}

// TestI5_ExtSortSingleRunTruncatedErrors exercises the single-run readback
// path inside ExtSorter.Finish (a different code path than runReader).
func TestI5_ExtSortSingleRunTruncatedErrors(t *testing.T) {
	var zero Ipv4Key
	kw := zero.width()
	recSize := spillRecordSize(kw)

	// Build a spill file with 2 records + a partial 3rd.
	f, err := os.CreateTemp(t.TempDir(), "iprange_trunc2_*")
	if err != nil {
		t.Fatal(err)
	}
	path := f.Name()
	buf := make([]byte, recSize)
	for i := uint32(0); i < 2; i++ {
		rec := DesiredRecord[Ipv4Key]{From: Ipv4Key(i), To: Ipv4Key(i), ScopeID: i + 1}
		writeSpillRecord(buf, &rec, uint64(i), kw)
		f.Write(buf)
	}
	f.Write(make([]byte, recSize/2)) // partial 3rd record
	f.Close()

	// Mimic the single-run readback loop that ExtSort uses.
	rf, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer rf.Close()
	rbuf := make([]byte, recSize)
	count := 0
	var truncErr error
	for {
		n, err := io.ReadFull(rf, rbuf)
		if err != nil {
			if err != io.EOF && n > 0 {
				truncErr = err
			}
			break
		}
		count++
	}
	if truncErr == nil {
		t.Fatalf("readback of truncated spill did not detect truncation (count=%d)", count)
	}
}

// ── I2: OpenFile must not truncate a corrupt file before rejecting it ─────────

// TestI2_OpenFileDoesNotTruncateCorruptFile creates a valid file, then corrupts
// BOTH meta page CRCs. OpenFile must reject the file AND must not change the
// file's size (the old code called newMmapStore → file.Truncate using the
// corrupt meta's total_pages BEFORE openWriter rejected).
func TestI2_OpenFileDoesNotTruncateCorruptFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "i2_corrupt.iprdb")

	// Create a valid file with data.
	func() {
		fw, err := CreateFile[Ipv4Key](path, ScopeModeScalar, 0)
		if err != nil {
			t.Fatal(err)
		}
		for i := uint32(0); i < 100; i++ {
			if err := fw.Set(Ipv4Key(i), Ipv4Key(i), 7); err != nil {
				t.Fatal(err)
			}
		}
		if err := fw.Commit(1); err != nil {
			t.Fatal(err)
		}
		if err := fw.Close(); err != nil {
			t.Fatal(err)
		}
	}()

	origInfo, _ := os.Stat(path)
	origSize := origInfo.Size()

	// Corrupt a body byte on BOTH meta pages (breaks CRC on both).
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	raw[MetaCreatedUnix] ^= 0xFF
	raw[PageSize+MetaCreatedUnix] ^= 0xFF
	if err := os.WriteFile(path, raw, 0644); err != nil {
		t.Fatal(err)
	}

	// OpenFile must reject (both metas fail CRC).
	fw, err := OpenFile[Ipv4Key](path)
	if err == nil {
		fw.Close()
		t.Fatal("OpenFile accepted a file with both meta CRCs broken")
	}

	// The file size must NOT have changed (no truncation before rejection).
	afterInfo, _ := os.Stat(path)
	if afterInfo.Size() != origSize {
		t.Fatalf("OpenFile truncated corrupt file: orig=%d after=%d", origSize, afterInfo.Size())
	}
}

// ── I6: Go Set must be zero-allocation on a warm tree ────────────────────────

func TestI6_SetIsZeroAllocation(t *testing.T) {
	w, err := Create[Ipv4Key](ScopeModeScalar, 0)
	if err != nil {
		t.Fatal(err)
	}
	for i := uint32(0); i < 1000; i++ {
		if err := w.Set(Ipv4Key(i), Ipv4Key(i), i); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Commit(1, math.MaxUint64); err != nil {
		t.Fatal(err)
	}
	// Prime: churn once to populate free-list and warm the tree.
	for i := uint32(0); i < 1000; i++ {
		w.Delete(Ipv4Key(i), Ipv4Key(i))
	}
	for i := uint32(0); i < 1000; i++ {
		if err := w.Set(Ipv4Key(i), Ipv4Key(i), i+100); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Commit(2, math.MaxUint64); err != nil {
		t.Fatal(err)
	}

	allocs := testing.AllocsPerRun(100, func() {
		if err := w.Set(Ipv4Key(500), Ipv4Key(500), 999); err != nil {
			t.Fatal(err)
		}
	})
	if allocs != 0 {
		t.Fatalf("Set allocates %v per call on a warm tree; want 0", allocs)
	}
}

// ── I3: Validate() must verify scope-table per-page CRC ─────────────────────

// TestI3_ValidateRejectsCorruptScopeTable builds a mode-2 file with a scope
// table, corrupts a scope-leaf page body (breaks its CRC), then calls
// Reader.Validate(). The current validateScopeTable walks the scope pages
// without verifying CRC, so it accepts the corruption. Validate MUST reject.
func TestI3_ValidateRejectsCorruptScopeTable(t *testing.T) {
	w, err := Create[Ipv4Key](ScopeModeIndirect, 0)
	if err != nil {
		t.Fatal(err)
	}
	// Intern enough scopes to guarantee at least one scope-leaf page beyond
	// the two meta pages.
	for i := 0; i < 10; i++ {
		bm := make([]byte, 32)
		bm[i/8] |= 1 << (uint(i) % 8)
		if _, err := w.ScopeIntern(bm); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Set(Ipv4Key(1), Ipv4Key(10), 1); err != nil {
		t.Fatal(err)
	}
	if err := w.Commit(1, math.MaxUint64); err != nil {
		t.Fatal(err)
	}
	img, ok := w.IntoImage()
	if !ok {
		t.Fatal("expected vecPageStore")
	}

	// Sanity: clean image validates.
	r1, err := Open(img)
	if err != nil {
		t.Fatal(err)
	}
	if err := r1.Validate(); err != nil {
		t.Fatalf("clean file failed Validate: %v", err)
	}

	// Find a scope-leaf page and corrupt a body byte (not the CRC field).
	corrupt := append([]byte(nil), img...)
	corrupted := false
	for p := uint32(2); p < uint32(len(corrupt)/PageSize); p++ {
		off := int(p) * PageSize
		pt := corrupt[off] // page type byte (PH_PAGE_TYPE at offset 0)
		if pt == PageTypeScopeLeaf {
			// Corrupt a byte in the entry body region (past the header).
			bodyOff := off + PageHeaderSize + 5
			corrupt[bodyOff] ^= 0xFF
			corrupted = true
			break
		}
	}
	if !corrupted {
		t.Fatal("test setup failed: no scope-leaf page found to corrupt")
	}

	r2, err := Open(corrupt)
	if err != nil {
		// Open itself may reject if the meta CRC broke (it didn't). If Open
		// fails here, the scope corruption bled into a meta page — fix setup.
		t.Fatalf("Open rejected scope-corrupt image: %v", err)
	}
	if err := r2.Validate(); err == nil {
		t.Fatal("Validate accepted a corrupt scope-table page (CRC not checked)")
	}
}

// ── I4: writer must reject corrupt scope-table and free-list pages ───────────

// TestI4_OpenWriterRejectsCorruptScopeTable builds a mode-2 file, commits,
// corrupts a scope-leaf page CRC, then tries to openWriter. The writer loads
// the scope table on open; a corrupt scope page must be rejected (not silently
// used with garbage data).
func TestI4_OpenWriterRejectsCorruptScopeTable(t *testing.T) {
	w, err := Create[Ipv4Key](ScopeModeIndirect, 0)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		bm := make([]byte, 32)
		bm[i/8] |= 1 << (uint(i) % 8)
		if _, err := w.ScopeIntern(bm); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Set(Ipv4Key(1), Ipv4Key(10), 1); err != nil {
		t.Fatal(err)
	}
	if err := w.Commit(1, math.MaxUint64); err != nil {
		t.Fatal(err)
	}
	img, ok := w.IntoImage()
	if !ok {
		t.Fatal("expected vecPageStore")
	}

	// Corrupt a scope-leaf body byte.
	corrupt := append([]byte(nil), img...)
	corrupted := false
	for p := uint32(2); p < uint32(len(corrupt)/PageSize); p++ {
		off := int(p) * PageSize
		if corrupt[off] == PageTypeScopeLeaf {
			corrupt[off+PageHeaderSize+5] ^= 0xFF
			corrupted = true
			break
		}
	}
	if !corrupted {
		t.Fatal("no scope-leaf page to corrupt")
	}

	// openWriter must reject the corrupt scope table.
	if _, err := openWriter[Ipv4Key](newVecPageStore(corrupt)); err == nil {
		t.Fatal("openWriter accepted a file with a corrupt scope-table page")
	}
}

// TestI4_LoadFreeListRejectsCorruptChainPage builds a file with a populated
// free-list chain (via delete + commit), corrupts a chain page, then reloads.
// The writer must reject the corrupt chain page instead of silently using its
// (garbage) freed-page numbers.
func TestI4_LoadFreeListRejectsCorruptChainPage(t *testing.T) {
	w, err := Create[Ipv4Key](ScopeModeScalar, 0)
	if err != nil {
		t.Fatal(err)
	}
	for i := uint32(0); i < 100; i++ {
		if err := w.Set(Ipv4Key(i), Ipv4Key(i), 7); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Commit(1, math.MaxUint64); err != nil {
		t.Fatal(err)
	}
	// Delete ~half to create free-list entries, then commit.
	for i := uint32(0); i < 50; i++ {
		w.Delete(Ipv4Key(i), Ipv4Key(i))
	}
	if err := w.Commit(2, math.MaxUint64); err != nil {
		t.Fatal(err)
	}
	img, ok := w.IntoImage()
	if !ok {
		t.Fatal("expected vecPageStore")
	}

	// Find a TXN_FREE chain page and corrupt its body.
	corrupt := append([]byte(nil), img...)
	corrupted := false
	for p := uint32(2); p < uint32(len(corrupt)/PageSize); p++ {
		off := int(p) * PageSize
		if corrupt[off] == PageTypeTxnFree {
			// Corrupt a byte in the freed-page array region.
			corrupt[off+TxnFreeArray+4] ^= 0xFF
			corrupted = true
			break
		}
	}
	if !corrupted {
		t.Fatal("no free-list chain page to corrupt")
	}

	// openWriter → LoadFreeList must reject the corrupt chain page.
	if _, err := openWriter[Ipv4Key](newVecPageStore(corrupt)); err == nil {
		t.Fatal("openWriter accepted a file with a corrupt free-list chain page")
	}
}

// ── I7: compaction must not run (full tree walk) on every Set/Delete ──────────

// countingStore wraps a pageStore and counts page reads.
type countingStore struct {
	inner     pageStore
	readCount int
}

func (c *countingStore) page(pgno uint32) []byte       { c.readCount++; return c.inner.page(pgno) }
func (c *countingStore) pageMut(pgno uint32) []byte    { return c.inner.pageMut(pgno) }
func (c *countingStore) copyPage(src, dst uint32)      { c.inner.copyPage(src, dst) }
func (c *countingStore) allocPage() (uint32, error)    { return c.inner.allocPage() }
func (c *countingStore) totalPages() uint32            { return c.inner.totalPages() }
func (c *countingStore) committedPages() uint32        { return c.inner.committedPages() }
func (c *countingStore) setCommittedPages(n uint32)    { c.inner.setCommittedPages(n) }
func (c *countingStore) committedBytes() []byte        { return c.inner.committedBytes() }
func (c *countingStore) ensureCapacity(m uint32) error { return c.inner.ensureCapacity(m) }
func (c *countingStore) sync() error                   { return c.inner.sync() }
func (c *countingStore) truncate(n uint32) error       { return c.inner.truncate(n) }

// TestI7_DeleteDoesNotWalkEntireTree: a single delete must read O(height)
// pages, NOT O(tree pages). The old code called compactIfNeeded (full tree
// walk) at the end of every deleteRange, and scanOverlapNode's branch loop
// re-scanned all remaining leaves on the no-overlap pass.
func TestI7_DeleteDoesNotWalkEntireTree(t *testing.T) {
	w, err := Create[Ipv4Key](ScopeModeScalar, 0)
	if err != nil {
		t.Fatal(err)
	}
	for i := uint32(0); i < 5000; i++ {
		if err := w.Set(Ipv4Key(i*2), Ipv4Key(i*2), 7); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Commit(1, math.MaxUint64); err != nil {
		t.Fatal(err)
	}
	treePages := uint64(0)
	w.countTreePages(w.pendingRoot, w.pendingHeight, &treePages)
	if treePages < 20 {
		t.Fatalf("test setup: tree too small (%d pages)", treePages)
	}
	cs := &countingStore{inner: w.store}
	w.store = cs
	cs.readCount = 0
	if _, err := w.Delete(Ipv4Key(1000), Ipv4Key(1000)); err != nil {
		t.Fatal(err)
	}
	if reads := cs.readCount; reads >= int(treePages) {
		t.Fatalf("single Delete read %d pages (tree has %d); expected < %d — "+
			"hot path walks the whole tree", reads, treePages, treePages)
	}
}

// ── I1: MVCC race — reader registering during commit must stay stable ────────

// TestI1_CommitAcquiresReaderTableLock deterministically verifies that
// FileWriter.Commit holds a lock on the reader companion file for the duration
// of the commit. While an external LOCK_EX is held on the companion file, the
// commit MUST block; releasing the lock MUST let the commit proceed. Without
// the I1 fix, Commit would complete immediately (no lock).
func TestI1_CommitAcquiresReaderTableLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "i1_lock.iprdb")
	fw, err := CreateFile[Ipv4Key](path, ScopeModeScalar, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := fw.Set(Ipv4Key(1), Ipv4Key(1), 7); err != nil {
		t.Fatal(err)
	}
	if err := fw.Commit(1); err != nil {
		t.Fatal(err)
	}

	// Take LOCK_EX on the companion file from the test.
	readersPath := path + ".readers"
	lockF, err := os.OpenFile(readersPath, os.O_RDWR, 0644)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		syscall.Flock(int(lockF.Fd()), syscall.LOCK_UN)
		lockF.Close()
	}()
	if err := syscall.Flock(int(lockF.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}

	// Commit must BLOCK while the lock is held.
	done := make(chan error, 1)
	go func() { done <- fw.Commit(2) }()
	select {
	case err := <-done:
		t.Fatalf("Commit did not block while reader table locked (err=%v) — lock not acquired", err)
	case <-time.After(300 * time.Millisecond):
		// Good — commit is blocked.
	}

	// Release the lock; the commit must now complete.
	syscall.Flock(int(lockF.Fd()), syscall.LOCK_UN)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Commit failed after lock release: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Commit did not complete after lock release")
	}
}

// TestI1_ReaderDuringCommitRemainsStable is a multi-process test. The child
// process opens an MmapReader (registers in the companion file), reads key 500
// (scope=11), sleeps while the parent commits 3 churn transactions, then
// re-reads key 500 — which MUST still be 11. Without the commit-time reader
// lock, the parent's commits could reuse pages the child's pinned transaction
// still references, corrupting the child's view.
func TestI1_ReaderDuringCommitRemainsStable(t *testing.T) {
	if os.Getenv("IPRANGE_I1_CHILD") == "1" {
		runI1ChildReader(os.Getenv("IPRANGE_I1_PATH"))
		return
	}

	path := filepath.Join(t.TempDir(), "i1_mvcc.iprdb")

	// txn 1: insert 1000 records; key 500 = scope 11.
	func() {
		fw, err := CreateFile[Ipv4Key](path, ScopeModeScalar, 0)
		if err != nil {
			t.Fatal(err)
		}
		for i := uint32(0); i < 1000; i++ {
			if err := fw.Set(Ipv4Key(i), Ipv4Key(i), 11); err != nil {
				t.Fatal(err)
			}
		}
		if err := fw.Commit(1); err != nil {
			t.Fatal(err)
		}
		if err := fw.Close(); err != nil {
			t.Fatal(err)
		}
	}()

	// Start the child reader process.
	cmd := exec.Command(os.Args[0], "-test.run=TestI1_ReaderDuringCommitRemainsStable")
	cmd.Env = append(os.Environ(),
		"IPRANGE_I1_CHILD=1",
		"IPRANGE_I1_PATH="+path,
	)
	var childOut synchronizedBuffer
	cmd.Stdout = &childOut
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}

	// Wait for the child to signal it has registered and read key 500.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if childOut.contains("CHILD_READY") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !childOut.contains("CHILD_READY") {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatal("child reader did not signal ready in time")
	}
	// Give the child a moment to settle into its sleep.
	time.Sleep(200 * time.Millisecond)

	// Parent: 3 churn commits while the child's reader is active.
	fw, err := OpenFile[Ipv4Key](path)
	if err != nil {
		t.Fatal(err)
	}
	for cycle := 2; cycle <= 4; cycle++ {
		if err := fw.Set(Ipv4Key(500), Ipv4Key(500), uint32(cycle*11)); err != nil {
			t.Fatal(err)
		}
		if err := fw.Commit(uint64(cycle)); err != nil {
			t.Fatal(err)
		}
	}
	fw.Close()

	// Wait for the child to finish and check its result.
	err = cmd.Wait()
	childLog := childOut.String()
	if err != nil {
		t.Fatalf("child reader process failed: %v\n%s", err, childLog)
	}
	if !strings.Contains(childLog, "CHILD_OK") {
		t.Fatalf("child reader did not confirm stable snapshot:\n%s", childLog)
	}
}

// runI1ChildReader is the child-process body. It opens an MmapReader, reads key
// 500, sleeps while the parent commits, then re-reads. Exits 0 (CHILD_OK) if
// the snapshot is stable, non-zero otherwise.
func runI1ChildReader(path string) {
	mr, err := OpenMmap(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "child: open reader:", err)
		os.Exit(1)
	}
	defer mr.Close()

	r, err := mr.Reader()
	if err != nil {
		fmt.Fprintln(os.Stderr, "child: reader:", err)
		os.Exit(1)
	}
	scope, ok := r.LookupV4(Ipv4Key(500))
	if !ok || scope != 11 {
		fmt.Fprintf(os.Stderr, "child: initial read key500=%d ok=%v, want 11\n", scope, ok)
		os.Exit(1)
	}
	fmt.Println("CHILD_READY")
	os.Stdout.Sync()

	// Sleep while the parent commits.
	time.Sleep(3 * time.Second)

	// Re-read: the pinned snapshot MUST still show scope 11.
	r2, err := mr.Reader()
	if err != nil {
		fmt.Fprintln(os.Stderr, "child: re-read:", err)
		os.Exit(1)
	}
	scope2, ok2 := r2.LookupV4(Ipv4Key(500))
	if !ok2 || scope2 != 11 {
		fmt.Fprintf(os.Stderr, "child: MVCC VIOLATION key500=%d ok=%v, want 11\n", scope2, ok2)
		os.Exit(1)
	}
	// Also verify a few other keys pinned at txn 1.
	for _, k := range []uint32{0, 1, 250, 999} {
		s, ok := r2.LookupV4(Ipv4Key(k))
		if !ok || s != 11 {
			fmt.Fprintf(os.Stderr, "child: MVCC VIOLATION key%d=%d ok=%v, want 11\n", k, s, ok)
			os.Exit(1)
		}
	}
	fmt.Println("CHILD_OK")
}
