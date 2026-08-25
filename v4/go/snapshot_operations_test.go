// Milestone 3 chunk 3b-3 snapshot parity tests (Rust
// tests/snapshot_operations.rs): one pinned immutable generation copied
// into a fresh published output under budget, preserving identity,
// generation, ranges, feeds, memberships, structures, and metadata. Every
// failure shape is asserted through the public Cause+Cleanup surface, and
// the no-implicit-validation rule is pinned by snapshotting a
// CRC-damaged source that traversal alone cannot catch.

package iprangedb

import (
	"bytes"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/firehol/iprange/v4/go/internal/format"
	"github.com/firehol/iprange/v4/go/internal/mapping"
	"github.com/firehol/iprange/v4/go/internal/snapshot"
	"github.com/firehol/iprange/v4/go/internal/writer"
)

// snapshotBudget returns one Rust-shaped snapshot budget with the given
// open-file count (Rust tests budget(open_files)): 16 MiB heap, 100k
// output pages.
func snapshotBudget(openFiles uint32) *SnapshotBudget {
	return &SnapshotBudget{MaxHeapBytes: 16 << 20, MaxOutputPages: 100_000, MaxOpenFiles: openFiles}
}

// snapshotDest returns one fresh destination path inside a fresh private
// directory of the test's temporary tree.
func snapshotDest(t *testing.T, name string) string {
	t.Helper()
	return filepath.Join(t.TempDir(), name)
}

// snapshotDir returns the parent directory of one snapshot destination.
func snapshotDir(destination string) string {
	return filepath.Dir(destination)
}

// assertNoSnapshotArtifacts fails when any private publication artifact
// remains in the destination directory (Rust assert_no_private_artifacts).
func assertNoSnapshotArtifacts(t *testing.T, directory string) {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatalf("read directory: %v", err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if len(name) >= len(".iprange-") && name[:len(".iprange-")] == ".iprange-" {
			t.Errorf("private snapshot artifact remained: %s", name)
		}
	}
}

// snapshotSidecar returns the coordination twin path of one main file.
func snapshotSidecar(path string) string {
	return path + format.CoordinationSuffix
}

// supportedSnapshotReplacement selects the replace policy the platform
// can honor (Rust supported_replacement): rollback-safe exchange where
// available, the explicit no-rollback rename elsewhere.
func supportedSnapshotReplacement() SnapshotPublicationPolicy {
	if mapping.ExchangeAvailable() {
		return PolicyReplaceExisting
	}
	return PolicyReplaceExistingNoRollback
}

// internalCode returns the internal ErrorCode of one machine failure
// cause.
func internalCode(t *testing.T, err error) format.ErrorCode {
	t.Helper()
	var internal *format.Error
	if !errors.As(err, &internal) {
		t.Fatalf("cause not an internal *format.Error: %v", err)
	}
	return internal.Code
}

// failureCode returns the public ErrorCode of one preparation failure.
func failureCode(t *testing.T, err error) ErrorCode {
	t.Helper()
	var failure *SnapshotPreparationFailure
	if !errors.As(err, &failure) {
		t.Fatalf("not a *SnapshotPreparationFailure: %v", err)
	}
	var public *Error
	if !errors.As(failure.Cause, &public) {
		t.Fatalf("cause not a public *Error: %v", failure.Cause)
	}
	return public.Code
}

// TestSnapshotImmutableDirectPreservesIdentityGenerationRangesAndMetadata
// snapshots the committed direct-ipv4 fixture with the fail-if-exists
// policy and verifies the published output byte-for-byte in identity,
// ranges, and metadata (Rust immutable_direct_snapshot_preserves_...).
func TestSnapshotImmutableDirectPreservesIdentityGenerationRangesAndMetadata(t *testing.T) {
	requirePublicationSecurity(t)
	source := openPublic(t, "direct-ipv4.iprdb")
	defer source.Close()
	sourceInfo, err := source.Info()
	if err != nil {
		t.Fatal("source info:", err)
	}
	sourceMetadata, ok, err := source.MetadataJSON()
	if err != nil || !ok {
		t.Fatalf("source metadata: ok=%v err=%v", ok, err)
	}
	sourceStat, err := os.Stat(fixture(t, "direct-ipv4.iprdb"))
	if err != nil {
		t.Fatal("source stat:", err)
	}

	destination := snapshotDest(t, "direct-snapshot.iprdb")
	result, err := SnapshotTo(fixture(t, "direct-ipv4.iprdb"), SnapshotSourceImmutable, destination, PolicyFailIfExists, snapshotBudget(2), nil)
	if err != nil {
		t.Fatal("snapshot:", err)
	}
	if result.Publication.Publication != PublicationPublished {
		t.Fatalf("status = %v, want published", result.Publication.Publication)
	}
	if result.CleanupState() != CleanupStateClean {
		t.Fatalf("cleanup = %v, want clean", result.CleanupState())
	}
	if _, err := os.Lstat(snapshotSidecar(destination)); !os.IsNotExist(err) {
		t.Fatalf("sidecar exists after snapshot: %v", err)
	}

	output := openPublished(t, destination)
	defer output.Close()
	outputInfo, err := output.Info()
	if err != nil {
		t.Fatal("output info:", err)
	}
	if outputInfo.DatabaseID != sourceInfo.DatabaseID {
		t.Errorf("database id preserved: %x != %x", outputInfo.DatabaseID, sourceInfo.DatabaseID)
	}
	if outputInfo.TransactionID != sourceInfo.TransactionID {
		t.Errorf("transaction id preserved: %d != %d", outputInfo.TransactionID, sourceInfo.TransactionID)
	}
	if outputInfo.CommitNonce != sourceInfo.CommitNonce {
		t.Errorf("commit nonce preserved: %x != %x", outputInfo.CommitNonce, sourceInfo.CommitNonce)
	}
	for _, probe := range []struct {
		address string
		value   uint32
		want    bool
	}{
		{"10.0.0.9", 0, false},
		{"10.0.0.10", 2, true},
		{"10.0.0.15", 3, true},
		{"10.0.0.18", 2, true},
		{"10.0.0.22", 0, false},
		{"10.0.0.28", 1, true},
		{"10.0.0.31", 1, true},
		{"10.0.0.32", 0, false},
	} {
		got, found, err := output.LookupDirectV4(parseV4(probe.address))
		if err != nil {
			t.Fatalf("lookup %s: %v", probe.address, err)
		}
		if found != probe.want || got != probe.value {
			t.Errorf("lookup %s = (%d, %v), want (%d, %v)", probe.address, got, found, probe.value, probe.want)
		}
	}
	outputMetadata, ok, err := output.MetadataJSON()
	if err != nil || !ok {
		t.Fatalf("output metadata: ok=%v err=%v", ok, err)
	}
	if !bytes.Equal(outputMetadata, sourceMetadata) {
		t.Errorf("metadata not preserved: %q != %q", outputMetadata, sourceMetadata)
	}
	t.Logf("source length %d, output length %d (page count %d)", sourceStat.Size(), outputInfo.PageCount*4096, outputInfo.PageCount)
	if outputInfo.PageCount*4096 > uint64(sourceStat.Size()) {
		t.Errorf("snapshot output %d bytes exceeds the source %d bytes", outputInfo.PageCount*4096, sourceStat.Size())
	}
	assertNoSnapshotArtifacts(t, snapshotDir(destination))
}

// TestSnapshotImmutableMembershipPreservesNamesIndexesBitmapsAndMetadata
// snapshots the committed membership-ipv4 fixture and verifies the feed
// catalog and the per-range bitmaps (Rust
// live_membership_snapshot_preserves_..., adapted to the immutable
// source that the Go boundary accepts).
func TestSnapshotImmutableMembershipPreservesNamesIndexesBitmapsAndMetadata(t *testing.T) {
	requirePublicationSecurity(t)
	source := openPublic(t, "membership-ipv4.iprdb")
	defer source.Close()
	sourceInfo, err := source.Info()
	if err != nil {
		t.Fatal("source info:", err)
	}
	_, sourceOK, err := source.MetadataJSON()
	if err != nil {
		t.Fatal("source metadata:", err)
	}

	destination := snapshotDest(t, "membership-snapshot.iprdb")
	result, err := SnapshotTo(fixture(t, "membership-ipv4.iprdb"), SnapshotSourceImmutable, destination, PolicyFailIfExists, snapshotBudget(2), nil)
	if err != nil {
		t.Fatal("snapshot:", err)
	}
	if result.Publication.Publication != PublicationPublished || result.CleanupState() != CleanupStateClean {
		t.Fatalf("publication = %+v", result.Publication)
	}

	output := openPublished(t, destination)
	defer output.Close()
	outputInfo, err := output.Info()
	if err != nil {
		t.Fatal("output info:", err)
	}
	if outputInfo.ActiveFeedCount != sourceInfo.ActiveFeedCount {
		t.Errorf("feed count preserved: %d != %d", outputInfo.ActiveFeedCount, sourceInfo.ActiveFeedCount)
	}
	beta, found, err := output.LookupFeed("feed-001")
	if err != nil || !found || beta.Index != 1 {
		t.Errorf("feed-001 preserved: index=%d found=%v err=%v", beta.Index, found, err)
	}
	reused, found, err := output.LookupFeed("feed-reused")
	if err != nil || !found {
		t.Errorf("feed-reused preserved: found=%v err=%v", found, err)
	}
	_ = reused
	pin, err := output.Pin()
	if err != nil {
		t.Fatal("pin:", err)
	}
	defer pin.Close()

	// Range 10.0.0.0/24 carries feeds 000, reused, 063, 064, 069; range
	// 10.0.1.128/25 carries only feeds 001 and 065.
	first, found, err := pin.LookupMembershipV4(parseV4("10.0.0.5"))
	if err != nil || !found {
		t.Fatalf("first membership: found=%v err=%v", found, err)
	}
	for _, probe := range []struct {
		index uint32
		want  bool
	}{
		{0, true}, {1, false}, {63, true}, {64, true}, {65, false}, {69, true},
	} {
		has, err := first.ContainsIndex(probe.index)
		if err != nil {
			t.Fatal("contains:", err)
		}
		if has != probe.want {
			t.Errorf("first range contains %d = %v, want %v", probe.index, has, probe.want)
		}
	}
	last, found, err := pin.LookupMembershipV4(parseV4("10.0.1.200"))
	if err != nil || !found {
		t.Fatalf("last membership: found=%v err=%v", found, err)
	}
	for _, probe := range []struct {
		index uint32
		want  bool
	}{
		{0, false}, {1, true}, {63, false}, {64, false}, {65, true}, {69, false},
	} {
		has, err := last.ContainsIndex(probe.index)
		if err != nil {
			t.Fatal("contains:", err)
		}
		if has != probe.want {
			t.Errorf("last range contains %d = %v, want %v", probe.index, has, probe.want)
		}
	}
	outputMetadata, outputOK, err := output.MetadataJSON()
	if err != nil {
		t.Fatal("output metadata:", err)
	}
	if outputOK != sourceOK {
		t.Errorf("metadata presence differs: output %v, source %v", outputOK, sourceOK)
	}
	if outputOK {
		t.Errorf("membership fixture gained metadata %q", outputMetadata)
	}
	assertNoSnapshotArtifacts(t, snapshotDir(destination))
}

// TestSnapshotImmutableStructuredPreservesRangesAndMetadata snapshots
// the structured fixtures, with and without threat memberships, and
// verifies payloads, locations, and the linked bitmaps (Rust structured
// snapshot coverage via copy_structured_v4/v6).
func TestSnapshotImmutableStructuredPreservesRangesAndMetadata(t *testing.T) {
	requirePublicationSecurity(t)
	for _, fixtureName := range []string{"structured-ipv4.iprdb", "structured-ipv4-nothreat.iprdb"} {
		t.Run(fixtureName, func(t *testing.T) {
			source := openPublic(t, fixtureName)
			defer source.Close()
			sourceMetadata, sourceOK, err := source.MetadataJSON()
			if err != nil {
				t.Fatal("source metadata:", err)
			}

			destination := snapshotDest(t, fixtureName+".snapshot")
			result, err := SnapshotTo(fixture(t, fixtureName), SnapshotSourceImmutable, destination, PolicyFailIfExists, snapshotBudget(2), nil)
			if err != nil {
				t.Fatal("snapshot:", err)
			}
			if result.Publication.Publication != PublicationPublished || result.CleanupState() != CleanupStateClean {
				t.Fatalf("publication = %+v", result.Publication)
			}
			output := openPublished(t, destination)
			defer output.Close()
			pin, err := output.Pin()
			if err != nil {
				t.Fatal("pin:", err)
			}
			defer pin.Close()

			if fixtureName == "structured-ipv4.iprdb" {
				view, found, err := pin.LookupNetworkEnrichmentV1V4(parseV4("10.1.0.10"))
				if err != nil || !found {
					t.Fatalf("lookup 10.1.0.10: found=%v err=%v", found, err)
				}
				value, err := view.Value()
				if err != nil {
					t.Fatal("value:", err)
				}
				if value.ASN != 64512 || value.CountryID != 1 || value.StateID != 2 || value.CityID != 3 {
					t.Errorf("payload = %+v, want ASN 64512 country 1 state 2 city 3", value)
				}
				if !value.HasLocation || value.Location.LatitudeMicrodegrees != 37983810 || value.Location.LongitudeMicrodegrees != 23727539 {
					t.Errorf("location = %+v, want 37983810/23727539", value.Location)
				}
				threats, found, err := view.ThreatMembership()
				if err != nil || !found {
					t.Fatalf("threat membership: found=%v err=%v", found, err)
				}
				has, err := threats.ContainsIndex(0)
				if err != nil {
					t.Fatal("contains:", err)
				}
				if !has {
					t.Errorf("botnet feed not set in the preserved threat bitmap")
				}
				// The range without a location keeps HasLocation off.
				view, found, err = pin.LookupNetworkEnrichmentV1V4(parseV4("10.1.0.70"))
				if err != nil || !found {
					t.Fatalf("lookup 10.1.0.70: found=%v err=%v", found, err)
				}
				value, err = view.Value()
				if err != nil {
					t.Fatal("value:", err)
				}
				if value.HasLocation {
					t.Errorf("range 10.1.0.70 gained a location: %+v", value)
				}
			} else {
				view, found, err := pin.LookupNetworkEnrichmentV1V4(parseV4("10.2.0.10"))
				if err != nil || !found {
					t.Fatalf("lookup 10.2.0.10: found=%v err=%v", found, err)
				}
				value, err := view.Value()
				if err != nil {
					t.Fatal("value:", err)
				}
				if value.ASN != 64514 || value.CountryID != 7 {
					t.Errorf("payload = %+v, want ASN 64514 country 7", value)
				}
				if _, found, err := view.ThreatMembership(); err != nil || found {
					t.Errorf("nothreat fixture gained a membership: found=%v err=%v", found, err)
				}
			}
			outputMetadata, outputOK, err := output.MetadataJSON()
			if err != nil {
				t.Fatal("output metadata:", err)
			}
			if outputOK != sourceOK || (outputOK && !bytes.Equal(outputMetadata, sourceMetadata)) {
				t.Errorf("metadata not preserved: source ok=%v output ok=%v", sourceOK, outputOK)
			}
			assertNoSnapshotArtifacts(t, snapshotDir(destination))
		})
	}
}

// TestSnapshotCancellationExistingDestinationAndBudgetFailurePublishNothing
// pins the three Rust early-refusal shapes (cancelled, name exists with a
// foreign destination preserved, and the open-file budget refusal).
func TestSnapshotCancellationExistingDestinationAndBudgetFailurePublishNothing(t *testing.T) {
	sourceFile := fixture(t, "direct-ipv4.iprdb")

	// Cancellation before the operation starts publishes nothing.
	cancelled := NewCancellationToken()
	cancelled.Cancel()
	destination := snapshotDest(t, "cancelled.iprdb")
	_, err := SnapshotTo(sourceFile, SnapshotSourceImmutable, destination, PolicyFailIfExists, snapshotBudget(2), cancelled)
	if code := failureCode(t, err); code != ErrorCancelled {
		t.Fatalf("cause code = %v, want cancelled", code)
	}
	if _, statErr := os.Lstat(destination); !os.IsNotExist(statErr) {
		t.Fatalf("cancelled snapshot produced an output: %v", statErr)
	}

	// An existing destination with the fail-if-exists policy refuses and
	// leaves the foreign file untouched.
	foreign := snapshotDest(t, "foreign.iprdb")
	if err := os.WriteFile(foreign, []byte("foreign"), 0o644); err != nil {
		t.Fatal("write foreign:", err)
	}
	_, err = SnapshotTo(sourceFile, SnapshotSourceImmutable, foreign, PolicyFailIfExists, snapshotBudget(2), nil)
	if code := failureCode(t, err); code != ErrorNameExists {
		t.Fatalf("cause code = %v, want name exists", code)
	}
	bytes, err := os.ReadFile(foreign)
	if err != nil {
		t.Fatal("read foreign:", err)
	}
	if string(bytes) != "foreign" {
		t.Errorf("foreign destination changed: %q", bytes)
	}

	// The open-file budget refuses before anything is created.
	destination = snapshotDest(t, "budget.iprdb")
	_, err = SnapshotTo(sourceFile, SnapshotSourceImmutable, destination, PolicyFailIfExists, snapshotBudget(0), nil)
	if code := failureCode(t, err); code != ErrorInsufficientResourceBudget {
		t.Fatalf("cause code = %v, want insufficient resource budget", code)
	}
	if _, statErr := os.Lstat(destination); !os.IsNotExist(statErr) {
		t.Fatalf("budget-refused snapshot produced an output: %v", statErr)
	}
}

// TestSnapshotHeapAndExactOutputPageBudgetsFailBeforePublication pins the
// heap input refusal and the exact output-page boundary (Rust
// heap_and_exact_output_page_budgets_fail_before_publication): a heap
// smaller than the metadata input fails before publication, page
// budget-1 fails before publication, and the exact page count publishes.
func TestSnapshotHeapAndExactOutputPageBudgetsFailBeforePublication(t *testing.T) {
	requirePublicationSecurity(t)
	sourceFile := fixture(t, "direct-ipv4.iprdb")

	// The direct fixture carries a 46-byte metadata payload; a 4-byte
	// heap cannot hold it.
	destination := snapshotDest(t, "heap.iprdb")
	_, err := SnapshotTo(sourceFile, SnapshotSourceImmutable, destination, PolicyFailIfExists, &SnapshotBudget{MaxHeapBytes: 4, MaxOutputPages: 100_000, MaxOpenFiles: 2}, nil)
	if code := failureCode(t, err); code != ErrorInsufficientResourceBudget {
		t.Fatalf("heap cause code = %v, want insufficient resource budget", code)
	}
	if _, statErr := os.Lstat(destination); !os.IsNotExist(statErr) {
		t.Fatalf("heap-refused snapshot produced an output: %v", statErr)
	}

	// Establish the exact output page count of this source.
	exact := snapshotDest(t, "exact.iprdb")
	result, err := SnapshotTo(sourceFile, SnapshotSourceImmutable, exact, PolicyFailIfExists, snapshotBudget(2), nil)
	if err != nil {
		t.Fatal("exact snapshot:", err)
	}
	output := openPublished(t, exact)
	info, err := output.Info()
	if err != nil {
		t.Fatal("info:", err)
	}
	output.Close()
	pages := info.PageCount
	t.Logf("exact page count %d, cleanup %v", pages, result.CleanupState())
	if pages < 3 {
		t.Fatalf("fixture unexpectedly small: %d pages", pages)
	}

	// One page short fails before publication.
	short := snapshotDest(t, "short.iprdb")
	_, err = SnapshotTo(sourceFile, SnapshotSourceImmutable, short, PolicyFailIfExists, &SnapshotBudget{MaxHeapBytes: 16 << 20, MaxOutputPages: pages - 1, MaxOpenFiles: 2}, nil)
	if code := failureCode(t, err); code != ErrorInsufficientResourceBudget {
		t.Fatalf("page-short cause code = %v, want insufficient resource budget", code)
	}
	if _, statErr := os.Lstat(short); !os.IsNotExist(statErr) {
		t.Fatalf("page-short snapshot produced an output: %v", statErr)
	}
	assertNoSnapshotArtifacts(t, snapshotDir(short))

	// The exact page count publishes.
	complete := snapshotDest(t, "complete.iprdb")
	result, err = SnapshotTo(sourceFile, SnapshotSourceImmutable, complete, PolicyFailIfExists, &SnapshotBudget{MaxHeapBytes: 16 << 20, MaxOutputPages: pages, MaxOpenFiles: 2}, nil)
	if err != nil {
		t.Fatal("complete snapshot:", err)
	}
	if result.Publication.Publication != PublicationPublished {
		t.Fatalf("complete status = %v", result.Publication.Publication)
	}
	assertNoSnapshotArtifacts(t, snapshotDir(complete))
}

// TestSnapshotLiveRefusedOnUnsupportedPlatforms pins the honest live
// refusal on platforms without proven sidecar coordination (Rust
// freebsd_boundary.rs every_constructible_live_entry_rejects_before_
// mutation): the refusal class is ErrorLiveCoordinationUnsupported
// before any path access, before budget validation, and no output is
// produced. On linux/darwin the live mode is real and this boundary is
// covered by the ported live snapshot tests below.
func TestSnapshotLiveRefusedOnUnsupportedPlatforms(t *testing.T) {
	if exchangeAvailable() {
		t.Skip("live coordination is implemented on this platform")
	}
	sourceFile := fixture(t, "direct-ipv4.iprdb")
	destination := snapshotDest(t, "live.iprdb")
	_, err := SnapshotTo(sourceFile, SnapshotSourceLive, destination, PolicyFailIfExists, snapshotBudget(0), nil)
	if code := failureCode(t, err); code != ErrorLiveCoordinationUnsupported {
		t.Fatalf("cause code = %v, want live coordination unsupported", code)
	}
	if _, statErr := os.Lstat(destination); !os.IsNotExist(statErr) {
		t.Fatalf("live snapshot produced an output: %v", statErr)
	}
}

// TestSnapshotReplacementAcceptsArbitraryPreviousBytesAndExactContent
// pins the replace policy over a pre-existing destination (Rust
// replacement_accepts_arbitrary_previous_bytes_...).
func TestSnapshotReplacementAcceptsArbitraryPreviousBytesAndExactContent(t *testing.T) {
	requirePublicationSecurity(t)
	sourceFile := fixture(t, "direct-ipv4.iprdb")
	destination := snapshotDest(t, "replace.iprdb")
	if err := os.WriteFile(destination, []byte("previous"), 0o644); err != nil {
		t.Fatal("write previous:", err)
	}
	result, err := SnapshotTo(sourceFile, SnapshotSourceImmutable, destination, supportedSnapshotReplacement(), snapshotBudget(3), nil)
	if err != nil {
		t.Fatal("replacement snapshot:", err)
	}
	if result.Publication.Publication != PublicationPublished || result.CleanupState() != CleanupStateClean {
		t.Fatalf("publication = %+v", result.Publication)
	}
	output := openPublished(t, destination)
	defer output.Close()
	value, found, err := output.LookupDirectV4(parseV4("10.0.0.10"))
	if err != nil || !found || value != 2 {
		t.Errorf("replaced destination lookup = (%d, %v, %v), want (2, true, nil)", value, found, err)
	}
	assertNoSnapshotArtifacts(t, snapshotDir(destination))
}

// TestSnapshotNoRollbackReplacementIsExplicitAndCannotBeRemovedAfterPublication
// pins the explicit no-rollback policy (Rust
// no_rollback_replacement_is_explicit_...): the destination provably holds
// the published content and the operation reports Clean.
func TestSnapshotNoRollbackReplacementIsExplicitAndCannotBeRemovedAfterPublication(t *testing.T) {
	requirePublicationSecurity(t)
	sourceFile := fixture(t, "direct-ipv4.iprdb")
	destination := snapshotDest(t, "no-rollback.iprdb")
	if err := os.WriteFile(destination, []byte("previous"), 0o644); err != nil {
		t.Fatal("write previous:", err)
	}
	result, err := SnapshotTo(sourceFile, SnapshotSourceImmutable, destination, PolicyReplaceExistingNoRollback, snapshotBudget(3), nil)
	if err != nil {
		t.Fatal("no-rollback snapshot:", err)
	}
	if result.Publication.Publication != PublicationPublished || result.CleanupState() != CleanupStateClean {
		t.Fatalf("publication = %+v", result.Publication)
	}
	published, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal("read published:", err)
	}
	if bytes.Equal(published, []byte("previous")) {
		t.Errorf("destination still holds the previous bytes")
	}
	output := openPublished(t, destination)
	defer output.Close()
	value, found, err := output.LookupDirectV4(parseV4("10.0.0.28"))
	if err != nil || !found || value != 1 {
		t.Errorf("published lookup = (%d, %v, %v), want (1, true, nil)", value, found, err)
	}
}

// TestSnapshotImmutableCanCompactItsOwnPathByReplacement snapshots one
// immutable file onto its own path (Rust
// immutable_snapshot_can_compact_its_own_path_by_replacement): the
// identity survives, the ranges survive, and no sidecar artifact remains.
func TestSnapshotImmutableCanCompactItsOwnPathByReplacement(t *testing.T) {
	requirePublicationSecurity(t)
	sourcePath := snapshotDest(t, "self.iprdb")
	if err := copyFixture(t, "direct-ipv4.iprdb", sourcePath); err != nil {
		t.Fatal("copy fixture:", err)
	}
	before := openPublished(t, sourcePath)
	beforeInfo, err := before.Info()
	if err != nil {
		t.Fatal("before info:", err)
	}
	if err := before.Close(); err != nil {
		t.Fatal("close before:", err)
	}

	result, err := SnapshotTo(sourcePath, SnapshotSourceImmutable, sourcePath, supportedSnapshotReplacement(), snapshotBudget(3), nil)
	if err != nil {
		t.Fatal("self snapshot:", err)
	}
	if result.Publication.Publication != PublicationPublished || result.CleanupState() != CleanupStateClean {
		t.Fatalf("publication = %+v", result.Publication)
	}
	if _, err := os.Lstat(snapshotSidecar(sourcePath)); !os.IsNotExist(err) {
		t.Fatalf("sidecar exists after self snapshot: %v", err)
	}
	after := openPublished(t, sourcePath)
	defer after.Close()
	afterInfo, err := after.Info()
	if err != nil {
		t.Fatal("after info:", err)
	}
	if afterInfo.DatabaseID != beforeInfo.DatabaseID || afterInfo.TransactionID != beforeInfo.TransactionID || afterInfo.CommitNonce != beforeInfo.CommitNonce {
		t.Errorf("identity changed by self compaction: before %x/%d/%x after %x/%d/%x",
			beforeInfo.DatabaseID, beforeInfo.TransactionID, beforeInfo.CommitNonce,
			afterInfo.DatabaseID, afterInfo.TransactionID, afterInfo.CommitNonce)
	}
	value, found, err := after.LookupDirectV4(parseV4("10.0.0.15"))
	if err != nil || !found || value != 3 {
		t.Errorf("post-compaction lookup = (%d, %v, %v), want (3, true, nil)", value, found, err)
	}
	assertNoSnapshotArtifacts(t, snapshotDir(sourcePath))
}

// TestSnapshotReplacementRequiresExistingDestination pins the NameNotFound
// classification of a replace policy over a missing destination (Rust
// replacement_requires_an_existing_destination...).
func TestSnapshotReplacementRequiresExistingDestination(t *testing.T) {
	requirePublicationSecurity(t)
	sourceFile := fixture(t, "direct-ipv4.iprdb")
	destination := snapshotDest(t, "missing.iprdb")
	_, err := SnapshotTo(sourceFile, SnapshotSourceImmutable, destination, supportedSnapshotReplacement(), snapshotBudget(3), nil)
	if code := failureCode(t, err); code != ErrorNameNotFound {
		t.Fatalf("cause code = %v, want name not found", code)
	}
	if _, statErr := os.Lstat(destination); !os.IsNotExist(statErr) {
		t.Fatalf("missing-destination snapshot produced an output: %v", statErr)
	}
	assertNoSnapshotArtifacts(t, snapshotDir(destination))
}

// TestSnapshotStrictReplacementFailsBeforeChangingDestination pins the
// DurabilityUnsupported refusal of the rollback-safe exchange on the
// platforms without the primitive (Rust strict_replacement_fails_...,
// cfg windows/freebsd); Linux implements the exchange and skips.
func TestSnapshotStrictReplacementFailsBeforeChangingDestination(t *testing.T) {
	if mapping.ExchangeAvailable() {
		t.Skip("atomic name exchange is available; the strict refusal does not apply")
	}
	sourceFile := fixture(t, "direct-ipv4.iprdb")
	destination := snapshotDest(t, "strict.iprdb")
	if err := os.WriteFile(destination, []byte("previous"), 0o644); err != nil {
		t.Fatal("write previous:", err)
	}
	_, err := SnapshotTo(sourceFile, SnapshotSourceImmutable, destination, PolicyReplaceExisting, snapshotBudget(3), nil)
	if code := failureCode(t, err); code != ErrorDurabilityUnsupported {
		t.Fatalf("cause code = %v, want durability unsupported", code)
	}
	bytes, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal("read destination:", err)
	}
	if string(bytes) != "previous" {
		t.Errorf("destination changed by the strict refusal: %q", bytes)
	}
	assertNoSnapshotArtifacts(t, snapshotDir(destination))
}

// TestSnapshotMalformedTraversalFailsCleanlyButCRCDamageIsNotImplicitlyValidated
// pins the Rust traversal/CRC pair: a corrupted range root fails the copy
// with FormatInvalid and publishes nothing, while a CRC-damaged root page
// copies without any implicit validation pass, exactly like the normal
// hot path.
func TestSnapshotMalformedTraversalFailsCleanlyButCRCDamageIsNotImplicitlyValidated(t *testing.T) {
	requirePublicationSecurity(t)
	// Corrupted range root: traversal fails with FormatInvalid.
	malformed := snapshotDest(t, "malformed.iprdb")
	if err := copyFixture(t, "direct-ipv4.iprdb", malformed); err != nil {
		t.Fatal("copy fixture:", err)
	}
	mutateSelectedRangeRoot(t, malformed, func(page []byte) {
		copy(page[:4], []byte("BAD!"))
	})
	// The open refuses nothing: the damage is on a data page.
	opened, err := OpenImmutable(malformed)
	if err != nil {
		t.Fatalf("open malformed source: %v", err)
	}
	opened.Close()

	destination := snapshotDest(t, "malformed-out.iprdb")
	_, err = SnapshotTo(malformed, SnapshotSourceImmutable, destination, PolicyFailIfExists, snapshotBudget(2), nil)
	if code := failureCode(t, err); code != ErrorFormatInvalid {
		t.Fatalf("cause code = %v, want format invalid", code)
	}
	if _, statErr := os.Lstat(destination); !os.IsNotExist(statErr) {
		t.Fatalf("malformed snapshot produced an output: %v", statErr)
	}
	assertNoSnapshotArtifacts(t, snapshotDir(destination))

	// CRC damage on the same page: the snapshot copies without implicit
	// validation and the output opens normally.
	damaged := snapshotDest(t, "damaged.iprdb")
	if err := copyFixture(t, "direct-ipv4.iprdb", damaged); err != nil {
		t.Fatal("copy fixture:", err)
	}
	mutateSelectedRangeRoot(t, damaged, func(page []byte) {
		page[28] ^= 0xff
	})
	destination = snapshotDest(t, "damaged-out.iprdb")
	if _, err := SnapshotTo(damaged, SnapshotSourceImmutable, destination, PolicyFailIfExists, snapshotBudget(2), nil); err != nil {
		t.Fatalf("CRC-damaged snapshot failed: %v", err)
	}
	output := openPublished(t, destination)
	defer output.Close()
	value, found, err := output.LookupDirectV4(parseV4("10.0.0.15"))
	if err != nil || !found || value != 3 {
		t.Errorf("damaged-source output lookup = (%d, %v, %v), want (3, true, nil)", value, found, err)
	}
}

// copyFixture copies one committed corpus fixture to a writable path.
func copyFixture(t *testing.T, fixtureName, destination string) error {
	t.Helper()
	bytes, err := os.ReadFile(fixture(t, fixtureName))
	if err != nil {
		return err
	}
	return os.WriteFile(destination, bytes, 0o644)
}

// mutateSelectedRangeRoot applies one mutation to the selected
// generation's range root page (Rust mutate_selected_range_root): the
// meta pair selects the newer transaction, whose RangeRoot names the
// first data page.
func mutateSelectedRangeRoot(t *testing.T, path string, mutate func(page []byte)) {
	t.Helper()
	bytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatal("read source:", err)
	}
	leftTxn := binary.LittleEndian.Uint64(bytes[48:56])
	rightTxn := binary.LittleEndian.Uint64(bytes[4096+48 : 4096+56])
	metaOffset := 0
	if rightTxn > leftTxn {
		metaOffset = 4096
	}
	root := binary.LittleEndian.Uint32(bytes[metaOffset+144 : metaOffset+148])
	if root < 2 {
		t.Fatalf("range root %d not on a data page", root)
	}
	start := int(root) * 4096
	mutate(bytes[start : start+4096])
	if err := os.WriteFile(path, bytes, 0o644); err != nil {
		t.Fatal("write source:", err)
	}
}

// TestSnapshotBoundaryGuards pins the two Go-boundary guards that have no
// Rust counterpart (Rust passes the budget by value and its mode enum is
// closed): a nil budget and an invalid source mode refuse with
// ErrorInvalidArgument before any destination artifact exists.
func TestSnapshotBoundaryGuards(t *testing.T) {
	sourceFile := fixture(t, "direct-ipv4.iprdb")

	destination := snapshotDest(t, "nil-budget.iprdb")
	_, err := SnapshotTo(sourceFile, SnapshotSourceImmutable, destination, PolicyFailIfExists, nil, nil)
	if code := failureCode(t, err); code != ErrorInvalidArgument {
		t.Fatalf("nil budget cause code = %v, want invalid argument", code)
	}
	if _, statErr := os.Lstat(destination); !os.IsNotExist(statErr) {
		t.Fatalf("nil-budget snapshot produced an output: %v", statErr)
	}

	destination = snapshotDest(t, "bad-mode.iprdb")
	_, err = SnapshotTo(sourceFile, SnapshotSourceMode(255), destination, PolicyFailIfExists, &SnapshotBudget{MaxHeapBytes: 16 << 20, MaxOutputPages: 100_000, MaxOpenFiles: 2}, nil)
	if code := failureCode(t, err); code != ErrorInvalidArgument {
		t.Fatalf("invalid mode cause code = %v, want invalid argument", code)
	}
	if _, statErr := os.Lstat(destination); !os.IsNotExist(statErr) {
		t.Fatalf("invalid-mode snapshot produced an output: %v", statErr)
	}
}

// TestSnapshotImmutableSourceReplacementDuringCopyBlocksPublication
// ports the Rust immutable_source_replacement_during_copy_blocks_
// publication race: a controller renames the source away as soon as the
// private output appears, and the final ConfirmUnchanged between the
// build and the publish must refuse with RecoveryCandidateChanged -
// never a snapshot of a swapped-in generation, never an output, never a
// private residue. The source is a 20k-range direct database so the copy
// outlasts the controller's first poll.
func TestSnapshotImmutableSourceReplacementDuringCopyBlocksPublication(t *testing.T) {
	requirePublicationSecurity(t)
	dir := t.TempDir()
	source := filepath.Join(dir, "source.iprdb")
	moved := filepath.Join(dir, "moved-source.iprdb")
	destination := filepath.Join(dir, "output.iprdb")
	buildLargeDirectSource(t, source, 20_000)

	controllerDone := make(chan struct{})
	controllerStarted := make(chan struct{})
	controller := make(chan bool, 1)
	go func() {
		defer close(controller)
		close(controllerStarted)
		for {
			select {
			case <-controllerDone:
				controller <- false
				return
			default:
			}
			entries, err := os.ReadDir(dir)
			if err != nil {
				controller <- false
				return
			}
			for _, entry := range entries {
				name := entry.Name()
				if len(name) >= len(".iprange-publish-") && name[:len(".iprange-publish-")] == ".iprange-publish-" {
					if err := os.Rename(source, moved); err != nil {
						t.Errorf("controller rename: %v", err)
						controller <- false
						return
					}
					controller <- true
					return
				}
			}
		}
	}()
	<-controllerStarted

	_, err := SnapshotTo(source, SnapshotSourceImmutable, destination, PolicyFailIfExists, &SnapshotBudget{MaxHeapBytes: 16 << 20, MaxOutputPages: 100_000, MaxOpenFiles: 2}, nil)
	close(controllerDone)
	if !<-controller {
		t.Fatalf("controller missed private-output creation")
	}
	if code := failureCode(t, err); code != ErrorRecoveryCandidateChanged {
		t.Fatalf("cause code = %v, want recovery candidate changed (err %v)", code, err)
	}
	if _, statErr := os.Lstat(destination); !os.IsNotExist(statErr) {
		t.Fatalf("blocked snapshot produced an output: %v", statErr)
	}
	assertNoSnapshotArtifacts(t, dir)
}

// TestSnapshotImmutableSidecarAppearingDuringBuildBlocksPublication
// pins the bind_current sidecar proof (Rust verify_path's
// require_sidecar_absent inside BasicSource::final_check): a .readers
// sidecar appearing after the immutable open but before the final check
// refuses the publication with the RecoveryCandidateChanged class even
// though the main-file identity never changed. The sidecar is injected
// from the first cancellation checkpoint, which runs after the source
// open and before the attempt creation, so the appearance window is
// guaranteed. The public SnapshotTo cannot inject a side effect into
// the checkpoint, so the test drives the machine directly, like the
// live-race suite.
func TestSnapshotImmutableSidecarAppearingDuringBuildBlocksPublication(t *testing.T) {
	requirePublicationSecurity(t)
	dir := t.TempDir()
	source := filepath.Join(dir, "source.iprdb")
	destination := filepath.Join(dir, "output.iprdb")
	if err := copyFixture(t, "direct-ipv4.iprdb", source); err != nil {
		t.Fatal("copy fixture:", err)
	}

	injected := false
	_, failure := snapshot.To(source, snapshot.SourceImmutable, destination, PolicyFailIfExists, &snapshot.Budget{MaxHeapBytes: 16 << 20, MaxOutputPages: 100_000, MaxOpenFiles: 2}, func() error {
		if !injected {
			injected = true
			if err := os.WriteFile(snapshotSidecar(source), []byte("readers"), 0o644); err != nil {
				return err
			}
		}
		return nil
	})
	if failure == nil {
		t.Fatalf("sidecar-appearing snapshot succeeded, want recovery candidate changed")
	}
	if code := internalCode(t, failure.Cause); code != format.CodeRecoveryCandidateChanged {
		t.Fatalf("cause code = %v, want recovery candidate changed (err %v)", code, failure.Cause)
	}
	if _, statErr := os.Lstat(destination); !os.IsNotExist(statErr) {
		t.Fatalf("blocked snapshot produced an output: %v", statErr)
	}
	assertNoSnapshotArtifacts(t, dir)
}

// buildLargeDirectSource constructs one sealed 20k-range direct database
// directly at path through the one-shot writer (the published-file
// format, no Publish needed): the snapshot source race needs a copy that
// outlasts the controller poll.
func buildLargeDirectSource(t *testing.T, path string, ranges int) {
	t.Helper()
	spec, err := writer.FreshOutputSpec(format.AddressFamilyIPv4, format.ValueKindDirect, format.StructureKindNone, mustTag(t, "race-source").Wire(), 0)
	if err != nil {
		t.Fatal("spec:", err)
	}
	builder, err := writer.NewOutputBuilder(path, spec, writer.OutputBudget{MaxOutputPages: 1 << 16}, 0, nil)
	if err != nil {
		t.Fatal("builder:", err)
	}
	for index := 0; index < ranges; index++ {
		address := uint32(index * 2)
		if err := builder.PushDirectV4(address, address, uint32(index%251+1)); err != nil {
			t.Fatal("push:", err)
		}
	}
	if err := builder.Finish(); err != nil {
		t.Fatal("finish:", err)
	}
	if err := builder.Close(); err != nil {
		t.Fatal("close:", err)
	}
}

// TestSnapshotTinyHeapMembershipPublishesWithBatchDisabled pins the
// Rust floor_power_of_two zero guard end-to-end: a snapshot heap under
// one reference-batch slot pair (32 bytes) disables both batches with no
// charge, so a membership source without metadata still publishes and a
// structured source whose metadata exceeds the heap refuses with
// InsufficientResourceBudget. Before the zero guard the charge wrapped
// the heap arithmetic and bypassed the metadata bound (review P1).
func TestSnapshotTinyHeapMembershipPublishesWithBatchDisabled(t *testing.T) {
	requirePublicationSecurity(t)
	destination := snapshotDest(t, "tiny-membership.iprdb")
	result, err := SnapshotTo(fixture(t, "membership-ipv4.iprdb"), SnapshotSourceImmutable, destination, PolicyFailIfExists, &SnapshotBudget{MaxHeapBytes: 16, MaxOutputPages: 100_000, MaxOpenFiles: 2}, nil)
	if err != nil {
		t.Fatal("tiny-heap membership snapshot:", err)
	}
	if result.Publication.Publication != PublicationPublished || result.CleanupState() != CleanupStateClean {
		t.Fatalf("publication = %+v", result.Publication)
	}
	output := openPublished(t, destination)
	defer output.Close()
	outputInfo, err := output.Info()
	if err != nil {
		t.Fatal("output info:", err)
	}
	source := openPublic(t, "membership-ipv4.iprdb")
	defer source.Close()
	sourceInfo, err := source.Info()
	if err != nil {
		t.Fatal("source info:", err)
	}
	if outputInfo.RangeRecordCount != sourceInfo.RangeRecordCount || outputInfo.ActiveFeedCount != sourceInfo.ActiveFeedCount {
		t.Errorf("content diverged: ranges %d/%d feeds %d/%d", outputInfo.RangeRecordCount, sourceInfo.RangeRecordCount, outputInfo.ActiveFeedCount, sourceInfo.ActiveFeedCount)
	}
	assertNoSnapshotArtifacts(t, snapshotDir(destination))
}

// TestSnapshotTinyHeapStructuredMetadataRefused pins the metadata heap
// bound under the tiny heap: the structured fixture's 87-byte metadata
// cannot fit a 16-byte heap after the (disabled) batches, so the copy
// refuses before publication, while the metadata-free sibling publishes.
func TestSnapshotTinyHeapStructuredMetadataRefused(t *testing.T) {
	requirePublicationSecurity(t)
	destination := snapshotDest(t, "tiny-structured.iprdb")
	_, err := SnapshotTo(fixture(t, "structured-ipv4.iprdb"), SnapshotSourceImmutable, destination, PolicyFailIfExists, &SnapshotBudget{MaxHeapBytes: 16, MaxOutputPages: 100_000, MaxOpenFiles: 2}, nil)
	if code := failureCode(t, err); code != ErrorInsufficientResourceBudget {
		t.Fatalf("cause code = %v, want insufficient resource budget (err %v)", code, err)
	}
	if _, statErr := os.Lstat(destination); !os.IsNotExist(statErr) {
		t.Fatalf("refused snapshot produced an output: %v", statErr)
	}
	assertNoSnapshotArtifacts(t, snapshotDir(destination))

	destination = snapshotDest(t, "tiny-structured-nothreat.iprdb")
	result, err := SnapshotTo(fixture(t, "structured-ipv4-nothreat.iprdb"), SnapshotSourceImmutable, destination, PolicyFailIfExists, &SnapshotBudget{MaxHeapBytes: 16, MaxOutputPages: 100_000, MaxOpenFiles: 2}, nil)
	if err != nil {
		t.Fatal("tiny-heap nothreat snapshot:", err)
	}
	if result.Publication.Publication != PublicationPublished || result.CleanupState() != CleanupStateClean {
		t.Fatalf("publication = %+v", result.Publication)
	}
	output := openPublished(t, destination)
	defer output.Close()
	outputInfo, err := output.Info()
	if err != nil {
		t.Fatal("output info:", err)
	}
	source := openPublic(t, "structured-ipv4-nothreat.iprdb")
	defer source.Close()
	sourceInfo, err := source.Info()
	if err != nil {
		t.Fatal("source info:", err)
	}
	if outputInfo.RangeRecordCount != sourceInfo.RangeRecordCount {
		t.Errorf("content diverged: ranges %d/%d", outputInfo.RangeRecordCount, sourceInfo.RangeRecordCount)
	}
}

// TestSnapshotLiveMembershipPreservesNamesIndexesBitmapsAndMetadata
// ports the Rust live_membership_snapshot_preserves_names_indexes_
// bitmaps_and_metadata test: one live membership pair snapshot through
// the live source coordinator, then the published output proves the
// database identity, the generation, the feed indexes, the membership
// bitmap, and the metadata of the pinned generation, exactly like the
// immutable source.
func TestSnapshotLiveMembershipPreservesNamesIndexesBitmapsAndMetadata(t *testing.T) {
	requireLiveCreation(t)
	if !exchangeAvailable() {
		t.Skip("live coordination is not implemented on this platform")
	}
	source := createLiveMembershipPair(t, 2)
	live, err := OpenLiveReader(source, nil)
	if err != nil {
		t.Fatal("live reader:", err)
	}
	sourceInfo, err := live.Info()
	if err != nil {
		t.Fatal("live info:", err)
	}
	first, found, err := live.LookupFeed("first")
	if err != nil || !found {
		t.Fatalf("feed first: found=%v err=%v", found, err)
	}
	if _, err := live.Close(); err != nil {
		t.Fatal("live close:", err)
	}

	destination := snapshotDest(t, "live-membership-snapshot.iprdb")
	result, err := SnapshotTo(source, SnapshotSourceLive, destination, PolicyFailIfExists, snapshotBudget(3), nil)
	if err != nil {
		t.Fatal("live snapshot:", err)
	}
	if result.Publication.Publication != PublicationPublished || result.CleanupState() != CleanupStateClean {
		t.Fatalf("publication = %+v", result.Publication)
	}

	output := openPublished(t, destination)
	defer output.Close()
	outputInfo, err := output.Info()
	if err != nil {
		t.Fatal("output info:", err)
	}
	if outputInfo.DatabaseID != sourceInfo.DatabaseID {
		t.Errorf("database id diverged: %x != %x", outputInfo.DatabaseID, sourceInfo.DatabaseID)
	}
	if outputInfo.TransactionID != sourceInfo.TransactionID {
		t.Errorf("transaction diverged: %d != %d", outputInfo.TransactionID, sourceInfo.TransactionID)
	}
	outputFirst, found, err := output.LookupFeed("first")
	if err != nil || !found || outputFirst.Index != first.Index {
		t.Errorf("feed first preserved: index=%d found=%v err=%v (want %d)", outputFirst.Index, found, err, first.Index)
	}
	pin, err := output.Pin()
	if err != nil {
		t.Fatal("pin:", err)
	}
	defer pin.Close()
	probe, found, err := pin.LookupMembershipV4(parseV4("0.0.0.6"))
	if err != nil || !found {
		t.Fatalf("probe membership: found=%v err=%v", found, err)
	}
	has, err := probe.ContainsIndex(first.Index)
	if err != nil {
		t.Fatal("contains:", err)
	}
	if !has {
		t.Errorf("probe bitmap lost feed %d", first.Index)
	}
	outputMeta, outputOK, err := output.MetadataJSON()
	if err != nil {
		t.Fatal("output metadata:", err)
	}
	if !outputOK || string(outputMeta) != "{}" {
		t.Errorf("live membership snapshot preserved metadata = (%q, %v), want (\"{}\", true)", outputMeta, outputOK)
	}
	assertNoSnapshotArtifacts(t, snapshotDir(destination))
}

// TestSnapshotLiveRequiresSidecarDescriptorBudget ports the Rust
// live_snapshot_requires_the_sidecar_descriptor_budget test: the live
// source costs a third descriptor (the coordination artifact), so the
// two-file budget refuses before any source or destination work, with
// no output and no private artifacts.
func TestSnapshotLiveRequiresSidecarDescriptorBudget(t *testing.T) {
	requireLiveCreation(t)
	if !exchangeAvailable() {
		t.Skip("live coordination is not implemented on this platform")
	}
	source, _ := createLivePublicPair(t, 2)
	destination := snapshotDest(t, "live-budget.iprdb")
	_, err := SnapshotTo(source, SnapshotSourceLive, destination, PolicyFailIfExists, snapshotBudget(2), nil)
	if code := failureCode(t, err); code != ErrorInsufficientResourceBudget {
		t.Fatalf("cause code = %v, want insufficient resource budget (err %v)", code, err)
	}
	if _, statErr := os.Lstat(destination); !os.IsNotExist(statErr) {
		t.Fatalf("refused snapshot produced an output: %v", statErr)
	}
	assertNoSnapshotArtifacts(t, snapshotDir(destination))
}

// TestSnapshotLiveCannotReplaceItsOwnSourcePath ports the live arm of
// the Rust replacement_requires_an_existing_destination_and_rejects_
// live_self test: a live snapshot that would replace its own source path
// is refused with InvalidArgument after the source open and before the
// destination create, the source bytes stay untouched, and no private
// artifact appears.
func TestSnapshotLiveCannotReplaceItsOwnSourcePath(t *testing.T) {
	requireLiveCreation(t)
	if !exchangeAvailable() {
		t.Skip("live coordination is not implemented on this platform")
	}
	source, _ := createLivePublicPair(t, 2)
	before, err := os.ReadFile(source)
	if err != nil {
		t.Fatal("read source:", err)
	}
	_, err = SnapshotTo(source, SnapshotSourceLive, source, supportedSnapshotReplacement(), snapshotBudget(3), nil)
	if code := failureCode(t, err); code != ErrorInvalidArgument {
		t.Fatalf("cause code = %v, want invalid argument (err %v)", code, err)
	}
	after, err := os.ReadFile(source)
	if err != nil {
		t.Fatal("read source after:", err)
	}
	if !bytes.Equal(before, after) {
		t.Error("live source bytes changed by the refused self-replacement")
	}
	assertNoSnapshotArtifacts(t, snapshotDir(source))
}

// TestSnapshotLiveRejectsInvalidDestinationNames pins the Rust
// Destination::bind name rules on the live self-replacement probe: a
// destination without a usable main name (empty, "." or "..") refuses
// with NameInvalid before any filesystem access (Rust path.file_name()
// returns None for these, mapping to InvalidName).
func TestSnapshotLiveRejectsInvalidDestinationNames(t *testing.T) {
	requireLiveCreation(t)
	if !exchangeAvailable() {
		t.Skip("live coordination is not implemented on this platform")
	}
	source, _ := createLivePublicPair(t, 2)
	for _, destination := range []string{"", ".", ".."} {
		_, err := SnapshotTo(source, SnapshotSourceLive, destination, supportedSnapshotReplacement(), snapshotBudget(3), nil)
		if code := failureCode(t, err); code != ErrorNameInvalid {
			t.Fatalf("destination %q cause code = %v, want name invalid (err %v)", destination, code, err)
		}
	}
}

// TestSnapshotLiveRejectsHardLinkedDestination pins the Rust
// regular_identity single-link rule on the live self-replacement probe:
// a destination with more than one link refuses with Conflict before
// any attempt is created (Rust open_regular require_single_link ->
// LinkCount "publication inode link count changed").
func TestSnapshotLiveRejectsHardLinkedDestination(t *testing.T) {
	requireLiveCreation(t)
	if !exchangeAvailable() {
		t.Skip("live coordination is not implemented on this platform")
	}
	source, _ := createLivePublicPair(t, 2)
	dir := t.TempDir()
	other := filepath.Join(dir, "other.iprdb")
	if err := os.WriteFile(other, []byte("other"), 0o644); err != nil {
		t.Fatal("write other:", err)
	}
	destination := filepath.Join(dir, "dest.iprdb")
	if err := os.Link(other, destination); err != nil {
		t.Fatal("hard link:", err)
	}
	_, err := SnapshotTo(source, SnapshotSourceLive, destination, supportedSnapshotReplacement(), snapshotBudget(3), nil)
	if code := failureCode(t, err); code != ErrorConflict {
		t.Fatalf("cause code = %v, want conflict (err %v)", code, err)
	}
	assertNoSnapshotArtifacts(t, dir)
}
