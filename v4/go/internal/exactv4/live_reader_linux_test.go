//go:build linux

package exactv4

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"
)

type linuxLiveReaderTestDatabase struct {
	directory string
	main      string
	sidecar   string
	header    sidecarHeader
}

func newLinuxLiveReaderTestDatabase(
	t testing.TB,
	physicalPages int,
	corruptLeafCRC bool,
) *linuxLiveReaderTestDatabase {
	t.Helper()
	if physicalPages < 3 {
		t.Fatalf("physical pages = %d, want at least 3", physicalPages)
	}
	directoryPath := t.TempDir()
	mainPath := filepath.Join(directoryPath, "main.iprdb")
	sidecarPath := mainPath + ".readers"

	meta := emptyDirectMeta(1)
	meta.AddressFamily = AddressFamilyIPv4
	meta.ValueKind = ValueKindDirect
	meta.PageCount = 3
	meta.RangeRoot = 2
	meta.RangeRecordCount = 2
	mainImage := make([]byte, physicalPages*PageSize)
	metaPage := meta.EncodePage()
	copy(mainImage[:PageSize], metaPage[:])
	copy(mainImage[PageSize:2*PageSize], metaPage[:])
	putRangeLeaf(t, mainImage[2*PageSize:3*PageSize], []rangeRecord[IPv4]{
		{from: 10, to: 20, value: 7},
		{from: 30, to: 40, value: 8},
	})
	if corruptLeafCRC {
		mainImage[2*PageSize+PageCRCOffset] ^= 1
	}
	if err := os.WriteFile(mainPath, mainImage, 0o600); err != nil {
		t.Fatal(err)
	}

	directory, mainComponent, openErr := openRetainedParent(mainPath)
	if openErr != nil {
		t.Fatal(openErr)
	}
	closeSetup := func(main, sidecar *retainedRegular) {
		if sidecar != nil {
			_ = sidecar.file.Close()
		}
		if main != nil {
			_ = main.file.Close()
		}
		_ = directory.file.Close()
	}
	sidecarComponent, componentErr := directory.sidecarComponent(mainComponent)
	if componentErr != nil {
		closeSetup(nil, nil)
		t.Fatal(componentErr)
	}
	created, err := os.OpenFile(sidecarPath, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		closeSetup(nil, nil)
		t.Fatal(err)
	}
	if err := created.Truncate(headerRegionSize + 2*int64(sidecarSlotSize)); err != nil {
		_ = created.Close()
		closeSetup(nil, nil)
		t.Fatal(err)
	}
	if err := created.Close(); err != nil {
		closeSetup(nil, nil)
		t.Fatal(err)
	}
	main, openErr := directory.openRegular(mainComponent, true)
	if openErr != nil {
		closeSetup(nil, nil)
		t.Fatal(openErr)
	}
	sidecar, openErr := directory.openRegular(sidecarComponent, true)
	if openErr != nil {
		closeSetup(main, nil)
		t.Fatal(openErr)
	}
	domain, domainErr := linuxProcessDomainToken()
	if domainErr != nil {
		closeSetup(main, sidecar)
		t.Fatal(domainErr)
	}
	commitment, bindingErr := basenameCommitment(basenamePOSIXBytes, []byte(mainComponent))
	if bindingErr != nil {
		closeSetup(main, sidecar)
		t.Fatal(bindingErr)
	}
	header := sidecarHeader{
		identityKind: localIdentityPOSIX, capacity: 1, state: sidecarReady,
		databaseID: meta.DatabaseID, mainIdentity: main.identity.encode(),
		sidecarIdentity: sidecar.identity.encode(), sidecarID: [16]byte{7},
		origin: sidecarOriginCreateLive, attemptedTxnID: meta.TxnID,
		attemptedCommitNonce: meta.CommitNonce, attemptedMainBytes: 3 * PageSize,
		attemptedMainSHA512: [64]byte{8}, processDomainKind: processDomainLinuxPIDNamespace,
		processDomainToken: domain, basenameEncoding: uint16(basenamePOSIXBytes),
		basenameLen: uint32(len(mainComponent)), basenameCommitment: commitment,
		creationSecurityKind: 1, creationSecurityCommitment: [32]byte{9}, headerSeq: 1,
	}
	var headerBlock [PageSize]byte
	header.encodeInto(&headerBlock)
	if err := sidecar.writeAllAt(headerBlock[:], 0); err != nil {
		closeSetup(main, sidecar)
		t.Fatal(err)
	}
	if err := sidecar.writeAllAt(headerBlock[:], PageSize); err != nil {
		closeSetup(main, sidecar)
		t.Fatal(err)
	}
	closeSetup(main, sidecar)
	return &linuxLiveReaderTestDatabase{
		directory: directoryPath, main: mainPath, sidecar: sidecarPath, header: header,
	}
}

func (database *linuxLiveReaderTestDatabase) slotAt(
	t testing.TB,
	path string,
	index uint32,
) [sidecarSlotSize]byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	offset, offsetErr := sidecarSlotOffset(database.header, index)
	if offsetErr != nil {
		t.Fatal(offsetErr)
	}
	end := offset + uint64(sidecarSlotSize)
	if end > uint64(len(data)) {
		t.Fatalf("slot %d end %d exceeds file length %d", index, end, len(data))
	}
	var raw [sidecarSlotSize]byte
	copy(raw[:], data[offset:end])
	return raw
}

func (database *linuxLiveReaderTestDatabase) slot(t testing.TB, index uint32) [sidecarSlotSize]byte {
	t.Helper()
	return database.slotAt(t, database.sidecar, index)
}

func (database *linuxLiveReaderTestDatabase) putSlot(
	t testing.TB,
	index uint32,
	raw [sidecarSlotSize]byte,
) {
	t.Helper()
	file, err := os.OpenFile(database.sidecar, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	offset, offsetErr := sidecarSlotOffset(database.header, index)
	if offsetErr != nil {
		t.Fatal(offsetErr)
	}
	written, err := file.WriteAt(raw[:], int64(offset))
	if err != nil || written != len(raw) {
		t.Fatalf("write slot = %d, %v", written, err)
	}
}

func injectedLinuxLiveOpenFailure() *linuxLiveReaderOpenCause {
	return &linuxLiveReaderOpenCause{
		code:   linuxLiveReaderOpenSlot,
		source: &linuxReaderSlotError{code: linuxReaderGenerationChanged},
	}
}

func requireLinuxLiveOpenError(
	t testing.TB,
	err *linuxLiveReaderOpenError,
	wantCode linuxLiveReaderOpenErrorCode,
	wantCause linuxLiveReaderOpenCauseCode,
) {
	t.Helper()
	if err == nil || err.code != wantCode || err.cause == nil || err.cause.code != wantCause {
		t.Fatalf("live open error = %#v, want code %d cause %d", err, wantCode, wantCause)
	}
}

func TestLinuxLiveReaderFailuresAtPostClaimBoundariesClearExactSlot(t *testing.T) {
	for _, failedStage := range []linuxLiveReaderOpenStage{
		linuxLiveReaderStageClaimPublished,
		linuxLiveReaderStagePinPublished,
		linuxLiveReaderStageBeforeUnlock,
		linuxLiveReaderStageViewSetup,
	} {
		t.Run(string(rune('0'+failedStage)), func(t *testing.T) {
			database := newLinuxLiveReaderTestDatabase(t, 3, false)
			_, openErr := openLinuxLiveReaderWithHook[IPv4](
				context.Background(), database.main,
				func(stage linuxLiveReaderOpenStage, _ *retainedLiveFiles, _ *linuxOwnedReaderSlot) *linuxLiveReaderOpenCause {
					if stage == failedStage {
						return injectedLinuxLiveOpenFailure()
					}
					return nil
				},
			)
			requireLinuxLiveOpenError(t, openErr, linuxLiveReaderOpenFailed, linuxLiveReaderOpenSlot)
			if openErr.cleanupOutcome == nil {
				t.Fatal("post-claim failure did not report cleanup outcome")
			}
			if raw := database.slot(t, 1); raw != ([sidecarSlotSize]byte{}) {
				t.Fatalf("reader slot after failed stage %d = %x", failedStage, raw)
			}
		})
	}
}

func TestLinuxLiveReaderWrongFamilyFailsBeforeClaim(t *testing.T) {
	database := newLinuxLiveReaderTestDatabase(t, 3, false)
	reachedHook := false
	_, openErr := openLinuxLiveReaderWithHook[IPv6](
		context.Background(), database.main,
		func(stage linuxLiveReaderOpenStage, _ *retainedLiveFiles, _ *linuxOwnedReaderSlot) *linuxLiveReaderOpenCause {
			if stage != linuxLiveReaderStageBeforeScan {
				reachedHook = true
			}
			return nil
		},
	)
	requireLinuxLiveOpenError(t, openErr, linuxLiveReaderOpenFailed, linuxLiveReaderOpenView)
	if reachedHook || openErr.cleanupOutcome != nil {
		t.Fatalf("preclaim view failure = hook %t cleanup %#v", reachedHook, openErr.cleanupOutcome)
	}
	if raw := database.slot(t, 1); raw != ([sidecarSlotSize]byte{}) {
		t.Fatalf("preclaim failure published slot = %x", raw)
	}
}

func TestLinuxLiveReaderAutomaticCleanupReportsBothReplacedPaths(t *testing.T) {
	database := newLinuxLiveReaderTestDatabase(t, 3, false)
	oldMain := filepath.Join(database.directory, "selected-main")
	oldSidecar := filepath.Join(database.directory, "selected-sidecar")
	_, openErr := openLinuxLiveReaderWithHook[IPv4](
		context.Background(), database.main,
		func(stage linuxLiveReaderOpenStage, _ *retainedLiveFiles, _ *linuxOwnedReaderSlot) *linuxLiveReaderOpenCause {
			if stage != linuxLiveReaderStageClaimPublished {
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
			return injectedLinuxLiveOpenFailure()
		},
	)
	requireLinuxLiveOpenError(t, openErr, linuxLiveReaderOpenFailed, linuxLiveReaderOpenSlot)
	if openErr.cleanupOutcome == nil || openErr.cleanupOutcome.mainPath == nil ||
		openErr.cleanupOutcome.sidecarPath == nil {
		t.Fatalf("path replacement cleanup = %#v", openErr.cleanupOutcome)
	}
	if raw := database.slotAt(t, oldSidecar, 1); raw != ([sidecarSlotSize]byte{}) {
		t.Fatalf("retained sidecar slot after cleanup = %x", raw)
	}
}

func TestLinuxLiveReaderCancellationIsTypedBeforeAndAfterClaim(t *testing.T) {
	database := newLinuxLiveReaderTestDatabase(t, 3, false)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, openErr := openLinuxLiveReaderContext[IPv4](ctx, database.main)
	requireLinuxLiveOpenError(t, openErr, linuxLiveReaderOpenFailed, linuxLiveReaderOpenCancelled)
	if openErr.cleanupOutcome != nil {
		t.Fatalf("preclaim cancellation cleanup = %#v", openErr.cleanupOutcome)
	}

	for _, cancelledStage := range []linuxLiveReaderOpenStage{
		linuxLiveReaderStageClaimPublished,
		linuxLiveReaderStagePinPublished,
		linuxLiveReaderStageBeforeUnlock,
		linuxLiveReaderStageViewSetup,
	} {
		database = newLinuxLiveReaderTestDatabase(t, 3, false)
		ctx, cancel = context.WithCancel(context.Background())
		_, openErr = openLinuxLiveReaderWithHook[IPv4](
			ctx, database.main,
			func(stage linuxLiveReaderOpenStage, _ *retainedLiveFiles, _ *linuxOwnedReaderSlot) *linuxLiveReaderOpenCause {
				if stage == cancelledStage {
					cancel()
				}
				return nil
			},
		)
		requireLinuxLiveOpenError(t, openErr, linuxLiveReaderOpenFailed, linuxLiveReaderOpenCancelled)
		if openErr.cleanupOutcome == nil || database.slot(t, 1) != ([sidecarSlotSize]byte{}) {
			t.Fatalf("postclaim cancellation stage %d = %#v slot %x", cancelledStage, openErr, database.slot(t, 1))
		}
	}
}

func TestLinuxLiveReaderRetainedOpenCancellationIsTyped(t *testing.T) {
	database := newLinuxLiveReaderTestDatabase(t, 3, false)
	blocker, pairErr := openLockedRetainedLiveFiles(database.main)
	if pairErr != nil {
		t.Fatal(pairErr)
	}
	defer closeRetainedLiveFiles(blocker)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, openErr := openLinuxLiveReaderContext[IPv4](ctx, database.main)
	requireLinuxLiveOpenError(t, openErr, linuxLiveReaderOpenFailed, linuxLiveReaderOpenCancelled)
	if openErr.cleanupOutcome != nil || !errors.Is(openErr, context.DeadlineExceeded) {
		t.Fatalf("cancelled retained open = %#v", openErr)
	}
}

func TestLinuxLiveReaderScanCancellationIsTypedFromScan(t *testing.T) {
	database := newLinuxLiveReaderTestDatabase(t, 3, false)
	ctx, cancel := context.WithCancel(context.Background())
	reachedScan := false
	_, openErr := openLinuxLiveReaderWithHook[IPv4](
		ctx, database.main,
		func(stage linuxLiveReaderOpenStage, _ *retainedLiveFiles, _ *linuxOwnedReaderSlot) *linuxLiveReaderOpenCause {
			if stage == linuxLiveReaderStageBeforeScan {
				reachedScan = true
				cancel()
			}
			return nil
		},
	)
	requireLinuxLiveOpenError(t, openErr, linuxLiveReaderOpenFailed, linuxLiveReaderOpenCancelled)
	var pairErr *linuxLivePairError
	var scanErr *linuxSidecarScanError
	if !reachedScan || !errors.As(openErr, &pairErr) || pairErr.code != linuxLivePairScan ||
		!errors.As(openErr, &scanErr) || scanErr.code != linuxSidecarScanCancelled ||
		openErr.cleanupOutcome != nil {
		t.Fatalf("scan cancellation classification = reached %t error %#v", reachedScan, openErr)
	}
}

func TestLinuxLiveReaderPostZeroPathFailureUsesCleanupCause(t *testing.T) {
	database := newLinuxLiveReaderTestDatabase(t, 4, false)
	writer := activeSlot{
		txnID: 1, processID: uint64(math.MaxInt32), processStart: 1,
		nonce: [16]byte{0x7a},
	}
	database.putSlot(t, 0, encodeActiveSlot(writer))
	oldMain := filepath.Join(database.directory, "post-zero-main")
	reachedPostClear := false
	_, openErr := openLinuxLiveReaderWithHook[IPv4](
		context.Background(), database.main,
		func(stage linuxLiveReaderOpenStage, _ *retainedLiveFiles, _ *linuxOwnedReaderSlot) *linuxLiveReaderOpenCause {
			if stage != linuxLiveReaderStageDeadWriterCleared {
				return nil
			}
			reachedPostClear = true
			if err := os.Rename(database.main, oldMain); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(database.main, []byte("replacement"), 0o600); err != nil {
				t.Fatal(err)
			}
			return nil
		},
	)
	requireLinuxLiveOpenError(t, openErr, linuxLiveReaderOpenFailed, linuxLiveReaderOpenPair)
	var pairErr *linuxLivePairError
	var staleScanErr *linuxSidecarScanError
	if !reachedPostClear || !errors.As(openErr, &pairErr) ||
		pairErr.code != linuxLivePairPostClearPath || errors.As(openErr, &staleScanErr) {
		t.Fatalf("post-zero terminal cause = reached %t error %#v", reachedPostClear, openErr)
	}
	if openErr.cleanupOutcome == nil || openErr.cleanupOutcome.mainPath == nil ||
		openErr.cleanupOutcome.sidecarPath != nil || openErr.guard != nil || openErr.cleanup != nil {
		t.Fatalf("post-zero cleanup result = %#v", openErr)
	}
	if raw := database.slot(t, 0); raw != ([sidecarSlotSize]byte{}) {
		t.Fatalf("post-zero writer slot = %x", raw)
	}
}

func TestLinuxLiveReaderDeadWriterCancellationCleansObligation(t *testing.T) {
	database := newLinuxLiveReaderTestDatabase(t, 4, false)
	writer := activeSlot{
		txnID: 1, processID: uint64(math.MaxInt32), processStart: 1,
		nonce: [16]byte{0x79},
	}
	database.putSlot(t, 0, encodeActiveSlot(writer))
	ctx, cancel := context.WithCancel(context.Background())
	_, openErr := openLinuxLiveReaderWithHook[IPv4](
		ctx, database.main,
		func(stage linuxLiveReaderOpenStage, _ *retainedLiveFiles, _ *linuxOwnedReaderSlot) *linuxLiveReaderOpenCause {
			if stage == linuxLiveReaderStageDeadWriterFound {
				cancel()
			}
			return nil
		},
	)
	requireLinuxLiveOpenError(t, openErr, linuxLiveReaderOpenFailed, linuxLiveReaderOpenCancelled)
	if openErr.cleanupOutcome == nil || database.slot(t, 0) != ([sidecarSlotSize]byte{}) {
		t.Fatalf("cancelled dead-writer cleanup = error %#v slot %x", openErr, database.slot(t, 0))
	}
	info, err := os.Stat(database.main)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != 3*PageSize {
		t.Fatalf("cancelled dead-writer tail size = %d", info.Size())
	}
}

func TestLinuxLiveReaderCleanupGuardRetriesWithoutConsumingAuthority(t *testing.T) {
	database := newLinuxLiveReaderTestDatabase(t, 3, false)
	_, openErr := openLinuxLiveReaderWithHook[IPv4](
		context.Background(), database.main,
		func(stage linuxLiveReaderOpenStage, files *retainedLiveFiles, owned *linuxOwnedReaderSlot) *linuxLiveReaderOpenCause {
			if stage != linuxLiveReaderStageClaimPublished {
				return nil
			}
			foreign := owned.active
			foreign.nonce = [16]byte{0x55}
			raw := encodeActiveSlot(foreign)
			offset, err := sidecarSlotOffset(owned.header, owned.index)
			if err != nil {
				t.Fatal(err)
			}
			if err := files.sidecar.writeAllAt(raw[:], offset); err != nil {
				t.Fatal(err)
			}
			return injectedLinuxLiveOpenFailure()
		},
	)
	requireLinuxLiveOpenError(t, openErr, linuxLiveReaderOpenCleanupRequired, linuxLiveReaderOpenSlot)
	guard := openErr.guard
	if guard == nil || openErr.cleanup == nil {
		t.Fatalf("cleanup-required error = %#v", openErr)
	}
	if _, cleanupErr := guard.retryCleanup(); cleanupErr == nil || cleanupErr.code != linuxLiveCleanupSlot {
		t.Fatalf("conflicting retry = %#v", cleanupErr)
	}
	owned := guard.owned
	raw := encodeActiveSlot(owned.active)
	offset, err := sidecarSlotOffset(owned.header, owned.index)
	if err != nil {
		t.Fatal(err)
	}
	if err := guard.files.sidecar.writeAllAt(raw[:], offset); err != nil {
		t.Fatal(err)
	}
	if outcome, cleanupErr := guard.close(); cleanupErr != nil || outcome == nil {
		t.Fatalf("guard close = outcome %#v error %#v", outcome, cleanupErr)
	}
	if outcome, cleanupErr := guard.retryCleanup(); cleanupErr != nil || outcome != nil {
		t.Fatalf("idempotent guard retry = outcome %#v error %#v", outcome, cleanupErr)
	}
}

func TestLinuxLiveReaderEstablishedCloseIsRetriableAndIdempotent(t *testing.T) {
	database := newLinuxLiveReaderTestDatabase(t, 3, false)
	reader, openErr := openLinuxLiveReader[IPv4](database.main)
	if openErr != nil {
		t.Fatal(openErr)
	}
	foreign := reader.owned.active
	foreign.nonce = [16]byte{0x66}
	raw := encodeActiveSlot(foreign)
	offset, err := sidecarSlotOffset(reader.owned.header, reader.owned.index)
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.files.sidecar.writeAllAt(raw[:], offset); err != nil {
		t.Fatal(err)
	}
	if _, closeErr := reader.close(); closeErr == nil || closeErr.code != linuxLiveReaderCloseCleanup {
		t.Fatalf("conflicting close = %#v", closeErr)
	}
	if _, _, contentErr := reader.lookup(IPv4(15)); contentErr == nil ||
		contentErr.code != linuxLiveReaderContentCloseRequired {
		t.Fatalf("content after failed close = %#v", contentErr)
	}
	raw = encodeActiveSlot(reader.owned.active)
	if err := reader.files.sidecar.writeAllAt(raw[:], offset); err != nil {
		t.Fatal(err)
	}
	if outcome, closeErr := reader.close(); closeErr != nil || outcome == nil {
		t.Fatalf("retried close = outcome %#v error %#v", outcome, closeErr)
	}
	if outcome, closeErr := reader.close(); closeErr != nil || outcome != nil {
		t.Fatalf("idempotent close = outcome %#v error %#v", outcome, closeErr)
	}
}

func TestLinuxLiveReaderFirstCloseAcceptsExactAlreadyZero(t *testing.T) {
	database := newLinuxLiveReaderTestDatabase(t, 3, false)
	reader, openErr := openLinuxLiveReader[IPv4](database.main)
	if openErr != nil {
		t.Fatal(openErr)
	}
	database.putSlot(t, 1, [sidecarSlotSize]byte{})
	if outcome, closeErr := reader.close(); closeErr != nil || outcome == nil {
		t.Fatalf("already-zero close = outcome %#v error %#v", outcome, closeErr)
	}
}

func TestLinuxLiveReaderCloseReportsBothReplacedPaths(t *testing.T) {
	database := newLinuxLiveReaderTestDatabase(t, 3, false)
	reader, openErr := openLinuxLiveReader[IPv4](database.main)
	if openErr != nil {
		t.Fatal(openErr)
	}
	if err := os.Rename(database.main, filepath.Join(database.directory, "old-main")); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(database.sidecar, filepath.Join(database.directory, "old-sidecar")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(database.main, []byte("replacement"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(database.sidecar, []byte("replacement"), 0o600); err != nil {
		t.Fatal(err)
	}
	outcome, closeErr := reader.close()
	if closeErr != nil || outcome == nil || outcome.mainPath == nil || outcome.sidecarPath == nil {
		t.Fatalf("replaced-path close = outcome %#v error %#v", outcome, closeErr)
	}
}

func TestLinuxLiveReaderReapsDeadWriterTailThenRegisters(t *testing.T) {
	database := newLinuxLiveReaderTestDatabase(t, 4, false)
	writer := activeSlot{
		txnID: 1, processID: uint64(math.MaxInt32), processStart: 1,
		nonce: [16]byte{0x77},
	}
	database.putSlot(t, 0, encodeActiveSlot(writer))
	reader, openErr := openLinuxLiveReader[IPv4](database.main)
	if openErr != nil {
		t.Fatal(openErr)
	}
	info, err := os.Stat(database.main)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != 3*PageSize || database.slot(t, 0) != ([sidecarSlotSize]byte{}) {
		t.Fatalf("dead writer cleanup = size %d slot %x", info.Size(), database.slot(t, 0))
	}
	record, found, contentErr := reader.lookup(IPv4(35))
	if contentErr != nil || !found || record.value != 8 {
		t.Fatalf("retained lookup = record %#v found %t error %#v", record, found, contentErr)
	}
	if _, closeErr := reader.close(); closeErr != nil {
		t.Fatal(closeErr)
	}
}

func TestLinuxLiveReaderDeadWriterFailureReturnsRetriableGuard(t *testing.T) {
	database := newLinuxLiveReaderTestDatabase(t, 4, false)
	writer := activeSlot{
		txnID: 1, processID: uint64(math.MaxInt32), processStart: 1,
		nonce: [16]byte{0x78},
	}
	database.putSlot(t, 0, encodeActiveSlot(writer))
	_, openErr := openLinuxLiveReaderWithHook[IPv4](
		context.Background(), database.main,
		func(stage linuxLiveReaderOpenStage, files *retainedLiveFiles, _ *linuxOwnedReaderSlot) *linuxLiveReaderOpenCause {
			if stage == linuxLiveReaderStageDeadWriterFound {
				if err := files.main.file.Truncate(2 * PageSize); err != nil {
					t.Fatal(err)
				}
			}
			return nil
		},
	)
	requireLinuxLiveOpenError(t, openErr, linuxLiveReaderOpenCleanupRequired, linuxLiveReaderOpenPair)
	guard := openErr.guard
	if _, cleanupErr := guard.retryCleanup(); cleanupErr == nil || cleanupErr.code != linuxLiveCleanupPair {
		t.Fatalf("dead-writer retry = %#v", cleanupErr)
	}
	if err := guard.files.main.file.Truncate(4 * PageSize); err != nil {
		t.Fatal(err)
	}
	if outcome, cleanupErr := guard.close(); cleanupErr != nil || outcome == nil {
		t.Fatalf("dead-writer guard close = outcome %#v error %#v", outcome, cleanupErr)
	}
	info, err := os.Stat(database.main)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != 3*PageSize || database.slot(t, 0) != ([sidecarSlotSize]byte{}) {
		t.Fatalf("retried dead writer cleanup = size %d slot %x", info.Size(), database.slot(t, 0))
	}
}

func TestLinuxLiveReaderRetainedContentSurvivesReplacementWithoutValidation(t *testing.T) {
	database := newLinuxLiveReaderTestDatabase(t, 3, true)
	reader, openErr := openLinuxLiveReader[IPv4](database.main)
	if openErr != nil {
		t.Fatal(openErr)
	}
	if err := os.Rename(database.main, filepath.Join(database.directory, "selected-main")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(database.main, []byte("not the selected database"), 0o600); err != nil {
		t.Fatal(err)
	}
	record, found, contentErr := reader.lookup(IPv4(15))
	if contentErr != nil || !found || record.value != 7 {
		t.Fatalf("retained lookup = record %#v found %t error %#v", record, found, contentErr)
	}
	count, contentErr := reader.countAddresses()
	if contentErr != nil || count != CardinalityFromUint64(22) {
		t.Fatalf("retained count = %v error %#v", count, contentErr)
	}
	outcome, closeErr := reader.close()
	if closeErr != nil || outcome == nil || outcome.mainPath == nil || outcome.sidecarPath != nil {
		t.Fatalf("retained replacement close = outcome %#v error %#v", outcome, closeErr)
	}
}

func TestLinuxLiveReaderHandleAndGuardCheckCreatorPIDFirst(t *testing.T) {
	database := newLinuxLiveReaderTestDatabase(t, 3, false)
	reader, openErr := openLinuxLiveReader[IPv4](database.main)
	if openErr != nil {
		t.Fatal(openErr)
	}
	reader.creatorPID++
	if _, _, contentErr := reader.lookup(IPv4(15)); contentErr == nil ||
		contentErr.code != linuxLiveReaderContentForkedHandle {
		t.Fatalf("foreign reader content = %#v", contentErr)
	}
	if _, closeErr := reader.close(); closeErr == nil || closeErr.code != linuxLiveReaderCloseForkedHandle {
		t.Fatalf("foreign reader close = %#v", closeErr)
	}
	reader.creatorPID = os.Getpid()
	if _, closeErr := reader.close(); closeErr != nil {
		t.Fatal(closeErr)
	}

	database = newLinuxLiveReaderTestDatabase(t, 3, false)
	_, openErr = openLinuxLiveReaderWithHook[IPv4](
		context.Background(), database.main,
		func(stage linuxLiveReaderOpenStage, files *retainedLiveFiles, owned *linuxOwnedReaderSlot) *linuxLiveReaderOpenCause {
			if stage == linuxLiveReaderStageClaimPublished {
				foreign := owned.active
				foreign.nonce = [16]byte{0x88}
				raw := encodeActiveSlot(foreign)
				offset, err := sidecarSlotOffset(owned.header, owned.index)
				if err != nil {
					t.Fatal(err)
				}
				if err := files.sidecar.writeAllAt(raw[:], offset); err != nil {
					t.Fatal(err)
				}
				return injectedLinuxLiveOpenFailure()
			}
			return nil
		},
	)
	guard := openErr.guard
	guard.creatorPID++
	if _, cleanupErr := guard.retryCleanup(); cleanupErr == nil ||
		cleanupErr.code != linuxLiveCleanupForkedHandle {
		t.Fatalf("foreign guard retry = %#v", cleanupErr)
	}
	guard.creatorPID = os.Getpid()
	raw := encodeActiveSlot(guard.owned.active)
	offset, err := sidecarSlotOffset(guard.owned.header, guard.owned.index)
	if err != nil {
		t.Fatal(err)
	}
	if err := guard.files.sidecar.writeAllAt(raw[:], offset); err != nil {
		t.Fatal(err)
	}
	if _, cleanupErr := guard.close(); cleanupErr != nil {
		t.Fatal(cleanupErr)
	}
}

func TestLinuxLiveReaderConcurrentContentHasNoLifecycleLock(t *testing.T) {
	database := newLinuxLiveReaderTestDatabase(t, 3, false)
	reader, openErr := openLinuxLiveReader[IPv4](database.main)
	if openErr != nil {
		t.Fatal(openErr)
	}
	defer reader.close()
	var wait sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for iteration := 0; iteration < 100; iteration++ {
				record, found, contentErr := reader.lookup(IPv4(35))
				if contentErr != nil || !found || record.value != 8 {
					t.Errorf("concurrent lookup = record %#v found %t error %#v", record, found, contentErr)
					return
				}
			}
		}()
	}
	wait.Wait()
}

func TestLinuxLiveReaderDropDoesNotMutateSlotAndCapacityIsExact(t *testing.T) {
	database := newLinuxLiveReaderTestDatabase(t, 3, false)
	reader, openErr := openLinuxLiveReader[IPv4](database.main)
	if openErr != nil {
		t.Fatal(openErr)
	}
	active := database.slot(t, 1)
	if active == ([sidecarSlotSize]byte{}) {
		t.Fatal("established reader did not publish its slot")
	}
	second, secondErr := openLinuxLiveReader[IPv4](database.main)
	if second != nil {
		t.Fatal("capacity-exhausted open returned a reader")
	}
	requireLinuxLiveOpenError(t, secondErr, linuxLiveReaderOpenFailed, linuxLiveReaderOpenSlot)
	var slotErr *linuxReaderSlotError
	if !errors.As(secondErr, &slotErr) || slotErr.code != linuxReaderCapacityExhausted ||
		secondErr.cleanupOutcome != nil || database.slot(t, 1) != active {
		t.Fatalf("capacity result = error %#v slot %x", secondErr, database.slot(t, 1))
	}

	retainedFiles := reader.files
	reader = nil
	runtime.GC()
	if database.slot(t, 1) != active {
		t.Fatalf("dropped reader mutated slot = %x", database.slot(t, 1))
	}
	closeRetainedLiveFiles(retainedFiles)

	database = newLinuxLiveReaderTestDatabase(t, 3, false)
	_, guardOpenErr := openLinuxLiveReaderWithHook[IPv4](
		context.Background(), database.main,
		func(stage linuxLiveReaderOpenStage, files *retainedLiveFiles, owned *linuxOwnedReaderSlot) *linuxLiveReaderOpenCause {
			if stage != linuxLiveReaderStageClaimPublished {
				return nil
			}
			foreign := owned.active
			foreign.nonce = [16]byte{0x99}
			raw := encodeActiveSlot(foreign)
			offset, err := sidecarSlotOffset(owned.header, owned.index)
			if err != nil {
				t.Fatal(err)
			}
			if err := files.sidecar.writeAllAt(raw[:], offset); err != nil {
				t.Fatal(err)
			}
			return injectedLinuxLiveOpenFailure()
		},
	)
	requireLinuxLiveOpenError(t, guardOpenErr, linuxLiveReaderOpenCleanupRequired, linuxLiveReaderOpenSlot)
	guard := guardOpenErr.guard
	if guard == nil {
		t.Fatal("cleanup-required drop test did not return a guard")
	}
	guardSlot := database.slot(t, 1)
	guardFiles := guard.files
	guard = nil
	runtime.GC()
	if database.slot(t, 1) != guardSlot {
		t.Fatalf("dropped cleanup guard mutated slot = %x", database.slot(t, 1))
	}
	closeRetainedLiveFiles(guardFiles)
}

func TestLinuxLiveReaderWarmedLookupDoesNotAllocate(t *testing.T) {
	database := newLinuxLiveReaderTestDatabase(t, 3, false)
	reader, openErr := openLinuxLiveReader[IPv4](database.main)
	if openErr != nil {
		t.Fatal(openErr)
	}
	defer reader.close()
	if _, _, contentErr := reader.lookup(IPv4(15)); contentErr != nil {
		t.Fatal(contentErr)
	}
	if raceEnabled {
		t.Skip("race instrumentation changes allocation accounting")
	}
	allocations := testing.AllocsPerRun(100, func() {
		_, found, contentErr := reader.lookup(IPv4(15))
		if contentErr != nil || !found {
			panic("warmed retained lookup failed")
		}
	})
	if allocations != 0 {
		t.Fatalf("warmed retained lookup allocations = %v, want 0", allocations)
	}
}

func BenchmarkLinuxLiveReaderRetainedLookup(b *testing.B) {
	database := newLinuxLiveReaderTestDatabase(b, 3, false)
	reader, openErr := openLinuxLiveReader[IPv4](database.main)
	if openErr != nil {
		b.Fatal(openErr)
	}
	defer reader.close()
	b.ReportAllocs()
	b.ResetTimer()
	for iteration := 0; iteration < b.N; iteration++ {
		_, found, contentErr := reader.lookup(IPv4(15))
		if contentErr != nil || !found {
			b.Fatalf("lookup = found %t error %#v", found, contentErr)
		}
	}
}
