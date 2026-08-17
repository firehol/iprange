package writer

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/firehol/iprange/v4/go/internal/format"
)

var openTestDBID = [16]byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10}
var openTestNonce = [16]byte{0x11, 0x12, 0x13, 0x14, 0x15, 0x16, 0x17, 0x18, 0x19, 0x1a, 0x1b, 0x1c, 0x1d, 0x1e, 0x1f, 0x20}

func testBudget() PageBudget {
	return PageBudget{MaxHeapBytes: 1 << 20, MaxPrivatePages: 4096, MaxGrowthPages: 4096}
}

// fixture returns the absolute path of one Rust conformance fixture.
func fixture(t testing.TB, name string) string {
	t.Helper()
	return filepath.Join("..", "..", "..", "conformance", "rust", name)
}

// copyFixture copies a conformance fixture into a fresh temp file.
func copyFixture(t testing.TB, name, destName string) string {
	t.Helper()
	raw, err := os.ReadFile(fixture(t, name))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, destName)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// assertReopen proves a failed open released the exclusive lifetime lock:
// a second open of the same path must return promptly with the file's own
// refusal (the fixture is still corrupt) instead of blocking on the leaked
// lock. OFD locks conflict even within one process, so a leaked lock hangs
// the probe until the timeout.
func assertReopen(t *testing.T, path string) {
	t.Helper()
	result := make(chan error, 1)
	go func() {
		c, err := Open(path, testBudget(), nil)
		if c != nil {
			c.Close()
		}
		result <- err
	}()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("reopen of the corrupt fixture succeeded")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("exclusive lifetime lock leaked by the refused open")
	}
}

func fileSize(t *testing.T, path string) int64 {
	t.Helper()
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return st.Size()
}

// makeEmptyDB builds a minimal valid empty direct database (two identical
// meta pages, txn 1, page count 2).
func makeEmptyDB(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.iprdb")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		page := make([]byte, format.PageSize)
		copy(page[0:8], format.MainMagic[:])
		format.PutU16(page[8:10], format.MetaSize)
		page[10] = format.PageShift
		page[11] = format.AddressFamilyIPv4
		page[12] = format.ValueKindDirect
		copy(page[16:32], "direct\x00")
		copy(page[32:48], openTestDBID[:])
		format.PutU64(page[48:56], 1)
		copy(page[56:72], openTestNonce[:])
		format.PutU64(page[72:80], 2)
		format.PutU32(page[252:256], format.MetaCRC32C(page))
		if _, err := f.Write(page); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestOpenTrimsUnpublishedTail pins trim_committed_tail: a file whose
// physical extent exceeds the committed generation opens as a writer and
// the unpublished tail is removed and synced, leaving a valid database.
func TestOpenTrimsUnpublishedTail(t *testing.T) {
	path := copyFixture(t, "direct-ipv4.iprdb", "tail.iprdb")
	committed := fileSize(t, path)
	if err := os.Truncate(path, committed+8*format.PageSize); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	before := info.Size()

	c, err := Open(path, testBudget(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if after := fileSize(t, path); after != committed {
		t.Fatalf("file size after open %d, want committed %d", after, committed)
	}
	if after := fileSize(t, path); after == before {
		t.Fatal("tail was not removed")
	}
	if c.m.Size() != uint64(committed) || c.m.PhysicalSize() != uint64(committed) {
		t.Fatalf("mapping size %d physical %d, want %d", c.m.Size(), c.m.PhysicalSize(), committed)
	}
	// The trimmed file must open immutably as the same generation.
	meta0, ok0, err := readMeta(path, 0)
	if err != nil || !ok0 {
		t.Fatalf("meta page 0 unreadable: %v", err)
	}
	if got := c.BaseInfo().PageCount * format.PageSize; got != uint64(committed) {
		t.Fatalf("base page count %d pages, want committed %d", c.BaseInfo().PageCount, committed)
	}
	if got := c.BaseInfo().DatabaseID; got != meta0.DatabaseID {
		t.Fatal("base database id does not match the file meta")
	}
}

func readMeta(path string, pg int) (format.Meta, bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return format.Meta{}, false, err
	}
	defer f.Close()
	page := make([]byte, format.PageSize)
	if _, err := f.ReadAt(page, int64(pg*format.PageSize)); err != nil {
		return format.Meta{}, false, err
	}
	m, ok := format.ParseIdentity(page)
	return m, ok, nil
}

// TestOpenNoTailNoOp pins the no-op fast path: a committed==physical file
// opens without changing the extent.
func TestOpenNoTailNoOp(t *testing.T) {
	path := copyFixture(t, "direct-ipv4.iprdb", "notail.iprdb")
	committed := fileSize(t, path)
	c, err := Open(path, testBudget(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if after := fileSize(t, path); after != committed {
		t.Fatalf("file size changed %d -> %d", committed, after)
	}
}

// TestOpenEmptyDatabase opens a two-page empty database: no remap work, no
// trim work, committed == physical == 2 pages.
func TestOpenEmptyDatabase(t *testing.T) {
	path := makeEmptyDB(t)
	c, err := Open(path, testBudget(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if c.m.Size() != 2*format.PageSize || c.m.PhysicalSize() != 2*format.PageSize {
		t.Fatalf("size %d physical %d, want 2 pages", c.m.Size(), c.m.PhysicalSize())
	}
	if c.BaseInfo().PageCount != 2 || c.BaseInfo().TransactionID != 1 {
		t.Fatalf("base page count %d txn %d, want 2/1", c.BaseInfo().PageCount, c.BaseInfo().TransactionID)
	}
}

// TestOpenWriterPrimitives mirrors the Rust sequence map_writer ->
// select_committed -> trim_committed_tail and pins the same outcome as the
// composed Open entry.
func TestOpenWriterPrimitives(t *testing.T) {
	path := copyFixture(t, "direct-ipv4.iprdb", "primitives.iprdb")
	committed := fileSize(t, path)
	if err := os.Truncate(path, committed+4*format.PageSize); err != nil {
		t.Fatal(err)
	}
	c, err := OpenWriter(path, testBudget(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.SelectCommitted(); err != nil {
		t.Fatal(err)
	}
	if c.base.CommittedBytes != uint64(committed) {
		t.Fatalf("committed %d, want %d", c.base.CommittedBytes, committed)
	}
	if err := c.TrimCommittedTail(); err != nil {
		t.Fatal(err)
	}
	if after := fileSize(t, path); after != committed {
		t.Fatalf("file size after trim %d, want %d", after, committed)
	}
}

// TestOpenRefusesSoleMeta pins the writer rule: a sole meta can never prove
// the current generation (Rust finish_open CurrentGenerationUnprovable).
func TestOpenRefusesSoleMeta(t *testing.T) {
	path := copyFixture(t, "direct-ipv4.iprdb", "solemeta.iprdb")
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteAt([]byte{'X'}, 0); err != nil {
		t.Fatal(err)
	}
	f.Close()
	_, err = Open(path, testBudget(), nil)
	ferr, ok := err.(*format.Error)
	if !ok || ferr.Code != format.CodeFormatInvalid {
		t.Fatalf("error %v, want FormatInvalid", err)
	}
	assertReopen(t, path)
}

// TestOpenRefusesNoValidMeta pins the no-candidate refusal.
func TestOpenRefusesNoValidMeta(t *testing.T) {
	path := copyFixture(t, "direct-ipv4.iprdb", "nometa.iprdb")
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteAt([]byte{'X'}, 0)
	f.WriteAt([]byte{'X'}, format.PageSize)
	f.Close()
	_, err = Open(path, testBudget(), nil)
	ferr, ok := err.(*format.Error)
	if !ok || ferr.Code != format.CodeFormatInvalid {
		t.Fatalf("error %v, want FormatInvalid", err)
	}
	assertReopen(t, path)
}

// TestOpenRefusesChecksumInvalidMeta pins the identity checksum rule: a
// meta with a broken CRC is not identity-readable, so the writer refuses.
func TestOpenRefusesChecksumInvalidMeta(t *testing.T) {
	// Two variants: one broken page (sole meta -> unprovable) and both
	// broken (no candidate).
	t.Run("one-page", func(t *testing.T) {
		path := copyFixture(t, "direct-ipv4.iprdb", "badcrc1.iprdb")
		f, err := os.OpenFile(path, os.O_RDWR, 0)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.WriteAt([]byte{0x00}, 252); err != nil {
			t.Fatal(err)
		}
		f.Close()
		_, err = Open(path, testBudget(), nil)
		ferr, ok := err.(*format.Error)
		if !ok || ferr.Code != format.CodeFormatInvalid {
			t.Fatalf("error %v, want FormatInvalid", err)
		}
		assertReopen(t, path)
	})
	t.Run("both-pages", func(t *testing.T) {
		path := copyFixture(t, "direct-ipv4.iprdb", "badcrc2.iprdb")
		f, err := os.OpenFile(path, os.O_RDWR, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, off := range []int64{252, format.PageSize + 252} {
			if _, err := f.WriteAt([]byte{0x00}, off); err != nil {
				t.Fatal(err)
			}
		}
		f.Close()
		_, err = Open(path, testBudget(), nil)
		ferr, ok := err.(*format.Error)
		if !ok || ferr.Code != format.CodeFormatInvalid {
			t.Fatalf("error %v, want FormatInvalid", err)
		}
		assertReopen(t, path)
	})
}

// TestOpenRefusesCommittedBeyondPhysical pins the per-meta physical rule: a
// meta whose page_count*4096 exceeds the file length is not
// bootstrap-valid, so the writer open refuses (Corrupt class).
func TestOpenRefusesCommittedBeyondPhysical(t *testing.T) {
	path := copyFixture(t, "direct-ipv4.iprdb", "short.iprdb")
	committed := fileSize(t, path)
	if committed < 3*format.PageSize {
		t.Skip("fixture too small for the truncation")
	}
	if err := os.Truncate(path, committed-format.PageSize); err != nil {
		t.Fatal(err)
	}
	_, err := Open(path, testBudget(), nil)
	ferr, ok := err.(*format.Error)
	if !ok || ferr.Code != format.CodeFormatInvalid {
		t.Fatalf("error %v, want FormatInvalid", err)
	}
	assertReopen(t, path)
}

// TestOpenBudgetAndInfo pins the budget round-trip and the base generation
// facts of the selected committed meta.
func TestOpenBudgetAndInfo(t *testing.T) {
	path := copyFixture(t, "direct-ipv4.iprdb", "budget.iprdb")
	c, err := Open(path, testBudget(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if c.Budget() != testBudget() {
		t.Fatalf("budget %+v, want %+v", c.Budget(), testBudget())
	}
	meta0, ok0, err := readMeta(path, 0)
	if err != nil || !ok0 {
		t.Fatalf("meta page 0 unreadable: %v", err)
	}
	info := c.BaseInfo()
	if info.AddressFamily != meta0.AddressFamily || info.ValueKind != meta0.ValueKind ||
		info.PageCount != meta0.PageCount || info.TransactionID != meta0.TxnID ||
		info.DatabaseID != meta0.DatabaseID {
		t.Fatalf("base info %+v does not match file meta %+v", info, meta0)
	}
}

// TestOpenRefusesPathReplacedDuringOpen pins the terminal path-identity
// re-verification (Rust open_locked's verify_pair; reader.go parity): when
// the path is replaced while the writer open is in flight, after the
// mapping owner's own namespace checks, Open must refuse with
// WrongState instead of publishing a writer bound to the detached inode.
// The namespace hook (check) runs under the exclusive lock before the
// first mapping and again after openMapping's final identity check; the
// rename on the second invocation lands exactly in the window between the
// mapping owner's checks and the writer's post-remap VerifyIdentity.
func TestOpenRefusesPathReplacedDuringOpen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "replaced.iprdb")
	raw, err := os.ReadFile(fixture(t, "direct-ipv4.iprdb"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(dir, "other.iprdb")
	if err := os.WriteFile(other, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	calls := 0
	check := func(clean string) error {
		calls++
		if calls == 2 {
			if err := os.Rename(other, path); err != nil {
				t.Errorf("replace path: %v", err)
			}
		}
		return nil
	}
	c, err := Open(path, testBudget(), check)
	if c != nil {
		c.Close()
		t.Fatal("open accepted a path replaced while the open was in flight")
	}
	fe, ok := err.(*format.Error)
	if !ok || fe.Code != format.CodeWrongState {
		t.Fatalf("replaced-during-open error %v, want WrongState (11)", err)
	}
	// Lock release after this refusal is not probe-observable here: the
	// path now names the replacement inode, and a leaked OFD lock would sit
	// on the detached original inode (locks are per-inode). The same-inode
	// release pins live in TestRefusedOpenPathMovedReleasesLock and in the
	// assertReopen probes over unchanged inodes.
}

// TestRefusedOpenPathMovedReleasesLock pins the lock release on the
// VerifyIdentity failure path with a same-inode probe: the check hook
// renames the opened file itself out of the way on its second invocation,
// so VerifyIdentity refuses with NameNotFound while the original inode
// stays reachable at the moved path. A reopened moved path lands on the
// same inode, so a leaked exclusive OFD lock would genuinely block the
// probe until the timeout; a prompt successful reopen proves the refused
// open released the lock.
func TestRefusedOpenPathMovedReleasesLock(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "moved.iprdb")
	moved := filepath.Join(dir, "moved-away.iprdb")
	raw, err := os.ReadFile(fixture(t, "direct-ipv4.iprdb"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	calls := 0
	check := func(clean string) error {
		calls++
		if calls == 2 {
			if err := os.Rename(path, moved); err != nil {
				t.Errorf("move path: %v", err)
			}
		}
		return nil
	}
	c, err := Open(path, testBudget(), check)
	if c != nil {
		c.Close()
		t.Fatal("open accepted a path moved while the open was in flight")
	}
	fe, ok := err.(*format.Error)
	if !ok || fe.Code != format.CodeNameNotFound {
		t.Fatalf("moved-during-open error %v, want NameNotFound (18)", err)
	}
	result := make(chan error, 1)
	go func() {
		c, err := Open(moved, testBudget(), nil)
		if c != nil {
			c.Close()
		}
		result <- err
	}()
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("reopen of the moved path failed: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("exclusive lifetime lock leaked by the NameNotFound refusal")
	}
}
