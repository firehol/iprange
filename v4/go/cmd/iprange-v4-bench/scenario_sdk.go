// SDK scenario group of the Go update-ipsets benchmark port (Rust
// benches/update_ipsets/scenarios/sdk.rs parity): immutable single-feed
// publication, history projection, membership matching/aggregation,
// direct and membership provider joins, membership algebra analysis and
// publication, and the complete update-ipsets workflow. Every scenario
// mirrors the Rust instruction-for-instruction: same names, work units,
// and assertions, with the Go SDK symbol used wherever the Rust name
// differs (mapped in the per-helper comments).
//
// Name mapping notes (Rust -> Go):
//   - create_immutable_feed_v4 -> iprangedb.CreateImmutableFeedV4: the
//     one-inode immutable feed builder (immutable_feed_public.go; the
//     Rust immutable_feed.rs parity row is present), fed by the bench
//     batch sources through the public RangeSource4 seam.
//   - seeded_direct / seeded_direct_with_tag -> sdkSeededDirect /
//     sdkSeededDirectWithTag.
//   - populated / populated_rotating -> sdkPopulated / sdkPopulatedRotating.
//   - create_membership -> sdkCreateMembership; all_scope -> sdkAllScope.
//   - history_windows -> sdkHistoryWindows; feed_name -> sdkFeedName;
//     tag -> sdkTag; require_published -> sdkRequirePublished;
//     immutable_budget -> sdkImmutableFeedBudgetFor;
//     algebra_output_budget -> sdkAlgebraOutputBudget;
//     query_budget -> sdkQueryBudget; algebra_budget -> sdkAlgebraBudget.
//   - AggregateCounter/DirectCounter/MembershipCounter -> sdk-prefixed
//     counters with the Go sink closure shapes (membership scope
//     Aggregate yields, join yield closures).
//   - named_feed_source_v4 -> sdkFeedRangeBatches (a forward
//     FeedRangeCursorV4 drained into bounded AddressRange4 batches).
//   - workflow.rs helpers -> sdkWorkflow* functions in this file.
//
// All helper names are prefixed with sdk so this file cannot collide with
// the sibling scenario group files (direct, membership, read,
// structured) that the other workers write in parallel.

package main

import (
	"fmt"

	iprangedb "github.com/firehol/iprange/v4/go"
)

const sdkQueryHeap = 64 * 1024 * 1024

func init() {
	registerScenario("immutable-feed-random", sdkImmutableFeed)
	registerScenario("history-project", sdkHistoryProject)
	registerScenario("membership-matching-feeds", sdkMatchingFeeds)
	registerScenario("membership-cardinalities", sdkAggregateCardinalities)
	registerScenario("membership-selected-pair", sdkAggregateSelectedPair)
	registerScenario("membership-all-pairs", sdkAggregateAllPairs)
	registerScenario("direct-provider-join", sdkDirectJoin)
	registerScenario("membership-provider-join", sdkMembershipJoin)
	registerScenario("algebra-count", sdkAlgebraCount)
	registerScenario("algebra-compare", sdkAlgebraCompare)
	registerScenario("algebra-publish-preserve", sdkAlgebraPublishPreserve)
	registerScenario("algebra-publish-flat", sdkAlgebraPublishFlat)
	registerScenario("update-ipsets-workflow", sdkUpdateIpsetsWorkflow)
}

// ---------------------------------------------------------------------------
// immutable-feed-random (Rust sdk::immutable_feed)
// ---------------------------------------------------------------------------

func sdkImmutableFeed(size int, auxiliary int) (*scenarioResult, error) {
	database, err := newTestDatabase("immutable-feed-random")
	if err != nil {
		return nil, err
	}
	defer database.cleanup()
	source, err := newFeedShapeSource(size, feedRandomDisjoint)
	if err != nil {
		return nil, err
	}
	valueTag, err := sdkTag([]byte("downloaded"))
	if err != nil {
		return nil, err
	}
	name, err := sdkFeedName(0)
	if err != nil {
		return nil, err
	}
	budget := sdkImmutableFeedBudgetFor(size)
	cancellation := iprangedb.NewCancellationToken()
	var result iprangedb.ImmutableFeedResult
	operationErr, measured := operation(func() error {
		var err error
		result, err = iprangedb.CreateImmutableFeedV4(
			database.main, valueTag, name, nil, iprangedb.PolicyFailIfExists,
			source, &budget, cancellation,
		)
		return err
	})
	if operationErr != nil {
		return nil, operationErr
	}
	if err := sdkRequirePublished(result.Publication); err != nil {
		return nil, err
	}
	if result.Report.InputRecordCount != uint64(size) || result.Report.NormalizedIntervalCount != uint64(size) {
		return nil, fmt.Errorf("unexpected immutable-feed report: %+v", result.Report)
	}
	return immutableResult(immutableResultSpec{
		Name:         "immutable-feed-random",
		Size:         size,
		Auxiliary:    0,
		WorkUnits:    uint64(size),
		EmittedUnits: result.Report.NormalizedIntervalCount,
	}, database, measured, database.main)
}

// ---------------------------------------------------------------------------
// history-project (Rust sdk::history_project)
// ---------------------------------------------------------------------------

func sdkHistoryProject(size int, windows int) (*scenarioResult, error) {
	windows = max(windows, 1)
	source, err := sdkSeededDirectWithTag("history-source", size, 2, iprangedb.ValueTagLastSeen())
	if err != nil {
		return nil, err
	}
	defer source.cleanup()
	destination, err := newTestDatabase("history-project")
	if err != nil {
		return nil, err
	}
	defer destination.cleanup()
	if err := sdkCreateMembership(destination); err != nil {
		return nil, err
	}
	requests, err := sdkHistoryWindows(windows)
	if err != nil {
		return nil, err
	}
	cancellation := iprangedb.NewCancellationToken()
	sourceReader, err := iprangedb.OpenLiveReader(source.main, cancellation)
	if err != nil {
		return nil, err
	}
	var report iprangedb.HistoryProjectionReport
	operationErr, measured := operation(func() error {
		writer, err := iprangedb.OpenLiveWriter(destination.main, toPageBudget(transactionBudget(size, windows)), cancellation)
		if err != nil {
			return err
		}
		finished, err := writer.ProjectHistory(iprangedb.HistoryProjectionSource{
			Kind: iprangedb.HistoryProjectionSourceLive,
			Live: sourceReader,
		}, requests, cancellation)
		if err != nil {
			return err
		}
		if !finished.IsChanged() {
			return fmt.Errorf("new history projection changed nothing: %+v", finished.Report())
		}
		report = finished.Report()
		if err := requireCommitted(finished.Commit()); err != nil {
			return err
		}
		return closeWriter(writer)
	})
	if operationErr != nil {
		return nil, operationErr
	}
	if report.SourceRangeCount != uint64(size) || len(report.Windows) != windows {
		return nil, fmt.Errorf("unexpected history projection report: %+v", report)
	}
	if err := closeLiveReader(sourceReader); err != nil {
		return nil, err
	}
	output, err := result("history-project", size, windows, uint64(size), destination, measured, destination.main)
	if err != nil {
		return nil, err
	}
	output.EmittedUnits = report.AfterIntervalCount
	return output, nil
}

// ---------------------------------------------------------------------------
// membership-matching-feeds (Rust sdk::matching_feeds)
// ---------------------------------------------------------------------------

func sdkMatchingFeeds(size int, feeds int) (*scenarioResult, error) {
	feeds = max(feeds, 1)
	width := min(feeds, 4)
	database, err := sdkPopulatedRotating("membership-matching-feeds", size, feeds, width)
	if err != nil {
		return nil, err
	}
	defer database.cleanup()
	cancellation := iprangedb.NewCancellationToken()
	reader, err := iprangedb.OpenLiveReader(database.main, cancellation)
	if err != nil {
		return nil, err
	}
	repetitions, workUnits, err := readerWork(size)
	if err != nil {
		return nil, err
	}
	var emitted uint64
	var hits uint64
	operationErr, measured := operation(func() error {
		var err error
		hits, err = countPoints(size, repetitions, func(address iprangedb.IPv4) (bool, error) {
			var pointEmitted uint64
			query, err := reader.MembershipQuery()
			if err != nil {
				return false, err
			}
			report, err := query.MatchingFeedsV4(address, func(name string) error {
				_ = name
				if pointEmitted == ^uint64(0) {
					return fmt.Errorf("matching-feed result count overflow")
				}
				pointEmitted++
				return nil
			}, cancellation)
			if err != nil {
				return false, err
			}
			if report.MatchingFeedCount != pointEmitted {
				return false, fmt.Errorf("matching-feed report disagrees with its sink")
			}
			if ^uint64(0)-emitted < pointEmitted {
				return false, fmt.Errorf("matching-feed result count overflow")
			}
			emitted += pointEmitted
			return pointEmitted != 0, nil
		})
		return err
	})
	if operationErr != nil {
		return nil, operationErr
	}
	if err := sdkRequireCount("matching feeds", hits, workUnits, "addresses"); err != nil {
		return nil, err
	}
	if err := closeLiveReader(reader); err != nil {
		return nil, err
	}
	output, err := result("membership-matching-feeds", size, feeds, workUnits, database, measured, database.main)
	if err != nil {
		return nil, err
	}
	output.EmittedUnits = emitted
	return output, nil
}

// ---------------------------------------------------------------------------
// membership aggregation (Rust sdk::aggregate_*)
// ---------------------------------------------------------------------------

func sdkAggregateCardinalities(size int, feeds int) (*scenarioResult, error) {
	feeds = max(feeds, 1)
	return sdkAggregate("membership-cardinalities", size, feeds, min(feeds, 4), iprangedb.MembershipAggregationCardinalities())
}

func sdkAggregateSelectedPair(size int, auxiliary int) (*scenarioResult, error) {
	left, err := sdkFeedName(0)
	if err != nil {
		return nil, err
	}
	right, err := sdkFeedName(1)
	if err != nil {
		return nil, err
	}
	pair := []iprangedb.FeedPair{{Left: string(left), Right: string(right)}}
	return sdkAggregate("membership-selected-pair", size, 2, 1, iprangedb.MembershipAggregationSelectedPairs(pair))
}

func sdkAggregateAllPairs(size int, feeds int) (*scenarioResult, error) {
	feeds = max(feeds, 2)
	return sdkAggregate("membership-all-pairs", size, feeds, (feeds+1)/2, iprangedb.MembershipAggregationAllPairs())
}

func sdkAggregate(name string, size, feeds, width int, mode iprangedb.MembershipAggregationMode) (*scenarioResult, error) {
	database, err := sdkPopulatedRotating(name, size, feeds, width)
	if err != nil {
		return nil, err
	}
	defer database.cleanup()
	cancellation := iprangedb.NewCancellationToken()
	reader, err := iprangedb.OpenLiveReader(database.main, cancellation)
	if err != nil {
		return nil, err
	}
	query, err := reader.MembershipQuery()
	if err != nil {
		return nil, err
	}
	scope, err := query.AllFeeds(sdkQueryBudget(), cancellation)
	if err != nil {
		return nil, err
	}
	counter := &sdkAggregateCounter{}
	var report iprangedb.MembershipAggregationReport
	operationErr, measured := operation(func() error {
		var err error
		report, err = scope.Aggregate(mode, counter.feedCardinalities, counter.feedOverlaps, cancellation)
		return err
	})
	if operationErr != nil {
		return nil, operationErr
	}
	if report.ScannedRangeCount != uint64(size) ||
		report.FeedResultCount != uint64(feeds) ||
		counter.feeds != report.FeedResultCount ||
		counter.pairs != report.PairResultCount {
		return nil, fmt.Errorf("unexpected membership aggregation report: %+v", report)
	}
	if err := closeLiveReader(reader); err != nil {
		return nil, err
	}
	output, err := result(name, size, feeds, uint64(size), database, measured, database.main)
	if err != nil {
		return nil, err
	}
	output.EmittedUnits = report.FeedResultCount + report.PairResultCount
	return output, nil
}

// ---------------------------------------------------------------------------
// direct-provider-join (Rust sdk::direct_join)
// ---------------------------------------------------------------------------

func sdkDirectJoin(size int, feeds int) (*scenarioResult, error) {
	feeds = max(feeds, 1)
	membership, err := sdkPopulatedRotating("direct-provider-join", size, feeds, min(feeds, 4))
	if err != nil {
		return nil, err
	}
	defer membership.cleanup()
	provider, err := sdkSeededDirect("direct-provider", size, 1)
	if err != nil {
		return nil, err
	}
	defer provider.cleanup()
	cancellation := iprangedb.NewCancellationToken()
	membershipReader, err := iprangedb.OpenLiveReader(membership.main, cancellation)
	if err != nil {
		return nil, err
	}
	providerReader, err := iprangedb.OpenLiveReader(provider.main, cancellation)
	if err != nil {
		return nil, err
	}
	query, err := membershipReader.MembershipQuery()
	if err != nil {
		return nil, err
	}
	scope, err := query.AllFeeds(sdkQueryBudget(), cancellation)
	if err != nil {
		return nil, err
	}
	counter := &sdkDirectCounter{}
	resultCells := uint64(feeds) * uint64(min(size, 251))
	var report iprangedb.DirectJoinReport
	operationErr, measured := operation(func() error {
		var err error
		report, err = scope.JoinDirect(
			iprangedb.DirectJoinSourceLive(providerReader),
			iprangedb.DirectJoinBudget{MaxResultCells: resultCells},
			counter.cells,
			cancellation,
		)
		return err
	})
	if operationErr != nil {
		return nil, operationErr
	}
	if report.MembershipRangeCount != uint64(size) ||
		report.DirectRangesVisited != uint64(size) ||
		report.JoinedSegmentCount != uint64(size) ||
		counter.count != report.ResultCellCount {
		return nil, fmt.Errorf("unexpected direct join report: %+v", report)
	}
	if err := closeLiveReader(providerReader); err != nil {
		return nil, err
	}
	if err := closeLiveReader(membershipReader); err != nil {
		return nil, err
	}
	output, err := result("direct-provider-join", size, feeds, uint64(size), membership, measured, membership.main)
	if err != nil {
		return nil, err
	}
	output.EmittedUnits = report.ResultCellCount
	return output, nil
}

// ---------------------------------------------------------------------------
// membership-provider-join (Rust sdk::membership_join)
// ---------------------------------------------------------------------------

func sdkMembershipJoin(size int, feeds int) (*scenarioResult, error) {
	feeds = max(feeds, 1)
	width := min(feeds, 4)
	left, err := sdkPopulatedRotating("membership-provider-left", size, feeds, width)
	if err != nil {
		return nil, err
	}
	defer left.cleanup()
	right, err := sdkPopulatedRotating("membership-provider-right", size, feeds, width)
	if err != nil {
		return nil, err
	}
	defer right.cleanup()
	cancellation := iprangedb.NewCancellationToken()
	leftReader, err := iprangedb.OpenLiveReader(left.main, cancellation)
	if err != nil {
		return nil, err
	}
	rightReader, err := iprangedb.OpenLiveReader(right.main, cancellation)
	if err != nil {
		return nil, err
	}
	leftQuery, err := leftReader.MembershipQuery()
	if err != nil {
		return nil, err
	}
	leftScope, err := leftQuery.AllFeeds(sdkQueryBudget(), cancellation)
	if err != nil {
		return nil, err
	}
	rightQuery, err := rightReader.MembershipQuery()
	if err != nil {
		return nil, err
	}
	rightScope, err := rightQuery.AllFeeds(sdkQueryBudget(), cancellation)
	if err != nil {
		return nil, err
	}
	counter := &sdkMembershipCounter{}
	var report iprangedb.MembershipJoinReport
	operationErr, measured := operation(func() error {
		var err error
		report, err = leftScope.JoinMembership(rightScope, counter.membershipCrossCells, counter.uncoveredFeeds, cancellation)
		return err
	})
	if operationErr != nil {
		return nil, operationErr
	}
	if report.LeftRangeCount != uint64(size) ||
		report.RightRangeCount != uint64(size) ||
		report.JoinedSegmentCount != uint64(size) ||
		counter.cross != report.CrossResultCount ||
		counter.uncovered != report.UncoveredResultCount {
		return nil, fmt.Errorf("unexpected membership join report: %+v", report)
	}
	if err := closeLiveReader(rightReader); err != nil {
		return nil, err
	}
	if err := closeLiveReader(leftReader); err != nil {
		return nil, err
	}
	output, err := result("membership-provider-join", size, feeds, uint64(size), left, measured, left.main)
	if err != nil {
		return nil, err
	}
	output.EmittedUnits = report.CrossResultCount + report.UncoveredResultCount
	return output, nil
}

// ---------------------------------------------------------------------------
// algebra analysis (Rust sdk::algebra_count / algebra_compare /
// algebra_analysis)
// ---------------------------------------------------------------------------

func sdkAlgebraCount(size int, feeds int) (*scenarioResult, error) {
	return sdkAlgebraAnalysis("algebra-count", size, feeds, func(algebra *iprangedb.MembershipAlgebra, cancellation *iprangedb.CancellationToken) (uint64, uint64, error) {
		report, err := algebra.Count(iprangedb.AlgebraFeedSelectionAll(), cancellation)
		if err != nil {
			return 0, 0, err
		}
		return report.SourceRangeCount, 1, nil
	})
}

func sdkAlgebraCompare(size int, feeds int) (*scenarioResult, error) {
	leftName, err := sdkFeedName(0)
	if err != nil {
		return nil, err
	}
	rightName, err := sdkFeedName(1)
	if err != nil {
		return nil, err
	}
	left := []string{string(leftName)}
	right := []string{string(rightName)}
	return sdkAlgebraAnalysis("algebra-compare", size, max(feeds, 2), func(algebra *iprangedb.MembershipAlgebra, cancellation *iprangedb.CancellationToken) (uint64, uint64, error) {
		report, err := algebra.Compare(
			iprangedb.AlgebraFeedSelectionNamed(left),
			iprangedb.AlgebraFeedSelectionNamed(right),
			cancellation,
		)
		if err != nil {
			return 0, 0, err
		}
		return report.SourceRangeCount, 1, nil
	})
}

type sdkAlgebraOperation func(algebra *iprangedb.MembershipAlgebra, cancellation *iprangedb.CancellationToken) (uint64, uint64, error)

func sdkAlgebraAnalysis(name string, size, feeds int, operationFn sdkAlgebraOperation) (*scenarioResult, error) {
	feeds = max(feeds, 2)
	width := min(feeds, 4)
	left, err := sdkPopulatedRotating("algebra-analysis-left", size, feeds, width)
	if err != nil {
		return nil, err
	}
	defer left.cleanup()
	right, err := sdkPopulatedRotating("algebra-analysis-right", size, feeds, width)
	if err != nil {
		return nil, err
	}
	defer right.cleanup()
	cancellation := iprangedb.NewCancellationToken()
	leftReader, err := iprangedb.OpenLiveReader(left.main, cancellation)
	if err != nil {
		return nil, err
	}
	rightReader, err := iprangedb.OpenLiveReader(right.main, cancellation)
	if err != nil {
		return nil, err
	}
	leftScope, err := sdkAllScope(leftReader, cancellation)
	if err != nil {
		return nil, err
	}
	rightScope, err := sdkAllScope(rightReader, cancellation)
	if err != nil {
		return nil, err
	}
	algebra, err := iprangedb.NewMembershipAlgebra([]*iprangedb.MembershipScope{leftScope, rightScope}, sdkAlgebraBudget(), cancellation)
	if err != nil {
		return nil, err
	}
	var scanned, emitted uint64
	operationErr, measured := operation(func() error {
		var err error
		scanned, emitted, err = operationFn(algebra, cancellation)
		return err
	})
	if operationErr != nil {
		return nil, operationErr
	}
	if scanned != uint64(size)*2 {
		return nil, fmt.Errorf("%s scanned %d source ranges", name, scanned)
	}
	if err := closeLiveReader(rightReader); err != nil {
		return nil, err
	}
	if err := closeLiveReader(leftReader); err != nil {
		return nil, err
	}
	output, err := result(name, size, feeds, scanned, left, measured, left.main)
	if err != nil {
		return nil, err
	}
	output.EmittedUnits = emitted
	return output, nil
}

// ---------------------------------------------------------------------------
// algebra publication (Rust sdk::algebra_publish)
// ---------------------------------------------------------------------------

func sdkAlgebraPublishPreserve(size int, feeds int) (*scenarioResult, error) {
	return sdkAlgebraPublish(size, feeds, false)
}

func sdkAlgebraPublishFlat(size int, feeds int) (*scenarioResult, error) {
	return sdkAlgebraPublish(size, feeds, true)
}

func sdkAlgebraPublish(size int, feeds int, flat bool) (*scenarioResult, error) {
	name := "algebra-publish-preserve"
	if flat {
		name = "algebra-publish-flat"
	}
	feeds = max(feeds, 2)
	width := min(feeds, 4)
	left, err := sdkPopulatedRotating("algebra-publish-left", size, feeds, width)
	if err != nil {
		return nil, err
	}
	defer left.cleanup()
	right, err := sdkPopulatedRotating("algebra-publish-right", size, feeds, width)
	if err != nil {
		return nil, err
	}
	defer right.cleanup()
	outputPath := left.path("algebra-output.v4")
	cancellation := iprangedb.NewCancellationToken()
	leftReader, err := iprangedb.OpenLiveReader(left.main, cancellation)
	if err != nil {
		return nil, err
	}
	rightReader, err := iprangedb.OpenLiveReader(right.main, cancellation)
	if err != nil {
		return nil, err
	}
	leftScope, err := sdkAllScope(leftReader, cancellation)
	if err != nil {
		return nil, err
	}
	rightScope, err := sdkAllScope(rightReader, cancellation)
	if err != nil {
		return nil, err
	}
	algebra, err := iprangedb.NewMembershipAlgebra([]*iprangedb.MembershipScope{leftScope, rightScope}, sdkAlgebraBudget(), cancellation)
	if err != nil {
		return nil, err
	}
	var mode iprangedb.AlgebraOutputMode
	if flat {
		mode, err = iprangedb.AlgebraOutputModeFlat("result")
		if err != nil {
			return nil, err
		}
	} else {
		mode = iprangedb.AlgebraOutputModePreserveFeeds()
	}
	var result iprangedb.AlgebraSetResult
	operationErr, measured := operation(func() error {
		valueTag, err := sdkTag([]byte("algebra"))
		if err != nil {
			return err
		}
		result, err = algebra.PublishSet(
			outputPath,
			valueTag,
			iprangedb.AlgebraSetUnion(iprangedb.AlgebraFeedSelectionAll()),
			mode,
			nil,
			iprangedb.PolicyFailIfExists,
			sdkAlgebraOutputBudget(size),
			cancellation,
		)
		return err
	})
	if operationErr != nil {
		return nil, operationErr
	}
	if err := sdkRequirePublished(result.Publication); err != nil {
		return nil, err
	}
	expectedFeeds := uint64(feeds)
	if flat {
		expectedFeeds = 1
	}
	if result.Report.SourceRangeCount != uint64(size)*2 ||
		result.Report.OutputRangeCount != uint64(size) ||
		result.Report.OutputFeedCount != expectedFeeds {
		return nil, fmt.Errorf("unexpected algebra publication report: %+v", result.Report)
	}
	if err := closeLiveReader(rightReader); err != nil {
		return nil, err
	}
	if err := closeLiveReader(leftReader); err != nil {
		return nil, err
	}
	return immutableResult(immutableResultSpec{
		Name:         name,
		Size:         size,
		Auxiliary:    feeds,
		WorkUnits:    result.Report.SourceRangeCount,
		EmittedUnits: result.Report.OutputRangeCount,
	}, left, measured, outputPath)
}

// ---------------------------------------------------------------------------
// update-ipsets-workflow (Rust sdk/workflow.rs)
// ---------------------------------------------------------------------------

type sdkWorkflowFiles struct {
	previous  string
	current   string
	firstSeen string
	lastSeen  string
	central   string
	output    string
}

type sdkWorkflowReport struct {
	scanned uint64
	emitted uint64
}

func sdkUpdateIpsetsWorkflow(size int, windows int) (*scenarioResult, error) {
	windows = max(windows, 1)
	workspace, err := newTestDatabase("update-ipsets-workflow")
	if err != nil {
		return nil, err
	}
	defer workspace.cleanup()
	files := sdkWorkflowFiles{
		previous:  workspace.path("previous.v4"),
		current:   workspace.path("current.v4"),
		firstSeen: workspace.path("first-seen.v4"),
		lastSeen:  workspace.path("last-seen.v4"),
		central:   workspace.path("central.v4"),
		output:    workspace.path("output.v4"),
	}
	if err := sdkCreateLiveFile(files.firstSeen, iprangedb.ValueKindDirect, iprangedb.ValueTagFirstSeen()); err != nil {
		return nil, err
	}
	if err := sdkCreateLiveFile(files.lastSeen, iprangedb.ValueKindDirect, iprangedb.ValueTagLastSeen()); err != nil {
		return nil, err
	}
	membershipTag, err := sdkTag([]byte("membership"))
	if err != nil {
		return nil, err
	}
	if err := sdkCreateLiveFile(files.central, iprangedb.ValueKindMembership, membershipTag); err != nil {
		return nil, err
	}
	requests, err := sdkHistoryWindows(windows)
	if err != nil {
		return nil, err
	}
	cancellation := iprangedb.NewCancellationToken()
	if err := sdkSeedPriorRound(files, requests, size, cancellation); err != nil {
		return nil, err
	}
	directProvider, err := sdkSeededDirect("complete-direct-provider", size, 1)
	if err != nil {
		return nil, err
	}
	defer directProvider.cleanup()
	membershipProvider, err := sdkPopulated("complete-membership-provider", size, 2)
	if err != nil {
		return nil, err
	}
	defer membershipProvider.cleanup()
	shift := uint32(max(size/10, 1))
	source, err := newAddressSource(size, shift)
	if err != nil {
		return nil, err
	}
	var report sdkWorkflowReport
	operationErr, measured := operation(func() error {
		var err error
		report, err = sdkWorkflowExecute(files, directProvider, membershipProvider, requests, source, size, cancellation)
		return err
	})
	if operationErr != nil {
		return nil, operationErr
	}
	if err := validateOutput(files.current, false); err != nil {
		return nil, err
	}
	if err := validateOutput(files.previous, false); err != nil {
		return nil, err
	}
	if err := validateOutput(files.firstSeen, true); err != nil {
		return nil, err
	}
	if err := validateOutput(files.lastSeen, true); err != nil {
		return nil, err
	}
	if err := validateOutput(files.central, true); err != nil {
		return nil, err
	}
	return immutableResult(immutableResultSpec{
		Name:         "update-ipsets-workflow",
		Size:         size,
		Auxiliary:    windows,
		WorkUnits:    report.scanned,
		EmittedUnits: report.emitted,
	}, workspace, measured, files.output)
}

func sdkWorkflowExecute(
	files sdkWorkflowFiles,
	directProvider, membershipProvider *testDatabase,
	windows []iprangedb.HistoryWindow,
	source *addressSource,
	size int,
	cancellation *iprangedb.CancellationToken,
) (sdkWorkflowReport, error) {
	zero := sdkWorkflowReport{}
	downloadedTag, err := sdkTag([]byte("downloaded"))
	if err != nil {
		return zero, err
	}
	budget := sdkImmutableFeedBudgetFor(size)
	current, err := iprangedb.CreateImmutableFeedV4(
		files.current, downloadedTag, sdkFeedNameMust(0), nil, iprangedb.PolicyFailIfExists,
		source, &budget, cancellation,
	)
	if err != nil {
		return zero, err
	}
	if err := sdkRequirePublished(current.Publication); err != nil {
		return zero, err
	}
	currentReader, err := iprangedb.OpenImmutable(files.current)
	if err != nil {
		return zero, err
	}

	first, err := sdkRefreshFirstSeen(files.firstSeen, currentReader, size, 200, cancellation)
	if err != nil {
		return zero, err
	}
	last, err := sdkRefreshLastSeen(files.lastSeen, currentReader, size, 200, 0, cancellation)
	if err != nil {
		return zero, err
	}
	base, err := sdkApplyBaseFeed(files.central, currentReader, size, true, cancellation)
	if err != nil {
		return zero, err
	}

	lastReader, err := iprangedb.OpenLiveReader(files.lastSeen, cancellation)
	if err != nil {
		return zero, err
	}
	history, err := sdkProjectHistory(files.central, lastReader, windows, size, cancellation)
	if err != nil {
		return zero, err
	}

	centralReader, err := iprangedb.OpenLiveReader(files.central, cancellation)
	if err != nil {
		return zero, err
	}
	directReader, err := iprangedb.OpenLiveReader(directProvider.main, cancellation)
	if err != nil {
		return zero, err
	}
	membershipReader, err := iprangedb.OpenLiveReader(membershipProvider.main, cancellation)
	if err != nil {
		return zero, err
	}
	centralScope, err := sdkAllScope(centralReader, cancellation)
	if err != nil {
		return zero, err
	}
	providerScope, err := sdkAllScope(membershipReader, cancellation)
	if err != nil {
		return zero, err
	}

	aggregateSink := &sdkAggregateCounter{}
	aggregate, err := centralScope.Aggregate(
		iprangedb.MembershipAggregationTargetAgainstScope(string(sdkFeedNameMust(0))),
		aggregateSink.feedCardinalities,
		aggregateSink.feedOverlaps,
		cancellation,
	)
	if err != nil {
		return zero, err
	}

	resultLimit := (uint64(len(windows)) + 1) * 252
	directSink := &sdkDirectCounter{}
	direct, err := centralScope.JoinDirect(
		iprangedb.DirectJoinSourceLive(directReader),
		iprangedb.DirectJoinBudget{MaxResultCells: resultLimit},
		directSink.cells,
		cancellation,
	)
	if err != nil {
		return zero, err
	}

	membershipSink := &sdkMembershipCounter{}
	membership, err := centralScope.JoinMembership(providerScope, membershipSink.membershipCrossCells, membershipSink.uncoveredFeeds, cancellation)
	if err != nil {
		return zero, err
	}

	algebra, err := iprangedb.NewMembershipAlgebra([]*iprangedb.MembershipScope{centralScope, providerScope}, sdkAlgebraBudget(), cancellation)
	if err != nil {
		return zero, err
	}
	publishedTag, err := sdkTag([]byte("published"))
	if err != nil {
		return zero, err
	}
	algebraOutput, err := algebra.PublishSet(
		files.output,
		publishedTag,
		iprangedb.AlgebraSetUnion(iprangedb.AlgebraFeedSelectionAll()),
		iprangedb.AlgebraOutputModePreserveFeeds(),
		nil,
		iprangedb.PolicyFailIfExists,
		sdkAlgebraOutputBudget(size),
		cancellation,
	)
	if err != nil {
		return zero, err
	}
	if err := sdkRequirePublished(algebraOutput.Publication); err != nil {
		return zero, err
	}

	outputReader, err := iprangedb.OpenImmutable(files.output)
	if err != nil {
		return zero, err
	}
	finalRanges, err := sdkCountFeedRanges(outputReader, string(sdkFeedNameMust(0)))
	if err != nil {
		return zero, err
	}
	expectedFinalRanges := uint64(size + max(size/10, 1))
	if finalRanges != expectedFinalRanges {
		return zero, fmt.Errorf("complete workflow enumerated %d of %d globally merged base ranges", finalRanges, expectedFinalRanges)
	}
	_ = outputReader.Close()
	if err := closeLiveReader(membershipReader); err != nil {
		return zero, err
	}
	if err := closeLiveReader(directReader); err != nil {
		return zero, err
	}
	if err := closeLiveReader(centralReader); err != nil {
		return zero, err
	}
	if err := closeLiveReader(lastReader); err != nil {
		return zero, err
	}
	_ = currentReader.Close()

	scanned, sumErr := sdkCheckedSum([]uint64{
		current.Report.InputRecordCount,
		first.InputRecordCount,
		last.InputRecordCount,
		base.InputRecordCount,
		history.SourceRangeCount,
		aggregate.ScannedRangeCount,
		direct.MembershipRangeCount,
		direct.DirectRangesVisited,
		membership.LeftRangeCount,
		membership.RightRangeCount,
		algebraOutput.Report.SourceRangeCount,
		finalRanges,
	})
	if sumErr != nil {
		return zero, fmt.Errorf("complete-workflow counter overflow")
	}
	emitted, sumErr := sdkCheckedSum([]uint64{
		current.Report.NormalizedIntervalCount,
		history.AfterIntervalCount,
		aggregate.FeedResultCount,
		aggregate.PairResultCount,
		direct.ResultCellCount,
		membership.CrossResultCount,
		membership.UncoveredResultCount,
		algebraOutput.Report.OutputRangeCount,
	})
	if sumErr != nil {
		return zero, fmt.Errorf("complete-workflow counter overflow")
	}
	return sdkWorkflowReport{scanned: scanned, emitted: emitted}, nil
}

func sdkRefreshFirstSeen(
	path string,
	current *iprangedb.ImmutableReader,
	size int,
	refresh uint32,
	cancellation *iprangedb.CancellationToken,
) (iprangedb.WorkflowReport, error) {
	writer, err := iprangedb.OpenLiveWriter(path, toPageBudget(transactionBudget(size, 1)), cancellation)
	if err != nil {
		return iprangedb.WorkflowReport{}, err
	}
	workflow, err := writer.BeginFirstSeenRefresh(refresh, cancellation)
	if err != nil {
		return iprangedb.WorkflowReport{}, err
	}
	if err := sdkFeedRangeBatches(current, string(sdkFeedNameMust(0)), workflow.AddRangesV4); err != nil {
		return iprangedb.WorkflowReport{}, err
	}
	report, err := sdkCommitWorkflow(workflow.FinishInput())
	if err != nil {
		return iprangedb.WorkflowReport{}, err
	}
	if err := closeWriter(writer); err != nil {
		return iprangedb.WorkflowReport{}, err
	}
	return report, nil
}

func sdkRefreshLastSeen(
	path string,
	current *iprangedb.ImmutableReader,
	size int,
	refresh, cutoff uint32,
	cancellation *iprangedb.CancellationToken,
) (iprangedb.WorkflowReport, error) {
	writer, err := iprangedb.OpenLiveWriter(path, toPageBudget(transactionBudget(size, 1)), cancellation)
	if err != nil {
		return iprangedb.WorkflowReport{}, err
	}
	workflow, err := writer.BeginLastSeenRefresh(refresh, cutoff, cancellation)
	if err != nil {
		return iprangedb.WorkflowReport{}, err
	}
	if err := sdkFeedRangeBatches(current, string(sdkFeedNameMust(0)), workflow.AddRangesV4); err != nil {
		return iprangedb.WorkflowReport{}, err
	}
	report, err := sdkCommitWorkflow(workflow.FinishInput())
	if err != nil {
		return iprangedb.WorkflowReport{}, err
	}
	if err := closeWriter(writer); err != nil {
		return iprangedb.WorkflowReport{}, err
	}
	return report, nil
}

func sdkApplyBaseFeed(
	path string,
	current *iprangedb.ImmutableReader,
	size int,
	replace bool,
	cancellation *iprangedb.CancellationToken,
) (iprangedb.WorkflowReport, error) {
	writer, err := iprangedb.OpenLiveWriter(path, toPageBudget(transactionBudget(size, 1)), cancellation)
	if err != nil {
		return iprangedb.WorkflowReport{}, err
	}
	feed := sdkFeedNameMust(0)
	var report iprangedb.WorkflowReport
	if replace {
		workflow, err := writer.BeginReplaceFeed(feed, cancellation)
		if err != nil {
			return iprangedb.WorkflowReport{}, err
		}
		if err := sdkFeedRangeBatches(current, string(feed), workflow.AddRangesV4); err != nil {
			return iprangedb.WorkflowReport{}, err
		}
		report, err = sdkCommitWorkflow(workflow.FinishInput())
		if err != nil {
			return iprangedb.WorkflowReport{}, err
		}
	} else {
		workflow, err := writer.BeginCreateFeed(feed, cancellation)
		if err != nil {
			return iprangedb.WorkflowReport{}, err
		}
		if err := sdkFeedRangeBatches(current, string(feed), workflow.AddRangesV4); err != nil {
			return iprangedb.WorkflowReport{}, err
		}
		report, err = sdkCommitWorkflow(workflow.FinishInput())
		if err != nil {
			return iprangedb.WorkflowReport{}, err
		}
	}
	if err := closeWriter(writer); err != nil {
		return iprangedb.WorkflowReport{}, err
	}
	return report, nil
}

func sdkSeedPriorRound(
	files sdkWorkflowFiles,
	windows []iprangedb.HistoryWindow,
	size int,
	cancellation *iprangedb.CancellationToken,
) error {
	source, err := newAddressSource(size, 0)
	if err != nil {
		return err
	}
	downloadedTag, err := sdkTag([]byte("downloaded"))
	if err != nil {
		return err
	}
	budget := sdkImmutableFeedBudgetFor(size)
	fed, err := iprangedb.CreateImmutableFeedV4(
		files.previous, downloadedTag, sdkFeedNameMust(0), nil, iprangedb.PolicyFailIfExists,
		source, &budget, cancellation,
	)
	if err != nil {
		return err
	}
	if err := sdkRequirePublished(fed.Publication); err != nil {
		return err
	}
	previousReader, err := iprangedb.OpenImmutable(files.previous)
	if err != nil {
		return err
	}
	if _, err := sdkRefreshFirstSeen(files.firstSeen, previousReader, size, 100, cancellation); err != nil {
		return err
	}
	if _, err := sdkRefreshLastSeen(files.lastSeen, previousReader, size, 100, 0, cancellation); err != nil {
		return err
	}
	if _, err := sdkApplyBaseFeed(files.central, previousReader, size, false, cancellation); err != nil {
		return err
	}
	_ = previousReader.Close()

	lastReader, err := iprangedb.OpenLiveReader(files.lastSeen, cancellation)
	if err != nil {
		return err
	}
	if _, err := sdkProjectHistory(files.central, lastReader, windows, size, cancellation); err != nil {
		return err
	}
	return closeLiveReader(lastReader)
}

func sdkProjectHistory(
	path string,
	lastSeen *iprangedb.LiveReader,
	windows []iprangedb.HistoryWindow,
	size int,
	cancellation *iprangedb.CancellationToken,
) (iprangedb.HistoryProjectionReport, error) {
	writer, err := iprangedb.OpenLiveWriter(path, toPageBudget(transactionBudget(size, len(windows)+1)), cancellation)
	if err != nil {
		return iprangedb.HistoryProjectionReport{}, err
	}
	finished, err := writer.ProjectHistory(iprangedb.HistoryProjectionSource{
		Kind: iprangedb.HistoryProjectionSourceLive,
		Live: lastSeen,
	}, windows, cancellation)
	if err != nil {
		return iprangedb.HistoryProjectionReport{}, err
	}
	if !finished.IsChanged() {
		return iprangedb.HistoryProjectionReport{}, fmt.Errorf("complete history projection changed nothing: %+v", finished.Report())
	}
	report := finished.Report()
	if err := requireCommitted(finished.Commit()); err != nil {
		return iprangedb.HistoryProjectionReport{}, err
	}
	if err := closeWriter(writer); err != nil {
		return iprangedb.HistoryProjectionReport{}, err
	}
	return report, nil
}

func sdkCommitWorkflow(finished *iprangedb.FinishedWorkflow, err error) (iprangedb.WorkflowReport, error) {
	if err != nil {
		return iprangedb.WorkflowReport{}, err
	}
	if !finished.IsChanged() {
		return iprangedb.WorkflowReport{}, fmt.Errorf("complete workflow unexpectedly changed nothing: %+v", finished.Report())
	}
	report := finished.Report()
	if err := requireCommitted(finished.Commit()); err != nil {
		return iprangedb.WorkflowReport{}, err
	}
	return report, nil
}

func sdkCreateLiveFile(path string, kind iprangedb.ValueKind, tag iprangedb.ValueTag) error {
	_, err := iprangedb.CreateLive(path, iprangedb.AddressFamilyIPv4, kind, iprangedb.StructureKindNone, tag, 1, iprangedb.NewCancellationToken())
	return err
}

func sdkCheckedSum(values []uint64) (uint64, error) {
	var total uint64
	for _, value := range values {
		next := total + value
		if next < total {
			return 0, fmt.Errorf("complete-workflow counter overflow")
		}
		total = next
	}
	return total, nil
}

// ---------------------------------------------------------------------------
// database builders (Rust scenarios/direct.rs seeded_direct* and
// scenarios/membership.rs populated*)
// ---------------------------------------------------------------------------

// sdkSeededDirect mirrors Rust seeds::seeded_direct: a generic direct
// database (value tag "timestamp") populated by one unordered direct
// replacement of dispersed singleton ranges.
func sdkSeededDirect(label string, size int, readerCapacity uint32) (*testDatabase, error) {
	return sdkSeededDirectWithTag(label, size, readerCapacity, sdkDirectTagMust())
}

// sdkSeededDirectWithTag mirrors Rust seeds::seeded_direct_with_tag.
func sdkSeededDirectWithTag(label string, size int, readerCapacity uint32, tag iprangedb.ValueTag) (*testDatabase, error) {
	database, err := newTestDatabase(label)
	if err != nil {
		return nil, err
	}
	if _, err := iprangedb.CreateLive(database.main, iprangedb.AddressFamilyIPv4, iprangedb.ValueKindDirect, iprangedb.StructureKindNone, tag, readerCapacity, iprangedb.NewCancellationToken()); err != nil {
		return nil, err
	}
	if err := sdkApplyDirect(database, size); err != nil {
		return nil, err
	}
	return database, nil
}

func sdkApplyDirect(database *testDatabase, size int) error {
	source, err := newDirectSource(size)
	if err != nil {
		return err
	}
	cancellation := iprangedb.NewCancellationToken()
	writer, err := iprangedb.OpenLiveWriter(database.main, toPageBudget(transactionBudget(size, 1)), cancellation)
	if err != nil {
		return err
	}
	workflow, err := writer.BeginDirectReplacement(cancellation)
	if err != nil {
		return err
	}
	for {
		batch, more := source.nextBatch()
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
		return fmt.Errorf("replacement unexpectedly changed nothing: %+v", finished.Report())
	}
	if err := requireCommitted(finished.Commit()); err != nil {
		return err
	}
	return closeWriter(writer)
}

// sdkPopulated mirrors Rust membership::populated: one membership
// database whose single membership bitmap carries every feed across all
// ranges.
func sdkPopulated(label string, ranges, feeds int) (*testDatabase, error) {
	database, err := newTestDatabase(label)
	if err != nil {
		return nil, err
	}
	if err := sdkCreateMembership(database); err != nil {
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
	membership, err := transaction.EmptyMembership()
	if err != nil {
		return nil, err
	}
	for index := 0; index < feeds; index++ {
		feed, err := transaction.EnsureFeed(sdkFeedNameMust(index))
		if err != nil {
			return nil, err
		}
		membership, err = transaction.AddFeed(membership, feed)
		if err != nil {
			return nil, err
		}
	}
	for index := 0; index < ranges; index++ {
		start := uint32(index) * 4
		if _, err := transaction.ApplyV4(iprangedb.IPv4(start), iprangedb.IPv4(start+1), membership, iprangedb.MembershipReplace); err != nil {
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

// sdkPopulatedRotating mirrors Rust membership::populated_rotating: one
// membership database whose per-address bitmaps rotate through sliding
// windows of width feeds.
func sdkPopulatedRotating(label string, ranges, feeds, width int) (*testDatabase, error) {
	feeds = max(feeds, 1)
	width = min(max(width, 1), feeds)
	database, err := newTestDatabase(label)
	if err != nil {
		return nil, err
	}
	if err := sdkCreateMembership(database); err != nil {
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
	for index := 0; index < feeds; index++ {
		feedRefs[index], err = transaction.EnsureFeed(sdkFeedNameMust(index))
		if err != nil {
			return nil, err
		}
	}
	memberships := make([]iprangedb.MembershipRef, feeds)
	for start := 0; start < feeds; start++ {
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

// sdkCreateMembership mirrors Rust sdk::create_membership: an empty
// membership live database with the "membership" value tag.
func sdkCreateMembership(database *testDatabase) error {
	tag, err := sdkTag([]byte("membership"))
	if err != nil {
		return err
	}
	_, err = iprangedb.CreateLive(database.main, iprangedb.AddressFamilyIPv4, iprangedb.ValueKindMembership, iprangedb.StructureKindNone, tag, 1, iprangedb.NewCancellationToken())
	return err
}

// sdkAllScope mirrors Rust sdk::all_scope.
func sdkAllScope(reader *iprangedb.LiveReader, cancellation *iprangedb.CancellationToken) (*iprangedb.MembershipScope, error) {
	query, err := reader.MembershipQuery()
	if err != nil {
		return nil, err
	}
	return query.AllFeeds(sdkQueryBudget(), cancellation)
}

// ---------------------------------------------------------------------------
// immutable single-feed publication budget (Rust sdk::immutable_budget)
// ---------------------------------------------------------------------------

// sdkImmutableFeedBudgetFor mirrors Rust sdk::immutable_budget.
func sdkImmutableFeedBudgetFor(size int) iprangedb.ImmutableFeedBudget {
	pages := uint64((size+7)/8) + 20_000
	return iprangedb.ImmutableFeedBudget{
		MaxHeapBytes:      sdkQueryHeap,
		MaxOutputPages:    pages,
		MaxWorkspacePages: pages,
		MaxOpenFiles:      3,
	}
}

// ---------------------------------------------------------------------------
// feed-range cursor drain (Rust named_feed_source_v4)
// ---------------------------------------------------------------------------

// sdkFeedRangeBatches drains one named feed's forward IPv4 range cursor
// into bounded batches of the size the Go feed workflows consume (the
// Rust FeedRangeSourceV4 batch contract).
func sdkFeedRangeBatches(reader *iprangedb.ImmutableReader, name string, sink func([]iprangedb.AddressRange4) error) error {
	cursor, err := reader.FeedRangeCursorV4(name, iprangedb.RangeDirectionForward)
	if err != nil {
		return err
	}
	var batchArray [batchCapacity]iprangedb.AddressRange4
	batch := batchArray[:0]
	for {
		rng, ok, err := cursor.NextRange()
		if err != nil {
			return err
		}
		if !ok {
			break
		}
		batch = append(batch, rng)
		if len(batch) == batchCapacity {
			if err := sink(batch); err != nil {
				return err
			}
			batch = batch[:0]
		}
	}
	if len(batch) != 0 {
		return sink(batch)
	}
	return nil
}

// sdkCountFeedRanges counts every forward range of one named feed (the
// workflow's final output enumeration).
func sdkCountFeedRanges(reader *iprangedb.ImmutableReader, name string) (uint64, error) {
	cursor, err := reader.FeedRangeCursorV4(name, iprangedb.RangeDirectionForward)
	if err != nil {
		return 0, err
	}
	var count uint64
	for {
		_, ok, err := cursor.NextRange()
		if err != nil {
			return 0, err
		}
		if !ok {
			break
		}
		count++
	}
	return count, nil
}

// ---------------------------------------------------------------------------
// small shared helpers (Rust sdk.rs bottom half)
// ---------------------------------------------------------------------------

// sdkAggregateCounter mirrors Rust AggregateCounter (MembershipAggregateSink).
type sdkAggregateCounter struct {
	feeds uint64
	pairs uint64
}

func (c *sdkAggregateCounter) feedCardinalities(batch []iprangedb.FeedCardinality) error {
	_ = batch
	c.feeds += uint64(len(batch))
	return nil
}

func (c *sdkAggregateCounter) feedOverlaps(batch []iprangedb.FeedOverlap) error {
	_ = batch
	c.pairs += uint64(len(batch))
	return nil
}

// sdkDirectCounter mirrors Rust DirectCounter (DirectJoinSink).
type sdkDirectCounter struct {
	count uint64
}

func (c *sdkDirectCounter) cells(batch []iprangedb.DirectJoinCell) error {
	_ = batch
	c.count += uint64(len(batch))
	return nil
}

// sdkMembershipCounter mirrors Rust MembershipCounter (MembershipJoinSink).
type sdkMembershipCounter struct {
	cross     uint64
	uncovered uint64
}

func (c *sdkMembershipCounter) membershipCrossCells(batch []iprangedb.MembershipCrossCell) error {
	_ = batch
	c.cross += uint64(len(batch))
	return nil
}

func (c *sdkMembershipCounter) uncoveredFeeds(batch []iprangedb.UncoveredFeed) error {
	_ = batch
	c.uncovered += uint64(len(batch))
	return nil
}

// sdkHistoryWindows mirrors Rust sdk::history_windows: windows with
// cutoffs spaced by 251 over count+1 slots.
func sdkHistoryWindows(count int) ([]iprangedb.HistoryWindow, error) {
	windows := make([]iprangedb.HistoryWindow, 0, count)
	for index := 0; index < count; index++ {
		cutoff := uint32((uint64(index+1) * 251) / uint64(count+1))
		name, err := sdkFeedNameOf("history-%06d", index)
		if err != nil {
			return nil, err
		}
		windows = append(windows, iprangedb.HistoryWindow{FeedName: string(name), Cutoff: cutoff})
	}
	return windows, nil
}

// sdkAlgebraOutputBudget mirrors Rust sdk::algebra_output_budget.
func sdkAlgebraOutputBudget(size int) iprangedb.AlgebraOutputBudget {
	return iprangedb.AlgebraOutputBudget{
		MaxOutputPages: uint64((size+7)/8) + 20_000,
		MaxOpenFiles:   3,
	}
}

// sdkQueryBudget mirrors Rust sdk::query_budget.
func sdkQueryBudget() iprangedb.MembershipQueryBudget {
	return iprangedb.MembershipQueryBudget{MaxHeapBytes: sdkQueryHeap}
}

// sdkAlgebraBudget mirrors Rust sdk::algebra_budget.
func sdkAlgebraBudget() iprangedb.MembershipAlgebraBudget {
	return iprangedb.MembershipAlgebraBudget{MaxHeapBytes: sdkQueryHeap, MaxSources: 2}
}

// sdkFeedName mirrors Rust sdk::feed_name("feed-{index:06}").
func sdkFeedName(index int) (iprangedb.FeedName, error) {
	return sdkFeedNameOf("feed-%06d", index)
}

// sdkFeedNameOf builds one validated feed name through the public
// validator (Rust FeedName::new).
func sdkFeedNameOf(format string, index int) (iprangedb.FeedName, error) {
	return iprangedb.NewFeedName(fmt.Sprintf(format, index))
}

// sdkFeedNameMust is the benchmark-internal unchecked feed-name helper
// for names proven valid at construction time.
func sdkFeedNameMust(index int) iprangedb.FeedName {
	name, err := sdkFeedName(index)
	if err != nil {
		panic(err)
	}
	return name
}

// sdkTag mirrors Rust sdk::tag (ValueTag::new).
func sdkTag(value []byte) (iprangedb.ValueTag, error) {
	tag, err := iprangedb.NewValueTag(value)
	if err != nil {
		return iprangedb.ValueTag{}, fmt.Errorf("invalid benchmark value tag")
	}
	return tag, nil
}

// sdkDirectTagMust is the generic "timestamp" tag of the seeded direct
// databases (Rust direct::direct_tag).
func sdkDirectTagMust() iprangedb.ValueTag {
	tag, err := sdkTag([]byte("timestamp"))
	if err != nil {
		panic(err)
	}
	return tag
}

// sdkRequirePublished mirrors Rust sdk::require_published: the
// publication result must carry the Published status.
func sdkRequirePublished(publication iprangedb.PublicationResult) error {
	if publication.Publication != iprangedb.PublicationPublished {
		return fmt.Errorf("publication did not complete: %v", publication.Publication)
	}
	return nil
}

// sdkRequireCount mirrors Rust scenarios::require_count.
func sdkRequireCount(label string, observed, expected uint64, noun string) error {
	if observed != expected {
		return fmt.Errorf("%s returned %d of %d %s", label, observed, expected, noun)
	}
	return nil
}
