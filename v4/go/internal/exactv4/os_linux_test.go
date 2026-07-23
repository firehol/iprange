//go:build linux

package exactv4

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestLinuxRetainedOpenRejectsSymlinkHardlinkAndNonregular(t *testing.T) {
	directory := t.TempDir()
	mainPath := filepath.Join(directory, "main.iprdb")
	if err := os.WriteFile(mainPath, []byte("main"), 0o600); err != nil {
		t.Fatal(err)
	}
	retainedDir, component, openErr := openRetainedParent(mainPath)
	if openErr != nil {
		t.Fatal(openErr)
	}
	defer retainedDir.file.Close()
	main, openErr := retainedDir.openRegular(component, true)
	if openErr != nil {
		t.Fatal(openErr)
	}
	main.file.Close()

	if err := os.Symlink("main.iprdb", filepath.Join(directory, "link")); err != nil {
		t.Fatal(err)
	}
	if _, openErr = retainedDir.openRegular("link", false); openErr == nil {
		t.Fatal("symlink open succeeded")
	}
	if err := os.Link(mainPath, filepath.Join(directory, "hard")); err != nil {
		t.Fatal(err)
	}
	if _, openErr = retainedDir.openRegular(component, false); openErr == nil || openErr.code != linuxOSLinkCountNotOne {
		t.Fatalf("hard-linked file open = %v", openErr)
	}
	if _, openErr = retainedDir.openRegular(".", false); openErr == nil || openErr.code != linuxOSInvalidPathComponent {
		t.Fatalf("nonregular component open = %v", openErr)
	}
	if err := unix.Mkfifo(filepath.Join(directory, "fifo"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, openErr = retainedDir.openRegular("fifo", false); openErr == nil || openErr.code != linuxOSNotRegular {
		t.Fatalf("FIFO open = %v", openErr)
	}
}

func TestLinuxRetainedDescriptorSurvivesReplacementAndPathCheckDetectsIt(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "main.iprdb")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	retainedDir, component, openErr := openRetainedParent(path)
	if openErr != nil {
		t.Fatal(openErr)
	}
	defer retainedDir.file.Close()
	retained, openErr := retainedDir.openRegular(component, true)
	if openErr != nil {
		t.Fatal(openErr)
	}
	defer retained.file.Close()
	if err := os.Rename(path, filepath.Join(directory, "old")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := retainedDir.verifyPath(component, retained); err == nil || err.code != linuxOSPathIdentityMismatch {
		t.Fatalf("replacement path check = %v", err)
	}
	var data [3]byte
	if err := retained.readExactAt(data[:], 0); err != nil || string(data[:]) != "old" {
		t.Fatalf("retained read = %q, %v", data, err)
	}
}

func TestLinuxPinnedPageSourceSurvivesReplacementAndReportsShortRead(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "main.iprdb")
	oldPath := filepath.Join(directory, "old.iprdb")
	data := rangeImage[IPv4](t, 0, 0, 3, func(data []byte) {
		for index := 2 * PageSize; index < 3*PageSize; index++ {
			data[index] = 0x5a
		}
	})
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	retainedDir, component, openErr := openRetainedParent(path)
	if openErr != nil {
		t.Fatal(openErr)
	}
	defer retainedDir.file.Close()
	retained, openErr := retainedDir.openRegular(component, false)
	if openErr != nil {
		t.Fatal(openErr)
	}
	defer retained.file.Close()
	bootstrap, openErr := retained.readMainBootstrap(OpenLiveReader)
	if openErr != nil {
		t.Fatal(openErr)
	}
	source, sourceErr := retained.pinnedPageSource(bootstrap)
	if sourceErr != nil {
		t.Fatal(sourceErr)
	}

	if err := os.Rename(path, oldPath); err != nil {
		t.Fatal(err)
	}
	replacement := append([]byte(nil), data...)
	for index := 2 * PageSize; index < 3*PageSize; index++ {
		replacement[index] = 0xa5
	}
	if err := os.WriteFile(path, replacement, 0o600); err != nil {
		t.Fatal(err)
	}
	var page [PageSize]byte
	if sourceErr = source.readPage(2, &page); sourceErr != nil {
		t.Fatal(sourceErr)
	}
	for index, value := range page {
		if value != 0x5a {
			t.Fatalf("retained page byte %d = %#x, want old descriptor byte", index, value)
		}
	}

	if err := os.Truncate(oldPath, 2*PageSize); err != nil {
		t.Fatal(err)
	}
	sourceErr = source.readPage(2, &page)
	if sourceErr == nil || sourceErr.code != pageSourceErrShortRead ||
		sourceErr.offset != 2*PageSize || sourceErr.expected != PageSize || sourceErr.actual != 0 {
		t.Fatalf("short retained read = %+v", sourceErr)
	}
	if sourceErr.evidence.hasRawOSCode {
		t.Fatalf("synthetic short-read errno = %d, want absent", sourceErr.evidence.rawOSCode)
	}
}

func TestLinuxRangePublicEntriesCheckForkBeforeCachedReuse(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "main.iprdb")
	data := rangeImage[IPv4](t, 2, 1, 3, func(data []byte) {
		putRangeLeaf(t, rangeImagePage(data, 2), []rangeRecord[IPv4]{{from: 10, to: 20, value: 1}})
	})
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	retainedDir, component, openErr := openRetainedParent(path)
	if openErr != nil {
		t.Fatal(openErr)
	}
	defer retainedDir.file.Close()
	retained, openErr := retainedDir.openRegular(component, false)
	if openErr != nil {
		t.Fatal(openErr)
	}
	defer retained.file.Close()
	bootstrap, openErr := retained.readMainBootstrap(OpenLiveReader)
	if openErr != nil {
		t.Fatal(openErr)
	}
	source := retained.pageReadSource()
	processAccess := source.access.(*processPageAccess)
	tree, err := newRangeTree[IPv4](source, bootstrap)
	if err != nil {
		t.Fatal(err)
	}
	requireFork := func(t *testing.T, err error) {
		t.Helper()
		got := requireRangeReadCode(t, err, rangeReadErrSource)
		var sourceErr *pageSourceError
		if !errors.As(got, &sourceErr) || sourceErr.code != pageSourceErrForkedHandle {
			t.Fatalf("fork check = %T/%v", err, err)
		}
	}

	cursorOperations := map[string]func(*rangeCursor[IPv4]) error{
		"first": func(cursor *rangeCursor[IPv4]) error {
			_, err := cursor.first()
			return err
		},
		"last": func(cursor *rangeCursor[IPv4]) error {
			_, err := cursor.last()
			return err
		},
		"seek": func(cursor *rangeCursor[IPv4]) error {
			_, err := cursor.seek(15)
			return err
		},
		"next": func(cursor *rangeCursor[IPv4]) error {
			_, err := cursor.next()
			return err
		},
		"previous": func(cursor *rangeCursor[IPv4]) error {
			_, err := cursor.previous()
			return err
		},
		"current": func(cursor *rangeCursor[IPv4]) error {
			_, _, err := cursor.current()
			return err
		},
	}
	for name, operation := range cursorOperations {
		t.Run(name, func(t *testing.T) {
			cursor := tree.cursor()
			if positioned, err := cursor.seek(15); err != nil || !positioned || !cursor.scratchValid {
				t.Fatalf("initial seek = %t/%v, cache=%t", positioned, err, cursor.scratchValid)
			}
			processAccess.creatorPID++
			err := operation(&cursor)
			processAccess.creatorPID--
			requireFork(t, err)
			if cursor.state != rangeCursorFailed {
				t.Fatalf("cursor state = %d, want failed", cursor.state)
			}
		})
	}

	t.Run("lookup", func(t *testing.T) {
		processAccess.creatorPID++
		_, _, err := tree.lookup(15)
		processAccess.creatorPID--
		requireFork(t, err)
	})
	t.Run("count", func(t *testing.T) {
		processAccess.creatorPID++
		_, err := tree.countAddresses()
		processAccess.creatorPID--
		requireFork(t, err)
	})
}

func TestLinuxRangeCursorShortReadIsTerminal(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "main.iprdb")
	data := rangeImage[IPv4](t, 2, 2, 5, func(data []byte) {
		putIPv4Branch(t, rangeImagePage(data, 2), 1, []ipv4BranchTestEntry{
			{lowerFence: 0, childPage: 3, subtreeRecordCount: 1, firstFrom: 10, lastFrom: 10, lastTo: 20},
			{lowerFence: 30, childPage: 4, subtreeRecordCount: 1, firstFrom: 30, lastFrom: 30, lastTo: 40},
		})
		putRangeLeaf(t, rangeImagePage(data, 3), []rangeRecord[IPv4]{{from: 10, to: 20, value: 1}})
		putRangeLeaf(t, rangeImagePage(data, 4), []rangeRecord[IPv4]{{from: 30, to: 40, value: 2}})
	})
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	retainedDir, component, openErr := openRetainedParent(path)
	if openErr != nil {
		t.Fatal(openErr)
	}
	defer retainedDir.file.Close()
	retained, openErr := retainedDir.openRegular(component, false)
	if openErr != nil {
		t.Fatal(openErr)
	}
	defer retained.file.Close()
	bootstrap, openErr := retained.readMainBootstrap(OpenLiveReader)
	if openErr != nil {
		t.Fatal(openErr)
	}
	tree, err := newRangeTree[IPv4](retained.pageReadSource(), bootstrap)
	if err != nil {
		t.Fatal(err)
	}
	cursor := tree.cursor()
	if positioned, err := cursor.first(); err != nil || !positioned {
		t.Fatalf("first = %t/%v", positioned, err)
	}
	if err := os.Truncate(path, 4*PageSize); err != nil {
		t.Fatal(err)
	}
	positioned, err := cursor.next()
	if positioned {
		t.Fatal("short-read move positioned cursor")
	}
	requireRangeReadCode(t, err, rangeReadErrSource)
	var sourceErr *pageSourceError
	if !errors.As(err, &sourceErr) || sourceErr.code != pageSourceErrShortRead ||
		sourceErr.offset != 4*PageSize || sourceErr.expected != PageSize || sourceErr.actual != 0 {
		t.Fatalf("short-read cause = %T/%v", err, err)
	}
	if cursor.scratchValid {
		t.Fatal("failed read left stale scratch marked valid")
	}
	if cursor.state != rangeCursorFailed {
		t.Fatalf("cursor state = %d, want failed", cursor.state)
	}
	_, _, err = cursor.current()
	requireRangeReadCode(t, err, rangeReadErrCursorFailed)
}

func TestLinuxLivePositionalGapLookupAllocatesNothing(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "main.iprdb")
	data := rangeImage[IPv4](t, 2, 2, 5, func(data []byte) {
		putIPv4Branch(t, rangeImagePage(data, 2), 1, []ipv4BranchTestEntry{
			{lowerFence: 0, childPage: 3, subtreeRecordCount: 1, firstFrom: 10, lastFrom: 10, lastTo: 20},
			{lowerFence: 30, childPage: 4, subtreeRecordCount: 1, firstFrom: 30, lastFrom: 30, lastTo: 40},
		})
		putRangeLeaf(t, rangeImagePage(data, 3), []rangeRecord[IPv4]{{from: 10, to: 20, value: 1}})
		putRangeLeaf(t, rangeImagePage(data, 4), []rangeRecord[IPv4]{{from: 30, to: 40, value: 2}})
	})
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	retainedDir, component, openErr := openRetainedParent(path)
	if openErr != nil {
		t.Fatal(openErr)
	}
	defer retainedDir.file.Close()
	retained, openErr := retainedDir.openRegular(component, false)
	if openErr != nil {
		t.Fatal(openErr)
	}
	defer retained.file.Close()
	bootstrap, openErr := retained.readMainBootstrap(OpenLiveReader)
	if openErr != nil {
		t.Fatal(openErr)
	}
	tree, err := newRangeTree[IPv4](retained.pageReadSource(), bootstrap)
	if err != nil {
		t.Fatal(err)
	}
	if _, found, err := tree.lookup(25); err != nil || found {
		t.Fatalf("warm gap lookup = %t/%v", found, err)
	}
	if raceEnabled {
		t.Skip("race instrumentation changes allocation accounting")
	}
	if allocations := testing.AllocsPerRun(1000, func() {
		if _, found, lookupErr := tree.lookup(25); lookupErr != nil || found {
			panic("live gap lookup failed")
		}
	}); allocations != 0 {
		t.Fatalf("live positional gap allocations = %v, want zero", allocations)
	}
}

func benchmarkLinuxRetainedRangeTree(b *testing.B, data []byte) rangeTree[IPv4] {
	b.Helper()
	path := filepath.Join(b.TempDir(), "main.iprdb")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		b.Fatal(err)
	}
	retainedDir, component, openErr := openRetainedParent(path)
	if openErr != nil {
		b.Fatal(openErr)
	}
	b.Cleanup(func() {
		if err := retainedDir.file.Close(); err != nil {
			b.Errorf("close retained directory: %v", err)
		}
	})
	retained, openErr := retainedDir.openRegular(component, false)
	if openErr != nil {
		b.Fatal(openErr)
	}
	b.Cleanup(func() {
		if err := retained.file.Close(); err != nil {
			b.Errorf("close retained file: %v", err)
		}
	})
	bootstrap, openErr := retained.readMainBootstrap(OpenLiveReader)
	if openErr != nil {
		b.Fatal(openErr)
	}
	tree, err := newRangeTree[IPv4](newFilePageRead(retained.file, retained.creatorPID), bootstrap)
	if err != nil {
		b.Fatal(err)
	}
	return tree
}

func BenchmarkLinuxRetainedPositionalLookup(b *testing.B) {
	b.Run("direct-hit", func(b *testing.B) {
		data := rangeImage[IPv4](b, 2, 1, 3, func(data []byte) {
			putRangeLeaf(b, rangeImagePage(data, 2), []rangeRecord[IPv4]{{from: 10, to: 20, value: 1}})
		})
		tree := benchmarkLinuxRetainedRangeTree(b, data)
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			record, found, err := tree.lookup(15)
			if err != nil || !found || record.value != 1 {
				b.Fatalf("lookup = %+v/%t/%v", record, found, err)
			}
		}
	})

	b.Run("cross-leaf-gap", func(b *testing.B) {
		data := rangeImage[IPv4](b, 2, 2, 5, func(data []byte) {
			putIPv4Branch(b, rangeImagePage(data, 2), 1, []ipv4BranchTestEntry{
				{lowerFence: 0, childPage: 3, subtreeRecordCount: 1, firstFrom: 10, lastFrom: 10, lastTo: 20},
				{lowerFence: 30, childPage: 4, subtreeRecordCount: 1, firstFrom: 30, lastFrom: 30, lastTo: 40},
			})
			putRangeLeaf(b, rangeImagePage(data, 3), []rangeRecord[IPv4]{{from: 10, to: 20, value: 1}})
			putRangeLeaf(b, rangeImagePage(data, 4), []rangeRecord[IPv4]{{from: 30, to: 40, value: 2}})
		})
		tree := benchmarkLinuxRetainedRangeTree(b, data)
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			if _, found, err := tree.lookup(25); err != nil || found {
				b.Fatalf("gap lookup = %t/%v", found, err)
			}
		}
	})
}

func TestLinuxRetainedMainBootstrapReadsOnlyMetaPagesAndCurrentLength(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "main.iprdb")
	data := metaImage(emptyDirectMeta(1), emptyDirectMeta(1))
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	retainedDir, component, openErr := openRetainedParent(path)
	if openErr != nil {
		t.Fatal(openErr)
	}
	defer retainedDir.file.Close()
	retained, openErr := retainedDir.openRegular(component, true)
	if openErr != nil {
		t.Fatal(openErr)
	}
	defer retained.file.Close()
	if retained.length != 2*PageSize {
		t.Fatalf("retained open length = %d, want %d", retained.length, 2*PageSize)
	}
	if err := retained.file.Truncate(3 * PageSize); err != nil {
		t.Fatal(err)
	}

	opened, openErr := retained.readMainBootstrap(OpenLiveReader)
	if openErr != nil {
		t.Fatal(openErr)
	}
	if opened.Meta != emptyDirectMeta(1) || opened.CommittedBytes != 2*PageSize || opened.PhysicalBytes != 3*PageSize {
		t.Fatalf("retained bootstrap = %+v", opened)
	}

	originalIdentity := retained.identity
	retained.identity.inode ^= 1
	_, openErr = retained.readMainBootstrap(OpenLiveReader)
	if openErr == nil || openErr.code != linuxOSPathIdentityMismatch {
		t.Fatalf("changed retained identity bootstrap = %v", openErr)
	}
	retained.identity = originalIdentity

	_, openErr = retained.readMainBootstrap(OpenImmutableReader)
	if openErr == nil || openErr.code != linuxOSBootstrap {
		t.Fatalf("immutable retained bootstrap = %v", openErr)
	}
	requireBootstrapCode(t, openErr, BootstrapErrImmutableLengthMismatch)
}

func TestLinuxIndependentOpenDescriptionsContendOnFlock(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "main.iprdb")
	if err := os.WriteFile(path, []byte("main"), 0o600); err != nil {
		t.Fatal(err)
	}
	retainedDir, component, openErr := openRetainedParent(path)
	if openErr != nil {
		t.Fatal(openErr)
	}
	defer retainedDir.file.Close()
	first, openErr := retainedDir.openRegular(component, true)
	if openErr != nil {
		t.Fatal(openErr)
	}
	defer first.file.Close()
	second, openErr := retainedDir.openRegular(component, true)
	if openErr != nil {
		t.Fatal(openErr)
	}
	defer second.file.Close()
	if err := first.acquireLock(linuxLockExclusive, false); err != nil {
		t.Fatal(err)
	}
	if err := second.acquireLock(linuxLockExclusive, true); err == nil || err.code != linuxOSLockBusy {
		t.Fatalf("contending flock = %v", err)
	}
	if err := first.releaseLock(); err != nil {
		t.Fatal(err)
	}
	if err := second.acquireLock(linuxLockShared, true); err != nil {
		t.Fatal(err)
	}
	if err := second.releaseLock(); err != nil {
		t.Fatal(err)
	}
}

func TestLinuxInterruptibleFlockObservesCancellationWhileContended(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "main.iprdb")
	if err := os.WriteFile(path, []byte("main"), 0o600); err != nil {
		t.Fatal(err)
	}
	retainedDir, component, openErr := openRetainedParent(path)
	if openErr != nil {
		t.Fatal(openErr)
	}
	defer retainedDir.file.Close()
	first, openErr := retainedDir.openRegular(component, true)
	if openErr != nil {
		t.Fatal(openErr)
	}
	defer first.file.Close()
	second, openErr := retainedDir.openRegular(component, true)
	if openErr != nil {
		t.Fatal(openErr)
	}
	defer second.file.Close()
	if err := first.acquireLock(linuxLockExclusive, false); err != nil {
		t.Fatal(err)
	}
	defer first.releaseLock()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err := second.acquireLockContext(ctx, linuxLockExclusive)
	if err == nil || err.code != linuxOSCancelled || !errors.Is(err, context.DeadlineExceeded) || second.lock != 0 {
		t.Fatalf("interruptible flock = error %#v lock %d", err, second.lock)
	}
}

func TestLinuxPositionalIODomainRandomAndCurrentProcess(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "main.iprdb")
	if err := os.WriteFile(path, make([]byte, 16), 0o600); err != nil {
		t.Fatal(err)
	}
	retainedDir, component, openErr := openRetainedParent(path)
	if openErr != nil {
		t.Fatal(openErr)
	}
	defer retainedDir.file.Close()
	if got, err := retainedDir.sidecarComponent(component); err != nil || got != "main.iprdb.readers" {
		t.Fatalf("sidecarComponent() = (%q, %v)", got, err)
	}
	retained, openErr := retainedDir.openRegular(component, true)
	if openErr != nil {
		t.Fatal(openErr)
	}
	defer retained.file.Close()
	if err := retained.writeAllAt([]byte("abcd"), 5); err != nil {
		t.Fatal(err)
	}
	var data [4]byte
	if err := retained.readExactAt(data[:], 5); err != nil || string(data[:]) != "abcd" {
		t.Fatalf("positional read = %q, %v", data, err)
	}
	if nonce, err := randomNonzero128(); err != nil || nonce == [16]byte{} {
		t.Fatalf("randomNonzero128() = (%x, %v)", nonce, err)
	}
	if _, err := randomNonzero128With(func([]byte) error { return io.ErrUnexpectedEOF }); err == nil || err.code != linuxOSRandomFailure {
		t.Fatalf("random failure injection = %v", err)
	}
	if _, err := randomNonzero128With(func(data []byte) error { clear(data); return nil }); err == nil || err.code != linuxOSRandomZero {
		t.Fatalf("random zero injection = %v", err)
	}
	for _, reserved := range []string{".IPRANGE-owned", "main.READERS"} {
		if _, _, err := openRetainedParent(filepath.Join(directory, reserved)); err == nil || err.code != linuxOSInvalidPathComponent {
			t.Fatalf("reserved main %q accepted: %v", reserved, err)
		}
	}
	if err := validateMainPathComponent(".İprange-main"); err != nil {
		t.Fatalf("non-ASCII name matched ASCII reservation: %v", err)
	}
	highMagic := uint32(0x9123683e)
	if got := uint32(int32(highMagic)); got != highMagic {
		t.Fatalf("signed filesystem magic normalized to %#x", got)
	}
	token, tokenErr := linuxProcessDomainToken()
	if tokenErr != nil || token == [32]byte{} || token[16] != 0 {
		t.Fatalf("linuxProcessDomainToken() = (%x, %v)", token, tokenErr)
	}
	nonce := [16]byte{1}
	active := currentLinuxActiveSlot(9, nonce)
	if active.processID != uint64(os.Getpid()) || active.processStart == 0 ||
		active.taskID == 0 || active.nonce != nonce || active.txnID != 9 {
		t.Fatalf("currentLinuxActiveSlot() = %+v", active)
	}
	observation := observeLinuxProcess(active)
	if observation.kind != posixProcessExists || observation.currentStart != active.processStart {
		t.Fatalf("observeLinuxProcess() = %+v", observation)
	}
	if _, dead := classifyPOSIXDeath(active, observation); dead {
		t.Fatal("current process was classified dead")
	}
	if err := unix.Kill(os.Getpid(), 0); err != nil && !errors.Is(err, unix.EPERM) {
		t.Fatalf("self kill probe failed: %v", err)
	}
}

type linuxLivePairFixture struct {
	directory        *retainedDirectory
	mainComponent    string
	sidecarComponent string
	mainPath         string
	sidecarPath      string
	main             *retainedRegular
	sidecar          *retainedRegular
	bootstrap        Bootstrap
	header           sidecarHeader
}

func newLinuxLivePairFixture(t *testing.T) *linuxLivePairFixture {
	t.Helper()
	directoryPath := t.TempDir()
	mainPath := filepath.Join(directoryPath, "main.iprdb")
	meta := emptyDirectMeta(1)
	if err := os.WriteFile(mainPath, metaImage(meta, meta), 0o600); err != nil {
		t.Fatal(err)
	}
	directory, mainComponent, openErr := openRetainedParent(mainPath)
	if openErr != nil {
		t.Fatal(openErr)
	}
	sidecarComponent, componentErr := directory.sidecarComponent(mainComponent)
	if componentErr != nil {
		directory.file.Close()
		t.Fatal(componentErr)
	}
	sidecarPath := filepath.Join(directoryPath, sidecarComponent)
	created, err := os.OpenFile(sidecarPath, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		directory.file.Close()
		t.Fatal(err)
	}
	if err := created.Truncate(headerRegionSize + 2*int64(sidecarSlotSize)); err != nil {
		created.Close()
		directory.file.Close()
		t.Fatal(err)
	}
	if err := created.Close(); err != nil {
		directory.file.Close()
		t.Fatal(err)
	}
	main, openErr := directory.openRegular(mainComponent, true)
	if openErr != nil {
		directory.file.Close()
		t.Fatal(openErr)
	}
	sidecar, openErr := directory.openRegular(sidecarComponent, true)
	if openErr != nil {
		main.file.Close()
		directory.file.Close()
		t.Fatal(openErr)
	}
	bootstrap, openErr := main.readMainBootstrap(OpenWriter)
	if openErr != nil {
		sidecar.file.Close()
		main.file.Close()
		directory.file.Close()
		t.Fatal(openErr)
	}
	domain, domainErr := linuxProcessDomainToken()
	if domainErr != nil {
		sidecar.file.Close()
		main.file.Close()
		directory.file.Close()
		t.Fatal(domainErr)
	}
	commitment, bindingErr := basenameCommitment(basenamePOSIXBytes, []byte(mainComponent))
	if bindingErr != nil {
		sidecar.file.Close()
		main.file.Close()
		directory.file.Close()
		t.Fatal(bindingErr)
	}
	header := sidecarHeader{
		identityKind: localIdentityPOSIX, capacity: 1, state: sidecarReady,
		databaseID: meta.DatabaseID, mainIdentity: main.identity.encode(),
		sidecarIdentity: sidecar.identity.encode(), sidecarID: [16]byte{2},
		origin: sidecarOriginCreateLive, attemptedTxnID: 1,
		attemptedCommitNonce: [16]byte{3}, attemptedMainBytes: 2 * PageSize,
		attemptedMainSHA512: [64]byte{4}, processDomainKind: processDomainLinuxPIDNamespace,
		processDomainToken: domain, basenameEncoding: uint16(basenamePOSIXBytes),
		basenameLen: uint32(len(mainComponent)), basenameCommitment: commitment,
		creationSecurityKind: 1, creationSecurityCommitment: [32]byte{6}, headerSeq: 1,
	}
	writeLinuxSidecarHeaderFixture(t, sidecar, header)
	fixture := &linuxLivePairFixture{
		directory: directory, mainComponent: mainComponent, sidecarComponent: sidecarComponent,
		mainPath: mainPath, sidecarPath: sidecarPath, main: main, sidecar: sidecar,
		bootstrap: bootstrap, header: header,
	}
	t.Cleanup(func() {
		_ = sidecar.file.Close()
		_ = main.file.Close()
		_ = directory.file.Close()
	})
	return fixture
}

func (fixture *linuxLivePairFixture) verify(header sidecarHeader) *linuxOSError {
	return fixture.directory.verifyLivePairBinding(
		fixture.mainComponent, fixture.main, fixture.sidecarComponent, fixture.sidecar,
		fixture.bootstrap, header,
	)
}

func (fixture *linuxLivePairFixture) acquireLiveLocks(t *testing.T) {
	t.Helper()
	if err := fixture.main.acquireLock(linuxLockShared, false); err != nil {
		t.Fatal(err)
	}
	if err := fixture.sidecar.acquireLock(linuxLockExclusive, false); err != nil {
		t.Fatal(err)
	}
}

func TestLinuxVerifyLivePairBindingRequiresLocksAndExactBinding(t *testing.T) {
	fixture := newLinuxLivePairFixture(t)
	if err := fixture.verify(fixture.header); err == nil || err.code != linuxOSLifetimeLockRequired {
		t.Fatalf("binding without lifetime lock = %v", err)
	}
	if err := fixture.main.acquireLock(linuxLockExclusive, false); err != nil {
		t.Fatal(err)
	}
	if err := fixture.verify(fixture.header); err == nil || err.code != linuxOSLifetimeLockRequired {
		t.Fatalf("binding with exclusive main lock = %v", err)
	}
	if err := fixture.main.releaseLock(); err != nil {
		t.Fatal(err)
	}
	if err := fixture.main.acquireLock(linuxLockShared, false); err != nil {
		t.Fatal(err)
	}
	if err := fixture.verify(fixture.header); err == nil || err.code != linuxOSOperationLockRequired {
		t.Fatalf("binding without operation lock = %v", err)
	}
	if err := fixture.sidecar.acquireLock(linuxLockExclusive, false); err != nil {
		t.Fatal(err)
	}
	if err := fixture.verify(fixture.header); err != nil {
		t.Fatalf("valid live-pair binding = %v", err)
	}

	tests := []struct {
		name string
		edit func(*sidecarHeader)
		want linuxOSErrorCode
	}{
		{"database identity", func(header *sidecarHeader) { header.databaseID[0] ^= 1 }, linuxOSSidecarDatabaseMismatch},
		{"main identity", func(header *sidecarHeader) { header.mainIdentity[0] ^= 1 }, linuxOSSidecarMainIdentityMismatch},
		{"sidecar identity", func(header *sidecarHeader) { header.sidecarIdentity[0] ^= 1 }, linuxOSSidecarIdentityMismatch},
		{"basename encoding", func(header *sidecarHeader) { header.basenameEncoding = uint16(basenameWindowsUTF16) }, linuxOSSidecarBasenameMismatch},
		{"basename length", func(header *sidecarHeader) { header.basenameLen++ }, linuxOSSidecarBasenameMismatch},
		{"basename commitment", func(header *sidecarHeader) { header.basenameCommitment[0] ^= 1 }, linuxOSSidecarBasenameMismatch},
		{"process domain kind", func(header *sidecarHeader) { header.processDomainKind = processDomainHostGlobal }, linuxOSSidecarProcessDomainMismatch},
		{"process domain token", func(header *sidecarHeader) { header.processDomainToken[0] ^= 1 }, linuxOSSidecarProcessDomainMismatch},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			header := fixture.header
			test.edit(&header)
			if err := fixture.verify(header); err == nil || err.code != test.want {
				t.Fatalf("changed binding = %v, want code %d", err, test.want)
			}
		})
	}
	if err := fixture.sidecar.releaseLock(); err != nil {
		t.Fatal(err)
	}
	if err := fixture.main.releaseLock(); err != nil {
		t.Fatal(err)
	}
}

func TestLinuxVerifyLivePairBindingRechecksBothCanonicalPaths(t *testing.T) {
	for _, replaceMain := range []bool{true, false} {
		name := "sidecar"
		if replaceMain {
			name = "main"
		}
		t.Run(name, func(t *testing.T) {
			fixture := newLinuxLivePairFixture(t)
			fixture.acquireLiveLocks(t)
			path := fixture.sidecarPath
			if replaceMain {
				path = fixture.mainPath
			}
			if err := os.Rename(path, path+".old"); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("replacement"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := fixture.verify(fixture.header); err == nil || err.code != linuxOSPathIdentityMismatch {
				t.Fatalf("binding after %s replacement = %v", name, err)
			}
		})
	}
}

func TestLinuxLockedSidecarTransitionUsesRetainedDescriptor(t *testing.T) {
	directory := t.TempDir()
	mainPath := filepath.Join(directory, "main.iprdb")
	sidecarPath := mainPath + ".readers"
	retainedDir, mainComponent, openErr := openRetainedParent(mainPath)
	if openErr != nil {
		t.Fatal(openErr)
	}
	defer retainedDir.file.Close()
	sidecarComponent, nameErr := retainedDir.sidecarComponent(mainComponent)
	if nameErr != nil {
		t.Fatal(nameErr)
	}
	created, err := os.OpenFile(sidecarPath, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := created.Truncate(headerRegionSize + 3*int64(sidecarSlotSize)); err != nil {
		t.Fatal(err)
	}
	created.Close()
	sidecar, openErr := retainedDir.openRegular(sidecarComponent, true)
	if openErr != nil {
		t.Fatal(openErr)
	}
	defer sidecar.file.Close()
	mainIdentity := sidecar.identity.encode()
	mainIdentity[0] ^= 1
	domain, domainErr := linuxProcessDomainToken()
	if domainErr != nil {
		t.Fatal(domainErr)
	}
	header := sidecarHeader{
		identityKind: localIdentityPOSIX, capacity: 2, state: sidecarReady,
		databaseID: [16]byte{1}, mainIdentity: mainIdentity,
		sidecarIdentity: sidecar.identity.encode(), sidecarID: [16]byte{2},
		origin: sidecarOriginCreateLive, attemptedTxnID: 1,
		attemptedCommitNonce: [16]byte{3}, attemptedMainBytes: 8192,
		attemptedMainSHA512: [64]byte{4}, processDomainKind: processDomainLinuxPIDNamespace,
		processDomainToken: domain, basenameEncoding: 1,
		basenameLen: uint32(len(mainComponent)), basenameCommitment: [32]byte{5},
		creationSecurityKind: 1, creationSecurityCommitment: [32]byte{6}, headerSeq: 1,
	}
	var block [PageSize]byte
	header.encodeInto(&block)
	if decoded, problem := decodeSidecarHeader(block[:]); problem != 0 || decoded != header {
		t.Fatalf("encoded sidecar header = %#v, problem=%d", decoded, problem)
	}
	if err := sidecar.writeAllAt(block[:], 0); err != nil {
		t.Fatal(err)
	}
	if err := sidecar.writeAllAt(block[:], PageSize); err != nil {
		t.Fatal(err)
	}

	target := currentLinuxActiveSlot(0, [16]byte{7})
	var zero [sidecarSlotSize]byte
	prepared, transitionErr := prepareSlotClaim(header, slotReader, 1, &zero, target, linuxSlotHostLimits())
	if transitionErr != nil {
		t.Fatal(transitionErr)
	}
	executionErr := sidecar.executeSidecarSlotTransition(prepared, linuxSlotHostLimits())
	if executionErr == nil || executionErr.storage == nil {
		t.Fatalf("unlocked execution = %#v", executionErr)
	}
	var linuxErr *linuxOSError
	if !errors.As(executionErr, &linuxErr) || linuxErr.code != linuxOSOperationLockRequired || sidecar.cleanupAuthority.kind != linuxCleanupNone {
		t.Fatalf("unlocked execution details = (%#v, %#v)", executionErr, sidecar.cleanupAuthority)
	}

	prepared, transitionErr = prepareSlotClaim(header, slotReader, 1, &zero, target, linuxSlotHostLimits())
	if transitionErr != nil {
		t.Fatal(transitionErr)
	}
	if err := sidecar.acquireLock(linuxLockExclusive, false); err != nil {
		t.Fatal(err)
	}
	if executionErr := sidecar.executeSidecarSlotTransition(prepared, linuxSlotHostLimits()); executionErr != nil {
		t.Fatalf("locked execution = %#v, storage=%#v, transition=%#v", executionErr, executionErr.storage, executionErr.transition)
	}
	if sidecar.cleanupAuthority.kind != linuxCleanupNone {
		t.Fatal("successful execution retained provenance")
	}
	activeImage, readErr := sidecar.readSidecarSlot(header, 1)
	if readErr != nil {
		t.Fatal(readErr)
	}
	stable, problem := decodeStableSlot(activeImage[:], slotReader, linuxSlotHostLimits())
	if problem != 0 || !stable.active || stable.claim != target {
		t.Fatalf("active sidecar slot = %#v, problem %d", stable, problem)
	}

	prepared, transitionErr = prepareSlotClearOwned(
		header, slotReader, 1, &activeImage, target, linuxSlotHostLimits(),
	)
	if transitionErr != nil {
		t.Fatal(transitionErr)
	}
	provenance, transitionErr := prepared.arm()
	if transitionErr != nil {
		t.Fatal(transitionErr)
	}
	offset, offsetErr := sidecarSlotOffset(header, 1)
	if offsetErr != nil {
		t.Fatal(offsetErr)
	}
	state2, transitionErr := provenance.state2Bytes()
	if transitionErr != nil {
		t.Fatal(transitionErr)
	}
	if err := sidecar.writeAllAt(state2[:], offset); err != nil {
		t.Fatal(err)
	}
	sidecar.cleanupAuthority = linuxSidecarCleanupAuthority{kind: linuxCleanupArmed, armed: provenance}
	if err := sidecar.releaseLock(); err == nil || err.code != linuxOSArmedTransition {
		t.Fatalf("release with armed transition = %v", err)
	}
	disposition, executionErr := sidecar.retrySidecarSlotCleanup(linuxSlotHostLimits())
	if executionErr != nil || disposition != cleanupAlreadyAbsent || sidecar.cleanupAuthority.kind != linuxCleanupNone {
		t.Fatalf("cleanup = (%d, %#v, %#v)", disposition, executionErr, sidecar.cleanupAuthority)
	}
	cleared, readErr := sidecar.readSidecarSlot(header, 1)
	if readErr != nil || cleared != [sidecarSlotSize]byte{} {
		t.Fatalf("cleared slot = %x, %v", cleared, readErr)
	}
	if err := sidecar.releaseLock(); err != nil {
		t.Fatal(err)
	}
}

type linuxSidecarFixtureSlot struct {
	index uint32
	claim activeSlot
}

func newLinuxReadySidecarFixture(
	t *testing.T,
	capacity uint32,
	slots []linuxSidecarFixtureSlot,
	writable bool,
) (*retainedRegular, sidecarHeader) {
	t.Helper()
	directory := t.TempDir()
	mainPath := filepath.Join(directory, "main.iprdb")
	sidecarPath := mainPath + ".readers"
	retainedDir, mainComponent, openErr := openRetainedParent(mainPath)
	if openErr != nil {
		t.Fatal(openErr)
	}
	sidecarComponent, componentErr := retainedDir.sidecarComponent(mainComponent)
	if componentErr != nil {
		t.Fatal(componentErr)
	}
	created, err := os.OpenFile(sidecarPath, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := created.Truncate(headerRegionSize + int64(capacity+1)*int64(sidecarSlotSize)); err != nil {
		created.Close()
		t.Fatal(err)
	}
	if err := created.Close(); err != nil {
		t.Fatal(err)
	}
	bootstrap, openErr := retainedDir.openRegular(sidecarComponent, true)
	if openErr != nil {
		t.Fatal(openErr)
	}
	domain, domainErr := linuxProcessDomainToken()
	if domainErr != nil {
		t.Fatal(domainErr)
	}
	mainIdentity := bootstrap.identity.encode()
	mainIdentity[0] ^= 1
	header := sidecarHeader{
		identityKind: localIdentityPOSIX, capacity: capacity, state: sidecarReady,
		databaseID: [16]byte{1}, mainIdentity: mainIdentity,
		sidecarIdentity: bootstrap.identity.encode(), sidecarID: [16]byte{2},
		origin: sidecarOriginCreateLive, attemptedTxnID: 1,
		attemptedCommitNonce: [16]byte{3}, attemptedMainBytes: 8192,
		attemptedMainSHA512: [64]byte{4}, processDomainKind: processDomainLinuxPIDNamespace,
		processDomainToken: domain, basenameEncoding: 1,
		basenameLen: uint32(len(mainComponent)), basenameCommitment: [32]byte{5},
		creationSecurityKind: 1, creationSecurityCommitment: [32]byte{6}, headerSeq: 1,
	}
	writeLinuxSidecarHeaderFixture(t, bootstrap, header)
	for _, slot := range slots {
		if slot.index > capacity {
			bootstrap.file.Close()
			t.Fatalf("fixture slot %d exceeds capacity %d", slot.index, capacity)
		}
		offset, offsetErr := sidecarSlotOffset(header, slot.index)
		if offsetErr != nil {
			bootstrap.file.Close()
			t.Fatal(offsetErr)
		}
		encoded := encodeActiveSlot(slot.claim)
		if err := bootstrap.writeAllAt(encoded[:], offset); err != nil {
			bootstrap.file.Close()
			t.Fatal(err)
		}
	}
	if err := bootstrap.file.Close(); err != nil {
		t.Fatal(err)
	}
	sidecar, openErr := retainedDir.openRegular(sidecarComponent, writable)
	if openErr != nil {
		t.Fatal(openErr)
	}
	t.Cleanup(func() {
		_ = sidecar.file.Close()
		_ = retainedDir.file.Close()
	})
	return sidecar, header
}

func writeLinuxSidecarHeaderFixture(t *testing.T, sidecar *retainedRegular, header sidecarHeader) {
	t.Helper()
	var block [PageSize]byte
	header.encodeInto(&block)
	if err := sidecar.writeAllAt(block[:], 0); err != nil {
		t.Fatal(err)
	}
	if err := sidecar.writeAllAt(block[:], PageSize); err != nil {
		t.Fatal(err)
	}
}

func TestLinuxReadySidecarScanStopsAtDeadWriterAndRetainsOnlyWriterAuthority(t *testing.T) {
	writer := activeSlot{txnID: 4, processID: 100, processStart: 10, nonce: [16]byte{1}}
	reader := activeSlot{txnID: 4, processID: 101, processStart: 11, nonce: [16]byte{2}}
	sidecar, header := newLinuxReadySidecarFixture(t, 2, []linuxSidecarFixtureSlot{
		{0, writer}, {1, reader},
	}, true)
	if err := sidecar.acquireLock(linuxLockExclusive, false); err != nil {
		t.Fatal(err)
	}
	observed := 0
	_, err := sidecar.scanAndReapReadySidecarWithObserver(
		header, 5, func(active activeSlot) posixProcessObservation {
			observed++
			return posixProcessObservation{kind: posixProcessMissing}
		},
	)
	var scanErr *linuxSidecarScanError
	if !errors.As(err, &scanErr) || scanErr.code != linuxSidecarScanDeadWriter ||
		scanErr.active != writer || scanErr.proof.kind != deathProofPOSIXMissing || observed != 1 {
		t.Fatalf("dead-writer scan = error %#v observed=%d", err, observed)
	}
	writerRaw := encodeActiveSlot(writer)
	wantWriter := linuxDeadWriterObligation{
		header: header, raw: writerRaw, active: writer,
		proof: deathProof{kind: deathProofPOSIXMissing, processID: writer.processID},
	}
	if sidecar.cleanupAuthority.kind != linuxCleanupDeadWriter ||
		sidecar.cleanupAuthority.writer != wantWriter || sidecar.lock != linuxLockExclusive {
		t.Fatalf("dead-writer authority = %#v lock %d", sidecar.cleanupAuthority, sidecar.lock)
	}
	for index, want := range map[uint32]activeSlot{0: writer, 1: reader} {
		raw, readErr := sidecar.readSidecarSlot(header, index)
		if readErr != nil {
			t.Fatal(readErr)
		}
		role := slotReader
		if index == 0 {
			role = slotWriter
		}
		stable, problem := decodeStableSlot(raw[:], role, linuxSlotHostLimits())
		if problem != 0 || !stable.active || stable.claim != want {
			t.Fatalf("surviving slot %d = %#v problem %d", index, stable, problem)
		}
	}
	directClear, transitionErr := prepareSlotClearProvenDead(
		header, slotWriter, 0, &writerRaw, writer, wantWriter.proof, linuxSlotHostLimits(),
	)
	if transitionErr != nil {
		t.Fatal(transitionErr)
	}
	var linuxErr *linuxOSError
	if executionErr := sidecar.executeSidecarSlotTransition(directClear, linuxSlotHostLimits()); !errors.As(executionErr, &linuxErr) || linuxErr.code != linuxOSWriterClearRequiresMainTail {
		t.Fatalf("generic writer clear = %#v", executionErr)
	}
	authority := sidecar.cleanupAuthority
	var zero [sidecarSlotSize]byte
	unrelated := activeSlot{txnID: 0, processID: 106, processStart: 16, nonce: [16]byte{7}}
	prepared, transitionErr := prepareSlotClaim(
		header, slotReader, 2, &zero, unrelated, linuxSlotHostLimits(),
	)
	if transitionErr != nil {
		t.Fatal(transitionErr)
	}
	executionErr := sidecar.executeSidecarSlotTransition(prepared, linuxSlotHostLimits())
	var writerCleanupErr *linuxOSError
	if !errors.As(executionErr, &writerCleanupErr) || writerCleanupErr.code != linuxOSWriterCleanupRequired ||
		sidecar.cleanupAuthority != authority {
		t.Fatalf("unrelated transition with dead writer = error %#v authority=%#v",
			executionErr, sidecar.cleanupAuthority)
	}
	stillZero, readErr := sidecar.readSidecarSlot(header, 2)
	if readErr != nil || stillZero != zero {
		t.Fatalf("refused transition changed reader slot = %x, %v", stillZero, readErr)
	}
	if _, err := sidecar.scanAndReapReadySidecarWithObserver(
		header, 5, func(activeSlot) posixProcessObservation {
			t.Fatal("repeated scan observed a process with pending writer cleanup")
			return posixProcessObservation{}
		},
	); err == nil {
		t.Fatal("repeated scan accepted pending dead-writer obligation")
	} else {
		var linuxErr *linuxOSError
		if !errors.As(err, &linuxErr) || linuxErr.code != linuxOSWriterCleanupRequired ||
			sidecar.cleanupAuthority != authority {
			t.Fatalf("repeated scan = error %#v authority %#v", err, sidecar.cleanupAuthority)
		}
	}
	if err := sidecar.releaseLock(); err == nil || err.code != linuxOSWriterCleanupRequired {
		t.Fatalf("release with dead-writer cleanup = %v", err)
	}
}

func TestLinuxReadySidecarScanChecksDomainBeforeSlots(t *testing.T) {
	sidecar, header := newLinuxReadySidecarFixture(t, 1, nil, true)
	header.processDomainToken[0] ^= 1
	header.headerSeq++
	writeLinuxSidecarHeaderFixture(t, sidecar, header)
	offset, offsetErr := sidecarSlotOffset(header, 1)
	if offsetErr != nil {
		t.Fatal(offsetErr)
	}
	if err := sidecar.writeAllAt([]byte{2, 0, 0, 0}, offset); err != nil {
		t.Fatal(err)
	}
	if err := sidecar.acquireLock(linuxLockExclusive, false); err != nil {
		t.Fatal(err)
	}
	observed := false
	_, err := sidecar.scanAndReapReadySidecarWithObserver(
		header, 5, func(activeSlot) posixProcessObservation {
			observed = true
			return posixProcessObservation{kind: posixProcessUncertain}
		},
	)
	readyErr := requireReadySidecarCode(t, err, readySidecarErrProcessDomainMismatch)
	if readyErr == nil || observed || sidecar.cleanupAuthority.kind != linuxCleanupNone || sidecar.lock != linuxLockExclusive {
		t.Fatalf("domain-first result = error %#v observed=%v authority=%#v lock=%d", readyErr, observed, sidecar.cleanupAuthority, sidecar.lock)
	}
	if err := sidecar.releaseLock(); err != nil {
		t.Fatal(err)
	}
}

func TestLinuxReadySidecarStructuralPassPreventsPartialReaping(t *testing.T) {
	dead := activeSlot{txnID: 5, processID: 151, processStart: 15, nonce: [16]byte{1}}
	sidecar, header := newLinuxReadySidecarFixture(t, 2, []linuxSidecarFixtureSlot{{1, dead}}, true)
	offset, offsetErr := sidecarSlotOffset(header, 2)
	if offsetErr != nil {
		t.Fatal(offsetErr)
	}
	if err := sidecar.writeAllAt([]byte{2, 0, 0, 0}, offset); err != nil {
		t.Fatal(err)
	}
	if err := sidecar.acquireLock(linuxLockExclusive, false); err != nil {
		t.Fatal(err)
	}
	observed := false
	_, err := sidecar.scanAndReapReadySidecarWithObserver(
		header, 5, func(activeSlot) posixProcessObservation {
			observed = true
			return posixProcessObservation{kind: posixProcessMissing}
		},
	)
	got := requireReadySidecarCode(t, err, readySidecarErrSlot)
	if got.index != 2 || got.problem != slotTransition || observed {
		t.Fatalf("structural pass = error %#v observed=%v", got, observed)
	}
	raw, readErr := sidecar.readSidecarSlot(header, 1)
	if readErr != nil {
		t.Fatal(readErr)
	}
	stable, problem := decodeStableSlot(raw[:], slotReader, linuxSlotHostLimits())
	if problem != 0 || !stable.active || stable.claim != dead || sidecar.cleanupAuthority.kind != linuxCleanupNone || sidecar.lock != linuxLockExclusive {
		t.Fatalf("dead reader changed before full validation: slot %#v problem=%d authority=%#v lock=%d", stable, problem, sidecar.cleanupAuthority, sidecar.lock)
	}
	if err := sidecar.releaseLock(); err != nil {
		t.Fatal(err)
	}
}

func TestLinuxReadySidecarCancellationBeforeReapingChangesNoSlot(t *testing.T) {
	dead := activeSlot{txnID: 5, processID: 159, processStart: 15, nonce: [16]byte{1}}
	sidecar, header := newLinuxReadySidecarFixture(t, 1, []linuxSidecarFixtureSlot{{1, dead}}, true)
	if err := sidecar.acquireLock(linuxLockExclusive, false); err != nil {
		t.Fatal(err)
	}
	defer sidecar.releaseLock()
	ctx, cancel := context.WithCancel(context.Background())
	observed := 0
	_, err := sidecar.scanAndReapReadySidecarWithObserverContext(
		ctx, header, 5, func(active activeSlot) posixProcessObservation {
			observed++
			cancel()
			return posixProcessObservation{kind: posixProcessMissing}
		},
	)
	var scanErr *linuxSidecarScanError
	if !errors.As(err, &scanErr) || scanErr.code != linuxSidecarScanCancelled || observed != 1 {
		t.Fatalf("cancelled scan = error %#v observations %d", err, observed)
	}
	raw, readErr := sidecar.readSidecarSlot(header, 1)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if raw != encodeActiveSlot(dead) || sidecar.cleanupAuthority.kind != linuxCleanupNone {
		t.Fatalf("cancelled reap changed slot/authority = %x %#v", raw, sidecar.cleanupAuthority)
	}
}

func TestLinuxReadySidecarRejectsZeroSelectedTransactionBeforeScan(t *testing.T) {
	reader := activeSlot{txnID: 1, processID: 161, processStart: 16, nonce: [16]byte{1}}
	sidecar, header := newLinuxReadySidecarFixture(t, 1, []linuxSidecarFixtureSlot{{1, reader}}, true)
	if err := sidecar.acquireLock(linuxLockExclusive, false); err != nil {
		t.Fatal(err)
	}
	observed := false
	_, err := sidecar.scanAndReapReadySidecarWithObserver(
		header, 0, func(activeSlot) posixProcessObservation {
			observed = true
			return posixProcessObservation{kind: posixProcessMissing}
		},
	)
	requireReadySidecarCode(t, err, readySidecarErrSelectedTransactionZero)
	if observed || sidecar.cleanupAuthority.kind != linuxCleanupNone || sidecar.lock != linuxLockExclusive {
		t.Fatalf("zero selected transaction scanned: observed=%v authority=%#v lock=%d", observed, sidecar.cleanupAuthority, sidecar.lock)
	}
	if err := sidecar.releaseLock(); err != nil {
		t.Fatal(err)
	}
}

func TestLinuxReadySidecarSecondPassIsStructuralBeforeTransactions(t *testing.T) {
	future := activeSlot{txnID: 6, processID: 201, processStart: 21, nonce: [16]byte{1}}
	last := activeSlot{txnID: 5, processID: 202, processStart: 22, nonce: [16]byte{2}}
	sidecar, header := newLinuxReadySidecarFixture(t, 2, []linuxSidecarFixtureSlot{
		{1, future}, {2, last},
	}, true)
	if err := sidecar.acquireLock(linuxLockExclusive, false); err != nil {
		t.Fatal(err)
	}
	_, err := sidecar.scanAndReapReadySidecarWithObserver(
		header,
		5,
		func(active activeSlot) posixProcessObservation {
			if active.processID == last.processID {
				offset, offsetErr := sidecarSlotOffset(header, 2)
				if offsetErr != nil {
					t.Fatal(offsetErr)
				}
				if writeErr := sidecar.writeAllAt([]byte{2, 0, 0, 0}, offset); writeErr != nil {
					t.Fatal(writeErr)
				}
			}
			return posixProcessObservation{kind: posixProcessExists, currentStart: active.processStart}
		},
	)
	got := requireReadySidecarCode(t, err, readySidecarErrSlot)
	if got.index != 2 || got.problem != slotTransition {
		t.Fatalf("second-pass structural error = %#v", got)
	}
	if sidecar.cleanupAuthority.kind != linuxCleanupNone || sidecar.lock != linuxLockExclusive {
		t.Fatalf("second-pass ownership = authority %#v lock %d", sidecar.cleanupAuthority, sidecar.lock)
	}
	if err := sidecar.releaseLock(); err != nil {
		t.Fatal(err)
	}
}

func TestLinuxReadySidecarValidatesSurvivorTransactions(t *testing.T) {
	tests := []struct {
		name  string
		index uint32
		claim activeSlot
		code  readySidecarErrorCode
	}{
		{
			name: "writer mismatch", index: 0,
			claim: activeSlot{txnID: 4, processID: 251, processStart: 25, nonce: [16]byte{1}},
			code:  readySidecarErrWriterTransactionMismatch,
		},
		{
			name: "future reader", index: 1,
			claim: activeSlot{txnID: 6, processID: 252, processStart: 26, nonce: [16]byte{2}},
			code:  readySidecarErrReaderTransactionFuture,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sidecar, header := newLinuxReadySidecarFixture(
				t, 1, []linuxSidecarFixtureSlot{{tc.index, tc.claim}}, true,
			)
			if err := sidecar.acquireLock(linuxLockExclusive, false); err != nil {
				t.Fatal(err)
			}
			_, err := sidecar.scanAndReapReadySidecarWithObserver(
				header,
				5,
				func(active activeSlot) posixProcessObservation {
					return posixProcessObservation{kind: posixProcessExists, currentStart: active.processStart}
				},
			)
			got := requireReadySidecarCode(t, err, tc.code)
			if got.expected != 5 || got.actual != tc.claim.txnID ||
				sidecar.cleanupAuthority.kind != linuxCleanupNone || sidecar.lock != linuxLockExclusive {
				t.Fatalf("transaction result = %#v authority=%#v lock=%d", got, sidecar.cleanupAuthority, sidecar.lock)
			}
			if err := sidecar.releaseLock(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestLinuxReadySidecarReselectsExactHeaderBetweenPasses(t *testing.T) {
	reader := activeSlot{txnID: 5, processID: 301, processStart: 31, nonce: [16]byte{1}}
	sidecar, header := newLinuxReadySidecarFixture(t, 1, []linuxSidecarFixtureSlot{{1, reader}}, true)
	if err := sidecar.acquireLock(linuxLockExclusive, false); err != nil {
		t.Fatal(err)
	}
	changed := false
	_, err := sidecar.scanAndReapReadySidecarWithObserver(
		header,
		5,
		func(active activeSlot) posixProcessObservation {
			if !changed {
				changed = true
				newHeader := header
				newHeader.headerSeq++
				writeLinuxSidecarHeaderFixture(t, sidecar, newHeader)
			}
			return posixProcessObservation{kind: posixProcessExists, currentStart: active.processStart}
		},
	)
	var linuxErr *linuxOSError
	if !errors.As(err, &linuxErr) || linuxErr.code != linuxOSSidecarHeaderChanged || !changed {
		t.Fatalf("header reselection = changed=%v error %#v", changed, err)
	}
	if sidecar.cleanupAuthority.kind != linuxCleanupNone || sidecar.lock != linuxLockExclusive {
		t.Fatalf("header-change ownership = authority %#v lock %d", sidecar.cleanupAuthority, sidecar.lock)
	}
	if err := sidecar.releaseLock(); err != nil {
		t.Fatal(err)
	}
}

func TestLinuxReadySidecarTransitionFailureRetainsSingleArmedAuthority(t *testing.T) {
	reader := activeSlot{txnID: 5, processID: 401, processStart: 41, nonce: [16]byte{1}}
	sidecar, header := newLinuxReadySidecarFixture(t, 1, []linuxSidecarFixtureSlot{{1, reader}}, false)
	if err := sidecar.acquireLock(linuxLockExclusive, false); err != nil {
		t.Fatal(err)
	}
	_, err := sidecar.scanAndReapReadySidecarWithObserver(
		header,
		5,
		func(activeSlot) posixProcessObservation {
			return posixProcessObservation{kind: posixProcessMissing}
		},
	)
	var executionErr *slotExecutionError
	if !errors.As(err, &executionErr) || executionErr.storage == nil ||
		sidecar.cleanupAuthority.kind != linuxCleanupArmed || sidecar.cleanupAuthority.armed == nil ||
		!sidecar.cleanupAuthority.armed.isArmed() || sidecar.cleanupAuthority.writer != (linuxDeadWriterObligation{}) ||
		sidecar.lock != linuxLockExclusive {
		t.Fatalf("interrupted reap = error %#v authority %#v lock %d", err, sidecar.cleanupAuthority, sidecar.lock)
	}
	armed := sidecar.cleanupAuthority.armed
	if _, retryErr := sidecar.retrySidecarSlotCleanup(linuxSlotHostLimits()); retryErr == nil || retryErr.storage == nil ||
		sidecar.cleanupAuthority.kind != linuxCleanupArmed || sidecar.cleanupAuthority.armed != armed ||
		sidecar.cleanupAuthority.writer != (linuxDeadWriterObligation{}) {
		t.Fatalf("retry retained authority = error %#v authority %#v", retryErr, sidecar.cleanupAuthority)
	}
	if unlockErr := sidecar.releaseLock(); unlockErr == nil || unlockErr.code != linuxOSArmedTransition {
		t.Fatalf("armed reap unlock = %v", unlockErr)
	}
}

type linuxDeadWriterLivePairFixture struct {
	pair        *retainedLiveFiles
	mainPath    string
	sidecarPath string
	writer      activeSlot
}

type linuxRetainedHeaderMutation uint8

const (
	linuxRetainedHeaderValidChanged linuxRetainedHeaderMutation = iota + 1
	linuxRetainedHeaderMalformed
	linuxRetainedHeaderTorn
)

func (mutation linuxRetainedHeaderMutation) name() string {
	switch mutation {
	case linuxRetainedHeaderValidChanged:
		return "valid-changed"
	case linuxRetainedHeaderMalformed:
		return "malformed"
	case linuxRetainedHeaderTorn:
		return "torn"
	default:
		return "unknown"
	}
}

func mutateLinuxRetainedSidecarHeader(
	t testing.TB,
	sidecar *retainedRegular,
	expected sidecarHeader,
	mutation linuxRetainedHeaderMutation,
) {
	t.Helper()
	var original [PageSize]byte
	expected.encodeInto(&original)
	changedHeader := expected
	changedHeader.headerSeq++
	var changed [PageSize]byte
	changedHeader.encodeInto(&changed)
	left, right := original, original
	switch mutation {
	case linuxRetainedHeaderValidChanged:
		left, right = changed, changed
	case linuxRetainedHeaderMalformed:
		left, right = [PageSize]byte{}, [PageSize]byte{}
	case linuxRetainedHeaderTorn:
		right = changed
	default:
		t.Fatalf("unknown retained header mutation %d", mutation)
	}
	if err := sidecar.writeAllAt(left[:], 0); err != nil {
		t.Fatal(err)
	}
	if err := sidecar.writeAllAt(right[:], PageSize); err != nil {
		t.Fatal(err)
	}
}

func TestLinuxLivePairOpenBootstrapsMainBeforeOpeningSidecar(t *testing.T) {
	mainPath := filepath.Join(t.TempDir(), "main.iprdb")
	if err := os.WriteFile(mainPath, make([]byte, 2*PageSize), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := openLockedRetainedLiveFiles(mainPath)
	var linuxErr *linuxOSError
	if err == nil || err.code != linuxLivePairOS || !errors.As(err, &linuxErr) || linuxErr.code != linuxOSBootstrap {
		t.Fatalf("invalid main without sidecar = %#v", err)
	}
}

func TestLinuxLivePairOpenRebootstrapsMainAfterBothLocks(t *testing.T) {
	fixture := newLinuxLivePairFixture(t)
	if err := fixture.sidecar.file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := fixture.main.file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := fixture.directory.file.Close(); err != nil {
		t.Fatal(err)
	}

	_, err := openLockedRetainedLiveFilesWithPreBinding(
		fixture.mainPath,
		func(main *retainedRegular) *linuxOSError {
			changed := emptyDirectMeta(1)
			changed.DatabaseID[0] ^= 1
			page := changed.EncodePage()
			if err := main.writeAllAt(page[:], 0); err != nil {
				return err
			}
			return main.writeAllAt(page[:], PageSize)
		},
	)
	var linuxErr *linuxOSError
	if err == nil || err.code != linuxLivePairOS || !errors.As(err, &linuxErr) ||
		linuxErr.code != linuxOSSidecarDatabaseMismatch {
		t.Fatalf("changed main after both locks = %#v", err)
	}
}

func newLinuxDeadWriterLivePairFixture(t *testing.T, physicalPages int) linuxDeadWriterLivePairFixture {
	t.Helper()
	if physicalPages < 2 {
		t.Fatalf("physical page count %d is below the two meta pages", physicalPages)
	}
	directoryPath := t.TempDir()
	mainPath := filepath.Join(directoryPath, "main.iprdb")
	sidecarPath := mainPath + ".readers"
	meta := emptyDirectMeta(1)
	mainImage := make([]byte, physicalPages*PageSize)
	copy(mainImage, metaImage(meta, meta))
	if err := os.WriteFile(mainPath, mainImage, 0o600); err != nil {
		t.Fatal(err)
	}
	directory, mainComponent, openErr := openRetainedParent(mainPath)
	if openErr != nil {
		t.Fatal(openErr)
	}
	sidecarComponent, componentErr := directory.sidecarComponent(mainComponent)
	if componentErr != nil {
		directory.file.Close()
		t.Fatal(componentErr)
	}
	created, err := os.OpenFile(sidecarPath, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		directory.file.Close()
		t.Fatal(err)
	}
	if err := created.Truncate(headerRegionSize + 2*int64(sidecarSlotSize)); err != nil {
		created.Close()
		directory.file.Close()
		t.Fatal(err)
	}
	if err := created.Close(); err != nil {
		directory.file.Close()
		t.Fatal(err)
	}
	main, openErr := directory.openRegular(mainComponent, true)
	if openErr != nil {
		directory.file.Close()
		t.Fatal(openErr)
	}
	sidecar, openErr := directory.openRegular(sidecarComponent, true)
	if openErr != nil {
		main.file.Close()
		directory.file.Close()
		t.Fatal(openErr)
	}
	domain, domainErr := linuxProcessDomainToken()
	if domainErr != nil {
		sidecar.file.Close()
		main.file.Close()
		directory.file.Close()
		t.Fatal(domainErr)
	}
	commitment, bindingErr := basenameCommitment(basenamePOSIXBytes, []byte(mainComponent))
	if bindingErr != nil {
		sidecar.file.Close()
		main.file.Close()
		directory.file.Close()
		t.Fatal(bindingErr)
	}
	header := sidecarHeader{
		identityKind: localIdentityPOSIX, capacity: 1, state: sidecarReady,
		databaseID: meta.DatabaseID, mainIdentity: main.identity.encode(),
		sidecarIdentity: sidecar.identity.encode(), sidecarID: [16]byte{2},
		origin: sidecarOriginCreateLive, attemptedTxnID: 1,
		attemptedCommitNonce: [16]byte{3}, attemptedMainBytes: 2 * PageSize,
		attemptedMainSHA512: [64]byte{4}, processDomainKind: processDomainLinuxPIDNamespace,
		processDomainToken: domain, basenameEncoding: uint16(basenamePOSIXBytes),
		basenameLen: uint32(len(mainComponent)), basenameCommitment: commitment,
		creationSecurityKind: 1, creationSecurityCommitment: [32]byte{6}, headerSeq: 1,
	}
	writeLinuxSidecarHeaderFixture(t, sidecar, header)
	writer := activeSlot{
		txnID: 1, processID: 41, processStart: 141, taskID: 241, nonce: [16]byte{9},
	}
	writerImage := encodeActiveSlot(writer)
	offset, offsetErr := sidecarSlotOffset(header, 0)
	if offsetErr != nil {
		t.Fatal(offsetErr)
	}
	if err := sidecar.writeAllAt(writerImage[:], offset); err != nil {
		t.Fatal(err)
	}
	if err := sidecar.file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := main.file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := directory.file.Close(); err != nil {
		t.Fatal(err)
	}
	pair, pairErr := openLockedRetainedLiveFiles(mainPath)
	if pairErr != nil {
		t.Fatal(pairErr)
	}
	t.Cleanup(func() {
		_ = pair.sidecar.file.Close()
		_ = pair.main.file.Close()
		_ = pair.directory.file.Close()
	})
	return linuxDeadWriterLivePairFixture{
		pair: pair, mainPath: mainPath, sidecarPath: sidecarPath, writer: writer,
	}
}

func retainMissingLinuxWriter(t *testing.T, fixture linuxDeadWriterLivePairFixture) {
	t.Helper()
	_, err := fixture.pair.scanAndReapWithObserver(func(active activeSlot) posixProcessObservation {
		return posixProcessObservation{kind: posixProcessMissing}
	})
	var scanErr *linuxSidecarScanError
	if err == nil || err.code != linuxLivePairScan || !errors.As(err, &scanErr) ||
		scanErr.code != linuxSidecarScanDeadWriter || scanErr.active != fixture.writer {
		t.Fatalf("retain dead writer = %#v", err)
	}
	if fixture.pair.sidecar.cleanupAuthority.kind != linuxCleanupDeadWriter {
		t.Fatalf("retained authority = %#v", fixture.pair.sidecar.cleanupAuthority)
	}
}

func retainedFileLength(t *testing.T, retained *retainedRegular) uint64 {
	t.Helper()
	info, err := retained.file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	return uint64(info.Size())
}

func TestLinuxLivePairTruncatesSyncsAndOnlyThenClearsDeadWriter(t *testing.T) {
	fixture := newLinuxDeadWriterLivePairFixture(t, 3)
	retainMissingLinuxWriter(t, fixture)
	if err := fixture.pair.retryDeadWriterCleanup(); err != nil {
		t.Fatal(err)
	}
	if length := retainedFileLength(t, fixture.pair.main); length != 2*PageSize {
		t.Fatalf("main length = %d, want %d", length, 2*PageSize)
	}
	writerImage, readErr := fixture.pair.sidecar.readSidecarSlot(fixture.pair.header, 0)
	if readErr != nil || writerImage != [sidecarSlotSize]byte{} {
		t.Fatalf("writer slot = %x, %v", writerImage, readErr)
	}
	if fixture.pair.sidecar.cleanupAuthority.kind != linuxCleanupNone {
		t.Fatalf("cleanup authority = %#v", fixture.pair.sidecar.cleanupAuthority)
	}
	inspection, scanErr := fixture.pair.scanAndReap()
	if scanErr != nil || inspection.writerActive {
		t.Fatalf("post-cleanup scan = %#v, %#v", inspection, scanErr)
	}
}

func TestLinuxDeadWriterHeaderBoundariesRetainDestructiveAuthority(t *testing.T) {
	mutations := []linuxRetainedHeaderMutation{
		linuxRetainedHeaderValidChanged,
		linuxRetainedHeaderMalformed,
		linuxRetainedHeaderTorn,
	}
	boundaries := []struct {
		name       string
		wantCode   linuxLivePairErrorCode
		wantLength uint64
		run        func(testing.TB, linuxDeadWriterLivePairFixture, linuxRetainedHeaderMutation) *linuxLivePairError
	}{
		{
			name: "immediately before truncate", wantCode: linuxLivePairOS, wantLength: 3 * PageSize,
			run: func(t testing.TB, fixture linuxDeadWriterLivePairFixture, mutation linuxRetainedHeaderMutation) *linuxLivePairError {
				return fixture.pair.retryDeadWriterCleanupWithTransition(
					defaultLinuxWriterTruncate, defaultLinuxWriterSync, nil,
					func() {
						mutateLinuxRetainedSidecarHeader(t, fixture.pair.sidecar, fixture.pair.header, mutation)
					},
					func(sidecar *retainedRegular, prepared *preparedSlotTransition, offset uint64) *slotExecutionError {
						return sidecar.executePreconfirmedSidecarSlotTransition(prepared, offset, true)
					},
				)
			},
		},
		{
			name: "after tail sync before slot read", wantCode: linuxLivePairOS, wantLength: 2 * PageSize,
			run: func(t testing.TB, fixture linuxDeadWriterLivePairFixture, mutation linuxRetainedHeaderMutation) *linuxLivePairError {
				return fixture.pair.retryDeadWriterCleanupWith(
					defaultLinuxWriterTruncate,
					func(file *os.File) error {
						if err := file.Sync(); err != nil {
							return err
						}
						mutateLinuxRetainedSidecarHeader(t, fixture.pair.sidecar, fixture.pair.header, mutation)
						return nil
					},
				)
			},
		},
		{
			name: "preconfirmed clear before arm", wantCode: linuxLivePairTransition, wantLength: 2 * PageSize,
			run: func(t testing.TB, fixture linuxDeadWriterLivePairFixture, mutation linuxRetainedHeaderMutation) *linuxLivePairError {
				return fixture.pair.retryDeadWriterCleanupWithTransition(
					defaultLinuxWriterTruncate, defaultLinuxWriterSync, nil, nil,
					func(sidecar *retainedRegular, prepared *preparedSlotTransition, offset uint64) *slotExecutionError {
						mutateLinuxRetainedSidecarHeader(t, sidecar, fixture.pair.header, mutation)
						return sidecar.executePreconfirmedSidecarSlotTransition(prepared, offset, true)
					},
				)
			},
		},
	}
	for _, boundary := range boundaries {
		for _, mutation := range mutations {
			t.Run(boundary.name+"/"+mutation.name(), func(t *testing.T) {
				fixture := newLinuxDeadWriterLivePairFixture(t, 3)
				retainMissingLinuxWriter(t, fixture)
				err := boundary.run(t, fixture, mutation)
				dead := fixture.pair.sidecar.cleanupAuthority.writer
				raw, readErr := fixture.pair.sidecar.readSidecarSlotAfterHeader(fixture.pair.header, 0)
				if err == nil || err.code != boundary.wantCode ||
					retainedFileLength(t, fixture.pair.main) != boundary.wantLength ||
					fixture.pair.sidecar.cleanupAuthority.kind != linuxCleanupDeadWriter ||
					!dead.bootstrapValid || !dead.tailValid || readErr != nil || raw != encodeActiveSlot(fixture.writer) {
					t.Fatalf("boundary error %#v length %d authority %#v read %v slot %x",
						err, retainedFileLength(t, fixture.pair.main), fixture.pair.sidecar.cleanupAuthority, readErr, raw)
				}
				writeLinuxSidecarHeaderFixture(t, fixture.pair.sidecar, fixture.pair.header)
				if err := fixture.pair.retryDeadWriterCleanup(); err != nil {
					t.Fatal(err)
				}
				if raw, err := fixture.pair.sidecar.readSidecarSlot(fixture.pair.header, 0); err != nil || raw != ([sidecarSlotSize]byte{}) {
					t.Fatalf("restored cleanup slot = %x, %v", raw, err)
				}
			})
		}
	}
}

func TestLinuxLivePairRetainsTailAcrossTruncateAndSyncFailures(t *testing.T) {
	fixture := newLinuxDeadWriterLivePairFixture(t, 3)
	retainMissingLinuxWriter(t, fixture)
	syncCalled := false
	err := fixture.pair.retryDeadWriterCleanupWith(
		func(*os.File, uint64) error { return errors.New("injected truncate failure") },
		func(*os.File) error { syncCalled = true; return nil },
	)
	var linuxErr *linuxOSError
	if err == nil || err.code != linuxLivePairOS || !errors.As(err, &linuxErr) ||
		linuxErr.operation != "truncate unpublished main tail" || syncCalled {
		t.Fatalf("truncate failure = %#v syncCalled=%v", err, syncCalled)
	}
	if length := retainedFileLength(t, fixture.pair.main); length != 3*PageSize {
		t.Fatalf("length after truncate failure = %d", length)
	}
	dead := fixture.pair.sidecar.cleanupAuthority.writer
	wantTail := linuxUnpublishedMainTail{
		mainIdentity: fixture.pair.main.identity, databaseID: [16]byte{1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1},
		transactionID: 1, commitNonce: [16]byte{2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2},
		committedLength: 2 * PageSize, observedEndExclusive: 3 * PageSize,
	}
	if fixture.pair.sidecar.cleanupAuthority.kind != linuxCleanupDeadWriter || !dead.tailValid || dead.tail != wantTail {
		t.Fatalf("retained tail = %#v", fixture.pair.sidecar.cleanupAuthority)
	}
	err = fixture.pair.retryDeadWriterCleanupWith(
		func(file *os.File, length uint64) error { return file.Truncate(int64(length)) },
		func(*os.File) error { return errors.New("injected sync failure") },
	)
	linuxErr = nil
	if err == nil || !errors.As(err, &linuxErr) || linuxErr.operation != "synchronize main tail cleanup" {
		t.Fatalf("sync failure = %#v", err)
	}
	if length := retainedFileLength(t, fixture.pair.main); length != 2*PageSize ||
		fixture.pair.sidecar.cleanupAuthority.kind != linuxCleanupDeadWriter {
		t.Fatalf("post-sync-failure length=%d authority=%#v", length, fixture.pair.sidecar.cleanupAuthority)
	}
	truncateCalled := false
	err = fixture.pair.retryDeadWriterCleanupWith(
		func(*os.File, uint64) error { truncateCalled = true; return nil },
		func(file *os.File) error { return file.Sync() },
	)
	if err != nil || truncateCalled {
		t.Fatalf("retry = %#v truncateCalled=%v", err, truncateCalled)
	}
}

func TestLinuxLivePairExactLengthStillSyncsAndTailGrowthConflicts(t *testing.T) {
	exact := newLinuxDeadWriterLivePairFixture(t, 2)
	retainMissingLinuxWriter(t, exact)
	syncCount := 0
	err := exact.pair.retryDeadWriterCleanupWith(
		func(*os.File, uint64) error { t.Fatal("exact-length main attempted truncate"); return nil },
		func(file *os.File) error { syncCount++; return file.Sync() },
	)
	if err != nil || syncCount != 1 {
		t.Fatalf("exact-length cleanup = %#v syncCount=%d", err, syncCount)
	}

	grown := newLinuxDeadWriterLivePairFixture(t, 3)
	retainMissingLinuxWriter(t, grown)
	if err := grown.pair.retryDeadWriterCleanupWith(
		func(*os.File, uint64) error { return errors.New("record tail without truncating") },
		func(*os.File) error { return nil },
	); err == nil {
		t.Fatal("tail-recording truncate unexpectedly succeeded")
	}
	if err := grown.pair.main.file.Truncate(4 * PageSize); err != nil {
		t.Fatal(err)
	}
	err = grown.pair.retryDeadWriterCleanup()
	if err == nil || err.code != linuxLivePairTailLengthConflict ||
		err.target != 2*PageSize || err.observedEnd != 3*PageSize || err.actual != 4*PageSize ||
		grown.pair.sidecar.cleanupAuthority.kind != linuxCleanupDeadWriter {
		t.Fatalf("growth conflict = %#v authority=%#v", err, grown.pair.sidecar.cleanupAuthority)
	}
}

func TestLinuxLivePairTailRetryRejectsChangedGenerationTuple(t *testing.T) {
	tests := []struct {
		name string
		edit func(*Meta)
		want linuxLivePairErrorCode
	}{
		{"database", func(meta *Meta) { meta.DatabaseID[0] ^= 1 }, linuxLivePairMainGenerationChanged},
		{"transaction", func(meta *Meta) { meta.TxnID++ }, linuxLivePairMainGenerationChanged},
		{"commit nonce", func(meta *Meta) { meta.CommitNonce[0] ^= 1 }, linuxLivePairMainGenerationChanged},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newLinuxDeadWriterLivePairFixture(t, 3)
			retainMissingLinuxWriter(t, fixture)
			if err := fixture.pair.retryDeadWriterCleanupWith(
				func(*os.File, uint64) error { return errors.New("record tail") },
				func(*os.File) error { return nil },
			); err == nil {
				t.Fatal("tail-recording failure unexpectedly succeeded")
			}
			meta := emptyDirectMeta(1)
			test.edit(&meta)
			page := meta.EncodePage()
			if err := fixture.pair.main.writeAllAt(page[:], 0); err != nil {
				t.Fatal(err)
			}
			if err := fixture.pair.main.writeAllAt(page[:], PageSize); err != nil {
				t.Fatal(err)
			}
			err := fixture.pair.retryDeadWriterCleanup()
			if err == nil || err.code != test.want || retainedFileLength(t, fixture.pair.main) != 3*PageSize ||
				fixture.pair.sidecar.cleanupAuthority.kind != linuxCleanupDeadWriter {
				t.Fatalf("changed tuple retry = %#v authority=%#v", err, fixture.pair.sidecar.cleanupAuthority)
			}
		})
	}
}

func TestLinuxLivePairSourceChangeFailsBeforeTailMutation(t *testing.T) {
	fixture := newLinuxDeadWriterLivePairFixture(t, 3)
	retainMissingLinuxWriter(t, fixture)
	changed := fixture.writer
	changed.nonce[0] ^= 1
	changedImage := encodeActiveSlot(changed)
	offset, offsetErr := sidecarSlotOffset(fixture.pair.header, 0)
	if offsetErr != nil {
		t.Fatal(offsetErr)
	}
	if err := fixture.pair.sidecar.writeAllAt(changedImage[:], offset); err != nil {
		t.Fatal(err)
	}
	err := fixture.pair.retryDeadWriterCleanup()
	if err == nil || err.code != linuxLivePairWriterSourceChanged ||
		retainedFileLength(t, fixture.pair.main) != 3*PageSize ||
		fixture.pair.sidecar.cleanupAuthority.kind != linuxCleanupDeadWriter {
		t.Fatalf("source change = %#v authority=%#v", err, fixture.pair.sidecar.cleanupAuthority)
	}
}

func TestLinuxLivePairArmedRetryDropsAuthorityBeforePostClearPathError(t *testing.T) {
	fixture := newLinuxDeadWriterLivePairFixture(t, 2)
	retainMissingLinuxWriter(t, fixture)
	dead := fixture.pair.sidecar.cleanupAuthority.writer
	bootstrap, bootstrapErr := fixture.pair.main.readMainBootstrap(OpenWriter)
	if bootstrapErr != nil {
		t.Fatal(bootstrapErr)
	}
	dead.bootstrapValid = true
	dead.bootstrap = bootstrap
	prepared, transitionErr := prepareSlotClearProvenDead(
		fixture.pair.header, slotWriter, 0, &dead.raw, dead.active, dead.proof, linuxSlotHostLimits(),
	)
	if transitionErr != nil {
		t.Fatal(transitionErr)
	}
	armed, transitionErr := prepared.arm()
	if transitionErr != nil {
		t.Fatal(transitionErr)
	}
	fixture.pair.sidecar.cleanupAuthority = linuxSidecarCleanupAuthority{
		kind: linuxCleanupArmed, armed: armed, writer: dead,
	}
	var zero [sidecarSlotSize]byte
	offset, offsetErr := sidecarSlotOffset(fixture.pair.header, 0)
	if offsetErr != nil {
		t.Fatal(offsetErr)
	}
	if err := fixture.pair.sidecar.writeAllAt(zero[:], offset); err != nil {
		t.Fatal(err)
	}
	err := fixture.pair.retryDeadWriterCleanupWithPostClear(func() {
		if renameErr := os.Rename(fixture.mainPath, fixture.mainPath+".old"); renameErr != nil {
			t.Fatal(renameErr)
		}
		if writeErr := os.WriteFile(fixture.mainPath, []byte("replacement"), 0o600); writeErr != nil {
			t.Fatal(writeErr)
		}
	})
	if err == nil || err.code != linuxLivePairPostClearPath ||
		fixture.pair.sidecar.cleanupAuthority.kind != linuxCleanupNone {
		t.Fatalf("post-clear path failure = %#v authority=%#v", err, fixture.pair.sidecar.cleanupAuthority)
	}
}

func newLinuxReaderLivePairFixture(t *testing.T) linuxDeadWriterLivePairFixture {
	t.Helper()
	fixture := newLinuxDeadWriterLivePairFixture(t, 2)
	var zero [sidecarSlotSize]byte
	offset, err := sidecarSlotOffset(fixture.pair.header, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.pair.sidecar.writeAllAt(zero[:], offset); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func readLinuxReaderSlotFixture(
	t *testing.T,
	pair *retainedLiveFiles,
	index uint32,
) [sidecarSlotSize]byte {
	t.Helper()
	raw, err := pair.sidecar.readSidecarSlot(pair.header, index)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestLinuxReaderSlotClaimPinReleaseAndCleanup(t *testing.T) {
	fixture := newLinuxReaderLivePairFixture(t)
	inspection, err := fixture.pair.scanAndReap()
	if err != nil {
		t.Fatal(err)
	}
	if !inspection.lowestFreeSlotValid || inspection.lowestFreeSlot != 1 {
		t.Fatalf("lowest free reader = valid %t index %d", inspection.lowestFreeSlotValid, inspection.lowestFreeSlot)
	}

	nonce := [16]byte{0x44}
	owned, claimErr := fixture.pair.claimReaderSlotWith(func() ([16]byte, *linuxOSError) {
		return nonce, nil
	})
	if claimErr != nil {
		t.Fatal(claimErr)
	}
	if owned.header != fixture.pair.header || owned.index != 1 || owned.active.txnID != 0 ||
		owned.active.nonce != nonce {
		t.Fatalf("claimed reader = %#v", owned)
	}
	claimed := readLinuxReaderSlotFixture(t, fixture.pair, 1)
	stable, problem := decodeStableSlot(claimed[:], slotReader, linuxSlotHostLimits())
	if problem != 0 || !stable.active || stable.claim != owned.active {
		t.Fatalf("claimed source = stable %#v problem %#x", stable, problem)
	}

	pinned, pinErr := fixture.pair.pinReaderSlot(owned)
	if pinErr != nil {
		t.Fatal(pinErr)
	}
	if owned.active.txnID != pinned.Meta.TxnID || owned.active.txnID != 1 ||
		owned.active.nonce != nonce {
		t.Fatalf("pinned reader = bootstrap %#v owner %#v", pinned, owned)
	}
	if releaseErr := fixture.pair.releaseReaderRegistrationLock(owned, pinned); releaseErr != nil {
		t.Fatal(releaseErr)
	}
	if fixture.pair.sidecar.lock != 0 {
		t.Fatalf("released operation lock = %d", fixture.pair.sidecar.lock)
	}
	cleanup, cleanupErr := fixture.pair.retryReaderSlotCleanup(owned)
	if cleanupErr != nil {
		t.Fatal(cleanupErr)
	}
	if cleanup.mainPath != nil || cleanup.sidecarPath != nil {
		t.Fatalf("cleanup paths = %#v", cleanup)
	}
	if fixture.pair.sidecar.lock != linuxLockExclusive {
		t.Fatalf("cleanup operation lock = %d", fixture.pair.sidecar.lock)
	}
	if raw := readLinuxReaderSlotFixture(t, fixture.pair, 1); raw != ([sidecarSlotSize]byte{}) {
		t.Fatalf("reader slot after cleanup = %x", raw)
	}
}

func TestLinuxReaderCapacityFailsBeforeNonceWithoutFileMutation(t *testing.T) {
	fixture := newLinuxReaderLivePairFixture(t)
	active := currentLinuxActiveSlot(1, [16]byte{0x55})
	activeImage := encodeActiveSlot(active)
	offset, offsetErr := sidecarSlotOffset(fixture.pair.header, 1)
	if offsetErr != nil {
		t.Fatal(offsetErr)
	}
	if err := fixture.pair.sidecar.writeAllAt(activeImage[:], offset); err != nil {
		t.Fatal(err)
	}
	inspection, err := fixture.pair.scanAndReap()
	if err != nil {
		t.Fatal(err)
	}
	if inspection.lowestFreeSlotValid {
		t.Fatalf("full reader table reported slot %d free", inspection.lowestFreeSlot)
	}
	nonceCalls := 0
	_, claimErr := fixture.pair.claimReaderSlotWith(func() ([16]byte, *linuxOSError) {
		nonceCalls++
		return [16]byte{0x66}, nil
	})
	if claimErr == nil || claimErr.code != linuxReaderCapacityExhausted || nonceCalls != 0 {
		t.Fatalf("capacity claim = error %#v nonce calls %d", claimErr, nonceCalls)
	}
	if raw := readLinuxReaderSlotFixture(t, fixture.pair, 1); raw != activeImage {
		t.Fatalf("reader slot changed on capacity failure = %x", raw)
	}
	_, claimErr = fixture.pair.claimReaderSlotWith(func() ([16]byte, *linuxOSError) {
		nonceCalls++
		return [16]byte{0x77}, nil
	})
	if claimErr == nil || claimErr.code != linuxReaderScanRequired || nonceCalls != 0 {
		t.Fatalf("consumed scan claim = error %#v nonce calls %d", claimErr, nonceCalls)
	}
}

func TestLinuxReaderBindingFailsBeforeNonceWithoutSlotMutation(t *testing.T) {
	fixture := newLinuxReaderLivePairFixture(t)
	if _, err := fixture.pair.scanAndReap(); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(fixture.mainPath, fixture.mainPath+".old"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fixture.mainPath, []byte("replacement"), 0o600); err != nil {
		t.Fatal(err)
	}
	nonceCalls := 0
	_, claimErr := fixture.pair.claimReaderSlotWith(func() ([16]byte, *linuxOSError) {
		nonceCalls++
		return [16]byte{0x88}, nil
	})
	if claimErr == nil || claimErr.code != linuxReaderOS || nonceCalls != 0 {
		t.Fatalf("changed binding claim = error %#v nonce calls %d", claimErr, nonceCalls)
	}
	if raw := readLinuxReaderSlotFixture(t, fixture.pair, 1); raw != ([sidecarSlotSize]byte{}) {
		t.Fatalf("reader slot changed on binding failure = %x", raw)
	}
}

func TestLinuxReaderReleaseRechecksExactOwnedSource(t *testing.T) {
	fixture := newLinuxReaderLivePairFixture(t)
	if _, err := fixture.pair.scanAndReap(); err != nil {
		t.Fatal(err)
	}
	owned, err := fixture.pair.claimReaderSlotWith(func() ([16]byte, *linuxOSError) {
		return [16]byte{0x99}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	pinned, err := fixture.pair.pinReaderSlot(owned)
	if err != nil {
		t.Fatal(err)
	}
	other := owned.active
	other.nonce[0] ^= 1
	otherImage := encodeActiveSlot(other)
	offset, offsetErr := sidecarSlotOffset(fixture.pair.header, owned.index)
	if offsetErr != nil {
		t.Fatal(offsetErr)
	}
	if err := fixture.pair.sidecar.writeAllAt(otherImage[:], offset); err != nil {
		t.Fatal(err)
	}
	releaseErr := fixture.pair.releaseReaderRegistrationLock(owned, pinned)
	if releaseErr == nil || releaseErr.code != linuxReaderOwnerMismatch ||
		fixture.pair.sidecar.lock != linuxLockExclusive {
		t.Fatalf("changed source release = error %#v lock %d", releaseErr, fixture.pair.sidecar.lock)
	}
	ownedImage := encodeActiveSlot(owned.active)
	if err := fixture.pair.sidecar.writeAllAt(ownedImage[:], offset); err != nil {
		t.Fatal(err)
	}
	if _, cleanupErr := fixture.pair.retryReaderSlotCleanup(owned); cleanupErr != nil {
		t.Fatal(cleanupErr)
	}
}

func TestLinuxReaderCleanupRequiresExactAuthority(t *testing.T) {
	fixture := newLinuxReaderLivePairFixture(t)
	unowned := currentLinuxActiveSlot(1, [16]byte{0x9a})
	unownedImage := encodeActiveSlot(unowned)
	offset, offsetErr := sidecarSlotOffset(fixture.pair.header, 1)
	if offsetErr != nil {
		t.Fatal(offsetErr)
	}
	if err := fixture.pair.sidecar.writeAllAt(unownedImage[:], offset); err != nil {
		t.Fatal(err)
	}
	_, cleanupErr := fixture.pair.retryReaderSlotCleanup(nil)
	if cleanupErr == nil || cleanupErr.code != linuxReaderNoCleanupAuthority {
		t.Fatalf("unowned cleanup = %#v", cleanupErr)
	}
	if raw := readLinuxReaderSlotFixture(t, fixture.pair, 1); raw != unownedImage {
		t.Fatalf("unowned reader changed = %x", raw)
	}
}

func TestLinuxReaderReleaseCrossBindsOwnedAndPinnedTransaction(t *testing.T) {
	fixture := newLinuxReaderLivePairFixture(t)
	if _, err := fixture.pair.scanAndReap(); err != nil {
		t.Fatal(err)
	}
	owned, err := fixture.pair.claimReaderSlotWith(func() ([16]byte, *linuxOSError) {
		return [16]byte{0x9b}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	pinned, err := fixture.pair.pinReaderSlot(owned)
	if err != nil {
		t.Fatal(err)
	}
	owned.index = 0
	releaseErr := fixture.pair.releaseReaderRegistrationLock(owned, pinned)
	if releaseErr == nil || releaseErr.code != linuxReaderOwnerMismatch {
		t.Fatalf("writer-slot-shaped release = %#v", releaseErr)
	}
	owned.index = 1
	owned.active.txnID = 0
	releaseErr = fixture.pair.releaseReaderRegistrationLock(owned, pinned)
	if releaseErr == nil || releaseErr.code != linuxReaderOwnerMismatch ||
		fixture.pair.sidecar.lock != linuxLockExclusive {
		t.Fatalf("cross-bound release = error %#v lock %d", releaseErr, fixture.pair.sidecar.lock)
	}
	owned.active.txnID = pinned.Meta.TxnID
	if _, cleanupErr := fixture.pair.retryReaderSlotCleanup(owned); cleanupErr != nil {
		t.Fatal(cleanupErr)
	}
}

func TestLinuxReaderReleaseRechecksPinnedGeneration(t *testing.T) {
	fixture := newLinuxReaderLivePairFixture(t)
	if _, err := fixture.pair.scanAndReap(); err != nil {
		t.Fatal(err)
	}
	owned, err := fixture.pair.claimReaderSlotWith(func() ([16]byte, *linuxOSError) {
		return [16]byte{0xaa}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	pinned, err := fixture.pair.pinReaderSlot(owned)
	if err != nil {
		t.Fatal(err)
	}
	changed := emptyDirectMeta(2)
	page := changed.EncodePage()
	if err := fixture.pair.main.writeAllAt(page[:], 0); err != nil {
		t.Fatal(err)
	}
	if err := fixture.pair.main.writeAllAt(page[:], PageSize); err != nil {
		t.Fatal(err)
	}
	releaseErr := fixture.pair.releaseReaderRegistrationLock(owned, pinned)
	if releaseErr == nil || releaseErr.code != linuxReaderGenerationChanged ||
		fixture.pair.sidecar.lock != linuxLockExclusive {
		t.Fatalf("changed generation release = error %#v lock %d", releaseErr, fixture.pair.sidecar.lock)
	}
	if _, cleanupErr := fixture.pair.retryReaderSlotCleanup(owned); cleanupErr != nil {
		t.Fatal(cleanupErr)
	}
}

func TestLinuxReaderCleanupRetriesRetainedArmedProvenance(t *testing.T) {
	fixture := newLinuxReaderLivePairFixture(t)
	if _, err := fixture.pair.scanAndReap(); err != nil {
		t.Fatal(err)
	}
	owned, err := fixture.pair.claimReaderSlotWith(func() ([16]byte, *linuxOSError) {
		return [16]byte{0xbb}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	current := readLinuxReaderSlotFixture(t, fixture.pair, owned.index)
	prepared, transitionErr := prepareSlotClearOwned(
		owned.header, slotReader, owned.index, &current, owned.active, linuxSlotHostLimits(),
	)
	if transitionErr != nil {
		t.Fatal(transitionErr)
	}
	armed, transitionErr := prepared.arm()
	if transitionErr != nil {
		t.Fatal(transitionErr)
	}
	fixture.pair.sidecar.cleanupAuthority = linuxSidecarCleanupAuthority{
		kind: linuxCleanupArmed, armed: armed,
	}
	cleanup, cleanupErr := fixture.pair.retryReaderSlotCleanup(nil)
	if cleanupErr != nil {
		t.Fatal(cleanupErr)
	}
	if cleanup.mainPath != nil || cleanup.sidecarPath != nil {
		t.Fatalf("cleanup paths = %#v", cleanup)
	}
	if fixture.pair.sidecar.cleanupAuthority.kind != linuxCleanupNone {
		t.Fatalf("cleanup authority = %#v", fixture.pair.sidecar.cleanupAuthority)
	}
	if raw := readLinuxReaderSlotFixture(t, fixture.pair, owned.index); raw != ([sidecarSlotSize]byte{}) {
		t.Fatalf("reader slot after retained cleanup = %x", raw)
	}
}

func TestLinuxReaderCleanupReportsCanonicalReplacementAfterExactClear(t *testing.T) {
	fixture := newLinuxReaderLivePairFixture(t)
	if _, err := fixture.pair.scanAndReap(); err != nil {
		t.Fatal(err)
	}
	owned, err := fixture.pair.claimReaderSlotWith(func() ([16]byte, *linuxOSError) {
		return [16]byte{0xbc}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	pinned, err := fixture.pair.pinReaderSlot(owned)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.pair.releaseReaderRegistrationLock(owned, pinned); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(fixture.sidecarPath, fixture.sidecarPath+".old"); err != nil {
		t.Fatal(err)
	}
	cleanup, cleanupErr := fixture.pair.retryReaderSlotCleanup(owned)
	if cleanupErr != nil {
		t.Fatal(cleanupErr)
	}
	if cleanup.mainPath != nil || cleanup.sidecarPath == nil {
		t.Fatalf("cleanup paths = %#v", cleanup)
	}
	if fixture.pair.sidecar.cleanupAuthority.kind != linuxCleanupNone {
		t.Fatalf("cleanup authority = %#v", fixture.pair.sidecar.cleanupAuthority)
	}
	if raw := readLinuxReaderSlotFixture(t, fixture.pair, owned.index); raw != ([sidecarSlotSize]byte{}) {
		t.Fatalf("reader slot after replaced-path cleanup = %x", raw)
	}
}
