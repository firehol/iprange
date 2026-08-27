// Read-group benchmark scenarios (Rust
// v4/rust/iprange-livedb/benches/update_ipsets/scenarios/read.rs):
// live and immutable direct lookups (ordered and random), live and
// immutable direct scans, live reader open/close, snapshot publication,
// and live/immutable/membership validation. Every scenario mirrors the
// Rust instruction-for-instruction: the same names, the same
// reader_work repetitions and work units, the same require_count
// assertions, the same measured closure boundaries, and the same
// result()/immutable_result() output handling.
package main

import (
	"fmt"
	"math"
	"runtime"

	"github.com/firehol/iprange/v4/go"
)

func init() {
	registerScenario("live-direct-lookup", scenarioLiveDirectLookup)
	registerScenario("immutable-direct-lookup", scenarioImmutableDirectLookup)
	registerScenario("live-direct-random-lookup", scenarioLiveDirectRandomLookup)
	registerScenario("immutable-direct-random-lookup", scenarioImmutableDirectRandomLookup)
	registerScenario("live-direct-scan", scenarioLiveDirectScan)
	registerScenario("immutable-direct-scan", scenarioImmutableDirectScan)
	registerScenario("live-open", scenarioLiveOpen)
	registerScenario("snapshot", scenarioSnapshot)
	registerScenario("live-validation", scenarioLiveValidation)
	registerScenario("live-membership-validation", scenarioLiveMembershipValidation)
	registerScenario("immutable-validation", scenarioImmutableValidation)
}

// readRequireCount mirrors Rust scenarios::require_count: the observed
// count must exactly equal the expected work units.
func readRequireCount(label string, observed, expected uint64, noun string) error {
	if observed != expected {
		return fmt.Errorf("%s returned %d of %d %s", label, observed, expected, noun)
	}
	return nil
}

// readSeededDirect builds a direct-value live database with `size`
// dispersed singleton ranges (Rust direct::seeded_direct + apply_direct,
// scenarios/direct.rs:129 and scenarios/direct.rs:157).
func readSeededDirect(label string, size int, readerCapacity uint32) (*testDatabase, error) {
	tag, err := iprangedb.NewValueTag([]byte("timestamp"))
	if err != nil {
		return nil, err
	}
	database, err := newTestDatabase(label)
	if err != nil {
		return nil, err
	}
	if _, err := iprangedb.CreateLive(database.main, iprangedb.AddressFamilyIPv4, iprangedb.ValueKindDirect, iprangedb.StructureKindNone, tag, readerCapacity, iprangedb.NewCancellationToken()); err != nil {
		return nil, err
	}
	cancellation := iprangedb.NewCancellationToken()
	writer, err := iprangedb.OpenLiveWriter(database.main, toPageBudget(transactionBudget(size, 1)), cancellation)
	if err != nil {
		return nil, err
	}
	workflow, err := writer.BeginDirectReplacement(cancellation)
	if err != nil {
		return nil, err
	}
	source, err := newDirectSource(size)
	if err != nil {
		return nil, err
	}
	for {
		batch, ok := source.nextBatch()
		if !ok {
			break
		}
		if err := workflow.AddRangesV4(batch); err != nil {
			return nil, err
		}
	}
	finished, err := workflow.FinishInput()
	if err != nil {
		return nil, err
	}
	if !finished.IsChanged() {
		return nil, fmt.Errorf("replacement unexpectedly changed nothing: %+v", finished.Report())
	}
	if err := requireCommitted(finished.Commit()); err != nil {
		return nil, err
	}
	if err := closeWriter(writer); err != nil {
		return nil, err
	}
	return database, nil
}

// scenarioLiveDirectLookup mirrors Rust read::live_direct_lookup
// (scenarios/read.rs:14): ordered direct lookups through a live reader.
func scenarioLiveDirectLookup(size, _ int) (*scenarioResult, error) {
	database, err := readSeededDirect("live-direct-lookup", size, 1)
	if err != nil {
		return nil, err
	}
	defer database.cleanup()
	reader, err := iprangedb.OpenLiveReader(database.main, nil)
	if err != nil {
		return nil, err
	}
	repetitions, workUnits, err := readerWork(size)
	if err != nil {
		return nil, err
	}
	var hits uint64
	opErr, measured := operation(func() error {
		var err error
		hits, err = countPoints(size, repetitions, func(address iprangedb.IPv4) (bool, error) {
			_, found, err := reader.LookupDirectV4(address)
			if err != nil {
				return false, err
			}
			return found, nil
		})
		return err
	})
	if opErr != nil {
		return nil, opErr
	}
	if err := readRequireCount("live direct lookup", hits, workUnits, "addresses"); err != nil {
		return nil, err
	}
	if err := closeLiveReader(reader); err != nil {
		return nil, err
	}
	return result("live-direct-lookup", size, 0, workUnits, database, measured, database.main)
}

// scenarioImmutableDirectLookup mirrors Rust read::immutable_direct_lookup
// (scenarios/read.rs:32): ordered direct lookups through the snapshot.
func scenarioImmutableDirectLookup(size, _ int) (*scenarioResult, error) {
	database, err := readSeededDirect("immutable-direct-lookup", size, 1)
	if err != nil {
		return nil, err
	}
	defer database.cleanup()
	snapshot, err := immutableSnapshot(database, size)
	if err != nil {
		return nil, err
	}
	reader, err := iprangedb.OpenImmutable(snapshot)
	if err != nil {
		return nil, err
	}
	repetitions, workUnits, err := readerWork(size)
	if err != nil {
		return nil, err
	}
	var hits uint64
	opErr, measured := operation(func() error {
		var err error
		hits, err = countPoints(size, repetitions, func(address iprangedb.IPv4) (bool, error) {
			_, found, err := reader.LookupDirectV4(address)
			if err != nil {
				return false, err
			}
			return found, nil
		})
		return err
	})
	if opErr != nil {
		return nil, opErr
	}
	if err := readRequireCount("immutable direct lookup", hits, workUnits, "addresses"); err != nil {
		return nil, err
	}
	_ = reader.Close()
	return result("immutable-direct-lookup", size, 0, workUnits, database, measured, snapshot)
}

// scenarioLiveDirectRandomLookup mirrors Rust read::live_direct_random_lookup
// (scenarios/read.rs:50): shuffled direct lookups through a live reader.
func scenarioLiveDirectRandomLookup(size, _ int) (*scenarioResult, error) {
	database, err := readSeededDirect("live-direct-random-lookup", size, 1)
	if err != nil {
		return nil, err
	}
	defer database.cleanup()
	points, err := randomPoints(size)
	if err != nil {
		return nil, err
	}
	reader, err := iprangedb.OpenLiveReader(database.main, nil)
	if err != nil {
		return nil, err
	}
	repetitions, workUnits, err := readerWork(size)
	if err != nil {
		return nil, err
	}
	var hits uint64
	opErr, measured := operation(func() error {
		var err error
		hits, err = countRandomPoints(points, repetitions, func(address iprangedb.IPv4) (bool, error) {
			_, found, err := reader.LookupDirectV4(address)
			if err != nil {
				return false, err
			}
			return found, nil
		})
		return err
	})
	if opErr != nil {
		return nil, opErr
	}
	if err := readRequireCount("live random direct lookup", hits, workUnits, "addresses"); err != nil {
		return nil, err
	}
	if err := closeLiveReader(reader); err != nil {
		return nil, err
	}
	return result("live-direct-random-lookup", size, 0, workUnits, database, measured, database.main)
}

// scenarioImmutableDirectRandomLookup mirrors Rust
// read::immutable_direct_random_lookup (scenarios/read.rs:68): shuffled
// direct lookups through the snapshot.
func scenarioImmutableDirectRandomLookup(size, _ int) (*scenarioResult, error) {
	database, err := readSeededDirect("immutable-direct-random-lookup", size, 1)
	if err != nil {
		return nil, err
	}
	defer database.cleanup()
	snapshot, err := immutableSnapshot(database, size)
	if err != nil {
		return nil, err
	}
	points, err := randomPoints(size)
	if err != nil {
		return nil, err
	}
	reader, err := iprangedb.OpenImmutable(snapshot)
	if err != nil {
		return nil, err
	}
	repetitions, workUnits, err := readerWork(size)
	if err != nil {
		return nil, err
	}
	var hits uint64
	opErr, measured := operation(func() error {
		var err error
		hits, err = countRandomPoints(points, repetitions, func(address iprangedb.IPv4) (bool, error) {
			_, found, err := reader.LookupDirectV4(address)
			if err != nil {
				return false, err
			}
			return found, nil
		})
		return err
	})
	if opErr != nil {
		return nil, opErr
	}
	if err := readRequireCount("immutable random direct lookup", hits, workUnits, "addresses"); err != nil {
		return nil, err
	}
	_ = reader.Close()
	return result("immutable-direct-random-lookup", size, 0, workUnits, database, measured, snapshot)
}

// scenarioLiveDirectScan mirrors Rust read::live_direct_scan
// (scenarios/read.rs:86): forward direct-range cursor sweeps through a
// live reader.
func scenarioLiveDirectScan(size, _ int) (*scenarioResult, error) {
	database, err := readSeededDirect("live-direct-scan", size, 1)
	if err != nil {
		return nil, err
	}
	defer database.cleanup()
	reader, err := iprangedb.OpenLiveReader(database.main, nil)
	if err != nil {
		return nil, err
	}
	repetitions, workUnits, err := readerWork(size)
	if err != nil {
		return nil, err
	}
	var records uint64
	opErr, measured := operation(func() error {
		var err error
		records, err = countCursor(repetitions,
			func() (*iprangedb.DirectCursorV4, error) {
				return reader.DirectCursorV4(iprangedb.RangeDirectionForward)
			},
			func(cursor *iprangedb.DirectCursorV4) (bool, error) {
				_, more, err := cursor.NextRange()
				if err != nil {
					return false, err
				}
				return more, nil
			},
		)
		return err
	})
	if opErr != nil {
		return nil, opErr
	}
	if err := readRequireCount("live direct scan", records, workUnits, "ranges"); err != nil {
		return nil, err
	}
	if err := closeLiveReader(reader); err != nil {
		return nil, err
	}
	return result("live-direct-scan", size, 0, workUnits, database, measured, database.main)
}

// scenarioImmutableDirectScan mirrors Rust read::immutable_direct_scan
// (scenarios/read.rs:105): forward direct-range cursor sweeps through
// the snapshot.
func scenarioImmutableDirectScan(size, _ int) (*scenarioResult, error) {
	database, err := readSeededDirect("immutable-direct-scan", size, 1)
	if err != nil {
		return nil, err
	}
	defer database.cleanup()
	snapshot, err := immutableSnapshot(database, size)
	if err != nil {
		return nil, err
	}
	reader, err := iprangedb.OpenImmutable(snapshot)
	if err != nil {
		return nil, err
	}
	repetitions, workUnits, err := readerWork(size)
	if err != nil {
		return nil, err
	}
	var records uint64
	opErr, measured := operation(func() error {
		var err error
		records, err = countCursor(repetitions,
			func() (*iprangedb.DirectCursorV4, error) {
				return reader.DirectCursorV4(iprangedb.RangeDirectionForward)
			},
			func(cursor *iprangedb.DirectCursorV4) (bool, error) {
				_, more, err := cursor.NextRange()
				if err != nil {
					return false, err
				}
				return more, nil
			},
		)
		return err
	})
	if opErr != nil {
		return nil, opErr
	}
	if err := readRequireCount("immutable direct scan", records, workUnits, "ranges"); err != nil {
		return nil, err
	}
	_ = reader.Close()
	return result("immutable-direct-scan", size, 0, workUnits, database, measured, snapshot)
}

// scenarioLiveOpen mirrors Rust read::live_open (scenarios/read.rs:124):
// 100 live-reader open/info/close cycles, measured.
func scenarioLiveOpen(size, auxiliary int) (*scenarioResult, error) {
	capacity := auxiliary
	if capacity < 1 {
		capacity = 1
	}
	if uint64(capacity) > math.MaxUint32 {
		return nil, fmt.Errorf("reader capacity exceeds u32")
	}
	database, err := readSeededDirect("live-open", size, uint32(capacity))
	if err != nil {
		return nil, err
	}
	defer database.cleanup()
	const iterations uint64 = 100
	selected := uint64(0)
	opErr, measured := operation(func() error {
		for range iterations {
			reader, err := iprangedb.OpenLiveReader(database.main, nil)
			if err != nil {
				return err
			}
			info, err := reader.Info()
			if err != nil {
				return err
			}
			selected ^= info.TransactionID
			if err := closeLiveReader(reader); err != nil {
				return err
			}
		}
		runtime.KeepAlive(selected)
		return nil
	})
	if opErr != nil {
		return nil, opErr
	}
	return result("live-open", size, capacity, iterations, database, measured, database.main)
}

// scenarioSnapshot mirrors Rust read::snapshot (scenarios/read.rs:139):
// one live-source snapshot to the canonical snapshot path inside the
// measured region.
func scenarioSnapshot(size, _ int) (*scenarioResult, error) {
	database, err := readSeededDirect("snapshot", size, 2)
	if err != nil {
		return nil, err
	}
	defer database.cleanup()
	output := database.snapshot()
	var snap iprangedb.SnapshotResult
	opErr, measured := operation(func() error {
		var err error
		snap, err = iprangedb.SnapshotTo(database.main, iprangedb.SnapshotSourceLive, output, iprangedb.PolicyFailIfExists, &iprangedb.SnapshotBudget{
			MaxHeapBytes:   64 * 1024 * 1024,
			MaxOutputPages: snapshotBudget(size).MaxOutputPages,
			MaxOpenFiles:   3,
		}, iprangedb.NewCancellationToken())
		return err
	})
	if opErr != nil {
		return nil, opErr
	}
	if snap.CleanupState() != iprangedb.CleanupStateClean {
		return nil, fmt.Errorf("snapshot cleanup is incomplete: %+v", snap)
	}
	return result("snapshot", size, 0, uint64(size), database, measured, output)
}

// scenarioLiveValidation mirrors Rust read::live_validation
// (scenarios/read.rs:158): full live-current validation of the main file.
func scenarioLiveValidation(size, _ int) (*scenarioResult, error) {
	database, err := readSeededDirect("live-validation", size, 1)
	if err != nil {
		return nil, err
	}
	defer database.cleanup()
	return readValidateCase("live-validation", size, 0, database, database.main, iprangedb.ValidationModeLiveCurrent)
}

// scenarioImmutableValidation mirrors Rust read::immutable_validation
// (scenarios/read.rs:165): full immutable-current validation of the
// snapshot.
func scenarioImmutableValidation(size, _ int) (*scenarioResult, error) {
	database, err := readSeededDirect("immutable-validation", size, 1)
	if err != nil {
		return nil, err
	}
	defer database.cleanup()
	snapshot, err := immutableSnapshot(database, size)
	if err != nil {
		return nil, err
	}
	return readValidateCase("immutable-validation", size, 0, database, snapshot, iprangedb.ValidationModeImmutableCurrent)
}

// scenarioLiveMembershipValidation mirrors Rust read::live_membership_validation
// (scenarios/read.rs:172): full live-current validation of a membership
// database with `feeds` feeds.
func scenarioLiveMembershipValidation(size, auxiliary int) (*scenarioResult, error) {
	feeds := auxiliary
	if feeds < 1 {
		feeds = 1
	}
	width := feeds
	if width > 8 {
		width = 8
	}
	database, err := readMembershipValidationDatabase("live-membership-validation", size, feeds, width)
	if err != nil {
		return nil, err
	}
	defer database.cleanup()
	return readValidateCase("live-membership-validation", size, feeds, database, database.main, iprangedb.ValidationModeLiveCurrent)
}

// readMembershipValidationDatabase mirrors Rust membership::populated_rotating
// (scenarios/membership.rs:344): one membership transaction creates
// every feed, builds one rotating membership per feed (each covering a
// window of `width` feeds), and applies all ranges to those rotating
// memberships with the Replace operation.
func readMembershipValidationDatabase(label string, ranges, feeds, width int) (*testDatabase, error) {
	if feeds < 1 {
		feeds = 1
	}
	if width < 1 {
		width = 1
	}
	if width > feeds {
		width = feeds
	}
	database, err := newTestDatabase(label)
	if err != nil {
		return nil, err
	}
	tag, err := iprangedb.NewValueTag([]byte("membership"))
	if err != nil {
		return nil, err
	}
	if _, err := iprangedb.CreateLive(database.main, iprangedb.AddressFamilyIPv4, iprangedb.ValueKindMembership, iprangedb.StructureKindNone, tag, 1, iprangedb.NewCancellationToken()); err != nil {
		return nil, err
	}
	cancellation := iprangedb.NewCancellationToken()
	writer, err := iprangedb.OpenLiveWriter(database.main, toPageBudget(transactionBudget(ranges, feeds)), cancellation)
	if err != nil {
		return nil, err
	}
	transaction, err := writer.BeginMembershipTransaction(cancellation)
	if err != nil {
		return nil, err
	}
	feedRefs := make([]iprangedb.FeedRef, feeds)
	for index := range feedRefs {
		name, err := iprangedb.NewFeedName(fmt.Sprintf("feed-%06d", index))
		if err != nil {
			return nil, err
		}
		feedRefs[index], err = transaction.EnsureFeed(name)
		if err != nil {
			return nil, err
		}
	}
	memberships := make([]iprangedb.MembershipRef, feeds)
	for start := range memberships {
		membership, err := transaction.EmptyMembership()
		if err != nil {
			return nil, err
		}
		for offset := 0; offset < width; offset++ {
			membership, err = transaction.AddFeed(membership, feedRefs[(start+offset)%feeds])
			if err != nil {
				return nil, err
			}
		}
		memberships[start] = membership
	}
	for index := 0; index < ranges; index++ {
		start := uint32(index) * 4
		if _, err := transaction.ApplyV4(iprangedb.IPv4(start), iprangedb.IPv4(start+1), memberships[index%feeds], iprangedb.MembershipReplace); err != nil {
			return nil, err
		}
	}
	if err := requireCommitted(transaction.Commit()); err != nil {
		return nil, err
	}
	if err := closeWriter(writer); err != nil {
		return nil, err
	}
	return database, nil
}

// readValidateCase mirrors Rust read::validate_case (scenarios/read.rs:181):
// one full validation inside the measured region, requiring a clean
// result and reporting checked unique pages as the work units.
func readValidateCase(name string, size, auxiliary int, database *testDatabase, file string, mode iprangedb.ValidationMode) (*scenarioResult, error) {
	validated := &iprangedb.ValidationResult{}
	opErr, measured := operation(func() error {
		var failure *iprangedb.ValidationFailure
		validated, failure = iprangedb.Validate(file, mode, iprangedb.HeapOnly(64*1024*1024, 2), nil, nil)
		if failure != nil {
			if failure.Cause != nil {
				return failure.Cause
			}
			return fmt.Errorf("validation failed: %+v", failure)
		}
		return nil
	})
	if opErr != nil {
		return nil, opErr
	}
	if !validated.Valid {
		return nil, fmt.Errorf("%s found %d validation failures", name, validated.Progress.FindingCount)
	}
	return result(name, size, auxiliary, validated.Progress.CheckedUniquePages, database, measured, file)
}
