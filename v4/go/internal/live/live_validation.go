package live

// Validation live source (Rust validation/source.rs LiveSource and
// LiveBootstrapSource over the shared reader-table machinery): one
// registered read-only source against the committed generation of a
// live database, or the bootstrap registration over a live pair whose
// committed generation cannot be selected but whose raw meta pair
// still binds a database identity. The selected arm is the snapshot
// LiveSource (OpenLiveSourceCurrent); the bootstrap arm is a narrow
// entry sharing the sidecar and gate helpers. The validation sweep
// borrows the source mapping and folds the terminal through
// FinishCurrent.

import (
	"errors"

	"github.com/firehol/iprange/v4/go/internal/bootstrap"
	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/mapping"
)

// LiveValidationOpened is the union outcome of one validation live open
// (Rust validation/source.rs LiveOpened): exactly one arm is set.
type LiveValidationOpened struct {
	Selected  *LiveSource
	Bootstrap *LiveBootstrapValidationSource
}

// LiveValidationOpenFailure is the failing terminal of one validation
// live open (Rust LiveOpenFailure): the primary cause and whether the
// claimed-open unwind left coordination residue.
type LiveValidationOpenFailure struct {
	Cause   error
	Residue bool
}

func (e *LiveValidationOpenFailure) Error() string { return e.Cause.Error() }

func (e *LiveValidationOpenFailure) Unwrap() error { return e.Cause }

// OpenLiveValidationSource opens the validation live source (Rust
// validation/source.rs LiveSource::open): the committed-generation
// registration when the generation selects, or the bootstrap
// registration when the generation selection is a format problem and
// the raw meta pair binds the sidecar's database identity. Every other
// failure is the plain open failure.
func OpenLiveValidationSource(path string, check func() error) (*LiveValidationOpened, *LiveValidationOpenFailure) {
	opened, err := OpenLiveSourceCurrent(path, check)
	if err == nil {
		return &LiveValidationOpened{Selected: opened}, nil
	}
	// The Rust open splits its failure at the register stage: a format
	// problem from the committed-generation bind continues to the
	// sidecar registration when the raw pair still binds an identity.
	// Everything else is the plain open failure (the claimed-open
	// residue travels on the typed failure). The refused problem is
	// exactly the one the open reported (Rust live_opened carries it
	// from register_live to the bootstrap sweep).
	problem, ok := bootstrap.AsProblem(err)
	if !ok {
		var openFailure *OpenFailure
		if errors.As(err, &openFailure) {
			return nil, &LiveValidationOpenFailure{Cause: openFailure.Cause, Residue: openFailure.Residue}
		}
		return nil, &LiveValidationOpenFailure{Cause: err}
	}
	source, openFailure := openLiveBootstrapSource(path, check)
	if openFailure != nil {
		return nil, openFailure
	}
	source.problem = problem
	return &LiveValidationOpened{Bootstrap: source}, nil
}

// LiveBootstrapValidationSource is one gate-held registration over a
// live pair whose committed generation cannot be selected (Rust
// validation/source.rs LiveBootstrapSource): the borrowed main mapping
// under the shared lifetime lock and the sidecar registration, with
// the gate held for the terminal release. Problem carries the refused
// committed-generation selection for the bootstrap findings.
type LiveBootstrapValidationSource struct {
	mapping    *mapping.Mapping
	path       string
	identity   FileIdentity
	sidecar    *Sidecar
	gateLocked bool
	problem    *bootstrap.ProblemError
	ownerPID   int
}

// Problem returns the committed-generation selection problem reported
// by this bootstrap registration.
func (s *LiveBootstrapValidationSource) Problem() *bootstrap.ProblemError { return s.problem }

// FileIdentity returns the retained device and inode of the mapped
// main file (Rust live_namespace::identity over the opened file).
func (s *LiveBootstrapValidationSource) FileIdentity() (device uint64, inode uint64, err error) {
	return s.identity.device, s.identity.inode, nil
}

// Finish releases the bootstrap registration (Rust
// LiveBootstrapSource::finish): the operation result is folded with
// the re-proved bound identity and the gate and lifetime locks are
// released; a failed release keeps the residue state.
func (s *LiveBootstrapValidationSource) Finish(operation func() error) LiveSourceEnd {
	cause := combineErrors(operation(), s.verify())
	return terminalResult(cause, s.release())
}

// verify re-proves the registration before the release (Rust
// LiveBootstrapSource::verify: the main path, the sidecar path and
// header, and the bound identity against the sidecar database).
func (s *LiveBootstrapValidationSource) verify() error {
	if err := verifyPath(s.path, s.identity); err != nil {
		return err
	}
	if err := s.sidecar.verifyPath(); err != nil {
		return err
	}
	if err := s.sidecar.verifyHeader(); err != nil {
		return err
	}
	bound, err := s.boundDatabaseID()
	if err != nil {
		return err
	}
	if bound != s.sidecar.header.databaseID {
		return &format.Error{Code: format.CodeRecoveryCandidateChanged, Detail: "reader table belongs to a different database"}
	}
	return nil
}

// boundDatabaseID re-reads the raw meta pair (Rust
// selected_or_bound_database_id over the format arm of the live-mode
// bind).
func (s *LiveBootstrapValidationSource) boundDatabaseID() ([16]byte, error) {
	p0, err := s.mapping.Page(0)
	if err != nil {
		return [16]byte{}, err
	}
	p1, err := s.mapping.Page(1)
	if err != nil {
		return [16]byte{}, err
	}
	return bootstrap.DatabaseIDFromPages(p0, p1)
}

// release releases the gate and the lifetime lock (Rust
// LiveBootstrapSource::release; the mapping close unmaps, releases the
// shared lifetime lock, and closes the descriptor).
func (s *LiveBootstrapValidationSource) release() error {
	if err := s.requireOwner(); err != nil {
		return err
	}
	if s.gateLocked {
		if err := s.sidecar.unlockGate(); err != nil {
			return liveCoordination(err)
		}
		s.gateLocked = false
	}
	if err := s.mapping.Close(); err != nil {
		return err
	}
	s.sidecar.Close()
	return nil
}

func (s *LiveBootstrapValidationSource) requireOwner() error {
	if currentPID != s.ownerPID {
		return &format.Error{Code: format.CodeForkedHandle, Detail: "live source was opened by a different process"}
	}
	return nil
}

// openLiveBootstrapSource runs the Rust bootstrap registration
// (bind_live_main's bound identity + sidecar open + exclusive gate +
// register_bootstrap: the identity equality, the reader scan, and the
// pair proofs). The gate stays held for the terminal release.
func openLiveBootstrapSource(path string, check func() error) (*LiveBootstrapValidationSource, *LiveValidationOpenFailure) {
	fail := func(cause error) (*LiveBootstrapValidationSource, *LiveValidationOpenFailure) {
		return nil, &LiveValidationOpenFailure{Cause: cause}
	}
	if err := checkpoint(check); err != nil {
		return fail(err)
	}
	m, err := mapping.OpenLiveReader(path, nil)
	if err != nil {
		return fail(err)
	}
	bootstrapFail := func(cause error) (*LiveBootstrapValidationSource, *LiveValidationOpenFailure) {
		m.Close()
		return fail(cause)
	}
	device, inode, err := m.FileIdentity()
	if err != nil {
		return bootstrapFail(err)
	}
	identity := FileIdentity{device: device, inode: inode}
	if err := verifyPath(path, identity); err != nil {
		return bootstrapFail(liveCoordination(err))
	}
	p0, err := m.Page(0)
	if err != nil {
		return bootstrapFail(err)
	}
	p1, err := m.Page(1)
	if err != nil {
		return bootstrapFail(err)
	}
	boundID, err := bootstrap.DatabaseIDFromPages(p0, p1)
	if err != nil {
		return bootstrapFail(err)
	}
	sidecar, err := open(path, boundID)
	if err != nil {
		return bootstrapFail(liveCoordination(err))
	}
	fail = func(cause error) (*LiveBootstrapValidationSource, *LiveValidationOpenFailure) {
		sidecar.Close()
		m.Close()
		return nil, &LiveValidationOpenFailure{Cause: cause}
	}
	unlockGate := func(cause error) (*LiveBootstrapValidationSource, *LiveValidationOpenFailure) {
		return fail(combineErrors(cause, sidecar.unlockGate()))
	}
	if err := sidecar.lockGateCancellable(LockExclusive, check); err != nil {
		return fail(liveCoordination(err))
	}
	// register_bootstrap: the bound identity must match the reader
	// table, the readers are scanned, and the pair is re-proven.
	if boundID != sidecar.header.databaseID {
		return unlockGate(&format.Error{Code: format.CodeRecoveryCandidateChanged, Detail: "reader table belongs to a different database"})
	}
	if err := sidecar.scanReadersCancellable(check, func(uint64) error { return nil }); err != nil {
		return unlockGate(liveCoordination(err))
	}
	if err := verifyPath(path, identity); err != nil {
		return unlockGate(liveCoordination(err))
	}
	if err := sidecar.verifyPath(); err != nil {
		return unlockGate(liveCoordination(err))
	}
	if err := sidecar.verifyHeader(); err != nil {
		return unlockGate(liveCoordination(err))
	}
	return &LiveBootstrapValidationSource{
		mapping:    m,
		path:       path,
		identity:   identity,
		sidecar:    sidecar,
		gateLocked: true,
		ownerPID:   currentPID,
	}, nil
}

func terminalResult(cause, released error) LiveSourceEnd {
	if released == nil {
		return LiveSourceEnd{Cause: cause}
	}
	if cause == nil {
		cause = CleanupForCause(released)
	}
	return LiveSourceEnd{Cause: cause, Residue: true}
}
