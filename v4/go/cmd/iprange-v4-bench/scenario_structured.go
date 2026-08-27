// Port of the Rust structured benchmark group (Rust
// benches/update_ipsets/scenarios/structured.rs): build, intern,
// assign, and commit workloads over a structured
// network_enrichment_v1 database, the live/immutable scalar and threat
// random lookups, the live/immutable scalar cursor scans, and the
// immutable separate-enrichment lookup that combines direct ASN, direct
// geo, and membership threat databases. All helpers are prefixed
// "structured" because every scenario group ports as its own package-
// main file; the registry names match the Rust scenario names exactly.
//
// The Go SDK exposes network-enrichment and membership lookups through
// a lifetime Pin rather than directly on the reader (Rust
// LiveReader::lookup_network_enrichment_v1_v4), so each reader-based
// workload pins once before the measured operation and closes the pin
// before the reader close. Cursor scans close every cursor before the
// reader close, because a live cursor holds one reader pin (Rust's
// cursors are dropped by the borrow checker instead).
package main

import (
	"fmt"
	"math/bits"

	iprangedb "github.com/firehol/iprange/v4/go"
)

// structuredProfileLimit mirrors the Rust PROFILE_LIMIT: the profile
// table never interns more than 65,536 distinct enrichment values.
const structuredProfileLimit = 65_536

// structuredLookupWorkUnits mirrors the Rust LOOKUP_WORK_UNITS floor
// for the random lookup repetition count.
const structuredLookupWorkUnits = 10_000_000

func init() {
	registerScenario("structured-build-random", structuredBuildRandom)
	registerScenario("structured-intern", structuredInternProfiles)
	registerScenario("structured-assign-random", structuredAssignRandom)
	registerScenario("structured-commit", structuredCommit)
	registerScenario("live-structured-scalar-random-lookup", structuredLiveScalarRandomLookup)
	registerScenario("immutable-structured-scalar-random-lookup", structuredImmutableScalarRandomLookup)
	registerScenario("live-structured-threat-random-lookup", structuredLiveThreatRandomLookup)
	registerScenario("immutable-structured-threat-random-lookup", structuredImmutableThreatRandomLookup)
	registerScenario("live-structured-scalar-scan", structuredLiveScalarScan)
	registerScenario("immutable-structured-scalar-scan", structuredImmutableScalarScan)
	registerScenario("immutable-separate-enrichment-random-lookup", structuredSeparateRandomLookup)
}

// structuredBuildRandom mirrors Rust structured::build_random: build the
// whole database (profiles, random-order assignments, commit) inside the
// measured operation.
func structuredBuildRandom(size, feeds int) (*scenarioResult, error) {
	feeds = structuredMaxFeed(feeds)
	database, err := structuredCreateDatabase("structured-build-random")
	if err != nil {
		return nil, err
	}
	defer database.cleanup()
	points, err := randomPoints(size)
	if err != nil {
		return nil, err
	}
	err, measured := operation(func() error {
		return structuredPopulate(database, size, feeds, points)
	})
	if err != nil {
		return nil, err
	}
	return result("structured-build-random", size, feeds, uint64(size), database, measured, database.main)
}

// structuredInternProfiles mirrors Rust structured::intern_profiles:
// one open transaction, interning the enrichment profile list with an
// alternating optional threat membership, then abort.
func structuredInternProfiles(size, feeds int) (*scenarioResult, error) {
	feeds = structuredMaxFeed(feeds)
	database, err := structuredCreateDatabase("structured-intern")
	if err != nil {
		return nil, err
	}
	defer database.cleanup()
	writer, err := iprangedb.OpenLiveWriter(database.main, toPageBudget(transactionBudget(size, feeds)), nil)
	if err != nil {
		return nil, err
	}
	transaction, err := writer.BeginStructuredTransaction(nil)
	if err != nil {
		return nil, err
	}
	threat, err := structuredPrepareThreatMembership(transaction, feeds)
	if err != nil {
		return nil, err
	}
	var observed uint64
	err, measured := operation(func() error {
		var count uint64
		for index := 0; index < size; index++ {
			var membership iprangedb.MembershipRef
			if index%2 == 0 {
				membership = threat
			}
			reference, err := transaction.InternNetworkEnrichmentV1(structuredProfile(index), membership)
			if err != nil {
				return err
			}
			_ = reference
			count++
		}
		observed = count
		return nil
	})
	if err != nil {
		return nil, err
	}
	if err := structuredRequireCount("structure interning", observed, uint64(size), "profiles"); err != nil {
		return nil, err
	}
	if err := transaction.Abort(); err != nil {
		return nil, err
	}
	if err := closeWriter(writer); err != nil {
		return nil, err
	}
	return result("structured-intern", size, feeds, uint64(size), database, measured, database.main)
}

// structuredAssignRandom mirrors Rust structured::assign_random: intern
// the profile table, then assign profiles to the random order of
// dispersed points, committing afterwards.
func structuredAssignRandom(size, feeds int) (*scenarioResult, error) {
	feeds = structuredMaxFeed(feeds)
	database, err := structuredCreateDatabase("structured-assign-random")
	if err != nil {
		return nil, err
	}
	defer database.cleanup()
	points, err := randomPoints(size)
	if err != nil {
		return nil, err
	}
	writer, err := iprangedb.OpenLiveWriter(database.main, toPageBudget(transactionBudget(size, feeds)), nil)
	if err != nil {
		return nil, err
	}
	transaction, err := writer.BeginStructuredTransaction(nil)
	if err != nil {
		return nil, err
	}
	profiles, err := structuredPrepareProfiles(transaction, size, feeds)
	if err != nil {
		return nil, err
	}
	var observed uint64
	err, measured := operation(func() error {
		var count uint64
		for _, point := range points {
			index := int(point / 4)
			if err := structuredAssign(transaction, index, profiles[index%len(profiles)]); err != nil {
				return err
			}
			count++
		}
		observed = count
		return nil
	})
	if err != nil {
		return nil, err
	}
	if err := structuredRequireCount("structured range assignment", observed, uint64(size), "ranges"); err != nil {
		return nil, err
	}
	if err := requireCommitted(transaction.Commit()); err != nil {
		return nil, err
	}
	if err := closeWriter(writer); err != nil {
		return nil, err
	}
	return result("structured-assign-random", size, feeds, uint64(size), database, measured, database.main)
}

// structuredCommit mirrors Rust structured::commit: build the full
// workload, then measure only the commit call.
func structuredCommit(size, feeds int) (*scenarioResult, error) {
	feeds = structuredMaxFeed(feeds)
	database, err := structuredCreateDatabase("structured-commit")
	if err != nil {
		return nil, err
	}
	defer database.cleanup()
	writer, err := iprangedb.OpenLiveWriter(database.main, toPageBudget(transactionBudget(size, feeds)), nil)
	if err != nil {
		return nil, err
	}
	transaction, err := writer.BeginStructuredTransaction(nil)
	if err != nil {
		return nil, err
	}
	profiles, err := structuredPrepareProfiles(transaction, size, feeds)
	if err != nil {
		return nil, err
	}
	for index := 0; index < size; index++ {
		if err := structuredAssign(transaction, index, profiles[index%len(profiles)]); err != nil {
			return nil, err
		}
	}
	var commitResult iprangedb.CommitResult
	err, measured := operation(func() error {
		var commitErr error
		commitResult, commitErr = transaction.Commit()
		return commitErr
	})
	if err != nil {
		return nil, err
	}
	if err := requireCommitted(commitResult, nil); err != nil {
		return nil, err
	}
	if err := closeWriter(writer); err != nil {
		return nil, err
	}
	return result("structured-commit", size, feeds, uint64(size), database, measured, database.main)
}

// structuredLiveScalarRandomLookup mirrors Rust
// structured::live_scalar_random_lookup: count nonzero ASN values over
// random points through a live reader pin.
func structuredLiveScalarRandomLookup(size, feeds int) (*scenarioResult, error) {
	feeds = structuredMaxFeed(feeds)
	database, err := structuredPopulatedDatabase("live-structured-scalar-random-lookup", size, feeds)
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
	repetitions, workUnits, err := structuredReaderWork(size)
	if err != nil {
		return nil, err
	}
	var observed uint64
	err, measured := operation(func() error {
		var lookupErr error
		observed, lookupErr = structuredCountScalarPoints(points, repetitions, func(address iprangedb.IPv4) (iprangedb.NetworkEnrichmentV1View, bool, error) {
			return pin.LookupNetworkEnrichmentV1V4(address)
		})
		return lookupErr
	})
	if err != nil {
		return nil, err
	}
	if err := structuredRequireCount("live structured scalar lookup", observed, workUnits, "addresses"); err != nil {
		return nil, err
	}
	if err := pin.Close(); err != nil {
		return nil, err
	}
	if err := closeLiveReader(reader); err != nil {
		return nil, err
	}
	return result("live-structured-scalar-random-lookup", size, feeds, workUnits, database, measured, database.main)
}

// structuredImmutableScalarRandomLookup mirrors Rust
// structured::immutable_scalar_random_lookup over the snapshot.
func structuredImmutableScalarRandomLookup(size, feeds int) (*scenarioResult, error) {
	feeds = structuredMaxFeed(feeds)
	database, err := structuredPopulatedDatabase("immutable-structured-scalar-random-lookup", size, feeds)
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
	repetitions, workUnits, err := structuredReaderWork(size)
	if err != nil {
		return nil, err
	}
	var observed uint64
	err, measured := operation(func() error {
		var lookupErr error
		observed, lookupErr = structuredCountScalarPoints(points, repetitions, func(address iprangedb.IPv4) (iprangedb.NetworkEnrichmentV1View, bool, error) {
			return pin.LookupNetworkEnrichmentV1V4(address)
		})
		return lookupErr
	})
	if err != nil {
		return nil, err
	}
	if err := structuredRequireCount("immutable structured scalar lookup", observed, workUnits, "addresses"); err != nil {
		return nil, err
	}
	if err := pin.Close(); err != nil {
		return nil, err
	}
	_ = reader.Close()
	return result("immutable-structured-scalar-random-lookup", size, feeds, workUnits, database, measured, snapshot)
}

// structuredLiveThreatRandomLookup mirrors Rust
// structured::live_threat_random_lookup: count random points whose
// enrichment value carries the target threat feed.
func structuredLiveThreatRandomLookup(size, feeds int) (*scenarioResult, error) {
	feeds = structuredMaxFeed(feeds)
	database, err := structuredPopulatedDatabase("live-structured-threat-random-lookup", size, feeds)
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
	target, err := structuredTargetFeedIndex(reader, feeds)
	if err != nil {
		return nil, err
	}
	repetitions, workUnits, err := structuredReaderWork(size)
	if err != nil {
		return nil, err
	}
	expected, err := structuredExpectedThreatHits(size, repetitions)
	if err != nil {
		return nil, err
	}
	var observed uint64
	err, measured := operation(func() error {
		var lookupErr error
		observed, lookupErr = structuredCountThreatPoints(points, repetitions, target, func(address iprangedb.IPv4) (iprangedb.NetworkEnrichmentV1View, bool, error) {
			return pin.LookupNetworkEnrichmentV1V4(address)
		})
		return lookupErr
	})
	if err != nil {
		return nil, err
	}
	if err := structuredRequireCount("live structured threat lookup", observed, expected, "matches"); err != nil {
		return nil, err
	}
	if err := pin.Close(); err != nil {
		return nil, err
	}
	if err := closeLiveReader(reader); err != nil {
		return nil, err
	}
	return result("live-structured-threat-random-lookup", size, feeds, workUnits, database, measured, database.main)
}

// structuredImmutableThreatRandomLookup mirrors Rust
// structured::immutable_threat_random_lookup over the snapshot.
func structuredImmutableThreatRandomLookup(size, feeds int) (*scenarioResult, error) {
	feeds = structuredMaxFeed(feeds)
	database, err := structuredPopulatedDatabase("immutable-structured-threat-random-lookup", size, feeds)
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
	target, err := structuredTargetFeedIndex(reader, feeds)
	if err != nil {
		return nil, err
	}
	repetitions, workUnits, err := structuredReaderWork(size)
	if err != nil {
		return nil, err
	}
	expected, err := structuredExpectedThreatHits(size, repetitions)
	if err != nil {
		return nil, err
	}
	var observed uint64
	err, measured := operation(func() error {
		var lookupErr error
		observed, lookupErr = structuredCountThreatPoints(points, repetitions, target, func(address iprangedb.IPv4) (iprangedb.NetworkEnrichmentV1View, bool, error) {
			return pin.LookupNetworkEnrichmentV1V4(address)
		})
		return lookupErr
	})
	if err != nil {
		return nil, err
	}
	if err := structuredRequireCount("immutable structured threat lookup", observed, expected, "matches"); err != nil {
		return nil, err
	}
	if err := pin.Close(); err != nil {
		return nil, err
	}
	_ = reader.Close()
	return result("immutable-structured-threat-random-lookup", size, feeds, workUnits, database, measured, snapshot)
}

// structuredLiveScalarScan mirrors Rust structured::live_scalar_scan:
// repeat forward enrichment-cursor sweeps, adding every ASN into a
// wrapping checksum. Each cursor is closed explicitly because a Go
// cursor holds one reader pin (the Rust borrow is dropped instead).
func structuredLiveScalarScan(size, feeds int) (*scenarioResult, error) {
	feeds = structuredMaxFeed(feeds)
	database, err := structuredPopulatedDatabase("live-structured-scalar-scan", size, feeds)
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
	var observed uint64
	err, measured := operation(func() error {
		var records uint64
		var checksum uint64
		for range repetitions {
			cursor, err := reader.NetworkEnrichmentV1CursorV4(iprangedb.RangeDirectionForward)
			if err != nil {
				return err
			}
			sweepErr := func() error {
				defer cursor.Close()
				for {
					record, ok, err := cursor.NextRange()
					if err != nil {
						return err
					}
					if !ok {
						return nil
					}
					value, err := record.Value.Value()
					if err != nil {
						return err
					}
					checksum += uint64(value.ASN)
					records++
				}
			}()
			if sweepErr != nil {
				return sweepErr
			}
		}
		_ = checksum
		observed = records
		return nil
	})
	if err != nil {
		return nil, err
	}
	if err := structuredRequireCount("live structured scalar scan", observed, workUnits, "ranges"); err != nil {
		return nil, err
	}
	if err := closeLiveReader(reader); err != nil {
		return nil, err
	}
	return result("live-structured-scalar-scan", size, feeds, workUnits, database, measured, database.main)
}

// structuredImmutableScalarScan mirrors Rust structured::immutable_scalar_scan
// over the snapshot.
func structuredImmutableScalarScan(size, feeds int) (*scenarioResult, error) {
	feeds = structuredMaxFeed(feeds)
	database, err := structuredPopulatedDatabase("immutable-structured-scalar-scan", size, feeds)
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
	var observed uint64
	err, measured := operation(func() error {
		var records uint64
		var checksum uint64
		for range repetitions {
			cursor, err := reader.NetworkEnrichmentV1CursorV4(iprangedb.RangeDirectionForward)
			if err != nil {
				return err
			}
			sweepErr := func() error {
				defer cursor.Close()
				for {
					record, ok, err := cursor.NextRange()
					if err != nil {
						return err
					}
					if !ok {
						return nil
					}
					value, err := record.Value.Value()
					if err != nil {
						return err
					}
					checksum += uint64(value.ASN)
					records++
				}
			}()
			if sweepErr != nil {
				return sweepErr
			}
		}
		_ = checksum
		observed = records
		return nil
	})
	if err != nil {
		return nil, err
	}
	if err := structuredRequireCount("immutable structured scalar scan", observed, workUnits, "ranges"); err != nil {
		return nil, err
	}
	_ = reader.Close()
	return result("immutable-structured-scalar-scan", size, feeds, workUnits, database, measured, snapshot)
}

// structuredSeparateRandomLookup mirrors Rust
// structured::immutable_separate_random_lookup: three immutable
// databases (direct ASN, direct geo, membership threat) answered per
// random point through separate readers, with the combined wrapping
// checksum asserted nonzero.
func structuredSeparateRandomLookup(size, feeds int) (*scenarioResult, error) {
	feeds = structuredMaxFeed(feeds)
	asn, err := structuredSeededDirect("separate-enrichment-asn", size, 1)
	if err != nil {
		return nil, err
	}
	defer asn.cleanup()
	geo, err := structuredSeededDirect("separate-enrichment-geo", size, 1)
	if err != nil {
		return nil, err
	}
	defer geo.cleanup()
	threat, err := structuredPopulatedThreat("separate-enrichment-threat", size, feeds)
	if err != nil {
		return nil, err
	}
	defer threat.cleanup()

	asnSnapshot, err := immutableSnapshot(asn, size)
	if err != nil {
		return nil, err
	}
	geoSnapshot, err := immutableSnapshot(geo, size)
	if err != nil {
		return nil, err
	}
	threatSnapshot, err := immutableSnapshot(threat, size)
	if err != nil {
		return nil, err
	}
	points, err := randomPoints(size)
	if err != nil {
		return nil, err
	}
	asnReader, err := iprangedb.OpenImmutable(asnSnapshot)
	if err != nil {
		return nil, err
	}
	geoReader, err := iprangedb.OpenImmutable(geoSnapshot)
	if err != nil {
		return nil, err
	}
	threatReader, err := iprangedb.OpenImmutable(threatSnapshot)
	if err != nil {
		return nil, err
	}
	// The membership bitmap lookup is a Pin operation in the Go SDK
	// (Rust ImmutableReader::lookup_membership_v4).
	threatPin, err := threatReader.Pin()
	if err != nil {
		return nil, err
	}
	target, err := structuredTargetFeedIndex(threatReader, feeds)
	if err != nil {
		return nil, err
	}
	repetitions, workUnits, err := structuredReaderWork(size)
	if err != nil {
		return nil, err
	}
	var checksum uint64
	err, measured := operation(func() error {
		var sum uint64
		for range repetitions {
			for _, address := range points {
				ip := iprangedb.IPv4(address)
				asnValue, found, err := asnReader.LookupDirectV4(ip)
				if err != nil {
					return err
				}
				if !found {
					return fmt.Errorf("separate ASN lookup missed an address")
				}
				geoValue, found, err := geoReader.LookupDirectV4(ip)
				if err != nil {
					return err
				}
				if !found {
					return fmt.Errorf("separate Geo lookup missed an address")
				}
				membership, found, err := threatPin.LookupMembershipV4(ip)
				if err != nil {
					return err
				}
				matched := false
				if found {
					matched, err = membership.ContainsIndex(target)
					if err != nil {
						return err
					}
				}
				var matchedValue uint64
				if matched {
					matchedValue = 1
				}
				sum += uint64(asnValue)
				sum += bits.RotateLeft64(uint64(geoValue), 17)
				sum += matchedValue
			}
		}
		checksum = sum
		return nil
	})
	if err != nil {
		return nil, err
	}
	if checksum == 0 {
		return nil, fmt.Errorf("separate enrichment lookup produced an empty checksum")
	}
	if err := threatPin.Close(); err != nil {
		return nil, err
	}
	_ = asnReader.Close()
	_ = geoReader.Close()
	_ = threatReader.Close()

	for _, snapshot := range []string{asnSnapshot, geoSnapshot, threatSnapshot} {
		if err := validateOutput(snapshot, false); err != nil {
			return nil, err
		}
	}
	file, err := structuredAggregateFileSize([]string{asnSnapshot, geoSnapshot, threatSnapshot})
	if err != nil {
		return nil, err
	}
	asnArtifacts, err := asn.privateArtifacts()
	if err != nil {
		return nil, err
	}
	geoArtifacts, err := geo.privateArtifacts()
	if err != nil {
		return nil, err
	}
	threatArtifacts, err := threat.privateArtifacts()
	if err != nil {
		return nil, err
	}
	if asnArtifacts > ^uint64(0)-geoArtifacts {
		return nil, fmt.Errorf("private artifact count overflow")
	}
	privateArtifacts := asnArtifacts + geoArtifacts
	if privateArtifacts > ^uint64(0)-threatArtifacts {
		return nil, fmt.Errorf("private artifact count overflow")
	}
	privateArtifacts += threatArtifacts
	if privateArtifacts != 0 {
		return nil, fmt.Errorf("separate enrichment lookup left %d private artifacts", privateArtifacts)
	}
	return &scenarioResult{
		Name:             "immutable-separate-enrichment-random-lookup",
		Size:             size,
		Auxiliary:        feeds,
		WorkUnits:        workUnits,
		RangeRecords:     uint64(size)*2 + uint64((size+1)/2),
		Feeds:            uint64(feeds),
		Measurement:      measured,
		File:             file,
		PrivateArtifacts: privateArtifacts,
	}, nil
}

// structuredPopulatedDatabase mirrors Rust structured::populated_structured.
func structuredPopulatedDatabase(label string, size, feeds int) (*testDatabase, error) {
	database, err := structuredCreateDatabase(label)
	if err != nil {
		return nil, err
	}
	if err := structuredPopulate(database, size, feeds, nil); err != nil {
		database.cleanup()
		return nil, err
	}
	return database, nil
}

// structuredCreateDatabase mirrors Rust structured::create_structured:
// an IPv4 structured network_enrichment_v1 live database carrying the
// canonical "enrichment" value tag.
func structuredCreateDatabase(label string) (*testDatabase, error) {
	database, err := newTestDatabase(label)
	if err != nil {
		return nil, err
	}
	tag, err := iprangedb.NewValueTag([]byte("enrichment"))
	if err != nil {
		database.cleanup()
		return nil, fmt.Errorf("invalid enrichment tag: %v", err)
	}
	if _, err := iprangedb.CreateLive(database.main, iprangedb.AddressFamilyIPv4, iprangedb.ValueKindStructured, iprangedb.StructureKindNetworkEnrichmentV1, tag, 1, nil); err != nil {
		database.cleanup()
		return nil, err
	}
	return database, nil
}

// structuredPopulate mirrors Rust structured::populate_structured: one
// structured transaction interning the profile table and assigning it
// either in the random point order or in index order, then commit and
// close the writer.
func structuredPopulate(database *testDatabase, size, feeds int, randomOrder []uint32) error {
	writer, err := iprangedb.OpenLiveWriter(database.main, toPageBudget(transactionBudget(size, feeds)), nil)
	if err != nil {
		return err
	}
	transaction, err := writer.BeginStructuredTransaction(nil)
	if err != nil {
		return err
	}
	profiles, err := structuredPrepareProfiles(transaction, size, feeds)
	if err != nil {
		return err
	}
	if randomOrder != nil {
		for _, point := range randomOrder {
			index := int(point / 4)
			if err := structuredAssign(transaction, index, profiles[index%len(profiles)]); err != nil {
				return err
			}
		}
	} else {
		for index := 0; index < size; index++ {
			if err := structuredAssign(transaction, index, profiles[index%len(profiles)]); err != nil {
				return err
			}
		}
	}
	if err := requireCommitted(transaction.Commit()); err != nil {
		return err
	}
	return closeWriter(writer)
}

// structuredPrepareProfiles mirrors Rust structured::prepare_profiles:
// one shared threat membership plus up to PROFILE_LIMIT interned
// enrichment profiles alternating membership presence.
func structuredPrepareProfiles(transaction *iprangedb.StructuredTransaction, size, feeds int) ([]iprangedb.StructureRef, error) {
	threat, err := structuredPrepareThreatMembership(transaction, feeds)
	if err != nil {
		return nil, err
	}
	profileCount := size
	if profileCount > structuredProfileLimit {
		profileCount = structuredProfileLimit
	}
	profiles := make([]iprangedb.StructureRef, 0, profileCount)
	for index := 0; index < profileCount; index++ {
		var membership iprangedb.MembershipRef
		if index%2 == 0 {
			membership = threat
		}
		reference, err := transaction.InternNetworkEnrichmentV1(structuredProfile(index), membership)
		if err != nil {
			return nil, err
		}
		profiles = append(profiles, reference)
	}
	return profiles, nil
}

// structuredPrepareThreatMembership mirrors Rust
// structured::prepare_threat_membership: feeds 0..feeds-1 exist and the
// last feed is the single target of the returned membership.
func structuredPrepareThreatMembership(transaction *iprangedb.StructuredTransaction, feeds int) (iprangedb.MembershipRef, error) {
	var target iprangedb.FeedRef
	var targetFound bool
	for index := 0; index < feeds; index++ {
		name, err := structuredFeedName(index)
		if err != nil {
			return iprangedb.MembershipRef{}, err
		}
		feed, err := transaction.EnsureFeed(name)
		if err != nil {
			return iprangedb.MembershipRef{}, err
		}
		if index+1 == feeds {
			target = feed
			targetFound = true
		}
	}
	if !targetFound {
		return iprangedb.MembershipRef{}, fmt.Errorf("structured benchmark has no target feed")
	}
	empty, err := transaction.EmptyMembership()
	if err != nil {
		return iprangedb.MembershipRef{}, err
	}
	return transaction.AddFeed(empty, target)
}

// structuredAssign mirrors Rust structured::assign: the inclusive
// [index*4, index*4+1] IPv4 range carries the profile.
func structuredAssign(transaction *iprangedb.StructuredTransaction, index int, profile iprangedb.StructureRef) error {
	start, err := structuredRangeStart(index)
	if err != nil {
		return err
	}
	if _, err := transaction.AssignV4(iprangedb.IPv4(start), iprangedb.IPv4(start+1), profile); err != nil {
		return err
	}
	return nil
}

// structuredProfile mirrors Rust structured::profile: the exact ASN,
// country, state, city and WGS84-microdegree location values.
func structuredProfile(index int) iprangedb.NetworkEnrichmentV1 {
	value := int32(index)
	return iprangedb.NetworkEnrichmentV1{
		ASN:       uint32(index) + 1,
		CountryID: uint32(index)%251 + 1,
		StateID:   uint32(index)%4093 + 1,
		CityID:    uint32(index) + 1,
		Location: iprangedb.NetworkEnrichmentV1Location{
			LatitudeMicrodegrees:  value%180_000_001 - 90_000_000,
			LongitudeMicrodegrees: (value*17)%360_000_001 - 180_000_000,
		},
		HasLocation: true,
	}
}

// structuredCountScalarPoints mirrors Rust structured::count_scalar_points:
// every random point must resolve, counting the nonzero ASN values.
func structuredCountScalarPoints(points []uint32, repetitions int, lookup func(iprangedb.IPv4) (iprangedb.NetworkEnrichmentV1View, bool, error)) (uint64, error) {
	var hits uint64
	for range repetitions {
		for _, address := range points {
			view, found, err := lookup(iprangedb.IPv4(address))
			if err != nil {
				return 0, err
			}
			if !found {
				return 0, fmt.Errorf("structured scalar lookup missed an address")
			}
			value, err := view.Value()
			if err != nil {
				return 0, err
			}
			if value.ASN != 0 {
				hits++
			}
		}
	}
	return hits, nil
}

// structuredCountThreatPoints mirrors Rust structured::count_threat_points:
// every random point must resolve, counting those whose threat
// membership contains the target feed index.
func structuredCountThreatPoints(points []uint32, repetitions int, target uint32, lookup func(iprangedb.IPv4) (iprangedb.NetworkEnrichmentV1View, bool, error)) (uint64, error) {
	var hits uint64
	for range repetitions {
		for _, address := range points {
			view, found, err := lookup(iprangedb.IPv4(address))
			if err != nil {
				return 0, err
			}
			if !found {
				return 0, fmt.Errorf("structured threat lookup missed an address")
			}
			matched := false
			membership, hasMembership, err := view.ThreatMembership()
			if err != nil {
				return 0, err
			}
			if hasMembership {
				matched, err = membership.ContainsIndex(target)
				if err != nil {
					return 0, err
				}
			}
			if matched {
				hits++
			}
		}
	}
	return hits, nil
}

// structuredExpectedThreatHits mirrors Rust structured::expected_threat_hits:
// every second workload range (ceil(size/2) per repetition) carries the
// target threat membership.
func structuredExpectedThreatHits(size, repetitions int) (uint64, error) {
	hits := uint64((size + 1) / 2)
	product := hits * uint64(repetitions)
	if hits != 0 && product/hits != uint64(repetitions) {
		return 0, fmt.Errorf("structured threat match count overflow")
	}
	return product, nil
}

// structuredReaderWork mirrors Rust structured::structured_reader_work:
// the reader_work repetition floor lifted to at least
// LOOKUP_WORK_UNITS/size.
func structuredReaderWork(size int) (int, uint64, error) {
	minimumRepetitions, _, err := readerWork(size)
	if err != nil {
		return 0, 0, err
	}
	repetitions := minimumRepetitions
	if units := (structuredLookupWorkUnits + size - 1) / size; units > repetitions {
		repetitions = units
	}
	if size < 0 || uint64(size) > ^uint64(0)/uint64(repetitions) {
		return 0, 0, fmt.Errorf("structured reader work count overflow")
	}
	workUnits := uint64(size) * uint64(repetitions)
	return repetitions, workUnits, nil
}

// structuredFeedLookup abstracts the named-feed lookup both reader kinds
// expose (Rust feed_name + LookupFeed).
type structuredFeedLookup interface {
	LookupFeed(name string) (iprangedb.FeedEntry, bool, error)
}

// structuredTargetFeedIndex mirrors Rust structured::target_feed_index:
// the target threat feed is the highest feed index.
func structuredTargetFeedIndex(lookup structuredFeedLookup, feeds int) (uint32, error) {
	name, err := structuredFeedName(feeds - 1)
	if err != nil {
		return 0, err
	}
	entry, found, err := lookup.LookupFeed(name.String())
	if err != nil {
		return 0, err
	}
	if !found {
		return 0, fmt.Errorf("target threat feed is absent")
	}
	return entry.Index, nil
}

// structuredFeedName mirrors Rust structured::feed_name: zero-padded
// six-digit feed labels.
func structuredFeedName(index int) (iprangedb.FeedName, error) {
	return iprangedb.NewFeedName(fmt.Sprintf("feed-%06d", index))
}

// structuredRangeStart mirrors Rust structured::range_start: the
// inclusive start of range index, four addresses apart, bounded by the
// IPv4 workload space.
func structuredRangeStart(index int) (uint32, error) {
	scaled := uint64(index) * 4
	if scaled > uint64(^uint32(0)) {
		return 0, fmt.Errorf("structured benchmark exceeds the IPv4 workload space")
	}
	return uint32(scaled), nil
}

// structuredRequireCount mirrors Rust scenarios::require_count.
func structuredRequireCount(label string, observed, expected uint64, noun string) error {
	if observed != expected {
		return fmt.Errorf("%s returned %d of %d %s", label, observed, expected, noun)
	}
	return nil
}

// structuredMaxFeed mirrors Rust feeds.max(1): the auxiliary feed count
// is at least one.
func structuredMaxFeed(feeds int) int {
	if feeds < 1 {
		return 1
	}
	return feeds
}

// structuredSeededDirect mirrors Rust direct::seeded_direct: a direct
// timestamp database populated with one unordered dispersed direct
// replacement (the ASN and geo inputs of the separate-enrichment
// lookup).
func structuredSeededDirect(label string, size int, readerCapacity uint32) (*testDatabase, error) {
	database, err := newTestDatabase(label)
	if err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			database.cleanup()
		}
	}()
	tag, err := iprangedb.NewValueTag([]byte("timestamp"))
	if err != nil {
		return nil, fmt.Errorf("invalid benchmark value tag: %v", err)
	}
	if _, err := iprangedb.CreateLive(database.main, iprangedb.AddressFamilyIPv4, iprangedb.ValueKindDirect, iprangedb.StructureKindNone, tag, readerCapacity, nil); err != nil {
		return nil, err
	}
	if err := structuredApplyDirect(database, size); err != nil {
		return nil, err
	}
	ok = true
	return database, nil
}

// structuredApplyDirect mirrors Rust direct::apply_direct: one complete
// direct-map replacement from the unordered DirectSource, committed and
// the writer closed.
func structuredApplyDirect(database *testDatabase, size int) error {
	writer, err := iprangedb.OpenLiveWriter(database.main, toPageBudget(transactionBudget(size, 1)), nil)
	if err != nil {
		return err
	}
	replacement, err := writer.BeginDirectReplacement(nil)
	if err != nil {
		return err
	}
	source, err := newDirectSource(size)
	if err != nil {
		return err
	}
	for {
		batch, more := source.nextBatch()
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
	if err := requireCommitted(finished.Commit()); err != nil {
		return err
	}
	return closeWriter(writer)
}

// structuredPopulatedThreat mirrors Rust structured::populated_threat: a
// membership-kind live database holding feeds 0..feeds-1 where every
// even range carries the target feed through a single membership.
func structuredPopulatedThreat(label string, size, feeds int) (*testDatabase, error) {
	database, err := newTestDatabase(label)
	if err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			database.cleanup()
		}
	}()
	tag, err := iprangedb.NewValueTag([]byte("threat"))
	if err != nil {
		return nil, fmt.Errorf("invalid threat tag: %v", err)
	}
	if _, err := iprangedb.CreateLive(database.main, iprangedb.AddressFamilyIPv4, iprangedb.ValueKindMembership, iprangedb.StructureKindNone, tag, 1, nil); err != nil {
		return nil, err
	}
	writer, err := iprangedb.OpenLiveWriter(database.main, toPageBudget(transactionBudget(size, feeds)), nil)
	if err != nil {
		return nil, err
	}
	transaction, err := writer.BeginMembershipTransaction(nil)
	if err != nil {
		return nil, err
	}
	var target iprangedb.FeedRef
	var targetFound bool
	for index := 0; index < feeds; index++ {
		name, err := structuredFeedName(index)
		if err != nil {
			return nil, err
		}
		feed, err := transaction.EnsureFeed(name)
		if err != nil {
			return nil, err
		}
		if index+1 == feeds {
			target = feed
			targetFound = true
		}
	}
	if !targetFound {
		return nil, fmt.Errorf("threat benchmark has no target feed")
	}
	empty, err := transaction.EmptyMembership()
	if err != nil {
		return nil, err
	}
	threat, err := transaction.AddFeed(empty, target)
	if err != nil {
		return nil, err
	}
	for index := 0; index < size; index += 2 {
		start, err := structuredRangeStart(index)
		if err != nil {
			return nil, err
		}
		if _, err := transaction.ApplyV4(iprangedb.IPv4(start), iprangedb.IPv4(start+1), threat, iprangedb.MembershipReplace); err != nil {
			return nil, err
		}
	}
	if err := requireCommitted(transaction.Commit()); err != nil {
		return nil, err
	}
	if err := closeWriter(writer); err != nil {
		return nil, err
	}
	ok = true
	return database, nil
}

// structuredAggregateFileSize mirrors Rust structured::aggregate_file_size:
// logical sizes sum with overflow refusal; physical sizes sum while every
// component reports one and become None on the first missing or
// overflowing component.
func structuredAggregateFileSize(paths []string) (fileSize, error) {
	var result fileSize
	physical := uint64(0)
	result.physical = &physical
	for _, path := range paths {
		current, err := fileSizeOf(path)
		if err != nil {
			return fileSize{}, err
		}
		if result.logical > ^uint64(0)-current.logical {
			return fileSize{}, fmt.Errorf("aggregate logical file size overflow")
		}
		result.logical += current.logical
		switch {
		case result.physical == nil || current.physical == nil:
			result.physical = nil
		default:
			next := *result.physical + *current.physical
			if next < *result.physical {
				result.physical = nil
			} else {
				*result.physical = next
			}
		}
	}
	return result, nil
}
