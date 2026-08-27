# iprange v4 conformance corpus

The normative wire contract is
[`../../.agents/sow/specs/binary-format-v4.md`](../../.agents/sow/specs/binary-format-v4.md).
The former unreleased and incompatible experimental fixtures were deleted; they
are not compatibility inputs.

## Corpus

`cases.json` is the language-neutral semantic manifest. The corpus contains
thirteen compact immutable snapshots produced through the public writers of
both implementations. The Rust-produced files come from the public Rust live
writer plus `snapshot_to`; the Go-produced files come from the public Go
`CreateLive`/`OpenLiveWriter` transactions and the public live `SnapshotTo`
(the generator stages every fixture and snapshots the closed live pair exactly
like the Rust generator):

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
  Go public `LiveWriter.ProjectHistory` workflow from 1000 singleton last-seen
  points, projected through three last-seen feeds (cutoffs 9/10/11 over
  `last_seen = 10 + index % 3`), pinning the Rust `one_source_pass` vector and
  window semantics in both readers;
- `go/membership-ipv4.iprdb`: the same 70-feed delete-and-reuse membership
  coverage as the Rust membership IPv4 fixture, produced by the Go
  `BeginMembershipTransaction` workflow (feed-005 deleted, its index reused,
  Replace over 10.0.0.0/24 and 10.0.1.0/24 with the 10.0.1.0-127 Union),
  pinning the member-import semantics in both readers;
- `go/membership-ipv6.iprdb`: the same whole-space global + special union over
  `2001:db8::` through `2001:db8::ffff` (the first 64 KiB of the /64) and the
  1 MiB repeated-byte metadata packet as the Rust membership IPv6 fixture,
  produced by the Go `BeginMembershipTransaction` workflow;
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
verifies both producer sets (Rust conformance opens all thirteen files; the Go
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
- Mixed live cooperation runs the same live database across languages in
  both directions: generation read-back (registration/release, sidecar
  replacement across generations, transition/reservation states, and
  publication inspection), writer exclusion, reclamation waiting for
  pinned readers plus stale-slot release (oldest-reader safety), the
  canonical commit-resolution attempt set (committed, same-transaction
  different-nonce, superseded-unknown), and compact-snapshot cross-open
  through each reader. The children are explicit entry points of the other
  language's test binary, built at test time; the battery is env-gated
  and linux/amd64-only so plain suites stay fast:
  ```bash
  IPRANGE_V4_MIXED_LIVE=1 nice go -C v4/go test . -run TestMixedLiveRustChild -v
  IPRANGE_V4_MIXED_LIVE=1 nice cargo test --manifest-path v4/rust/Cargo.toml --test mixed_live -- --nocapture
  ```
  Both commands need both toolchains; each skips with a message when the
  other toolchain is missing. The Go parent builds the Rust test binary
  with `cargo test --no-run` (incremental), the Rust parent builds the
  Go test binary with `go test -c` (incremental).
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
