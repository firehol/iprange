package iprangedb

// Go-produced conformance fixture generation (SOW-0025 chunk-6 design
// record D5, extended by slice C): each Go fixture uses the exact op
// sequence of the matching Rust generator, with producer-tagged metadata
// where the Rust generator writes text, so both readers cross-open them.
// Membership (history projection) and structured (network_enrichment_v1)
// fixtures are Go-produced since slices B and C; recovery fixtures stay
// Rust-only until the Go recovery milestone.
//
// The test is env-gated (IPRANGE_V4_GO_REGENERATE_FIXTURES=1), mirroring
// the Rust #[ignore] regeneration entry point: a normal suite run never
// writes into the committed corpus.

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// goFixturePath resolves one Go-produced fixture path under the shared
// conformance corpus.
func goFixturePath(name string) string {
	return filepath.Join("..", "conformance", "go", name)
}

// copyFile is the test-only publish helper: the staged file may live on a
// different device than the corpus, so publication copies next to the
// target and renames over it (Rust generate.rs publish parity).
func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o644)
}

// regenDirectIPv4 writes direct-ipv4.iprdb into dir with the exact op
// sequence of the Rust direct_ipv4 generator (generate.rs:175): the four
// assigns and one clear coalesce into the same four canonical ranges, and
// the metadata is producer-tagged so the two producers stay
// distinguishable.
func regenDirectIPv4(t *testing.T, dir string) {
	t.Helper()
	tag, err := NewValueTag([]byte("asn"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "direct-ipv4.iprdb")
	_, err = Create(path, AddressFamilyIPv4, ValueKindDirect, StructureKindNone, tag)
	if err != nil {
		t.Fatal(err)
	}
	w, err := OpenWriter(path, DefaultBudget())
	if err != nil {
		t.Fatal(err)
	}
	tx, err := w.BeginDirect()
	if err != nil {
		t.Fatal(err)
	}
	ops := []struct {
		kind  string
		from  IPv4
		to    IPv4
		value uint32
	}{
		{"assign", IPv4(0x0a000014), IPv4(0x0a00001d), 1}, // 10.0.0.20-10.0.0.29
		{"assign", IPv4(0x0a00000a), IPv4(0x0a000019), 2}, // 10.0.0.10-10.0.0.25
		{"assign", IPv4(0x0a00000f), IPv4(0x0a000011), 3}, // 10.0.0.15-10.0.0.17
		{"clear", IPv4(0x0a000016), IPv4(0x0a00001b), 0},  // 10.0.0.22-10.0.0.27
		{"assign", IPv4(0x0a00001e), IPv4(0x0a00001f), 1}, // 10.0.0.30-10.0.0.31
	}
	for _, op := range ops {
		switch op.kind {
		case "assign":
			if changed, err := tx.AssignV4(op.from, op.to, op.value); err != nil || !changed {
				t.Fatalf("assign %v-%v = changed %v err %v", op.from, op.to, changed, err)
			}
		case "clear":
			if changed, err := tx.ClearV4(op.from, op.to); err != nil || !changed {
				t.Fatalf("clear %v-%v = changed %v err %v", op.from, op.to, changed, err)
			}
		}
	}
	if changed, err := tx.SetMetadataJSON([]byte(`{"fixture":"go-direct-ipv4","producer":"go"}`)); err != nil || !changed {
		t.Fatalf("metadata set = changed %v err %v", changed, err)
	}
	res, err := tx.Commit()
	if err != nil || res.Status != CommitCommitted {
		t.Fatalf("fixture commit = %+v err %v", res, err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
}

// regenFirstSeenIPv6 writes first-seen-ipv6.iprdb into dir: the whole
// IPv6 space assigned 1_700_000_000 with present-empty metadata, mirroring
// the Rust first_seen_ipv6 generator via the first-seen refresh workflow
// (generate.rs:199).
func regenFirstSeenIPv6(t *testing.T, dir string) {
	t.Helper()
	tag, err := NewValueTag([]byte("first_seen"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "first-seen-ipv6.iprdb")
	if _, err := Create(path, AddressFamilyIPv6, ValueKindDirect, StructureKindNone, tag); err != nil {
		t.Fatal(err)
	}
	w, err := OpenWriter(path, DefaultBudget())
	if err != nil {
		t.Fatal(err)
	}
	tx, err := w.BeginDirect()
	if err != nil {
		t.Fatal(err)
	}
	if changed, err := tx.AssignV6(IPv6{}, IPv6{Hi: ^uint64(0), Lo: ^uint64(0)}, 1_700_000_000); err != nil || !changed {
		t.Fatalf("whole-space v6 assign = changed %v err %v", changed, err)
	}
	if changed, err := tx.SetMetadataJSON([]byte{}); err != nil || !changed {
		t.Fatalf("empty metadata set = changed %v err %v", changed, err)
	}
	res, err := tx.Commit()
	if err != nil || res.Status != CommitCommitted {
		t.Fatalf("fixture commit = %+v err %v", res, err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
}

// regenHistoryMembershipIPv4 writes history-membership-ipv4.iprdb into
// dir: the Rust one_source_pass vector projected through the public
// Writer.ProjectHistory workflow into three last-seen feeds (cutoffs
// 9/10/11 over last_seen 10+index%3), so cutoffs keep 1000/666/333
// points. The destination is the committed projection itself - feed
// catalog, membership dictionary, and ranges all produced by the Go
// writer - and both readers cross-open it.
func regenHistoryMembershipIPv4(t *testing.T, dir string) {
	t.Helper()

	// The source: one fresh last_seen direct database with the exact
	// Rust ranges1000 vector (a temporary input, never a corpus file).
	sourcePath := filepath.Join(t.TempDir(), "history-source.iprdb")
	if _, err := Create(sourcePath, AddressFamilyIPv4, ValueKindDirect, StructureKindNone, ValueTagLastSeen()); err != nil {
		t.Fatal(err)
	}
	sourceWriter, err := OpenWriter(sourcePath, DefaultBudget())
	if err != nil {
		t.Fatal(err)
	}
	tx, err := sourceWriter.BeginDirect()
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 1000; index++ {
		address := uint32(index * 2)
		if changed, err := tx.AssignV4(IPv4(address), IPv4(address), 10+uint32(index%3)); err != nil || !changed {
			t.Fatalf("source assign %d = changed %v err %v", address, changed, err)
		}
	}
	res, err := tx.Commit()
	if err != nil || res.Status != CommitCommitted {
		t.Fatalf("source commit = %+v err %v", res, err)
	}
	if err := sourceWriter.Close(); err != nil {
		t.Fatal(err)
	}

	tag, err := NewValueTag([]byte("feeds"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "history-membership-ipv4.iprdb")
	if _, err := Create(path, AddressFamilyIPv4, ValueKindMembership, StructureKindNone, tag); err != nil {
		t.Fatal(err)
	}
	w, err := OpenWriter(path, DefaultBudget())
	if err != nil {
		t.Fatal(err)
	}
	source, err := OpenImmutable(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := w.ProjectHistory(HistoryProjectionSource{Kind: HistoryProjectionSourceImmutable, Reader: source}, []HistoryWindow{
		{FeedName: "one", Cutoff: 9},
		{FeedName: "two", Cutoff: 10},
		{FeedName: "three", Cutoff: 11},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !handle.IsChanged() {
		t.Fatal("history projection of 1000 new points is not changed")
	}
	report := handle.Report()
	if report.SourceRangeCount != 1000 || report.CreatedFeedCount != 3 {
		t.Fatalf("history report source=%d feeds=%d, want 1000/3", report.SourceRangeCount, report.CreatedFeedCount)
	}
	res, err = handle.Commit()
	if err != nil || res.Status != CommitCommitted {
		t.Fatalf("history commit = %+v err %v", res, err)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
}

// regenStructuredIPv4 writes structured-ipv4.iprdb into dir with the
// exact op sequence of the Rust structured_ipv4 generator
// (generate.rs:76): feeds botnet/scanner, two interned
// network_enrichment_v1 payloads linked to those memberships, the
// broad/narrow assigns, the clear, and text metadata.
func regenStructuredIPv4(t *testing.T, dir string) {
	t.Helper()
	tag, err := NewValueTag([]byte("enrichment"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "structured-ipv4.iprdb")
	if _, err := Create(path, AddressFamilyIPv4, ValueKindStructured, StructureKindNetworkEnrichmentV1, tag); err != nil {
		t.Fatal(err)
	}
	w, err := OpenWriter(path, DefaultBudget())
	if err != nil {
		t.Fatal(err)
	}
	tx, err := w.BeginStructuredTransaction(NewCancellationToken())
	if err != nil {
		t.Fatal(err)
	}
	botnet, err := tx.EnsureFeed(FeedName("botnet"))
	if err != nil {
		t.Fatal(err)
	}
	scanner, err := tx.EnsureFeed(FeedName("scanner"))
	if err != nil {
		t.Fatal(err)
	}
	empty, err := tx.EmptyMembership()
	if err != nil {
		t.Fatal(err)
	}
	botnetMembership, err := tx.AddFeed(empty, botnet)
	if err != nil {
		t.Fatal(err)
	}
	scannerMembership, err := tx.AddFeed(empty, scanner)
	if err != nil {
		t.Fatal(err)
	}
	broad, err := tx.InternNetworkEnrichmentV1(NetworkEnrichmentV1{
		ASN:         64512,
		CountryID:   1,
		StateID:     2,
		CityID:      3,
		Location:    NetworkEnrichmentV1Location{LatitudeMicrodegrees: 37_983_810, LongitudeMicrodegrees: 23_727_539},
		HasLocation: true,
	}, botnetMembership)
	if err != nil {
		t.Fatal(err)
	}
	narrow, err := tx.InternNetworkEnrichmentV1(NetworkEnrichmentV1{
		ASN:       64513,
		CountryID: 4,
		StateID:   5,
		CityID:    6,
	}, scannerMembership)
	if err != nil {
		t.Fatal(err)
	}
	if changed, err := tx.AssignV4(IPv4(0x0a010000), IPv4(0x0a0100ff), broad); err != nil || !changed {
		t.Fatalf("broad assign = changed %v err %v", changed, err)
	}
	if changed, err := tx.AssignV4(IPv4(0x0a010040), IPv4(0x0a01007f), narrow); err != nil || !changed {
		t.Fatalf("narrow assign = changed %v err %v", changed, err)
	}
	if changed, err := tx.ClearV4(IPv4(0x0a010064), IPv4(0x0a01006d)); err != nil || !changed {
		t.Fatalf("clear = changed %v err %v", changed, err)
	}
	if changed, err := tx.SetMetadataJSON([]byte(`{"fixture":"go-structured-ipv4","producer":"go"}`)); err != nil || !changed {
		t.Fatalf("metadata set = changed %v err %v", changed, err)
	}
	res, err := tx.Commit()
	if err != nil || res.Status != CommitCommitted {
		t.Fatalf("fixture commit = %+v err %v", res, err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
}

// regenStructuredIPv4NoThreat writes structured-ipv4-nothreat.iprdb into
// dir with the exact op sequence of the Rust structured_ipv4_nothreat
// generator (generate.rs:139): every interned enrichment carries
// membership id zero (feeds absent), pinning the canonical absence result
// in both readers, and no metadata is written.
func regenStructuredIPv4NoThreat(t *testing.T, dir string) {
	t.Helper()
	tag, err := NewValueTag([]byte("enrichment"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "structured-ipv4-nothreat.iprdb")
	if _, err := Create(path, AddressFamilyIPv4, ValueKindStructured, StructureKindNetworkEnrichmentV1, tag); err != nil {
		t.Fatal(err)
	}
	w, err := OpenWriter(path, DefaultBudget())
	if err != nil {
		t.Fatal(err)
	}
	tx, err := w.BeginStructuredTransaction(NewCancellationToken())
	if err != nil {
		t.Fatal(err)
	}
	plain, err := tx.InternNetworkEnrichmentV1(NetworkEnrichmentV1{
		ASN:         64514,
		CountryID:   7,
		StateID:     8,
		CityID:      9,
		Location:    NetworkEnrichmentV1Location{LatitudeMicrodegrees: 40_640_060, LongitudeMicrodegrees: 22_944_420},
		HasLocation: true,
	}, MembershipRef{})
	if err != nil {
		t.Fatal(err)
	}
	bare, err := tx.InternNetworkEnrichmentV1(NetworkEnrichmentV1{
		ASN:       64515,
		CountryID: 10,
		StateID:   11,
		CityID:    12,
	}, MembershipRef{})
	if err != nil {
		t.Fatal(err)
	}
	if changed, err := tx.AssignV4(IPv4(0x0a020000), IPv4(0x0a02007f), plain); err != nil || !changed {
		t.Fatalf("plain assign = changed %v err %v", changed, err)
	}
	if changed, err := tx.AssignV4(IPv4(0x0a020080), IPv4(0x0a0200ff), bare); err != nil || !changed {
		t.Fatalf("bare assign = changed %v err %v", changed, err)
	}
	res, err := tx.Commit()
	if err != nil || res.Status != CommitCommitted {
		t.Fatalf("fixture commit = %+v err %v", res, err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestRegenerateGoFixtures regenerates the five Go-produced fixtures with
// the Rust two-phase contract: generate all files into a staging corpus,
// verify the staging corpus with the exact same conformance suite in a
// subprocess, and only then publish each file next to its committed
// target and rename over it. A failed verification leaves the committed
// corpus untouched. Set IPRANGE_V4_GO_REGENERATE_FIXTURES=1 to run.
func TestRegenerateGoFixtures(t *testing.T) {
	if os.Getenv("IPRANGE_V4_GO_REGENERATE_FIXTURES") == "" {
		t.Skip("fixture regeneration is explicit: set IPRANGE_V4_GO_REGENERATE_FIXTURES=1")
	}
	corpus := filepath.Join("..", "conformance")
	staging := t.TempDir()

	// Stage: a full corpus copy with only the go/ fixtures replaced
	// (the inventory check in loadManifest requires the other six
	// files to be present and listed).
	for _, name := range []string{"cases.json"} {
		data, err := os.ReadFile(filepath.Join(corpus, name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(staging, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(staging, "rust"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{
		"rust/direct-ipv4.iprdb", "rust/first-seen-ipv6.iprdb",
		"rust/membership-ipv4.iprdb", "rust/membership-ipv6.iprdb",
		"rust/structured-ipv4.iprdb", "rust/structured-ipv4-nothreat.iprdb",
	} {
		if err := copyFile(filepath.Join(corpus, name), filepath.Join(staging, name)); err != nil {
			t.Fatal(err)
		}
	}
	goDir := filepath.Join(staging, "go")
	if err := os.MkdirAll(goDir, 0o755); err != nil {
		t.Fatal(err)
	}
	regenDirectIPv4(t, goDir)
	regenFirstSeenIPv6(t, goDir)
	regenHistoryMembershipIPv4(t, goDir)
	regenStructuredIPv4(t, goDir)
	regenStructuredIPv4NoThreat(t, goDir)

	// Verify: the staged corpus must pass the full conformance suite
	// (same binary, same test, corpus root redirected by env).
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, "-test.run=^TestConformanceRustFixtures$")
	cmd.Env = append(os.Environ(), "IPRANGE_V4_GO_CORPUS="+staging)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			t.Fatal("staged corpus verification timed out")
		}
		t.Fatalf("staged corpus verification failed: %v (committed corpus untouched)", err)
	}

	// Publish: copy next to the target, then rename over it. The target
	// file is replaced atomically only after verification passed.
	publish := func(name string) {
		t.Helper()
		stage := filepath.Join(goDir, name)
		target := filepath.Join(corpus, "go", name)
		replacement := target + ".replacement"
		if err := copyFile(stage, replacement); err != nil {
			t.Fatal(err)
		}
		// Windows rename refuses an existing destination; the committed
		// target must be removed first (Rust generate.rs publish parity,
		// which gates the same removal on cfg(windows)). On POSIX the
		// rename stays the atomic replace.
		if runtime.GOOS == "windows" {
			if _, err := os.Stat(target); err == nil {
				if err := os.Remove(target); err != nil {
					t.Fatal(err)
				}
			}
		}
		if err := os.Rename(replacement, target); err != nil {
			t.Fatal(err)
		}
	}
	publish("direct-ipv4.iprdb")
	publish("first-seen-ipv6.iprdb")
	publish("history-membership-ipv4.iprdb")
	publish("structured-ipv4.iprdb")
	publish("structured-ipv4-nothreat.iprdb")
}
