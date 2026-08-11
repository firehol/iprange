# iprange v4 Rust SDK

This workspace contains the Rust implementation of the exact unsigned Phase-1
v4 database and its Rust-provided C ABI. It is unreleased and has not yet passed
the user's final acceptance gate. The post-acceptance Go port is outside this
milestone.

Only the current v4 contract is supported. There is no v3 compatibility,
importer, exporter, or alternate v4 layout. Snapshot signing is separate
Phase-2 work.

## Components

- `iprange-livedb` is the native Rust SDK and the one format implementation.
- `iprange-capi` is a thin C boundary over that Rust engine.
- [`../conformance/`](../conformance/) contains the current Rust-produced
  semantic corpus.
- [`binary-format-v4.md`](../../.agents/sow/specs/binary-format-v4.md) defines
  the wire and durability contract.
- [`design-iprange-engine.md`](../../.agents/sow/specs/design-iprange-engine.md)
  defines the product and SDK architecture.
- [`c-abi-v4.md`](../../.agents/sow/specs/c-abi-v4.md) defines the frozen C
  boundary.

## Internal ownership

Healthy selected-generation reads have one owner (`ReaderCore`). Healthy COW
mutation, allocation, retirement, page sealing, and committed-generation
publication in the main file have one owner (`WriterCore` over `DraftStore`).
Exact workflows, advanced transactions, and the C adapter sequence logical
operations over those owners; they cannot access mappings, roots, pages, or
allocators directly. Validation and recovery retain a separate untrusted
mapped-inspection boundary and reuse the canonical codecs and mapped output
builders. Sidecar coordination and filesystem namespace/publication are
separate persistent concerns with separate owners.

[`check-architecture.sh`](check-architecture.sh) enforces those dependency
directions. The final 2026-08-11 inventory counts implementation files under
the two library `src/` trees while excluding dedicated files and directories
whose names contain `test`. It contains 335 files and 96,226 newline-counted
source lines; Lizard reports 87,313 code lines across 5,183 functions.
Functions average 14.0 code lines and cyclomatic complexity 3.5. The largest is
a 191-line recovery-attempt state machine. Fifty-four files exceed the
directional 500-line target, while the largest file has 950 lines and no file
reaches 1,000.

At a 15-line/100-token threshold, exact clone detection finds 11 production
shapes totaling 254 lines (about 0.29%). They are C adapter forms, typed
workflow/cursor wrappers, reader facades, codec trait adapters, and distinct
damaged-input policies—not duplicate persistent-format operations. The audit
did find copied membership/structured recovery construction and ID-table code;
that code was consolidated before these final figures. These measurements do
not prove every line is intrinsically required; they make the remaining size
and review boundary explicit.

## Database model

There is one main-file format with two explicit lifecycle modes:

- A live database uses the main file plus a local `<filename>.readers`
  coordination sidecar. The sidecar is never distributed.
- An immutable snapshot contains the same database bytes without a sidecar.

Each file has one address family, one value kind, a random database identity,
and a value tag containing up to 15 non-NUL bytes plus its required NUL.

- `direct` ranges contain opaque caller-defined `u32` values. Exact
  `first_seen` and `last_seen` tags select their specialized timestamp-refresh
  workflows; every other direct tag remains generic.
- `membership` ranges refer to canonical in-file feed combinations. The SDK
  owns feed indexes, bitmap combinations, and membership IDs; callers use feed
  names and generation-bound references.
- `structured` ranges refer to SDK-owned fixed records selected by one
  `StructureKind` for the whole file. The current
  `NetworkEnrichmentV1` record contains ASN, country/state/city IDs, optional
  signed microdegree coordinates, and a reference to the existing canonical
  threat-membership dictionary. Callers receive typed values and never submit
  raw structure or membership IDs.

Structured storage has one common manager for ID allocation, exact interning,
hash collisions, refcounts, COW mutation, retirement, validation, recovery, and
snapshot rebuilding. Each hardcoded structure has an independent codec module
that alone owns its fields, offsets, canonical checks, and typed translation.
Adding another enum value therefore adds a codec and typed adapter, not another
storage manager or runtime schema.

The optional file-level metadata is one opaque payload intended to contain a
JSON object. The engine reads and writes the exact bytes but does not parse or
normalize JSON. The uncompressed limit is 20 MiB (20,971,520 bytes); the
committed representation is compressed.

## Writer behavior

The writer accepts unordered, duplicate, adjacent, and overlapping ranges and
normalizes them directly into destination COW pages. Direct assignments apply
in arrival order: a later range overwrites only the addresses it covers.
Ordinary ingestion never presorts input and never creates an external sorting
file.

The public Rust SDK exposes:

- advanced direct and membership transactions;
- typed structured transactions, including SDK-owned profile interning,
  threat-feed composition, arrival-order IPv4/IPv6 assignment, and clearing;
- named-feed create, replace, rename, and delete workflows;
- complete direct-map replacement;
- exact first-seen refresh, where continuously present addresses keep their
  original timestamp, new addresses receive the refresh timestamp, and absent
  addresses are removed;
- exact last-seen refresh, where current addresses receive the monotonic refresh
  timestamp, recent absence is retained above a cutoff, and old absence expires;
- name-based multi-feed membership import;
- mapped named-feed sources that chain one v4 reader directly into create,
  replace, timestamp, and output workflows;
- one-pass unordered immutable single-feed construction in its final inode;
- one-pass projection of one last-seen map into every requested history feed;
- named membership scopes, point matching, cardinality/pair aggregation, and
  ordered direct or membership provider joins;
- same-name global multi-file count and comparison;
- union, intersection, and exclusion published directly as immutable v4 with
  preserved feeds or one flat feed;
- one optional metadata replacement in the same committed generation;
- compact immutable snapshot construction.

An operation either publishes its complete draft or aborts it. A commit reports
`NotCommitted`, `Committed`, or `OutcomeUnknown` with exact resolution
evidence. Normal replacement and timestamp refreshes deliberately have no best-effort
flag because a read failure cannot safely mean deletion.

Normal ingestion, import, commit, queries, and snapshot construction use no
external scratch files. Only explicit validation or recovery graph work may
use caller-authorized bounded scratch space. A private snapshot output is the
prospective final database, not sorting scratch.

## Open, validation, and recovery

Opening is explicit through immutable-reader, live-reader, and live-writer
constructors. Open performs only the checks required for safe bounded access;
it does not scan or checksum the complete database.

Full validation is an explicit operation. Recovery is also explicit: it
inspects bounded candidates and returns typed evidence so the caller can choose
whether and how to revive data. Ordinary readers and writers do not repair,
reset, initialize, validate, or switch modes automatically.

Validation, candidate inspection, recovery, and retained-cleanup retry use the
SDK's isolated fault worker. Install the exact platform executable name
`iprange-v4-worker` beside the final application executable. The SDK does not
search `PATH` or accept an environment override. A build-ID handshake rejects a
missing or different worker before source scanning or destination mutation.
The worker is built and included by the `iprange-livedb` Cargo package; an
application installer must install both executables together. The only
secondary lookup is Cargo's exact integration-test layout: a process under a
directory literally named `deps` may use the worker in that directory's parent.
Applications must not depend on that test convention.

Invalid content is a completed `ValidationResult`; an operational failure is a
`ValidationFailure` with partial progress, cleanup facts, and any retained
source-cleanup guard. When that guard is present, the caller must retry its
cleanup before discarding the obligation.

## Build and verify

The minimum supported Rust version is 1.74.

```bash
./v4/rust/check-architecture.sh
./v4/rust/check-mmap-storage.sh
./v4/rust/check-mmap-runtime.sh
./v4/rust/check-source-graph.sh
cargo test --manifest-path v4/rust/Cargo.toml \
  --workspace --all-features --all-targets
cargo test --manifest-path v4/rust/Cargo.toml \
  --workspace --no-default-features --all-targets
cargo clippy --manifest-path v4/rust/Cargo.toml \
  --workspace --all-features --all-targets -- -D warnings
cargo fmt --manifest-path v4/rust/Cargo.toml --all -- --check
RUSTDOCFLAGS='-D warnings' cargo doc --manifest-path v4/rust/Cargo.toml \
  --workspace --all-features --no-deps
```

The static storage gate rejects persistent-content I/O and owned production
page images. The Linux runtime gate traces representative database, sidecar,
snapshot, recovery, publication, reservation, and scratch paths and requires
mapped access with zero persistent-content transfer syscalls. The source-graph
gate cross-checks every Rust source against fresh Linux, Windows, macOS, and
FreeBSD compiler dependency graphs. It also rejects compiler warnings and
dead-code suppression. The native C panic shim is the one explicit exception
because its integration test compiles it directly at runtime.

Use a separate target directory for Rust 1.74.1 so Cargo does not reuse
incompatible current-toolchain metadata:

```bash
CARGO_TARGET_DIR=/tmp/iprange-v4-msrv \
  cargo +1.74.1 test --manifest-path v4/rust/Cargo.toml \
  --workspace --all-features --all-targets
```

Normal conformance tests are read-only and explicitly validate every committed
fixture. Fixture regeneration is an ignored, manually selected test:

```bash
cargo test --manifest-path v4/rust/Cargo.toml \
  -p iprange-livedb --test conformance -- --nocapture
```

The corpus is Rust-first evidence. Bidirectional Rust/Go cross-open becomes
mandatory only after the accepted Rust result is ported independently to Go.

## C ABI

The committed boundary has 168 generation-1 functions. Its generated public
artifacts are:

- [`iprange_v4.h`](iprange-capi/include/iprange_v4.h)
- [`iprange_v4_abi1_manifest.json`](iprange-capi/include/iprange_v4_abi1_manifest.json)

The Rust tests regenerate and compare both artifacts, inspect exact shared
library exports, compile all layouts as C11 and C++17, and run eight native C
behavior programs.

## Measured publisher workloads

Build the fault worker in the same optimized profile, then run the desired
matrix:

```bash
cargo build --manifest-path v4/rust/Cargo.toml \
  --profile bench -p iprange-livedb --bin iprange-v4-worker
cargo bench --manifest-path v4/rust/Cargo.toml \
  --bench update_ipsets -- smoke
cargo bench --manifest-path v4/rust/Cargo.toml \
  --bench update_ipsets -- scale
cargo bench --manifest-path v4/rust/Cargo.toml \
  --bench update_ipsets -- local
cargo bench --manifest-path v4/rust/Cargo.toml \
  --bench update_ipsets -- ci
cargo bench --manifest-path v4/rust/Cargo.toml \
  --bench component_floors -- suite
```

`local` runs one untimed warm-up and five isolated child-process samples for
the full matrix. It records compiler, CPU, profile, fixture identity, result
shape, allocations, RSS, descriptors, and file size; repeated samples must
produce the same semantic result. `ci` runs three samples of a representative
subset against the committed baseline. Its limits are twice the local median
plus an absolute noise allowance, so it is a disaster gate rather than the
performance acceptance authority.

The preserved 2026-08-10 baseline used a generic optimized build with Rust
1.91.1 on x86_64 Linux and an Intel i9-12900K. The accepted baseline and the
independent final repeat both use one warm-up and five isolated samples. All 79
final-repeat cases stayed within the deliberately loose limits.

| Scenario | Work | Accepted median | Final-repeat median |
|---|---:|---:|---:|
| Direct replacement | 1,000,000 ranges | 0.262 s | 0.260 s |
| First-seen refresh | 1,000,000 ranges | 0.337 s | 0.337 s |
| Last-seen refresh | 1,000,000 ranges | 0.343 s | 0.350 s |
| IPv6 direct replacement | 1,000,000 ranges | 0.511 s | 0.501 s |
| Nested arrival-order overwrite | 1,000,000 ranges | 0.315 s | 0.292 s |
| Exact feed replacement, 421 feeds | 1,000,000 ranges | 0.326 s | 0.308 s |
| Membership import, 421 feeds | 1,000,000 ranges | 0.0414 s | 0.0407 s |
| Compact snapshot | 1,000,000 ranges | 0.0427 s | 0.0456 s |
| Seven-window history projection | 1,000,000 ranges | 0.0697 s | 0.0755 s |
| Point-match name enumeration | 4,000,000 names | 0.710 s | 0.622 s |
| Feed cardinalities | 1,000,000 ranges, 64 feeds | 0.0292 s | 0.0291 s |
| Direct-provider join | 1,000,000 ranges, 421 feeds | 0.0975 s | 0.103 s |
| Membership-provider join | 1,000,000 ranges, 421 feeds | 0.0989 s | 0.104 s |
| Global algebra count | 2,000,000 inputs, 421 feeds | 0.131 s | 0.130 s |
| Preserve-feed algebra publication | 2,000,000 inputs | 0.252 s | 0.222 s |
| Complete publisher-shaped workflow | 13,600,000 work units | 1.217 s | 1.102 s |

The complete workflow combines construction, both timestamp refreshes,
central/history updates, aggregation, both provider joins, algebra, and final
enumeration. It is not one primitive's latency.

Mapped readers are materially faster than writers. The random point cases
build a one-million-range tree and issue one million deterministically shuffled
queries. The scan cases enumerate one million records through bounded cursors.

| Reader operation | Live accepted median | Immutable accepted median |
|---|---:|---:|
| Random direct point lookup | 0.223 s | 0.202 s |
| Random membership point lookup, 421 feeds | 0.307 s | 0.234 s |
| Direct ordered scan | 0.00673 s | 0.00627 s |
| Named-feed ordered scan, 421 feeds | 0.00808 s | 0.00795 s |

The structured-value A/B used one pinned i9-12900K performance core, one warm-up,
and five isolated samples. Point cases contain one million real ranges and run
ten million deterministically shuffled queries; scans enumerate one million
ranges. Timed reader paths allocate nothing.

| Structured operation | Work | Median | Rate |
|---|---:|---:|---:|
| Live scalar lookup | 10,000,000 queries | 4.517 s | 2.214 M/s |
| Immutable scalar lookup | 10,000,000 queries | 3.396 s | 2.945 M/s |
| Live scalar + selected threat check | 10,000,000 queries | 5.477 s | 1.826 M/s |
| Immutable scalar + selected threat check | 10,000,000 queries | 4.412 s | 2.267 M/s |
| Live typed scan | 1,000,000 ranges | 0.0490 s | 20.408 M/s |
| Immutable typed scan | 1,000,000 ranges | 0.0471 s | 21.246 M/s |
| Random structured construction | 1,000,000 ranges | 0.925 s | 1.081 M/s |
| Profile interning | 65,536 profiles | 0.0848 s | 0.773 M/s |
| Random assignment after interning | 1,000,000 ranges | 0.818 s | 1.223 M/s |
| Commit of an already-built draft | 1,000,000 ranges | 0.0358 s | 27.964 M/s |

The equivalent immutable design using separate ASN, Geo, and threat files took
9.409 s for the same ten million combined answers (1.063 M/s). One structured
file is therefore 2.77 times faster in this controlled shape. The compact
immutable structured file is 23,457,792 bytes; the live file is 39,534,592
bytes. Construction uses 865 bounded setup allocations and 3,420,976 bytes at
100,000 and one million ranges; the allocation count is constant, and timed
interning/assignment/read paths allocate nothing. Commit uses four allocations
totaling 48 bytes.

The original fixed-tree structure index produced 1.852 M immutable scalar
lookups/s. Replacing only that index with the retained direct ID table produced
2.945 M/s. In the final profile, required range-tree descent accounts for
43.73% of samples; direct structure location is 1.35%, typed decode is 4.38%,
and error-drop glue is 1.05%. Necessary-work tests prove one range lookup, no
membership lookup for scalar-only access, one lazy membership lookup when
requested, no metadata work, and no implicit validation. This does not prove a
mathematical hardware maximum; it means the audit found no actionable
structure-manager work left in the measured hot path.

Explicit whole-file validation is separate from open and normal access. One
million direct ranges validate in 0.0103 s, one million membership ranges with
421 feeds in 0.0145 s, and the equivalent immutable direct snapshot in
0.00823 s. A commit that seals one million already-built direct ranges takes
0.00730 s median; page checksums are commit work, not ingestion work.

The feed-construction matrix also measures the input shapes that exercise the
normalizer's edge and arbitrary-location paths. A first-feed timer includes
empty-file creation, writer open, feed creation, one million inputs, finish,
commit, and close. A second-feed timer creates and commits the first feed
outside the timer, then reopens the same file and measures creation of the
second feed. Result enumeration and explicit validation are outside both
timers.

| One-million-range input | First feed accepted median | Second feed accepted median | Final ranges |
|---|---:|---:|---:|
| Ascending disjoint | 0.0368 s | 0.0683 s | 1,000,000 |
| Descending disjoint | 0.129 s | 0.155 s | 1,000,000 |
| Deterministic random disjoint | 0.361 s | 0.382 s | 1,000,000 |
| Deterministic random overlap chain | 0.275 s | 0.278 s | 1 |

The overlap chain deterministically permutes intervals whose neighbors overlap
by half; it is one defined stress shape, not a claim about every random overlap
distribution. Ordered first-feed cases use 39 fixed setup allocations totaling
1,135-1,148 bytes; ordered second-feed cases use 21 totaling 462-466 bytes.
Random and overlap cases add one lazy, operation-private scalar locator capped
at 256 KiB. It stores only first keys and page numbers, never mapped content.
All eight cases keep file descriptors stable, leave no private artifact, and
build pages directly in the mapped destination without a sorting file.

All scale cases keep file descriptors stable, leave zero private artifacts,
and explicitly validate every output after timing. Direct, first-seen,
last-seen, nested, feed-replacement, import, and snapshot construction make
22/22/22/22/23/20/31 fixed setup allocations totaling about
263/263/263/263/263/0.5/0.9 KiB. Those counts and bounded bytes are constant
rather than per range. Timed lookup and scan paths allocate nothing.

The component kernels measure physical floors without v4 semantics. Final
seven-run medians were 0.327 ms to scan one million mapped 8-byte records, 24.5
ms for one million binary searches in a 512-key mapped page, 4.41 ms to build
64 MiB of mapped pages, 5.57 ms to CRC32C 64 MiB, 80.3 ms to SHA-512 64 MiB,
and 23.5 ms to dirty, flush, and sync 64 MiB. They classify profile costs; no
full SDK operation is expected to equal a component floor.

A read-only inventory of the authorized public update-ipsets corpus found
1,457 feed artifacts and about 48.2 million source lines. A separate mapped
source harness included parsing, creation, commit, and close. The median-ranked
shape had 168 parsed records, normalized to 148 ranges, and completed in about
1.53 ms. The p99-ranked shape had 318,373 records, normalized to 261,610, and
completed in about 47.8 ms. The largest shape had 22,637,111 records,
normalized to 3,094,652, and completed in a 1.079 s three-run median; separate
validation took 27.2 ms. Its profile spent about 62% in the harness parser and
batching, while no SDK function reached 5%, so the SDK was not the limiting
component of that replay. Names, paths, and literal ranges are intentionally
not recorded.

Readers access mapped tree pages directly. Independent point queries restart at
the mapped root, while ordered cursors retain their bounded traversal state.
There is no application page cache and no positional page transfer. Peak RSS
includes mapped pages faulted into the process and fixture construction; it is
not an engine-owned heap measurement. These are one-machine baselines, not
portable timing guarantees.

Live handles automatically reject inherited use after `fork`. On supported
Linux kernels this ownership check reads a private `MADV_WIPEONFORK` control
mapping and performs no per-operation process-ID syscall. Unsupported kernels
and other platforms retain the process-ID fallback.

The current flat update-ipsets set remains faster for simple lookup and scan,
but it does not provide COW publication, live readers, direct values, named
feeds, timestamp refreshes, recovery, or portable snapshots. The Rust SDK proves
the required publisher primitives and bounded resource shape; final product
acceptance and consumer integration remain separate gates.

## Platform evidence

- Linux: native tests, process/crash tests, C/C++ callers, AddressSanitizer, and
  Valgrind have passed.
- macOS: both native feature matrices, process/crash tests, SIGBUS chaining,
  live lifecycle, publication, conformance, validation/recovery, Rust C-boundary
  tests, and C11/C++17 header checks pass on Apple ARM64.
- Windows with the GNU Rust target: both native feature matrices, mapped-reader
  tail retention/reuse, live lifecycle/crash resolution,
  publication/housekeeping, conformance, validation/recovery, C11/C++17 header
  checks, and an external C caller using the Windows calling convention and a
  non-ASCII UTF-16 path pass on local NTFS.
- FreeBSD 14: both native feature matrices pass for immutable reading,
  validation/recovery, and durable fail-if-exists/no-rollback publication.
  Strict replacement rejects before destination mutation. Live coordination is
  explicitly unsupported and every live entry returns error code 44 before path
  access or artifact mutation.

Cross-compilation is compilation evidence only. It is not native platform
proof.
