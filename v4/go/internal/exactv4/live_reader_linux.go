//go:build linux

package exactv4

import (
	"context"
	"errors"
	"os"
	"sync"
)

type linuxLiveReaderOpenCauseCode uint8

const (
	linuxLiveReaderOpenPair linuxLiveReaderOpenCauseCode = iota + 1
	linuxLiveReaderOpenSlot
	linuxLiveReaderOpenView
	linuxLiveReaderOpenCancelled
)

type linuxLiveReaderOpenCause struct {
	code   linuxLiveReaderOpenCauseCode
	source error
}

func (e *linuxLiveReaderOpenCause) Error() string {
	return "exact v4 Linux live-reader open cause"
}

func (e *linuxLiveReaderOpenCause) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.source
}

type linuxLiveCleanupErrorCode uint8

const (
	linuxLiveCleanupForkedHandle linuxLiveCleanupErrorCode = iota + 1
	linuxLiveCleanupPair
	linuxLiveCleanupSlot
	linuxLiveCleanupWriter
)

type linuxLiveCleanupError struct {
	code   linuxLiveCleanupErrorCode
	source error
}

func (e *linuxLiveCleanupError) Error() string {
	return "exact v4 Linux live cleanup failed"
}

func (e *linuxLiveCleanupError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.source
}

type linuxLiveReaderOpenErrorCode uint8

const (
	linuxLiveReaderOpenFailed linuxLiveReaderOpenErrorCode = iota + 1
	linuxLiveReaderOpenCleanupRequired
)

type linuxLiveReaderOpenError struct {
	code           linuxLiveReaderOpenErrorCode
	cause          *linuxLiveReaderOpenCause
	cleanupOutcome *linuxReaderSlotCleanupOutcome
	cleanup        *linuxLiveCleanupError
	guard          *linuxLiveCleanupGuard
}

func (e *linuxLiveReaderOpenError) Error() string {
	return "exact v4 Linux live-reader open failed"
}

func (e *linuxLiveReaderOpenError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

type linuxLiveReaderContentErrorCode uint8

const (
	linuxLiveReaderContentForkedHandle linuxLiveReaderContentErrorCode = iota + 1
	linuxLiveReaderContentCloseRequired
	linuxLiveReaderContentRead
)

type linuxLiveReaderContentError struct {
	code   linuxLiveReaderContentErrorCode
	source error
}

func (e *linuxLiveReaderContentError) Error() string {
	return "exact v4 Linux live-reader content failed"
}

func (e *linuxLiveReaderContentError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.source
}

type linuxLiveReaderCloseErrorCode uint8

const (
	linuxLiveReaderCloseForkedHandle linuxLiveReaderCloseErrorCode = iota + 1
	linuxLiveReaderCloseCleanup
)

type linuxLiveReaderCloseError struct {
	code   linuxLiveReaderCloseErrorCode
	source error
}

func (e *linuxLiveReaderCloseError) Error() string {
	return "exact v4 Linux live-reader close failed"
}

func (e *linuxLiveReaderCloseError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.source
}

type linuxLiveReaderState uint8

const (
	linuxLiveReaderStateOpen linuxLiveReaderState = iota + 1
	linuxLiveReaderStateCleanupOnly
	linuxLiveReaderStateClosed
)

type linuxLiveReaderOpenStage uint8

const (
	linuxLiveReaderStageDeadWriterFound linuxLiveReaderOpenStage = iota + 1
	linuxLiveReaderStageClaimPublished
	linuxLiveReaderStagePinPublished
	linuxLiveReaderStageBeforeUnlock
	linuxLiveReaderStageViewSetup
	linuxLiveReaderStageBeforeScan
	linuxLiveReaderStageDeadWriterCleared
)

type linuxLiveReaderOpenHook func(
	stage linuxLiveReaderOpenStage,
	files *retainedLiveFiles,
	owned *linuxOwnedReaderSlot,
) *linuxLiveReaderOpenCause

type linuxDeadWriterCleanupAttempt func(
	files *retainedLiveFiles,
	postClear func(),
) *linuxLivePairError

// linuxLiveCleanupGuard retains the descriptor identity and cleanup authority
// needed to retry an open failure. Losing the guard never mutates coordination.
type linuxLiveCleanupGuard struct {
	mu          sync.Mutex
	files       *retainedLiveFiles
	owned       *linuxOwnedReaderSlot
	ownedWriter *linuxOwnedWriterLease
	creatorPID  int
}

func (guard *linuxLiveCleanupGuard) retryCleanup() (*linuxReaderSlotCleanupOutcome, *linuxLiveCleanupError) {
	if guard == nil || os.Getpid() != guard.creatorPID {
		return nil, &linuxLiveCleanupError{code: linuxLiveCleanupForkedHandle}
	}
	guard.mu.Lock()
	defer guard.mu.Unlock()
	if guard.files == nil {
		return nil, nil
	}
	outcome, cleanupErr := retryLinuxAnyLiveCleanup(guard.files, guard.owned, guard.ownedWriter)
	if cleanupErr != nil {
		return nil, cleanupErr
	}
	closeRetainedLiveFiles(guard.files)
	guard.owned = nil
	guard.ownedWriter = nil
	guard.files = nil
	return &outcome, nil
}

func (guard *linuxLiveCleanupGuard) close() (*linuxReaderSlotCleanupOutcome, *linuxLiveCleanupError) {
	return guard.retryCleanup()
}

// linuxLiveReader is the private retained-descriptor reader lifecycle. Content
// methods may run concurrently, but callers must not race them with close.
type linuxLiveReader[K rangeKey[K]] struct {
	files      *retainedLiveFiles
	owned      *linuxOwnedReaderSlot
	source     positionalPageRead
	bootstrap  Bootstrap
	creatorPID int
	state      linuxLiveReaderState
}

func openLinuxLiveReader[K rangeKey[K]](path string) (*linuxLiveReader[K], *linuxLiveReaderOpenError) {
	return openLinuxLiveReaderContext[K](context.Background(), path)
}

func openLinuxLiveReaderContext[K rangeKey[K]](
	ctx context.Context,
	path string,
) (*linuxLiveReader[K], *linuxLiveReaderOpenError) {
	return openLinuxLiveReaderWithHook[K](ctx, path, nil)
}

func openLinuxLiveReaderWithHook[K rangeKey[K]](
	ctx context.Context,
	path string,
	hook linuxLiveReaderOpenHook,
) (*linuxLiveReader[K], *linuxLiveReaderOpenError) {
	return openLinuxLiveReaderWithHookAndDeadCleanup[K](ctx, path, hook, nil)
}

func openLinuxLiveReaderWithHookAndDeadCleanup[K rangeKey[K]](
	ctx context.Context,
	path string,
	hook linuxLiveReaderOpenHook,
	deadCleanup linuxDeadWriterCleanupAttempt,
) (*linuxLiveReader[K], *linuxLiveReaderOpenError) {
	if deadCleanup == nil {
		deadCleanup = func(files *retainedLiveFiles, postClear func()) *linuxLivePairError {
			return files.retryDeadWriterCleanupWithPostClear(postClear)
		}
	}
	files, pairErr := openLockedRetainedLiveFilesContext(ctx, path)
	if pairErr != nil {
		return nil, &linuxLiveReaderOpenError{
			code: linuxLiveReaderOpenFailed, cause: linuxLivePairOpenCause(pairErr),
		}
	}

	for {
		if cause := callLinuxLiveReaderOpenHook(hook, linuxLiveReaderStageBeforeScan, files, nil); cause != nil {
			return nil, failLinuxLiveOpen(files, nil, cause, nil)
		}
		_, pairErr = files.scanAndReapContext(ctx)
		if pairErr == nil {
			break
		}
		if !linuxLivePairIsDeadWriter(pairErr) {
			return nil, failLinuxLiveOpen(files, nil, linuxLivePairOpenCause(pairErr), nil)
		}
		if cause := callLinuxLiveReaderOpenHook(hook, linuxLiveReaderStageDeadWriterFound, files, nil); cause != nil {
			return nil, failLinuxLiveOpen(files, nil, cause, nil)
		}
		if cause := linuxLiveCancellationCause(ctx); cause != nil {
			return nil, failLinuxLiveOpen(files, nil, cause, nil)
		}
		var postClearCause *linuxLiveReaderOpenCause
		var postClear func()
		if hook != nil {
			postClear = func() {
				postClearCause = callLinuxLiveReaderOpenHook(
					hook, linuxLiveReaderStageDeadWriterCleared, files, nil,
				)
			}
		}
		cleanupErr := deadCleanup(files, postClear)
		if cleanupErr != nil {
			return nil, failLinuxLiveOpen(
				files, nil, linuxLivePairOpenCause(pairErr),
				&linuxLiveCleanupError{code: linuxLiveCleanupPair, source: cleanupErr},
			)
		}
		if postClearCause != nil {
			return nil, failLinuxLiveOpen(files, nil, postClearCause, nil)
		}
	}

	if cause := linuxLiveCancellationCause(ctx); cause != nil {
		return nil, failLinuxLiveOpen(files, nil, cause, nil)
	}
	bootstrap := files.lastBootstrap
	source := files.main.pageReadSource()
	if _, viewErr := newRangeTree[K](source, bootstrap); viewErr != nil {
		return nil, failLinuxLiveOpen(files, nil, &linuxLiveReaderOpenCause{
			code: linuxLiveReaderOpenView, source: viewErr,
		}, nil)
	}

	owned, slotErr := files.claimReaderSlot()
	if slotErr != nil {
		return nil, failLinuxLiveOpen(files, nil, &linuxLiveReaderOpenCause{
			code: linuxLiveReaderOpenSlot, source: slotErr,
		}, nil)
	}
	if cause := callLinuxLiveReaderOpenHook(hook, linuxLiveReaderStageClaimPublished, files, owned); cause != nil {
		return nil, failLinuxLiveOpen(files, owned, cause, nil)
	}
	if cause := linuxLiveCancellationCause(ctx); cause != nil {
		return nil, failLinuxLiveOpen(files, owned, cause, nil)
	}

	bootstrap, slotErr = files.pinReaderSlot(owned)
	if slotErr != nil {
		return nil, failLinuxLiveOpen(files, owned, &linuxLiveReaderOpenCause{
			code: linuxLiveReaderOpenSlot, source: slotErr,
		}, nil)
	}
	if cause := callLinuxLiveReaderOpenHook(hook, linuxLiveReaderStagePinPublished, files, owned); cause != nil {
		return nil, failLinuxLiveOpen(files, owned, cause, nil)
	}
	if cause := linuxLiveCancellationCause(ctx); cause != nil {
		return nil, failLinuxLiveOpen(files, owned, cause, nil)
	}

	if cause := callLinuxLiveReaderOpenHook(hook, linuxLiveReaderStageBeforeUnlock, files, owned); cause != nil {
		return nil, failLinuxLiveOpen(files, owned, cause, nil)
	}
	if cause := linuxLiveCancellationCause(ctx); cause != nil {
		return nil, failLinuxLiveOpen(files, owned, cause, nil)
	}
	if slotErr = files.releaseReaderRegistrationLock(owned, bootstrap); slotErr != nil {
		return nil, failLinuxLiveOpen(files, owned, &linuxLiveReaderOpenCause{
			code: linuxLiveReaderOpenSlot, source: slotErr,
		}, nil)
	}
	if cause := linuxLiveCancellationCause(ctx); cause != nil {
		return nil, failLinuxLiveOpen(files, owned, cause, nil)
	}
	if cause := callLinuxLiveReaderOpenHook(hook, linuxLiveReaderStageViewSetup, files, owned); cause != nil {
		return nil, failLinuxLiveOpen(files, owned, cause, nil)
	}
	if cause := linuxLiveCancellationCause(ctx); cause != nil {
		return nil, failLinuxLiveOpen(files, owned, cause, nil)
	}

	return &linuxLiveReader[K]{
		files: files, owned: owned, source: source, bootstrap: bootstrap,
		creatorPID: os.Getpid(), state: linuxLiveReaderStateOpen,
	}, nil
}

func (reader *linuxLiveReader[K]) lookup(
	target K,
) (rangeRecord[K], bool, *linuxLiveReaderContentError) {
	_, contentErr := reader.openFiles()
	if contentErr != nil {
		return rangeRecord[K]{}, false, contentErr
	}
	tree, readErr := newRangeTree[K](reader.source, reader.bootstrap)
	if readErr != nil {
		return rangeRecord[K]{}, false, &linuxLiveReaderContentError{
			code: linuxLiveReaderContentRead, source: readErr,
		}
	}
	record, found, readErr := tree.lookup(target)
	if readErr != nil {
		return rangeRecord[K]{}, false, &linuxLiveReaderContentError{
			code: linuxLiveReaderContentRead, source: readErr,
		}
	}
	return record, found, nil
}

func (reader *linuxLiveReader[K]) countAddresses() (Cardinality129, *linuxLiveReaderContentError) {
	_, contentErr := reader.openFiles()
	if contentErr != nil {
		return Cardinality129{}, contentErr
	}
	tree, readErr := newRangeTree[K](reader.source, reader.bootstrap)
	if readErr != nil {
		return Cardinality129{}, &linuxLiveReaderContentError{
			code: linuxLiveReaderContentRead, source: readErr,
		}
	}
	count, readErr := tree.countAddresses()
	if readErr != nil {
		return Cardinality129{}, &linuxLiveReaderContentError{
			code: linuxLiveReaderContentRead, source: readErr,
		}
	}
	return count, nil
}

func (reader *linuxLiveReader[K]) close() (*linuxReaderSlotCleanupOutcome, *linuxLiveReaderCloseError) {
	if reader == nil || os.Getpid() != reader.creatorPID {
		return nil, &linuxLiveReaderCloseError{code: linuxLiveReaderCloseForkedHandle}
	}
	if reader.state == linuxLiveReaderStateClosed {
		return nil, nil
	}
	reader.state = linuxLiveReaderStateCleanupOnly
	outcome, cleanupErr := reader.files.retryReaderSlotCleanup(reader.owned)
	if cleanupErr != nil {
		return nil, &linuxLiveReaderCloseError{
			code: linuxLiveReaderCloseCleanup, source: cleanupErr,
		}
	}
	closeRetainedLiveFiles(reader.files)
	reader.owned = nil
	reader.files = nil
	reader.state = linuxLiveReaderStateClosed
	return &outcome, nil
}

func (reader *linuxLiveReader[K]) openFiles() (*retainedLiveFiles, *linuxLiveReaderContentError) {
	if reader == nil || os.Getpid() != reader.creatorPID {
		return nil, &linuxLiveReaderContentError{code: linuxLiveReaderContentForkedHandle}
	}
	if reader.state != linuxLiveReaderStateOpen {
		return nil, &linuxLiveReaderContentError{code: linuxLiveReaderContentCloseRequired}
	}
	return reader.files, nil
}

func callLinuxLiveReaderOpenHook(
	hook linuxLiveReaderOpenHook,
	stage linuxLiveReaderOpenStage,
	files *retainedLiveFiles,
	owned *linuxOwnedReaderSlot,
) *linuxLiveReaderOpenCause {
	if hook == nil {
		return nil
	}
	return hook(stage, files, owned)
}

func linuxLivePairOpenCause(pairErr *linuxLivePairError) *linuxLiveReaderOpenCause {
	if linuxLivePairIsCancelled(pairErr) {
		return &linuxLiveReaderOpenCause{code: linuxLiveReaderOpenCancelled, source: pairErr}
	}
	return &linuxLiveReaderOpenCause{code: linuxLiveReaderOpenPair, source: pairErr}
}

func linuxLivePairIsCancelled(pairErr *linuxLivePairError) bool {
	if pairErr == nil {
		return false
	}
	var osErr *linuxOSError
	if errors.As(pairErr, &osErr) && osErr.code == linuxOSCancelled {
		return true
	}
	var scanErr *linuxSidecarScanError
	return errors.As(pairErr, &scanErr) && scanErr.code == linuxSidecarScanCancelled
}

func linuxLivePairIsDeadWriter(pairErr *linuxLivePairError) bool {
	if pairErr == nil || pairErr.code != linuxLivePairScan {
		return false
	}
	var scanErr *linuxSidecarScanError
	return errors.As(pairErr, &scanErr) && scanErr.code == linuxSidecarScanDeadWriter
}

func linuxLiveCancellationCause(ctx context.Context) *linuxLiveReaderOpenCause {
	if linuxContextCancellation(ctx) == nil {
		return nil
	}
	return &linuxLiveReaderOpenCause{code: linuxLiveReaderOpenCancelled, source: ctx.Err()}
}

func failLinuxLiveOpen(
	files *retainedLiveFiles,
	owned *linuxOwnedReaderSlot,
	cause *linuxLiveReaderOpenCause,
	knownCleanupErr *linuxLiveCleanupError,
) *linuxLiveReaderOpenError {
	if !linuxLiveCleanupRequired(files, owned, nil) {
		var outcome *linuxReaderSlotCleanupOutcome
		terminalCause := cause
		if knownCleanupErr != nil {
			paths := files.readerCleanupPaths()
			outcome = &paths
			terminalCause = linuxLiveCleanupOpenCause(knownCleanupErr)
		}
		closeRetainedLiveFiles(files)
		return &linuxLiveReaderOpenError{
			code: linuxLiveReaderOpenFailed, cause: terminalCause, cleanupOutcome: outcome,
		}
	}

	var outcome linuxReaderSlotCleanupOutcome
	var cleanupErr *linuxLiveCleanupError
	if knownCleanupErr != nil {
		cleanupErr = knownCleanupErr
	} else {
		outcome, cleanupErr = retryLinuxLiveCleanup(files, owned)
	}
	if cleanupErr == nil {
		closeRetainedLiveFiles(files)
		return &linuxLiveReaderOpenError{
			code: linuxLiveReaderOpenFailed, cause: cause, cleanupOutcome: &outcome,
		}
	}
	return &linuxLiveReaderOpenError{
		code: linuxLiveReaderOpenCleanupRequired, cause: cause, cleanup: cleanupErr,
		guard: &linuxLiveCleanupGuard{
			files: files, owned: owned, creatorPID: os.Getpid(),
		},
	}
}

func linuxLiveCleanupOpenCause(cleanupErr *linuxLiveCleanupError) *linuxLiveReaderOpenCause {
	switch cleanupErr.code {
	case linuxLiveCleanupForkedHandle:
		return &linuxLiveReaderOpenCause{
			code: linuxLiveReaderOpenSlot,
			source: &linuxReaderSlotError{
				code: linuxReaderOS, source: &linuxOSError{code: linuxOSForkedHandle},
			},
		}
	case linuxLiveCleanupPair:
		return &linuxLiveReaderOpenCause{code: linuxLiveReaderOpenPair, source: cleanupErr.source}
	case linuxLiveCleanupSlot:
		return &linuxLiveReaderOpenCause{code: linuxLiveReaderOpenSlot, source: cleanupErr.source}
	default:
		return &linuxLiveReaderOpenCause{code: linuxLiveReaderOpenSlot, source: cleanupErr}
	}
}

func retryLinuxLiveCleanup(
	files *retainedLiveFiles,
	owned *linuxOwnedReaderSlot,
) (linuxReaderSlotCleanupOutcome, *linuxLiveCleanupError) {
	return retryLinuxAnyLiveCleanup(files, owned, nil)
}

func retryLinuxAnyLiveCleanup(
	files *retainedLiveFiles,
	owned *linuxOwnedReaderSlot,
	ownedWriter *linuxOwnedWriterLease,
) (linuxReaderSlotCleanupOutcome, *linuxLiveCleanupError) {
	if files.sidecar.cleanupAuthority.kind == linuxCleanupDeadWriter || linuxArmedDeadWriterCleanup(files) {
		if pairErr := files.retryDeadWriterCleanup(); pairErr != nil {
			if !linuxLiveCleanupRequired(files, owned, ownedWriter) {
				return files.readerCleanupPaths(), nil
			}
			return linuxReaderSlotCleanupOutcome{}, &linuxLiveCleanupError{
				code: linuxLiveCleanupPair, source: pairErr,
			}
		}
		return files.readerCleanupPaths(), nil
	}
	if files.writerTail != nil || ownedWriter != nil || linuxArmedWriterCleanup(files) {
		outcome, writerErr := files.retryWriterLeaseCleanup(ownedWriter)
		if writerErr != nil {
			return linuxReaderSlotCleanupOutcome{}, &linuxLiveCleanupError{
				code: linuxLiveCleanupWriter, source: writerErr,
			}
		}
		return outcome, nil
	}
	outcome, slotErr := files.retryReaderSlotCleanup(owned)
	if slotErr != nil {
		return linuxReaderSlotCleanupOutcome{}, &linuxLiveCleanupError{
			code: linuxLiveCleanupSlot, source: slotErr,
		}
	}
	return outcome, nil
}

func linuxLiveCleanupRequired(
	files *retainedLiveFiles,
	owned *linuxOwnedReaderSlot,
	ownedWriter *linuxOwnedWriterLease,
) bool {
	if owned != nil || ownedWriter != nil || files.writerTail != nil {
		return true
	}
	authority := files.sidecar.cleanupAuthority
	return !authority.valid() || authority.kind != linuxCleanupNone
}

func linuxArmedWriterCleanup(files *retainedLiveFiles) bool {
	if files == nil || files.sidecar == nil || files.sidecar.cleanupAuthority.kind != linuxCleanupArmed {
		return false
	}
	armed := files.sidecar.cleanupAuthority.armed
	return armed != nil && armed.roleValue() == slotWriter && armed.slotIndexValue() == 0
}

func linuxArmedDeadWriterCleanup(files *retainedLiveFiles) bool {
	if !linuxArmedWriterCleanup(files) {
		return false
	}
	authority := files.sidecar.cleanupAuthority
	return armedDeadWriterObligationMatches(authority.armed, authority.writer)
}

func closeRetainedLiveFiles(files *retainedLiveFiles) {
	if files == nil {
		return
	}
	if files.sidecar != nil && files.sidecar.file != nil {
		_ = files.sidecar.file.Close()
	}
	if files.main != nil && files.main.file != nil {
		_ = files.main.file.Close()
	}
	if files.directory != nil && files.directory.file != nil {
		_ = files.directory.file.Close()
	}
}
