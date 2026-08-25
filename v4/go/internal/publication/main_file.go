// Atomic main-name publication and exact reservation retirement (Rust
// publication/main_file.rs). The main file appears under its final
// name with one atomic namespace operation per policy, the file and
// directory synced and re-proved; retirement then retires the replaced
// previous and the coordination reservation and proves the namespace
// empty of them before the result facts are built. The posix arms
// unlink exactly; the windows arms run the authenticated GC
// transition (main_file_windows.go).

package publication

import (
	"github.com/firehol/iprange/v4/go/internal/fault"
	"github.com/firehol/iprange/v4/go/internal/live"
)

// mainPoint is one main-file machine checkpoint (Rust main_file::Point).
type mainPoint uint8

const (
	mainPointMainRenamed mainPoint = iota
	mainPointMainSynced
	mainPointDirectorySynced
	mainPointDesiredProven
	mainPointPreviousUnlinked
	mainPointReservationUnlinked
	mainPointRetirementSynced
)

// mainFailure is one main-file failure carrying the still-owned
// attempt (Rust Failure<MainAttempt>). The owner rides by value so
// the success path of the machine stays on the stack (Rust moves the
// owner; Go copies it into the failure only on the failure path).
type mainFailure struct {
	owner mainAttempt
	cause error
}

// mainAttempt is the publish owner: the output and armed reservation
// plus the exact physical-state flags the recovery decision needs
// (Rust MainAttempt).
type mainAttempt struct {
	output          *preparedOutput
	reservation     armedReservation
	mainCallStarted bool
	renameSucceeded bool
	desiredProven   bool
}

// publishedMain is the proven published output and its reservation
// (Rust PublishedMain).
type publishedMain struct {
	output      *preparedOutput
	reservation armedReservation
}

// retiringMain is the retirement owner: the published main plus the
// exact retirement-progress flags, housekeeping evidence, and the
// optional visible housekeeping artifacts (Rust RetiringMain).
type retiringMain struct {
	published                publishedMain
	previousUnlinked         bool
	previousRetiredProven    bool
	reservationUnlinked      bool
	directorySynced          bool
	reservationRetiredProven bool
	housekeeping             Housekeeping
	visibleHousekeeping      []HousekeepingArtifact
}

// retiringMainFailure is one retirement failure carrying the still-
// owned retiring main (Rust Failure<RetiringMain>).
type retiringMainFailure struct {
	owner retiringMain
	cause error
}

// publishedOutput is the retired publication output (Rust
// PublishedOutput): the main inode and its lifetime lock stay owned
// through result construction, with the exact housekeeping evidence.
type publishedOutput struct {
	outputGuard         *preparedOutput
	reservationIdentity live.FileIdentity
	housekeeping        Housekeeping
	visibleHousekeeping []HousekeepingArtifact
}

// publishProved runs the main-name publication without checkpoints
// (Rust main_file::publish).
func publishProved(output *preparedOutput, reservation armedReservation) (publishedMain, *mainFailure) {
	return publishWith(output, reservation, func(mainPoint) error { return nil })
}

// publishObserved runs the main-name publication with one exact
// checkpoint per physical step (Rust main_file::publish_observed; the
// attempt machine wires the DesiredProven observation here).
func publishObserved(output *preparedOutput, reservation armedReservation, checkpoint func(mainPoint) error) (publishedMain, *mainFailure) {
	return publishWith(output, reservation, checkpoint)
}

func publishWith(output *preparedOutput, reservation armedReservation, checkpoint func(mainPoint) error) (publishedMain, *mainFailure) {
	owner := mainAttempt{
		output:      output,
		reservation: reservation,
	}
	if err := publishSteps(&owner, checkpoint); err != nil {
		return publishedMain{}, &mainFailure{owner: owner, cause: err}
	}
	return publishedMain{output: owner.output, reservation: owner.reservation}, nil
}

func publishSteps(owner *mainAttempt, checkpoint func(mainPoint) error) error {
	if err := verifyBeforeMain(owner); err != nil {
		return err
	}
	if err := renameMain(owner); err != nil {
		return err
	}
	if err := checkpoint(mainPointMainRenamed); err != nil {
		return checkpointFailure(err)
	}
	if err := synchronizeMain(owner, checkpoint); err != nil {
		return err
	}
	return proveMain(owner, checkpoint)
}

// verifyBeforeMain proves the reservation is armed for this output
// and the destination main slot is either the verified previous or
// absent (Rust verify_before_main).
func verifyBeforeMain(owner *mainAttempt) error {
	if err := owner.reservation.verifyBeforeMain(owner.output); err != nil {
		return err
	}
	if owner.output.previous != nil {
		return owner.output.verifyDestinationBeforeMain()
	}
	destination := owner.output.attempt.destinationOf()
	return destination.directory().RequireAbsent(destination.mainName())
}

// renameMain places the private output under the final main name with
// the policy's atomic operation (Rust rename_main).
func renameMain(owner *mainAttempt) error {
	destination := owner.output.attempt.destinationOf()
	owner.mainCallStarted = true
	switch owner.output.policy {
	case reservationPolicyFailIfExists:
		if err := destination.directory().RenameNoReplace(owner.output.attempt.nameOf(), owner.output.file, destination.mainName()); err != nil {
			return err
		}
	case reservationPolicyReplaceExisting:
		if err := destination.directory().RenameExchange(owner.output.attempt.nameOf(), destination.mainName()); err != nil {
			return err
		}
	default: // reservationPolicyReplaceExistingNoRollback
		if err := destination.directory().RenamePlain(owner.output.attempt.nameOf(), destination.mainName()); err != nil {
			return err
		}
	}
	owner.renameSucceeded = true
	fault.Crash("publication.after_main_rename")
	return nil
}

// synchronizeMain durably flushes the main file and the directory at
// the exact Rust steps (Rust synchronize_main).
func synchronizeMain(owner *mainAttempt, checkpoint func(mainPoint) error) error {
	if err := live.SyncFile(owner.output.file); err != nil {
		return err
	}
	fault.Crash("publication.after_main_sync")
	if err := checkpoint(mainPointMainSynced); err != nil {
		return checkpointFailure(err)
	}
	if err := owner.output.attempt.destinationOf().directory().Sync(); err != nil {
		return err
	}
	fault.Crash("publication.after_main_directory_sync")
	return checkpoint(mainPointDirectorySynced)
}

// proveMain re-proves the directory and the main output, remembers the
// desired proof, and re-proves the reservation (Rust prove_main).
func proveMain(owner *mainAttempt, checkpoint func(mainPoint) error) error {
	if err := owner.output.attempt.destinationOf().directory().Verify(); err != nil {
		return err
	}
	if err := owner.output.verifyMain(); err != nil {
		return err
	}
	owner.desiredProven = true
	fault.Crash("publication.after_main_proof")
	if err := checkpoint(mainPointDesiredProven); err != nil {
		return checkpointFailure(err)
	}
	return owner.reservation.verifyAfterMain(owner.output)
}

// retire retires one published main without checkpoints (Rust
// PublishedMain::retire). retire_observed shares the same arms; the
// windows gc wiring observes housekeeping through the GC transition.
func (p *publishedMain) retire() (publishedOutput, *retiringMainFailure) {
	return retireMain(*p, func(mainPoint) error { return nil })
}

func retireMain(p publishedMain, checkpoint func(mainPoint) error) (publishedOutput, *retiringMainFailure) {
	owner := retiringMain{published: p}
	if err := retireSteps(&owner, checkpoint); err != nil {
		return publishedOutput{}, &retiringMainFailure{owner: owner, cause: err}
	}
	return publishedOutput{
		outputGuard:         owner.published.output,
		reservationIdentity: owner.published.reservation.identity,
		housekeeping:        owner.housekeeping,
		visibleHousekeeping: owner.visibleHousekeeping,
	}, nil
}

func retireSteps(owner *retiringMain, checkpoint func(mainPoint) error) error {
	if err := verifyPublished(owner.published); err != nil {
		return err
	}
	unlinked, err := unlinkPrevious(owner)
	if err != nil {
		return err
	}
	if unlinked {
		if err := checkpoint(mainPointPreviousUnlinked); err != nil {
			return checkpointFailure(err)
		}
	}
	if err := unlinkReservation(owner); err != nil {
		return err
	}
	if err := checkpoint(mainPointReservationUnlinked); err != nil {
		return checkpointFailure(err)
	}
	if err := syncRetirement(owner); err != nil {
		return err
	}
	if err := checkpoint(mainPointRetirementSynced); err != nil {
		return checkpointFailure(err)
	}
	return verifyRetired(owner)
}

// verifyPublished re-proves the published main and its reservation
// before any retirement syscall (Rust verify_published).
func verifyPublished(published publishedMain) error {
	if err := published.output.verifyMain(); err != nil {
		return err
	}
	return published.reservation.verifyAfterMain(published.output)
}

// syncRetirement synchronizes the directory after the unlinks (Rust
// sync_retirement).
func syncRetirement(owner *retiringMain) error {
	if err := owner.published.output.attempt.destinationOf().directory().Sync(); err != nil {
		return err
	}
	owner.directorySynced = true
	fault.Crash("publication.after_retirement_sync")
	return nil
}

// verifyRetired proves the directory, the absent coordination name,
// the retired previous, and the still-intact main (Rust
// verify_retired).
func verifyRetired(owner *retiringMain) error {
	output := owner.published.output
	destination := output.attempt.destinationOf()
	if err := destination.directory().Verify(); err != nil {
		return err
	}
	if err := destination.directory().RequireAbsent(destination.coordinationName()); err != nil {
		return err
	}
	if !owner.previousRetiredProven {
		if previous := output.previous; previous != nil {
			if err := previous.verifyRetired(destination, output.attempt.nameOf()); err != nil {
				return err
			}
		}
	}
	owner.previousRetiredProven = true
	owner.reservationRetiredProven = true
	return output.verifyMain()
}
