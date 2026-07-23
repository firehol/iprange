//go:build linux

package exactv4

import (
	"context"
	"os"
	"sync"
)

type linuxLiveWriterOpenCauseCode uint8

const (
	linuxLiveWriterOpenPair linuxLiveWriterOpenCauseCode = iota + 1
	linuxLiveWriterOpenLease
	linuxLiveWriterOpenCancelled
)

type linuxLiveWriterOpenCause struct {
	code   linuxLiveWriterOpenCauseCode
	source error
}

func (e *linuxLiveWriterOpenCause) Error() string {
	return "exact v4 Linux live-writer open cause"
}

func (e *linuxLiveWriterOpenCause) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.source
}

type linuxLiveWriterOpenErrorCode uint8

const (
	linuxLiveWriterOpenFailed linuxLiveWriterOpenErrorCode = iota + 1
	linuxLiveWriterOpenCleanupRequired
)

type linuxLiveWriterOpenError struct {
	code           linuxLiveWriterOpenErrorCode
	cause          *linuxLiveWriterOpenCause
	cleanupOutcome *linuxLiveClaimCleanupOutcome
	cleanup        *linuxLiveCleanupError
	guard          *linuxLiveCleanupGuard
}

func (e *linuxLiveWriterOpenError) Error() string {
	return "exact v4 Linux live-writer open failed"
}

func (e *linuxLiveWriterOpenError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

type linuxLiveWriterCloseErrorCode uint8

const (
	linuxLiveWriterCloseForkedHandle linuxLiveWriterCloseErrorCode = iota + 1
	linuxLiveWriterCloseBarrierHeld
	linuxLiveWriterCloseCleanup
)

type linuxLiveWriterCloseError struct {
	code   linuxLiveWriterCloseErrorCode
	source error
}

func (e *linuxLiveWriterCloseError) Error() string {
	return "exact v4 Linux live-writer close failed"
}

func (e *linuxLiveWriterCloseError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.source
}

type linuxLiveWriterState uint8

const (
	linuxLiveWriterStateOpen linuxLiveWriterState = iota + 1
	linuxLiveWriterStateCleanupOnly
	linuxLiveWriterStateClosed
)

type linuxLiveWriterOpenStage uint8

const (
	linuxLiveWriterStageDeadWriterFound linuxLiveWriterOpenStage = iota + 1
	linuxLiveWriterStageScanComplete
	linuxLiveWriterStageClaimPublished
	linuxLiveWriterStageBeforeTailCleanup
	linuxLiveWriterStageDeadWriterCleared
)

type linuxLiveWriterOpenHook func(
	stage linuxLiveWriterOpenStage,
	files *retainedLiveFiles,
	owned *linuxOwnedWriterLease,
) *linuxLiveWriterOpenCause

type linuxLiveWriterBarrierErrorCode uint8

const (
	linuxLiveWriterBarrierForkedHandle linuxLiveWriterBarrierErrorCode = iota + 1
	linuxLiveWriterBarrierState
	linuxLiveWriterBarrierCancelled
	linuxLiveWriterBarrierLock
	linuxLiveWriterBarrierPair
	linuxLiveWriterBarrierLease
	linuxLiveWriterBarrierUnlockFailed
)

type linuxLiveWriterBarrierError struct {
	code   linuxLiveWriterBarrierErrorCode
	source error
}

func (e *linuxLiveWriterBarrierError) Error() string {
	return "exact v4 Linux live-writer reader barrier failed"
}

func (e *linuxLiveWriterBarrierError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.source
}

type linuxLiveWriterReaderProtection struct {
	selectedTxnID        uint64
	activeReaders        uint32
	registeringReaders   uint32
	oldestReaderTxnValid bool
	oldestReaderTxn      uint64
	reapedReaders        uint32
}

type linuxLiveWriterReaderBarrier struct {
	writer *linuxLiveWriter
	epoch  uint64
}

type linuxLiveWriterBarrierUnlock func(*retainedRegular) *linuxOSError

// linuxLiveWriter is the retained-descriptor writer lifecycle. Transaction
// mutation and commit are deliberately outside this private slice.
type linuxLiveWriter struct {
	operation    sync.Mutex
	files        *retainedLiveFiles
	owned        *linuxOwnedWriterLease
	bootstrap    Bootstrap
	barrier      linuxLiveWriterReaderProtection
	barrierWork  linuxSidecarScanWorkspace
	barrierEpoch uint64
	creatorPID   int
	state        linuxLiveWriterState
	barrierHeld  bool
	barrierValid bool
}

func openLinuxLiveWriter(path string) (*linuxLiveWriter, *linuxLiveWriterOpenError) {
	return openLinuxLiveWriterContext(context.Background(), path)
}

func openLinuxLiveWriterContext(
	ctx context.Context,
	path string,
) (*linuxLiveWriter, *linuxLiveWriterOpenError) {
	return openLinuxLiveWriterWithHookAndIO(ctx, path, nil, defaultLinuxWriterTruncate, defaultLinuxWriterSync)
}

func openLinuxLiveWriterWithHook(
	ctx context.Context,
	path string,
	hook linuxLiveWriterOpenHook,
) (*linuxLiveWriter, *linuxLiveWriterOpenError) {
	return openLinuxLiveWriterWithHookAndIO(ctx, path, hook, defaultLinuxWriterTruncate, defaultLinuxWriterSync)
}

func openLinuxLiveWriterWithHookAndIO(
	ctx context.Context,
	path string,
	hook linuxLiveWriterOpenHook,
	truncate func(*os.File, uint64) error,
	synchronize func(*os.File) error,
) (*linuxLiveWriter, *linuxLiveWriterOpenError) {
	return openLinuxLiveWriterWithHookIOAndDeadCleanup(
		ctx, path, hook, truncate, synchronize, nil,
	)
}

func openLinuxLiveWriterWithHookIOAndDeadCleanup(
	ctx context.Context,
	path string,
	hook linuxLiveWriterOpenHook,
	truncate func(*os.File, uint64) error,
	synchronize func(*os.File) error,
	deadCleanup linuxDeadWriterCleanupAttempt,
) (*linuxLiveWriter, *linuxLiveWriterOpenError) {
	if deadCleanup == nil {
		deadCleanup = func(files *retainedLiveFiles, postClear func()) *linuxLivePairError {
			return files.retryDeadWriterCleanupWithPostClear(postClear)
		}
	}
	files, pairErr := openLockedRetainedLiveFilesContext(ctx, path)
	if pairErr != nil {
		return nil, &linuxLiveWriterOpenError{
			code: linuxLiveWriterOpenFailed, cause: linuxLiveWriterPairOpenCause(pairErr),
		}
	}

	for {
		_, pairErr = files.scanAndReapContext(ctx)
		if pairErr == nil {
			break
		}
		if !linuxLivePairIsDeadWriter(pairErr) {
			return nil, failLinuxLiveWriterOpen(files, nil, linuxLiveWriterPairOpenCause(pairErr), nil)
		}
		if cause := callLinuxLiveWriterOpenHook(
			hook, linuxLiveWriterStageDeadWriterFound, files, nil,
		); cause != nil {
			return nil, failLinuxLiveWriterOpen(files, nil, cause, nil)
		}
		if cause := linuxLiveWriterCancellationCause(ctx); cause != nil {
			return nil, failLinuxLiveWriterOpen(files, nil, cause, nil)
		}
		var postClearCause *linuxLiveWriterOpenCause
		var postClear func()
		if hook != nil {
			postClear = func() {
				postClearCause = callLinuxLiveWriterOpenHook(
					hook, linuxLiveWriterStageDeadWriterCleared, files, nil,
				)
			}
		}
		cleanupErr := deadCleanup(files, postClear)
		if cleanupErr != nil {
			return nil, failLinuxLiveWriterOpen(
				files, nil, linuxLiveWriterPairOpenCause(pairErr),
				&linuxLiveCleanupError{code: linuxLiveCleanupPair, source: cleanupErr},
			)
		}
		if postClearCause != nil {
			return nil, failLinuxLiveWriterOpen(files, nil, postClearCause, nil)
		}
	}

	if cause := linuxLiveWriterCancellationCause(ctx); cause != nil {
		return nil, failLinuxLiveWriterOpen(files, nil, cause, nil)
	}
	if cause := callLinuxLiveWriterOpenHook(hook, linuxLiveWriterStageScanComplete, files, nil); cause != nil {
		return nil, failLinuxLiveWriterOpen(files, nil, cause, nil)
	}

	owned, writerErr := files.claimWriterLease()
	if writerErr != nil {
		return nil, failLinuxLiveWriterOpen(files, nil, &linuxLiveWriterOpenCause{
			code: linuxLiveWriterOpenLease, source: writerErr,
		}, nil)
	}
	if cause := callLinuxLiveWriterOpenHook(hook, linuxLiveWriterStageClaimPublished, files, owned); cause != nil {
		return nil, failLinuxLiveWriterOpen(files, owned, cause, nil)
	}
	if cause := linuxLiveWriterCancellationCause(ctx); cause != nil {
		return nil, failLinuxLiveWriterOpen(files, owned, cause, nil)
	}
	if cause := callLinuxLiveWriterOpenHook(hook, linuxLiveWriterStageBeforeTailCleanup, files, owned); cause != nil {
		return nil, failLinuxLiveWriterOpen(files, owned, cause, nil)
	}

	bootstrap, writerErr := files.prepareWriterForExposureWith(owned, truncate, synchronize)
	if writerErr != nil {
		return nil, failLinuxLiveWriterOpen(files, owned, &linuxLiveWriterOpenCause{
			code: linuxLiveWriterOpenLease, source: writerErr,
		}, nil)
	}
	return &linuxLiveWriter{
		files: files, owned: owned, bootstrap: bootstrap,
		creatorPID: os.Getpid(), state: linuxLiveWriterStateOpen,
	}, nil
}

func (writer *linuxLiveWriter) close() (*linuxLiveClaimCleanupOutcome, *linuxLiveWriterCloseError) {
	return writer.closeWithIO(defaultLinuxWriterTruncate, defaultLinuxWriterSync)
}

func (writer *linuxLiveWriter) closeWithIO(
	truncate func(*os.File, uint64) error,
	synchronize func(*os.File) error,
) (*linuxLiveClaimCleanupOutcome, *linuxLiveWriterCloseError) {
	if writer == nil || os.Getpid() != writer.creatorPID {
		return nil, &linuxLiveWriterCloseError{code: linuxLiveWriterCloseForkedHandle}
	}
	writer.operation.Lock()
	defer writer.operation.Unlock()
	if writer.state == linuxLiveWriterStateClosed {
		return nil, nil
	}
	if writer.barrierHeld {
		return nil, &linuxLiveWriterCloseError{code: linuxLiveWriterCloseBarrierHeld}
	}
	writer.state = linuxLiveWriterStateCleanupOnly
	outcome, writerErr := writer.files.retryWriterLeaseCleanupWith(writer.owned, truncate, synchronize)
	if writerErr != nil {
		return nil, &linuxLiveWriterCloseError{code: linuxLiveWriterCloseCleanup, source: writerErr}
	}
	closeRetainedLiveFiles(writer.files)
	writer.owned = nil
	writer.files = nil
	writer.state = linuxLiveWriterStateClosed
	writer.barrier = linuxLiveWriterReaderProtection{}
	writer.barrierWork = linuxSidecarScanWorkspace{}
	writer.barrierHeld = false
	writer.barrierValid = false
	return &outcome, nil
}

func (writer *linuxLiveWriter) acquireReaderThresholdBarrierContext(
	ctx context.Context,
) (linuxLiveWriterReaderBarrier, *linuxLiveWriterBarrierError) {
	return writer.acquireReaderThresholdBarrierWithObserverContext(ctx, nil)
}

func (writer *linuxLiveWriter) acquireReaderThresholdBarrierWithObserverContext(
	ctx context.Context,
	observe func(activeSlot) posixProcessObservation,
) (linuxLiveWriterReaderBarrier, *linuxLiveWriterBarrierError) {
	if writer == nil || os.Getpid() != writer.creatorPID {
		return linuxLiveWriterReaderBarrier{}, &linuxLiveWriterBarrierError{
			code: linuxLiveWriterBarrierForkedHandle,
		}
	}
	writer.operation.Lock()
	defer writer.operation.Unlock()
	if barrier, barrierErr := writer.readerThresholdBarrierStateLocked(); barrierErr != nil {
		return barrier, barrierErr
	}
	preparedValid := observe == nil
	if preparedValid {
		if barrierErr := writer.prepareReaderThresholdBarrierLocked(ctx); barrierErr != nil {
			return linuxLiveWriterReaderBarrier{}, barrierErr
		}
	}
	return writer.acquirePreparedReaderThresholdBarrierLocked(ctx, observe, preparedValid)
}

func (writer *linuxLiveWriter) readerThresholdBarrierStateLocked() (
	linuxLiveWriterReaderBarrier,
	*linuxLiveWriterBarrierError,
) {
	if writer.barrierHeld {
		return linuxLiveWriterReaderBarrier{writer: writer, epoch: writer.barrierEpoch},
			&linuxLiveWriterBarrierError{code: linuxLiveWriterBarrierState}
	}
	if writer.state != linuxLiveWriterStateOpen || writer.files == nil || writer.owned == nil ||
		writer.files.sidecar.lock != 0 || writer.barrierEpoch == ^uint64(0) {
		return linuxLiveWriterReaderBarrier{}, &linuxLiveWriterBarrierError{
			code: linuxLiveWriterBarrierState,
		}
	}
	return linuxLiveWriterReaderBarrier{}, nil
}

func (writer *linuxLiveWriter) prepareReaderThresholdBarrierLocked(
	ctx context.Context,
) *linuxLiveWriterBarrierError {
	if cancellation := linuxContextCancellation(ctx); cancellation != nil {
		return &linuxLiveWriterBarrierError{
			code: linuxLiveWriterBarrierCancelled, source: cancellation,
		}
	}
	if err := writer.files.sidecar.prepareDeadReaderCandidatesContext(
		ctx, writer.files.header, &writer.barrierWork,
	); err != nil {
		code := linuxLiveWriterBarrierPair
		if err.code == linuxOSCancelled {
			code = linuxLiveWriterBarrierCancelled
		}
		return &linuxLiveWriterBarrierError{code: code, source: err}
	}
	return nil
}

func (writer *linuxLiveWriter) acquirePreparedReaderThresholdBarrierLocked(
	ctx context.Context,
	observe func(activeSlot) posixProcessObservation,
	preparedValid bool,
) (linuxLiveWriterReaderBarrier, *linuxLiveWriterBarrierError) {
	if barrier, barrierErr := writer.readerThresholdBarrierStateLocked(); barrierErr != nil {
		return barrier, barrierErr
	}
	if !preparedValid && observe == nil {
		return linuxLiveWriterReaderBarrier{}, &linuxLiveWriterBarrierError{
			code: linuxLiveWriterBarrierState,
		}
	}
	if err := writer.files.sidecar.acquireLockContext(ctx, linuxLockExclusive); err != nil {
		code := linuxLiveWriterBarrierLock
		if err.code == linuxOSCancelled {
			code = linuxLiveWriterBarrierCancelled
		}
		return linuxLiveWriterReaderBarrier{}, &linuxLiveWriterBarrierError{
			code: code, source: err,
		}
	}
	writer.barrierEpoch++
	writer.barrierHeld = true
	barrier := linuxLiveWriterReaderBarrier{writer: writer, epoch: writer.barrierEpoch}

	before, writerErr := writer.files.verifyOwnedWriter(writer.owned)
	if writerErr != nil {
		return barrier, writer.failReaderThresholdBarrier(
			linuxLiveWriterBarrierLease, writerErr,
		)
	}
	if !sameLinuxWriterGeneration(before, writer.bootstrap) {
		return barrier, writer.failReaderThresholdBarrier(
			linuxLiveWriterBarrierLease,
			&linuxWriterLeaseError{code: linuxWriterGenerationChanged},
		)
	}

	inspection, pairErr := writer.files.scanAndReapOwnedWriterContext(
		ctx, writer.owned, &writer.barrierWork, observe, preparedValid,
	)
	if pairErr != nil {
		code := linuxLiveWriterBarrierPair
		if linuxLivePairIsCancelled(pairErr) {
			code = linuxLiveWriterBarrierCancelled
		}
		return barrier, writer.failReaderThresholdBarrier(
			code, pairErr,
		)
	}
	if cancellation := linuxContextCancellation(ctx); cancellation != nil {
		return barrier, writer.failReaderThresholdBarrier(
			linuxLiveWriterBarrierCancelled, cancellation,
		)
	}
	after, writerErr := writer.files.verifyOwnedWriter(writer.owned)
	if writerErr != nil {
		return barrier, writer.failReaderThresholdBarrier(
			linuxLiveWriterBarrierLease, writerErr,
		)
	}
	if !sameLinuxWriterGeneration(after, writer.bootstrap) ||
		!sameLinuxWriterGeneration(after, before) {
		return barrier, writer.failReaderThresholdBarrier(
			linuxLiveWriterBarrierLease,
			&linuxWriterLeaseError{code: linuxWriterGenerationChanged},
		)
	}

	writer.barrier = linuxLiveWriterReaderProtection{
		selectedTxnID: after.Meta.TxnID,
		activeReaders: inspection.activeReaders, registeringReaders: inspection.registeringReaders,
		oldestReaderTxnValid: inspection.oldestReaderTxnValid,
		oldestReaderTxn:      inspection.oldestReaderTxn,
		reapedReaders:        inspection.reapedReaders,
	}
	writer.barrierValid = true
	return barrier, nil
}

func (writer *linuxLiveWriter) failReaderThresholdBarrier(
	code linuxLiveWriterBarrierErrorCode,
	source error,
) *linuxLiveWriterBarrierError {
	// A post-flock failure keeps explicit barrier authority on the writer. The
	// caller must release or abort so an unlock failure remains observable and
	// retryable instead of being hidden behind the validation failure.
	writer.barrier = linuxLiveWriterReaderProtection{}
	writer.barrierValid = false
	return &linuxLiveWriterBarrierError{code: code, source: source}
}

func (barrier linuxLiveWriterReaderBarrier) readerProtection() (
	linuxLiveWriterReaderProtection,
	*linuxLiveWriterBarrierError,
) {
	writer := barrier.writer
	if writer == nil {
		return linuxLiveWriterReaderProtection{}, &linuxLiveWriterBarrierError{
			code: linuxLiveWriterBarrierState,
		}
	}
	if os.Getpid() != writer.creatorPID {
		return linuxLiveWriterReaderProtection{}, &linuxLiveWriterBarrierError{
			code: linuxLiveWriterBarrierForkedHandle,
		}
	}
	writer.operation.Lock()
	defer writer.operation.Unlock()
	if !writer.barrierHeld || !writer.barrierValid || barrier.epoch != writer.barrierEpoch || writer.files == nil ||
		writer.files.sidecar.lock != linuxLockExclusive {
		return linuxLiveWriterReaderProtection{}, &linuxLiveWriterBarrierError{
			code: linuxLiveWriterBarrierState,
		}
	}
	return writer.barrier, nil
}

func (barrier linuxLiveWriterReaderBarrier) protects(
	targetTxnID uint64,
) (bool, *linuxLiveWriterBarrierError) {
	writer := barrier.writer
	if writer == nil {
		return false, &linuxLiveWriterBarrierError{code: linuxLiveWriterBarrierState}
	}
	if os.Getpid() != writer.creatorPID {
		return false, &linuxLiveWriterBarrierError{code: linuxLiveWriterBarrierForkedHandle}
	}
	writer.operation.Lock()
	defer writer.operation.Unlock()
	if !writer.barrierHeld || !writer.barrierValid || barrier.epoch != writer.barrierEpoch || writer.files == nil ||
		writer.files.sidecar.lock != linuxLockExclusive {
		return false, &linuxLiveWriterBarrierError{code: linuxLiveWriterBarrierState}
	}
	return writer.barrier.registeringReaders != 0 ||
		(writer.barrier.oldestReaderTxnValid && writer.barrier.oldestReaderTxn < targetTxnID), nil
}

func (barrier linuxLiveWriterReaderBarrier) release() *linuxLiveWriterBarrierError {
	return barrier.releaseWith((*retainedRegular).releaseLock)
}

func (barrier linuxLiveWriterReaderBarrier) abort() *linuxLiveWriterBarrierError {
	return barrier.release()
}

func (barrier linuxLiveWriterReaderBarrier) releaseWith(
	unlock linuxLiveWriterBarrierUnlock,
) *linuxLiveWriterBarrierError {
	writer := barrier.writer
	if writer == nil {
		return &linuxLiveWriterBarrierError{code: linuxLiveWriterBarrierState}
	}
	if os.Getpid() != writer.creatorPID {
		return &linuxLiveWriterBarrierError{code: linuxLiveWriterBarrierForkedHandle}
	}
	writer.operation.Lock()
	defer writer.operation.Unlock()
	if writer.files == nil || !writer.barrierHeld || barrier.epoch != writer.barrierEpoch ||
		writer.files.sidecar.lock != linuxLockExclusive || unlock == nil {
		return &linuxLiveWriterBarrierError{code: linuxLiveWriterBarrierState}
	}
	if err := unlock(writer.files.sidecar); err != nil {
		return &linuxLiveWriterBarrierError{code: linuxLiveWriterBarrierUnlockFailed, source: err}
	}
	writer.barrier = linuxLiveWriterReaderProtection{}
	writer.barrierHeld = false
	writer.barrierValid = false
	return nil
}

func callLinuxLiveWriterOpenHook(
	hook linuxLiveWriterOpenHook,
	stage linuxLiveWriterOpenStage,
	files *retainedLiveFiles,
	owned *linuxOwnedWriterLease,
) *linuxLiveWriterOpenCause {
	if hook == nil {
		return nil
	}
	return hook(stage, files, owned)
}

func linuxLiveWriterPairOpenCause(pairErr *linuxLivePairError) *linuxLiveWriterOpenCause {
	if linuxLivePairIsCancelled(pairErr) {
		return &linuxLiveWriterOpenCause{code: linuxLiveWriterOpenCancelled, source: pairErr}
	}
	return &linuxLiveWriterOpenCause{code: linuxLiveWriterOpenPair, source: pairErr}
}

func linuxLiveWriterCancellationCause(ctx context.Context) *linuxLiveWriterOpenCause {
	if linuxContextCancellation(ctx) == nil {
		return nil
	}
	return &linuxLiveWriterOpenCause{code: linuxLiveWriterOpenCancelled, source: ctx.Err()}
}

func failLinuxLiveWriterOpen(
	files *retainedLiveFiles,
	owned *linuxOwnedWriterLease,
	cause *linuxLiveWriterOpenCause,
	knownCleanupErr *linuxLiveCleanupError,
) *linuxLiveWriterOpenError {
	if !linuxLiveCleanupRequired(files, nil, owned) {
		var outcome *linuxLiveClaimCleanupOutcome
		terminalCause := cause
		if knownCleanupErr != nil {
			paths := files.liveCleanupPaths()
			outcome = &paths
			terminalCause = linuxLiveWriterCleanupOpenCause(knownCleanupErr)
		}
		closeRetainedLiveFiles(files)
		return &linuxLiveWriterOpenError{
			code: linuxLiveWriterOpenFailed, cause: terminalCause, cleanupOutcome: outcome,
		}
	}

	var outcome linuxLiveClaimCleanupOutcome
	var cleanupErr *linuxLiveCleanupError
	if knownCleanupErr != nil {
		cleanupErr = knownCleanupErr
	} else {
		outcome, cleanupErr = retryLinuxAnyLiveCleanup(files, nil, owned)
	}
	if cleanupErr == nil {
		closeRetainedLiveFiles(files)
		return &linuxLiveWriterOpenError{
			code: linuxLiveWriterOpenFailed, cause: cause, cleanupOutcome: &outcome,
		}
	}
	return &linuxLiveWriterOpenError{
		code: linuxLiveWriterOpenCleanupRequired, cause: cause, cleanup: cleanupErr,
		guard: &linuxLiveCleanupGuard{
			files: files, ownedWriter: owned, creatorPID: os.Getpid(),
		},
	}
}

func linuxLiveWriterCleanupOpenCause(cleanupErr *linuxLiveCleanupError) *linuxLiveWriterOpenCause {
	switch cleanupErr.code {
	case linuxLiveCleanupForkedHandle:
		return &linuxLiveWriterOpenCause{
			code: linuxLiveWriterOpenLease,
			source: &linuxWriterLeaseError{
				code: linuxWriterOS, source: &linuxOSError{code: linuxOSForkedHandle},
			},
		}
	case linuxLiveCleanupPair:
		return &linuxLiveWriterOpenCause{code: linuxLiveWriterOpenPair, source: cleanupErr.source}
	case linuxLiveCleanupWriter:
		return &linuxLiveWriterOpenCause{code: linuxLiveWriterOpenLease, source: cleanupErr.source}
	default:
		return &linuxLiveWriterOpenCause{code: linuxLiveWriterOpenLease, source: cleanupErr}
	}
}

func defaultLinuxWriterTruncate(file *os.File, length uint64) error {
	if length > uint64(^uint64(0)>>1) {
		return &linuxOSError{code: linuxOSOffsetOverflow}
	}
	return file.Truncate(int64(length))
}

func defaultLinuxWriterSync(file *os.File) error {
	return file.Sync()
}
