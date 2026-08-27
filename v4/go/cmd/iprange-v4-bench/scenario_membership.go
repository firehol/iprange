// Membership scenarios (Rust
// benches/update_ipsets/scenarios/membership.rs): shaped feed creation,
// feed replacement, membership import, live/immutable membership
// lookups, and feed scans, ported instruction-for-instruction. The Go
// SDK exposes membership lookups only through pins
// (Pin.LookupMembershipV4, reader_public.go), so each lookup scenario
// pins the reader where the Rust harness opens the reader and keeps the
// pin open across the measured workload.
package main

import (
	"fmt"

	iprangedb "github.com/firehol/iprange/v4/go"
)

func init() {
	registerScenario("feed-replace", func(size, auxiliary int) (*scenarioResult, error) {
		return replaceFeed(size, auxiliary)
	})
	registerScenario("feed-first-ascending", func(size, _ int) (*scenarioResult, error) {
		return shapedFeed("feed-first-ascending", size, feedAscendingDisjoint, false)
	})
	registerScenario("feed-second-ascending", func(size, _ int) (*scenarioResult, error) {
		return shapedFeed("feed-second-ascending", size, feedAscendingDisjoint, true)
	})
	registerScenario("feed-first-descending", func(size, _ int) (*scenarioResult, error) {
		return shapedFeed("feed-first-descending", size, feedDescendingDisjoint, false)
	})
	registerScenario("feed-second-descending", func(size, _ int) (*scenarioResult, error) {
		return shapedFeed("feed-second-descending", size, feedDescendingDisjoint, true)
	})
	registerScenario("feed-first-random", func(size, _ int) (*scenarioResult, error) {
		return shapedFeed("feed-first-random", size, feedRandomDisjoint, false)
	})
	registerScenario("feed-second-random", func(size, _ int) (*scenarioResult, error) {
		return shapedFeed("feed-second-random", size, feedRandomDisjoint, true)
	})
	registerScenario("feed-first-overlap", func(size, _ int) (*scenarioResult, error) {
		return shapedFeed("feed-first-overlap", size, feedRandomOverlapChain, false)
	})
	registerScenario("feed-second-overlap", func(size, _ int) (*scenarioResult, error) {
		return shapedFeed("feed-second-overlap", size, feedRandomOverlapChain, true)
	})
	registerScenario("membership-import", func(size, auxiliary int) (*scenarioResult, error) {
		return membershipImport(size, auxiliary)
	})
	registerScenario("live-membership-lookup", func(size, auxiliary int) (*scenarioResult, error) {
		return liveLookup(size, auxiliary)
	})
	registerScenario("immutable-membership-lookup", func(size, auxiliary int) (*scenarioResult, error) {
		return immutableLookup(size, auxiliary)
	})
	registerScenario("live-membership-random-lookup", func(size, auxiliary int) (*scenarioResult, error) {
		return liveRandomLookup(size, auxiliary)
	})
	registerScenario("immutable-membership-random-lookup", func(size, auxiliary int) (*scenarioResult, error) {
		return immutableRandomLookup(size, auxiliary)
	})
	registerScenario("live-feed-scan", func(size, auxiliary int) (*scenarioResult, error) {
		return liveScan(size, auxiliary)
	})
	registerScenario("immutable-feed-scan", func(size, auxiliary int) (*scenarioResult, error) {
		return immutableScan(size, auxiliary)
	})
}

// shapedFeed mirrors Rust membership::shaped_feed: prepare the database
// (membership file plus, for second feeds, the "first" feed) outside the
// measurement, create one named feed with the shape source inside it,
// and verify the workflow report and the final range/feed facts.
func shapedFeed(name string, size int, shape feedShape, second bool) (*scenarioResult, error) {
	database, err := newTestDatabase(name)
	if err != nil {
		return nil, err
	}
	defer database.cleanup()
	if second {
		if err := createMembershipFile(database); err != nil {
			return nil, err
		}
		if _, err := createShapedFeed(database, "first", size, shape); err != nil {
			return nil, err
		}
	}
	var report iprangedb.WorkflowReport
	err, measured := operation(func() error {
		if !second {
			if err := createMembershipFile(database); err != nil {
				return err
			}
		}
		feed := "first"
		if second {
			feed = "second"
		}
		var err error
		report, err = createShapedFeed(database, feed, size, shape)
		return err
	})
	if err != nil {
		return nil, err
	}
	expected := shape.expectedIntervals(size)
	if report.InputRecordCount != uint64(size) || report.InputNormalizedIntervalCount != expected {
		return nil, fmt.Errorf("unexpected shaped-feed report: %+v", report)
	}
	auxiliary := 0
	if second {
		auxiliary = 1
	}
	measuredResult, err := result(name, size, auxiliary, uint64(size), database, measured, database.main)
	if err != nil {
		return nil, err
	}
	expectedFeeds := uint64(1)
	if second {
		expectedFeeds = 2
	}
	if measuredResult.RangeRecords != expected || measuredResult.Feeds != expectedFeeds {
		return nil, fmt.Errorf("%s produced %d ranges and %d feeds; expected %d and %d",
			name, measuredResult.RangeRecords, measuredResult.Feeds, expected, expectedFeeds)
	}
	return measuredResult, nil
}

// replaceFeed mirrors Rust membership::replace_feed: populate a small
// database, then replace the last named feed with size dispersed
// singletons at (index+phase)*4 and report the live facts.
func replaceFeed(size, feeds int) (*scenarioResult, error) {
	feeds = max(feeds, 1)
	database, err := populated("feed-replace", min(size, 128), feeds)
	if err != nil {
		return nil, err
	}
	defer database.cleanup()
	phase := uint32(max(size/11, 1))
	var measured measurement
	err, measured = operation(func() (result error) {
		writer, err := iprangedb.OpenLiveWriter(database.main, toPageBudget(transactionBudget(size, feeds)), nil)
		if err != nil {
			return err
		}
		// Rust drops the writer on every failure path; mirror with a
		// best-effort close so the database lease is released.
		defer func() {
			if result != nil {
				_, _ = writer.Close()
			}
		}()
		feed, err := feedName(feeds - 1)
		if err != nil {
			return err
		}
		workflow, err := writer.BeginReplaceFeed(feed, nil)
		if err != nil {
			return err
		}
		source, err := newAddressSource(size, phase)
		if err != nil {
			return err
		}
		for {
			batch, ok := source.nextBatch()
			if !ok {
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
		if finished.IsChanged() {
			if err := requireCommitted(finished.Commit()); err != nil {
				return err
			}
			return closeWriter(writer)
		}
		return fmt.Errorf("feed replacement changed nothing: %+v", finished.Report())
	})
	if err != nil {
		return nil, err
	}
	return result("feed-replace", size, feeds, uint64(size), database, measured, database.main)
}

// membershipImport mirrors Rust membership::import: build the source and
// destination membership databases, import the source into the
// destination through a live reader source, and report the destination
// facts.
func membershipImport(size, feeds int) (*scenarioResult, error) {
	feeds = max(feeds, 1)
	source, err := populated("membership-import-source", size, feeds)
	if err != nil {
		return nil, err
	}
	defer source.cleanup()
	destination, err := populated("membership-import-destination", min(size, 128), (feeds+1)/2)
	if err != nil {
		return nil, err
	}
	defer destination.cleanup()
	sourceReader, err := iprangedb.OpenLiveReader(source.main, nil)
	if err != nil {
		return nil, err
	}
	var measured measurement
	err, measured = operation(func() (result error) {
		writer, err := iprangedb.OpenLiveWriter(destination.main, toPageBudget(transactionBudget(size, feeds)), nil)
		if err != nil {
			return err
		}
		defer func() {
			if result != nil {
				_, _ = writer.Close()
			}
		}()
		workflow, err := writer.BeginMembershipImport(iprangedb.MembershipImportSourceLive(sourceReader), nil)
		if err != nil {
			return err
		}
		finished, err := workflow.FinishInput()
		if err != nil {
			return err
		}
		if finished.IsChanged() {
			if err := requireCommitted(finished.Commit()); err != nil {
				return err
			}
			return closeWriter(writer)
		}
		return fmt.Errorf("membership import changed nothing: %+v", finished.Report())
	})
	if err != nil {
		return nil, err
	}
	if err := closeLiveReader(sourceReader); err != nil {
		return nil, err
	}
	return result("membership-import", size, feeds, uint64(size), destination, measured, destination.main)
}

// liveLookup mirrors Rust membership::live_lookup: sweep the dispersed
// address list through the live membership reader and require every
// lookup to hit the target feed.
func liveLookup(size, feeds int) (*scenarioResult, error) {
	feeds = max(feeds, 1)
	database, err := populated("live-membership-lookup", size, feeds)
	if err != nil {
		return nil, err
	}
	defer database.cleanup()
	reader, err := iprangedb.OpenLiveReader(database.main, nil)
	if err != nil {
		return nil, err
	}
	pin, err := reader.Pin()
	if err != nil {
		return nil, err
	}
	target, repetitions, workUnits, err := membershipReaderWork(size, feeds, reader.LookupFeed)
	if err != nil {
		return nil, err
	}
	var hits uint64
	err, measured := operation(func() error {
		var err error
		hits, err = countPoints(size, repetitions, func(address iprangedb.IPv4) (bool, error) {
			view, found, err := pin.LookupMembershipV4(address)
			return membershipContains(view, found, err, target)
		})
		return err
	})
	if err != nil {
		return nil, err
	}
	if err := requireCount("live membership lookup", hits, workUnits, "addresses"); err != nil {
		return nil, err
	}
	if err := pin.Close(); err != nil {
		return nil, err
	}
	if err := closeLiveReader(reader); err != nil {
		return nil, err
	}
	return result("live-membership-lookup", size, feeds, workUnits, database, measured, database.main)
}

// immutableLookup mirrors Rust membership::immutable_lookup: snapshot
// the populated database and sweep the dispersed address list through
// the immutable membership reader.
func immutableLookup(size, feeds int) (*scenarioResult, error) {
	feeds = max(feeds, 1)
	database, err := populated("immutable-membership-lookup", size, feeds)
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
	pin, err := reader.Pin()
	if err != nil {
		return nil, err
	}
	target, repetitions, workUnits, err := membershipReaderWork(size, feeds, reader.LookupFeed)
	if err != nil {
		return nil, err
	}
	var hits uint64
	err, measured := operation(func() error {
		var err error
		hits, err = countPoints(size, repetitions, func(address iprangedb.IPv4) (bool, error) {
			view, found, err := pin.LookupMembershipV4(address)
			return membershipContains(view, found, err, target)
		})
		return err
	})
	if err != nil {
		return nil, err
	}
	if err := requireCount("immutable membership lookup", hits, workUnits, "addresses"); err != nil {
		return nil, err
	}
	if err := pin.Close(); err != nil {
		return nil, err
	}
	if err := reader.Close(); err != nil {
		return nil, err
	}
	return result("immutable-membership-lookup", size, feeds, workUnits, database, measured, snapshot)
}

// liveRandomLookup mirrors Rust membership::live_random_lookup: sweep
// the shuffled dispersed point list through the live membership reader.
func liveRandomLookup(size, feeds int) (*scenarioResult, error) {
	feeds = max(feeds, 1)
	database, err := populated("live-membership-random-lookup", size, feeds)
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
	pin, err := reader.Pin()
	if err != nil {
		return nil, err
	}
	target, repetitions, workUnits, err := membershipReaderWork(size, feeds, reader.LookupFeed)
	if err != nil {
		return nil, err
	}
	var hits uint64
	err, measured := operation(func() error {
		var err error
		hits, err = countRandomPoints(points, repetitions, func(address iprangedb.IPv4) (bool, error) {
			view, found, err := pin.LookupMembershipV4(address)
			return membershipContains(view, found, err, target)
		})
		return err
	})
	if err != nil {
		return nil, err
	}
	if err := requireCount("live random membership lookup", hits, workUnits, "addresses"); err != nil {
		return nil, err
	}
	if err := pin.Close(); err != nil {
		return nil, err
	}
	if err := closeLiveReader(reader); err != nil {
		return nil, err
	}
	return result("live-membership-random-lookup", size, feeds, workUnits, database, measured, database.main)
}

// immutableRandomLookup mirrors Rust
// membership::immutable_random_lookup: sweep the shuffled dispersed
// point list through the immutable membership reader of the snapshot.
func immutableRandomLookup(size, feeds int) (*scenarioResult, error) {
	feeds = max(feeds, 1)
	database, err := populated("immutable-membership-random-lookup", size, feeds)
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
	pin, err := reader.Pin()
	if err != nil {
		return nil, err
	}
	target, repetitions, workUnits, err := membershipReaderWork(size, feeds, reader.LookupFeed)
	if err != nil {
		return nil, err
	}
	var hits uint64
	err, measured := operation(func() error {
		var err error
		hits, err = countRandomPoints(points, repetitions, func(address iprangedb.IPv4) (bool, error) {
			view, found, err := pin.LookupMembershipV4(address)
			return membershipContains(view, found, err, target)
		})
		return err
	})
	if err != nil {
		return nil, err
	}
	if err := requireCount("immutable random membership lookup", hits, workUnits, "addresses"); err != nil {
		return nil, err
	}
	if err := pin.Close(); err != nil {
		return nil, err
	}
	if err := reader.Close(); err != nil {
		return nil, err
	}
	return result("immutable-membership-random-lookup", size, feeds, workUnits, database, measured, snapshot)
}

// liveScan mirrors Rust membership::live_scan: open the named-feed range
// projection cursor once per repetition and count every coalesced range
// of the last feed.
func liveScan(size, feeds int) (*scenarioResult, error) {
	feeds = max(feeds, 1)
	database, err := populated("live-feed-scan", size, feeds)
	if err != nil {
		return nil, err
	}
	defer database.cleanup()
	reader, err := iprangedb.OpenLiveReader(database.main, nil)
	if err != nil {
		return nil, err
	}
	name, repetitions, workUnits, err := membershipScanWork(size, feeds)
	if err != nil {
		return nil, err
	}
	var records uint64
	err, measured := operation(func() error {
		var err error
		records, err = countCursor[iprangedb.FeedRangeCursorV4](repetitions,
			func() (*iprangedb.FeedRangeCursorV4, error) {
				return reader.FeedRangeCursorV4(name, iprangedb.RangeDirectionForward)
			},
			func(cursor *iprangedb.FeedRangeCursorV4) (bool, error) {
				_, ok, err := cursor.NextRange()
				return ok, err
			})
		return err
	})
	if err != nil {
		return nil, err
	}
	if err := requireCount("live feed scan", records, workUnits, "ranges"); err != nil {
		return nil, err
	}
	if err := closeLiveReader(reader); err != nil {
		return nil, err
	}
	return result("live-feed-scan", size, feeds, workUnits, database, measured, database.main)
}

// immutableScan mirrors Rust membership::immutable_scan: the same
// named-feed range scan over the snapshot's immutable reader.
func immutableScan(size, feeds int) (*scenarioResult, error) {
	feeds = max(feeds, 1)
	database, err := populated("immutable-feed-scan", size, feeds)
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
	name, repetitions, workUnits, err := membershipScanWork(size, feeds)
	if err != nil {
		return nil, err
	}
	var records uint64
	err, measured := operation(func() error {
		var err error
		records, err = countCursor[iprangedb.FeedRangeCursorV4](repetitions,
			func() (*iprangedb.FeedRangeCursorV4, error) {
				return reader.FeedRangeCursorV4(name, iprangedb.RangeDirectionForward)
			},
			func(cursor *iprangedb.FeedRangeCursorV4) (bool, error) {
				_, ok, err := cursor.NextRange()
				return ok, err
			})
		return err
	})
	if err != nil {
		return nil, err
	}
	if err := requireCount("immutable feed scan", records, workUnits, "ranges"); err != nil {
		return nil, err
	}
	if err := reader.Close(); err != nil {
		return nil, err
	}
	return result("immutable-feed-scan", size, feeds, workUnits, database, measured, snapshot)
}

// populated mirrors Rust membership::populated: create a fresh
// membership database whose feeds all carry the same size dispersed
// singleton ranges (start = index*4) and whose membership bitmaps name
// every feed.
func populated(label string, ranges, feeds int) (database *testDatabase, err error) {
	database, err = newTestDatabase(label)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil && database != nil {
			database.cleanup()
		}
	}()
	if err = createMembershipFile(database); err != nil {
		return nil, err
	}
	writer, err := iprangedb.OpenLiveWriter(database.main, toPageBudget(transactionBudget(ranges, feeds)), nil)
	if err != nil {
		return nil, err
	}
	transaction, err := writer.BeginMembershipTransaction(nil)
	if err != nil {
		return nil, err
	}
	membership, err := transaction.EmptyMembership()
	if err != nil {
		return nil, err
	}
	for index := 0; index < feeds; index++ {
		feedNameValue, err := feedName(index)
		if err != nil {
			return nil, err
		}
		feed, err := transaction.EnsureFeed(feedNameValue)
		if err != nil {
			return nil, err
		}
		membership, err = transaction.AddFeed(membership, feed)
		if err != nil {
			return nil, err
		}
	}
	for index := 0; index < ranges; index++ {
		start := iprangedb.IPv4(uint32(index) * 4)
		if _, err = transaction.ApplyV4(start, start+1, membership, iprangedb.MembershipReplace); err != nil {
			return nil, err
		}
	}
	if err = requireCommitted(transaction.Commit()); err != nil {
		return nil, err
	}
	if err = closeWriter(writer); err != nil {
		return nil, err
	}
	return database, nil
}

// createMembershipFile mirrors Rust membership::create_membership_file:
// create an empty live membership database with the benchmark value tag
// and one reader slot.
func createMembershipFile(database *testDatabase) error {
	tag, err := membershipValueTag()
	if err != nil {
		return err
	}
	_, err = iprangedb.CreateLive(database.main, iprangedb.AddressFamilyIPv4,
		iprangedb.ValueKindMembership, iprangedb.StructureKindNone, tag, 1, nil)
	return err
}

// createShapedFeed mirrors Rust membership::create_shaped_feed: create
// one named feed from the shape source, require a changed commit, and
// return the workflow report.
func createShapedFeed(database *testDatabase, name string, size int, shape feedShape) (report iprangedb.WorkflowReport, err error) {
	writer, err := iprangedb.OpenLiveWriter(database.main, toPageBudget(transactionBudget(size, 2)), nil)
	if err != nil {
		return iprangedb.WorkflowReport{}, err
	}
	defer func() {
		if err != nil {
			_, _ = writer.Close()
		}
	}()
	feed, err := iprangedb.NewFeedName(name)
	if err != nil {
		return iprangedb.WorkflowReport{}, err
	}
	workflow, err := writer.BeginCreateFeed(feed, nil)
	if err != nil {
		return iprangedb.WorkflowReport{}, err
	}
	source, err := newFeedShapeSource(size, shape)
	if err != nil {
		return iprangedb.WorkflowReport{}, err
	}
	for {
		batch, ok := source.nextBatch()
		if !ok {
			break
		}
		if err := workflow.AddRangesV4(batch); err != nil {
			return iprangedb.WorkflowReport{}, err
		}
	}
	finished, err := workflow.FinishInput()
	if err != nil {
		return iprangedb.WorkflowReport{}, err
	}
	report = finished.Report()
	if finished.IsChanged() {
		if err = requireCommitted(finished.Commit()); err != nil {
			return iprangedb.WorkflowReport{}, err
		}
		err = closeWriter(writer)
		return report, err
	}
	return iprangedb.WorkflowReport{}, fmt.Errorf("feed creation changed nothing: %+v", report)
}

// feedName mirrors Rust membership::feed_name: the zero-padded six-digit
// feed catalog name of one index.
func feedName(index int) (iprangedb.FeedName, error) {
	return iprangedb.NewFeedName(fmt.Sprintf("feed-%06d", index))
}

// membershipReaderWork mirrors Rust membership::membership_reader_work:
// resolve the target feed (the last feed) and shape the reader
// repetitions and work units.
func membershipReaderWork(size, feeds int, lookup func(string) (iprangedb.FeedEntry, bool, error)) (uint32, int, uint64, error) {
	name, err := feedName(feeds - 1)
	if err != nil {
		return 0, 0, 0, err
	}
	target, found, err := lookup(string(name))
	if err != nil {
		return 0, 0, 0, err
	}
	if !found {
		return 0, 0, 0, fmt.Errorf("target feed is absent")
	}
	repetitions, workUnits, err := readerWork(size)
	if err != nil {
		return 0, 0, 0, err
	}
	return target.Index, repetitions, workUnits, nil
}

// membershipScanWork mirrors Rust membership::membership_scan_work: the
// scanned feed name (the last feed) plus the reader repetitions and work
// units.
func membershipScanWork(size, feeds int) (string, int, uint64, error) {
	name, err := feedName(feeds - 1)
	if err != nil {
		return "", 0, 0, err
	}
	repetitions, workUnits, err := readerWork(size)
	if err != nil {
		return "", 0, 0, err
	}
	return string(name), repetitions, workUnits, nil
}

// membershipContains mirrors Rust membership::membership_contains: an
// absent membership never hits; a present membership hits when the
// target feed index is a member.
func membershipContains(view iprangedb.MembershipView, found bool, err error, feedIndex uint32) (bool, error) {
	if err != nil {
		return false, err
	}
	if !found {
		return false, nil
	}
	return view.ContainsIndex(feedIndex)
}

// requireCount mirrors Rust scenarios::require_count.
func requireCount(label string, observed, expected uint64, noun string) error {
	if observed != expected {
		return fmt.Errorf("%s returned %d of %d %s", label, observed, expected, noun)
	}
	return nil
}

// membershipValueTag mirrors Rust membership.rs ValueTag::new(b"membership").
func membershipValueTag() (iprangedb.ValueTag, error) {
	tag, err := iprangedb.NewValueTag([]byte("membership"))
	if err != nil {
		return iprangedb.ValueTag{}, fmt.Errorf("invalid benchmark value tag")
	}
	return tag, nil
}
