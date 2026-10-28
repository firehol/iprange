# SOW-0040 - Immutable provider construction

## Status

Status: completed

Sub-state: implemented, validated and independently reviewed; recorded with the
local signed implementation checkpoint. No push or deployment.

## Requirements and authorization

The user requested "fix the sdk" on 2026-10-28 after rejecting live construction
plus snapshot as an optimal immutable-provider path. The approved repair covers
IPv4 and IPv6 direct values and named multi-label memberships. Provider SOW-0130
retains decisions 36A (independent datasets and context), 37B (CAIDA most-specific
prefix precedence), and 39B (both address families).

Remove avoidable live-file, commit, sidecar and snapshot work without duplicate
storage algorithms, whole-provider heap materialization, a wire-format change,
weakened durability or ingester-side storage workarounds. Preserve the existing
single-feed APIs. Provider policy remains outside the generic SDK.

Local checkpointing follows the existing AGENTS.md SOW completion rule. No push,
deployment, dependency installation or production-service change is included.
UPDATE-IPSETS-V3.md section 2 item 9 exempts this work from REVIEWS.md reviewer
rounds; independent internal review and SDK ownership/storage gates still apply.

## Analysis

The public SDK previously exposed immutable single-feed construction only. Its
private immutable_output::Builder already supported direct values and both
address families. Live construction followed by snapshot first built and committed
a live tree, then scanned every range to build another file.

The repair composes existing range mutation, dictionary, mapped workspace,
canonical output and publication owners. Ordered input can validate/coalesce with
one pending range. Unordered input still requires normalization and a final range
pass; this work does not claim otherwise or promise universal optimality.

Initial historical-shape measurements on this server varied substantially:
100,000 direct replacement records had a 1.954-second median, and snapshot-only
had a 7.526-second median. They were separate workloads, not an A/B comparison;
waiting dominated their wall time. Acceptance instead uses contemporaneous,
equal-output comparisons with explicit output checks.

## Pre-Implementation Gate

Status: ready

Before implementation, the user-authorized boundary was recorded as:

- Add direct and named membership constructors for both families.
- Expose OrderedDisjoint and Unordered input modes.
- Ordered mode validates ascending/disjoint ranges and coalesces equal adjacency,
  with no normalization workspace.
- Unordered direct maps use callback-order last assignment; membership overlaps
  union names through existing mapped range/dictionary owners.
- Membership input supplies a caller-owned catalog and borrowed label sets.
  Internal IDs and bitmaps remain private.
- Metadata publishes with the map. Existing budget/result/publication contracts
  and cleanup semantics are reused.
- Preserve zero direct values, full IPv6 cardinality, and active empty feeds.
- Bound jobs to two CPUs, 8 GiB and ten minutes; use existing tools offline.

Risks investigated: precedence, dictionary lifetime/refcounts, mapped extents,
metadata budgets, cancellation, publication uncertainty and failure cleanup.
No new provider policy or application storage algorithm was authorized.

## Implementation

1. Added public create_immutable_direct_v4/v6 and
   create_immutable_membership_v4/v6, typed input order, borrowed named sources,
   and aliases for existing immutable budget/report/result types.
2. Kept the public adapter logical. immutable_output owns normalization and
   canonical writing; shared construction unmaps workspace before truncation.
3. Reused single-feed publication/cleanup orchestration instead of duplicating it.
4. Reserved mandatory membership bitmap scratch before optional reference batching.
   This fixes an implementation-review finding where an 8-byte budget worked but
   optional batching consumed all of a larger 32-byte budget.
5. Deferred dictionary collection until each entire range transformation finishes.
   A zero-delta marker retains unused operands for collection without redundant
   physical pin/unpin writes. Both buffered and spilled paths preserve zero deltas.
6. Added an optional exact ID-translation cache during final membership emission.
   It holds at most 1,024 scalar pairs (8 KiB), only after workspace IDs stabilize.
   Collisions fall back to interning. Its heap is released before metadata work.
7. Added public duplicate-catalog validation returning InvalidArgument, rather
   than exposing the trusted builder's internal-corruption error.
8. Added scalar-model tests, necessary-work proof, runtime mapped-storage probes,
   and permanent smoke/local/CI benchmark cases and baselines.

## Validation

### Correctness and compatibility

- Fourteen new immutable-map integration tests pass on the final implementation.
  They cover both families, ordered coalescing, direct zero/overwrite semantics,
  full IPv6 direct and membership cardinality, empty catalogs, active empty feeds,
  unknown/duplicate labels and catalogs, cancellation, source errors and exhaustion
  preserving a valid previous destination.
- Membership scalar models use 70 labels and 500 overlaps. A 2,000-input subset
  reclamation test succeeds with 24 workspace pages. Heap budgets of 8, 32, 64 and
  32,768 bytes succeed where sufficient; a one-slot translation cache exercises
  collisions without changing membership results.
- Explicit ImmutableCurrent validation passes on generated outputs.
- Existing ten immutable-feed tests pass after shared construction refactoring.
- Full SDK all-features library/integration tests pass after the refcount/cache
  repair. The workspace no-default-features library/integration matrix passes.
  Final focused all-features tests cover the later duplicate-catalog refinement.
- Debug C ABI/native fixture tests pass in the functional matrices. No C/Go API,
  wire layout, byte codec, dependency or lockfile was changed.
- Ordered direct necessary-work test: one source pass, 1,000 consumed records,
  zero tree lookups/descents, page copies/splits/retirements, membership interns,
  and no sidecar.

### Architecture, storage, portability and documentation

- Architecture gate passes, including the new public adapter.
- Static mmap gate passes over 348 production sources.
- Runtime mmap syscall gate passes for existing operations plus new unordered
  direct and overlapping named construction and reads: no persistent-content
  transfer syscalls observed. The probe opens published outputs because their
  construction mappings use private names before rename.
- Warning-denied source graph passes: 537 sources, four supported targets
  (Linux, Windows GNU, Apple ARM and FreeBSD), and one runtime-compiled fixture.
  Cross-compilation is not a claim of native foreign-platform execution.
- All 19 changed/new Rust files pass explicit formatting checks.
- SDK-specific warning-denied rustdoc passes.
- Existing ingester `cargo check --locked --offline --all-targets` passes.

### Measured construction cost

Optimized x86_64 Linux, Rust 1.97.1, AMD EPYC 7453, bounded to two CPUs.
One warm-up and five samples per case; 100,000 inputs; identical no-metadata,
fail-if-exists publication. Every direct range and membership result is compared
with an independent scalar model outside timing. Country cases have eight labels;
overlaps emit 199,999 normalized intervals. Output sizes match old/new.

| Shape | Live + snapshot median ms | New median ms | Old/new |
|---|---:|---:|---:|
| IPv4 direct ordered | 266.263 | 97.059 | 2.74x |
| IPv4 direct reverse/unordered | 229.564 | 164.992 | 1.39x |
| IPv4 membership ordered | 692.970 | 323.734 | 2.14x |
| IPv4 membership overlap | 1232.184 | 919.959 | 1.34x |
| IPv6 direct ordered | 306.112 | 118.067 | 2.59x |
| IPv6 direct reverse/unordered | 289.715 | 243.439 | 1.19x |
| IPv6 membership ordered | 742.633 | 302.263 | 2.46x |
| IPv6 membership overlap | 1343.176 | 980.085 | 1.37x |

An earlier overlapping-membership version was slightly slower than live+snapshot.
That finding led to removing redundant refcount writes and repeated output interning;
these final measurements supersede the earlier three-sample investigation estimates.

The optional cache adds heap, and not every workload uses less peak RSS. These
synthetic results establish the measured improvements and removal of identified
avoidable work, not absolute optimality or production throughput.

The 16 comparisons participate in smoke/local matrices; eight new constructors
also participate in CI. `providers` runs the focused five-sample acceptance matrix;
`providers-ci` runs three samples against saved 2x-median + 100 ms thresholds.
All eight thresholds and the expanded optimized smoke matrix pass. Historical
baseline rows remain unchanged; appended rows identify this host calibration.

### Limits and unsuccessful aggregate commands

These failures are recorded explicitly, not described as passed gates:

- Debug workspace all-targets reached its ten-minute limit in the harness-free
  structured-lookup benchmark after preceding test executables passed. Debug
  functional matrices and optimized benchmark smoke were therefore run separately.
  The component-floor debug smoke had passed before that timeout.
- An attempted entirely release-profile workspace test run failed the existing
  native C panic shim: it expects a debug_assertions-only probe. Debug C tests pass.
  This is why release workspace tests are not a substitute for debug functional
  tests plus optimized benchmark execution.
- Selecting an older benchmark against a rebuilt worker correctly failed its
  build-ID handshake. Matching artifacts were then selected; failed mismatched
  runs supply no performance evidence. IDs hash SDK Cargo.toml and src files.
- Global fmt reports 102 pre-existing files outside this repair. Strict Clippy
  stops at pre-existing build.rs:35; a complete lint inventory without -D warnings
  reports 43 unmodified paths and none in changed/new construction sources.
- Global warning-denied rustdoc fails pre-existing CLI legacy/options.rs:100–101
  unresolved [33]/[129] links. SDK documentation passes separately.

Pending SOW-0041 owns the unrelated formatting/lint/rustdoc baseline. No blanket
cleanup or suppressed production lint was introduced to hide those failures.

### Review and same-failure search

Independent read-only adversarial reviews found no remaining blocking defect.
Findings addressed: optional heap starving mandatory scratch; redundant refcount
writes; repeated output interning; duplicate-catalog test initially also rejecting
empty labels. The final duplicate test uses otherwise valid input.

Reviewed both widths and all constructor paths for mandatory/optional heap ordering,
workspace lifetime before truncation, ID reuse during caching, operand collection,
zero values and cardinality endpoints, duplicate logical names, source failures,
publication cleanup and budget monotonicity. Existing publication uncertainty and
fault handling remain shared rather than reimplemented.

Evidence logs are retained in task-private scratch: sdk40-sdk-final.log,
sdk40-no-default.log, sdk40-final-focused.log, sdk40-sdk-doc.log,
sdk40-mmap-runtime-02.log, sdk40-source-graph-close.log,
sdk40-provider-baselines-02.log and sdk40-provider-ci-smoke.log. Public tests and
benchmark commands are reproducible; private logs are not authoritative fixtures.

## Artifact maintenance and follow-up mapping

- Parent AGENTS.md: operational findings and validation limitations updated.
- Runtime skill project-v4-rust: recorded the observed debug-benchmark timeout,
  debug-only panic fixture, and matching worker/benchmark selection lesson.
- SDK README and design spec: API semantics, budgets, ownership and measurements
  updated. Wire-format, C ABI and Go specs are unaffected.
- No output/operator skill exists for this SDK, so none needs synchronization.
- v3 starting brief and provider SOW-0130: SDK dependency status updated; provider
  parsing/publication decisions and implementation remain in that milestone.
- SOW-0041 tracks unrelated baseline hygiene; implementation is not authorized.
- No untracked performance repair remains from review. Universal optimality is
  not asserted, and unrelated live/query optimizations are outside this repair.
- Sensitive-data review: only synthetic ranges/names and aggregate measurements;
  no credentials, provider payloads, private endpoints or personal names added.
- Local checkpoint includes implementation, docs, benchmark baseline, skill,
  this SOW lifecycle change and the concrete hygiene follow-up together.

## Outcome

The SDK now constructs immutable direct-valued and named multi-membership files
for both families without a live database/commit/snapshot roundtrip. Ordered input
avoids normalization workspace; unordered input remains bounded and uses shared
engine owners. Existing single-feed behavior and publication contracts are retained.
Local checkpoint and final closure review are recorded below.

## Closure — 2026-10-28

Independent final artifact review: PASS, no remaining blocking findings. Final complete
Clippy inventory finishes successfully without warning denial and contains no diagnostic
in any changed/new file; global baseline warnings remain explicitly tracked. Release
benchmark inspection finds no work-counter symbols or selected counter field strings.
All changed/new Rust formatting and git diff whitespace checks pass. Consumer compilation
passes in 6.17 seconds. Final provider CI and expanded smoke pass; final four-target
source graph and runtime mmap gates pass.

Implementation, documentation, baseline additions, workflow lesson, this completed SOW
and its move to done are recorded together in the local signed checkpoint, following
AGENTS.md completion rules. Root planning notes and the sibling provider tracking note
reflect the SDK contract; no sibling implementation or unapproved sibling commit is
included. The only remaining implementation items are explicitly owned by provider
SOW-0130 and baseline-hygiene SOW-0041, outside this completed SDK repair.
