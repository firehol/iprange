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

## Database model

There is one main-file format with two explicit operating modes:

- A live database uses the main file plus a local `<filename>.readers`
  coordination sidecar. The sidecar is never distributed.
- An immutable snapshot contains the same database bytes without a sidecar.

Each file has one address family, one value kind, a random database identity,
and a value tag containing up to 15 non-NUL bytes plus its required NUL.

- `direct` ranges contain opaque caller-defined `u32` values. The exact
  `retention` tag enables the separate retention-refresh workflow.
- `membership` ranges refer to canonical in-file feed combinations. The SDK
  owns feed indexes, bitmap combinations, and membership IDs; callers use feed
  names and generation-bound references.

The optional file-level metadata is one opaque payload intended to contain a
JSON object. The engine reads and writes the exact bytes but does not parse or
normalize JSON. The uncompressed limit is 1 MiB; the committed representation
is compressed.

## Writer behavior

The writer accepts unordered, duplicate, adjacent, and overlapping ranges and
normalizes them directly into destination COW pages. Direct assignments apply
in arrival order: a later range overwrites only the addresses it covers.
Ordinary ingestion never presorts input and never creates an external sorting
file.

The public Rust SDK exposes:

- advanced direct and membership transactions;
- named-feed create, replace, rename, and delete workflows;
- complete direct-map replacement;
- exact retention refresh, where surviving addresses keep their original
  value, new addresses receive the new refresh value, and missing addresses are
  removed;
- name-based multi-feed membership import;
- one optional metadata replacement in the same committed generation;
- compact immutable snapshot construction.

An operation either publishes its complete draft or aborts it. A commit reports
`NotCommitted`, `Committed`, or `OutcomeUnknown` with exact resolution
evidence. Normal replacement and retention deliberately have no best-effort
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

The committed boundary has 136 generation-1 functions. Its generated public
artifacts are:

- [`iprange_v4.h`](iprange-capi/include/iprange_v4.h)
- [`iprange_v4_abi1_manifest.json`](iprange-capi/include/iprange_v4_abi1_manifest.json)

The Rust tests regenerate and compare both artifacts, inspect exact shared
library exports, compile all layouts as C11 and C++17, and run five native C
behavior programs.

## Measured publisher workloads

Run the small matrix routinely and the production-shaped matrix explicitly:

```bash
cargo bench --manifest-path v4/rust/Cargo.toml \
  --bench update_ipsets -- smoke
cargo bench --manifest-path v4/rust/Cargo.toml \
  --bench update_ipsets -- scale
```

Five local Linux runs on 2026-08-07, each pinned to one performance core,
produced these medians and observed ranges. Each case processes at least one
million inputs or timed reader operations; setup, snapshot construction, open,
close, and explicit validation are outside the reader timer.

| Scenario | Work | Median | Observed range | Median rate |
|---|---:|---:|---:|---:|
| Direct replacement, dispersed input | 1,000,000 ranges | 0.481 s | 0.448-0.762 s | 2.08 million/s |
| Retention refresh | 1,000,000 ranges | 0.475 s | 0.449-0.549 s | 2.11 million/s |
| Compact snapshot | 1,000,000 ranges | 0.0665 s | 0.0551-0.0806 s | 15.03 million/s |
| Live direct point lookup | 1,000,000 lookups over 100,000 ranges | 0.0801 s | 0.0797-0.0822 s | 12.48 million/s |
| Immutable direct point lookup | 1,000,000 lookups over 100,000 ranges | 0.0641 s | 0.0618-0.0960 s | 15.59 million/s |
| Live membership point check, 421 feeds | 1,000,000 checks over 100,000 ranges | 0.1398 s | 0.1385-0.1509 s | 7.15 million/s |
| Immutable membership point check, 421 feeds | 1,000,000 checks over 100,000 ranges | 0.1280 s | 0.1270-0.1449 s | 7.81 million/s |
| Live direct ordered scan | 1,000,000 ranges | 0.00928 s | 0.00595-0.01152 s | 107.80 million/s |
| Immutable direct ordered scan | 1,000,000 ranges | 0.00505 s | 0.00480-0.00650 s | 198.19 million/s |
| Live named-feed ordered scan, 421 feeds | 1,000,000 ranges | 0.00793 s | 0.00782-0.01575 s | 126.05 million/s |
| Immutable named-feed ordered scan, 421 feeds | 1,000,000 ranges | 0.00667 s | 0.00622-0.00830 s | 149.99 million/s |

All scale cases kept file descriptors stable, left zero private artifacts, and
explicitly validated every output after timing. The final complete run counted
21 allocations totalling 402 bytes for direct replacement, 21 allocations
totalling 414 bytes for retention, and 31 allocations totalling 935 bytes for
snapshot construction; those counts are constant rather than per range. The
timed lookup and scan paths allocate nothing.

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
feeds, retention refresh, recovery, or portable snapshots. The Rust SDK proves
the required publisher primitives and bounded resource shape; final product
acceptance and consumer integration remain separate gates.

## Platform evidence

- Linux: native tests, process/crash tests, C/C++ callers, AddressSanitizer, and
  Valgrind have passed.
- macOS: both native feature matrices, process/crash tests, SIGBUS chaining,
  live lifecycle, publication, conformance, validation/recovery, Rust C-boundary
  tests, and C11/C++17 header checks pass on Apple ARM64.
- Windows: both native feature matrices, mapped-reader tail retention/reuse,
  live lifecycle/crash resolution, publication/housekeeping, conformance,
  validation/recovery, C11/C++17 header checks, and an external C caller using
  the Windows calling convention and a non-ASCII UTF-16 path pass on local NTFS.
- FreeBSD 14: both native feature matrices pass for immutable reading,
  validation/recovery, and durable fail-if-exists/no-rollback publication.
  Strict replacement rejects before destination mutation. Live coordination is
  explicitly unsupported and every live entry returns error code 44 before path
  access or artifact mutation.

Cross-compilation is compilation evidence only. It is not native platform
proof.
