# SOW-0023 - Rust v4 Modular Structured Values

## Status

Status: completed

Sub-state: delivered; all acceptance, audit, performance, and portability gates
pass.

## Requirements

### Purpose

Make the Rust v4 database capable of one zero-allocation IP lookup that returns a
fixed numeric enrichment profile plus lazy threat-feed membership, while keeping
the format thin, mmap-only, modular, and open to additional hardcoded structures
without adding a runtime schema system.

### User Request

- Raise the optional opaque metadata limit to 20 MiB uncompressed; keep it
  compressed in the file.
- Preserve the range record's existing `value:u32` field. In a structured file,
  the SDK owns that value as a structure ID.
- Add a common structured-value mechanism that can support multiple hardcoded
  structures selected by an enum. A file selects one structure.
- Keep every structure's contents, byte codec, and semantic checks modular in
  code. New structures reuse the common manager instead of copying it.
- Implement `NetworkEnrichmentV1` first. Its numeric fields are ASN, country,
  state, city, latitude, longitude, and threat membership; names remain in the
  optional file metadata.
- Optimize the reader for the maximum performance the format can provide.
- Complete and prove Rust before starting or changing the Go implementation.

### Assistant Understanding

Facts:

- Current range leaves already store `value:u32`; the physical IPv4 and IPv6
  range-record sizes need not change.
- Current files select exactly one of `Direct` or `Membership` through the
  immutable one-byte `value_kind` field. Bytes 13 through 15 of the meta header
  are currently reserved.
- Current membership files already provide SDK-owned feed indexes, canonical
  membership IDs, hash interning, used-ID allocation, COW mutation, lazy bitmap
  reads, and explicit validation/recovery.
- Current metadata is one opaque zlib stream capped at 1 MiB uncompressed.
- Measured current publisher data requires about 5.86 MiB of compact JSON for
  the ASN-name mapping and distinct country/state/city labels, before small
  wrapper overhead. A 20 MiB limit leaves about 14.4 MiB of headroom.
- The current v4 bytes and Rust-provided C ABI have not been released. The user
  explicitly requires only the final current format and no compatibility with
  experimental predecessors.
- The Rust implementation is the only implementation in scope. Go begins only
  after explicit user acceptance of the completed Rust result.

Inferences:

- `ValueKind::Structured` plus a separate immutable `StructureKind` is clearer
  than treating every hardcoded structure as an unrelated storage family.
- The common manager must own structure identity, interning, exact refcounts,
  allocation, COW, and reclamation. A per-structure module must own only its
  payload layout and semantics.
- `NetworkEnrichmentV1` should store an internal membership ID, not an inline
  fixed-width bitmap. This preserves the unbounded feed-index model and avoids
  repeating equal bitmaps across many geographic profiles.
- The structure table's first physical implementation must be selected with
  measured reader evidence. Reusing the proven fixed tree is the simplicity
  baseline; a denser page-indexed design is accepted only if controlled A/B
  evidence shows a material lookup benefit worth the extra mechanism.

Unknowns:

- No product or semantic decision remains open. Exact internal Rust type and
  function names, page packing, and the measured choice between proven fixed-tree
  indexing and a direct page-indexed structure table are implementation details.

### Acceptance Criteria

- The exact v4 spec defines `ValueKind::Structured`, `StructureKind`, the common
  structured-value tables, `NetworkEnrichmentV1`, its canonical 32-byte payload,
  its relationship to membership data, and clean unknown-structure rejection.
- One file selects exactly one hardcoded structure kind. There is no runtime
  schema, per-record structure tag, caller-defined payload, or mixed structure
  kind within a file.
- Common mapped code implements structure-ID lookup, interning, collision-safe
  equality, refcounts, lowest-free-ID allocation/reuse, COW mutation, retirement,
  and reclamation once.
- `NetworkEnrichmentV1` has a separate cohesive module owning all payload offsets,
  encode/decode logic, canonical validation, and typed semantic translation.
  The common manager contains no ASN, geography, coordinate, or field-offset
  knowledge.
- The persisted enrichment payload is exactly 32 bytes: ASN `u32`, country ID
  `u32`, state ID `u32`, city ID `u32`, latitude in signed microdegrees `i32`,
  longitude in signed microdegrees `i32`, internal membership ID `u32`, and flags
  `u32`.
- Structure ID zero means absence and is never stored. Equal canonical payloads
  intern to one structure ID. Callers cannot submit a raw structure ID or raw
  membership ID as mutation authority.
- A typed IPv4/IPv6 enrichment lookup returns the numeric profile and a lazy
  membership view without heap allocation, metadata parsing, implicit whole-file
  validation, content I/O, or an application page cache.
- The metadata limit is exactly 20 MiB uncompressed. Boundary, stored-block,
  compressed, malformed, budget, live COW, snapshot, validation, and recovery
  tests cover it.
- Structured values participate correctly in creation, arbitrary arrival-order
  range application, commit/abort, snapshots, compaction, explicit validation,
  strict recovery, and best-effort recovery.
- The Rust-provided C ABI exposes equivalent typed behavior. Because it is
  unreleased, the current ABI generation and manifest are updated in place; no
  compatibility ABI or second format is retained.
- Reader benchmarks include at least one million true-random IPv4 lookups over a
  one-million-range enrichment database, selected threat checks, scalar-only
  access, and comparison with separate ASN, Geo, and membership lookups on the
  same machine and data shape. Results separate facts from projections.
- Writer benchmarks cover bulk construction, structure interning, and combined
  source-boundary output. Writer speed need not match direct ingestion, but work
  remains bounded, mmap-only, single-pass over ordered semantic inputs, and free
  of temporary sorting files.
- Necessary-work counters prove one range descent, the selected minimum
  structure lookup work, lazy membership work only when requested, no per-lookup
  allocation, no metadata work, and no hidden validation in the lookup path.
- Source-graph, architecture, mmap-only, syscall-runtime, tests, Clippy, format,
  rustdoc, MSRV, portable-codec, sanitizer, conformance, generated-C, and native
  C gates pass. Native non-Linux platform testing remains subject to explicit
  user authorization.
- A final code-organization and duplication audit finds one physical authority
  and no structure-specific copy of manager operations. Any finding reopens the
  implementation loop instead of completing the SOW.
- No Go implementation or signing work is started.

## Analysis

Sources checked:

- `.agents/sow/specs/design-iprange-engine.md`
- `.agents/sow/specs/binary-format-v4.md`
- `.agents/sow/specs/c-abi-v4.md`
- `.agents/skills/project-v4-rust/SKILL.md`
- `v4/rust/iprange-livedb/src/contract.rs`
- `v4/rust/iprange-livedb/src/fixed_tree.rs`
- `v4/rust/iprange-livedb/src/membership_tree.rs`
- `v4/rust/iprange-livedb/src/membership_dictionary/`
- `v4/rust/iprange-livedb/src/draft_store/membership.rs`
- `v4/rust/iprange-livedb/src/membership_view.rs`
- `v4/rust/iprange-livedb/src/metadata.rs`
- `v4/rust/iprange-livedb/src/validation/`
- `v4/rust/iprange-livedb/src/recovery/`
- `v4/rust/iprange-livedb/src/immutable_output/`
- `v4/rust/iprange-capi/` and the generated ABI manifest/header tests
- completed SOW-0020, SOW-0021, and SOW-0022 as historical implementation and
  performance evidence
- pending SOW-0017 to verify that signing remains independent and untouched
- the current Netdata ASN/Geo writer and Netflow Geo decoder for the exact
  consumed fields and six-decimal coordinate behavior
- the current installed public ASN/Geo artifacts through a temporary read-only
  measurement program; only aggregate counts and sizes are recorded here
- the open-source reference below

Current state:

- `ValueKind` has only `Direct=1` and `Membership=2`; unknown values fail meta
  bootstrap.
- The meta has no structure kind, structure count/limit, or structure roots, but
  has enough currently reserved space for them without increasing `meta_size`.
- Page types 1 through 17 are assigned. Types 18 through 21 can identify the
  structure-ID and structure-hash branch/leaf pages without changing the common
  page header.
- Membership dictionary code already separates its generic fixed-tree use,
  record codec, bitmap/blob semantics, and draft-store ownership sufficiently to
  reuse the fixed-tree/COW primitives but not the membership record layout.
- The current membership refcount means range-record references. Structured
  files require membership lifetime to count live structure-record references;
  the common membership authority must encode and validate the owner model once
  based on the selected value family.
- Current metadata allocation and traversal use fixed compile-time page-number
  arrays sized for the 1 MiB bound. Raising the bound to 20 MiB grows them to
  roughly 5,182 page numbers, about 21 KiB each; this is bounded cold-path state,
  not a database page image or hot-path cost.
- The C ABI freezes the current unreleased constants and exported surface. The
  user has rejected compatibility with every experimental format/API, so the one
  current ABI is regenerated rather than version-stacked.

Risks:

- A second independent dictionary implementation would repeat the exact
  authority problem previously removed from the codebase.
- A profile B+tree can add avoidable mapped-page probes to every lookup; an
  unproven direct index can add more code and COW complexity than its measured
  benefit. The choice must be empirical before the wire is frozen.
- Structure and membership refcount coupling can reclaim a bitmap too early or
  leak it forever if creation/deletion transitions are not atomic.
- A malformed profile could point to a missing membership, carry an invalid
  coordinate, use noncanonical absence bytes, or disagree with the hash/used-ID
  indexes.
- Snapshot and recovery can accidentally copy source-local IDs instead of
  rebuilding semantic values in the destination generation.
- Enlarging metadata permits larger caller allocations and longer explicit
  validation/recovery work. The limit remains hard, compression is bounded, and
  ordinary lookup/open does not decompress metadata.
- Extending the current Rust and C surfaces can increase the already large
  codebase. The modular structure boundary and final authority/clone/size audit
  are acceptance requirements, not optional cleanup.

## Pre-Implementation Gate

Status: ready

Problem / root-cause model:

- The current format can return either one opaque `u32` or one threat-feed
  bitmap. Netflow enrichment needs ASN, country, state, city, coordinates, and
  threat matches together. Separate files require multiple IP-tree lookups and
  cannot publish one generation-consistent combined answer.
- Storing every categorical value as a membership bit makes sparse namespaces
  and coordinate combinations large. Keeping the range's `u32` as an SDK-owned
  structure ID preserves compact ranges while allowing a fixed numeric profile.
- Hardcoding only one profile directly into the manager would make the next
  profile version duplicate or contaminate physical management code. A common
  manager plus modular hardcoded codecs is the smallest design that supports the
  approved V1 and a later exact V2 without a runtime schema.

Evidence reviewed:

- The current meta and range record layouts in the normative binary spec and
  `contract.rs`.
- Current membership ID/hash/used-bitmap, fixed-tree, draft-store, view,
  validation, recovery, and snapshot paths.
- Current one-authority and minimum-work evidence in SOW-0020 and SOW-0022.
- Current C ABI numeric/symbol freeze and generation checks.
- Current Netdata ASN/Geo record production and Netflow field consumption.
- Aggregate measurement of current public data: 80,568 ASN names and 178,260
  distinct Geo labels need about 5.86 MiB compact JSON together; 20 MiB is a
  bounded limit with substantial measured headroom.
- MaxMind's format uses one per-database record-type discriminator while leaving
  the actual record schema to the producer. v4 deliberately adopts only the
  useful per-file discriminator; its supported structures remain exact and
  hardcoded.

Affected contracts and surfaces:

- v4 meta bytes, page-type registry, structure and membership dictionary
  invariants, range-value semantics, static identity, metadata resource limit,
  creation/open information, Rust reader/writer APIs, C ABI constants/layouts/
  symbols, commit/abort/reclaim, snapshots, validation, recovery, conformance,
  benchmarks, generated artifacts, Rust README, project skill, and SOW lifecycle.
- Go is affected eventually because the shared wire changes, but is explicitly
  outside execution until Rust acceptance.
- Signing remains isolated in pending SOW-0017 and is unaffected.

Existing patterns to reuse:

- `fixed_tree` as the sole ordered mapped-tree traversal and COW-edit authority.
- `slotted_page` and typed codecs as the sole cell layout/movement authorities.
- membership dictionary's ID/hash collision-safe interning and used-ID bitmap
  semantics, generalized without copying its bitmap-specific codec.
- `DraftStore` as the only healthy mapped mutation owner.
- `ReaderCore::read()`/`GenerationReader` as the only healthy selected-generation
  read capability.
- `MembershipRef` and lazy `MembershipView` for internal bitmap ownership and
  zero-allocation reads.
- explicit validation/recovery isolation over shared codecs and mapped output.
- immutable-output and snapshot semantic rebuilding rather than physical ID copy.
- test-only necessary-work accounting with no release symbols.

Risk and blast radius:

- High: new permanent wire semantics, COW ownership graph, cross-dictionary
  lifetime, validation/recovery, snapshots, and unreleased C API.
- Correctness risks include dangling IDs, duplicate canonical structures,
  refcount disagreement, noncanonical payloads, hash collisions, stale handles,
  transaction rollback errors, and incorrect best-effort recovery.
- Performance risks include an extra tree descent, repeated structure decoding,
  membership work on scalar-only calls, per-lookup allocation, metadata parsing,
  cache-density loss, and writer-side per-record hashing/copies.
- Compatibility risk is intentionally resolved by replacing the experimental
  current contract. No old v4 bytes or ABI are supported after the change.
- Data-loss risk is contained by preserving the existing COW, retirement,
  commit-sealing, flush/sync, and publication authorities and extending their
  graph inventories before writing new page types.

Sensitive data handling plan:

- Use synthetic ranges and authorized public source artifacts only. Record
  aggregate sizes, cardinalities, timings, and profile attribution.
- Do not record source names, operational paths, literal IP ranges, credentials,
  private endpoints, customer/community data, personal data, or proprietary
  incident details in durable artifacts.
- C fixtures use synthetic labels, values, and generated temporary files.

Implementation plan:

1. Update the exact specs first: identity enums, meta fields, pages, common
   structure records/indexes, V1 payload, membership ownership, APIs, metadata
   bound, validation/recovery, and C ABI.
2. Add the 20 MiB metadata bound and boundary/resource tests independently, with
   no lookup-path change.
3. Add structure identity and the generic mapped manager over existing page,
   tree, bitmap, allocation, retirement, and hash primitives. Keep the common
   manager unaware of V1 fields.
4. Add a dedicated `NetworkEnrichmentV1` module containing the 32-byte codec,
   canonical rules, public semantic types, and conversion to/from the common
   manager and existing membership authority.
5. Add typed Rust creation, transaction-bound structure construction/application,
   lookup/view, cursor, and bulk construction surfaces. Preserve arrival-order
   range semantics and zero-allocation reads.
6. Extend commit/reclaim, snapshots, explicit validation, strict recovery, and
   best-effort recovery. Rebuild destination-local structure and membership IDs
   semantically.
7. Extend the unreleased Rust-provided C ABI, regenerate its one exact header and
   manifest, and add native C behavior/conformance tests.
8. Add corruption, model/property, stale-handle, cancellation, crash, mmap,
   allocation, necessary-work, and conformance tests.
9. A/B the proven fixed-tree manager against any simpler page-indexed candidate.
   Keep only the fastest design whose gain is material and whose extra mechanism
   remains justified. Profile one-million true-random lookups and combined
   enrichment use.
10. Run the complete Rust gates and repeat physical-authority, hot-path waste,
    file/function size, complexity, clone, dead-code, and same-failure audits.
    Iterate until no actionable finding remains.

Validation plan:

- Literal portable codec vectors for every new meta field, common record, hash
  record, and V1 payload; run selected production codecs under s390x Miri.
- Unit/model/property tests for interning, exact collision comparison, ID reuse,
  refcounts, range split/coalescing, membership sharing, abort, reclaim, and
  stale/foreign handles.
- Explicit malformed-page and cross-root contradiction tests for every new field,
  page, ID, refcount, hash, payload, flag, coordinate, and membership reference.
- Snapshot/compaction equivalence and strict/best-effort recovery tests with
  independently corrupted structure and membership components.
- Live crash/durability and old-reader generation-pinning tests.
- Exact metadata tests at 20 MiB and 20 MiB plus one byte, compressible and
  incompressible, minimum heap budget, COW visibility, and malformed zlib/chains.
- One-million-range scalar and scalar-plus-threat lookup benchmarks, scans,
  construction, file size, allocation, page visits, and controlled profiles.
- Existing architecture/mmap/source/syscall gates; complete all-feature and
  no-default-feature tests; Clippy/format/rustdoc; MSRV; ASan; generated C header,
  manifest, exports, C11/C++17, and native behavior.
- Same-failure searches for copied profile offsets/codecs, raw structure or
  membership IDs above the private engine, per-item allocation, implicit
  validation/metadata parsing, content I/O, page copies, and duplicate graph
  traversal/mutation/recovery builders.

Artifact impact plan:

- AGENTS.md: update only if the completed work establishes a new project-wide
  structure-extension rule not already captured by the v4 philosophy.
- Runtime project skills: update `project-v4-rust` with the exact structured-value
  verification and benchmark workflow after it is proven.
- Specs: update all three normative v4 specs before implementation and keep them
  synchronized with final bytes and APIs.
- End-user/operator docs: update `v4/rust/README.md` with typed enrichment use,
  metadata limit, resource behavior, and factual benchmarks.
- End-user/operator skills: none currently exist; record evidence if this remains
  true at close.
- SOW lifecycle: SOW-0023 is the sole current SOW. SOW-0017 remains pending and
  untouched. Before close, map every deferred item; create a real pending Go SOW
  only after explicit Rust acceptance authorizes that next phase.

Open-source reference evidence:

- `maxmind/MaxMind-DB @ b7cb76231170032009679af84c5748244005d645`
  - `MaxMind-DB-spec.md:58-82` distinguishes format-version changes from the
    per-database record-structure discriminator.
  - `MaxMind-DB-spec.md:184-208` maps one IP-tree result to one data-section
    pointer.
- This is structural comparison evidence only. v4 does not adopt MMDB's dynamic
  maps, arrays, pointer encoding, or producer-defined schemas.

Open decisions:

- None. Resolved user decisions are listed below. Physical index selection is a
  measured implementation choice constrained by the approved simplicity and
  maximum-reader-performance requirements.

## Implications And Decisions

1. The optional metadata limit is 20 MiB uncompressed and remains zlib-compressed
   in the file.
2. `value:u32` remains unchanged in range records. Structured files interpret it
   as an SDK-owned structure ID; callers never mutate by raw ID.
3. The format supports multiple hardcoded structures through one shared manager.
   Each file selects exactly one structure enum value.
4. Structure contents are modular. A structure-specific module owns its fixed
   payload, offsets, encode/decode, canonical checks, and typed semantic API.
5. The common manager owns all IDs, hashing, exact interning, refcounts, mapped
   storage, COW, allocation/reuse, retirement, reclamation, snapshots, validation,
   and recovery once.
6. `NetworkEnrichmentV1` is the first structure and has the approved fixed
   32-byte numeric payload.
7. V1 references the existing canonical membership dictionary. It does not
   inline a fixed-width bitmap or impose a feed-count ceiling.
8. Names and publisher annotations remain in the opaque metadata payload; the
   physical manager does not parse JSON or perform string work in lookups.
9. Unknown structure kinds are reported as unsupported before graph traversal;
   they are not silently interpreted, dynamically decoded, or accepted as a
   generic schema.
10. Only the latest unreleased v4 and current unreleased C ABI remain. No old
    experimental format, parser, importer, exporter, fixture, or ABI variant is
    retained.
11. Rust is implemented, validated, and benchmarked first. Go remains untouched
    until explicit user acceptance of the Rust result.

## Plan

1. Specify exact bytes and public semantics.
2. Raise and prove the metadata bound.
3. Implement common structured storage and modular V1 codec/API.
4. Integrate all lifecycle, validation, recovery, snapshot, and C surfaces.
5. Prove correctness, bounded resources, mmap-only operation, and lookup speed.
6. Audit and iterate until there is no actionable waste or duplicate authority.
7. Update artifacts, obtain explicit Rust acceptance, and close the SOW.

## Execution Log

### 2026-08-11

- Read the project Rust skill, all current normative specs, the sole pending SOW,
  the current source graph/status, relevant completed authority/performance SOWs,
  the common fixed-tree and membership authorities, metadata implementation,
  C ABI registry, current Netdata field producer/consumer, and the external
  reference above.
- Measured only aggregate label/name cardinality and compact JSON size from
  authorized public local data. No source identity, path, or literal range was
  recorded.
- Recorded the approved modular hardcoded-structure design and marked the
  pre-implementation gate ready. Production code has not yet changed.
- Updated all three normative v4 specifications before production code. The
  wire now defines the structured value/structure-kind identity, common
  structure dictionary and reverse index, exact modular V1 payload, membership
  ownership, 20 MiB metadata bound, typed Rust semantics, validation/recovery/
  snapshot behavior, and the corresponding unreleased C ABI surface.
- Implemented the common structure manager, the independent
  `network_enrichment_v1` codec/API, lifecycle integration, validation,
  recovery, snapshots, conformance, and the Rust-provided C surface. Added
  corruption, recovery, stale-handle, allocation, necessary-work, and native C
  coverage.
- Profiled 10 million true-random lookups over one million ranges on one pinned
  performance core. The measured immutable structured scalar path completed
  1.852 million lookups/second, while three separate ASN, Geo, and threat files
  completed 1.048 million combined lookups/second. The structured file is 1.77
  times faster than the three-file design, but its generic structure-ID B+tree
  remains avoidable work: the two generic fixed-tree lower-bound loops consume
  62.85% of sampled cycles, while structure record decoding consumes 1.20%.
- The controlled index A/B therefore proceeds with a common direct ID-indexed
  mapped table candidate. It keeps the existing canonical record envelope and
  writer-only hash tree, but replaces binary searches in the authoritative ID
  index with arithmetic radix-page selection. The candidate is retained only
  if the same benchmark proves a material gain and full lifecycle,
  validation, and recovery gates pass; otherwise it is removed.
- Retained the direct table after the controlled repeat raised immutable
  structured scalar lookup from 1.852 million to 2.945 million lookups/second
  on the same pinned core. Structure-table location then accounted for 1.35%
  of sampled cycles; range-tree descent became the dominant required work.
- The first exact-clone audit found genuine copied recovery mechanics between
  membership and structured modes: common immutable-build orchestration and
  the fixed-record source-ID scratch index. These are implementation defects,
  not acceptable policy variation. Repair them through one shared indirect
  recovery builder and one shared ID-table authority; retain only the distinct
  membership and structure record codecs/output policies. Repeat the same
  audit after repair before acceptance.
- Consolidated recovery through common `recovery/id_table.rs`,
  `recovery/indirect_build.rs`, `recovery/page_read.rs`, and page-claim
  authorities. Membership and structured recovery now supply only their
  different record codecs and semantic output policies.
- Repeated the exact-clone audit after repair. The production result is 11
  small shapes totaling 254 lines, about 0.29% of analyzed production code.
  Manual inspection found C adapter forms, codec trait shells, typed workflow/
  cursor wrappers, reader facades, and distinct damaged-input policies. It
  found no copied persistent layout, ID, hash, refcount, allocation, COW,
  validation, recovery, snapshot, or range-construction authority.
- Added the 20 MiB metadata boundary and resource coverage, structured
  corruption/recovery/snapshot tests, one committed structured conformance
  fixture, the typed Rust and C APIs, generated 168-symbol artifacts, and a
  fixed literal-byte V1 vector that passes on big-endian s390x Miri.
- Measured the retained direct structure table on the accepted pinned-core
  matrix. Immutable scalar lookup is 2.945 million queries/second versus 1.063
  million combined answers/second from separate ASN, Geo, and threat files, a
  2.77-times improvement. Live scalar lookup is 2.214 million/second, live
  scalar plus selected threat is 1.826 million/second, immutable typed scan is
  21.246 million ranges/second, and live typed scan is 20.408 million/second.
- Split the writer timing into profile interning, range assignment, and commit.
  One million random structured ranges build at 1.081 million/second;
  pre-interned assignment runs at 1.223 million/second; commit runs at 27.964
  million ranges/second. Setup allocation count remains 865 at 10,000,
  100,000, and one million ranges; timed interning, assignment, and all reader
  paths allocate nothing, while commit uses four allocations totaling 48 bytes.
- Profiled the exact timed reader region with frame pointers. Required range
  descent accounts for 43.73% of samples; direct structure location is 1.35%,
  typed decode is 4.38%, and error-drop glue is 1.05%. Necessary-work tests
  prove one range lookup, one structure lookup/decode, no scalar membership
  work, one lazy membership lookup only when requested, no metadata work, no
  implicit validation, and zero timed allocations.
- Audited production organization and size after the final portable vector:
  335 files, 96,226 physical lines, 87,313 Lizard code lines, 5,183 functions,
  average function NLOC 14.0, and average CCN 3.5. Fifty-four files exceed the
  directional 500-line target; the largest is 950 lines and no file reaches
  1,000. The largest function remains the cohesive 191-line recovery-attempt
  state machine. The codebase is far above the directional 5,000-line goal;
  the final authority/clone/complexity review found no further safe reduction
  in this SOW's structured-value blast radius.
- Ran local Codacy Analysis with Trivy, Semgrep, and Lizard over the final
  source. Trivy reported no finding. Structured-code output contains 47 minor
  complexity signals, five medium function-size signals, one file-size signal,
  one parameter-count signal, and 16 generic `unsafe` findings in the required
  C FFI adapter. Manual review found validated opaque inputs/outputs and no
  actionable structured correctness or security defect. Eight pre-existing
  Semgrep partial-parse warnings and one Trivy OSV service HTTP warning limit
  tool completeness and are recorded rather than hidden.
- Completed native all-feature/all-target tests on Linux, Windows 11 GNU Rust,
  macOS M4, and FreeBSD 14. FreeBSD's persistent filesystems had no free bytes,
  so compilation used a temporary tmpfs and output used a temporary local UFS
  memory disk with ACLs; both temporary mounts were released afterward. Its
  exact boundary proves immutable validation, recovery, and publication while
  every live entry rejects before mutation.

## Validation

Acceptance criteria evidence:

- The exact binary, engine, and C ABI specs define structured identity, the
  common manager, modular codecs, exact 32-byte V1 payload, membership
  ownership, metadata bound, typed APIs, validation, recovery, and snapshots.
- `structured_value/manager.rs`, `table.rs`, and `codec.rs` own the common
  structure operations. `network_enrichment_v1.rs` alone owns V1 fields,
  offsets, canonical checks, and typed translation. The architecture gate
  rejects a structured adapter that reaches either physical authority and
  rejects V1 field names in the common manager.
- The public mutation surfaces accept typed scalar fields, feed names, and
  generation-bound references. Source searches and compile-time visibility
  prove no raw structure ID or membership ID is public mutation authority.
- Exact 20 MiB and one-byte-over-limit metadata tests cover compressed,
  stored-block, malformed, budget, COW visibility, snapshot, validation, and
  recovery behavior. Ordinary open and lookup do not decompress metadata.
- Structured creation, arrival-order overwrite/clear, abort, commit, reclaim,
  stale/foreign handles, snapshot, compaction, strict recovery, best-effort
  recovery, corruption, and conformance are permanent tests.
- The generated header and manifest exactly match the Rust boundary; all 168
  symbols, 15 opaque handles, and 19 callbacks are frozen in the one unreleased
  ABI. Eight native C programs cover the real shared library.
- The final controlled profile finds no actionable structured-manager work in
  the hot path. The dominant cost is the required range-tree descent. The
  retained direct ID table removed the avoidable second B+tree descent.
- Release-binary symbol and string inspection finds no `work` counter storage,
  counter fields, or counter calls. The necessary-work instrumentation compiles
  away outside tests.
- The final physical-authority and clone audit finds no duplicated structure
  manager or copied persistent format operation. The common manager contains no
  ASN, country, state, city, latitude, longitude, or V1 offset knowledge.

Tests or equivalent validation:

- `check-architecture.sh`, `check-mmap-storage.sh`,
  `check-mmap-runtime.sh`, and `check-source-graph.sh` pass. The runtime trace
  observes mappings for the database, reader sidecar, snapshot, recovery,
  publication, reservation, and scratch paths and no persistent-content
  transfer syscall.
- Current-toolchain all-feature/all-target and no-default-feature/all-target
  matrices each execute 576 passing tests/targets. Clippy with warnings denied,
  rustfmt check, warning-denied rustdoc, and `git diff --check` pass.
- Rust 1.74.1 executes the same 576 passing tests/targets from its own target
  directory. Big-endian s390x Miri passes 11 literal production-codec vectors,
  including the V1 structured payload.
- AddressSanitizer with leak detection passes 410 livedb library tests, 19 C
  adapter library tests, and all nine Linux native-C test entries with the
  identically instrumented adjacent worker.
- The committed conformance corpus opens through the public reader, performs
  explicit full validation, and independently matches declared semantics. The
  new structured fixture is included; regeneration remains an ignored explicit
  operation.
- The optimized 18-case CI benchmark passes every loose disaster threshold.
  The component-floor suite passes mapped scan, page search/build, CRC32C,
  SHA-512, and flush/sync measurements with zero timed allocations.
- Native all-feature/all-target results pass on Linux (576), Windows 11 with the
  required GNU toolchain and isolated target (417), macOS M4 (525), and FreeBSD
  14 (352). Platform counts differ because unsupported tests are compile-time
  excluded. Windows native header and Windows-specific behavior tests pass.
  FreeBSD's two permanent boundary tests pass.

Real-use evidence:

- Authorized current publisher data needs about 5.86 MiB of compact JSON for
  ASN names and distinct Geo labels, leaving about 14.4 MiB under the 20 MiB
  bound. Only aggregate counts and sizes were recorded.
- One million current-shaped ranges and ten million deterministic true-random
  queries were replayed on one pinned performance core. One structured file is
  2.77 times faster than the equivalent separate ASN, Geo, and threat files in
  that controlled workload. Detailed medians, allocations, and file sizes are
  committed in the Rust README and benchmark baseline.

Reviewer findings:

- No external reviewer or subagent was authorized or used. Acceptance uses
  direct code inspection, compiled tests, native platforms, profiles, static
  analysis, and exact-clone evidence.
- The first audit rejected copied recovery orchestration and source-ID tables.
  They were consolidated, and the same audit was repeated. The second audit
  found no actionable duplicate authority or hot-path waste.

Same-failure scan:

- V1 field offsets occur only in the V1 codec. Common recovery locator offsets
  describe its scratch format, not the V1 payload.
- Structured high-level and C adapters do not mention physical roots, page
  types, ID limits, common manager modules, or raw IDs.
- Structured lookup/cursor/table paths contain no heap collection, metadata
  parsing, implicit validation, content-I/O call, full-page copy, or application
  page cache.
- Membership and structured recovery share page loading, page claiming,
  source-ID indexing, retained-budget accounting, sort reuse, catalog output,
  immutable construction, and failure cleanup. Their remaining matched shells
  encode genuinely different records and damaged-input policies.
- Unknown structure kinds, malformed payloads, dangling membership references,
  refcount disagreement, hash disagreement, and reused/stale handles all have
  explicit rejection tests.

Sensitive data gate:

- Durable artifacts contain no raw secrets, credentials, bearer tokens, private
  keys, customer/community names, personal data, identifying IP ranges, private
  endpoints, operational source names/paths, or proprietary incident details.
  Public-source evidence is aggregate only.

Artifact maintenance gate:

- AGENTS.md: updated with the one-manager/per-kind-codec project rule.
- Runtime project skills: `project-v4-rust` now requires structured A/B,
  zero-allocation, lazy-work, and duplicate-authority evidence and records the
  90-case local/18-case CI matrix and 168-symbol ABI.
- Specs: all three normative v4 specs describe the exact implemented wire,
  engine behavior, and C boundary.
- End-user/operator docs: the Rust README records the typed structured model,
  20 MiB bound, C ABI inventory, code audit, benchmarks, allocations, and
  profile limits.
- End-user/operator skills: none exist in this repository, so no downstream
  skill can be stale.
- SOW lifecycle: SOW-0023 is the sole current SOW and is ready to move to
  `done/` with its implementation commit. Signing SOW-0017 remains independent
  and untouched.

Specs update:

- `design-iprange-engine.md`, `binary-format-v4.md`, and `c-abi-v4.md` are
  synchronized with the final implementation and generated artifacts.

Project skills update:

- Updated `.agents/skills/project-v4-rust/SKILL.md` with the final structured
  verification and benchmark workflow.

End-user/operator docs update:

- Updated `v4/rust/README.md` with the final API, resource rules, performance,
  and honest code-size/duplication evidence.

End-user/operator skills update:

- None exist; the skill index confirms there is no output/reference skill to
  update.

Lessons:

- Reusing a generic B+tree was simple in source but not minimal in reader work.
  A controlled A/B justified the arithmetic direct table and removed the extra
  mapped search without introducing a second manager.
- Similar membership and structure recovery modes can hide copied physical
  authority behind different names. Exact-clone detection plus manual semantic
  review found and removed that duplication.
- A same-machine encode/decode round trip is not portable-byte evidence. Every
  new persistent codec needs a literal vector selected by the big-endian Miri
  gate.
- ABI verification consumes the normative spec as well as code and generated
  artifacts. A native test overlay must include all four or it correctly fails
  as inconsistent.
- FreeBSD publication proof requires a local filesystem with working ACLs. A
  full disk or tmpfs is correctly rejected rather than silently weakening
  access-policy or durability guarantees.

Follow-up mapping:

- No V2 structure is requested or deferred. The enum and codec boundary prove
  extensibility without inventing a second payload.
- The Go port is not deferred work from this SOW: it remains unauthorized until
  explicit user acceptance of the Rust result. No Go SOW or Go code was created.
- Authenticated signing remains tracked by pending SOW-0017 and did not enter
  this implementation.

## Outcome

- Rust v4 now supports one modular structured-value family with one common
  mapped manager and the hardcoded `NetworkEnrichmentV1` codec. It returns ASN,
  geographic IDs, optional coordinates, and lazy threat membership through one
  zero-allocation lookup, preserves every required lifecycle and recovery
  contract, and has matching typed Rust and C surfaces.
- The retained design materially beats three separate enrichment files in the
  controlled reader workload and has no actionable structured-manager waste or
  copied physical authority in the final audit.
- Rust is implementation-complete for this approved SOW. Product acceptance of
  Rust and authorization of the Go port remain user decisions, not claims made
  by this SOW.

## Lessons Extracted

- Added the common-manager/per-kind-codec rule to AGENTS.md.
- Added structured performance, lazy-work, portability, and duplicate-authority
  gates to the runtime Rust project skill.

## Followup

- After explicit Rust acceptance, the user may authorize a separate pure-Go
  port SOW. Do not begin it from this SOW.
- Pending signing SOW-0017 remains Phase 2 and independent.

## Regression Log

None yet.
