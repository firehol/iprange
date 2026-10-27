# SOW-0039 - Filter normalized immutable feed ranges before publication

## Status

Status: completed

Sub-state: validated and approved for local signed checkpoint under decision35A.

## Requirements

Add a generic, fallible post-normalization filter to immutable feed creation so the ingester can enforce its approved output policy without constructing another database. Preserve existing APIs and their behavior. Keep policy out of SDK code and retain the single mapped normalization/output owner.

## Pre-Implementation Gate

Status: ready

Problem and evidence: public create_immutable_feed_v4/v6 normalize privately and publish immediately (src/immutable_feed.rs); immutable_output/unordered.rs write_normalized streams canonical ranges to the mapped builder. Filtering source records cannot detect a union becoming a full address space. A second publication would duplicate construction, I/O and cleanup.

Affected surfaces: additive Rust API, immutable builder filtering/report counts, docs/spec and tests. No wire, C ABI, Go, CLI parser, live database, service or packaging change.

Existing owner: retain immutable_output/unordered for normalization and mapped construction, immutable_feed for orchestration, publication for durability and failure cleanup. Callback sees only logical AddressRange values, not physical database state.

Design approved by 26A: add create_immutable_feed_v4_filtered/v6_filtered with a caller-owned FnMut(&AddressRange<K>) -> Result<bool>. Visit each maximal normalized range once in ascending order before output. False drops the entire range, errors abort private preparation without replacing the destination. No splitting/mutation of ranges. Callback can durably record application counters; the SDK does not roll back those external effects. Existing entrypoints use an always-keep closure. Report input count remains original input count; normalized interval count and addresses describe retained output.

Risks and consequences: lazily intern membership only for a retained range so all-filtered output is canonical; cancellation cadence tracks visited, not retained, ranges. Preserve empty active feeds and IPv6 full-space cardinality. Do not catch callback panics or claim rollback of caller side effects. Memory/budget/publication ownership remains unchanged.

Implementation plan:
1. Thread the generic predicate through existing construction; add public wrappers/reexports and documentation.
2. Add public API tests for normalization order, union full-space, singleton zero, legitimate zero-containing ranges, all/none filtering, IPv6, callback error and cancellation preserving an existing destination.
3. Run focused immutable-feed tests, architecture and format/lint checks; run supported source graph/native SDK gates before closure. Bound transient jobs to2CPUs/8GiB/10minutes; source graph may take several minutes and is run once for the API change.

Sensitive data: synthetic numeric ranges only; no feed bodies, private URLs or credentials in artifacts. No new dependencies or toolchain installs.

Artifact impact: Rust SDK README and design spec describe callback sequencing and result semantics; SOW records evidence; root project memory records consumer use. C ABI/spec, operator skills, deployment and snapshot signing unaffected.

Reference: firehol/iprange @8a013ee7, v4/rust/iprange-livedb/src/immutable_feed.rs and immutable_output/unordered.rs. Consumer update-ipsets @4188609, M2 SOW-0129. Decisions20A/26A control application policy and placement, not wire semantics.

Open decisions: none for this additive SDK hook. User supplied generic hook choice; signatures and callback plumbing are routine implementation within that approved boundary.

## Validation

Implementation and required native/source-graph/storage gates pass, with evidence below. Local checkpoint and lifecycle closure approved under decision35A.

## Outcome

Validated implementation approved for the local signed checkpoint under decision35A.

### Validation progress — 2026-10-26

-10 public immutable-feed tests pass, including explicit validation of filtered empty files, full IPv6 cardinality, and cancellation across5000rejected ranges. Existing unordered construction work/allocation assertion passes. Architecture gate passes.
- Storage scan fails on pre-existing cfg(test) GuardStore associated page-array types at free_bitmap/mutation.rs:424–425, verified identical at HEAD8a013ee7. No production page-copy was introduced; scanner source selection does not strip this inline test module. Gate remains reported failed; no unrelated scanner/test change included.
- Initial library test filter immutable_output_tests selected zero tests; corrected to the actual unordered_feed_construction test and verified one test ran. Do not count the empty run as evidence.

- Four-target source-graph gate passed: 530 sources; native Linux, Windows GNU, Apple ARM and FreeBSD. Consumer policy/recovery regression passed, including abandoned private-output cleanup through public SDK maintenance.

### Consumer dependency alignment — 2026-10-26

User requires latest stable ingester pins, verified against the live crates.io API. Cargo cannot resolve SDK exact flate2 1.1.9 alongside consumer 1.1.10, or same-major serde_json exact pins. Align flate2 to1.1.10 and workspace serde_json pins to1.0.154; both retain SDK Rust1.74 support (dependency minima1.67/1.71). Update the two SDK lockfile entries without broadly upgrading unrelated SDK dependencies. SDK sha2 0.10.9 remains separate from consumer0.11.1 because semver-incompatible majors coexist and latest sha2 requires Rust1.88; no SDK MSRV change is authorized or required for consumer resolution. Run compression/JSON-relevant SDK checks and consumer regressions.
- Aligned dependency validation:12 metadata::tests tests passed, including compressor allocation budget, maximum payload round-trip, corruption and gzip rejection. Initial metadata_tests filter matched zero and is not evidence. Stable1.97 only; SDK Rust1.74 minimum was not compiler-tested, and miniz_oxide0.9.1 has no declared rust-version.

### Required storage-gate repair

The previously reported failure is caused solely by an existing cfg(test) GuardStore in free_bitmap/mutation.rs:412 onward; HEAD has identical page-array associated types. check-mmap-storage.sh:49 deliberately excludes *_tests.rs, but does not parse inline cfg(test) sections. To finish the required gate without weakening scanner patterns or changing storage behavior, move this unchanged test module body into free_bitmap/mutation_guard_tests.rs and retain a cfg(test) path module declaration. This is a test-layout correction required by the existing validation contract, not a storage/API design change. Verify exact dedented body equality, run the guard test, storage/architecture gates and source-graph inventory. No file deletion, production code change, compiler or dependency change.

- Relocation verification passed: exact dedented test-module body equality; the free-bit self/ancestor/limit guard test still executes and passes. mmap-only source gate now passes344 production files with unchanged scanner patterns. Four-target source graph is rerunning for the new test-file inventory.

- Four-target source-graph rerun passed531 sources including the relocated test (Linux, WindowsGNU, AppleARM, FreeBSD). Existing storage scanner unchanged and passing; guard test passing. Independent read-only review confirmed byte-identical dedented test relocation.

## Proposed local checkpoint paths

Decision35A authorizes the following explicit paths; move this SOW
from current to done and mark completed in the same approved checkpoint. No push or
deployment is included. Unrelated untracked files are excluded.

- `.agents/sow/current/SOW-0039-20261026-normalized-feed-filter.md` → `.agents/sow/done/SOW-0039-20261026-normalized-feed-filter.md`
- `.agents/sow/specs/design-iprange-engine.md`
- `v4/rust/Cargo.lock`
- `v4/rust/README.md`
- `v4/rust/iprange-capi/Cargo.toml`
- `v4/rust/iprange-cli/Cargo.toml`
- `v4/rust/iprange-livedb/Cargo.toml`
- `v4/rust/iprange-livedb/src/free_bitmap/mutation.rs`
- `v4/rust/iprange-livedb/src/free_bitmap/mutation_guard_tests.rs`
- `v4/rust/iprange-livedb/src/immutable_feed.rs`
- `v4/rust/iprange-livedb/src/immutable_output/unordered.rs`
- `v4/rust/iprange-livedb/src/immutable_output_tests.rs`
- `v4/rust/iprange-livedb/src/lib.rs`
- `v4/rust/iprange-livedb/tests/immutable_feed.rs`

## Closure — 2026-10-27

The user approved35A. Implementation, validation and independent final review passed.
This completed SOW and its approved artifacts are recorded together in the local signed
checkpoint. No push or deployment is authorized.
