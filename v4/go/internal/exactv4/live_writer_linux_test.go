//go:build linux

package exactv4

import (
	"context"
	"errors"
	"math"
	"os"
	"runtime"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func resizeLinuxLiveWriterSidecar(
	t testing.TB,
	database *linuxLiveReaderTestDatabase,
	capacity uint32,
) {
	t.Helper()
	file, err := os.OpenFile(database.sidecar, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	slots, ok := checkedAdd(uint64(capacity), 1)
	if !ok {
		_ = file.Close()
		t.Fatal("sidecar fixture slot count overflow")
	}
	slotBytes, ok := checkedMul(slots, uint64(sidecarSlotSize))
	if !ok {
		_ = file.Close()
		t.Fatal("sidecar fixture slot bytes overflow")
	}
	length, ok := checkedAdd(headerRegionSize, slotBytes)
	if !ok || length > math.MaxInt64 {
		_ = file.Close()
		t.Fatal("sidecar fixture length overflow")
	}
	if err := file.Truncate(int64(length)); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	database.header.capacity = capacity
	database.header.headerSeq++
	var block [PageSize]byte
	database.header.encodeInto(&block)
	for _, offset := range []int64{0, PageSize} {
		count, writeErr := file.WriteAt(block[:], offset)
		if writeErr != nil || count != len(block) {
			_ = file.Close()
			t.Fatalf("write resized sidecar header = %d, %v", count, writeErr)
		}
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func openLinuxLiveWriterSidecarCompetitor(
	t testing.TB,
	database *linuxLiveReaderTestDatabase,
) (*retainedDirectory, *retainedRegular) {
	t.Helper()
	directory, mainComponent, err := openRetainedParent(database.main)
	if err != nil {
		t.Fatal(err)
	}
	sidecarComponent, err := directory.sidecarComponent(mainComponent)
	if err != nil {
		_ = directory.file.Close()
		t.Fatal(err)
	}
	sidecar, err := directory.openRegular(sidecarComponent, true)
	if err != nil {
		_ = directory.file.Close()
		t.Fatal(err)
	}
	return directory, sidecar
}

func injectedLinuxLiveWriterOpenFailure() *linuxLiveWriterOpenCause {
	return &linuxLiveWriterOpenCause{
		code:   linuxLiveWriterOpenLease,
		source: &linuxWriterLeaseError{code: linuxWriterGenerationChanged},
	}
}

func requireLinuxLiveWriterOpenError(
	t testing.TB,
	err *linuxLiveWriterOpenError,
	wantCode linuxLiveWriterOpenErrorCode,
	wantCause linuxLiveWriterOpenCauseCode,
) {
	t.Helper()
	if err == nil || err.code != wantCode || err.cause == nil || err.cause.code != wantCause {
		t.Fatalf("live writer open error = %#v, want code %d cause %d", err, wantCode, wantCause)
	}
}

func linuxLiveWriterMeta(database *linuxLiveReaderTestDatabase, txnID uint64) Meta {
	meta := emptyDirectMeta(txnID)
	meta.DatabaseID = database.header.databaseID
	meta.AddressFamily = AddressFamilyIPv4
	meta.ValueKind = ValueKindDirect
	meta.PageCount = 3
	meta.RangeRoot = 2
	meta.RangeRecordCount = 2
	return meta
}

func replaceLinuxLiveWriterMetaPair(
	t testing.TB,
	database *linuxLiveReaderTestDatabase,
	meta Meta,
) {
	t.Helper()
	replaceLinuxLiveWriterMetaPages(t, database, meta, meta)
}

func replaceLinuxLiveWriterMetaPages(
	t testing.TB,
	database *linuxLiveReaderTestDatabase,
	left Meta,
	right Meta,
) {
	t.Helper()
	file, err := os.OpenFile(database.main, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	leftPage := left.EncodePage()
	rightPage := right.EncodePage()
	for _, write := range []struct {
		offset int64
		page   *[PageSize]byte
	}{{0, &leftPage}, {PageSize, &rightPage}} {
		written, writeErr := file.WriteAt(write.page[:], write.offset)
		if writeErr != nil || written != PageSize {
			t.Fatalf("replace meta at %d = %d, %v", write.offset, written, writeErr)
		}
	}
}

func replaceLinuxSidecarHeaderPair(
	t testing.TB,
	database *linuxLiveReaderTestDatabase,
	left [PageSize]byte,
	right [PageSize]byte,
) {
	t.Helper()
	file, err := os.OpenFile(database.sidecar, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	for _, write := range []struct {
		offset int64
		page   *[PageSize]byte
	}{{0, &left}, {PageSize, &right}} {
		count, writeErr := file.WriteAt(write.page[:], write.offset)
		if writeErr != nil || count != PageSize {
			t.Fatalf("replace sidecar header at %d = %d, %v", write.offset, count, writeErr)
		}
	}
}

func linuxLiveWriterTail(writer *linuxLiveWriter, observedPages uint64) linuxUnpublishedMainTail {
	return linuxUnpublishedMainTail{
		mainIdentity: writer.files.main.identity, databaseID: writer.bootstrap.Meta.DatabaseID,
		transactionID: writer.bootstrap.Meta.TxnID, commitNonce: writer.bootstrap.Meta.CommitNonce,
		committedLength:      writer.bootstrap.CommittedBytes,
		observedEndExclusive: observedPages * PageSize,
	}
}

type linuxDeadWriterClearFaultPhase uint8

const (
	linuxDeadWriterClearFaultState2 linuxDeadWriterClearFaultPhase = iota + 1
	linuxDeadWriterClearFaultBody
	linuxDeadWriterClearFaultState0
	linuxDeadWriterClearFaultReadback
)

func interruptedLinuxDeadWriterClear(
	t testing.TB,
	phase linuxDeadWriterClearFaultPhase,
	afterInterrupt func(),
) linuxDeadWriterCleanupAttempt {
	t.Helper()
	return func(files *retainedLiveFiles, postClear func()) *linuxLivePairError {
		pairErr := files.retryDeadWriterCleanupWithTransition(
			defaultLinuxWriterTruncate,
			defaultLinuxWriterSync,
			postClear,
			nil,
			func(
				sidecar *retainedRegular,
				prepared *preparedSlotTransition,
				offset uint64,
			) *slotExecutionError {
				dead := sidecar.cleanupAuthority.writer
				armed, transitionErr := prepared.arm()
				if transitionErr != nil {
					return &slotExecutionError{transition: transitionErr}
				}
				sidecar.cleanupAuthority = linuxSidecarCleanupAuthority{
					kind: linuxCleanupArmed, armed: armed, writer: dead,
				}
				state2, transitionErr := armed.state2Bytes()
				if transitionErr != nil {
					return &slotExecutionError{transition: transitionErr}
				}
				if err := sidecar.writeAllAt(state2[:], offset); err != nil {
					return &slotExecutionError{storage: err}
				}
				if phase == linuxDeadWriterClearFaultState2 {
					return &slotExecutionError{storage: errors.New("injected stale-writer state-2 interruption")}
				}
				body, transitionErr := armed.bodyBytes()
				if transitionErr != nil {
					return &slotExecutionError{transition: transitionErr}
				}
				if err := sidecar.writeAllAt(body[:], offset+4); err != nil {
					return &slotExecutionError{storage: err}
				}
				if phase == linuxDeadWriterClearFaultBody {
					return &slotExecutionError{storage: errors.New("injected stale-writer body interruption")}
				}
				state0, transitionErr := armed.publishStateBytes()
				if transitionErr != nil {
					return &slotExecutionError{transition: transitionErr}
				}
				if err := sidecar.writeAllAt(state0[:], offset); err != nil {
					return &slotExecutionError{storage: err}
				}
				if phase == linuxDeadWriterClearFaultState0 {
					return &slotExecutionError{storage: errors.New("injected stale-writer final-state interruption")}
				}
				return &slotExecutionError{storage: errors.New("injected stale-writer readback interruption")}
			},
		)
		if pairErr == nil {
			t.Fatal("injected stale-writer Clear unexpectedly succeeded")
		}
		if afterInterrupt != nil {
			afterInterrupt()
		}
		return pairErr
	}
}

func TestLinuxLiveWriterOpenClaimsSelectedTransactionAndClosesIdempotently(t *testing.T) {
	database := newLinuxLiveReaderTestDatabase(t, 4, false)
	writer, openErr := openLinuxLiveWriter(database.main)
	if openErr != nil {
		t.Fatal(openErr)
	}
	if writer.bootstrap.Meta.TxnID != 1 {
		t.Fatalf("selected transaction = %d", writer.bootstrap.Meta.TxnID)
	}
	info, err := os.Stat(database.main)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != 3*PageSize {
		t.Fatalf("tail cleanup size = %d", info.Size())
	}
	raw := database.slot(t, 0)
	stable, problem := decodeStableSlot(raw[:], slotWriter, linuxSlotHostLimits())
	if problem != 0 || !stable.active || stable.claim.txnID != 1 || stable.claim != writer.owned.active {
		t.Fatalf("writer lease = %#v problem %d", stable, problem)
	}
	if writer.files.sidecar.lock != 0 {
		t.Fatalf("successful open retained operation lock = %d", writer.files.sidecar.lock)
	}

	_, busyErr := openLinuxLiveWriter(database.main)
	requireLinuxLiveWriterOpenError(t, busyErr, linuxLiveWriterOpenFailed, linuxLiveWriterOpenLease)
	var leaseErr *linuxWriterLeaseError
	if !errors.As(busyErr, &leaseErr) || leaseErr.code != linuxWriterBusy || busyErr.cleanupOutcome != nil {
		t.Fatalf("second writer result = %#v", busyErr)
	}
	if outcome, closeErr := writer.close(); closeErr != nil || outcome == nil {
		t.Fatalf("first close = outcome %#v error %#v", outcome, closeErr)
	}
	if outcome, closeErr := writer.close(); closeErr != nil || outcome != nil {
		t.Fatalf("idempotent close = outcome %#v error %#v", outcome, closeErr)
	}
	if raw := database.slot(t, 0); raw != ([sidecarSlotSize]byte{}) {
		t.Fatalf("closed writer slot = %x", raw)
	}
}

func TestLinuxLiveWriterPostClaimFailuresClearExactLease(t *testing.T) {
	for _, failedStage := range []linuxLiveWriterOpenStage{
		linuxLiveWriterStageClaimPublished,
		linuxLiveWriterStageBeforeTailCleanup,
	} {
		t.Run(string(rune('0'+failedStage)), func(t *testing.T) {
			database := newLinuxLiveReaderTestDatabase(t, 3, false)
			_, openErr := openLinuxLiveWriterWithHook(
				context.Background(), database.main,
				func(stage linuxLiveWriterOpenStage, _ *retainedLiveFiles, _ *linuxOwnedWriterLease) *linuxLiveWriterOpenCause {
					if stage == failedStage {
						return injectedLinuxLiveWriterOpenFailure()
					}
					return nil
				},
			)
			requireLinuxLiveWriterOpenError(t, openErr, linuxLiveWriterOpenFailed, linuxLiveWriterOpenLease)
			if openErr.cleanupOutcome == nil || openErr.guard != nil || openErr.cleanup != nil {
				t.Fatalf("post-claim cleanup result = %#v", openErr)
			}
			if raw := database.slot(t, 0); raw != ([sidecarSlotSize]byte{}) {
				t.Fatalf("writer slot after failed stage %d = %x", failedStage, raw)
			}
		})
	}
}

func TestLinuxLiveWriterCancellationBeforeAndAfterClaim(t *testing.T) {
	database := newLinuxLiveReaderTestDatabase(t, 3, false)
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	_, openErr := openLinuxLiveWriterContext(cancelled, database.main)
	requireLinuxLiveWriterOpenError(t, openErr, linuxLiveWriterOpenFailed, linuxLiveWriterOpenCancelled)
	if openErr.cleanupOutcome != nil || database.slot(t, 0) != ([sidecarSlotSize]byte{}) {
		t.Fatalf("preclaim cancellation = %#v slot %x", openErr, database.slot(t, 0))
	}

	ctx, cancelAfterClaim := context.WithCancel(context.Background())
	_, openErr = openLinuxLiveWriterWithHook(
		ctx, database.main,
		func(stage linuxLiveWriterOpenStage, _ *retainedLiveFiles, _ *linuxOwnedWriterLease) *linuxLiveWriterOpenCause {
			if stage == linuxLiveWriterStageClaimPublished {
				cancelAfterClaim()
			}
			return nil
		},
	)
	requireLinuxLiveWriterOpenError(t, openErr, linuxLiveWriterOpenFailed, linuxLiveWriterOpenCancelled)
	if openErr.cleanupOutcome == nil || database.slot(t, 0) != ([sidecarSlotSize]byte{}) {
		t.Fatalf("postclaim cancellation = %#v slot %x", openErr, database.slot(t, 0))
	}
}

func TestLinuxLiveWriterFullReaderCapacityDoesNotBlockLease(t *testing.T) {
	database := newLinuxLiveReaderTestDatabase(t, 3, false)
	reader := currentLinuxActiveSlot(1, [16]byte{0x31})
	database.putSlot(t, 1, encodeActiveSlot(reader))
	writer, openErr := openLinuxLiveWriter(database.main)
	if openErr != nil {
		t.Fatal(openErr)
	}
	if raw := database.slot(t, 1); raw != encodeActiveSlot(reader) {
		t.Fatalf("writer changed active reader = %x", raw)
	}
	if _, closeErr := writer.close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if raw := database.slot(t, 1); raw != encodeActiveSlot(reader) {
		t.Fatalf("writer close changed active reader = %x", raw)
	}
}

func TestLinuxLiveWriterBusyAndDeadWriterCleanup(t *testing.T) {
	busy := newLinuxLiveReaderTestDatabase(t, 3, false)
	live := currentLinuxActiveSlot(1, [16]byte{0x41})
	busy.putSlot(t, 0, encodeActiveSlot(live))
	_, openErr := openLinuxLiveWriter(busy.main)
	requireLinuxLiveWriterOpenError(t, openErr, linuxLiveWriterOpenFailed, linuxLiveWriterOpenLease)
	var writerErr *linuxWriterLeaseError
	if !errors.As(openErr, &writerErr) || writerErr.code != linuxWriterBusy || busy.slot(t, 0) != encodeActiveSlot(live) {
		t.Fatalf("busy writer result = %#v slot %x", openErr, busy.slot(t, 0))
	}

	dead := newLinuxLiveReaderTestDatabase(t, 5, false)
	stale := activeSlot{
		txnID: 1, processID: uint64(math.MaxInt32), processStart: 1,
		nonce: [16]byte{0x42},
	}
	dead.putSlot(t, 0, encodeActiveSlot(stale))
	writer, openErr := openLinuxLiveWriter(dead.main)
	if openErr != nil {
		t.Fatal(openErr)
	}
	info, err := os.Stat(dead.main)
	if err != nil || info.Size() != 3*PageSize {
		t.Fatalf("dead writer tail cleanup = size %v error %v", info, err)
	}
	if dead.slot(t, 0) == ([sidecarSlotSize]byte{}) || dead.slot(t, 0) == encodeActiveSlot(stale) {
		t.Fatalf("dead writer was not replaced by new lease = %x", dead.slot(t, 0))
	}
	if _, closeErr := writer.close(); closeErr != nil {
		t.Fatal(closeErr)
	}
}

func TestLinuxLiveWriterDeadWriterCancellationCleansOwnedObligation(t *testing.T) {
	database := newLinuxLiveReaderTestDatabase(t, 4, false)
	stale := activeSlot{
		txnID: 1, processID: uint64(math.MaxInt32), processStart: 1,
		nonce: [16]byte{0x43},
	}
	database.putSlot(t, 0, encodeActiveSlot(stale))
	ctx, cancel := context.WithCancel(context.Background())
	_, openErr := openLinuxLiveWriterWithHook(
		ctx, database.main,
		func(stage linuxLiveWriterOpenStage, _ *retainedLiveFiles, _ *linuxOwnedWriterLease) *linuxLiveWriterOpenCause {
			if stage == linuxLiveWriterStageDeadWriterFound {
				cancel()
			}
			return nil
		},
	)
	requireLinuxLiveWriterOpenError(t, openErr, linuxLiveWriterOpenFailed, linuxLiveWriterOpenCancelled)
	info, err := os.Stat(database.main)
	if openErr.cleanupOutcome == nil || openErr.guard != nil || err != nil || info.Size() != 3*PageSize ||
		database.slot(t, 0) != ([sidecarSlotSize]byte{}) {
		t.Fatalf("cancelled dead-writer cleanup = error %#v info %v stat %v slot %x",
			openErr, info, err, database.slot(t, 0))
	}
}

func TestLinuxLiveOpenGuardsRetryInterruptedDeadWriterClear(t *testing.T) {
	openers := []string{"reader", "writer"}
	phases := []linuxDeadWriterClearFaultPhase{
		linuxDeadWriterClearFaultState2,
		linuxDeadWriterClearFaultBody,
		linuxDeadWriterClearFaultState0,
		linuxDeadWriterClearFaultReadback,
	}
	for _, opener := range openers {
		for _, phase := range phases {
			t.Run(opener+string(rune('0'+phase)), func(t *testing.T) {
				database := newLinuxLiveReaderTestDatabase(t, 4, false)
				stale := activeSlot{
					txnID: 1, processID: uint64(math.MaxInt32), processStart: 1,
					nonce: [16]byte{0x46},
				}
				database.putSlot(t, 0, encodeActiveSlot(stale))
				oldMain, oldSidecar := database.main+".armed-old", database.sidecar+".armed-old"
				replacePaths := phase == linuxDeadWriterClearFaultReadback
				var afterInterrupt func()
				if replacePaths {
					afterInterrupt = func() {
						if err := os.Rename(database.main, oldMain); err != nil {
							t.Fatal(err)
						}
						if err := os.Rename(database.sidecar, oldSidecar); err != nil {
							t.Fatal(err)
						}
						if err := os.WriteFile(database.main, []byte("replacement"), 0o600); err != nil {
							t.Fatal(err)
						}
						if err := os.WriteFile(database.sidecar, []byte("replacement"), 0o600); err != nil {
							t.Fatal(err)
						}
					}
				}
				attempt := interruptedLinuxDeadWriterClear(t, phase, afterInterrupt)
				var guard *linuxLiveCleanupGuard
				if opener == "reader" {
					_, openErr := openLinuxLiveReaderWithHookAndDeadCleanup[IPv4](
						context.Background(), database.main, nil, attempt,
					)
					requireLinuxLiveOpenError(t, openErr, linuxLiveReaderOpenCleanupRequired, linuxLiveReaderOpenPair)
					var origin, cleanup *linuxLivePairError
					if !errors.As(openErr.cause, &origin) || origin.code != linuxLivePairScan ||
						openErr.cleanup == nil || !errors.As(openErr.cleanup, &cleanup) ||
						cleanup.code != linuxLivePairTransition {
						t.Fatalf("reader phase %d error truth = %#v", phase, openErr)
					}
					guard = openErr.guard
				} else {
					_, openErr := openLinuxLiveWriterWithHookIOAndDeadCleanup(
						context.Background(), database.main, nil,
						defaultLinuxWriterTruncate, defaultLinuxWriterSync, attempt,
					)
					requireLinuxLiveWriterOpenError(t, openErr, linuxLiveWriterOpenCleanupRequired, linuxLiveWriterOpenPair)
					var origin, cleanup *linuxLivePairError
					if !errors.As(openErr.cause, &origin) || origin.code != linuxLivePairScan ||
						openErr.cleanup == nil || !errors.As(openErr.cleanup, &cleanup) ||
						cleanup.code != linuxLivePairTransition {
						t.Fatalf("writer phase %d error truth = %#v", phase, openErr)
					}
					guard = openErr.guard
				}
				if guard == nil || guard.files == nil || guard.files.sidecar.lock != linuxLockExclusive ||
					!linuxArmedDeadWriterCleanup(guard.files) || guard.files.writerBootstrap != nil {
					t.Fatalf("%s phase %d guard authority = %#v", opener, phase, guard)
				}
				info, statErr := guard.files.main.file.Stat()
				if statErr != nil || info.Size() != 3*PageSize {
					t.Fatalf("%s phase %d resolved tail = info %v error %v", opener, phase, info, statErr)
				}
				var outcome *linuxLiveClaimCleanupOutcome
				var cleanupErr *linuxLiveCleanupError
				if phase%2 == 0 {
					outcome, cleanupErr = guard.close()
				} else {
					outcome, cleanupErr = guard.retryCleanup()
				}
				if replacePaths {
					var pairErr *linuxLivePairError
					var osErr *linuxOSError
					if outcome != nil || cleanupErr == nil || !errors.As(cleanupErr, &pairErr) ||
						pairErr.code != linuxLivePairOS || !errors.As(cleanupErr, &osErr) ||
						osErr.code != linuxOSPathIdentityMismatch || !linuxArmedDeadWriterCleanup(guard.files) {
						t.Fatalf("%s phase %d replacement retry = outcome %#v error %#v guard %#v",
							opener, phase, outcome, cleanupErr, guard)
					}
					replacementMain, replacementSidecar := database.main+".replacement", database.sidecar+".replacement"
					if err := os.Rename(database.main, replacementMain); err != nil {
						t.Fatal(err)
					}
					if err := os.Rename(database.sidecar, replacementSidecar); err != nil {
						t.Fatal(err)
					}
					if err := os.Rename(oldMain, database.main); err != nil {
						t.Fatal(err)
					}
					if err := os.Rename(oldSidecar, database.sidecar); err != nil {
						t.Fatal(err)
					}
					outcome, cleanupErr = guard.retryCleanup()
					if cleanupErr != nil || outcome == nil || outcome.mainPath != nil || outcome.sidecarPath != nil ||
						database.slot(t, 0) != ([sidecarSlotSize]byte{}) {
						t.Fatalf("%s phase %d restored retry = outcome %#v error %#v slot %x",
							opener, phase, outcome, cleanupErr, database.slot(t, 0))
					}
				} else if cleanupErr != nil || outcome == nil || outcome.mainPath != nil ||
					outcome.sidecarPath != nil || database.slot(t, 0) != ([sidecarSlotSize]byte{}) {
					t.Fatalf("%s phase %d cleanup outcome = %#v slot %x",
						opener, phase, outcome, database.slot(t, 0))
				}
				if outcome, cleanupErr := guard.close(); cleanupErr != nil || outcome != nil {
					t.Fatalf("%s phase %d idempotent guard close = outcome %#v error %#v",
						opener, phase, outcome, cleanupErr)
				}
			})
		}
	}
}

func TestLinuxArmedDeadWriterClearRetryRevalidatesCompleteGeneration(t *testing.T) {
	type mutationCase struct {
		name    string
		mutate  func(testing.TB, *linuxLiveReaderTestDatabase)
		restore func(testing.TB, *linuxLiveReaderTestDatabase)
	}
	restoreMeta := func(t testing.TB, database *linuxLiveReaderTestDatabase) {
		replaceLinuxLiveWriterMetaPair(t, database, linuxLiveWriterMeta(database, 1))
	}
	mutations := []mutationCase{
		{
			name: "tail growth within frozen bound",
			mutate: func(t testing.TB, database *linuxLiveReaderTestDatabase) {
				if err := os.Truncate(database.main, 4*PageSize); err != nil {
					t.Fatal(err)
				}
			},
			restore: func(t testing.TB, database *linuxLiveReaderTestDatabase) {
				if err := os.Truncate(database.main, 3*PageSize); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "same transaction and nonce different roots",
			mutate: func(t testing.TB, database *linuxLiveReaderTestDatabase) {
				replacement := linuxLiveWriterMeta(database, 1)
				replacement.RangeRoot = 0
				replacement.RangeRecordCount = 0
				replaceLinuxLiveWriterMetaPair(t, database, replacement)
			},
			restore: restoreMeta,
		},
		{
			name: "same transaction different nonce",
			mutate: func(t testing.TB, database *linuxLiveReaderTestDatabase) {
				replacement := linuxLiveWriterMeta(database, 1)
				replacement.CommitNonce[0] ^= 0xff
				replaceLinuxLiveWriterMetaPair(t, database, replacement)
			},
			restore: restoreMeta,
		},
		{
			name: "later transaction",
			mutate: func(t testing.TB, database *linuxLiveReaderTestDatabase) {
				replaceLinuxLiveWriterMetaPair(t, database, linuxLiveWriterMeta(database, 2))
			},
			restore: restoreMeta,
		},
	}
	for _, opener := range []string{"reader", "writer"} {
		for _, phase := range []linuxDeadWriterClearFaultPhase{
			linuxDeadWriterClearFaultState2,
			linuxDeadWriterClearFaultBody,
			linuxDeadWriterClearFaultState0,
			linuxDeadWriterClearFaultReadback,
		} {
			for _, mutation := range mutations {
				t.Run(opener+string(rune('0'+phase))+"/"+mutation.name, func(t *testing.T) {
					database := newLinuxLiveReaderTestDatabase(t, 4, false)
					stale := activeSlot{
						txnID: 1, processID: uint64(math.MaxInt32), processStart: 1,
						nonce: [16]byte{0x48},
					}
					database.putSlot(t, 0, encodeActiveSlot(stale))
					attempt := interruptedLinuxDeadWriterClear(t, phase, nil)
					var guard *linuxLiveCleanupGuard
					if opener == "reader" {
						_, openErr := openLinuxLiveReaderWithHookAndDeadCleanup[IPv4](
							context.Background(), database.main, nil, attempt,
						)
						requireLinuxLiveOpenError(
							t, openErr, linuxLiveReaderOpenCleanupRequired, linuxLiveReaderOpenPair,
						)
						guard = openErr.guard
					} else {
						_, openErr := openLinuxLiveWriterWithHookIOAndDeadCleanup(
							context.Background(), database.main, nil,
							defaultLinuxWriterTruncate, defaultLinuxWriterSync, attempt,
						)
						requireLinuxLiveWriterOpenError(
							t, openErr, linuxLiveWriterOpenCleanupRequired, linuxLiveWriterOpenPair,
						)
						guard = openErr.guard
					}
					if guard == nil || guard.files == nil || !linuxArmedDeadWriterCleanup(guard.files) {
						t.Fatalf("initial armed guard = %#v", guard)
					}
					beforeAuthority := guard.files.sidecar.cleanupAuthority
					mutation.mutate(t, database)
					outcome, cleanupErr := guard.retryCleanup()
					var pairErr *linuxLivePairError
					if outcome != nil || cleanupErr == nil || !errors.As(cleanupErr, &pairErr) ||
						pairErr.code != linuxLivePairMainGenerationChanged || guard.files == nil ||
						guard.files.sidecar.cleanupAuthority.armed != beforeAuthority.armed ||
						guard.files.sidecar.cleanupAuthority.writer != beforeAuthority.writer ||
						!linuxArmedDeadWriterCleanup(guard.files) {
						t.Fatalf("mutated armed retry = outcome %#v error %#v pair %#v guard %#v",
							outcome, cleanupErr, pairErr, guard)
					}
					mutation.restore(t, database)
					outcome, cleanupErr = guard.close()
					if cleanupErr != nil || outcome == nil || database.slot(t, 0) != ([sidecarSlotSize]byte{}) {
						t.Fatalf("restored armed retry = outcome %#v error %#v slot %x",
							outcome, cleanupErr, database.slot(t, 0))
					}
				})
			}
		}
	}
}

func TestLinuxArmedDeadWriterClearRetryRejectsHeaderMutations(t *testing.T) {
	for _, phase := range []linuxDeadWriterClearFaultPhase{
		linuxDeadWriterClearFaultState2,
		linuxDeadWriterClearFaultBody,
		linuxDeadWriterClearFaultState0,
		linuxDeadWriterClearFaultReadback,
	} {
		for _, mutation := range []linuxRetainedHeaderMutation{
			linuxRetainedHeaderValidChanged,
			linuxRetainedHeaderMalformed,
			linuxRetainedHeaderTorn,
		} {
			t.Run(string(rune('0'+phase))+"/"+mutation.name(), func(t *testing.T) {
				database := newLinuxLiveReaderTestDatabase(t, 4, false)
				stale := activeSlot{
					txnID: 1, processID: uint64(math.MaxInt32), processStart: 1,
					nonce: [16]byte{0x49},
				}
				database.putSlot(t, 0, encodeActiveSlot(stale))
				_, openErr := openLinuxLiveWriterWithHookIOAndDeadCleanup(
					context.Background(), database.main, nil,
					defaultLinuxWriterTruncate, defaultLinuxWriterSync,
					interruptedLinuxDeadWriterClear(t, phase, nil),
				)
				requireLinuxLiveWriterOpenError(
					t, openErr, linuxLiveWriterOpenCleanupRequired, linuxLiveWriterOpenPair,
				)
				guard := openErr.guard
				if guard == nil || guard.files == nil || !linuxArmedDeadWriterCleanup(guard.files) {
					t.Fatalf("initial armed guard = %#v", guard)
				}
				beforeAuthority := guard.files.sidecar.cleanupAuthority
				mutateLinuxRetainedSidecarHeader(
					t, guard.files.sidecar, guard.files.header, mutation,
				)
				outcome, cleanupErr := guard.retryCleanup()
				var pairErr *linuxLivePairError
				var osErr *linuxOSError
				if outcome != nil || cleanupErr == nil || !errors.As(cleanupErr, &pairErr) ||
					pairErr.code != linuxLivePairOS || !errors.As(cleanupErr, &osErr) ||
					guard.files == nil || guard.files.sidecar.cleanupAuthority.armed != beforeAuthority.armed ||
					guard.files.sidecar.cleanupAuthority.writer != beforeAuthority.writer ||
					!linuxArmedDeadWriterCleanup(guard.files) {
					t.Fatalf("mutated header retry = outcome %#v error %#v pair %#v os %#v guard %#v",
						outcome, cleanupErr, pairErr, osErr, guard)
				}
				writeLinuxSidecarHeaderFixture(t, guard.files.sidecar, guard.files.header)
				outcome, cleanupErr = guard.close()
				if cleanupErr != nil || outcome == nil || database.slot(t, 0) != ([sidecarSlotSize]byte{}) {
					t.Fatalf("restored header retry = outcome %#v error %#v slot %x",
						outcome, cleanupErr, database.slot(t, 0))
				}
			})
		}
	}
}

func TestLinuxWriterClaimPrepublicationFailuresLeaveNoAuthority(t *testing.T) {
	for _, zero := range []bool{false, true} {
		database := newLinuxLiveReaderTestDatabase(t, 4, false)
		files, pairErr := openLockedRetainedLiveFiles(database.main)
		if pairErr != nil {
			t.Fatal(pairErr)
		}
		defer closeRetainedLiveFiles(files)
		if _, pairErr := files.scanAndReap(); pairErr != nil {
			t.Fatal(pairErr)
		}
		_, writerErr := files.claimWriterLeaseWith(func() ([16]byte, *linuxOSError) {
			if zero {
				return [16]byte{}, nil
			}
			return [16]byte{}, &linuxOSError{code: linuxOSRandomFailure}
		})
		if writerErr == nil || (!zero && writerErr.code != linuxWriterOS) ||
			(zero && writerErr.code != linuxWriterTransitionBeforeArm) {
			t.Fatalf("claim failure zero=%t = %#v", zero, writerErr)
		}
		if files.writerTail != nil || files.sidecar.cleanupAuthority.kind != linuxCleanupNone ||
			database.slot(t, 0) != ([sidecarSlotSize]byte{}) {
			t.Fatalf("prepublication residue zero=%t = tail %#v authority %#v slot %x",
				zero, files.writerTail, files.sidecar.cleanupAuthority, database.slot(t, 0))
		}
	}
}

func TestLinuxLiveWriterPreclaimGenerationChangeDoesNotPublish(t *testing.T) {
	database := newLinuxLiveReaderTestDatabase(t, 3, false)
	_, openErr := openLinuxLiveWriterWithHook(
		context.Background(), database.main,
		func(stage linuxLiveWriterOpenStage, _ *retainedLiveFiles, _ *linuxOwnedWriterLease) *linuxLiveWriterOpenCause {
			if stage == linuxLiveWriterStageScanComplete {
				replaceLinuxLiveWriterMetaPair(t, database, linuxLiveWriterMeta(database, 2))
			}
			return nil
		},
	)
	requireLinuxLiveWriterOpenError(t, openErr, linuxLiveWriterOpenFailed, linuxLiveWriterOpenLease)
	var writerErr *linuxWriterLeaseError
	if !errors.As(openErr, &writerErr) || writerErr.code != linuxWriterGenerationChanged ||
		openErr.cleanupOutcome != nil || database.slot(t, 0) != ([sidecarSlotSize]byte{}) {
		t.Fatalf("preclaim generation result = %#v slot %x", openErr, database.slot(t, 0))
	}
}

func TestLinuxLiveWriterReplacementAfterClaimReportsBothPaths(t *testing.T) {
	database := newLinuxLiveReaderTestDatabase(t, 3, false)
	oldMain, oldSidecar := database.main+".old", database.sidecar+".old"
	_, openErr := openLinuxLiveWriterWithHook(
		context.Background(), database.main,
		func(stage linuxLiveWriterOpenStage, _ *retainedLiveFiles, _ *linuxOwnedWriterLease) *linuxLiveWriterOpenCause {
			if stage != linuxLiveWriterStageClaimPublished {
				return nil
			}
			if err := os.Rename(database.main, oldMain); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(database.sidecar, oldSidecar); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(database.main, []byte("replacement"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(database.sidecar, []byte("replacement"), 0o600); err != nil {
				t.Fatal(err)
			}
			return injectedLinuxLiveWriterOpenFailure()
		},
	)
	requireLinuxLiveWriterOpenError(t, openErr, linuxLiveWriterOpenFailed, linuxLiveWriterOpenLease)
	if openErr.cleanupOutcome == nil || openErr.cleanupOutcome.mainPath == nil ||
		openErr.cleanupOutcome.sidecarPath == nil || openErr.guard != nil {
		t.Fatalf("replacement cleanup outcome = %#v", openErr)
	}
	if raw := database.slotAt(t, oldSidecar, 0); raw != ([sidecarSlotSize]byte{}) {
		t.Fatalf("retained replaced sidecar slot = %x", raw)
	}
}

func TestLinuxLiveWriterCleanupGuardRetriesNonConsumingly(t *testing.T) {
	database := newLinuxLiveReaderTestDatabase(t, 3, false)
	_, openErr := openLinuxLiveWriterWithHook(
		context.Background(), database.main,
		func(stage linuxLiveWriterOpenStage, files *retainedLiveFiles, owned *linuxOwnedWriterLease) *linuxLiveWriterOpenCause {
			if stage != linuxLiveWriterStageClaimPublished {
				return nil
			}
			foreign := owned.active
			foreign.nonce = [16]byte{0x55}
			offset, err := sidecarSlotOffset(owned.header, 0)
			if err != nil {
				t.Fatal(err)
			}
			raw := encodeActiveSlot(foreign)
			if err := files.sidecar.writeAllAt(raw[:], offset); err != nil {
				t.Fatal(err)
			}
			return injectedLinuxLiveWriterOpenFailure()
		},
	)
	requireLinuxLiveWriterOpenError(t, openErr, linuxLiveWriterOpenCleanupRequired, linuxLiveWriterOpenLease)
	guard := openErr.guard
	if guard == nil || guard.ownedWriter == nil || guard.files == nil {
		t.Fatalf("writer cleanup guard = %#v", guard)
	}
	if _, cleanupErr := guard.retryCleanup(); cleanupErr == nil || cleanupErr.code != linuxLiveCleanupWriter ||
		guard.files == nil || guard.ownedWriter == nil {
		t.Fatalf("non-consuming cleanup failure = %#v guard %#v", cleanupErr, guard)
	}
	offset, err := sidecarSlotOffset(guard.ownedWriter.header, 0)
	if err != nil {
		t.Fatal(err)
	}
	raw := encodeActiveSlot(guard.ownedWriter.active)
	if err := guard.files.sidecar.writeAllAt(raw[:], offset); err != nil {
		t.Fatal(err)
	}
	if outcome, cleanupErr := guard.close(); cleanupErr != nil || outcome == nil {
		t.Fatalf("guard close = outcome %#v error %#v", outcome, cleanupErr)
	}
	if outcome, cleanupErr := guard.close(); cleanupErr != nil || outcome != nil {
		t.Fatalf("idempotent guard close = outcome %#v error %#v", outcome, cleanupErr)
	}
	if raw := database.slot(t, 0); raw != ([sidecarSlotSize]byte{}) {
		t.Fatalf("guard-cleared slot = %x", raw)
	}
}

func TestLinuxLiveWriterTailFaultsReturnOriginalCauseAfterCleanup(t *testing.T) {
	for _, failSync := range []bool{false, true} {
		database := newLinuxLiveReaderTestDatabase(t, 4, false)
		_, openErr := openLinuxLiveWriterWithHookAndIO(
			context.Background(), database.main, nil,
			func(file *os.File, length uint64) error {
				if failSync {
					return file.Truncate(int64(length))
				}
				return errors.New("injected truncate failure")
			},
			func(file *os.File) error {
				if failSync {
					return errors.New("injected sync failure")
				}
				return file.Sync()
			},
		)
		requireLinuxLiveWriterOpenError(t, openErr, linuxLiveWriterOpenFailed, linuxLiveWriterOpenLease)
		var writerErr *linuxWriterLeaseError
		if !errors.As(openErr, &writerErr) || writerErr.code != linuxWriterOS ||
			openErr.cleanupOutcome == nil || openErr.guard != nil {
			t.Fatalf("tail fault failSync=%t = %#v", failSync, openErr)
		}
		info, err := os.Stat(database.main)
		if err != nil || info.Size() != 3*PageSize || database.slot(t, 0) != ([sidecarSlotSize]byte{}) {
			t.Fatalf("tail fault cleanup failSync=%t = info %v err %v slot %x",
				failSync, info, err, database.slot(t, 0))
		}
	}
}

func TestLinuxLiveWriterSameTxnDifferentNonceRetainsLeaseGuard(t *testing.T) {
	database := newLinuxLiveReaderTestDatabase(t, 4, false)
	replacement := linuxLiveWriterMeta(database, 1)
	replacement.CommitNonce[0] ^= 0xff
	_, openErr := openLinuxLiveWriterWithHook(
		context.Background(), database.main,
		func(stage linuxLiveWriterOpenStage, _ *retainedLiveFiles, _ *linuxOwnedWriterLease) *linuxLiveWriterOpenCause {
			if stage == linuxLiveWriterStageClaimPublished {
				replaceLinuxLiveWriterMetaPair(t, database, replacement)
			}
			return nil
		},
	)
	requireLinuxLiveWriterOpenError(t, openErr, linuxLiveWriterOpenCleanupRequired, linuxLiveWriterOpenLease)
	guard := openErr.guard
	var writerErr *linuxWriterLeaseError
	if !errors.As(openErr, &writerErr) || writerErr.code != linuxWriterGenerationChanged ||
		guard == nil || guard.files == nil || guard.files.writerTail == nil ||
		database.slot(t, 0) == ([sidecarSlotSize]byte{}) {
		t.Fatalf("same-txn replacement guard = %#v", openErr)
	}
	replaceLinuxLiveWriterMetaPair(t, database, linuxLiveWriterMeta(database, 1))
	if outcome, cleanupErr := guard.close(); cleanupErr != nil || outcome == nil {
		t.Fatalf("same-txn guard close = outcome %#v error %#v", outcome, cleanupErr)
	}
	info, err := os.Stat(database.main)
	if err != nil || info.Size() != 3*PageSize || database.slot(t, 0) != ([sidecarSlotSize]byte{}) {
		t.Fatalf("same-txn guard cleanup = info %v err %v slot %x",
			info, err, database.slot(t, 0))
	}
}

func TestLinuxLiveWriterFullGenerationMutationMatrixRetainsLeaseGuard(t *testing.T) {
	tests := []struct {
		name     string
		mutate   func(testing.TB, *linuxLiveReaderTestDatabase)
		wantTxn  uint64
		wantPage uint8
	}{
		{
			name: "same transaction and nonce with different roots and counts",
			mutate: func(t testing.TB, database *linuxLiveReaderTestDatabase) {
				replacement := linuxLiveWriterMeta(database, 1)
				replacement.RangeRoot = 0
				replacement.RangeRecordCount = 0
				replaceLinuxLiveWriterMetaPair(t, database, replacement)
			},
			wantTxn: 1, wantPage: 1,
		},
		{
			name: "selected meta page changes",
			mutate: func(t testing.TB, database *linuxLiveReaderTestDatabase) {
				replaceLinuxLiveWriterMetaPages(
					t, database, linuxLiveWriterMeta(database, 2), linuxLiveWriterMeta(database, 1),
				)
			},
			wantTxn: 2, wantPage: 0,
		},
		{
			name: "later transaction on same selected page",
			mutate: func(t testing.TB, database *linuxLiveReaderTestDatabase) {
				replaceLinuxLiveWriterMetaPair(t, database, linuxLiveWriterMeta(database, 3))
			},
			wantTxn: 3, wantPage: 1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			database := newLinuxLiveReaderTestDatabase(t, 4, false)
			_, openErr := openLinuxLiveWriterWithHook(
				context.Background(), database.main,
				func(stage linuxLiveWriterOpenStage, _ *retainedLiveFiles, _ *linuxOwnedWriterLease) *linuxLiveWriterOpenCause {
					if stage != linuxLiveWriterStageClaimPublished {
						return nil
					}
					test.mutate(t, database)
					data, err := os.ReadFile(database.main)
					if err != nil {
						t.Fatal(err)
					}
					bootstrap, err := Open(data, OpenWriter)
					if err != nil || bootstrap.Selection != SelectionProvenCurrent ||
						bootstrap.Meta.TxnID != test.wantTxn || bootstrap.SelectedMetaPage != test.wantPage {
						t.Fatalf("mutated bootstrap = %#v error %v, want txn/page %d/%d",
							bootstrap, err, test.wantTxn, test.wantPage)
					}
					return nil
				},
			)
			requireLinuxLiveWriterOpenError(
				t, openErr, linuxLiveWriterOpenCleanupRequired, linuxLiveWriterOpenLease,
			)
			guard := openErr.guard
			var writerErr *linuxWriterLeaseError
			if !errors.As(openErr, &writerErr) || writerErr.code != linuxWriterGenerationChanged ||
				guard == nil || guard.files == nil || guard.files.writerTail == nil ||
				database.slot(t, 0) == ([sidecarSlotSize]byte{}) {
				t.Fatalf("generation mutation guard = %#v", openErr)
			}
			replaceLinuxLiveWriterMetaPair(t, database, linuxLiveWriterMeta(database, 1))
			if outcome, cleanupErr := guard.close(); cleanupErr != nil || outcome == nil {
				t.Fatalf("generation mutation guard close = outcome %#v error %#v", outcome, cleanupErr)
			}
			info, err := os.Stat(database.main)
			if err != nil || info.Size() != 3*PageSize || database.slot(t, 0) != ([sidecarSlotSize]byte{}) {
				t.Fatalf("generation mutation cleanup = info %v error %v slot %x",
					info, err, database.slot(t, 0))
			}
		})
	}
}

func TestLinuxLiveWriterPostClaimGrowthIsCapturedBeforeExposureOrCleanup(t *testing.T) {
	for _, failAfterGrowth := range []bool{false, true} {
		database := newLinuxLiveReaderTestDatabase(t, 3, false)
		writer, openErr := openLinuxLiveWriterWithHook(
			context.Background(), database.main,
			func(stage linuxLiveWriterOpenStage, _ *retainedLiveFiles, _ *linuxOwnedWriterLease) *linuxLiveWriterOpenCause {
				if stage != linuxLiveWriterStageClaimPublished {
					return nil
				}
				if err := os.Truncate(database.main, 4*PageSize); err != nil {
					t.Fatal(err)
				}
				if failAfterGrowth {
					return injectedLinuxLiveWriterOpenFailure()
				}
				return nil
			},
		)
		info, err := os.Stat(database.main)
		if err != nil || info.Size() != 3*PageSize {
			t.Fatalf("captured growth fail=%t = info %v error %v", failAfterGrowth, info, err)
		}
		if failAfterGrowth {
			requireLinuxLiveWriterOpenError(t, openErr, linuxLiveWriterOpenFailed, linuxLiveWriterOpenLease)
			if writer != nil || openErr.cleanupOutcome == nil || openErr.guard != nil ||
				database.slot(t, 0) != ([sidecarSlotSize]byte{}) {
				t.Fatalf("post-growth failure = writer %#v error %#v slot %x",
					writer, openErr, database.slot(t, 0))
			}
			continue
		}
		if openErr != nil || writer == nil {
			t.Fatalf("post-growth exposure = writer %#v error %#v", writer, openErr)
		}
		if _, closeErr := writer.close(); closeErr != nil {
			t.Fatal(closeErr)
		}
	}
}

func TestLinuxLiveWriterRecordedTailEndIsFrozen(t *testing.T) {
	database := newLinuxLiveReaderTestDatabase(t, 4, false)
	_, openErr := openLinuxLiveWriterWithHook(
		context.Background(), database.main,
		func(stage linuxLiveWriterOpenStage, _ *retainedLiveFiles, _ *linuxOwnedWriterLease) *linuxLiveWriterOpenCause {
			if stage == linuxLiveWriterStageClaimPublished {
				if err := os.Truncate(database.main, 5*PageSize); err != nil {
					t.Fatal(err)
				}
			}
			return nil
		},
	)
	requireLinuxLiveWriterOpenError(t, openErr, linuxLiveWriterOpenCleanupRequired, linuxLiveWriterOpenLease)
	guard := openErr.guard
	var writerErr *linuxWriterLeaseError
	if !errors.As(openErr, &writerErr) || writerErr.code != linuxWriterTailLengthConflict ||
		guard == nil || guard.files == nil || guard.files.writerTail == nil ||
		guard.files.writerTail.observedEndExclusive != 4*PageSize ||
		database.slot(t, 0) == ([sidecarSlotSize]byte{}) {
		t.Fatalf("frozen tail conflict = %#v", openErr)
	}
	if _, cleanupErr := guard.retryCleanup(); cleanupErr == nil || cleanupErr.code != linuxLiveCleanupWriter ||
		guard.files == nil || guard.files.writerTail == nil {
		t.Fatalf("frozen tail retry = error %#v guard %#v", cleanupErr, guard)
	}
	if err := os.Truncate(database.main, 4*PageSize); err != nil {
		t.Fatal(err)
	}
	if outcome, cleanupErr := guard.close(); cleanupErr != nil || outcome == nil {
		t.Fatalf("frozen tail close = outcome %#v error %#v", outcome, cleanupErr)
	}
	info, err := os.Stat(database.main)
	if err != nil || info.Size() != 3*PageSize || database.slot(t, 0) != ([sidecarSlotSize]byte{}) {
		t.Fatalf("frozen tail cleanup = info %v err %v slot %x", info, err, database.slot(t, 0))
	}
}

func TestLinuxWriterArmedClaimCapturesPostPublicationGrowthBeforeClear(t *testing.T) {
	database := newLinuxLiveReaderTestDatabase(t, 3, false)
	files, pairErr := openLockedRetainedLiveFiles(database.main)
	if pairErr != nil {
		t.Fatal(pairErr)
	}
	defer closeRetainedLiveFiles(files)
	if _, pairErr := files.scanAndReap(); pairErr != nil {
		t.Fatal(pairErr)
	}
	bootstrap, inspection := files.lastBootstrap, files.lastInspection
	current, readErr := files.sidecar.readSidecarSlotAfterHeader(inspection.header, 0)
	if readErr != nil {
		t.Fatal(readErr)
	}
	active := currentLinuxActiveSlot(bootstrap.Meta.TxnID, [16]byte{0x71})
	prepared, transitionErr := prepareSlotClaim(
		inspection.header, slotWriter, 0, &current, active, linuxSlotHostLimits(),
	)
	if transitionErr != nil {
		t.Fatal(transitionErr)
	}
	armed, transitionErr := prepared.arm()
	if transitionErr != nil {
		t.Fatal(transitionErr)
	}
	files.sidecar.cleanupAuthority = linuxSidecarCleanupAuthority{
		kind: linuxCleanupArmed, armed: armed,
	}
	files.writerBootstrap = &bootstrap
	if err := os.Truncate(database.main, 4*PageSize); err != nil {
		t.Fatal(err)
	}
	outcome, writerErr := files.retryWriterLeaseCleanup(nil)
	if writerErr != nil || outcome.mainPath != nil || outcome.sidecarPath != nil {
		t.Fatalf("armed-claim cleanup = outcome %#v error %#v", outcome, writerErr)
	}
	info, statErr := os.Stat(database.main)
	if statErr != nil || info.Size() != 3*PageSize || files.writerTail != nil ||
		files.writerBootstrap != nil || files.sidecar.cleanupAuthority.kind != linuxCleanupNone ||
		database.slot(t, 0) != ([sidecarSlotSize]byte{}) {
		t.Fatalf("armed-claim growth cleanup = info %v err %v tail %#v generation %#v authority %#v slot %x",
			info, statErr, files.writerTail, files.writerBootstrap,
			files.sidecar.cleanupAuthority, database.slot(t, 0))
	}
}

func TestLinuxLiveWriterInterruptedClaimPhasesReturnExactRetryGuard(t *testing.T) {
	for completedWrites := 0; completedWrites <= 3; completedWrites++ {
		t.Run(string(rune('0'+completedWrites)), func(t *testing.T) {
			database := newLinuxLiveReaderTestDatabase(t, 4, false)
			files, pairErr := openLockedRetainedLiveFiles(database.main)
			if pairErr != nil {
				t.Fatal(pairErr)
			}
			if _, pairErr := files.scanAndReap(); pairErr != nil {
				closeRetainedLiveFiles(files)
				t.Fatal(pairErr)
			}
			bootstrap, inspection := files.lastBootstrap, files.lastInspection
			current, readErr := files.sidecar.readSidecarSlotAfterHeader(inspection.header, 0)
			if readErr != nil {
				closeRetainedLiveFiles(files)
				t.Fatal(readErr)
			}
			active := currentLinuxActiveSlot(bootstrap.Meta.TxnID, [16]byte{0x73})
			prepared, transitionErr := prepareSlotClaim(
				inspection.header, slotWriter, 0, &current, active, linuxSlotHostLimits(),
			)
			if transitionErr != nil {
				closeRetainedLiveFiles(files)
				t.Fatal(transitionErr)
			}
			armed, transitionErr := prepared.arm()
			if transitionErr != nil {
				closeRetainedLiveFiles(files)
				t.Fatal(transitionErr)
			}
			files.sidecar.cleanupAuthority = linuxSidecarCleanupAuthority{
				kind: linuxCleanupArmed, armed: armed,
			}
			files.writerBootstrap = &bootstrap
			tail := linuxUnpublishedMainTail{
				mainIdentity: files.main.identity, databaseID: bootstrap.Meta.DatabaseID,
				transactionID: bootstrap.Meta.TxnID, commitNonce: bootstrap.Meta.CommitNonce,
				committedLength:      bootstrap.CommittedBytes,
				observedEndExclusive: bootstrap.PhysicalBytes,
			}
			files.writerTail = &tail
			offset, offsetErr := sidecarSlotOffset(inspection.header, 0)
			if offsetErr != nil {
				closeRetainedLiveFiles(files)
				t.Fatal(offsetErr)
			}
			state2, transitionErr := armed.state2Bytes()
			if transitionErr != nil {
				t.Fatal(transitionErr)
			}
			body, transitionErr := armed.bodyBytes()
			if transitionErr != nil {
				t.Fatal(transitionErr)
			}
			state1, transitionErr := armed.publishStateBytes()
			if transitionErr != nil {
				t.Fatal(transitionErr)
			}
			if completedWrites >= 1 {
				if err := files.sidecar.writeAllAt(state2[:], offset); err != nil {
					t.Fatal(err)
				}
			}
			if completedWrites >= 2 {
				if err := files.sidecar.writeAllAt(body[:], offset+4); err != nil {
					t.Fatal(err)
				}
			}
			if completedWrites >= 3 {
				if err := files.sidecar.writeAllAt(state1[:], offset); err != nil {
					t.Fatal(err)
				}
			}
			openErr := failLinuxLiveWriterOpen(
				files, nil,
				&linuxLiveWriterOpenCause{
					code:   linuxLiveWriterOpenLease,
					source: &linuxWriterLeaseError{code: linuxWriterTransition},
				},
				&linuxLiveCleanupError{
					code:   linuxLiveCleanupWriter,
					source: &linuxWriterLeaseError{code: linuxWriterArmedCleanup},
				},
			)
			requireLinuxLiveWriterOpenError(t, openErr, linuxLiveWriterOpenCleanupRequired, linuxLiveWriterOpenLease)
			if openErr.guard == nil || openErr.guard.files == nil || openErr.guard.files.writerTail == nil {
				t.Fatalf("interrupted phase %d guard = %#v", completedWrites, openErr)
			}
			if outcome, cleanupErr := openErr.guard.close(); cleanupErr != nil || outcome == nil {
				t.Fatalf("interrupted phase %d close = outcome %#v error %#v",
					completedWrites, outcome, cleanupErr)
			}
			if outcome, cleanupErr := openErr.guard.close(); cleanupErr != nil || outcome != nil {
				t.Fatalf("interrupted phase %d idempotent close = outcome %#v error %#v",
					completedWrites, outcome, cleanupErr)
			}
			info, statErr := os.Stat(database.main)
			if statErr != nil || info.Size() != 3*PageSize || database.slot(t, 0) != ([sidecarSlotSize]byte{}) {
				t.Fatalf("interrupted phase %d cleanup = info %v err %v slot %x",
					completedWrites, info, statErr, database.slot(t, 0))
			}
		})
	}
}

func TestLinuxLiveWriterDeadWriterCancellationRetainsFactualCauseOnCleanupFailure(t *testing.T) {
	database := newLinuxLiveReaderTestDatabase(t, 4, false)
	stale := activeSlot{
		txnID: 1, processID: uint64(math.MaxInt32), processStart: 1,
		nonce: [16]byte{0x72},
	}
	database.putSlot(t, 0, encodeActiveSlot(stale))
	oldMain := database.main + ".cancelled-old"
	ctx, cancel := context.WithCancel(context.Background())
	_, openErr := openLinuxLiveWriterWithHook(
		ctx, database.main,
		func(stage linuxLiveWriterOpenStage, _ *retainedLiveFiles, _ *linuxOwnedWriterLease) *linuxLiveWriterOpenCause {
			if stage != linuxLiveWriterStageDeadWriterFound {
				return nil
			}
			cancel()
			if err := os.Rename(database.main, oldMain); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(database.main, []byte("replacement"), 0o600); err != nil {
				t.Fatal(err)
			}
			return nil
		},
	)
	requireLinuxLiveWriterOpenError(t, openErr, linuxLiveWriterOpenCleanupRequired, linuxLiveWriterOpenCancelled)
	if openErr.guard == nil || openErr.cleanup == nil || openErr.guard.files == nil ||
		openErr.guard.files.sidecar.cleanupAuthority.kind != linuxCleanupDeadWriter ||
		database.slot(t, 0) != encodeActiveSlot(stale) {
		t.Fatalf("cancelled dead-writer cleanup failure = %#v slot %x", openErr, database.slot(t, 0))
	}
	if err := os.Remove(database.main); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(oldMain, database.main); err != nil {
		t.Fatal(err)
	}
	if outcome, cleanupErr := openErr.guard.close(); cleanupErr != nil || outcome == nil {
		t.Fatalf("cancelled dead-writer guard close = outcome %#v error %#v", outcome, cleanupErr)
	}
	if raw := database.slot(t, 0); raw != ([sidecarSlotSize]byte{}) {
		t.Fatalf("cancelled dead-writer guard slot = %x", raw)
	}
}

func TestLinuxLiveWriterCloseIsCleanupOnlyAndRetriesTail(t *testing.T) {
	database := newLinuxLiveReaderTestDatabase(t, 3, false)
	writer, openErr := openLinuxLiveWriter(database.main)
	if openErr != nil {
		t.Fatal(openErr)
	}
	if err := os.Truncate(database.main, 4*PageSize); err != nil {
		t.Fatal(err)
	}
	tail := linuxLiveWriterTail(writer, 4)
	writer.files.writerTail = &tail
	active := database.slot(t, 0)
	_, closeErr := writer.closeWithIO(
		func(file *os.File, length uint64) error { return file.Truncate(int64(length)) },
		func(*os.File) error { return errors.New("injected close sync failure") },
	)
	if closeErr == nil || closeErr.code != linuxLiveWriterCloseCleanup ||
		writer.state != linuxLiveWriterStateCleanupOnly || writer.files == nil ||
		writer.files.writerTail == nil || database.slot(t, 0) != active {
		t.Fatalf("failed close = error %#v writer %#v slot %x", closeErr, writer, database.slot(t, 0))
	}
	if outcome, closeErr := writer.close(); closeErr != nil || outcome == nil {
		t.Fatalf("retried close = outcome %#v error %#v", outcome, closeErr)
	}
	if outcome, closeErr := writer.close(); closeErr != nil || outcome != nil {
		t.Fatalf("idempotent retried close = outcome %#v error %#v", outcome, closeErr)
	}
	if raw := database.slot(t, 0); raw != ([sidecarSlotSize]byte{}) {
		t.Fatalf("retried close slot = %x", raw)
	}
}

func TestLinuxLiveWriterFirstCloseAcceptsAlreadyZeroLeaseWithoutTail(t *testing.T) {
	database := newLinuxLiveReaderTestDatabase(t, 3, false)
	writer, openErr := openLinuxLiveWriter(database.main)
	if openErr != nil {
		t.Fatal(openErr)
	}
	database.putSlot(t, 0, [sidecarSlotSize]byte{})
	if outcome, closeErr := writer.close(); closeErr != nil || outcome == nil {
		t.Fatalf("already-zero first close = outcome %#v error %#v", outcome, closeErr)
	}
	if writer.state != linuxLiveWriterStateClosed || writer.files != nil || writer.owned != nil {
		t.Fatalf("already-zero writer state = %#v", writer)
	}
}

func TestLinuxLiveWriterExactZeroRejectsUnalignedRetainedMainLengthAndRetries(t *testing.T) {
	tests := []struct {
		name       string
		mutate     func(testing.TB, *linuxLiveReaderTestDatabase)
		wantActual uint64
		wantEnd    uint64
	}{
		{
			name: "one appended byte",
			mutate: func(t testing.TB, database *linuxLiveReaderTestDatabase) {
				file, err := os.OpenFile(database.main, os.O_WRONLY|os.O_APPEND, 0)
				if err != nil {
					t.Fatal(err)
				}
				if count, err := file.Write([]byte{0x7a}); err != nil || count != 1 {
					file.Close()
					t.Fatalf("append byte = %d, %v", count, err)
				}
				if err := file.Close(); err != nil {
					t.Fatal(err)
				}
			},
			wantActual: 3*PageSize + 1, wantEnd: 3*PageSize + 1,
		},
		{
			name: "one byte short",
			mutate: func(t testing.TB, database *linuxLiveReaderTestDatabase) {
				if err := os.Truncate(database.main, 3*PageSize-1); err != nil {
					t.Fatal(err)
				}
			},
			wantActual: 3*PageSize - 1, wantEnd: 3 * PageSize,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			database := newLinuxLiveReaderTestDatabase(t, 3, false)
			writer, openErr := openLinuxLiveWriter(database.main)
			if openErr != nil {
				t.Fatal(openErr)
			}
			test.mutate(t, database)
			database.putSlot(t, 0, [sidecarSlotSize]byte{})
			outcome, closeErr := writer.close()
			var writerErr *linuxWriterLeaseError
			if outcome != nil || closeErr == nil || !errors.As(closeErr, &writerErr) ||
				writerErr.code != linuxWriterTailLengthConflict || writerErr.target != 3*PageSize ||
				writerErr.observedEnd != test.wantEnd || writerErr.actual != test.wantActual ||
				writer.state != linuxLiveWriterStateCleanupOnly || writer.files == nil ||
				writer.files.writerBootstrap == nil || writer.files.writerTail != nil ||
				writer.files.sidecar.cleanupAuthority.kind != linuxCleanupNone {
				t.Fatalf("unaligned zero close = outcome %#v close %#v writer error %#v writer %#v",
					outcome, closeErr, writerErr, writer)
			}
			if err := os.Truncate(database.main, 3*PageSize); err != nil {
				t.Fatal(err)
			}
			outcome, closeErr = writer.close()
			if closeErr != nil || outcome == nil || writer.state != linuxLiveWriterStateClosed {
				t.Fatalf("restored exact length close = outcome %#v error %#v writer %#v",
					outcome, closeErr, writer)
			}
		})
	}
}

func TestLinuxLiveWriterOpenGuardRetriesExactZeroUnalignedMain(t *testing.T) {
	database := newLinuxLiveReaderTestDatabase(t, 3, false)
	_, openErr := openLinuxLiveWriterWithHook(
		context.Background(), database.main,
		func(stage linuxLiveWriterOpenStage, _ *retainedLiveFiles, _ *linuxOwnedWriterLease) *linuxLiveWriterOpenCause {
			if stage != linuxLiveWriterStageClaimPublished {
				return nil
			}
			file, err := os.OpenFile(database.main, os.O_WRONLY|os.O_APPEND, 0)
			if err != nil {
				t.Fatal(err)
			}
			if count, err := file.Write([]byte{0x7b}); err != nil || count != 1 {
				file.Close()
				t.Fatalf("append byte = %d, %v", count, err)
			}
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}
			database.putSlot(t, 0, [sidecarSlotSize]byte{})
			return injectedLinuxLiveWriterOpenFailure()
		},
	)
	requireLinuxLiveWriterOpenError(
		t, openErr, linuxLiveWriterOpenCleanupRequired, linuxLiveWriterOpenLease,
	)
	guard := openErr.guard
	var writerErr *linuxWriterLeaseError
	if openErr.cleanup == nil || openErr.cleanup.code != linuxLiveCleanupWriter ||
		!errors.As(openErr.cleanup, &writerErr) || writerErr.code != linuxWriterTailLengthConflict ||
		writerErr.target != 3*PageSize || writerErr.actual != 3*PageSize+1 ||
		guard == nil || guard.files == nil || guard.ownedWriter == nil ||
		guard.files.writerBootstrap == nil || guard.files.writerTail != nil ||
		guard.files.sidecar.cleanupAuthority.kind != linuxCleanupNone {
		t.Fatalf("unaligned exact-zero guard = %#v", openErr)
	}
	if err := os.Truncate(database.main, 3*PageSize); err != nil {
		t.Fatal(err)
	}
	if outcome, cleanupErr := guard.close(); cleanupErr != nil || outcome == nil {
		t.Fatalf("restored exact-zero guard = outcome %#v error %#v", outcome, cleanupErr)
	}
	if outcome, cleanupErr := guard.close(); cleanupErr != nil || outcome != nil {
		t.Fatalf("idempotent exact-zero guard = outcome %#v error %#v", outcome, cleanupErr)
	}
}

func TestLinuxExactZeroRetainedMainStatFailuresPreserveAuthority(t *testing.T) {
	tests := []struct {
		name      string
		stat      func(int, *unix.Stat_t) error
		wantCode  linuxOSErrorCode
		operation string
	}{
		{
			name: "fstat error",
			stat: func(int, *unix.Stat_t) error {
				return unix.EIO
			},
			wantCode: linuxOSIO, operation: "inspect retained writer main",
		},
		{
			name: "identity mismatch",
			stat: func(fd int, stat *unix.Stat_t) error {
				if err := unix.Fstat(fd, stat); err != nil {
					return err
				}
				stat.Ino++
				return nil
			},
			wantCode: linuxOSPathIdentityMismatch,
		},
		{
			name: "not regular",
			stat: func(fd int, stat *unix.Stat_t) error {
				if err := unix.Fstat(fd, stat); err != nil {
					return err
				}
				stat.Mode = stat.Mode&^unix.S_IFMT | unix.S_IFDIR
				return nil
			},
			wantCode: linuxOSNotRegular,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			database := newLinuxLiveReaderTestDatabase(t, 3, false)
			writer, openErr := openLinuxLiveWriter(database.main)
			if openErr != nil {
				t.Fatal(openErr)
			}
			database.putSlot(t, 0, [sidecarSlotSize]byte{})
			if err := writer.files.sidecar.acquireLock(linuxLockExclusive, false); err != nil {
				t.Fatal(err)
			}
			absent, writerErr := writer.files.ownedWriterLeaseAlreadyAbsentWithStat(writer.owned, test.stat)
			var osErr *linuxOSError
			if absent || writerErr == nil || writerErr.code != linuxWriterOS ||
				!errors.As(writerErr, &osErr) || osErr.code != test.wantCode ||
				osErr.operation != test.operation || writer.files.writerBootstrap == nil ||
				writer.files.sidecar.cleanupAuthority.kind != linuxCleanupNone {
				t.Fatalf("stat failure = absent %t writer %#v os %#v files %#v",
					absent, writerErr, osErr, writer.files)
			}
			if outcome, closeErr := writer.close(); closeErr != nil || outcome == nil {
				t.Fatalf("stat failure retry = outcome %#v error %#v", outcome, closeErr)
			}
		})
	}
}

func TestLinuxLiveWriterExactZeroPrecedesClaimedMainGenerationWithoutTail(t *testing.T) {
	for _, sameTxn := range []bool{false, true} {
		database := newLinuxLiveReaderTestDatabase(t, 3, false)
		writer, openErr := openLinuxLiveWriter(database.main)
		if openErr != nil {
			t.Fatal(openErr)
		}
		database.putSlot(t, 0, [sidecarSlotSize]byte{})
		replacement := linuxLiveWriterMeta(database, 2)
		if sameTxn {
			replacement = linuxLiveWriterMeta(database, 1)
			replacement.CommitNonce[0] ^= 0xff
		}
		replaceLinuxLiveWriterMetaPair(t, database, replacement)
		if outcome, closeErr := writer.close(); closeErr != nil || outcome == nil {
			t.Fatalf("zero precedence sameTxn=%t = outcome %#v error %#v", sameTxn, outcome, closeErr)
		}
		if writer.state != linuxLiveWriterStateClosed || writer.files != nil || writer.owned != nil {
			t.Fatalf("zero precedence sameTxn=%t writer = %#v", sameTxn, writer)
		}
	}
}

func TestLinuxLiveWriterExactZeroDoesNotBypassTailObligation(t *testing.T) {
	database := newLinuxLiveReaderTestDatabase(t, 3, false)
	writer, openErr := openLinuxLiveWriter(database.main)
	if openErr != nil {
		t.Fatal(openErr)
	}
	if err := os.Truncate(database.main, 4*PageSize); err != nil {
		t.Fatal(err)
	}
	tail := linuxLiveWriterTail(writer, 4)
	writer.files.writerTail = &tail
	database.putSlot(t, 0, [sidecarSlotSize]byte{})
	_, closeErr := writer.close()
	var writerErr *linuxWriterLeaseError
	info, statErr := os.Stat(database.main)
	if closeErr == nil || !errors.As(closeErr, &writerErr) || writerErr.code != linuxWriterOwnerMismatch ||
		statErr != nil || info.Size() != 4*PageSize || writer.state != linuxLiveWriterStateCleanupOnly ||
		writer.files == nil || writer.files.writerTail == nil {
		t.Fatalf("zero with tail = close %#v writer error %#v info %v stat %v writer %#v",
			closeErr, writerErr, info, statErr, writer)
	}
	database.putSlot(t, 0, encodeActiveSlot(writer.owned.active))
	if outcome, closeErr := writer.close(); closeErr != nil || outcome == nil {
		t.Fatalf("restored tail authority = outcome %#v error %#v", outcome, closeErr)
	}
	info, statErr = os.Stat(database.main)
	if statErr != nil || info.Size() != 3*PageSize || database.slot(t, 0) != ([sidecarSlotSize]byte{}) {
		t.Fatalf("restored tail cleanup = info %v stat %v slot %x", info, statErr, database.slot(t, 0))
	}
}

func TestLinuxLiveWriterCloseReselectsSidecarHeaderBeforeTailOrClear(t *testing.T) {
	for _, malformed := range []bool{false, true} {
		database := newLinuxLiveReaderTestDatabase(t, 3, false)
		writer, openErr := openLinuxLiveWriter(database.main)
		if openErr != nil {
			t.Fatal(openErr)
		}
		if err := os.Truncate(database.main, 4*PageSize); err != nil {
			t.Fatal(err)
		}
		tail := linuxLiveWriterTail(writer, 4)
		writer.files.writerTail = &tail
		active := database.slot(t, 0)
		var original [PageSize]byte
		database.header.encodeInto(&original)
		left, right := original, original
		if malformed {
			left[0] ^= 0xff
			right[0] ^= 0xff
		} else {
			changed := database.header
			changed.headerSeq++
			changed.encodeInto(&left)
			right = left
		}
		replaceLinuxSidecarHeaderPair(t, database, left, right)
		_, closeErr := writer.close()
		var osErr *linuxOSError
		wantCode := linuxOSSidecarHeaderChanged
		if malformed {
			wantCode = linuxOSSidecar
		}
		info, statErr := os.Stat(database.main)
		if closeErr == nil || closeErr.code != linuxLiveWriterCloseCleanup ||
			!errors.As(closeErr, &osErr) || osErr.code != wantCode || statErr != nil ||
			info.Size() != 4*PageSize || database.slot(t, 0) != active ||
			writer.state != linuxLiveWriterStateCleanupOnly || writer.files == nil ||
			writer.files.writerTail == nil {
			t.Fatalf("header reselect malformed=%t = close %#v os %#v info %v stat %v slot %x writer %#v",
				malformed, closeErr, osErr, info, statErr, database.slot(t, 0), writer)
		}
		replaceLinuxSidecarHeaderPair(t, database, original, original)
		if outcome, closeErr := writer.close(); closeErr != nil || outcome == nil {
			t.Fatalf("header restore malformed=%t = outcome %#v error %#v", malformed, outcome, closeErr)
		}
		info, statErr = os.Stat(database.main)
		if statErr != nil || info.Size() != 3*PageSize || database.slot(t, 0) != ([sidecarSlotSize]byte{}) {
			t.Fatalf("header restore cleanup malformed=%t = info %v stat %v slot %x",
				malformed, info, statErr, database.slot(t, 0))
		}
	}
}

func TestLinuxLiveWriterCreatorPIDPrecedesCleanupAndNoFinalizerMutates(t *testing.T) {
	database := newLinuxLiveReaderTestDatabase(t, 3, false)
	writer, openErr := openLinuxLiveWriter(database.main)
	if openErr != nil {
		t.Fatal(openErr)
	}
	active := database.slot(t, 0)
	writer.creatorPID++
	if _, closeErr := writer.close(); closeErr == nil || closeErr.code != linuxLiveWriterCloseForkedHandle ||
		writer.state != linuxLiveWriterStateOpen || database.slot(t, 0) != active {
		t.Fatalf("forked close = error %#v state %d slot %x", closeErr, writer.state, database.slot(t, 0))
	}
	writer.creatorPID = os.Getpid()
	writer = nil
	runtime.GC()
	if raw := database.slot(t, 0); raw != active {
		t.Fatalf("dropped writer mutated lease = %x", raw)
	}
}

func TestLinuxLiveWriterGuardCreatorPIDPrecedesTailAndSlotMutation(t *testing.T) {
	database := newLinuxLiveReaderTestDatabase(t, 4, false)
	replacement := linuxLiveWriterMeta(database, 1)
	replacement.CommitNonce[0] ^= 0xff
	_, openErr := openLinuxLiveWriterWithHook(
		context.Background(), database.main,
		func(stage linuxLiveWriterOpenStage, _ *retainedLiveFiles, _ *linuxOwnedWriterLease) *linuxLiveWriterOpenCause {
			if stage == linuxLiveWriterStageClaimPublished {
				replaceLinuxLiveWriterMetaPair(t, database, replacement)
			}
			return nil
		},
	)
	requireLinuxLiveWriterOpenError(t, openErr, linuxLiveWriterOpenCleanupRequired, linuxLiveWriterOpenLease)
	guard := openErr.guard
	active := database.slot(t, 0)
	guard.creatorPID++
	if _, cleanupErr := guard.retryCleanup(); cleanupErr == nil || cleanupErr.code != linuxLiveCleanupForkedHandle {
		t.Fatalf("forked guard cleanup = %#v", cleanupErr)
	}
	info, err := os.Stat(database.main)
	if err != nil || info.Size() != 4*PageSize || database.slot(t, 0) != active ||
		guard.files == nil || guard.ownedWriter == nil {
		t.Fatalf("forked guard mutation = info %v err %v slot %x guard %#v",
			info, err, database.slot(t, 0), guard)
	}
}

func TestLinuxLiveWriterReaderBarrierContentionAndCancellation(t *testing.T) {
	database := newLinuxLiveReaderTestDatabase(t, 3, false)
	writer, openErr := openLinuxLiveWriter(database.main)
	if openErr != nil {
		t.Fatal(openErr)
	}
	directory, blocker := openLinuxLiveWriterSidecarCompetitor(t, database)
	defer directory.file.Close()
	defer blocker.file.Close()
	if err := blocker.acquireLock(linuxLockExclusive, false); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, barrierErr := writer.acquireReaderThresholdBarrierContext(ctx)
	var osErr *linuxOSError
	if barrierErr == nil || barrierErr.code != linuxLiveWriterBarrierCancelled ||
		!errors.As(barrierErr, &osErr) || osErr.code != linuxOSCancelled ||
		writer.barrierHeld || writer.files.sidecar.lock != 0 {
		t.Fatalf("contended barrier = error %#v os %#v writer %#v", barrierErr, osErr, writer)
	}
	if err := blocker.releaseLock(); err != nil {
		t.Fatal(err)
	}
	barrier, barrierErr := writer.acquireReaderThresholdBarrierContext(context.Background())
	protection, protectionErr := barrier.readerProtection()
	if barrierErr != nil || protectionErr != nil ||
		protection.selectedTxnID != writer.bootstrap.Meta.TxnID || !writer.barrierHeld {
		t.Fatalf("barrier after contention = %#v/%#v writer %#v", barrier, barrierErr, writer)
	}
	if barrierErr = barrier.abort(); barrierErr != nil {
		t.Fatal(barrierErr)
	}
	if _, closeErr := writer.close(); closeErr != nil {
		t.Fatal(closeErr)
	}
}

func TestLinuxLiveWriterReaderBarrierPostLockCancellationRetainsAuthority(t *testing.T) {
	database := newLinuxLiveReaderTestDatabase(t, 3, false)
	resizeLinuxLiveWriterSidecar(t, database, 1)
	writer, openErr := openLinuxLiveWriter(database.main)
	if openErr != nil {
		t.Fatal(openErr)
	}
	reader := currentLinuxActiveSlot(1, [16]byte{0x71})
	database.putSlot(t, 1, encodeActiveSlot(reader))
	ctx, cancel := context.WithCancel(context.Background())
	barrier, barrierErr := writer.acquireReaderThresholdBarrierWithObserverContext(
		ctx,
		func(active activeSlot) posixProcessObservation {
			cancel()
			return posixProcessObservation{
				kind: posixProcessExists, currentStart: active.processStart,
			}
		},
	)
	var scanErr *linuxSidecarScanError
	if barrierErr == nil || barrierErr.code != linuxLiveWriterBarrierCancelled ||
		!errors.As(barrierErr, &scanErr) || scanErr.code != linuxSidecarScanCancelled ||
		!writer.barrierHeld || writer.files.sidecar.lock != linuxLockExclusive ||
		database.slot(t, 1) != encodeActiveSlot(reader) {
		t.Fatalf("post-lock cancellation = error %#v scan %#v writer %#v",
			barrierErr, scanErr, writer)
	}
	if abortErr := barrier.abort(); abortErr != nil {
		t.Fatal(abortErr)
	}
	database.putSlot(t, 1, [sidecarSlotSize]byte{})
	if _, closeErr := writer.close(); closeErr != nil {
		t.Fatal(closeErr)
	}
}

func TestLinuxLiveWriterReaderBarrierSerializesConcurrentOperations(t *testing.T) {
	database := newLinuxLiveReaderTestDatabase(t, 3, false)
	writer, openErr := openLinuxLiveWriter(database.main)
	if openErr != nil {
		t.Fatal(openErr)
	}
	directory, blocker := openLinuxLiveWriterSidecarCompetitor(t, database)
	defer directory.file.Close()
	defer blocker.file.Close()
	if err := blocker.acquireLock(linuxLockExclusive, false); err != nil {
		t.Fatal(err)
	}

	type acquireResult struct {
		barrier linuxLiveWriterReaderBarrier
		err     *linuxLiveWriterBarrierError
	}
	first := make(chan acquireResult, 1)
	go func() {
		barrier, barrierErr := writer.acquireReaderThresholdBarrierContext(context.Background())
		first <- acquireResult{barrier: barrier, err: barrierErr}
	}()
	deadline := time.Now().Add(time.Second)
	for {
		if !writer.operation.TryLock() {
			break
		}
		writer.operation.Unlock()
		if time.Now().After(deadline) {
			_ = blocker.releaseLock()
			t.Fatal("first barrier operation did not acquire the handle mutex")
		}
		runtime.Gosched()
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	second := make(chan acquireResult, 1)
	go func() {
		barrier, barrierErr := writer.acquireReaderThresholdBarrierContext(cancelled)
		second <- acquireResult{barrier: barrier, err: barrierErr}
	}()
	select {
	case result := <-second:
		_ = blocker.releaseLock()
		t.Fatalf("second barrier bypassed handle serialization: %#v", result)
	case <-time.After(20 * time.Millisecond):
	}
	if err := blocker.releaseLock(); err != nil {
		t.Fatal(err)
	}

	firstResult := <-first
	firstProtection, protectionErr := firstResult.barrier.readerProtection()
	if firstResult.err != nil || protectionErr != nil || firstProtection.selectedTxnID != 1 {
		t.Fatalf("first serialized barrier = %#v", firstResult)
	}
	secondResult := <-second
	if secondResult.err == nil || secondResult.err.code != linuxLiveWriterBarrierState ||
		!writer.barrierHeld || writer.files.sidecar.lock != linuxLockExclusive {
		t.Fatalf("second serialized barrier = %#v writer %#v", secondResult, writer)
	}
	if barrierErr := firstResult.barrier.abort(); barrierErr != nil {
		t.Fatal(barrierErr)
	}
	if _, closeErr := writer.close(); closeErr != nil {
		t.Fatal(closeErr)
	}
}

func TestLinuxLiveWriterReaderBarrierValidatesBeforeReaping(t *testing.T) {
	database := newLinuxLiveReaderTestDatabase(t, 3, false)
	resizeLinuxLiveWriterSidecar(t, database, 2)
	writer, openErr := openLinuxLiveWriter(database.main)
	if openErr != nil {
		t.Fatal(openErr)
	}
	dead := activeSlot{
		txnID: 1, processID: uint64(math.MaxInt32), processStart: 1,
		nonce: [16]byte{0x81},
	}
	database.putSlot(t, 1, encodeActiveSlot(dead))
	malformed := [sidecarSlotSize]byte{2}
	database.putSlot(t, 2, malformed)
	barrier, barrierErr := writer.acquireReaderThresholdBarrierContext(context.Background())
	var pairErr *linuxLivePairError
	var readyErr *readySidecarError
	if barrierErr == nil || barrierErr.code != linuxLiveWriterBarrierPair ||
		!errors.As(barrierErr, &pairErr) || pairErr.code != linuxLivePairScan ||
		!errors.As(barrierErr, &readyErr) || readyErr.code != readySidecarErrSlot ||
		database.slot(t, 1) != encodeActiveSlot(dead) || database.slot(t, 2) != malformed ||
		!writer.barrierHeld || writer.files.sidecar.lock != linuxLockExclusive {
		t.Fatalf("late malformed slot = error %#v pair %#v ready %#v writer %#v", barrierErr, pairErr, readyErr, writer)
	}
	if _, protectionErr := barrier.readerProtection(); protectionErr == nil ||
		protectionErr.code != linuxLiveWriterBarrierState {
		t.Fatalf("failed acquisition exposed protection facts: %#v", protectionErr)
	}
	injected := errors.New("injected validation-error unlock failure")
	if releaseErr := barrier.releaseWith(
		func(*retainedRegular) *linuxOSError {
			return &linuxOSError{code: linuxOSIO, source: injected}
		},
	); releaseErr == nil || releaseErr.code != linuxLiveWriterBarrierUnlockFailed ||
		!errors.Is(releaseErr, injected) || !errors.As(barrierErr, &readyErr) ||
		!writer.barrierHeld || writer.files.sidecar.lock != linuxLockExclusive {
		t.Fatalf("failed validation abort = release %#v original %#v writer %#v",
			releaseErr, barrierErr, writer)
	}
	if abortErr := barrier.abort(); abortErr != nil {
		t.Fatal(abortErr)
	}
	database.putSlot(t, 1, [sidecarSlotSize]byte{})
	database.putSlot(t, 2, [sidecarSlotSize]byte{})
	if _, closeErr := writer.close(); closeErr != nil {
		t.Fatal(closeErr)
	}
}

func TestLinuxLiveWriterReaderBarrierRejectsIdentityGenerationAndLeaseReplacement(t *testing.T) {
	t.Run("canonical paths", func(t *testing.T) {
		database := newLinuxLiveReaderTestDatabase(t, 3, false)
		writer, openErr := openLinuxLiveWriter(database.main)
		if openErr != nil {
			t.Fatal(openErr)
		}
		oldMain, oldSidecar := database.main+".barrier-old", database.sidecar+".barrier-old"
		if err := os.Rename(database.main, oldMain); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(database.sidecar, oldSidecar); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(database.main, []byte("replacement"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(database.sidecar, []byte("replacement"), 0o600); err != nil {
			t.Fatal(err)
		}
		barrier, barrierErr := writer.acquireReaderThresholdBarrierContext(context.Background())
		var osErr *linuxOSError
		if barrierErr == nil || barrierErr.code != linuxLiveWriterBarrierLease ||
			!errors.As(barrierErr, &osErr) || osErr.code != linuxOSPathIdentityMismatch ||
			!writer.barrierHeld || writer.files.sidecar.lock != linuxLockExclusive {
			t.Fatalf("path replacement = error %#v os %#v writer %#v", barrierErr, osErr, writer)
		}
		if abortErr := barrier.abort(); abortErr != nil {
			t.Fatal(abortErr)
		}
		if _, closeErr := writer.close(); closeErr != nil {
			t.Fatal(closeErr)
		}
	})

	t.Run("selected generation", func(t *testing.T) {
		database := newLinuxLiveReaderTestDatabase(t, 3, false)
		writer, openErr := openLinuxLiveWriter(database.main)
		if openErr != nil {
			t.Fatal(openErr)
		}
		replacement := linuxLiveWriterMeta(database, 1)
		replacement.RangeRoot = 0
		replacement.RangeRecordCount = 0
		replaceLinuxLiveWriterMetaPair(t, database, replacement)
		barrier, barrierErr := writer.acquireReaderThresholdBarrierContext(context.Background())
		var writerErr *linuxWriterLeaseError
		if barrierErr == nil || barrierErr.code != linuxLiveWriterBarrierLease ||
			!errors.As(barrierErr, &writerErr) || writerErr.code != linuxWriterGenerationChanged ||
			!writer.barrierHeld || writer.files.sidecar.lock != linuxLockExclusive {
			t.Fatalf("generation replacement = error %#v writer error %#v writer %#v", barrierErr, writerErr, writer)
		}
		if abortErr := barrier.abort(); abortErr != nil {
			t.Fatal(abortErr)
		}
		replaceLinuxLiveWriterMetaPair(t, database, linuxLiveWriterMeta(database, 1))
		if _, closeErr := writer.close(); closeErr != nil {
			t.Fatal(closeErr)
		}
	})

	t.Run("owned lease", func(t *testing.T) {
		database := newLinuxLiveReaderTestDatabase(t, 3, false)
		writer, openErr := openLinuxLiveWriter(database.main)
		if openErr != nil {
			t.Fatal(openErr)
		}
		owned := database.slot(t, 0)
		foreign := writer.owned.active
		foreign.nonce[0] ^= 0xff
		database.putSlot(t, 0, encodeActiveSlot(foreign))
		barrier, barrierErr := writer.acquireReaderThresholdBarrierContext(context.Background())
		var writerErr *linuxWriterLeaseError
		if barrierErr == nil || barrierErr.code != linuxLiveWriterBarrierLease ||
			!errors.As(barrierErr, &writerErr) || writerErr.code != linuxWriterOwnerMismatch ||
			!writer.barrierHeld || writer.files.sidecar.lock != linuxLockExclusive {
			t.Fatalf("lease replacement = error %#v writer error %#v writer %#v", barrierErr, writerErr, writer)
		}
		if abortErr := barrier.abort(); abortErr != nil {
			t.Fatal(abortErr)
		}
		database.putSlot(t, 0, owned)
		if _, closeErr := writer.close(); closeErr != nil {
			t.Fatal(closeErr)
		}
	})
}

func TestLinuxLiveWriterReaderBarrierFactsReapingAndContinuousLock(t *testing.T) {
	database := newLinuxLiveReaderTestDatabase(t, 3, false)
	resizeLinuxLiveWriterSidecar(t, database, 4)
	replaceLinuxLiveWriterMetaPair(t, database, linuxLiveWriterMeta(database, 7))
	writer, openErr := openLinuxLiveWriter(database.main)
	if openErr != nil {
		t.Fatal(openErr)
	}
	registering := currentLinuxActiveSlot(0, [16]byte{0x91})
	oldest := currentLinuxActiveSlot(3, [16]byte{0x92})
	newest := currentLinuxActiveSlot(7, [16]byte{0x93})
	dead := activeSlot{
		txnID: 2, processID: uint64(math.MaxInt32), processStart: 1,
		nonce: [16]byte{0x94},
	}
	database.putSlot(t, 1, encodeActiveSlot(registering))
	database.putSlot(t, 2, encodeActiveSlot(oldest))
	database.putSlot(t, 3, encodeActiveSlot(newest))
	database.putSlot(t, 4, encodeActiveSlot(dead))
	barrier, barrierErr := writer.acquireReaderThresholdBarrierContext(context.Background())
	protection, protectionErr := barrier.readerProtection()
	protectsThree, protectsThreeErr := barrier.protects(3)
	protectsEight, protectsEightErr := barrier.protects(8)
	if barrierErr != nil || protectionErr != nil || protectsThreeErr != nil || protectsEightErr != nil ||
		protection.selectedTxnID != 7 || protection.activeReaders != 3 ||
		protection.registeringReaders != 1 || !protection.oldestReaderTxnValid ||
		protection.oldestReaderTxn != 3 || protection.reapedReaders != 1 ||
		!protectsThree || !protectsEight ||
		database.slot(t, 4) != ([sidecarSlotSize]byte{}) || !writer.barrierHeld ||
		writer.files.sidecar.lock != linuxLockExclusive {
		t.Fatalf("barrier facts = %#v/%#v error %#v/%#v writer %#v dead %x",
			barrier, protection, barrierErr, protectionErr, writer, database.slot(t, 4))
	}
	directory, competitor := openLinuxLiveWriterSidecarCompetitor(t, database)
	if err := competitor.acquireLock(linuxLockExclusive, true); err == nil || err.code != linuxOSLockBusy {
		t.Fatalf("competitor acquired held barrier lock: %#v", err)
	}
	if _, closeErr := writer.close(); closeErr == nil || closeErr.code != linuxLiveWriterCloseBarrierHeld ||
		!writer.barrierHeld {
		t.Fatalf("close with barrier = %#v writer %#v", closeErr, writer)
	}
	if barrierErr = barrier.release(); barrierErr != nil {
		t.Fatal(barrierErr)
	}
	if _, staleErr := barrier.readerProtection(); staleErr == nil ||
		staleErr.code != linuxLiveWriterBarrierState {
		t.Fatalf("released barrier retained protection authority: %#v", staleErr)
	}
	if protected, staleErr := barrier.protects(8); staleErr == nil ||
		staleErr.code != linuxLiveWriterBarrierState || protected {
		t.Fatalf("released barrier retained protection decision: %v/%#v", protected, staleErr)
	}
	if err := competitor.acquireLock(linuxLockExclusive, true); err != nil {
		t.Fatal(err)
	}
	if err := competitor.releaseLock(); err != nil {
		t.Fatal(err)
	}
	_ = competitor.file.Close()
	_ = directory.file.Close()

	database.putSlot(t, 1, [sidecarSlotSize]byte{})
	barrier, barrierErr = writer.acquireReaderThresholdBarrierContext(context.Background())
	protection, protectionErr = barrier.readerProtection()
	protectsThree, protectsThreeErr = barrier.protects(3)
	protectsFour, protectsFourErr := barrier.protects(4)
	if barrierErr != nil || protectionErr != nil || protectsThreeErr != nil || protectsFourErr != nil ||
		protection.registeringReaders != 0 || protection.activeReaders != 2 ||
		!protection.oldestReaderTxnValid || protection.oldestReaderTxn != 3 ||
		protectsThree || !protectsFour {
		t.Fatalf("non-registering barrier facts = %#v/%#v error %#v/%#v",
			barrier, protection, barrierErr, protectionErr)
	}
	if barrierErr = barrier.abort(); barrierErr != nil {
		t.Fatal(barrierErr)
	}
	if _, closeErr := writer.close(); closeErr != nil {
		t.Fatal(closeErr)
	}
}

func TestLinuxLiveWriterReaderBarrierReleaseIsNonConsuming(t *testing.T) {
	database := newLinuxLiveReaderTestDatabase(t, 3, false)
	writer, openErr := openLinuxLiveWriter(database.main)
	if openErr != nil {
		t.Fatal(openErr)
	}
	barrier, barrierErr := writer.acquireReaderThresholdBarrierContext(context.Background())
	if barrierErr != nil {
		t.Fatal(barrierErr)
	}
	protection, protectionErr := barrier.readerProtection()
	if protectionErr != nil {
		t.Fatal(protectionErr)
	}
	injected := &linuxOSError{code: linuxOSIO, operation: "injected barrier unlock"}
	barrierErr = barrier.releaseWith(func(*retainedRegular) *linuxOSError {
		return injected
	})
	afterFailure, afterFailureErr := barrier.readerProtection()
	if barrierErr == nil || barrierErr.code != linuxLiveWriterBarrierUnlockFailed ||
		barrierErr.source != injected || afterFailureErr != nil || afterFailure != protection ||
		!writer.barrierHeld || writer.barrier != protection ||
		writer.files.sidecar.lock != linuxLockExclusive {
		t.Fatalf("failed release = error %#v writer %#v", barrierErr, writer)
	}
	if closeErr := func() *linuxLiveWriterCloseError {
		_, closeErr := writer.close()
		return closeErr
	}(); closeErr == nil || closeErr.code != linuxLiveWriterCloseBarrierHeld {
		t.Fatalf("close after failed release = %#v", closeErr)
	}
	if barrierErr = barrier.abort(); barrierErr != nil {
		t.Fatal(barrierErr)
	}
	if _, staleErr := barrier.readerProtection(); staleErr == nil ||
		staleErr.code != linuxLiveWriterBarrierState || writer.barrier != (linuxLiveWriterReaderProtection{}) {
		t.Fatalf("successful release retained facts: error %#v writer %#v", staleErr, writer)
	}
	replacement, replacementErr := writer.acquireReaderThresholdBarrierContext(context.Background())
	if replacementErr != nil {
		t.Fatal(replacementErr)
	}
	if _, staleErr := barrier.readerProtection(); staleErr == nil ||
		staleErr.code != linuxLiveWriterBarrierState {
		t.Fatalf("old token revived during a later barrier: %#v", staleErr)
	}
	if barrierErr = barrier.abort(); barrierErr == nil ||
		barrierErr.code != linuxLiveWriterBarrierState || !writer.barrierHeld {
		t.Fatalf("stale abort = %#v writer %#v", barrierErr, writer)
	}
	if barrierErr = replacement.release(); barrierErr != nil {
		t.Fatal(barrierErr)
	}
	if _, closeErr := writer.close(); closeErr != nil {
		t.Fatal(closeErr)
	}
}

func TestLinuxLiveWriterReaderBarrierCreatorPIDPrecedesMutexLockAndUnlock(t *testing.T) {
	database := newLinuxLiveReaderTestDatabase(t, 3, false)
	writer, openErr := openLinuxLiveWriter(database.main)
	if openErr != nil {
		t.Fatal(openErr)
	}
	active := database.slot(t, 0)
	writer.creatorPID++
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	allocations := testing.AllocsPerRun(100, func() {
		_, barrierErr := writer.acquireReaderThresholdBarrierContext(cancelled)
		if barrierErr == nil || barrierErr.code != linuxLiveWriterBarrierForkedHandle {
			panic("barrier did not reject inherited writer first")
		}
	})
	if allocations > 1 || writer.barrierHeld || writer.files.sidecar.lock != 0 || database.slot(t, 0) != active {
		t.Fatalf("forked acquire = allocations %v writer %#v slot %x", allocations, writer, database.slot(t, 0))
	}
	writer.creatorPID = os.Getpid()
	barrier, barrierErr := writer.acquireReaderThresholdBarrierContext(context.Background())
	if barrierErr != nil {
		t.Fatal(barrierErr)
	}
	writer.creatorPID++
	if barrierErr := barrier.release(); barrierErr == nil ||
		barrierErr.code != linuxLiveWriterBarrierForkedHandle || !writer.barrierHeld ||
		writer.files.sidecar.lock != linuxLockExclusive || database.slot(t, 0) != active {
		t.Fatalf("forked release = %#v writer %#v slot %x", barrierErr, writer, database.slot(t, 0))
	}
	writer.creatorPID = os.Getpid()
	if barrierErr := barrier.release(); barrierErr != nil {
		t.Fatal(barrierErr)
	}
	if _, closeErr := writer.close(); closeErr != nil {
		t.Fatal(closeErr)
	}
}

func TestLinuxLiveWriterReaderBarrierPreparedDeathProofsAreExactAndConservative(t *testing.T) {
	prepare := func(t *testing.T, writer *linuxLiveWriter) {
		t.Helper()
		writer.operation.Lock()
		barrierErr := writer.prepareReaderThresholdBarrierLocked(context.Background())
		writer.operation.Unlock()
		if barrierErr != nil {
			t.Fatal(barrierErr)
		}
		if writer.barrierHeld || writer.files.sidecar.lock != 0 {
			t.Fatalf("preflight acquired operation flock: %#v", writer)
		}
	}
	acquirePrepared := func(t *testing.T, writer *linuxLiveWriter) linuxLiveWriterReaderBarrier {
		t.Helper()
		writer.operation.Lock()
		barrier, barrierErr := writer.acquirePreparedReaderThresholdBarrierLocked(
			context.Background(), nil, true,
		)
		writer.operation.Unlock()
		if barrierErr != nil {
			t.Fatal(barrierErr)
		}
		return barrier
	}

	t.Run("exact candidate", func(t *testing.T) {
		database := newLinuxLiveReaderTestDatabase(t, 3, false)
		resizeLinuxLiveWriterSidecar(t, database, 1)
		writer, openErr := openLinuxLiveWriter(database.main)
		if openErr != nil {
			t.Fatal(openErr)
		}
		dead := activeSlot{
			txnID: 1, processID: uint64(math.MaxInt32), processStart: 1,
			nonce: [16]byte{0xb1},
		}
		image := encodeActiveSlot(dead)
		database.putSlot(t, 1, image)
		prepare(t, writer)
		if len(writer.barrierWork.candidates) != 1 || cap(writer.barrierWork.candidates) > 1 ||
			len(writer.barrierWork.observations) != 0 || database.slot(t, 1) != image {
			t.Fatalf("prepared candidate = %#v slot %x", writer.barrierWork.candidates, database.slot(t, 1))
		}
		barrier := acquirePrepared(t, writer)
		protection, protectionErr := barrier.readerProtection()
		if protectionErr != nil || protection.reapedReaders != 1 || protection.activeReaders != 0 ||
			database.slot(t, 1) != ([sidecarSlotSize]byte{}) {
			t.Fatalf("exact prepared reap = %#v/%#v slot %x", protection, protectionErr, database.slot(t, 1))
		}
		if barrierErr := barrier.release(); barrierErr != nil {
			t.Fatal(barrierErr)
		}
		if _, closeErr := writer.close(); closeErr != nil {
			t.Fatal(closeErr)
		}
	})

	t.Run("deduplicated pid-reuse proof", func(t *testing.T) {
		database := newLinuxLiveReaderTestDatabase(t, 3, false)
		resizeLinuxLiveWriterSidecar(t, database, 2)
		writer, openErr := openLinuxLiveWriter(database.main)
		if openErr != nil {
			t.Fatal(openErr)
		}
		current := currentLinuxActiveSlot(1, [16]byte{0xb6})
		stale := current
		stale.processStart++
		stale.nonce = [16]byte{0xb7}
		currentImage, staleImage := encodeActiveSlot(current), encodeActiveSlot(stale)
		database.putSlot(t, 1, currentImage)
		database.putSlot(t, 2, staleImage)
		prepare(t, writer)
		if len(writer.barrierWork.candidates) != 1 ||
			writer.barrierWork.candidates[0].index != 2 ||
			writer.barrierWork.candidates[0].proof.kind != deathProofPOSIXPIDReused {
			t.Fatalf("PID-reuse candidates = %#v", writer.barrierWork.candidates)
		}
		barrier := acquirePrepared(t, writer)
		protection, protectionErr := barrier.readerProtection()
		if protectionErr != nil || protection.reapedReaders != 1 || protection.activeReaders != 1 ||
			database.slot(t, 1) != currentImage || database.slot(t, 2) != ([sidecarSlotSize]byte{}) {
			t.Fatalf("PID-reuse prepared reap = %#v/%#v slots %x/%x",
				protection, protectionErr, database.slot(t, 1), database.slot(t, 2))
		}
		if barrierErr := barrier.release(); barrierErr != nil {
			t.Fatal(barrierErr)
		}
		database.putSlot(t, 1, [sidecarSlotSize]byte{})
		if _, closeErr := writer.close(); closeErr != nil {
			t.Fatal(closeErr)
		}
	})

	t.Run("changed owner", func(t *testing.T) {
		database := newLinuxLiveReaderTestDatabase(t, 3, false)
		resizeLinuxLiveWriterSidecar(t, database, 1)
		writer, openErr := openLinuxLiveWriter(database.main)
		if openErr != nil {
			t.Fatal(openErr)
		}
		dead := activeSlot{
			txnID: 1, processID: uint64(math.MaxInt32), processStart: 1,
			nonce: [16]byte{0xb2},
		}
		database.putSlot(t, 1, encodeActiveSlot(dead))
		prepare(t, writer)
		current := currentLinuxActiveSlot(1, [16]byte{0xb3})
		currentImage := encodeActiveSlot(current)
		database.putSlot(t, 1, currentImage)
		barrier := acquirePrepared(t, writer)
		protection, protectionErr := barrier.readerProtection()
		if protectionErr != nil || protection.reapedReaders != 0 || protection.activeReaders != 1 ||
			database.slot(t, 1) != currentImage {
			t.Fatalf("changed prepared owner = %#v/%#v slot %x", protection, protectionErr, database.slot(t, 1))
		}
		if barrierErr := barrier.release(); barrierErr != nil {
			t.Fatal(barrierErr)
		}
		database.putSlot(t, 1, [sidecarSlotSize]byte{})
		if _, closeErr := writer.close(); closeErr != nil {
			t.Fatal(closeErr)
		}
	})

	t.Run("unprepared existing pid", func(t *testing.T) {
		database := newLinuxLiveReaderTestDatabase(t, 3, false)
		resizeLinuxLiveWriterSidecar(t, database, 1)
		writer, openErr := openLinuxLiveWriter(database.main)
		if openErr != nil {
			t.Fatal(openErr)
		}
		prepare(t, writer)
		current := currentLinuxActiveSlot(1, [16]byte{0xb4})
		current.processStart++
		image := encodeActiveSlot(current)
		database.putSlot(t, 1, image)
		barrier := acquirePrepared(t, writer)
		protection, protectionErr := barrier.readerProtection()
		if protectionErr != nil || protection.reapedReaders != 0 || protection.activeReaders != 1 ||
			database.slot(t, 1) != image {
			t.Fatalf("unprepared existing PID = %#v/%#v slot %x", protection, protectionErr, database.slot(t, 1))
		}
		if barrierErr := barrier.release(); barrierErr != nil {
			t.Fatal(barrierErr)
		}
		database.putSlot(t, 1, [sidecarSlotSize]byte{})
		if _, closeErr := writer.close(); closeErr != nil {
			t.Fatal(closeErr)
		}
	})

	t.Run("unprepared missing pid", func(t *testing.T) {
		database := newLinuxLiveReaderTestDatabase(t, 3, false)
		resizeLinuxLiveWriterSidecar(t, database, 1)
		writer, openErr := openLinuxLiveWriter(database.main)
		if openErr != nil {
			t.Fatal(openErr)
		}
		prepare(t, writer)
		dead := activeSlot{
			txnID: 1, processID: uint64(math.MaxInt32), processStart: 1,
			nonce: [16]byte{0xb5},
		}
		database.putSlot(t, 1, encodeActiveSlot(dead))
		barrier := acquirePrepared(t, writer)
		protection, protectionErr := barrier.readerProtection()
		if protectionErr != nil || protection.reapedReaders != 1 || protection.activeReaders != 0 ||
			database.slot(t, 1) != ([sidecarSlotSize]byte{}) {
			t.Fatalf("unprepared missing PID = %#v/%#v slot %x", protection, protectionErr, database.slot(t, 1))
		}
		if barrierErr := barrier.release(); barrierErr != nil {
			t.Fatal(barrierErr)
		}
		if _, closeErr := writer.close(); closeErr != nil {
			t.Fatal(closeErr)
		}
	})
}

func TestLinuxLiveWriterReaderBarrierPreflightParserMatchesCanonicalParser(t *testing.T) {
	for _, data := range [][]byte{
		linuxProcStat([]byte("name with ) embedded"), []byte("424242")),
		linuxProcStat([]byte(") )"), []byte("18446744073709551615")),
		[]byte("123 no command end"),
		[]byte("123 (x) R 1 2"),
		linuxProcStat([]byte("x"), []byte("-1")),
		linuxProcStat([]byte("x"), []byte("0")),
		linuxProcStat([]byte("x"), []byte("18446744073709551616")),
	} {
		wantStart, wantProblem := parseLinuxProcStatStart(data)
		gotStart, gotProblem := parseLinuxProcStatStartWithoutFields(data)
		if gotStart != wantStart || gotProblem != wantProblem {
			t.Fatalf("preflight parser = (%d, %d), canonical = (%d, %d) for %q",
				gotStart, gotProblem, wantStart, wantProblem, data)
		}
	}
}

func TestLinuxLiveWriterReaderBarrierRepeatedCyclesHaveBoundedAllocations(t *testing.T) {
	if raceEnabled {
		t.Skip("race instrumentation changes allocation accounting")
	}
	allocationsByCapacity := make(map[uint32]float64, 2)
	failureAllocationsByCapacity := make(map[uint32]float64, 2)
	for _, capacity := range []uint32{1, 1024} {
		t.Run(string(rune('0'+capacity/1024)), func(t *testing.T) {
			database := newLinuxLiveReaderTestDatabase(t, 3, false)
			resizeLinuxLiveWriterSidecar(t, database, capacity)
			writer, openErr := openLinuxLiveWriter(database.main)
			if openErr != nil {
				t.Fatal(openErr)
			}
			cycles := 100
			if capacity > 1 {
				cycles = 10
			}
			allocations := testing.AllocsPerRun(cycles, func() {
				barrier, barrierErr := writer.acquireReaderThresholdBarrierContext(context.Background())
				protection, protectionErr := barrier.readerProtection()
				if barrierErr != nil || protectionErr != nil ||
					protection.selectedTxnID != 1 || protection.activeReaders != 0 {
					panic("unexpected repeated barrier result")
				}
				if barrierErr = barrier.release(); barrierErr != nil {
					panic("unexpected repeated barrier release")
				}
			})
			// Retained canonical-path checks use the safe x/sys pathname API,
			// which constructs NUL-terminated syscall arguments. Keep that fixed
			// cost bounded and prove the complete table scan adds no per-slot heap.
			if allocations > 64 {
				t.Fatalf("capacity %d barrier allocations = %v, want at most 64", capacity, allocations)
			}
			allocationsByCapacity[capacity] = allocations

			malformed := [sidecarSlotSize]byte{4: 1}
			database.putSlot(t, capacity, malformed)
			failureAllocations := testing.AllocsPerRun(cycles, func() {
				barrier, barrierErr := writer.acquireReaderThresholdBarrierContext(context.Background())
				var readyErr *readySidecarError
				if barrierErr == nil || barrierErr.code != linuxLiveWriterBarrierPair ||
					!errors.As(barrierErr, &readyErr) || readyErr.code != readySidecarErrSlot {
					panic("unexpected repeated malformed barrier result")
				}
				if barrierErr = barrier.abort(); barrierErr != nil {
					panic("unexpected repeated malformed barrier abort")
				}
			})
			if failureAllocations > 64 {
				t.Fatalf("capacity %d failed barrier allocations = %v, want at most 64",
					capacity, failureAllocations)
			}
			failureAllocationsByCapacity[capacity] = failureAllocations
			database.putSlot(t, capacity, [sidecarSlotSize]byte{})
			if writer.barrierHeld || writer.files.sidecar.lock != 0 || writer.state != linuxLiveWriterStateOpen {
				t.Fatalf("capacity %d repeated state = %#v", capacity, writer)
			}
			if _, closeErr := writer.close(); closeErr != nil {
				t.Fatal(closeErr)
			}
		})
	}
	if allocationsByCapacity[1024] != allocationsByCapacity[1] {
		t.Fatalf("barrier allocations scale with capacity: one=%v 1024=%v",
			allocationsByCapacity[1], allocationsByCapacity[1024])
	}
	if failureAllocationsByCapacity[1024] != failureAllocationsByCapacity[1] {
		t.Fatalf("failed barrier allocations scale with capacity: one=%v 1024=%v",
			failureAllocationsByCapacity[1], failureAllocationsByCapacity[1024])
	}
}

func TestLinuxLiveWriterReaderBarrierLockedPhaseRealReadersDoNotScaleAllocations(t *testing.T) {
	if raceEnabled {
		t.Skip("race instrumentation changes allocation accounting")
	}
	allocationsByCapacity := make(map[uint32]float64, 2)
	for _, capacity := range []uint32{1, 1024} {
		t.Run(string(rune('0'+capacity/1024)), func(t *testing.T) {
			database := newLinuxLiveReaderTestDatabase(t, 3, false)
			resizeLinuxLiveWriterSidecar(t, database, capacity)
			active := currentLinuxActiveSlot(1, [16]byte{0xa1})
			if active.processStart == 0 {
				t.Fatal("current process start is unavailable")
			}
			image := encodeActiveSlot(active)
			for index := uint32(1); index <= capacity; index++ {
				database.putSlot(t, index, image)
			}
			writer, openErr := openLinuxLiveWriter(database.main)
			if openErr != nil {
				t.Fatal(openErr)
			}
			writer.operation.Lock()
			prepareErr := writer.prepareReaderThresholdBarrierLocked(context.Background())
			writer.operation.Unlock()
			if prepareErr != nil {
				t.Fatal(prepareErr)
			}
			cycles := 20
			if capacity > 1 {
				cycles = 3
			}
			allocations := testing.AllocsPerRun(cycles, func() {
				writer.operation.Lock()
				barrier, barrierErr := writer.acquirePreparedReaderThresholdBarrierLocked(
					context.Background(), nil, true,
				)
				writer.operation.Unlock()
				protection, protectionErr := barrier.readerProtection()
				if barrierErr != nil || protectionErr != nil ||
					protection.activeReaders != capacity || protection.oldestReaderTxn != 1 {
					panic("unexpected real-reader barrier result")
				}
				if barrierErr = barrier.release(); barrierErr != nil {
					panic("unexpected real-reader barrier release")
				}
			})
			if allocations > 64 {
				t.Fatalf("capacity %d real-reader barrier allocations = %v, want at most 64",
					capacity, allocations)
			}
			// Acquisition retains a fixed canonical-path verification cost. The
			// allocator/retirement fixed point, not this barrier, is the section 13
			// literal-zero allocation region.
			allocationsByCapacity[capacity] = allocations
			if _, closeErr := writer.close(); closeErr != nil {
				t.Fatal(closeErr)
			}
		})
	}
	if allocationsByCapacity[1024] != allocationsByCapacity[1] {
		t.Fatalf("real active-reader allocations scale with capacity: one=%v 1024=%v",
			allocationsByCapacity[1], allocationsByCapacity[1024])
	}
	t.Logf("locked-phase real-reader allocations: one=%v 1024=%v",
		allocationsByCapacity[1], allocationsByCapacity[1024])
}

func BenchmarkLinuxLiveWriterOpenClose(b *testing.B) {
	database := newLinuxLiveReaderTestDatabase(b, 3, false)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		writer, openErr := openLinuxLiveWriter(database.main)
		if openErr != nil {
			b.Fatal(openErr)
		}
		if _, closeErr := writer.close(); closeErr != nil {
			b.Fatal(closeErr)
		}
	}
}

func BenchmarkLinuxLiveWriterReaderBarrierPreflightProcessObservation(b *testing.B) {
	active := currentLinuxActiveSlot(1, [16]byte{0xc1})
	var workspace linuxProcessObservationWorkspace
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		observation := observeLinuxProcessWithWorkspace(active, &workspace)
		if observation.kind != posixProcessExists || observation.currentStart != active.processStart {
			b.Fatal("current process observation changed")
		}
	}
}
