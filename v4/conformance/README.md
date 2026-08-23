# iprange v4 conformance corpus

The normative wire contract is
[`../../.agents/sow/specs/binary-format-v4.md`](../../.agents/sow/specs/binary-format-v4.md).
The former unreleased and incompatible experimental fixtures were deleted; they
are not compatibility inputs.

## Corpus

`cases.json` is the language-neutral semantic manifest. The corpus contains
eleven compact immutable snapshots produced through the public writers of both
implementations. The Rust-produced files come from the public Rust live writer
and public snapshot operation; the Go-produced files come from the public Go
writer (`v4/go` `Create`/`OpenWriter`/`BeginDirect`/`ProjectHistory`/`Commit`,
no snapshot operation yet - Go writes the compact main file directly):

- `rust/direct-ipv4.iprdb`: arrival-order direct assignments and clearing;
- `rust/first-seen-ipv6.iprdb`: full IPv6 first-seen coverage and empty metadata;
- `rust/membership-ipv4.iprdb`: 70 named feeds, index reuse, and memberships
  crossing the 64-bit boundary;
- `rust/membership-ipv6.iprdb`: full IPv6 membership and a 1 MiB uncompressed
  metadata payload (a 1039-byte compressed chain on disk);
- `rust/structured-ipv4.iprdb`: typed network enrichment, named threat feeds,
  arrival-order overwrites, clearing, lazy membership, and exact metadata;
- `rust/structured-ipv4-nothreat.iprdb`: structured values without threat
  feeds (membership id zero), pinning the canonical absence result in both
  readers;
- `go/direct-ipv4.iprdb`: the same direct IPv4 semantics produced by the Go
  writer;
- `go/first-seen-ipv6.iprdb`: the same first-seen IPv6 coverage produced by the
  Go writer; and
- `go/history-membership-ipv4.iprdb`: a membership destination produced by the
  Go public `Writer.ProjectHistory` workflow from 1000 singleton last-seen
  points, projected through three last-seen feeds (cutoffs 9/10/11 over
  `last_seen = 10 + index % 3`), pinning the Rust `one_source_pass` vector and
  window semantics in both readers;
- `go/structured-ipv4.iprdb`: the same typed network enrichment, named threat
  feeds, arrival-order overwrites, clearing, lazy membership, and exact
  metadata as the Rust structured fixture, produced by the Go structured
  transaction (`BeginStructuredTransaction`); and
- `go/structured-ipv4-nothreat.iprdb`: structured values without threat feeds
  (membership id zero) produced by the Go writer, pinning the canonical absence
  result in both readers.

The Rust test actually opens and explicitly validates every listed file. It
compares every direct or structured range, typed enrichment field, feed
name/index, resolved membership, named-feed projection, exact decompressed
metadata state and bytes, and exact 129-bit cardinality. It also performs true
point lookups and derives temporary wrong-magic, short, and unaligned inputs from
the valid corpus to prove their rejection. Normal tests never modify the
committed corpus.

Run the read-only proof:

```bash
nice cargo test --manifest-path v4/rust/Cargo.toml --test conformance
```

Regenerate only the Rust-produced files explicitly:

```bash
nice cargo test --manifest-path v4/rust/Cargo.toml   --test conformance regenerate_rust_fixtures -- --ignored --exact
```

Regeneration has no test-only wire encoder. It creates live files with the
public writer, builds compact snapshots with `snapshot_to`, verifies all staged
outputs against `cases.json`, and only then replaces the committed Rust files.

## Cross-language gate

Both producer sets are now committed, and each reader opens and semantically
verifies both producer sets (Rust conformance opens all eleven files; the Go
conformance inventory lists each fixture file with its producer and the Go
reader verifies both sets).

- Mixed subprocess smoke gates run the same cross-open verdicts from a
  fresh process: `v4/go/subprocess_cross_open_test.go` spawns the Go test
  binary as a child that opens `rust/direct-ipv4.iprdb`,
  `go/direct-ipv4.iprdb`, and `go/history-membership-ipv4.iprdb`, and runs
  one full create-commit-read-back roundtrip;
  `v4/rust/iprange-livedb/tests/mixed_subprocess.rs` spawns the Rust test
  binary as a child that opens `rust/direct-ipv4.iprdb`,
  `go/direct-ipv4.iprdb`, the Go history fixture, and both Go structured
  fixtures with `ImmutableReader`, checking the last-seen feed indexes, the
  1000-range tree, and one typed enrichment lookup per structured fixture.
  The full manifest verdicts (every range, metadata state, cardinality, and
  invalid mutation) are proven in-process by the Go and Rust conformance
  suites.
- Malformed transformations must produce equivalent public errors (the
  invalid-cases checks in both conformance suites cover this).
- Byte-identical files are not required. Page placement, mutable tree shape,
  membership IDs, and compressed bytes may differ when observable data is
  equal. At the cross-language gate, a missing required producer or skipped
  cross-open is a failure.

Regenerate the Go-produced files explicitly with the Go conformance test
(instead of a committed encoder, it creates a live file through the public
writer, verifies the staged output against `cases.json`, and only then
replaces the committed file):

```bash
cd v4/go && IPRANGE_V4_GO_REGENERATE_FIXTURES=1 nice go test . -run TestRegenerateGoFixtures
```

The Rust regeneration command above only replaces the Rust-produced files.
Mixed subprocess gates run with the normal suites:

```bash
nice go test . -run 'TestGoSubprocess' -v   # from v4/go
nice cargo test --manifest-path v4/rust/Cargo.toml --test mixed_subprocess
```

Reachable empty leaves are malformed. An empty logical tree uses root page zero.
Live reader coordination is external to the database and uses OS-held locks; it
does not store process tokens in the database or sidecar.

Snapshot signing is intentionally absent from Phase 1. Pending SOW-0017 adds
authenticated artifacts only after the unsigned SDK is accepted and measured.
