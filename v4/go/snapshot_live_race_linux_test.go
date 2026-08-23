// Live snapshot source races (Rust tests/snapshot_operations.rs
// live_snapshot_pins_its_generation_while_a_writer_advances and
// live_source_replacement_after_reader_claim_blocks_publication): the
// first pins the claimed generation while a writer commits a newer one,
// the second replaces the source path after the reader claim and proves
// the final check refuses publication. Both use the sidecar reader-slot
// claim as the handshake and require the Linux OFD byte-range locks.

//go:build linux

package iprangedb

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/snapshot"
	"github.com/firehol/iprange/v4/go/internal/writer"
	"golang.org/x/sys/unix"
)

// buildLargeLiveDirectSource constructs one live direct-ipv4 pair with
// count single-address ranges at even addresses through the bulk
// builder and the InitializeLive conversion (the Go bulk path for the
// Rust populated_large_direct live replacement workflow): a large
// source copy outlasts the controller polls of the race tests.
func buildLargeLiveDirectSource(t *testing.T, path string, count int) {
	t.Helper()
	buildLargeDirectSource(t, path, count)
	if _, err := InitializeLive(path, 2, nil); err != nil {
		t.Fatal("initialize live:", err)
	}
}

// snapshotTryExclusiveGate attempts the sidecar gate byte-range lock
// non-blocking (Rust try_exclusive_gate: F_OFD_SETLK F_WRLCK at offset
// 0, length 1). The gate is the same OFD region the live owners lock,
// so a false result with a nil error means the snapshot still holds the
// gate. An unexpected errno is returned for the caller to report: the
// controller runs off the test goroutine, so these helpers never call
// the Fatal family (Go requires FailNow on the test goroutine).
func snapshotTryExclusiveGate(file *os.File) (bool, error) {
	lock := unix.Flock_t{Type: unix.F_WRLCK, Whence: unix.SEEK_SET, Start: 0, Len: 1}
	for {
		err := unix.FcntlFlock(file.Fd(), unix.F_OFD_SETLK, &lock)
		if err == nil {
			return true, nil
		}
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if errors.Is(err, unix.EACCES) || errors.Is(err, unix.EAGAIN) {
			return false, nil
		}
		return false, err
	}
}

// snapshotUnlockGate releases the gate lock the controller acquired.
func snapshotUnlockGate(file *os.File) error {
	lock := unix.Flock_t{Type: unix.F_UNLCK, Whence: unix.SEEK_SET, Start: 0, Len: 1}
	for {
		err := unix.FcntlFlock(file.Fd(), unix.F_OFD_SETLK, &lock)
		if err == nil {
			return nil
		}
		if !errors.Is(err, unix.EINTR) {
			return err
		}
	}
}

// snapshotWaitForReader polls the sidecar slot table until any reader
// claim becomes visible or the snapshot side signals completion (Rust
// wait_for_reader: any non-zero byte from the first slot page onward
// means one claimed slot).
func snapshotWaitForReader(t *testing.T, sidecar string, done <-chan struct{}) bool {
	t.Helper()
	for {
		select {
		case <-done:
			return false
		default:
		}
		data, err := os.ReadFile(sidecar)
		if err != nil {
			t.Fatalf("read sidecar: %v", err)
		}
		for _, b := range data[format.PageSize:] {
			if b != 0 {
				return true
			}
		}
		runtime.Gosched()
	}
}

// TestSnapshotLivePinsItsGenerationWhileWriterAdvances ports the Rust
// live_snapshot_pins_its_generation_while_a_writer_advances test: the
// snapshot claims a reader slot of the old generation, a writer then
// commits a new generation, and the snapshot still publishes the pinned
// old generation while the live reader observes the new one.
func TestSnapshotLivePinsItsGenerationWhileWriterAdvances(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "live.iprdb")
	destination := filepath.Join(dir, "output.iprdb")
	buildLargeLiveDirectSource(t, source, 5_000)

	live, err := OpenLiveReader(source, nil)
	if err != nil {
		t.Fatal("live reader:", err)
	}
	before, err := live.Info()
	if err != nil {
		t.Fatal("live info:", err)
	}
	if _, err := live.Close(); err != nil {
		t.Fatal("live close:", err)
	}

	snapshotDone := make(chan struct{})
	resultCh := make(chan error, 1)
	go func() {
		_, err := SnapshotTo(source, SnapshotSourceLive, destination, PolicyFailIfExists, snapshotBudget(3), nil)
		resultCh <- err
		close(snapshotDone)
	}()
	if !snapshotWaitForReader(t, snapshotSidecar(source), snapshotDone) {
		t.Fatal("snapshot did not expose its reader claim")
	}

	writer, err := OpenLiveWriter(source, DefaultBudget(), nil)
	if err != nil {
		t.Fatal("writer:", err)
	}
	transaction, err := writer.BeginDirect()
	if err != nil {
		t.Fatal("begin:", err)
	}
	if changed, err := transaction.AssignV4(IPv4(1_000_000), IPv4(1_000_000), 999); err != nil || !changed {
		t.Fatalf("assign: changed=%v err=%v", changed, err)
	}
	if _, err := transaction.Commit(); err != nil {
		t.Fatal("commit:", err)
	}
	if _, err := writer.Close(); err != nil {
		t.Fatal("writer close:", err)
	}

	if err := <-resultCh; err != nil {
		t.Fatal("live snapshot:", err)
	}

	output := openPublished(t, destination)
	defer output.Close()
	outputInfo, err := output.Info()
	if err != nil {
		t.Fatal("output info:", err)
	}
	if outputInfo.TransactionID != before.TransactionID {
		t.Errorf("snapshot txn = %d, want pinned %d", outputInfo.TransactionID, before.TransactionID)
	}
	if _, found, err := output.LookupDirectV4(IPv4(1_000_000)); err != nil || found {
		t.Errorf("snapshot sees the newer range: found=%v err=%v", found, err)
	}

	live2, err := OpenLiveReader(source, nil)
	if err != nil {
		t.Fatal("live reader 2:", err)
	}
	defer live2.Close()
	after, err := live2.Info()
	if err != nil {
		t.Fatal("live info 2:", err)
	}
	if after.TransactionID <= before.TransactionID {
		t.Errorf("live txn = %d, want > %d", after.TransactionID, before.TransactionID)
	}
	if value, found, err := live2.LookupDirectV4(IPv4(1_000_000)); err != nil || !found || value != 999 {
		t.Errorf("live lookup = (%d, %v, %v), want (999, true, nil)", value, found, err)
	}
}

// TestSnapshotLiveSourceReselectsGenerationCommittedWhileOpen ports
// the Rust live-source claim window: the snapshot opens while the
// writer is idle, a writer commits a new generation before the
// snapshot takes the exclusive reader-table gate, and the gate-time
// re-bind must select, remap, claim, and publish the NEW generation
// (Rust prepare_claim re-stats the file under the gate with
// OpenMode::LiveReader). The cancellation checkpoint inside
// lockGateCancellable is the barrier between the mapping open and the
// gate trylock.
func TestSnapshotLiveSourceReselectsGenerationCommittedWhileOpen(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "live.iprdb")
	destination := filepath.Join(dir, "output.iprdb")
	buildLargeLiveDirectSource(t, source, 5_000)

	live, err := OpenLiveReader(source, nil)
	if err != nil {
		t.Fatal("live reader:", err)
	}
	before, err := live.Info()
	if err != nil {
		t.Fatal("live info:", err)
	}
	if _, err := live.Close(); err != nil {
		t.Fatal("live close:", err)
	}

	var mu sync.Mutex
	calls := 0
	atBarrier := make(chan struct{})
	release := make(chan struct{})
	check := func() error {
		mu.Lock()
		calls++
		block := calls == 2
		mu.Unlock()
		if block {
			close(atBarrier)
			<-release
		}
		return nil
	}
	resultCh := make(chan error, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		// The internal machine takes the checkpoint hook directly; the
		// public facade only exposes Cancel-based tokens.
		_, failure := snapshot.To(source, snapshot.SourceLive, destination, writer.PolicyFailIfExists, &snapshot.Budget{MaxHeapBytes: 16 << 20, MaxOutputPages: 100_000, MaxOpenFiles: 3}, check)
		if failure != nil {
			resultCh <- failure.Cause
			return
		}
		resultCh <- nil
	}()

	// The barrier must fire while the snapshot sits at the gate; a
	// snapshot that finishes first (resultCh send happens-before the
	// done close) means the reselect race never ran.
	select {
	case <-atBarrier:
	case <-done:
		t.Fatalf("snapshot finished before the gate barrier (cause %v); the reselect race never ran", <-resultCh)
	}
	writer, err := OpenLiveWriter(source, DefaultBudget(), nil)
	if err != nil {
		t.Fatal("writer:", err)
	}
	transaction, err := writer.BeginDirect()
	if err != nil {
		t.Fatal("begin:", err)
	}
	if changed, err := transaction.AssignV4(IPv4(1_000_000), IPv4(1_000_000), 999); err != nil || !changed {
		t.Fatalf("assign: changed=%v err=%v", changed, err)
	}
	if _, err := transaction.Commit(); err != nil {
		t.Fatal("commit:", err)
	}
	if _, err := writer.Close(); err != nil {
		t.Fatal("writer close:", err)
	}
	close(release)

	if err := <-resultCh; err != nil {
		t.Fatal("live snapshot:", err)
	}

	live2, err := OpenLiveReader(source, nil)
	if err != nil {
		t.Fatal("live reader 2:", err)
	}
	defer live2.Close()
	after, err := live2.Info()
	if err != nil {
		t.Fatal("live info 2:", err)
	}

	output := openPublished(t, destination)
	defer output.Close()
	outputInfo, err := output.Info()
	if err != nil {
		t.Fatal("output info:", err)
	}
	if outputInfo.TransactionID != after.TransactionID {
		t.Errorf("snapshot txn = %d, want the gate-time generation %d (open-time was %d)", outputInfo.TransactionID, after.TransactionID, before.TransactionID)
	}
	if value, found, err := output.LookupDirectV4(IPv4(1_000_000)); err != nil || !found || value != 999 {
		t.Errorf("snapshot lookup = (%d, %v, %v), want (999, true, nil)", value, found, err)
	}
}

// TestSnapshotLiveSourceReplacementAfterReaderClaimBlocksPublication
// ports the Rust live_source_replacement_after_reader_claim_blocks_
// publication test: a controller waits for the claimed reader slot, then
// takes the gate and renames the source path away; the snapshot final
// check must refuse publication (RecoveryCandidateChanged or
// LiveRecoveryCoordinationUnavailable) with no output and no private
// artifacts.
func TestSnapshotLiveSourceReplacementAfterReaderClaimBlocksPublication(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "live.iprdb")
	moved := filepath.Join(dir, "moved-live.iprdb")
	destination := filepath.Join(dir, "output.iprdb")
	buildLargeLiveDirectSource(t, source, 20_000)

	controllerDone := make(chan struct{})
	controllerSaw := make(chan bool, 1)
	go func() {
		defer close(controllerSaw)
		gate, err := os.OpenFile(snapshotSidecar(source), os.O_RDWR, 0)
		if err != nil {
			t.Errorf("controller open sidecar: %v", err)
			return
		}
		defer gate.Close()
		for {
			select {
			case <-controllerDone:
				return
			default:
			}
			data, err := os.ReadFile(snapshotSidecar(source))
			if err != nil {
				t.Errorf("controller read sidecar: %v", err)
				return
			}
			readerActive := false
			for _, b := range data[format.PageSize:] {
				if b != 0 {
					readerActive = true
					break
				}
			}
			if readerActive {
				acquired, err := snapshotTryExclusiveGate(gate)
				if err != nil {
					t.Errorf("controller gate lock: %v", err)
					return
				}
				if !acquired {
					runtime.Gosched()
					continue
				}
				// The slot is cleared and the gate released only after
				// the final check, which the held gate blocks; re-read
				// the slot bytes now to prove the snapshot is still
				// inside the copy and did not win the race (guard
				// against controller starvation past the whole build).
				data, err := os.ReadFile(snapshotSidecar(source))
				if err != nil {
					t.Errorf("controller re-read sidecar: %v", err)
					snapshotUnlockGate(gate)
					return
				}
				stillClaimed := false
				for _, b := range data[format.PageSize:] {
					if b != 0 {
						stillClaimed = true
						break
					}
				}
				if !stillClaimed {
					if unlockErr := snapshotUnlockGate(gate); unlockErr != nil {
						t.Errorf("controller unlock: %v", unlockErr)
					}
					controllerSaw <- false
					return
				}
				if err := os.Rename(source, moved); err != nil {
					t.Errorf("controller rename: %v", err)
					if unlockErr := snapshotUnlockGate(gate); unlockErr != nil {
						t.Errorf("controller unlock: %v", unlockErr)
					}
					return
				}
				if err := snapshotUnlockGate(gate); err != nil {
					t.Errorf("controller unlock: %v", err)
				}
				controllerSaw <- true
				return
			}
			runtime.Gosched()
		}
	}()

	_, err := SnapshotTo(source, SnapshotSourceLive, destination, PolicyFailIfExists, snapshotBudget(3), nil)
	close(controllerDone)
	if !<-controllerSaw {
		t.Fatal("controller missed the live claim")
	}
	code := failureCode(t, err)
	if code != ErrorRecoveryCandidateChanged && code != ErrorLiveRecoveryCoordinationUnavailable {
		t.Fatalf("cause code = %v, want recovery candidate changed or live coordination unavailable (err %v)", code, err)
	}
	if _, statErr := os.Lstat(destination); !os.IsNotExist(statErr) {
		t.Fatalf("blocked snapshot produced an output: %v", statErr)
	}
	assertNoSnapshotArtifacts(t, dir)
}
