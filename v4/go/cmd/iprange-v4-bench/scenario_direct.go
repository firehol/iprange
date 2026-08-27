// Scenario group "direct" (Rust
// v4/rust/iprange-livedb/benches/update_ipsets/scenarios/direct.rs):
// complete direct-map replacement with dispersed singleton and nested
// inputs, the commit-only measurement split, and the first-seen /
// last-seen refresh workflows over the shared address source.
package main

import (
	"fmt"

	iprangedb "github.com/firehol/iprange/v4/go"
)

func init() {
	registerScenario("direct-replace", directReplace)
	registerScenario("direct-replace-v6", directReplaceV6)
	registerScenario("direct-commit", directCommit)
	registerScenario("nested-overwrite", directNested)
	registerScenario("first-seen-refresh", directFirstSeen)
	registerScenario("last-seen-refresh", directLastSeen)
}

// directReplace mirrors Rust direct::replace: one complete direct
// replacement fed from the dispersed singleton source, source
// construction included inside the measured region like the Rust
// closure.
func directReplace(size int, _ int) (*scenarioResult, error) {
	tag, err := directTag([]byte("timestamp"))
	if err != nil {
		return nil, err
	}
	db, err := newTestDatabase("direct")
	if err != nil {
		return nil, err
	}
	defer db.cleanup()
	if _, err := iprangedb.CreateLive(db.main, iprangedb.AddressFamilyIPv4, iprangedb.ValueKindDirect, iprangedb.StructureKindNone, tag, 1, nil); err != nil {
		return nil, err
	}
	opErr, measured := operation(func() error {
		input, err := newDirectSource(size)
		if err != nil {
			return err
		}
		return applyDirect(db, input, size)
	})
	if opErr != nil {
		return nil, opErr
	}
	return result("direct-replace", size, 0, uint64(size), db, measured, db.main)
}

// directReplaceV6 mirrors Rust direct::replace_v6: the same complete
// replacement over dispersed low-2^32 IPv6 singleton ranges; the
// source records are converted to the public DirectRangeV6 shape per
// batch.
func directReplaceV6(size int, _ int) (*scenarioResult, error) {
	tag, err := directTag([]byte("timestamp"))
	if err != nil {
		return nil, err
	}
	db, err := newTestDatabase("direct-v6")
	if err != nil {
		return nil, err
	}
	defer db.cleanup()
	if _, err := iprangedb.CreateLive(db.main, iprangedb.AddressFamilyIPv6, iprangedb.ValueKindDirect, iprangedb.StructureKindNone, tag, 1, nil); err != nil {
		return nil, err
	}
	opErr, measured := operation(func() error {
		input, err := newDirectSourceV6(size)
		if err != nil {
			return err
		}
		return applyDirectV6(db, input, size)
	})
	if opErr != nil {
		return nil, opErr
	}
	return result("direct-replace-v6", size, 0, uint64(size), db, measured, db.main)
}

// directNested mirrors Rust direct::nested: one complete replacement
// fed from the shrinking nested pattern, all measured.
func directNested(size int, _ int) (*scenarioResult, error) {
	tag, err := directTag([]byte("timestamp"))
	if err != nil {
		return nil, err
	}
	db, err := newTestDatabase("nested")
	if err != nil {
		return nil, err
	}
	defer db.cleanup()
	if _, err := iprangedb.CreateLive(db.main, iprangedb.AddressFamilyIPv4, iprangedb.ValueKindDirect, iprangedb.StructureKindNone, tag, 1, nil); err != nil {
		return nil, err
	}
	opErr, measured := operation(func() error {
		input, err := newNestedSource(size)
		if err != nil {
			return err
		}
		return applyDirect(db, input, size)
	})
	if opErr != nil {
		return nil, opErr
	}
	return result("nested-overwrite", size, 0, uint64(size), db, measured, db.main)
}

// directCommit mirrors Rust direct::commit: prepare the replacement
// outside the measured region and measure only prepared.Commit()
// (Rust prepared.commit() is the whole measured closure), then close
// the writer and report.
func directCommit(size int, _ int) (*scenarioResult, error) {
	tag, err := directTag([]byte("timestamp"))
	if err != nil {
		return nil, err
	}
	db, err := newTestDatabase("direct-commit")
	if err != nil {
		return nil, err
	}
	defer db.cleanup()
	if _, err := iprangedb.CreateLive(db.main, iprangedb.AddressFamilyIPv4, iprangedb.ValueKindDirect, iprangedb.StructureKindNone, tag, 1, nil); err != nil {
		return nil, err
	}
	writer, err := iprangedb.OpenLiveWriter(db.main, toPageBudget(transactionBudget(size, 1)), nil)
	if err != nil {
		return nil, err
	}
	replacement, err := writer.BeginDirectReplacement(nil)
	if err != nil {
		return nil, err
	}
	input, err := newDirectSource(size)
	if err != nil {
		return nil, err
	}
	for {
		batch, more := input.nextBatch()
		if !more {
			break
		}
		if err := replacement.AddRangesV4(batch); err != nil {
			return nil, err
		}
	}
	finished, err := replacement.FinishInput()
	if err != nil {
		return nil, err
	}
	if !finished.IsChanged() {
		return nil, fmt.Errorf("prepared commit unexpectedly changed nothing")
	}
	var commitResult iprangedb.CommitResult
	opErr, measured := operation(func() error {
		var err error
		commitResult, err = finished.Commit()
		return err
	})
	if err := requireCommitted(commitResult, opErr); err != nil {
		return nil, err
	}
	if err := closeWriter(writer); err != nil {
		return nil, err
	}
	return result("direct-commit", size, 0, uint64(size), db, measured, db.main)
}

// directFirstSeen mirrors Rust direct::first_seen: seed the first_seen
// database with refresh 100, then measure a second refresh at 200 over
// the phase-shifted dispersed source (Rust shift = (size/10).max(1)).
func directFirstSeen(size int, _ int) (*scenarioResult, error) {
	db, err := newTestDatabase("first-seen")
	if err != nil {
		return nil, err
	}
	defer db.cleanup()
	if _, err := iprangedb.CreateLive(db.main, iprangedb.AddressFamilyIPv4, iprangedb.ValueKindDirect, iprangedb.StructureKindNone, iprangedb.ValueTagFirstSeen(), 1, nil); err != nil {
		return nil, err
	}
	seed, err := newAddressSource(size, 0)
	if err != nil {
		return nil, err
	}
	if err := applyFirstSeen(db, seed, size, 100); err != nil {
		return nil, err
	}
	shift := uint32(max(size/10, 1))
	opErr, measured := operation(func() error {
		input, err := newAddressSource(size, shift)
		if err != nil {
			return err
		}
		return applyFirstSeen(db, input, size, 200)
	})
	if opErr != nil {
		return nil, opErr
	}
	return result("first-seen-refresh", size, 0, uint64(size), db, measured, db.main)
}

// directLastSeen mirrors Rust direct::last_seen: seed the last_seen
// database at refresh 100 cutoff 0, then measure a second refresh at
// 200 with cutoff 100 over the phase-shifted source.
func directLastSeen(size int, _ int) (*scenarioResult, error) {
	db, err := newTestDatabase("last-seen")
	if err != nil {
		return nil, err
	}
	defer db.cleanup()
	if _, err := iprangedb.CreateLive(db.main, iprangedb.AddressFamilyIPv4, iprangedb.ValueKindDirect, iprangedb.StructureKindNone, iprangedb.ValueTagLastSeen(), 1, nil); err != nil {
		return nil, err
	}
	seed, err := newAddressSource(size, 0)
	if err != nil {
		return nil, err
	}
	if err := applyLastSeen(db, seed, size, 100, 0); err != nil {
		return nil, err
	}
	shift := uint32(max(size/10, 1))
	opErr, measured := operation(func() error {
		input, err := newAddressSource(size, shift)
		if err != nil {
			return err
		}
		return applyLastSeen(db, input, size, 200, 100)
	})
	if opErr != nil {
		return nil, opErr
	}
	return result("last-seen-refresh", size, 0, uint64(size), db, measured, db.main)
}

// applyDirect mirrors Rust direct::apply_direct: open the live writer
// under the transaction budget, stream one complete replacement, expect
// a logical change, require a durable commit, and close the writer
// through closeWriter (all inside the caller's measured region).
func applyDirect(db *testDatabase, input *directSource, size int) error {
	writer, err := iprangedb.OpenLiveWriter(db.main, toPageBudget(transactionBudget(size, 1)), nil)
	if err != nil {
		return err
	}
	replacement, err := writer.BeginDirectReplacement(nil)
	if err != nil {
		return err
	}
	for {
		batch, more := input.nextBatch()
		if !more {
			break
		}
		if err := replacement.AddRangesV4(batch); err != nil {
			return err
		}
	}
	finished, err := replacement.FinishInput()
	if err != nil {
		return err
	}
	if !finished.IsChanged() {
		return fmt.Errorf("replacement unexpectedly changed nothing: %+v", finished.Report())
	}
	commitResult, commitErr := finished.Commit()
	if err := requireCommitted(commitResult, commitErr); err != nil {
		return err
	}
	return closeWriter(writer)
}

// applyDirectV6 mirrors Rust direct::apply_direct_v6: the same
// replacement over the IPv6 source, with each source record converted
// to the public DirectRangeV6 shape (low-2^32 ranges keep Hi parts
// zero).
func applyDirectV6(db *testDatabase, input *directSourceV6, size int) error {
	writer, err := iprangedb.OpenLiveWriter(db.main, toPageBudget(transactionBudget(size, 1)), nil)
	if err != nil {
		return err
	}
	replacement, err := writer.BeginDirectReplacement(nil)
	if err != nil {
		return err
	}
	var convertedPool [batchCapacity]iprangedb.DirectRangeV6
	for {
		batch, more := input.nextBatch()
		if !more {
			break
		}
		converted := convertedPool[:len(batch)]
		for index, source := range batch {
			converted[index] = iprangedb.DirectRangeV6{
				FromHi: 0, FromLo: source.fromLo,
				ToHi: 0, ToLo: source.toLo,
				Value: source.value,
			}
		}
		if err := replacement.AddRangesV6(converted); err != nil {
			return err
		}
	}
	finished, err := replacement.FinishInput()
	if err != nil {
		return err
	}
	if !finished.IsChanged() {
		return fmt.Errorf("replacement unexpectedly changed nothing: %+v", finished.Report())
	}
	commitResult, commitErr := finished.Commit()
	if err := requireCommitted(commitResult, commitErr); err != nil {
		return err
	}
	return closeWriter(writer)
}

// applyFirstSeen mirrors Rust direct::apply_first_seen: one complete
// first-seen refresh workflow (begin, stream, finish, commit, close).
func applyFirstSeen(db *testDatabase, input *addressSource, size int, refresh uint32) error {
	writer, err := iprangedb.OpenLiveWriter(db.main, toPageBudget(transactionBudget(size, 1)), nil)
	if err != nil {
		return err
	}
	workflow, err := writer.BeginFirstSeenRefresh(refresh, nil)
	if err != nil {
		return err
	}
	for {
		batch, more := input.nextBatch()
		if !more {
			break
		}
		if err := workflow.AddRangesV4(batch); err != nil {
			return err
		}
	}
	finished, err := workflow.FinishInput()
	if err != nil {
		return err
	}
	if !finished.IsChanged() {
		return fmt.Errorf("refresh unexpectedly changed nothing: %+v", finished.Report())
	}
	commitResult, commitErr := finished.Commit()
	if err := requireCommitted(commitResult, commitErr); err != nil {
		return err
	}
	return closeWriter(writer)
}

// applyLastSeen mirrors Rust direct::apply_last_seen: one complete
// last-seen refresh workflow with the refresh value and cutoff.
func applyLastSeen(db *testDatabase, input *addressSource, size int, refresh, cutoff uint32) error {
	writer, err := iprangedb.OpenLiveWriter(db.main, toPageBudget(transactionBudget(size, 1)), nil)
	if err != nil {
		return err
	}
	workflow, err := writer.BeginLastSeenRefresh(refresh, cutoff, nil)
	if err != nil {
		return err
	}
	for {
		batch, more := input.nextBatch()
		if !more {
			break
		}
		if err := workflow.AddRangesV4(batch); err != nil {
			return err
		}
	}
	finished, err := workflow.FinishInput()
	if err != nil {
		return err
	}
	if !finished.IsChanged() {
		return fmt.Errorf("refresh unexpectedly changed nothing: %+v", finished.Report())
	}
	commitResult, commitErr := finished.Commit()
	if err := requireCommitted(commitResult, commitErr); err != nil {
		return err
	}
	return closeWriter(writer)
}

// directTag mirrors Rust direct::direct_tag for the benchmark's
// "timestamp" value tag.
func directTag(value []byte) (iprangedb.ValueTag, error) {
	tag, err := iprangedb.NewValueTag(value)
	if err != nil {
		return iprangedb.ValueTag{}, fmt.Errorf("invalid benchmark value tag")
	}
	return tag, nil
}
