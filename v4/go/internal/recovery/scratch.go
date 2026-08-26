package recovery

// Authorized recovery scratch owner (Rust recovery/scratch.rs): exact
// ownership and bounded I/O for the lazily established scratch files
// of one recovery operation. Each file is created creator-only inside
// the retained scratch directory, carries the 128-byte ownership
// header, and is accessed exclusively through read-write mapping
// windows (8 MiB rounds) under the worker scratch probe. The resource
// discipline mirrors the Rust owner exactly: retainedBytes accounts
// only mapped capacity, growth rounds to the mapping window inside
// the byte budget, and the cleanup terminal folds every unproved
// removal into a residue.

import (
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"syscall"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/live"
	"github.com/firehol/iprange/v4/go/internal/mapping"
	"github.com/firehol/iprange/v4/go/internal/publication"
	"github.com/firehol/iprange/v4/go/internal/security"
)

const (
	scratchMaxOwned     = 2
	scratchMappingWin   = 8 * 1024 * 1024
	scratchMinOpenFiles = 3
	scratchSortOpen     = 4
)

// scratchSlot is one retained scratch slot (Rust ScratchSlot).
type scratchSlot struct {
	index int
}

// scratchProblem is the exact cleanup problem retained for one
// scratch residue (Rust ScratchProblem: the code class, the optional
// errno, and the exact detail).
type scratchProblem struct {
	code   format.ErrorCode
	osCode *int32
	detail string
}

// scratchResidue is one authorized scratch artifact whose durable
// absence was not proved (Rust ScratchResidue).
type scratchResidue struct {
	ordinal                    uint32
	directoryIdentity          publication.LocalFileIdentity
	basename                   []byte
	identity                   publication.LocalFileIdentity
	creationSecurityKind       uint16
	creationSecurityCommitment [32]byte
	problem                    scratchProblem
}

// scratchCleanup is the terminal facts of one scratch attempt (Rust
// ScratchCleanup).
type scratchCleanup struct {
	attemptID                  [16]byte
	directoryIdentity          publication.LocalFileIdentity
	creationSecurityKind       uint16
	creationSecurityCommitment [32]byte
	residues                   []scratchResidue
	housekeeping               publication.Housekeeping
	visibleHousekeeping        []publication.HousekeepingArtifact
}

// clean reports whether every artifact absence was proved (Rust
// ScratchCleanup::clean).
func (s *scratchCleanup) clean() bool { return len(s.residues) == 0 }

// scratch is one lazily established recovery scratch namespace (Rust
// Scratch).
type scratch struct {
	directory     *live.Directory
	profile       security.Profile
	attemptID     [16]byte
	nextOrdinal   uint64
	source        format.Meta
	maxBytes      uint64
	maxFiles      int
	maxOpenFiles  uint32
	retainedBytes uint64
	owned         [scratchMaxOwned]*scratchOwned
}

// scratchOwned is one retained scratch slot owner (Rust Owned).
type scratchOwned struct {
	shared   *scratchSharedFile
	name     string
	identity live.FileIdentity
	ordinal  uint32
}

// scratchSharedFile is the mapped file of one scratch slot (Rust
// SharedFile): the file descriptor, the exclusive mapping, and the
// logical length and mapped capacity counters.
type scratchSharedFile struct {
	file     *os.File
	mu       sync.Mutex
	mapping  *mapping.Mapping
	length   atomic.Uint64
	capacity atomic.Uint64
}

// close releases the mapped file of one slot (Rust SharedFile drop:
// the cleanup consumes every owned slot and closes its handles
// deterministically instead of waiting for the Go garbage collector;
// an open handle keeps the artifact undeletable on Windows).
func (f *scratchSharedFile) close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.mapping != nil {
		_ = f.mapping.Close()
		f.mapping = nil
	}
	_ = f.file.Close()
}

// scratchStart opens the scratch namespace of one recovery operation
// (Rust Scratch::start): the file and byte budgets, the retained
// directory open, the creator profile capture, the fresh attempt
// identity, and the worker scratch checkpoint.
func scratchStart(directoryPath string, source format.Meta, maxBytes uint64, maxFiles uint32, maxOpenFiles uint32) (*scratch, error) {
	if maxFiles == 0 || maxOpenFiles < scratchMinOpenFiles {
		return nil, &format.Error{Code: format.CodeInsufficientResourceBudget, Detail: "recovery scratch requires one file descriptor"}
	}
	if maxBytes < scratchHeaderSize {
		return nil, &format.Error{Code: format.CodeInsufficientResourceBudget, Detail: "recovery scratch bytes"}
	}
	directory, err := live.OpenDirectory(directoryPath)
	if err != nil {
		return nil, scratchNamespaceError(err)
	}
	profile, err := security.Capture()
	if err != nil {
		directory.Close()
		return nil, err
	}
	attemptID, err := newScratchAttempt(directory)
	if err != nil {
		directory.Close()
		return nil, err
	}
	files := int(maxFiles)
	if files > scratchMaxOwned {
		files = scratchMaxOwned
	}
	owned := scratch{
		directory:    directory,
		profile:      profile,
		attemptID:    attemptID,
		source:       source,
		maxBytes:     maxBytes,
		maxFiles:     files,
		maxOpenFiles: maxOpenFiles,
	}
	if err := startScratchCheckpoint(attemptID, scratchLocal(directory.Identity()), &publication.CreationSecurity{
		Kind:       scratchCreationSecurityKind(),
		Commitment: profile.Commitment(),
	}); err != nil {
		directory.Close()
		return nil, err
	}
	return &owned, nil
}

// create establishes one owned scratch file (Rust Scratch::create):
// a free slot, the ordinal, the created creator-only artifact, the
// worker checkpoint entry, and the initial mapped header.
func (s *scratch) create() (scratchSlot, error) {
	slot, err := s.freeSlot()
	if err != nil {
		return scratchSlot{}, err
	}
	ordinal, err := s.takeOrdinal()
	if err != nil {
		return scratchSlot{}, err
	}
	if err := s.install(slot, ordinal); err != nil {
		return scratchSlot{}, err
	}
	headerBytes := scratchHeader(s.source, s.attemptID, ordinal, s.profile.Commitment())
	if err := security.SecureCreatorOnly(s.owned[slot].shared.file, s.profile); err != nil {
		return scratchSlot{}, scratchNamespaceError(err)
	}
	if err := s.owned[slot].shared.write(0, headerBytes[:]); err != nil {
		return scratchSlot{}, err
	}
	return scratchSlot{index: slot}, nil
}

// requireExternalSort proves the two-file sort budget (Rust
// Scratch::require_external_sort).
func (s *scratch) requireExternalSort() error {
	if s.maxFiles < 2 || s.maxOpenFiles < scratchSortOpen {
		return &format.Error{Code: format.CodeInsufficientResourceBudget, Detail: "external recovery sort requires two scratch files"}
	}
	return nil
}

// remainingBytes is the unretained byte headroom (Rust
// Scratch::remaining_bytes).
func (s *scratch) remainingBytes() uint64 {
	return s.maxBytes - s.retainedBytes
}

// freeSlot finds the first unowned slot inside the file budget (Rust
// Scratch::free_slot).
func (s *scratch) freeSlot() (int, error) {
	for index := 0; index < s.maxFiles; index++ {
		if s.owned[index] == nil {
			return index, nil
		}
	}
	return 0, &format.Error{Code: format.CodeInsufficientResourceBudget, Detail: "recovery scratch files"}
}

// takeOrdinal advances the attempt ordinal (Rust Scratch::take_ordinal).
func (s *scratch) takeOrdinal() (uint32, error) {
	if s.nextOrdinal > uint64(^uint32(0)) {
		return 0, &format.Error{Code: format.CodeArithmeticOverflow, Detail: "recovery scratch ordinal"}
	}
	ordinal := uint32(s.nextOrdinal)
	s.nextOrdinal++
	return ordinal, nil
}

// install creates the file, captures its identity, and records the
// worker checkpoint entry (Rust Scratch::install).
func (s *scratch) install(slot int, ordinal uint32) error {
	if err := s.compactMappingSlack(-1); err != nil {
		return err
	}
	if err := s.requireGrowth(0, scratchHeaderSize); err != nil {
		return err
	}
	name, err := scratchNameOf(s.attemptID, ordinal)
	if err != nil {
		return err
	}
	file, err := scratchCreateFile(s.directory, name, s.profile)
	if err != nil {
		return scratchNamespaceError(err)
	}
	identity, err := live.RegularIdentity(file, s.directory.Identity())
	if err != nil {
		file.Close()
		return scratchNamespaceError(err)
	}
	s.owned[slot] = &scratchOwned{
		shared:   newScratchSharedFile(file),
		name:     name,
		identity: identity,
		ordinal:  ordinal,
	}
	if err := addScratchCheckpoint(ordinal, scratchLocal(identity)); err != nil {
		return err
	}
	if err := s.owned[slot].shared.remap(scratchHeaderSize, scratchHeaderSize); err != nil {
		return err
	}
	s.retainedBytes += scratchHeaderSize
	return nil
}

// length is the logical length of one slot (Rust Scratch::length).
func (s *scratch) length(slot scratchSlot) uint64 {
	return s.owner(slot).shared.length.Load()
}

// write appends or overwrites payload bytes inside the retained
// length (Rust Scratch::write): the ownership header is immutable,
// the length grows to the written end, and the mapped capacity grows
// in windows inside the byte budget.
func (s *scratch) write(slot scratchSlot, offset uint64, bytes []byte) error {
	if offset < scratchHeaderSize {
		return &format.Error{Code: format.CodeInvalidArgument, Detail: "scratch records cannot overwrite their ownership header"}
	}
	end, ok := checkedAdd(offset, uint64(len(bytes)))
	if !ok {
		return &format.Error{Code: format.CodeArithmeticOverflow, Detail: "recovery scratch write"}
	}
	old := s.length(slot)
	new := old
	if end > new {
		new = end
	}
	if err := s.ensureWriteCapacity(slot, end); err != nil {
		return err
	}
	if err := s.owner(slot).shared.write(offset, bytes); err != nil {
		return err
	}
	s.owner(slot).shared.length.Store(new)
	return nil
}

// read copies payload bytes from inside the retained length (Rust
// Scratch::read).
func (s *scratch) read(slot scratchSlot, offset uint64, output []byte) error {
	end, ok := checkedAdd(offset, uint64(len(output)))
	if !ok {
		return &format.Error{Code: format.CodeArithmeticOverflow, Detail: "recovery scratch read"}
	}
	if offset < scratchHeaderSize || end > s.length(slot) {
		return &format.Error{Code: format.CodeFormatInvalid, Detail: "scratch read exceeds its retained length"}
	}
	return s.owner(slot).shared.read(offset, output)
}

// reset restores one slot to its bare header (Rust Scratch::reset).
func (s *scratch) reset(slot scratchSlot) error {
	return s.resize(slot, scratchHeaderSize)
}

// resize changes one slot's logical length (Rust Scratch::resize):
// the header is always retained, the mapped capacity follows the
// length inside the byte budget.
func (s *scratch) resize(slot scratchSlot, length uint64) error {
	if length < scratchHeaderSize {
		return &format.Error{Code: format.CodeInvalidArgument, Detail: "scratch length cannot exclude its ownership header"}
	}
	old := s.capacity(slot)
	if err := s.requireGrowth(old, length); err != nil {
		return err
	}
	if err := s.owner(slot).shared.remap(length, length); err != nil {
		return err
	}
	s.retainedBytes = s.retainedBytes - old + length
	return nil
}

// detach hands one slot's shared file to the external sort (Rust
// Scratch::detach).
func (s *scratch) detach(slot scratchSlot) scratchFile {
	return scratchFile{index: slot.index, shared: s.owner(slot).shared}
}

// attach returns a detached shared file to its slot (Rust
// Scratch::attach): the ownership must be unchanged.
func (s *scratch) attach(detached scratchFile) scratchSlot {
	slot := detached.slot()
	if s.owner(slot).shared != detached.shared {
		panic("detached scratch ownership changed")
	}
	return slot
}

// cleanup consumes the scratch owner and produces its terminal facts
// (Rust Scratch::cleanup; the platform arm runs the exact removal
// machine).
func (s *scratch) cleanup() *scratchCleanup {
	cleanup := s.cleanupPlatform()
	s.directory.Close()
	return cleanup
}

// owner resolves one owned slot (Rust Scratch::owner; a missing slot
// is a machine invariant violation).
func (s *scratch) owner(slot scratchSlot) *scratchOwned {
	if slot.index < 0 || slot.index >= scratchMaxOwned || s.owned[slot.index] == nil {
		panic("scratch slot is owned")
	}
	return s.owned[slot.index]
}

// capacity is the mapped capacity of one slot (Rust Scratch::capacity).
func (s *scratch) capacity(slot scratchSlot) uint64 {
	return s.owner(slot).shared.capacity.Load()
}

// ensureWriteCapacity grows the mapped capacity of one slot inside
// the byte budget (Rust Scratch::ensure_write_capacity): the required
// extent rounds up to the 8 MiB mapping window, never beyond the
// available bytes, after compacting the slack of every other slot.
func (s *scratch) ensureWriteCapacity(slot scratchSlot, required uint64) error {
	old := s.capacity(slot)
	if required <= old {
		return nil
	}
	available, err := s.availableCapacity(old)
	if err != nil {
		return err
	}
	if required > available {
		if err := s.compactMappingSlack(slot.index); err != nil {
			return err
		}
		available, err = s.availableCapacity(old)
		if err != nil {
			return err
		}
	}
	if required > available {
		return &format.Error{Code: format.CodeInsufficientResourceBudget, Detail: "recovery scratch bytes"}
	}
	rounded := uint64(^uint64(0))
	if required <= ^uint64(0)-(scratchMappingWin-1) {
		rounded = (required + scratchMappingWin - 1) / scratchMappingWin * scratchMappingWin
	}
	capacity := rounded
	if capacity > available {
		capacity = available
	}
	if capacity < required {
		capacity = required
	}
	length := s.length(slot)
	if err := s.owner(slot).shared.remap(capacity, length); err != nil {
		return err
	}
	s.retainedBytes = s.retainedBytes - old + capacity
	return nil
}

// availableCapacity is the byte budget remaining after this slot's
// current capacity is released (Rust Scratch::available_capacity).
func (s *scratch) availableCapacity(replaced uint64) (uint64, error) {
	without := s.retainedBytes - replaced
	if without > s.retainedBytes {
		return 0, &format.Error{Code: format.CodeArithmeticOverflow, Detail: "recovery scratch bytes"}
	}
	if s.maxBytes < without {
		return 0, &format.Error{Code: format.CodeArithmeticOverflow, Detail: "recovery scratch bytes"}
	}
	return s.maxBytes - without, nil
}

// compactMappingSlack shrinks every other slot's mapping to its
// logical length (Rust Scratch::compact_mapping_slack; except -1
// compacts all slots).
func (s *scratch) compactMappingSlack(except int) error {
	for index := 0; index < scratchMaxOwned; index++ {
		if index == except || s.owned[index] == nil {
			continue
		}
		capacity := s.capacity(scratchSlot{index: index})
		length := s.length(scratchSlot{index: index})
		if capacity != length {
			if err := s.owner(scratchSlot{index: index}).shared.remap(length, length); err != nil {
				return err
			}
			s.retainedBytes = s.retainedBytes - capacity + length
		}
	}
	return nil
}

// requireGrowth proves the retained total stays inside the byte
// budget (Rust Scratch::require_growth).
func (s *scratch) requireGrowth(old, new uint64) error {
	without := s.retainedBytes - old
	if without > s.retainedBytes {
		return &format.Error{Code: format.CodeArithmeticOverflow, Detail: "recovery scratch bytes"}
	}
	total := without + new
	if total < without {
		return &format.Error{Code: format.CodeArithmeticOverflow, Detail: "recovery scratch bytes"}
	}
	if total > s.maxBytes {
		return &format.Error{Code: format.CodeInsufficientResourceBudget, Detail: "recovery scratch bytes"}
	}
	return nil
}

// newScratchSharedFile creates the shared mapped-file owner of one
// slot (Rust SharedFile::new with the absent mapping).
func newScratchSharedFile(file *os.File) *scratchSharedFile {
	return &scratchSharedFile{file: file}
}

// read copies mapped payload bytes under the worker scratch probe
// (Rust SharedFile::read: the exclusive mapping lock, the probe, and
// the changed-mapping refusal).
func (f *scratchSharedFile) read(offset uint64, output []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.mapping == nil {
		return &format.Error{Code: format.CodeWrongState, Detail: "scratch mapping is unavailable"}
	}
	mapped := f.mapping
	return mapped.Probe(mapping.RoleScratch, func() error {
		view, err := mapped.View(offset, uint64(len(output)))
		if err != nil {
			return &format.Error{Code: format.CodeFormatInvalid, Detail: "scratch mapping changed while reading"}
		}
		copy(output, view)
		return nil
	})
}

// write copies mapped payload bytes under the worker scratch probe
// (Rust SharedFile::write).
func (f *scratchSharedFile) write(offset uint64, input []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.mapping == nil {
		return &format.Error{Code: format.CodeWrongState, Detail: "scratch mapping is unavailable"}
	}
	mapped := f.mapping
	return mapped.Probe(mapping.RoleScratch, func() error {
		view, err := mapped.View(offset, uint64(len(input)))
		if err != nil {
			return err
		}
		copy(view, input)
		return nil
	})
}

// remap resizes the file extent and re-establishes its read-write
// mapping (Rust SharedFile::remap: set_len, replace the mapping with
// a read-write view, and publish the new length and capacity).
func (f *scratchSharedFile) remap(capacity uint64, length uint64) error {
	if length > capacity {
		return &format.Error{Code: format.CodeInvalidArgument, Detail: "scratch logical length exceeds mapped capacity"}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.mapping != nil {
		if err := f.mapping.Close(); err != nil {
			return err
		}
		f.mapping = nil
	}
	if err := f.file.Truncate(int64(capacity)); err != nil {
		return &format.Error{Code: format.CodeIO, Detail: "truncate scratch file: " + err.Error()}
	}
	mapped, err := mapping.MapFile(f.file, capacity, true)
	if err != nil {
		return err
	}
	f.mapping = mapped
	f.length.Store(length)
	f.capacity.Store(capacity)
	return nil
}

// scratchLocal converts one retained identity to the portable local
// form (Rust scratch::local over namespace::local_identity).
func scratchLocal(identity live.FileIdentity) publication.LocalFileIdentity {
	device, inode := live.IdentityDeviceInode(&identity)
	return publication.LocalFileIdentityFromDeviceInode(device, inode)
}

// scratchNamespaceError maps one retained-directory namespace failure
// to the scratch owner classes (Rust scratch::namespace_error).
func scratchNamespaceError(err error) error {
	nerr, ok := live.AsNamespaceError(err)
	if !ok {
		// The creator-only security owner reports the platform classes
		// directly (Rust folds them into NamespaceError at the machine
		// boundary; both fold to the same scratch classes here).
		var fe *format.Error
		if errors.As(err, &fe) {
			switch fe.Code {
			case format.CodeAccessPolicyUnsupported:
				return &format.Error{Code: format.CodeOSUnsupported, Detail: "scratch directory does not meet the ownership contract"}
			case format.CodeDurabilityUnsupported:
				return &format.Error{Code: format.CodeOSUnsupported, Detail: "scratch directory lacks required local operations"}
			}
		}
		return err
	}
	switch nerr.Kind {
	case live.NamespaceInvalidName:
		return &format.Error{Code: format.CodeInvalidArgument, Detail: "invalid recovery scratch name"}
	case live.NamespaceExists:
		return &format.Error{Code: format.CodeNameExists, Detail: "feed name already exists"}
	case live.NamespaceForkedHandle:
		return &format.Error{Code: format.CodeForkedHandle, Detail: "scratch owner crossed fork"}
	case live.NamespaceIo, live.NamespaceIoAt:
		return &format.Error{Code: format.CodeIO, Detail: nerr.Error()}
	case live.NamespaceUnsupported:
		return &format.Error{Code: format.CodeOSUnsupported, Detail: "scratch directory lacks required local operations"}
	default:
		// NotDirectory, NotRegular, Missing, IdentityChanged,
		// LinkCount, CrossFilesystem, and AccessPolicy all fold into
		// the Rust ownership-contract arm (scratch.rs
		// namespace_error).
		return &format.Error{Code: format.CodeOSUnsupported, Detail: "scratch directory does not meet the ownership contract"}
	}
}

// scratchResidueOf builds the residue of one failed removal (Rust
// cleanup::residue).
func scratchResidueOf(directoryIdentity publication.LocalFileIdentity, profile security.Profile, owner *scratchOwned, problem scratchProblem) scratchResidue {
	return scratchResidue{
		ordinal:                    owner.ordinal,
		directoryIdentity:          directoryIdentity,
		basename:                   []byte(owner.name),
		identity:                   scratchLocal(owner.identity),
		creationSecurityKind:       scratchCreationSecurityKind(),
		creationSecurityCommitment: profile.Commitment(),
		problem:                    problem,
	}
}

// scratchResidueError maps the first residue problem to the SDK error
// of an unclean cleanup (Rust cleanup::residue_error): an Io problem
// with a captured errno surfaces the bare OS error, and every other
// residue of the recovery machine carries a static (borrowed) detail
// which Rust folds into the Corrupt class. The Go peer never builds
// owned worker-operation details on this path.
func scratchResidueError(cleanup *scratchCleanup) error {
	if len(cleanup.residues) == 0 {
		return &format.Error{Code: format.CodeFormatInvalid, Detail: "recovery scratch cleanup is incomplete"}
	}
	problem := cleanup.residues[0].problem
	if problem.code == format.CodeIO && problem.osCode != nil {
		errno := syscall.Errno(*problem.osCode)
		return &format.Error{Code: format.CodeIO, Detail: errno.Error()}
	}
	if problem.detail != "" {
		return &format.Error{Code: format.CodeFormatInvalid, Detail: problem.detail}
	}
	return &format.Error{Code: format.CodeFormatInvalid, Detail: "recovery scratch cleanup is incomplete"}
}

// cleanupIncompleteError folds the failed operation cause and the
// residue problem into the cleanup-incomplete class (Rust
// Error::CleanupIncomplete Display: "{cause}; cleanup also failed:
// {cleanup}").
func cleanupIncompleteError(cause error, cleanup *scratchCleanup) error {
	if cause == nil {
		cause = &format.Error{Code: format.CodeFormatInvalid, Detail: "recovery scratch cleanup is incomplete"}
	}
	return &format.Error{Code: format.CodeCleanupInProgress, Detail: cause.Error() + "; cleanup also failed: " + scratchResidueError(cleanup).Error()}
}

// conflictScratchProblem builds the fixed cleanup-conflict problem
// (Rust cleanup::conflict).
func conflictScratchProblem(detail string) *scratchProblem {
	return &scratchProblem{
		code:   format.CodeCleanupConflict,
		detail: detail,
	}
}

// rawOSError captures the OS error number of one wrapped failure, or
// nil (Rust error.raw_os_error).
func rawOSError(cause error) *int32 {
	var errno syscall.Errno
	if errors.As(cause, &errno) {
		value := int32(errno)
		return &value
	}
	return nil
}
