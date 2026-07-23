//go:build linux

package exactv4

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

type linuxOSErrorCode uint8

const (
	linuxOSIO linuxOSErrorCode = iota + 1
	linuxOSInvalidPathComponent
	linuxOSNotDirectory
	linuxOSNotRegular
	linuxOSUnsupportedFilesystem
	linuxOSCrossFilesystem
	linuxOSLinkCountNotOne
	linuxOSPathIdentityMismatch
	linuxOSForkedHandle
	linuxOSLockAlreadyHeld
	linuxOSLockNotHeld
	linuxOSLockBusy
	linuxOSCancelled
	linuxOSOffsetOverflow
	linuxOSRandomFailure
	linuxOSRandomZero
	linuxOSOperationLockRequired
	linuxOSSidecar
	linuxOSSidecarHeaderChanged
	linuxOSSidecarIdentityMismatch
	linuxOSSidecarSizeMismatch
	linuxOSSlotOffsetOverflow
	linuxOSArmedTransition
	linuxOSBootstrap
	linuxOSWriterCleanupRequired
	linuxOSLifetimeLockRequired
	linuxOSSidecarDatabaseMismatch
	linuxOSSidecarMainIdentityMismatch
	linuxOSSidecarBasenameMismatch
	linuxOSSidecarProcessDomainMismatch
	linuxOSBasenameBinding
	linuxOSWriterClearRequiresMainTail
)

type linuxOSError struct {
	code      linuxOSErrorCode
	operation string
	source    error
	value     uint64
}

func (e *linuxOSError) Error() string { return "exact v4 Linux operation failed" }
func (e *linuxOSError) Unwrap() error { return e.source }

type posixIdentity struct {
	device uint64
	inode  uint64
}

func (identity posixIdentity) encode() [32]byte {
	var data [32]byte
	putU64(data[:], 0, identity.device)
	putU64(data[:], 8, identity.inode)
	return data
}

type retainedDirectory struct {
	file       *os.File
	identity   posixIdentity
	creatorPID int
}

func openRetainedParent(path string) (*retainedDirectory, string, *linuxOSError) {
	component := filepath.Base(path)
	if err := validateMainPathComponent(component); err != nil {
		return nil, "", err
	}
	if len(component) > 255-len(".readers") {
		return nil, "", &linuxOSError{code: linuxOSInvalidPathComponent}
	}
	parent := filepath.Dir(path)
	fd, err := unix.Open(parent, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, "", linuxIOError("open parent directory", err)
	}
	file := os.NewFile(uintptr(fd), parent)
	if file == nil {
		_ = unix.Close(fd)
		return nil, "", linuxIOError("retain parent directory", unix.EBADF)
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		_ = file.Close()
		return nil, "", linuxIOError("inspect parent directory", err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR {
		_ = file.Close()
		return nil, "", &linuxOSError{code: linuxOSNotDirectory}
	}
	if err := requireSupportedLinuxFilesystem(fd); err != nil {
		_ = file.Close()
		return nil, "", err
	}
	return &retainedDirectory{
		file: file, identity: identityFromStat(&stat), creatorPID: os.Getpid(),
	}, component, nil
}

func (directory *retainedDirectory) sidecarComponent(mainComponent string) (string, *linuxOSError) {
	if err := directory.checkCreator(); err != nil {
		return "", err
	}
	if err := validateMainPathComponent(mainComponent); err != nil {
		return "", err
	}
	// Every supported Phase-1 Linux filesystem has NAME_MAX=255. Reject before
	// concatenation so an untrusted basename cannot force an oversized copy.
	if len(mainComponent) > 255-len(".readers") {
		return "", &linuxOSError{code: linuxOSInvalidPathComponent}
	}
	component := mainComponent + ".readers"
	if err := validatePathComponent(component); err != nil {
		return "", err
	}
	return component, nil
}

func (directory *retainedDirectory) openRegular(component string, writable bool) (*retainedRegular, *linuxOSError) {
	if err := directory.checkCreator(); err != nil {
		return nil, err
	}
	if err := validatePathComponent(component); err != nil {
		return nil, err
	}
	flags := unix.O_RDONLY | unix.O_NOFOLLOW | unix.O_CLOEXEC | unix.O_NONBLOCK
	if writable {
		flags = unix.O_RDWR | unix.O_NOFOLLOW | unix.O_CLOEXEC | unix.O_NONBLOCK
	}
	fd, err := unix.Openat(int(directory.file.Fd()), component, flags, 0)
	if err != nil {
		return nil, linuxIOError("open retained regular file", err)
	}
	file := os.NewFile(uintptr(fd), component)
	if file == nil {
		_ = unix.Close(fd)
		return nil, linuxIOError("retain regular file", unix.EBADF)
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		_ = file.Close()
		return nil, linuxIOError("inspect retained regular file", err)
	}
	if err := validateRegularStat(&stat); err != nil {
		_ = file.Close()
		return nil, err
	}
	if uint64(stat.Dev) != directory.identity.device {
		_ = file.Close()
		return nil, &linuxOSError{code: linuxOSCrossFilesystem}
	}
	if err := requireSupportedLinuxFilesystem(fd); err != nil {
		_ = file.Close()
		return nil, err
	}
	if stat.Size < 0 {
		_ = file.Close()
		return nil, &linuxOSError{code: linuxOSOffsetOverflow}
	}
	return &retainedRegular{
		file: file, identity: identityFromStat(&stat), length: uint64(stat.Size),
		creatorPID: directory.creatorPID,
	}, nil
}

func (directory *retainedDirectory) verifyPath(component string, retained *retainedRegular) *linuxOSError {
	if err := directory.checkCreator(); err != nil {
		return err
	}
	if err := retained.checkCreator(); err != nil {
		return err
	}
	if err := validatePathComponent(component); err != nil {
		return err
	}
	var descriptor, path unix.Stat_t
	if err := unix.Fstat(int(retained.file.Fd()), &descriptor); err != nil {
		return linuxIOError("recheck retained regular file", err)
	}
	if err := validateRegularStat(&descriptor); err != nil {
		return err
	}
	if identityFromStat(&descriptor) != retained.identity {
		return &linuxOSError{code: linuxOSPathIdentityMismatch}
	}
	if err := unix.Fstatat(int(directory.file.Fd()), component, &path, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return linuxIOError("recheck canonical path", err)
	}
	if err := validateRegularStat(&path); err != nil {
		return err
	}
	if identityFromStat(&path) != retained.identity {
		return &linuxOSError{code: linuxOSPathIdentityMismatch}
	}
	return nil
}

func (directory *retainedDirectory) verifyLivePairBinding(
	mainComponent string,
	main *retainedRegular,
	sidecarComponent string,
	sidecar *retainedRegular,
	bootstrap Bootstrap,
	header sidecarHeader,
) *linuxOSError {
	if err := directory.checkCreator(); err != nil {
		return err
	}
	if main.lock != linuxLockShared {
		return &linuxOSError{code: linuxOSLifetimeLockRequired}
	}
	if err := sidecar.requireExclusiveLock(); err != nil {
		return err
	}
	if err := directory.verifyPath(mainComponent, main); err != nil {
		return err
	}
	if err := directory.verifyPath(sidecarComponent, sidecar); err != nil {
		return err
	}
	return directory.verifyRetainedLivePairBinding(mainComponent, main, sidecar, bootstrap, header)
}

func (directory *retainedDirectory) verifyRetainedLivePairBinding(
	mainComponent string,
	main *retainedRegular,
	sidecar *retainedRegular,
	bootstrap Bootstrap,
	header sidecarHeader,
) *linuxOSError {
	if err := directory.checkCreator(); err != nil {
		return err
	}
	if main.lock != linuxLockShared {
		return &linuxOSError{code: linuxOSLifetimeLockRequired}
	}
	if err := sidecar.requireExclusiveLock(); err != nil {
		return err
	}
	if header.databaseID != bootstrap.Meta.DatabaseID {
		return &linuxOSError{code: linuxOSSidecarDatabaseMismatch}
	}
	if header.mainIdentity != main.identity.encode() {
		return &linuxOSError{code: linuxOSSidecarMainIdentityMismatch}
	}
	if header.identityKind != localIdentityPOSIX || header.sidecarIdentity != sidecar.identity.encode() {
		return &linuxOSError{code: linuxOSSidecarIdentityMismatch}
	}
	if uint64(len(mainComponent)) > math.MaxUint32 {
		return &linuxOSError{code: linuxOSOffsetOverflow}
	}
	// Accepted Linux components are at most NAME_MAX bytes. Copying into a
	// fixed stack buffer preserves the exact bytes without a string allocation.
	var name [255]byte
	if len(mainComponent) > len(name) {
		return &linuxOSError{code: linuxOSOffsetOverflow}
	}
	nameLength := copy(name[:], mainComponent)
	commitment, bindingErr := basenameCommitment(basenamePOSIXBytes, name[:nameLength])
	if bindingErr != nil {
		return &linuxOSError{code: linuxOSBasenameBinding, source: bindingErr}
	}
	if header.basenameEncoding != uint16(basenamePOSIXBytes) ||
		header.basenameLen != uint32(nameLength) || header.basenameCommitment != commitment {
		return &linuxOSError{code: linuxOSSidecarBasenameMismatch}
	}
	domain, err := linuxProcessDomainToken()
	if err != nil {
		return err
	}
	if header.processDomainKind != processDomainLinuxPIDNamespace || header.processDomainToken != domain {
		return &linuxOSError{code: linuxOSSidecarProcessDomainMismatch}
	}
	return nil
}

func (directory *retainedDirectory) checkCreator() *linuxOSError {
	if os.Getpid() != directory.creatorPID {
		return &linuxOSError{code: linuxOSForkedHandle}
	}
	return nil
}

type linuxLockMode uint8

const (
	linuxLockShared linuxLockMode = iota + 1
	linuxLockExclusive
)

type retainedRegular struct {
	file             *os.File
	identity         posixIdentity
	length           uint64
	creatorPID       int
	lock             linuxLockMode
	cleanupAuthority linuxSidecarCleanupAuthority
}

type linuxDeadWriterObligation struct {
	header         sidecarHeader
	raw            [sidecarSlotSize]byte
	active         activeSlot
	proof          deathProof
	bootstrapValid bool
	bootstrap      Bootstrap
	tailValid      bool
	tail           linuxUnpublishedMainTail
}

type linuxUnpublishedMainTail struct {
	mainIdentity         posixIdentity
	databaseID           [16]byte
	transactionID        uint64
	commitNonce          [16]byte
	committedLength      uint64
	observedEndExclusive uint64
}

type linuxSidecarCleanupAuthorityKind uint8

const (
	linuxCleanupNone linuxSidecarCleanupAuthorityKind = iota
	linuxCleanupArmed
	linuxCleanupDeadWriter
)

type linuxSidecarCleanupAuthority struct {
	kind   linuxSidecarCleanupAuthorityKind
	armed  *armedSlotTransition
	writer linuxDeadWriterObligation
}

func (authority linuxSidecarCleanupAuthority) valid() bool {
	switch authority.kind {
	case linuxCleanupNone:
		return authority.armed == nil && authority.writer == (linuxDeadWriterObligation{})
	case linuxCleanupArmed:
		if authority.armed == nil || !authority.armed.isArmed() {
			return false
		}
		if authority.writer == (linuxDeadWriterObligation{}) {
			return true
		}
		return armedDeadWriterObligationMatches(authority.armed, authority.writer)
	case linuxCleanupDeadWriter:
		return authority.armed == nil && authority.writer != (linuxDeadWriterObligation{})
	default:
		return false
	}
}

func armedDeadWriterObligationMatches(
	armed *armedSlotTransition,
	dead linuxDeadWriterObligation,
) bool {
	if armed == nil || dead == (linuxDeadWriterObligation{}) || !dead.bootstrapValid {
		return false
	}
	source := armed.sourceValue()
	return armed.headerValue() == dead.header && armed.roleValue() == slotWriter &&
		armed.slotIndexValue() == 0 && armed.kindValue() == slotTransitionClear &&
		source.kind == slotTransitionSourceProvenDeadActive &&
		source.active == dead.active && source.proof == dead.proof
}

func (retained *retainedRegular) acquireLock(mode linuxLockMode, nonblocking bool) *linuxOSError {
	if err := retained.checkCreator(); err != nil {
		return err
	}
	if retained.lock != 0 {
		return &linuxOSError{code: linuxOSLockAlreadyHeld}
	}
	how := unix.LOCK_SH
	if mode == linuxLockExclusive {
		how = unix.LOCK_EX
	}
	if nonblocking {
		how |= unix.LOCK_NB
	}
	for {
		err := unix.Flock(int(retained.file.Fd()), how)
		if err == nil {
			retained.lock = mode
			return nil
		}
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if nonblocking && errors.Is(err, unix.EWOULDBLOCK) {
			return &linuxOSError{code: linuxOSLockBusy}
		}
		return linuxIOError("acquire flock", err)
	}
}

func (retained *retainedRegular) acquireLockContext(ctx context.Context, mode linuxLockMode) *linuxOSError {
	if err := retained.checkCreator(); err != nil {
		return err
	}
	if retained.lock != 0 {
		return &linuxOSError{code: linuxOSLockAlreadyHeld}
	}
	how := unix.LOCK_SH | unix.LOCK_NB
	if mode == linuxLockExclusive {
		how = unix.LOCK_EX | unix.LOCK_NB
	}
	var retry *time.Timer
	defer func() {
		if retry != nil {
			retry.Stop()
		}
	}()
	for {
		if err := linuxContextCancellation(ctx); err != nil {
			return err
		}
		err := unix.Flock(int(retained.file.Fd()), how)
		if err == nil {
			retained.lock = mode
			return nil
		}
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if !errors.Is(err, unix.EWOULDBLOCK) {
			return linuxIOError("acquire interruptible flock", err)
		}
		if err := linuxContextCancellation(ctx); err != nil {
			return err
		}
		if retry == nil {
			retry = time.NewTimer(time.Millisecond)
		} else {
			retry.Reset(time.Millisecond)
		}
		select {
		case <-ctx.Done():
			return &linuxOSError{code: linuxOSCancelled, source: ctx.Err()}
		case <-retry.C:
		}
	}
}

func (retained *retainedRegular) releaseLock() *linuxOSError {
	if err := retained.checkCreator(); err != nil {
		return err
	}
	if retained.lock == 0 {
		return &linuxOSError{code: linuxOSLockNotHeld}
	}
	if !retained.cleanupAuthority.valid() {
		return &linuxOSError{code: linuxOSArmedTransition}
	}
	if retained.cleanupAuthority.kind == linuxCleanupArmed {
		return &linuxOSError{code: linuxOSArmedTransition}
	}
	if retained.cleanupAuthority.kind == linuxCleanupDeadWriter {
		return &linuxOSError{code: linuxOSWriterCleanupRequired}
	}
	for {
		err := unix.Flock(int(retained.file.Fd()), unix.LOCK_UN)
		if err == nil {
			retained.lock = 0
			return nil
		}
		if errors.Is(err, unix.EINTR) {
			continue
		}
		return linuxIOError("release flock", err)
	}
}

func (retained *retainedRegular) readExactAt(data []byte, offset uint64) *linuxOSError {
	if err := retained.checkCreator(); err != nil {
		return err
	}
	for len(data) != 0 {
		if offset > math.MaxInt64 {
			return &linuxOSError{code: linuxOSOffsetOverflow}
		}
		count, err := retained.file.ReadAt(data, int64(offset))
		if count != 0 {
			offset += uint64(count)
			data = data[count:]
		}
		if err != nil && !errors.Is(err, io.EOF) {
			return linuxIOError("positional read", err)
		}
		if count == 0 {
			return linuxIOError("positional read", io.ErrUnexpectedEOF)
		}
	}
	return nil
}

func (retained *retainedRegular) pageReadSource() positionalPageRead {
	return newFilePageRead(retained.file, retained.creatorPID)
}

func (retained *retainedRegular) pinnedPageSource(bootstrap Bootstrap) (pinnedPageSource, *pageSourceError) {
	return newPinnedPageSource(retained.pageReadSource(), bootstrap)
}

func (retained *retainedRegular) readMainBootstrap(mode OpenMode) (Bootstrap, *linuxOSError) {
	if err := retained.checkCreator(); err != nil {
		return Bootstrap{}, err
	}
	var stat unix.Stat_t
	if err := unix.Fstat(int(retained.file.Fd()), &stat); err != nil {
		return Bootstrap{}, linuxIOError("inspect retained main file", err)
	}
	if err := validateRegularStat(&stat); err != nil {
		return Bootstrap{}, err
	}
	if identityFromStat(&stat) != retained.identity {
		return Bootstrap{}, &linuxOSError{code: linuxOSPathIdentityMismatch}
	}
	if stat.Size < 0 {
		return Bootstrap{}, &linuxOSError{code: linuxOSOffsetOverflow}
	}

	var pages [2 * PageSize]byte
	if err := retained.readExactAt(pages[:], 0); err != nil {
		return Bootstrap{}, err
	}
	page0 := (*[PageSize]byte)(pages[:PageSize])
	page1 := (*[PageSize]byte)(pages[PageSize:])
	bootstrap, err := openMetaPages(page0, page1, uint64(stat.Size), mode)
	if err != nil {
		return Bootstrap{}, &linuxOSError{code: linuxOSBootstrap, source: err}
	}
	return bootstrap, nil
}

func (retained *retainedRegular) writeAllAt(data []byte, offset uint64) *linuxOSError {
	if err := retained.checkCreator(); err != nil {
		return err
	}
	for len(data) != 0 {
		if offset > math.MaxInt64 {
			return &linuxOSError{code: linuxOSOffsetOverflow}
		}
		count, err := retained.file.WriteAt(data, int64(offset))
		if count != 0 {
			offset += uint64(count)
			data = data[count:]
		}
		if err != nil {
			return linuxIOError("positional write", err)
		}
		if count == 0 {
			return linuxIOError("positional write", io.ErrShortWrite)
		}
	}
	return nil
}

func (retained *retainedRegular) readReadySidecarHeader(expected *sidecarHeader) (sidecarHeader, *linuxOSError) {
	if err := retained.requireExclusiveLock(); err != nil {
		return sidecarHeader{}, err
	}
	var stat unix.Stat_t
	if err := unix.Fstat(int(retained.file.Fd()), &stat); err != nil {
		return sidecarHeader{}, linuxIOError("inspect retained sidecar", err)
	}
	if err := validateRegularStat(&stat); err != nil {
		return sidecarHeader{}, err
	}
	if identityFromStat(&stat) != retained.identity {
		return sidecarHeader{}, &linuxOSError{code: linuxOSSidecarIdentityMismatch}
	}
	var data [headerRegionSize]byte
	if err := retained.readExactAt(data[:], 0); err != nil {
		return sidecarHeader{}, err
	}
	header, sidecarErr := selectSidecarHeader(data[:])
	if sidecarErr != nil {
		return sidecarHeader{}, &linuxOSError{code: linuxOSSidecar, source: sidecarErr}
	}
	if header.state != sidecarReady {
		return sidecarHeader{}, &linuxOSError{
			code: linuxOSSidecar, source: &sidecarError{code: sidecarErrNotReady, state: header.state},
		}
	}
	expectedSize, ok := header.exactFileSize()
	if !ok || stat.Size < 0 {
		return sidecarHeader{}, &linuxOSError{code: linuxOSSlotOffsetOverflow}
	}
	actualSize := uint64(stat.Size)
	if actualSize != expectedSize {
		return sidecarHeader{}, &linuxOSError{
			code: linuxOSSidecarSizeMismatch, value: actualSize,
		}
	}
	if header.identityKind != localIdentityPOSIX || header.sidecarIdentity != retained.identity.encode() {
		return sidecarHeader{}, &linuxOSError{code: linuxOSSidecarIdentityMismatch}
	}
	if expected != nil && *expected != header {
		return sidecarHeader{}, &linuxOSError{code: linuxOSSidecarHeaderChanged}
	}
	return header, nil
}

func (retained *retainedRegular) readSidecarSlot(expected sidecarHeader, index uint32) ([sidecarSlotSize]byte, *linuxOSError) {
	header, err := retained.readReadySidecarHeader(&expected)
	if err != nil {
		return [sidecarSlotSize]byte{}, err
	}
	return retained.readSidecarSlotAfterHeader(header, index)
}

func (retained *retainedRegular) readSidecarSlotAfterHeader(
	header sidecarHeader,
	index uint32,
) ([sidecarSlotSize]byte, *linuxOSError) {
	if err := retained.requireExclusiveLock(); err != nil {
		return [sidecarSlotSize]byte{}, err
	}
	offset, err := sidecarSlotOffset(header, index)
	if err != nil {
		return [sidecarSlotSize]byte{}, err
	}
	var slot [sidecarSlotSize]byte
	if err := retained.readExactAt(slot[:], offset); err != nil {
		return [sidecarSlotSize]byte{}, err
	}
	return slot, nil
}

func (retained *retainedRegular) executeSidecarSlotTransition(
	prepared *preparedSlotTransition,
	host slotHostLimits,
) *slotExecutionError {
	if prepared == nil {
		return &slotExecutionError{transition: &slotTransitionError{code: slotTransitionErrSourceMismatch}}
	}
	if prepared.role == slotWriter && prepared.kind == slotTransitionClear {
		return &slotExecutionError{storage: &linuxOSError{code: linuxOSWriterClearRequiresMainTail}}
	}
	return retained.executeSidecarSlotTransitionAfterTail(prepared, host)
}

func (retained *retainedRegular) executeSidecarSlotTransitionAfterTail(
	prepared *preparedSlotTransition,
	host slotHostLimits,
) *slotExecutionError {
	if err := retained.requireExclusiveLock(); err != nil {
		return &slotExecutionError{storage: err}
	}
	if !retained.cleanupAuthority.valid() {
		return &slotExecutionError{transition: &slotTransitionError{code: slotTransitionErrProvenanceOccupied}}
	}
	switch retained.cleanupAuthority.kind {
	case linuxCleanupArmed:
		return &slotExecutionError{transition: &slotTransitionError{code: slotTransitionErrProvenanceOccupied}}
	case linuxCleanupDeadWriter:
		return &slotExecutionError{storage: &linuxOSError{code: linuxOSWriterCleanupRequired}}
	}
	header, err := retained.readReadySidecarHeader(ptrSidecarHeader(prepared.headerValue()))
	if err != nil {
		return &slotExecutionError{storage: err}
	}
	offset, err := sidecarSlotOffset(header, prepared.slotIndexValue())
	if err != nil {
		return &slotExecutionError{storage: err}
	}
	var current [sidecarSlotSize]byte
	if err := retained.readExactAt(current[:], offset); err != nil {
		return &slotExecutionError{storage: err}
	}
	if transitionErr := prepared.confirmSource(&current, host); transitionErr != nil {
		return &slotExecutionError{transition: transitionErr}
	}
	return retained.executePreconfirmedSidecarSlotTransition(prepared, offset, false)
}

func (retained *retainedRegular) executePreconfirmedSidecarSlotTransition(
	prepared *preparedSlotTransition,
	offset uint64,
	replaceDeadWriter bool,
) *slotExecutionError {
	if prepared == nil {
		return &slotExecutionError{transition: &slotTransitionError{code: slotTransitionErrSourceMismatch}}
	}
	if _, err := retained.readReadySidecarHeader(ptrSidecarHeader(prepared.headerValue())); err != nil {
		return &slotExecutionError{storage: err}
	}
	authorityMatches := retained.cleanupAuthority.kind == linuxCleanupNone && !replaceDeadWriter
	var dead linuxDeadWriterObligation
	if retained.cleanupAuthority.kind == linuxCleanupDeadWriter && replaceDeadWriter &&
		prepared.role == slotWriter && prepared.kind == slotTransitionClear &&
		retained.cleanupAuthority.writer.bootstrapValid {
		authorityMatches = true
		dead = retained.cleanupAuthority.writer
	}
	if !retained.cleanupAuthority.valid() || !authorityMatches {
		return &slotExecutionError{transition: &slotTransitionError{code: slotTransitionErrProvenanceOccupied}}
	}
	armed, transitionErr := prepared.arm()
	if transitionErr != nil {
		return &slotExecutionError{transition: transitionErr}
	}
	retained.cleanupAuthority = linuxSidecarCleanupAuthority{
		kind: linuxCleanupArmed, armed: armed, writer: dead,
	}
	executionErr := armed.executeArmed(
		func(relative int, data []byte) error {
			if err := retained.writeAllAt(data, offset+uint64(relative)); err != nil {
				return err
			}
			return nil
		},
		func(observed *[sidecarSlotSize]byte) error {
			if err := retained.readExactAt(observed[:], offset); err != nil {
				return err
			}
			return nil
		},
	)
	if executionErr == nil {
		retained.cleanupAuthority = linuxSidecarCleanupAuthority{}
	}
	return executionErr
}

func (retained *retainedRegular) retrySidecarSlotCleanup(
	host slotHostLimits,
) (cleanupDisposition, *slotExecutionError) {
	if err := retained.requireExclusiveLock(); err != nil {
		return 0, &slotExecutionError{storage: err}
	}
	if !retained.cleanupAuthority.valid() {
		return 0, &slotExecutionError{transition: &slotTransitionError{code: slotTransitionErrProvenanceOccupied}}
	}
	if retained.cleanupAuthority.kind == linuxCleanupDeadWriter {
		return 0, &slotExecutionError{storage: &linuxOSError{code: linuxOSWriterCleanupRequired}}
	}
	if retained.cleanupAuthority.kind != linuxCleanupArmed {
		return 0, &slotExecutionError{transition: &slotTransitionError{code: slotTransitionErrNotArmed}}
	}
	armed := retained.cleanupAuthority.armed
	header, err := retained.readReadySidecarHeader(ptrSidecarHeader(armed.headerValue()))
	if err != nil {
		return 0, &slotExecutionError{storage: err}
	}
	offset, err := sidecarSlotOffset(header, armed.slotIndexValue())
	if err != nil {
		return 0, &slotExecutionError{storage: err}
	}
	disposition, executionErr := armed.retryCleanup(
		host,
		func(relative int, data []byte) error {
			if err := retained.writeAllAt(data, offset+uint64(relative)); err != nil {
				return err
			}
			return nil
		},
		func(observed *[sidecarSlotSize]byte) error {
			if err := retained.readExactAt(observed[:], offset); err != nil {
				return err
			}
			return nil
		},
	)
	if executionErr == nil {
		retained.cleanupAuthority = linuxSidecarCleanupAuthority{}
	}
	return disposition, executionErr
}

type linuxReadySidecarScan struct {
	header               sidecarHeader
	writerActive         bool
	writer               activeSlot
	activeReaders        uint32
	registeringReaders   uint32
	oldestReaderTxnValid bool
	oldestReaderTxn      uint64
	lowestFreeSlotValid  bool
	lowestFreeSlot       uint32
	reapedReaders        uint32
}

type linuxSidecarScanWorkspace struct {
	slot         [sidecarSlotSize]byte
	process      linuxProcessObservationWorkspace
	candidates   []linuxPreparedDeadReader
	observations map[uint64]posixProcessObservation
}

type linuxPreparedDeadReader struct {
	index  uint32
	active activeSlot
	proof  deathProof
}

type linuxSidecarScanErrorCode uint8

const (
	linuxSidecarScanDeadWriter linuxSidecarScanErrorCode = iota + 1
	linuxSidecarScanCancelled
)

type linuxSidecarScanError struct {
	code   linuxSidecarScanErrorCode
	active activeSlot
	proof  deathProof
}

func (e *linuxSidecarScanError) Error() string { return "exact v4 Linux sidecar scan failed" }

func (retained *retainedRegular) prepareDeadReaderCandidatesContext(
	ctx context.Context,
	header sidecarHeader,
	workspace *linuxSidecarScanWorkspace,
) *linuxOSError {
	if workspace == nil {
		return &linuxOSError{code: linuxOSIO}
	}
	if err := retained.checkCreator(); err != nil {
		return err
	}
	if retained.lock != 0 {
		return &linuxOSError{code: linuxOSLockAlreadyHeld}
	}
	if uint64(cap(workspace.candidates)) > uint64(header.capacity) {
		workspace.candidates = nil
		workspace.observations = nil
	}
	workspace.candidates = workspace.candidates[:0]
	if workspace.observations == nil {
		workspace.observations = make(map[uint64]posixProcessObservation)
	} else {
		clear(workspace.observations)
	}
	defer clear(workspace.observations)
	host := linuxSlotHostLimits()
	for rawIndex := uint64(1); rawIndex <= uint64(header.capacity); rawIndex++ {
		if cancellation := linuxContextCancellation(ctx); cancellation != nil {
			workspace.candidates = workspace.candidates[:0]
			return cancellation
		}
		index := uint32(rawIndex)
		stable, scanErr := retained.readDecodedSidecarSlotInto(
			header, index, slotReader, host, &workspace.slot,
		)
		if scanErr != nil || !stable.active {
			continue
		}
		if !appendLinuxPreparedDeadReader(workspace, linuxPreparedDeadReader{
			index: index, active: stable.claim,
		}, header.capacity) {
			break
		}
	}
	// Every logical owner snapshot precedes every process observation. This is
	// required before reusing an observation for multiple slots with the same
	// PID: a PID could otherwise be recycled between a cached observation and a
	// later slot read.
	prepared := workspace.candidates[:0]
	for _, candidate := range workspace.candidates {
		if cancellation := linuxContextCancellation(ctx); cancellation != nil {
			workspace.candidates = workspace.candidates[:0]
			return cancellation
		}
		observation, found := workspace.observations[candidate.active.processID]
		if !found {
			observation = observeLinuxProcessWithWorkspace(candidate.active, &workspace.process)
			workspace.observations[candidate.active.processID] = observation
		}
		proof, dead := classifyPOSIXDeath(candidate.active, observation)
		if !dead || !validSlotDeathProof(header, candidate.active, proof) {
			continue
		}
		candidate.proof = proof
		prepared = append(prepared, candidate)
	}
	workspace.candidates = prepared
	return nil
}

func appendLinuxPreparedDeadReader(
	workspace *linuxSidecarScanWorkspace,
	candidate linuxPreparedDeadReader,
	capacity uint32,
) bool {
	if uint64(len(workspace.candidates)) >= uint64(capacity) {
		return false
	}
	if len(workspace.candidates) == cap(workspace.candidates) {
		newCapacity := 4
		if cap(workspace.candidates) != 0 {
			newCapacity = cap(workspace.candidates) * 2
		}
		maxInt := int(^uint(0) >> 1)
		if uint64(newCapacity) > uint64(capacity) || newCapacity < 0 {
			if uint64(capacity) > uint64(maxInt) {
				newCapacity = maxInt
			} else {
				newCapacity = int(capacity)
			}
		}
		if newCapacity <= cap(workspace.candidates) {
			return false
		}
		grown := make([]linuxPreparedDeadReader, len(workspace.candidates), newCapacity)
		copy(grown, workspace.candidates)
		workspace.candidates = grown
	}
	workspace.candidates = append(workspace.candidates, candidate)
	return true
}

// scanAndReapReadySidecar keeps descriptor identity, the exclusive operation
// lock, and any armed transition provenance on retained for the whole scan.
// Proven-dead writers remain descriptor-owned for the separate main-tail
// cleanup protocol; this helper only clears proven-dead reader slots.
func (retained *retainedRegular) scanAndReapReadySidecar(
	expectedHeader sidecarHeader,
	selectedTxn uint64,
) (linuxReadySidecarScan, error) {
	return retained.scanAndReapReadySidecarInternal(
		context.Background(), expectedHeader, selectedTxn, nil, nil, nil, nil, false,
	)
}

func (retained *retainedRegular) scanAndReapReadySidecarWithObserver(
	expectedHeader sidecarHeader,
	selectedTxn uint64,
	observe func(activeSlot) posixProcessObservation,
) (linuxReadySidecarScan, error) {
	return retained.scanAndReapReadySidecarWithObserverContext(
		context.Background(), expectedHeader, selectedTxn, observe,
	)
}

func (retained *retainedRegular) scanAndReapReadySidecarWithObserverContext(
	ctx context.Context,
	expectedHeader sidecarHeader,
	selectedTxn uint64,
	observe func(activeSlot) posixProcessObservation,
) (linuxReadySidecarScan, error) {
	return retained.scanAndReapReadySidecarInternal(
		ctx, expectedHeader, selectedTxn, observe, nil, nil, nil, false,
	)
}

func (retained *retainedRegular) scanAndReapReadySidecarOwnedWriterContext(
	ctx context.Context,
	expectedHeader sidecarHeader,
	selectedTxn uint64,
	ownedWriter *activeSlot,
	workspace *linuxSidecarScanWorkspace,
	observe func(activeSlot) posixProcessObservation,
	prepared []linuxPreparedDeadReader,
	preparedValid bool,
) (linuxReadySidecarScan, error) {
	return retained.scanAndReapReadySidecarInternal(
		ctx, expectedHeader, selectedTxn, observe, ownedWriter, workspace, prepared, preparedValid,
	)
}

func (retained *retainedRegular) scanAndReapReadySidecarInternal(
	ctx context.Context,
	expectedHeader sidecarHeader,
	selectedTxn uint64,
	observe func(activeSlot) posixProcessObservation,
	ownedWriter *activeSlot,
	workspace *linuxSidecarScanWorkspace,
	prepared []linuxPreparedDeadReader,
	preparedValid bool,
) (linuxReadySidecarScan, error) {
	if err := retained.requireExclusiveLock(); err != nil {
		return linuxReadySidecarScan{}, err
	}
	if !retained.cleanupAuthority.valid() || retained.cleanupAuthority.kind == linuxCleanupArmed {
		return linuxReadySidecarScan{}, &slotExecutionError{
			transition: &slotTransitionError{code: slotTransitionErrProvenanceOccupied},
		}
	}
	if retained.cleanupAuthority.kind == linuxCleanupDeadWriter {
		return linuxReadySidecarScan{}, &linuxOSError{code: linuxOSWriterCleanupRequired}
	}
	if selectedTxn == 0 {
		return linuxReadySidecarScan{}, &readySidecarError{code: readySidecarErrSelectedTransactionZero}
	}
	header, err := retained.readReadySidecarHeader(&expectedHeader)
	if err != nil {
		return linuxReadySidecarScan{}, err
	}
	if err := requireCurrentLinuxPIDDomain(header); err != nil {
		return linuxReadySidecarScan{}, err
	}

	host := linuxSlotHostLimits()
	var localWorkspace linuxSidecarScanWorkspace
	if workspace == nil {
		workspace = &localWorkspace
	}
	// Complete structural validation is read-only. No owner is observed or
	// reaped until every explicitly sized slot has decoded successfully.
	for rawIndex := uint64(0); rawIndex <= uint64(header.capacity); rawIndex++ {
		if linuxContextCancellation(ctx) != nil {
			return linuxReadySidecarScan{}, &linuxSidecarScanError{code: linuxSidecarScanCancelled}
		}
		index := uint32(rawIndex)
		role := slotReader
		if index == 0 {
			role = slotWriter
		}
		if _, scanErr := retained.readDecodedSidecarSlotInto(
			header, index, role, host, &workspace.slot,
		); scanErr != nil {
			return linuxReadySidecarScan{}, scanErr
		}
	}
	header, err = retained.readReadySidecarHeader(&header)
	if err != nil {
		return linuxReadySidecarScan{}, err
	}
	if err := requireCurrentLinuxPIDDomain(header); err != nil {
		return linuxReadySidecarScan{}, err
	}

	var reapedReaders uint32
	preparedIndex := 0
	// The second pass performs liveness classification and exact proven-dead
	// reader clears only after the complete structural pass succeeded.
	for rawIndex := uint64(0); rawIndex <= uint64(header.capacity); rawIndex++ {
		if linuxContextCancellation(ctx) != nil {
			return linuxReadySidecarScan{}, &linuxSidecarScanError{code: linuxSidecarScanCancelled}
		}
		index := uint32(rawIndex)
		role := slotReader
		if index == 0 {
			role = slotWriter
		}
		stable, scanErr := retained.readDecodedSidecarSlotInto(
			header, index, role, host, &workspace.slot,
		)
		if scanErr != nil {
			return linuxReadySidecarScan{}, scanErr
		}
		if !stable.active {
			continue
		}
		for preparedIndex < len(prepared) && prepared[preparedIndex].index < index {
			preparedIndex++
		}
		var proof deathProof
		dead := false
		if index == 0 && ownedWriter != nil && stable.claim == *ownedWriter {
			observation := posixProcessObservation{
				kind: posixProcessExists, currentStart: stable.claim.processStart,
			}
			proof, dead = classifyPOSIXDeath(stable.claim, observation)
		} else if preparedValid && preparedIndex < len(prepared) &&
			prepared[preparedIndex].index == index && prepared[preparedIndex].active == stable.claim {
			proof, dead = prepared[preparedIndex].proof, true
		} else if preparedValid {
			proof, dead = classifyPOSIXDeath(stable.claim, observeLinuxProcessWithoutStart(stable.claim))
		} else if observe == nil {
			proof, dead = classifyPOSIXDeath(
				stable.claim, observeLinuxProcessWithWorkspace(stable.claim, &workspace.process),
			)
		} else {
			proof, dead = classifyPOSIXDeath(stable.claim, observe(stable.claim))
		}
		if !dead {
			continue
		}
		if !validSlotDeathProof(header, stable.claim, proof) {
			return linuxReadySidecarScan{}, &slotTransitionError{code: slotTransitionErrInvalidDeathProof}
		}
		if linuxContextCancellation(ctx) != nil {
			return linuxReadySidecarScan{}, &linuxSidecarScanError{code: linuxSidecarScanCancelled}
		}
		if index == 0 {
			writer := linuxDeadWriterObligation{
				header: header, raw: workspace.slot, active: stable.claim, proof: proof,
			}
			retained.cleanupAuthority = linuxSidecarCleanupAuthority{
				kind: linuxCleanupDeadWriter, writer: writer,
			}
			return linuxReadySidecarScan{}, &linuxSidecarScanError{
				code: linuxSidecarScanDeadWriter, active: stable.claim, proof: proof,
			}
		}
		prepared, transitionErr := prepareSlotClearProvenDead(
			header, slotReader, index, &workspace.slot, stable.claim, proof, host,
		)
		if transitionErr != nil {
			return linuxReadySidecarScan{}, transitionErr
		}
		if executionErr := retained.executeSidecarSlotTransition(prepared, host); executionErr != nil {
			return linuxReadySidecarScan{}, executionErr
		}
		reapedReaders++
	}

	// The transition executor rechecks the header before every clear. Reselect it
	// once more after the complete reap pass so the third, survivor-summary pass
	// is bound to the exact same ready generation even when no reader was cleared.
	header, err = retained.readReadySidecarHeader(&header)
	if err != nil {
		return linuxReadySidecarScan{}, err
	}
	if err := requireCurrentLinuxPIDDomain(header); err != nil {
		return linuxReadySidecarScan{}, err
	}

	result := linuxReadySidecarScan{header: header, reapedReaders: reapedReaders}
	newestReaderTxnValid := false
	var newestReaderTxn uint64
	for rawIndex := uint64(0); rawIndex <= uint64(header.capacity); rawIndex++ {
		if linuxContextCancellation(ctx) != nil {
			return linuxReadySidecarScan{}, &linuxSidecarScanError{code: linuxSidecarScanCancelled}
		}
		index := uint32(rawIndex)
		role := slotReader
		if index == 0 {
			role = slotWriter
		}
		stable, scanErr := retained.readDecodedSidecarSlotInto(
			header, index, role, host, &workspace.slot,
		)
		if scanErr != nil {
			return linuxReadySidecarScan{}, scanErr
		}
		if index == 0 {
			if stable.active {
				result.writerActive, result.writer = true, stable.claim
			}
			continue
		}
		if !stable.active {
			if !result.lowestFreeSlotValid {
				result.lowestFreeSlotValid, result.lowestFreeSlot = true, index
			}
			continue
		}
		result.activeReaders++
		if stable.claim.txnID == 0 {
			result.registeringReaders++
			continue
		}
		if !result.oldestReaderTxnValid || stable.claim.txnID < result.oldestReaderTxn {
			result.oldestReaderTxnValid, result.oldestReaderTxn = true, stable.claim.txnID
		}
		if !newestReaderTxnValid || stable.claim.txnID > newestReaderTxn {
			newestReaderTxnValid, newestReaderTxn = true, stable.claim.txnID
		}
	}

	// Transaction checks deliberately follow the complete structural survivor scan.
	if result.writerActive && result.writer.txnID != selectedTxn {
		return linuxReadySidecarScan{}, &readySidecarError{
			code: readySidecarErrWriterTransactionMismatch, expected: selectedTxn, actual: result.writer.txnID,
		}
	}
	if newestReaderTxnValid && newestReaderTxn > selectedTxn {
		return linuxReadySidecarScan{}, &readySidecarError{
			code: readySidecarErrReaderTransactionFuture, expected: selectedTxn, actual: newestReaderTxn,
		}
	}
	return result, nil
}

type retainedLiveFiles struct {
	directory        *retainedDirectory
	mainComponent    string
	sidecarComponent string
	main             *retainedRegular
	sidecar          *retainedRegular
	header           sidecarHeader
	lastScanValid    bool
	lastBootstrap    Bootstrap
	lastInspection   linuxReadySidecarScan
	writerTail       *linuxUnpublishedMainTail
	writerBootstrap  *Bootstrap
}

type linuxOwnedReaderSlot struct {
	header sidecarHeader
	index  uint32
	active activeSlot
}

type linuxOwnedWriterLease struct {
	header    sidecarHeader
	active    activeSlot
	bootstrap Bootstrap
}

type linuxLivePairErrorCode uint8

const (
	linuxLivePairOS linuxLivePairErrorCode = iota + 1
	linuxLivePairScan
	linuxLivePairNoDeadWriter
	linuxLivePairWriterSourceChanged
	linuxLivePairMainGenerationChanged
	linuxLivePairTailLengthConflict
	linuxLivePairTransitionBeforeArm
	linuxLivePairTransition
	linuxLivePairArmedCleanup
	linuxLivePairPostClearPath
)

type linuxLivePairError struct {
	code        linuxLivePairErrorCode
	source      error
	target      uint64
	observedEnd uint64
	actual      uint64
}

func (e *linuxLivePairError) Error() string { return "exact v4 Linux live-pair operation failed" }
func (e *linuxLivePairError) Unwrap() error { return e.source }

type linuxReaderSlotErrorCode uint8

const (
	linuxReaderScanRequired linuxReaderSlotErrorCode = iota + 1
	linuxReaderScanChanged
	linuxReaderCapacityExhausted
	linuxReaderOwnerMismatch
	linuxReaderGenerationChanged
	linuxReaderOutstandingWriterCleanup
	linuxReaderNoCleanupAuthority
	linuxReaderOS
	linuxReaderTransitionBeforeArm
	linuxReaderTransition
	linuxReaderArmedCleanup
)

type linuxReaderSlotError struct {
	code   linuxReaderSlotErrorCode
	source error
}

func (e *linuxReaderSlotError) Error() string { return "exact v4 Linux reader-slot operation failed" }
func (e *linuxReaderSlotError) Unwrap() error { return e.source }

type linuxReaderSlotCleanupOutcome struct {
	mainPath    *linuxOSError
	sidecarPath *linuxOSError
}

type linuxLiveClaimCleanupOutcome = linuxReaderSlotCleanupOutcome

type linuxWriterLeaseErrorCode uint8

const (
	linuxWriterScanRequired linuxWriterLeaseErrorCode = iota + 1
	linuxWriterScanChanged
	linuxWriterBusy
	linuxWriterOwnerMismatch
	linuxWriterGenerationChanged
	linuxWriterTailCleanupRequired
	linuxWriterTailLengthConflict
	linuxWriterOutstandingWriterCleanup
	linuxWriterNoCleanupAuthority
	linuxWriterOS
	linuxWriterTransitionBeforeArm
	linuxWriterTransition
	linuxWriterArmedCleanup
)

type linuxWriterLeaseError struct {
	code        linuxWriterLeaseErrorCode
	source      error
	target      uint64
	observedEnd uint64
	actual      uint64
}

func (e *linuxWriterLeaseError) Error() string { return "exact v4 Linux writer-lease operation failed" }
func (e *linuxWriterLeaseError) Unwrap() error { return e.source }

func openLockedRetainedLiveFiles(path string) (*retainedLiveFiles, *linuxLivePairError) {
	return openLockedRetainedLiveFilesContext(context.Background(), path)
}

func openLockedRetainedLiveFilesContext(
	ctx context.Context,
	path string,
) (*retainedLiveFiles, *linuxLivePairError) {
	return openLockedRetainedLiveFilesContextWithPreBinding(ctx, path, nil)
}

func openLockedRetainedLiveFilesWithPreBinding(
	path string,
	preBinding func(*retainedRegular) *linuxOSError,
) (*retainedLiveFiles, *linuxLivePairError) {
	return openLockedRetainedLiveFilesContextWithPreBinding(context.Background(), path, preBinding)
}

func openLockedRetainedLiveFilesContextWithPreBinding(
	ctx context.Context,
	path string,
	preBinding func(*retainedRegular) *linuxOSError,
) (*retainedLiveFiles, *linuxLivePairError) {
	if err := linuxContextCancellation(ctx); err != nil {
		return nil, &linuxLivePairError{code: linuxLivePairOS, source: err}
	}
	directory, mainComponent, err := openRetainedParent(path)
	if err != nil {
		return nil, &linuxLivePairError{code: linuxLivePairOS, source: err}
	}
	var main, sidecar *retainedRegular
	closeOnError := func() {
		if sidecar != nil {
			_ = sidecar.file.Close()
		}
		if main != nil {
			_ = main.file.Close()
		}
		_ = directory.file.Close()
	}
	sidecarComponent, err := directory.sidecarComponent(mainComponent)
	if err != nil {
		closeOnError()
		return nil, &linuxLivePairError{code: linuxLivePairOS, source: err}
	}
	main, err = directory.openRegular(mainComponent, true)
	if err != nil {
		closeOnError()
		return nil, &linuxLivePairError{code: linuxLivePairOS, source: err}
	}
	_, err = main.readMainBootstrap(OpenWriter)
	if err != nil {
		closeOnError()
		return nil, &linuxLivePairError{code: linuxLivePairOS, source: err}
	}
	if err := main.acquireLockContext(ctx, linuxLockShared); err != nil {
		closeOnError()
		return nil, &linuxLivePairError{code: linuxLivePairOS, source: err}
	}
	if err := linuxContextCancellation(ctx); err != nil {
		closeOnError()
		return nil, &linuxLivePairError{code: linuxLivePairOS, source: err}
	}
	sidecar, err = directory.openRegular(sidecarComponent, true)
	if err != nil {
		closeOnError()
		return nil, &linuxLivePairError{code: linuxLivePairOS, source: err}
	}
	if err := sidecar.acquireLockContext(ctx, linuxLockExclusive); err != nil {
		closeOnError()
		return nil, &linuxLivePairError{code: linuxLivePairOS, source: err}
	}
	if err := linuxContextCancellation(ctx); err != nil {
		closeOnError()
		return nil, &linuxLivePairError{code: linuxLivePairOS, source: err}
	}
	if preBinding != nil {
		if err := preBinding(main); err != nil {
			closeOnError()
			return nil, &linuxLivePairError{code: linuxLivePairOS, source: err}
		}
	}
	bootstrap, err := main.readMainBootstrap(OpenWriter)
	if err != nil {
		closeOnError()
		return nil, &linuxLivePairError{code: linuxLivePairOS, source: err}
	}
	header, err := sidecar.readReadySidecarHeader(nil)
	if err != nil {
		closeOnError()
		return nil, &linuxLivePairError{code: linuxLivePairOS, source: err}
	}
	if err := directory.verifyLivePairBinding(
		mainComponent, main, sidecarComponent, sidecar, bootstrap, header,
	); err != nil {
		closeOnError()
		return nil, &linuxLivePairError{code: linuxLivePairOS, source: err}
	}
	return &retainedLiveFiles{
		directory: directory, mainComponent: mainComponent, sidecarComponent: sidecarComponent,
		main: main, sidecar: sidecar, header: header,
	}, nil
}

func (live *retainedLiveFiles) scanAndReap() (linuxReadySidecarScan, *linuxLivePairError) {
	return live.scanAndReapContext(context.Background())
}

func (live *retainedLiveFiles) scanAndReapContext(
	ctx context.Context,
) (linuxReadySidecarScan, *linuxLivePairError) {
	return live.scanAndReapWithObserverContext(ctx, nil)
}

func (live *retainedLiveFiles) scanAndReapWithObserver(
	observe func(activeSlot) posixProcessObservation,
) (linuxReadySidecarScan, *linuxLivePairError) {
	return live.scanAndReapWithObserverContext(context.Background(), observe)
}

func (live *retainedLiveFiles) scanAndReapWithObserverContext(
	ctx context.Context,
	observe func(activeSlot) posixProcessObservation,
) (linuxReadySidecarScan, *linuxLivePairError) {
	live.lastScanValid = false
	live.lastBootstrap = Bootstrap{}
	live.lastInspection = linuxReadySidecarScan{}
	bootstrap, err := live.main.readMainBootstrap(OpenWriter)
	if err != nil {
		return linuxReadySidecarScan{}, &linuxLivePairError{code: linuxLivePairOS, source: err}
	}
	if err := live.directory.verifyLivePairBinding(
		live.mainComponent, live.main, live.sidecarComponent, live.sidecar, bootstrap, live.header,
	); err != nil {
		return linuxReadySidecarScan{}, &linuxLivePairError{code: linuxLivePairOS, source: err}
	}
	inspection, scanErr := live.sidecar.scanAndReapReadySidecarWithObserverContext(
		ctx, live.header, bootstrap.Meta.TxnID, observe,
	)
	if scanErr != nil {
		return linuxReadySidecarScan{}, &linuxLivePairError{code: linuxLivePairScan, source: scanErr}
	}
	live.lastScanValid = true
	live.lastBootstrap = bootstrap
	live.lastInspection = inspection
	return inspection, nil
}

func (live *retainedLiveFiles) scanAndReapOwnedWriterContext(
	ctx context.Context,
	owned *linuxOwnedWriterLease,
	workspace *linuxSidecarScanWorkspace,
	observe func(activeSlot) posixProcessObservation,
	preparedValid bool,
) (linuxReadySidecarScan, *linuxLivePairError) {
	if owned == nil {
		return linuxReadySidecarScan{}, &linuxLivePairError{
			code:   linuxLivePairScan,
			source: &linuxWriterLeaseError{code: linuxWriterNoCleanupAuthority},
		}
	}
	bootstrap, err := live.main.readMainBootstrap(OpenWriter)
	if err != nil {
		return linuxReadySidecarScan{}, &linuxLivePairError{code: linuxLivePairOS, source: err}
	}
	if err := live.directory.verifyLivePairBinding(
		live.mainComponent, live.main, live.sidecarComponent, live.sidecar, bootstrap, live.header,
	); err != nil {
		return linuxReadySidecarScan{}, &linuxLivePairError{code: linuxLivePairOS, source: err}
	}
	inspection, scanErr := live.sidecar.scanAndReapReadySidecarOwnedWriterContext(
		ctx, live.header, bootstrap.Meta.TxnID, &owned.active, workspace, observe,
		workspace.candidates, preparedValid,
	)
	if scanErr != nil {
		return linuxReadySidecarScan{}, &linuxLivePairError{code: linuxLivePairScan, source: scanErr}
	}
	return inspection, nil
}

func (live *retainedLiveFiles) retryDeadWriterCleanup() *linuxLivePairError {
	return live.retryDeadWriterCleanupWithPostClear(nil)
}

func (live *retainedLiveFiles) retryDeadWriterCleanupWithPostClear(
	postClear func(),
) *linuxLivePairError {
	return live.retryDeadWriterCleanupWithActions(
		func(file *os.File, length uint64) error {
			if length > math.MaxInt64 {
				return &linuxOSError{code: linuxOSOffsetOverflow}
			}
			return file.Truncate(int64(length))
		},
		func(file *os.File) error { return file.Sync() },
		postClear,
	)
}

func (live *retainedLiveFiles) retryDeadWriterCleanupWith(
	truncate func(*os.File, uint64) error,
	synchronize func(*os.File) error,
) *linuxLivePairError {
	return live.retryDeadWriterCleanupWithActions(truncate, synchronize, nil)
}

func (live *retainedLiveFiles) retryDeadWriterCleanupWithActions(
	truncate func(*os.File, uint64) error,
	synchronize func(*os.File) error,
	postClear func(),
) *linuxLivePairError {
	return live.retryDeadWriterCleanupWithTransition(
		truncate, synchronize, postClear, nil,
		func(
			sidecar *retainedRegular,
			prepared *preparedSlotTransition,
			offset uint64,
		) *slotExecutionError {
			return sidecar.executePreconfirmedSidecarSlotTransition(prepared, offset, true)
		},
	)
}

func (live *retainedLiveFiles) retryDeadWriterCleanupWithTransition(
	truncate func(*os.File, uint64) error,
	synchronize func(*os.File) error,
	postClear func(),
	beforeTruncateHeaderCheck func(),
	execute func(*retainedRegular, *preparedSlotTransition, uint64) *slotExecutionError,
) *linuxLivePairError {
	if live.main.lock != linuxLockShared {
		return &linuxLivePairError{
			code: linuxLivePairOS, source: &linuxOSError{code: linuxOSLifetimeLockRequired},
		}
	}
	if err := live.sidecar.requireExclusiveLock(); err != nil {
		return &linuxLivePairError{code: linuxLivePairOS, source: err}
	}
	if !live.sidecar.cleanupAuthority.valid() {
		return &linuxLivePairError{
			code:   linuxLivePairTransitionBeforeArm,
			source: &slotTransitionError{code: slotTransitionErrProvenanceOccupied},
		}
	}
	if live.sidecar.cleanupAuthority.kind == linuxCleanupArmed {
		if !linuxArmedDeadWriterCleanup(live) {
			return &linuxLivePairError{code: linuxLivePairNoDeadWriter}
		}
		return live.retryArmedDeadWriterCleanup(postClear)
	}
	if live.sidecar.cleanupAuthority.kind != linuxCleanupDeadWriter {
		return &linuxLivePairError{code: linuxLivePairNoDeadWriter}
	}
	dead := live.sidecar.cleanupAuthority.writer
	header, err := live.sidecar.readReadySidecarHeader(&dead.header)
	if err != nil {
		return &linuxLivePairError{code: linuxLivePairOS, source: err}
	}
	source, err := live.sidecar.readSidecarSlotAfterHeader(header, 0)
	if err != nil {
		return &linuxLivePairError{code: linuxLivePairOS, source: err}
	}
	if source != dead.raw {
		return &linuxLivePairError{code: linuxLivePairWriterSourceChanged}
	}
	if dead.tailValid {
		var stat unix.Stat_t
		if err := unix.Fstat(int(live.main.file.Fd()), &stat); err != nil {
			return &linuxLivePairError{code: linuxLivePairOS, source: linuxIOError("inspect retained main tail", err)}
		}
		if err := validateRegularStat(&stat); err != nil {
			return &linuxLivePairError{code: linuxLivePairOS, source: err}
		}
		if identityFromStat(&stat) != dead.tail.mainIdentity {
			return &linuxLivePairError{code: linuxLivePairMainGenerationChanged}
		}
		if stat.Size < 0 {
			return &linuxLivePairError{code: linuxLivePairOS, source: &linuxOSError{code: linuxOSOffsetOverflow}}
		}
		actual := uint64(stat.Size)
		if actual < dead.tail.committedLength || actual > dead.tail.observedEndExclusive {
			return &linuxLivePairError{
				code: linuxLivePairTailLengthConflict, target: dead.tail.committedLength,
				observedEnd: dead.tail.observedEndExclusive, actual: actual,
			}
		}
	}
	before, err := live.main.readMainBootstrap(OpenWriter)
	if err != nil {
		return &linuxLivePairError{code: linuxLivePairOS, source: err}
	}
	if dead.bootstrapValid {
		if pairErr := requireExactLinuxDeadWriterBootstrap(dead.bootstrap, before); pairErr != nil {
			return pairErr
		}
	} else {
		dead.bootstrapValid = true
		dead.bootstrap = before
	}
	if err := live.directory.verifyLivePairBinding(
		live.mainComponent, live.main, live.sidecarComponent, live.sidecar, before, header,
	); err != nil {
		return &linuxLivePairError{code: linuxLivePairOS, source: err}
	}
	if dead.tailValid {
		if err := requireSameLinuxTailGeneration(dead.tail, live.main.identity, before); err != nil {
			return err
		}
	} else if before.PhysicalBytes > before.CommittedBytes {
		dead.tailValid = true
		dead.tail = linuxUnpublishedMainTail{
			mainIdentity: live.main.identity, databaseID: before.Meta.DatabaseID,
			transactionID: before.Meta.TxnID, commitNonce: before.Meta.CommitNonce,
			committedLength: before.CommittedBytes, observedEndExclusive: before.PhysicalBytes,
		}
	}
	live.sidecar.cleanupAuthority = linuxSidecarCleanupAuthority{
		kind: linuxCleanupDeadWriter, writer: dead,
	}
	if beforeTruncateHeaderCheck != nil && before.PhysicalBytes > before.CommittedBytes {
		beforeTruncateHeaderCheck()
	}
	header, err = live.sidecar.readReadySidecarHeader(&dead.header)
	if err != nil {
		return &linuxLivePairError{code: linuxLivePairOS, source: err}
	}
	source, err = live.sidecar.readSidecarSlotAfterHeader(header, 0)
	if err != nil {
		return &linuxLivePairError{code: linuxLivePairOS, source: err}
	}
	if source != dead.raw {
		return &linuxLivePairError{code: linuxLivePairWriterSourceChanged}
	}
	if before.PhysicalBytes > before.CommittedBytes {
		if err := truncate(live.main.file, before.CommittedBytes); err != nil {
			return &linuxLivePairError{
				code: linuxLivePairOS, source: linuxIOError("truncate unpublished main tail", err),
			}
		}
	}
	if err := synchronize(live.main.file); err != nil {
		return &linuxLivePairError{
			code: linuxLivePairOS, source: linuxIOError("synchronize main tail cleanup", err),
		}
	}
	header, err = live.sidecar.readReadySidecarHeader(&dead.header)
	if err != nil {
		return &linuxLivePairError{code: linuxLivePairOS, source: err}
	}
	source, err = live.sidecar.readSidecarSlotAfterHeader(header, 0)
	if err != nil {
		return &linuxLivePairError{code: linuxLivePairOS, source: err}
	}
	if source != dead.raw {
		return &linuxLivePairError{code: linuxLivePairWriterSourceChanged}
	}
	after, err := live.main.readMainBootstrap(OpenWriter)
	if err != nil {
		return &linuxLivePairError{code: linuxLivePairOS, source: err}
	}
	if pairErr := requireExactLinuxDeadWriterBootstrap(dead.bootstrap, after); pairErr != nil {
		return pairErr
	}
	if after.PhysicalBytes != dead.bootstrap.CommittedBytes {
		return &linuxLivePairError{code: linuxLivePairMainGenerationChanged}
	}
	if dead.tailValid {
		if pairErr := requireSameLinuxTailGeneration(dead.tail, live.main.identity, after); pairErr != nil {
			return pairErr
		}
	}
	if err := live.directory.verifyLivePairBinding(
		live.mainComponent, live.main, live.sidecarComponent, live.sidecar, after, header,
	); err != nil {
		return &linuxLivePairError{code: linuxLivePairOS, source: err}
	}
	finalSource, err := live.sidecar.readSidecarSlot(dead.header, 0)
	if err != nil {
		return &linuxLivePairError{code: linuxLivePairOS, source: err}
	}
	if finalSource != dead.raw {
		return &linuxLivePairError{code: linuxLivePairWriterSourceChanged}
	}
	prepared, transitionErr := prepareSlotClearProvenDead(
		header, slotWriter, 0, &finalSource, dead.active, dead.proof, linuxSlotHostLimits(),
	)
	if transitionErr != nil {
		return &linuxLivePairError{code: linuxLivePairTransitionBeforeArm, source: transitionErr}
	}
	offset, offsetErr := sidecarSlotOffset(header, 0)
	if offsetErr != nil {
		return &linuxLivePairError{code: linuxLivePairOS, source: offsetErr}
	}
	if executionErr := execute(live.sidecar, prepared, offset); executionErr != nil {
		return &linuxLivePairError{code: linuxLivePairTransition, source: executionErr}
	}
	if postClear != nil {
		postClear()
	}
	if err := live.directory.verifyPath(live.mainComponent, live.main); err != nil {
		return &linuxLivePairError{code: linuxLivePairPostClearPath, source: err}
	}
	if err := live.directory.verifyPath(live.sidecarComponent, live.sidecar); err != nil {
		return &linuxLivePairError{code: linuxLivePairPostClearPath, source: err}
	}
	return nil
}

func (live *retainedLiveFiles) retryArmedDeadWriterCleanup(
	postClear func(),
) *linuxLivePairError {
	authority := live.sidecar.cleanupAuthority
	if authority.kind != linuxCleanupArmed ||
		!armedDeadWriterObligationMatches(authority.armed, authority.writer) {
		return &linuxLivePairError{code: linuxLivePairNoDeadWriter}
	}
	dead := authority.writer
	header, err := live.sidecar.readReadySidecarHeader(&dead.header)
	if err != nil {
		return &linuxLivePairError{code: linuxLivePairOS, source: err}
	}
	if _, err := live.sidecar.readSidecarSlotAfterHeader(header, 0); err != nil {
		return &linuxLivePairError{code: linuxLivePairOS, source: err}
	}
	if !dead.bootstrapValid {
		return &linuxLivePairError{code: linuxLivePairMainGenerationChanged}
	}
	current, err := live.main.readMainBootstrap(OpenWriter)
	if err != nil {
		return &linuxLivePairError{code: linuxLivePairOS, source: err}
	}
	if pairErr := requireExactLinuxDeadWriterBootstrap(dead.bootstrap, current); pairErr != nil {
		return pairErr
	}
	if current.PhysicalBytes != dead.bootstrap.CommittedBytes {
		return &linuxLivePairError{code: linuxLivePairMainGenerationChanged}
	}
	if dead.tailValid {
		if pairErr := requireSameLinuxTailGeneration(dead.tail, live.main.identity, current); pairErr != nil {
			return pairErr
		}
	}
	if err := live.directory.verifyLivePairBinding(
		live.mainComponent, live.main, live.sidecarComponent, live.sidecar, current, header,
	); err != nil {
		return &linuxLivePairError{code: linuxLivePairOS, source: err}
	}
	if _, executionErr := live.sidecar.retrySidecarSlotCleanup(linuxSlotHostLimits()); executionErr != nil {
		return &linuxLivePairError{code: linuxLivePairArmedCleanup, source: executionErr}
	}
	if postClear != nil {
		postClear()
	}
	if err := live.directory.verifyPath(live.mainComponent, live.main); err != nil {
		return &linuxLivePairError{code: linuxLivePairPostClearPath, source: err}
	}
	if err := live.directory.verifyPath(live.sidecarComponent, live.sidecar); err != nil {
		return &linuxLivePairError{code: linuxLivePairPostClearPath, source: err}
	}
	return nil
}

func requireExactLinuxDeadWriterBootstrap(expected, actual Bootstrap) *linuxLivePairError {
	if !sameLinuxWriterGeneration(actual, expected) {
		return &linuxLivePairError{code: linuxLivePairMainGenerationChanged}
	}
	return nil
}

func (live *retainedLiveFiles) claimWriterLease() (*linuxOwnedWriterLease, *linuxWriterLeaseError) {
	return live.claimWriterLeaseWith(randomNonzero128)
}

func (live *retainedLiveFiles) claimWriterLeaseWith(
	nonce func() ([16]byte, *linuxOSError),
) (*linuxOwnedWriterLease, *linuxWriterLeaseError) {
	if !live.lastScanValid {
		return nil, &linuxWriterLeaseError{code: linuxWriterScanRequired}
	}
	bootstrap, inspection := live.lastBootstrap, live.lastInspection
	live.lastScanValid = false
	live.lastBootstrap = Bootstrap{}
	live.lastInspection = linuxReadySidecarScan{}
	if inspection.header != live.header {
		return nil, &linuxWriterLeaseError{code: linuxWriterScanChanged}
	}
	if inspection.writerActive {
		return nil, &linuxWriterLeaseError{code: linuxWriterBusy}
	}
	currentBootstrap, err := live.main.readMainBootstrap(OpenWriter)
	if err != nil {
		return nil, &linuxWriterLeaseError{code: linuxWriterOS, source: err}
	}
	if currentBootstrap != bootstrap {
		return nil, &linuxWriterLeaseError{code: linuxWriterGenerationChanged}
	}
	if err := live.directory.verifyLivePairBinding(
		live.mainComponent, live.main, live.sidecarComponent, live.sidecar,
		currentBootstrap, inspection.header,
	); err != nil {
		return nil, &linuxWriterLeaseError{code: linuxWriterOS, source: err}
	}

	// Entropy, source reads, transition preparation, and returned ownership are
	// all prepared before the lease can become visible.
	claimNonce, err := nonce()
	if err != nil {
		return nil, &linuxWriterLeaseError{code: linuxWriterOS, source: err}
	}
	current, readErr := live.sidecar.readSidecarSlotAfterHeader(inspection.header, 0)
	if readErr != nil {
		return nil, &linuxWriterLeaseError{code: linuxWriterOS, source: readErr}
	}
	active := currentLinuxActiveSlot(bootstrap.Meta.TxnID, claimNonce)
	prepared, transitionErr := prepareSlotClaim(
		inspection.header, slotWriter, 0, &current, active, linuxSlotHostLimits(),
	)
	if transitionErr != nil {
		return nil, &linuxWriterLeaseError{code: linuxWriterTransitionBeforeArm, source: transitionErr}
	}
	owned := &linuxOwnedWriterLease{
		header: inspection.header, active: active, bootstrap: bootstrap,
	}
	writerBootstrap := bootstrap
	live.writerBootstrap = &writerBootstrap
	if bootstrap.PhysicalBytes > bootstrap.CommittedBytes {
		tail := linuxUnpublishedMainTail{
			mainIdentity: live.main.identity, databaseID: bootstrap.Meta.DatabaseID,
			transactionID: bootstrap.Meta.TxnID, commitNonce: bootstrap.Meta.CommitNonce,
			committedLength: bootstrap.CommittedBytes, observedEndExclusive: bootstrap.PhysicalBytes,
		}
		live.writerTail = &tail
	}
	if executionErr := live.sidecar.executeSidecarSlotTransition(prepared, linuxSlotHostLimits()); executionErr != nil {
		if live.sidecar.cleanupAuthority.kind != linuxCleanupArmed {
			live.writerTail = nil
			live.writerBootstrap = nil
		}
		return nil, &linuxWriterLeaseError{code: linuxWriterTransition, source: executionErr}
	}
	return owned, nil
}

func (live *retainedLiveFiles) prepareWriterForExposure(
	owned *linuxOwnedWriterLease,
) (Bootstrap, *linuxWriterLeaseError) {
	return live.prepareWriterForExposureWith(
		owned,
		func(file *os.File, length uint64) error {
			if length > math.MaxInt64 {
				return &linuxOSError{code: linuxOSOffsetOverflow}
			}
			return file.Truncate(int64(length))
		},
		func(file *os.File) error { return file.Sync() },
	)
}

func (live *retainedLiveFiles) prepareWriterForExposureWith(
	owned *linuxOwnedWriterLease,
	truncate func(*os.File, uint64) error,
	synchronize func(*os.File) error,
) (Bootstrap, *linuxWriterLeaseError) {
	bootstrap, writerErr := live.captureOwnedWriterTail(owned, true)
	if writerErr != nil {
		return Bootstrap{}, writerErr
	}
	if writerErr := live.resolveOwnedWriterTailWith(owned, truncate, synchronize, true); writerErr != nil {
		return Bootstrap{}, writerErr
	}
	finalBootstrap, writerErr := live.verifyOwnedWriter(owned)
	if writerErr != nil {
		return Bootstrap{}, writerErr
	}
	if finalBootstrap.Meta != bootstrap.Meta || finalBootstrap.Selection != bootstrap.Selection ||
		finalBootstrap.SelectedMetaPage != bootstrap.SelectedMetaPage ||
		finalBootstrap.CommittedBytes != bootstrap.CommittedBytes ||
		finalBootstrap.PhysicalBytes != bootstrap.CommittedBytes {
		return Bootstrap{}, &linuxWriterLeaseError{code: linuxWriterGenerationChanged}
	}
	if err := live.sidecar.releaseLock(); err != nil {
		return Bootstrap{}, &linuxWriterLeaseError{code: linuxWriterOS, source: err}
	}
	return finalBootstrap, nil
}

func (live *retainedLiveFiles) verifyOwnedWriter(
	owned *linuxOwnedWriterLease,
) (Bootstrap, *linuxWriterLeaseError) {
	return live.verifyOwnedWriterWithPaths(owned, true)
}

func (live *retainedLiveFiles) verifyOwnedWriterWithPaths(
	owned *linuxOwnedWriterLease,
	requireCanonicalPaths bool,
) (Bootstrap, *linuxWriterLeaseError) {
	bootstrap, active, writerErr := live.inspectOwnedWriterForCleanup(owned, requireCanonicalPaths)
	if writerErr != nil {
		return Bootstrap{}, writerErr
	}
	if !active {
		return Bootstrap{}, &linuxWriterLeaseError{code: linuxWriterOwnerMismatch}
	}
	return bootstrap, nil
}

func (live *retainedLiveFiles) inspectOwnedWriterForCleanup(
	owned *linuxOwnedWriterLease,
	requireCanonicalPaths bool,
) (Bootstrap, bool, *linuxWriterLeaseError) {
	if owned == nil {
		return Bootstrap{}, false, &linuxWriterLeaseError{code: linuxWriterNoCleanupAuthority}
	}
	if owned.header != live.header || owned.active.txnID == 0 {
		return Bootstrap{}, false, &linuxWriterLeaseError{code: linuxWriterOwnerMismatch}
	}
	if live.writerBootstrap == nil || owned.bootstrap != *live.writerBootstrap ||
		owned.active.txnID != live.writerBootstrap.Meta.TxnID {
		return Bootstrap{}, false, &linuxWriterLeaseError{code: linuxWriterOwnerMismatch}
	}
	bootstrap, err := live.main.readMainBootstrap(OpenWriter)
	if err != nil {
		return Bootstrap{}, false, &linuxWriterLeaseError{code: linuxWriterOS, source: err}
	}
	if !sameLinuxWriterGeneration(bootstrap, *live.writerBootstrap) {
		return Bootstrap{}, false, &linuxWriterLeaseError{code: linuxWriterGenerationChanged}
	}
	if _, err := live.sidecar.readReadySidecarHeader(&owned.header); err != nil {
		return Bootstrap{}, false, &linuxWriterLeaseError{code: linuxWriterOS, source: err}
	}
	if err := live.verifyOwnedWriterBinding(bootstrap, owned.header, requireCanonicalPaths); err != nil {
		return Bootstrap{}, false, &linuxWriterLeaseError{code: linuxWriterOS, source: err}
	}
	current, err := live.sidecar.readSidecarSlotAfterHeader(owned.header, 0)
	if err != nil {
		return Bootstrap{}, false, &linuxWriterLeaseError{code: linuxWriterOS, source: err}
	}
	if current == ([sidecarSlotSize]byte{}) {
		return bootstrap, false, nil
	}
	stable, problem := decodeStableSlot(current[:], slotWriter, linuxSlotHostLimits())
	if problem != 0 || !stable.active || stable.claim != owned.active {
		return Bootstrap{}, false, &linuxWriterLeaseError{code: linuxWriterOwnerMismatch}
	}
	return bootstrap, true, nil
}

func sameLinuxWriterGeneration(actual Bootstrap, expected Bootstrap) bool {
	return actual.Meta == expected.Meta && actual.Selection == expected.Selection &&
		actual.SelectedMetaPage == expected.SelectedMetaPage &&
		actual.CommittedBytes == expected.CommittedBytes
}

func (live *retainedLiveFiles) captureOwnedWriterTail(
	owned *linuxOwnedWriterLease,
	requireCanonicalPaths bool,
) (Bootstrap, *linuxWriterLeaseError) {
	bootstrap, active, writerErr := live.inspectOwnedWriterForCleanup(owned, requireCanonicalPaths)
	if writerErr != nil {
		return Bootstrap{}, writerErr
	}
	if bootstrap.PhysicalBytes < bootstrap.CommittedBytes {
		return Bootstrap{}, &linuxWriterLeaseError{
			code: linuxWriterTailLengthConflict, target: bootstrap.CommittedBytes,
			actual: bootstrap.PhysicalBytes,
		}
	}
	if bootstrap.PhysicalBytes == bootstrap.CommittedBytes {
		if live.writerTail != nil && !active {
			return Bootstrap{}, &linuxWriterLeaseError{code: linuxWriterOwnerMismatch}
		}
		return bootstrap, nil
	}
	if !active {
		return Bootstrap{}, &linuxWriterLeaseError{code: linuxWriterOwnerMismatch}
	}
	if live.writerTail == nil {
		tail := linuxUnpublishedMainTail{
			mainIdentity: live.main.identity, databaseID: bootstrap.Meta.DatabaseID,
			transactionID: bootstrap.Meta.TxnID, commitNonce: bootstrap.Meta.CommitNonce,
			committedLength: bootstrap.CommittedBytes, observedEndExclusive: bootstrap.PhysicalBytes,
		}
		live.writerTail = &tail
		return bootstrap, nil
	}
	if writerErr := requireSameOwnedLinuxTailGeneration(
		*live.writerTail, live.main.identity, bootstrap,
	); writerErr != nil {
		return Bootstrap{}, writerErr
	}
	return bootstrap, nil
}

func (live *retainedLiveFiles) verifyOwnedWriterBinding(
	bootstrap Bootstrap,
	header sidecarHeader,
	requireCanonicalPaths bool,
) *linuxOSError {
	if requireCanonicalPaths {
		return live.directory.verifyLivePairBinding(
			live.mainComponent, live.main, live.sidecarComponent, live.sidecar,
			bootstrap, header,
		)
	}
	return live.directory.verifyRetainedLivePairBinding(
		live.mainComponent, live.main, live.sidecar, bootstrap, header,
	)
}

func (live *retainedLiveFiles) captureArmedWriterTail() (Bootstrap, *linuxWriterLeaseError) {
	if live.writerBootstrap == nil || live.sidecar.cleanupAuthority.kind != linuxCleanupArmed {
		return Bootstrap{}, &linuxWriterLeaseError{code: linuxWriterNoCleanupAuthority}
	}
	armed := live.sidecar.cleanupAuthority.armed
	if armed == nil || armed.roleValue() != slotWriter || armed.slotIndexValue() != 0 ||
		armed.headerValue() != live.header {
		return Bootstrap{}, &linuxWriterLeaseError{code: linuxWriterOwnerMismatch}
	}
	if _, err := live.sidecar.readReadySidecarHeader(&live.header); err != nil {
		return Bootstrap{}, &linuxWriterLeaseError{code: linuxWriterOS, source: err}
	}
	bootstrap, err := live.main.readMainBootstrap(OpenWriter)
	if err != nil {
		return Bootstrap{}, &linuxWriterLeaseError{code: linuxWriterOS, source: err}
	}
	if !sameLinuxWriterGeneration(bootstrap, *live.writerBootstrap) {
		return Bootstrap{}, &linuxWriterLeaseError{code: linuxWriterGenerationChanged}
	}
	if err := live.verifyOwnedWriterBinding(bootstrap, live.header, false); err != nil {
		return Bootstrap{}, &linuxWriterLeaseError{code: linuxWriterOS, source: err}
	}
	if bootstrap.PhysicalBytes < bootstrap.CommittedBytes {
		return Bootstrap{}, &linuxWriterLeaseError{
			code: linuxWriterTailLengthConflict, target: bootstrap.CommittedBytes,
			actual: bootstrap.PhysicalBytes,
		}
	}
	if bootstrap.PhysicalBytes == bootstrap.CommittedBytes {
		return bootstrap, nil
	}
	if live.writerTail == nil {
		tail := linuxUnpublishedMainTail{
			mainIdentity: live.main.identity, databaseID: bootstrap.Meta.DatabaseID,
			transactionID: bootstrap.Meta.TxnID, commitNonce: bootstrap.Meta.CommitNonce,
			committedLength: bootstrap.CommittedBytes, observedEndExclusive: bootstrap.PhysicalBytes,
		}
		live.writerTail = &tail
		return bootstrap, nil
	}
	if writerErr := requireSameOwnedLinuxTailGeneration(
		*live.writerTail, live.main.identity, bootstrap,
	); writerErr != nil {
		return Bootstrap{}, writerErr
	}
	return bootstrap, nil
}

func (live *retainedLiveFiles) claimReaderSlot() (*linuxOwnedReaderSlot, *linuxReaderSlotError) {
	return live.claimReaderSlotWith(randomNonzero128)
}

func (live *retainedLiveFiles) claimReaderSlotWith(
	nonce func() ([16]byte, *linuxOSError),
) (*linuxOwnedReaderSlot, *linuxReaderSlotError) {
	if !live.lastScanValid {
		return nil, &linuxReaderSlotError{code: linuxReaderScanRequired}
	}
	bootstrap, inspection := live.lastBootstrap, live.lastInspection
	live.lastScanValid = false
	live.lastBootstrap = Bootstrap{}
	live.lastInspection = linuxReadySidecarScan{}
	if !inspection.lowestFreeSlotValid {
		return nil, &linuxReaderSlotError{code: linuxReaderCapacityExhausted}
	}
	if inspection.header != live.header {
		return nil, &linuxReaderSlotError{code: linuxReaderScanChanged}
	}
	if err := live.directory.verifyLivePairBinding(
		live.mainComponent, live.main, live.sidecarComponent, live.sidecar, bootstrap, inspection.header,
	); err != nil {
		return nil, &linuxReaderSlotError{code: linuxReaderOS, source: err}
	}
	claimNonce, err := nonce()
	if err != nil {
		return nil, &linuxReaderSlotError{code: linuxReaderOS, source: err}
	}
	current, readErr := live.sidecar.readSidecarSlotAfterHeader(
		inspection.header, inspection.lowestFreeSlot,
	)
	if readErr != nil {
		return nil, &linuxReaderSlotError{code: linuxReaderOS, source: readErr}
	}
	active := currentLinuxActiveSlot(0, claimNonce)
	prepared, transitionErr := prepareSlotClaim(
		inspection.header, slotReader, inspection.lowestFreeSlot, &current, active, linuxSlotHostLimits(),
	)
	if transitionErr != nil {
		return nil, &linuxReaderSlotError{
			code: linuxReaderTransitionBeforeArm, source: transitionErr,
		}
	}
	if executionErr := live.sidecar.executeSidecarSlotTransition(prepared, linuxSlotHostLimits()); executionErr != nil {
		return nil, &linuxReaderSlotError{code: linuxReaderTransition, source: executionErr}
	}
	return &linuxOwnedReaderSlot{
		header: inspection.header, index: inspection.lowestFreeSlot, active: active,
	}, nil
}

func (live *retainedLiveFiles) pinReaderSlot(
	owned *linuxOwnedReaderSlot,
) (Bootstrap, *linuxReaderSlotError) {
	if owned == nil || owned.header != live.header || owned.index == 0 || owned.index > live.header.capacity {
		return Bootstrap{}, &linuxReaderSlotError{code: linuxReaderOwnerMismatch}
	}
	bootstrap, err := live.main.readMainBootstrap(OpenLiveReader)
	if err != nil {
		return Bootstrap{}, &linuxReaderSlotError{code: linuxReaderOS, source: err}
	}
	if err := live.directory.verifyLivePairBinding(
		live.mainComponent, live.main, live.sidecarComponent, live.sidecar, bootstrap, owned.header,
	); err != nil {
		return Bootstrap{}, &linuxReaderSlotError{code: linuxReaderOS, source: err}
	}
	current, readErr := live.sidecar.readSidecarSlotAfterHeader(owned.header, owned.index)
	if readErr != nil {
		return Bootstrap{}, &linuxReaderSlotError{code: linuxReaderOS, source: readErr}
	}
	target := owned.active
	target.txnID = bootstrap.Meta.TxnID
	prepared, transitionErr := prepareSlotUpdate(
		owned.header, slotReader, owned.index, &current, owned.active, target, linuxSlotHostLimits(),
	)
	if transitionErr != nil {
		return Bootstrap{}, &linuxReaderSlotError{
			code: linuxReaderTransitionBeforeArm, source: transitionErr,
		}
	}
	if executionErr := live.sidecar.executeSidecarSlotTransition(prepared, linuxSlotHostLimits()); executionErr != nil {
		return Bootstrap{}, &linuxReaderSlotError{code: linuxReaderTransition, source: executionErr}
	}
	owned.active = target
	return bootstrap, nil
}

func (live *retainedLiveFiles) releaseReaderRegistrationLock(
	owned *linuxOwnedReaderSlot,
	pinned Bootstrap,
) *linuxReaderSlotError {
	if owned == nil || owned.header != live.header || owned.index == 0 || owned.index > live.header.capacity ||
		owned.active.txnID == 0 || owned.active.txnID != pinned.Meta.TxnID {
		return &linuxReaderSlotError{code: linuxReaderOwnerMismatch}
	}
	currentBootstrap, err := live.main.readMainBootstrap(OpenLiveReader)
	if err != nil {
		return &linuxReaderSlotError{code: linuxReaderOS, source: err}
	}
	if currentBootstrap != pinned {
		return &linuxReaderSlotError{code: linuxReaderGenerationChanged}
	}
	if err := live.directory.verifyLivePairBinding(
		live.mainComponent, live.main, live.sidecarComponent, live.sidecar, currentBootstrap, owned.header,
	); err != nil {
		return &linuxReaderSlotError{code: linuxReaderOS, source: err}
	}
	current, readErr := live.sidecar.readSidecarSlotAfterHeader(owned.header, owned.index)
	if readErr != nil {
		return &linuxReaderSlotError{code: linuxReaderOS, source: readErr}
	}
	stable, problem := decodeStableSlot(current[:], slotReader, linuxSlotHostLimits())
	if problem != 0 || !stable.active || stable.claim != owned.active {
		return &linuxReaderSlotError{code: linuxReaderOwnerMismatch}
	}
	if err := live.sidecar.releaseLock(); err != nil {
		return &linuxReaderSlotError{code: linuxReaderOS, source: err}
	}
	return nil
}

func (live *retainedLiveFiles) clearOwnedReaderSlot(
	owned *linuxOwnedReaderSlot,
) *linuxReaderSlotError {
	if owned == nil || owned.header != live.header || owned.index == 0 || owned.index > live.header.capacity {
		return &linuxReaderSlotError{code: linuxReaderOwnerMismatch}
	}
	current, err := live.sidecar.readSidecarSlot(owned.header, owned.index)
	if err != nil {
		return &linuxReaderSlotError{code: linuxReaderOS, source: err}
	}
	if current == ([sidecarSlotSize]byte{}) {
		return nil
	}
	prepared, transitionErr := prepareSlotClearOwned(
		owned.header, slotReader, owned.index, &current, owned.active, linuxSlotHostLimits(),
	)
	if transitionErr != nil {
		return &linuxReaderSlotError{code: linuxReaderTransitionBeforeArm, source: transitionErr}
	}
	if executionErr := live.sidecar.executeSidecarSlotTransition(prepared, linuxSlotHostLimits()); executionErr != nil {
		return &linuxReaderSlotError{code: linuxReaderTransition, source: executionErr}
	}
	return nil
}

func (live *retainedLiveFiles) retryReaderSlotCleanup(
	owned *linuxOwnedReaderSlot,
) (linuxReaderSlotCleanupOutcome, *linuxReaderSlotError) {
	live.lastScanValid = false
	live.lastBootstrap = Bootstrap{}
	live.lastInspection = linuxReadySidecarScan{}
	if live.main.lock != linuxLockShared {
		return linuxReaderSlotCleanupOutcome{}, &linuxReaderSlotError{
			code: linuxReaderOS, source: &linuxOSError{code: linuxOSLifetimeLockRequired},
		}
	}
	if !live.sidecar.cleanupAuthority.valid() {
		return linuxReaderSlotCleanupOutcome{}, &linuxReaderSlotError{
			code:   linuxReaderTransitionBeforeArm,
			source: &slotTransitionError{code: slotTransitionErrProvenanceOccupied},
		}
	}
	switch live.sidecar.cleanupAuthority.kind {
	case linuxCleanupArmed:
		if _, executionErr := live.sidecar.retrySidecarSlotCleanup(linuxSlotHostLimits()); executionErr != nil {
			return linuxReaderSlotCleanupOutcome{}, &linuxReaderSlotError{code: linuxReaderArmedCleanup, source: executionErr}
		}
		return live.readerCleanupPaths(), nil
	case linuxCleanupDeadWriter:
		return linuxReaderSlotCleanupOutcome{}, &linuxReaderSlotError{code: linuxReaderOutstandingWriterCleanup}
	}
	if owned == nil {
		return linuxReaderSlotCleanupOutcome{}, &linuxReaderSlotError{code: linuxReaderNoCleanupAuthority}
	}
	if live.sidecar.lock == 0 {
		if err := live.sidecar.acquireLock(linuxLockExclusive, false); err != nil {
			return linuxReaderSlotCleanupOutcome{}, &linuxReaderSlotError{code: linuxReaderOS, source: err}
		}
	}
	if err := live.clearOwnedReaderSlot(owned); err != nil {
		return linuxReaderSlotCleanupOutcome{}, err
	}
	return live.readerCleanupPaths(), nil
}

func (live *retainedLiveFiles) readerCleanupPaths() linuxReaderSlotCleanupOutcome {
	return live.liveCleanupPaths()
}

func (live *retainedLiveFiles) liveCleanupPaths() linuxLiveClaimCleanupOutcome {
	return linuxReaderSlotCleanupOutcome{
		mainPath:    live.directory.verifyPath(live.mainComponent, live.main),
		sidecarPath: live.directory.verifyPath(live.sidecarComponent, live.sidecar),
	}
}

func (live *retainedLiveFiles) retryWriterLeaseCleanup(
	owned *linuxOwnedWriterLease,
) (linuxLiveClaimCleanupOutcome, *linuxWriterLeaseError) {
	return live.retryWriterLeaseCleanupWith(
		owned,
		func(file *os.File, length uint64) error {
			if length > math.MaxInt64 {
				return &linuxOSError{code: linuxOSOffsetOverflow}
			}
			return file.Truncate(int64(length))
		},
		func(file *os.File) error { return file.Sync() },
	)
}

func (live *retainedLiveFiles) retryWriterLeaseCleanupWith(
	owned *linuxOwnedWriterLease,
	truncate func(*os.File, uint64) error,
	synchronize func(*os.File) error,
) (linuxLiveClaimCleanupOutcome, *linuxWriterLeaseError) {
	live.lastScanValid = false
	live.lastBootstrap = Bootstrap{}
	live.lastInspection = linuxReadySidecarScan{}
	if live.main.lock != linuxLockShared {
		return linuxLiveClaimCleanupOutcome{}, &linuxWriterLeaseError{
			code: linuxWriterOS, source: &linuxOSError{code: linuxOSLifetimeLockRequired},
		}
	}
	if live.sidecar.lock == 0 {
		if err := live.sidecar.acquireLock(linuxLockExclusive, false); err != nil {
			return linuxLiveClaimCleanupOutcome{}, &linuxWriterLeaseError{code: linuxWriterOS, source: err}
		}
	}
	alreadyAbsent, writerErr := live.ownedWriterLeaseAlreadyAbsent(owned)
	if writerErr != nil {
		return linuxLiveClaimCleanupOutcome{}, writerErr
	}
	if alreadyAbsent {
		live.writerBootstrap = nil
		return live.liveCleanupPaths(), nil
	}
	if _, writerErr := live.freezeWriterTailForCleanup(owned); writerErr != nil {
		return linuxLiveClaimCleanupOutcome{}, writerErr
	}
	if writerErr := live.resolveOwnedWriterTailWith(owned, truncate, synchronize, false); writerErr != nil {
		return linuxLiveClaimCleanupOutcome{}, writerErr
	}
	if _, writerErr := live.freezeWriterTailForCleanup(owned); writerErr != nil {
		return linuxLiveClaimCleanupOutcome{}, writerErr
	}
	if live.writerTail != nil {
		return linuxLiveClaimCleanupOutcome{}, &linuxWriterLeaseError{code: linuxWriterTailCleanupRequired}
	}
	switch live.sidecar.cleanupAuthority.kind {
	case linuxCleanupArmed:
		if _, executionErr := live.sidecar.retrySidecarSlotCleanup(linuxSlotHostLimits()); executionErr != nil {
			return linuxLiveClaimCleanupOutcome{}, &linuxWriterLeaseError{
				code: linuxWriterArmedCleanup, source: executionErr,
			}
		}
	case linuxCleanupDeadWriter:
		return linuxLiveClaimCleanupOutcome{}, &linuxWriterLeaseError{code: linuxWriterOutstandingWriterCleanup}
	case linuxCleanupNone:
		if owned == nil {
			return linuxLiveClaimCleanupOutcome{}, &linuxWriterLeaseError{code: linuxWriterNoCleanupAuthority}
		}
		if writerErr := live.clearOwnedWriterLease(owned); writerErr != nil {
			return linuxLiveClaimCleanupOutcome{}, writerErr
		}
	default:
		return linuxLiveClaimCleanupOutcome{}, &linuxWriterLeaseError{
			code:   linuxWriterTransitionBeforeArm,
			source: &slotTransitionError{code: slotTransitionErrProvenanceOccupied},
		}
	}
	live.writerBootstrap = nil
	return live.liveCleanupPaths(), nil
}

func (live *retainedLiveFiles) ownedWriterLeaseAlreadyAbsent(
	owned *linuxOwnedWriterLease,
) (bool, *linuxWriterLeaseError) {
	return live.ownedWriterLeaseAlreadyAbsentWithStat(owned, unix.Fstat)
}

func (live *retainedLiveFiles) ownedWriterLeaseAlreadyAbsentWithStat(
	owned *linuxOwnedWriterLease,
	statRetainedMain func(int, *unix.Stat_t) error,
) (bool, *linuxWriterLeaseError) {
	if err := live.sidecar.requireExclusiveLock(); err != nil {
		return false, &linuxWriterLeaseError{code: linuxWriterOS, source: err}
	}
	if live.writerTail != nil || live.sidecar.cleanupAuthority.kind != linuxCleanupNone {
		return false, nil
	}
	if owned == nil {
		return false, &linuxWriterLeaseError{code: linuxWriterNoCleanupAuthority}
	}
	if owned.header != live.header || owned.active.txnID == 0 {
		return false, &linuxWriterLeaseError{code: linuxWriterOwnerMismatch}
	}
	if live.writerBootstrap == nil || owned.bootstrap != *live.writerBootstrap ||
		owned.active.txnID != live.writerBootstrap.Meta.TxnID {
		return false, &linuxWriterLeaseError{code: linuxWriterOwnerMismatch}
	}
	current, err := live.sidecar.readSidecarSlot(owned.header, 0)
	if err != nil {
		return false, &linuxWriterLeaseError{code: linuxWriterOS, source: err}
	}
	if current != ([sidecarSlotSize]byte{}) {
		return false, nil
	}
	if writerErr := live.requireExactZeroWriterMainLengthWithStat(statRetainedMain); writerErr != nil {
		return false, writerErr
	}
	return true, nil
}

func (live *retainedLiveFiles) requireExactZeroWriterMainLengthWithStat(
	statRetainedMain func(int, *unix.Stat_t) error,
) *linuxWriterLeaseError {
	if live.writerBootstrap == nil {
		return &linuxWriterLeaseError{code: linuxWriterNoCleanupAuthority}
	}
	if err := live.main.checkCreator(); err != nil {
		return &linuxWriterLeaseError{code: linuxWriterOS, source: err}
	}
	var stat unix.Stat_t
	if err := statRetainedMain(int(live.main.file.Fd()), &stat); err != nil {
		return &linuxWriterLeaseError{
			code: linuxWriterOS, source: linuxIOError("inspect retained writer main", err),
		}
	}
	if err := validateRegularStat(&stat); err != nil {
		return &linuxWriterLeaseError{code: linuxWriterOS, source: err}
	}
	if identityFromStat(&stat) != live.main.identity {
		return &linuxWriterLeaseError{
			code: linuxWriterOS, source: &linuxOSError{code: linuxOSPathIdentityMismatch},
		}
	}
	if stat.Size < 0 {
		return &linuxWriterLeaseError{
			code: linuxWriterOS, source: &linuxOSError{code: linuxOSOffsetOverflow},
		}
	}
	target := live.writerBootstrap.CommittedBytes
	actual := uint64(stat.Size)
	if actual != target {
		observedEnd := actual
		if observedEnd < target {
			observedEnd = target
		}
		return &linuxWriterLeaseError{
			code: linuxWriterTailLengthConflict, target: target,
			observedEnd: observedEnd, actual: actual,
		}
	}
	return nil
}

func (live *retainedLiveFiles) freezeWriterTailForCleanup(
	owned *linuxOwnedWriterLease,
) (Bootstrap, *linuxWriterLeaseError) {
	switch live.sidecar.cleanupAuthority.kind {
	case linuxCleanupArmed:
		if !linuxArmedWriterCleanup(live) {
			return Bootstrap{}, &linuxWriterLeaseError{code: linuxWriterOwnerMismatch}
		}
		return live.captureArmedWriterTail()
	case linuxCleanupDeadWriter:
		return Bootstrap{}, &linuxWriterLeaseError{code: linuxWriterOutstandingWriterCleanup}
	case linuxCleanupNone:
		if owned == nil {
			return Bootstrap{}, &linuxWriterLeaseError{code: linuxWriterNoCleanupAuthority}
		}
		return live.captureOwnedWriterTail(owned, false)
	default:
		return Bootstrap{}, &linuxWriterLeaseError{code: linuxWriterOwnerMismatch}
	}
}

func (live *retainedLiveFiles) resolveOwnedWriterTailWith(
	owned *linuxOwnedWriterLease,
	truncate func(*os.File, uint64) error,
	synchronize func(*os.File) error,
	requireCanonicalPaths bool,
) *linuxWriterLeaseError {
	if live.writerTail == nil {
		return nil
	}
	if err := live.sidecar.requireExclusiveLock(); err != nil {
		return &linuxWriterLeaseError{code: linuxWriterOS, source: err}
	}
	switch live.sidecar.cleanupAuthority.kind {
	case linuxCleanupArmed:
		armed := live.sidecar.cleanupAuthority.armed
		if armed == nil || armed.roleValue() != slotWriter || armed.slotIndexValue() != 0 {
			return &linuxWriterLeaseError{code: linuxWriterOwnerMismatch}
		}
	case linuxCleanupDeadWriter:
		return &linuxWriterLeaseError{code: linuxWriterOutstandingWriterCleanup}
	case linuxCleanupNone:
		if owned == nil {
			return &linuxWriterLeaseError{code: linuxWriterNoCleanupAuthority}
		}
		if _, writerErr := live.verifyOwnedWriterWithPaths(owned, requireCanonicalPaths); writerErr != nil {
			return writerErr
		}
	default:
		return &linuxWriterLeaseError{code: linuxWriterOwnerMismatch}
	}

	tail := *live.writerTail
	var stat unix.Stat_t
	if err := unix.Fstat(int(live.main.file.Fd()), &stat); err != nil {
		return &linuxWriterLeaseError{
			code: linuxWriterOS, source: linuxIOError("inspect retained writer tail", err),
		}
	}
	if err := validateRegularStat(&stat); err != nil {
		return &linuxWriterLeaseError{code: linuxWriterOS, source: err}
	}
	if identityFromStat(&stat) != tail.mainIdentity {
		return &linuxWriterLeaseError{code: linuxWriterGenerationChanged}
	}
	if stat.Size < 0 {
		return &linuxWriterLeaseError{code: linuxWriterOS, source: &linuxOSError{code: linuxOSOffsetOverflow}}
	}
	actual := uint64(stat.Size)
	if actual < tail.committedLength || actual > tail.observedEndExclusive {
		return &linuxWriterLeaseError{
			code: linuxWriterTailLengthConflict, target: tail.committedLength,
			observedEnd: tail.observedEndExclusive, actual: actual,
		}
	}
	before, err := live.main.readMainBootstrap(OpenWriter)
	if err != nil {
		return &linuxWriterLeaseError{code: linuxWriterOS, source: err}
	}
	if writerErr := requireSameOwnedLinuxTailGeneration(tail, live.main.identity, before); writerErr != nil {
		return writerErr
	}
	if err := live.verifyOwnedWriterBinding(before, live.header, requireCanonicalPaths); err != nil {
		return &linuxWriterLeaseError{code: linuxWriterOS, source: err}
	}
	if before.PhysicalBytes > before.CommittedBytes {
		if _, err := live.sidecar.readReadySidecarHeader(&live.header); err != nil {
			return &linuxWriterLeaseError{code: linuxWriterOS, source: err}
		}
		if err := truncate(live.main.file, before.CommittedBytes); err != nil {
			return &linuxWriterLeaseError{
				code: linuxWriterOS, source: linuxIOError("truncate owned unpublished main tail", err),
			}
		}
	}
	if err := synchronize(live.main.file); err != nil {
		return &linuxWriterLeaseError{
			code: linuxWriterOS, source: linuxIOError("synchronize owned main tail cleanup", err),
		}
	}
	after, err := live.main.readMainBootstrap(OpenWriter)
	if err != nil {
		return &linuxWriterLeaseError{code: linuxWriterOS, source: err}
	}
	if after.PhysicalBytes != before.CommittedBytes || after.CommittedBytes != before.CommittedBytes ||
		after.Meta != before.Meta || after.Selection != before.Selection ||
		after.SelectedMetaPage != before.SelectedMetaPage {
		return &linuxWriterLeaseError{code: linuxWriterGenerationChanged}
	}
	if err := live.verifyOwnedWriterBinding(after, live.header, requireCanonicalPaths); err != nil {
		return &linuxWriterLeaseError{code: linuxWriterOS, source: err}
	}
	var finalBootstrap Bootstrap
	var writerErr *linuxWriterLeaseError
	if live.sidecar.cleanupAuthority.kind == linuxCleanupArmed {
		finalBootstrap, writerErr = live.captureArmedWriterTail()
	} else if owned != nil {
		finalBootstrap, writerErr = live.captureOwnedWriterTail(owned, requireCanonicalPaths)
	} else {
		return &linuxWriterLeaseError{code: linuxWriterNoCleanupAuthority}
	}
	if writerErr != nil {
		return writerErr
	}
	if finalBootstrap.PhysicalBytes != tail.committedLength {
		return &linuxWriterLeaseError{code: linuxWriterTailCleanupRequired}
	}
	live.writerTail = nil
	return nil
}

func (live *retainedLiveFiles) clearOwnedWriterLease(
	owned *linuxOwnedWriterLease,
) *linuxWriterLeaseError {
	currentBootstrap, writerErr := live.captureOwnedWriterTail(owned, false)
	if writerErr != nil {
		return writerErr
	}
	if live.writerTail != nil || currentBootstrap.PhysicalBytes != currentBootstrap.CommittedBytes {
		return &linuxWriterLeaseError{code: linuxWriterTailCleanupRequired}
	}
	if owned == nil || owned.header != live.header || owned.active.txnID == 0 {
		return &linuxWriterLeaseError{code: linuxWriterOwnerMismatch}
	}
	current, err := live.sidecar.readSidecarSlot(owned.header, 0)
	if err != nil {
		return &linuxWriterLeaseError{code: linuxWriterOS, source: err}
	}
	if current == ([sidecarSlotSize]byte{}) {
		return nil
	}
	prepared, transitionErr := prepareSlotClearOwned(
		owned.header, slotWriter, 0, &current, owned.active, linuxSlotHostLimits(),
	)
	if transitionErr != nil {
		return &linuxWriterLeaseError{code: linuxWriterTransitionBeforeArm, source: transitionErr}
	}
	if executionErr := live.sidecar.executeSidecarSlotTransitionAfterTail(
		prepared, linuxSlotHostLimits(),
	); executionErr != nil {
		return &linuxWriterLeaseError{code: linuxWriterTransition, source: executionErr}
	}
	return nil
}

func requireSameOwnedLinuxTailGeneration(
	tail linuxUnpublishedMainTail,
	identity posixIdentity,
	bootstrap Bootstrap,
) *linuxWriterLeaseError {
	if tail.mainIdentity != identity || tail.databaseID != bootstrap.Meta.DatabaseID ||
		tail.transactionID != bootstrap.Meta.TxnID || tail.commitNonce != bootstrap.Meta.CommitNonce ||
		tail.committedLength != bootstrap.CommittedBytes {
		return &linuxWriterLeaseError{code: linuxWriterGenerationChanged}
	}
	if bootstrap.PhysicalBytes < tail.committedLength || bootstrap.PhysicalBytes > tail.observedEndExclusive {
		return &linuxWriterLeaseError{
			code: linuxWriterTailLengthConflict, target: tail.committedLength,
			observedEnd: tail.observedEndExclusive, actual: bootstrap.PhysicalBytes,
		}
	}
	return nil
}

func requireSameLinuxTailGeneration(
	tail linuxUnpublishedMainTail,
	identity posixIdentity,
	bootstrap Bootstrap,
) *linuxLivePairError {
	if tail.mainIdentity != identity || tail.databaseID != bootstrap.Meta.DatabaseID ||
		tail.transactionID != bootstrap.Meta.TxnID || tail.commitNonce != bootstrap.Meta.CommitNonce ||
		tail.committedLength != bootstrap.CommittedBytes {
		return &linuxLivePairError{code: linuxLivePairMainGenerationChanged}
	}
	if bootstrap.PhysicalBytes < tail.committedLength || bootstrap.PhysicalBytes > tail.observedEndExclusive {
		return &linuxLivePairError{
			code: linuxLivePairTailLengthConflict, target: tail.committedLength,
			observedEnd: tail.observedEndExclusive, actual: bootstrap.PhysicalBytes,
		}
	}
	return nil
}

func requireCurrentLinuxPIDDomain(header sidecarHeader) error {
	current, err := linuxProcessDomainToken()
	if err != nil {
		return err
	}
	if header.processDomainKind != processDomainLinuxPIDNamespace || header.processDomainToken != current {
		return &readySidecarError{code: readySidecarErrProcessDomainMismatch}
	}
	return nil
}

func (retained *retainedRegular) readDecodedSidecarSlot(
	header sidecarHeader,
	index uint32,
	role slotRole,
	host slotHostLimits,
) ([sidecarSlotSize]byte, stableSlot, error) {
	var raw [sidecarSlotSize]byte
	stable, err := retained.readDecodedSidecarSlotInto(header, index, role, host, &raw)
	return raw, stable, err
}

func (retained *retainedRegular) readDecodedSidecarSlotInto(
	header sidecarHeader,
	index uint32,
	role slotRole,
	host slotHostLimits,
	raw *[sidecarSlotSize]byte,
) (stableSlot, error) {
	if raw == nil {
		return stableSlot{}, &linuxOSError{code: linuxOSIO}
	}
	offset, err := sidecarSlotOffset(header, index)
	if err != nil {
		return stableSlot{}, err
	}
	if err := retained.readExactAt(raw[:], offset); err != nil {
		return stableSlot{}, err
	}
	stable, problem := decodeLinuxStableSlotImage(raw, role, host)
	if problem != 0 {
		return stableSlot{}, &readySidecarError{
			code: readySidecarErrSlot, index: index, problem: problem,
		}
	}
	return stable, nil
}

func decodeLinuxStableSlotImage(
	data *[sidecarSlotSize]byte,
	role slotRole,
	host slotHostLimits,
) (stableSlot, slotProblem) {
	if data == nil {
		return stableSlot{}, slotUnknownState
	}
	state := u32(data[:], 0)
	if state == 0 {
		if anyNonzero(data[:]) {
			return stableSlot{}, slotFreeNonzero
		}
		return stableSlot{}, 0
	}
	if state == 2 {
		return stableSlot{}, slotTransition
	}
	if state != 1 {
		return stableSlot{}, slotUnknownState
	}
	if u32(data[:], 4) != 0 || u32(data[:], 56) != 0 {
		return stableSlot{}, slotReserved
	}
	txnID := u64(data[:], 8)
	if role == slotWriter && txnID == 0 {
		return stableSlot{}, slotWriterTransactionZero
	}
	processID := u64(data[:], 16)
	if processID == 0 {
		return stableSlot{}, slotProcessIDZero
	}
	if processID > host.processIDMax {
		return stableSlot{}, slotProcessIDUnrepresentable
	}
	taskID := u64(data[:], 32)
	if taskID > host.taskIDMax {
		return stableSlot{}, slotTaskIDUnrepresentable
	}
	nonce := read16(data[:], 40)
	if nonce == [16]byte{} {
		return stableSlot{}, slotNonceZero
	}
	if crc32cLinuxSlotImage(data) != u32(data[:], slotCRCOffset) {
		return stableSlot{}, slotChecksum
	}
	return stableSlot{active: true, claim: activeSlot{
		txnID: txnID, processID: processID, processStart: u64(data[:], 24),
		taskID: taskID, nonce: nonce,
	}}, 0
}

func crc32cLinuxSlotImage(data *[sidecarSlotSize]byte) uint32 {
	crc := ^uint32(0)
	for index, value := range data {
		if index >= slotCRCOffset && index < slotCRCOffset+4 {
			value = 0
		}
		crc = castagnoliTable[byte(crc)^value] ^ (crc >> 8)
	}
	return ^crc
}

func (retained *retainedRegular) requireExclusiveLock() *linuxOSError {
	if err := retained.checkCreator(); err != nil {
		return err
	}
	if retained.lock != linuxLockExclusive {
		return &linuxOSError{code: linuxOSOperationLockRequired}
	}
	return nil
}

func sidecarSlotOffset(header sidecarHeader, index uint32) (uint64, *linuxOSError) {
	if index > header.capacity {
		return 0, &linuxOSError{code: linuxOSSlotOffsetOverflow}
	}
	bytes, ok := checkedMul(uint64(index), uint64(sidecarSlotSize))
	if !ok {
		return 0, &linuxOSError{code: linuxOSSlotOffsetOverflow}
	}
	offset, ok := checkedAdd(headerRegionSize, bytes)
	if !ok {
		return 0, &linuxOSError{code: linuxOSSlotOffsetOverflow}
	}
	return offset, nil
}

func ptrSidecarHeader(header sidecarHeader) *sidecarHeader { return &header }

func (retained *retainedRegular) checkCreator() *linuxOSError {
	if os.Getpid() != retained.creatorPID {
		return &linuxOSError{code: linuxOSForkedHandle}
	}
	return nil
}

func randomNonzero128() ([16]byte, *linuxOSError) {
	return randomNonzero128With(func(nonce []byte) error {
		_, err := io.ReadFull(rand.Reader, nonce)
		return err
	})
}

func randomNonzero128With(fill func([]byte) error) ([16]byte, *linuxOSError) {
	var nonce [16]byte
	if err := fill(nonce[:]); err != nil {
		return [16]byte{}, &linuxOSError{code: linuxOSRandomFailure, source: err}
	}
	if nonce == [16]byte{} {
		return [16]byte{}, &linuxOSError{code: linuxOSRandomZero}
	}
	return nonce, nil
}

func linuxProcessDomainToken() ([32]byte, *linuxOSError) {
	var stat unix.Stat_t
	if err := unix.Stat("/proc/self/ns/pid", &stat); err != nil {
		return [32]byte{}, linuxIOError("inspect Linux PID namespace", err)
	}
	return identityFromStat(&stat).encode(), nil
}

func linuxSlotHostLimits() slotHostLimits {
	return slotHostLimits{processIDMax: math.MaxInt32, taskIDMax: math.MaxInt32}
}

func currentLinuxActiveSlot(txnID uint64, nonce [16]byte) activeSlot {
	processID := uint64(os.Getpid())
	var workspace linuxProcessObservationWorkspace
	processStart, _ := readLinuxProcessStartWithWorkspace(processID, &workspace)
	taskID := uint64(unix.Gettid())
	return activeSlot{
		txnID: txnID, processID: processID, processStart: processStart,
		taskID: taskID, nonce: nonce,
	}
}

func observeLinuxProcess(active activeSlot) posixProcessObservation {
	var workspace linuxProcessObservationWorkspace
	return observeLinuxProcessWithWorkspace(active, &workspace)
}

type linuxProcessObservationWorkspace struct {
	path [32]byte
	stat [4096]byte
}

func observeLinuxProcessWithWorkspace(
	active activeSlot,
	workspace *linuxProcessObservationWorkspace,
) posixProcessObservation {
	if active.processID == 0 || active.processID > math.MaxInt32 {
		return posixProcessObservation{kind: posixProcessUncertain}
	}
	err := unix.Kill(int(active.processID), 0)
	if err == nil || err == unix.EPERM {
		start, _ := readLinuxProcessStartWithWorkspace(active.processID, workspace)
		return posixProcessObservation{kind: posixProcessExists, currentStart: start}
	}
	if err == unix.ESRCH {
		return posixProcessObservation{kind: posixProcessMissing}
	}
	return posixProcessObservation{kind: posixProcessUncertain}
}

func observeLinuxProcessWithoutStart(active activeSlot) posixProcessObservation {
	if active.processID == 0 || active.processID > math.MaxInt32 {
		return posixProcessObservation{kind: posixProcessUncertain}
	}
	err := unix.Kill(int(active.processID), 0)
	if err == nil || err == unix.EPERM {
		return posixProcessObservation{kind: posixProcessExists}
	}
	if err == unix.ESRCH {
		return posixProcessObservation{kind: posixProcessMissing}
	}
	return posixProcessObservation{kind: posixProcessUncertain}
}

func readLinuxProcessStartWithWorkspace(
	processID uint64,
	workspace *linuxProcessObservationWorkspace,
) (uint64, bool) {
	if processID == 0 || processID > math.MaxInt32 {
		return 0, false
	}
	if workspace == nil {
		return 0, false
	}
	pathLength, ok := writeLinuxProcStatPath(&workspace.path, processID)
	if !ok {
		return 0, false
	}
	fd, err := unix.Open(string(workspace.path[:pathLength]), unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return 0, false
	}
	used := 0
	for used != len(workspace.stat) {
		count, err := unix.Read(fd, workspace.stat[used:])
		used += count
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			_ = unix.Close(fd)
			return 0, false
		}
		if count == 0 {
			break
		}
	}
	_ = unix.Close(fd)
	if used == len(workspace.stat) {
		return 0, false
	}
	start, problem := parseLinuxProcStatStartWithoutFields(workspace.stat[:used])
	return start, problem == 0
}

func parseLinuxProcStatStartWithoutFields(data []byte) (uint64, processStartParseError) {
	commandEnd := len(data) - 1
	for commandEnd >= 0 && data[commandEnd] != ')' {
		commandEnd--
	}
	if commandEnd < 0 {
		return 0, processStartMissingCommandEnd
	}
	index := commandEnd + 1
	for field := 0; field <= 19; field++ {
		for index < len(data) && linuxProcStatFieldSpace(data[index]) {
			index++
		}
		start := index
		for index < len(data) && !linuxProcStatFieldSpace(data[index]) {
			index++
		}
		if start == index {
			return 0, processStartMissingField
		}
		if field == 19 {
			return parseNonzeroUint64(data[start:index])
		}
	}
	return 0, processStartMissingField
}

func linuxProcStatFieldSpace(value byte) bool {
	return value == ' ' || value == '\t' || value == '\n' ||
		value == '\v' || value == '\f' || value == '\r'
}

func writeLinuxProcStatPath(path *[32]byte, processID uint64) (int, bool) {
	if path == nil || processID == 0 || processID > math.MaxInt32 {
		return 0, false
	}
	const prefix = "/proc/"
	const suffix = "/stat"
	copy(path[:], prefix)
	digits := 1
	for value := processID; value >= 10; value /= 10 {
		digits++
	}
	end := len(prefix) + digits
	value := processID
	for index := end; index > len(prefix); {
		index--
		path[index] = byte('0' + value%10)
		value /= 10
	}
	copy(path[end:], suffix)
	return end + len(suffix), true
}

func validatePathComponent(component string) *linuxOSError {
	if component == "" || component == "." || component == ".." ||
		strings.ContainsAny(component, "/\x00") {
		return &linuxOSError{code: linuxOSInvalidPathComponent}
	}
	return nil
}

func validateMainPathComponent(component string) *linuxOSError {
	if err := validatePathComponent(component); err != nil {
		return err
	}
	if hasASCIIFoldPrefix(component, ".iprange-") || hasASCIIFoldSuffix(component, ".readers") {
		return &linuxOSError{code: linuxOSInvalidPathComponent}
	}
	return nil
}

func hasASCIIFoldPrefix(value, prefix string) bool {
	return len(value) >= len(prefix) && equalASCIIFold(value[:len(prefix)], prefix)
}

func hasASCIIFoldSuffix(value, suffix string) bool {
	return len(value) >= len(suffix) && equalASCIIFold(value[len(value)-len(suffix):], suffix)
}

func equalASCIIFold(left, right string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range len(left) {
		a, b := left[index], right[index]
		if 'A' <= a && a <= 'Z' {
			a += 'a' - 'A'
		}
		if 'A' <= b && b <= 'Z' {
			b += 'a' - 'A'
		}
		if a != b {
			return false
		}
	}
	return true
}

func validateRegularStat(stat *unix.Stat_t) *linuxOSError {
	if stat.Mode&unix.S_IFMT != unix.S_IFREG {
		return &linuxOSError{code: linuxOSNotRegular}
	}
	if stat.Nlink != 1 {
		return &linuxOSError{code: linuxOSLinkCountNotOne, value: uint64(stat.Nlink)}
	}
	return nil
}

func identityFromStat(stat *unix.Stat_t) posixIdentity {
	return posixIdentity{device: uint64(stat.Dev), inode: stat.Ino}
}

func requireSupportedLinuxFilesystem(fd int) *linuxOSError {
	var stat unix.Statfs_t
	if err := unix.Fstatfs(fd, &stat); err != nil {
		return linuxIOError("inspect live-coordination filesystem", err)
	}
	const zfsSuperMagic = 0x2fc12fc1
	filesystem := uint32(stat.Type)
	switch filesystem {
	case unix.EXT4_SUPER_MAGIC, unix.XFS_SUPER_MAGIC, unix.BTRFS_SUPER_MAGIC,
		unix.F2FS_SUPER_MAGIC, zfsSuperMagic, unix.BCACHEFS_SUPER_MAGIC:
		return nil
	default:
		return &linuxOSError{code: linuxOSUnsupportedFilesystem, value: uint64(filesystem)}
	}
}

func linuxIOError(operation string, source error) *linuxOSError {
	return &linuxOSError{code: linuxOSIO, operation: operation, source: source}
}

func linuxContextCancellation(ctx context.Context) *linuxOSError {
	if err := ctx.Err(); err != nil {
		return &linuxOSError{code: linuxOSCancelled, source: err}
	}
	return nil
}

func (identity posixIdentity) String() string {
	return fmt.Sprintf("%d:%d", identity.device, identity.inode)
}
