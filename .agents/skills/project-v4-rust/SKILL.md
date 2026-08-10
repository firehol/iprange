---
name: project-v4-rust
description: Verify, benchmark, review, or change the Rust v4 database engine, its Rust-provided C ABI, and the shared v4 conformance corpus. Use for work under v4/rust or v4/conformance, Rust v4 portability and resource claims, generated ABI checks, or update-ipsets suitability evidence.
---

# Verify Rust v4

Keep Rust v4 work inside the approved exact-v4 contract and prove claims with
the compiled implementation. Do not infer current behavior from obsolete source
files, historical SOW claims, or unapproved future work.

## Establish the boundary

1. Read the sole active SOW under `.agents/sow/current/`.
2. Read `.agents/sow/specs/binary-format-v4.md` and
   `.agents/sow/specs/design-iprange-engine.md`. Also read
   `.agents/sow/specs/c-abi-v4.md` when the C boundary is affected.
3. Inspect `git status --short`. Preserve unrelated changes and stage files
   explicitly.
4. Keep Phase 1 unsigned and exact. Do not restore v3, add signing, start Go,
   add another physical layout, or invent semantics beyond the current specs.
5. Keep validation explicit. Ordinary open and read paths must not perform a
   whole-file validation.

## Prove the compiled implementation

Every Rust source must belong to at least one supported Cargo compiler graph or
the one exact native fixture compiled at test runtime. Run the repository gate:

```bash
./v4/rust/check-source-graph.sh
```

The gate uses fresh Cargo dependency files for Linux, Windows, macOS, and
FreeBSD, denies compiler warnings and dead-code suppression, and compares the
normalized union with the repository source inventory. Never treat a file's
presence, a test name, or an old SOW claim as proof that production compiles or
calls it. Never delete a file without user approval and a preservation commit.

## Run the Rust gates

Use these baseline gates:

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

The architecture gate enforces one healthy-reader owner, one healthy-writer
owner, a separate untrusted validation/recovery boundary, the Rust-owned C
adapter, one publication namespace owner, and a separate mapped sidecar owner.
Do not weaken its patterns to permit a new physical-state bypass. When adding a
persistent operation, put it in the existing owning layer or update the active
SOW's ownership inventory before implementation.

Treat the implementation as two authority levels:

- Public semantic adapters own Rust/C handles, typed workflows, logical
  sequencing, named scopes, joins, algebra, cancellation, and result
  translation. They must not inspect a mapping, metadata page, root, page
  number, allocator, raw membership ID, bitmap width, dictionary hash/refcount,
  or persistent record.
- The private engine owns mapped byte access, canonical codecs, fixed-tree
  query/cursor/mutation, ordered range construction, COW allocation,
  retirement, sealing, and selected-generation read/write cores.

`ReaderCore::read()` is the healthy selected-generation read capability.
`WriterCore` over `DraftStore` is the healthy mapped mutation capability.
`database_file` owns main-file mapping/bootstrap/empty construction;
`mapped_bytes`, `page_io`, and the typed codec modules own physical access;
`immutable_output` owns canonical mapped snapshot/recovery construction.
Validation and recovery may inspect damaged mappings independently, but they
must consume those codecs and builders rather than redefine v4 bytes. A new
high-level physical import or a second field offset/record encoder is an
architecture defect even when tests pass.

The storage gate is architectural, not stylistic. Production SDK code must use
file-backed mappings for persistent content; it must not issue positional or
buffered content-I/O calls, own complete database-page images, or retain an
application page cache. Lifecycle calls such as open, metadata, resize,
mapping, flush, file synchronization, locking, rename, and unlink remain
necessary. The Linux runtime gate must also prove representative database,
sidecar, snapshot, recovery, publication, reservation, and scratch paths use
mappings without persistent-content transfer syscalls. Do not weaken either
gate to make a failure green.

## Prove portable byte order

Run the focused Rust codec vectors under Miri's s390x target:

```bash
cargo +nightly miri test --manifest-path v4/rust/Cargo.toml \
  --target s390x-unknown-linux-gnu -p iprange-livedb \
  --no-default-features --lib big_endian_portable_
```

Each selected test must drive the authoritative production field or record
encoder and compare with fixed literal bytes; a second test-only encoder or a
symmetric round trip is not evidence. This gate proves the portable byte
codecs only. Miri cannot prove the SDK's wall-clock, file-locking, mmap,
process, or durability behavior; keep those claims on the native platform
matrix and do not describe this gate as full reader/writer parity.

Explicit validation, candidate inspection, recovery, and retained-cleanup retry
require the exact `iprange-v4-worker` executable beside the consuming process
executable. There is no `PATH` search or environment override. The one exact
secondary candidate supports Cargo integration tests whose executable is under
a directory literally named `deps`; it is the worker in that directory's
parent and remains build-ID checked. Verify that the Cargo package contains the
worker, application packaging installs it adjacent to the application, the
build-ID handshake rejects mismatches, and missing/mismatched workers fail
before source scanning or destination mutation.

Use a different `CARGO_TARGET_DIR` for each toolchain. Reusing one directory
between current Rust and Rust 1.74.1 can produce incompatible cached metadata:

```bash
CARGO_TARGET_DIR=/tmp/iprange-v4-msrv \
  cargo +1.74.1 test --manifest-path v4/rust/Cargo.toml \
  --workspace --all-features --all-targets
```

For a C ABI change, additionally prove:

- generated header and manifest equal the committed files;
- all 158 frozen symbols are exported exactly;
- the header compiles as C11 and C++17;
- all native C behavior programs pass;
- numeric constants, layouts, callbacks, ownership, and error contracts did not
  drift.

## Maintain conformance evidence

Keep normal conformance tests read-only. They must:

- open committed snapshots with the public reader;
- invoke explicit full validation;
- compare declared semantics independently;
- reject missing, extra, stale, malformed, or duplicate corpus entries.

Fixture regeneration must remain an explicit ignored test. Generate through the
public live writer and `snapshot_to`; do not add a test-only encoder. Until the
Go port exists, describe the corpus as Rust-first and never claim
bidirectional cross-language proof.

## Prove bounded update-ipsets workloads

Build the exact fault worker in the benchmark profile, then run the public-SDK
benchmark:

```bash
cargo build --manifest-path v4/rust/Cargo.toml \
  --profile bench -p iprange-livedb --bin iprange-v4-worker
cargo bench --manifest-path v4/rust/Cargo.toml --bench update_ipsets -- smoke
cargo bench --manifest-path v4/rust/Cargo.toml --bench update_ipsets -- scale
cargo bench --manifest-path v4/rust/Cargo.toml --bench update_ipsets -- local
cargo bench --manifest-path v4/rust/Cargo.toml --bench update_ipsets -- ci
cargo bench --manifest-path v4/rust/Cargo.toml --bench component_floors -- suite
```

`smoke` and `scale` are single-pass investigation matrices. `local` is the
performance acceptance runner: one untimed warm-up and five isolated samples
per scale case, with machine/compiler/profile metadata, distribution output,
resource accounting, and repeated semantic-result checks. Run it only after
profiles find no remaining actionable waste. `ci` runs three samples of a
representative subset against the committed baseline. Its roughly 2x limits
plus absolute noise allowance detect disasters; they do not establish local
optimality. Component floors classify mapped access, page search/build,
checksum, digest, and durability costs but are not full-operation targets.

Record elapsed time, records per second, counted allocations and bytes, peak
RSS, open file descriptors, logical and physical file size, page counts, and
temporary residue. Check scaling, not only one result. Ordinary unordered
ingestion and snapshot construction must not use an external sorting file.
The acceptance matrix includes one million direct replacements, first-seen and
last-seen inputs, nested arrival-order overwrites, exact feed inputs with 421
feeds, membership imports with 421 feeds, immutable unordered construction,
multi-window history projection, named point matches, overlap aggregation,
both provider joins, global algebra analysis/publication, the complete
publisher-shaped workflow, and snapshot ranges. It also includes one million
real live and immutable direct lookups, membership lookups, direct cursor
outputs, and named-feed cursor outputs.
For feed normalization, additionally measure one million ascending disjoint,
descending disjoint, deterministic random disjoint, and deterministic random
overlap-chain inputs both as the first feed in a new file and as a second feed
in that same file. Verify the exact final range count and feed count, enumerate
the result, and run explicit validation outside the timer. The first-feed timer
includes empty-file creation through committed close; the second-feed timer
excludes construction of the already committed first feed and includes reopen
through committed close for the second.
Reader evidence must name live and immutable readers separately and time at
least one million actual operations. Keep database construction, compact
snapshot construction, open, close, and explicit validation outside the timed
operation. A Linux reader-performance change must also preserve inherited
handle rejection with the real fork subprocess test and prove that the
supported `MADV_WIPEONFORK` path performs no process-ID call in repeated
ownership checks.

Use the test-only necessary-work snapshot to assert deterministic tree lookups,
page visits/copies/splits/sealing, range input/output passes, catalog and
membership work, mapping changes, and durability calls. These counters are
proof machinery, not a public observability API. A final release build must have
no `iprange_livedb::work` symbol, counter field string, counter storage, or
counter call left in the benchmark executable.

For a candidate-complete performance claim, profile the exact timed regions of
unordered immutable construction, timestamp refresh, representative query/join,
algebra publication, and the complete publisher-shaped workflow with frame
pointers. Classify every dominant cost against a required semantic, mapped-page
access, tree descent, COW edit, output build, commit seal, or durability action.
Implicit validation, pre-commit checksum, per-record allocation, repeated
lookup/delete, page reconstruction, cache churn, and an extra comparison/source
pass are findings, not acceptable overhead.

Replay authorized public feed data through a separate mapped source harness at
median, p99, and largest observed shapes. Include parsing, creation, commit,
and close in the replay timer, and time explicit validation separately. Record
only aggregate input/canonical counts, timings, and profile attribution; never
record source names, paths, literal ranges, or operational configuration.

Audit production source separately from tests. Review file/function size and
cyclomatic complexity as signals; do not create helper chains merely to lower a
metric. Run exact-clone detection at a meaningful threshold and inspect every
clone manually. Public ABI/typestate shapes and distinct damaged-input policies
may legitimately resemble each other; duplicated persistent layout, traversal,
mutation, allocation, retirement, or construction may not. Warning-denied
four-target compilation plus the source graph is the dead-code gate—never hide
an unwired source with `allow(dead_code)`.

## Report proof precisely

- Cross-compilation proves compilation only. Run native platform tests only
  with explicit authorization.
- Native Windows C-boundary proof includes `tests/native_header.rs` and
  `tests/native_windows.rs`; neither test may be replaced by cross-compilation.
  The supported Windows compiler graph is `x86_64-pc-windows-gnu`; select that
  toolchain explicitly rather than relying on a host's default MSVC toolchain,
  and use a separate Cargo target directory so their artifacts cannot mix.
- State the exact FreeBSD boundary: immutable reading and publication can be
  supported while live reader/writer coordination remains unsupported. The
  permanent native proof is `tests/freebsd_boundary.rs`; the live-only
  update-ipsets benchmark is intentionally a no-op on FreeBSD.
- Search for the same failure class before declaring a repair complete.
- Update the active SOW and every affected spec, SDK document, project skill,
  and `AGENTS.md` entry.
- Separate facts from working theories. A non-reproduced transient failure is
  not proof of a defect or proof of correctness.
- Do not declare Rust accepted or start the Go port before the user accepts the
  final Rust evidence.
