# SOW-0028 - Production `iprange` CLI, JSON-RPC API, And External Qualification

## Standing Review Rules (user-mandated, read after every compaction)

0. When the user asks to use the swarm, read and follow
   `/home/costa/.codex/SWARM.md` in whole; never skim it.
1. Prefer workers and reviewers in the lead assistant's own model.
2. Use `glm-5.3-responses` for the final review of the whole milestone.
3. Parallelize with the lead's own model as much as possible (the more
   the better); never block on a single worker.
4. Spawn multiple reviewers of the lead's own model, each with a
   different focus, then run one `glm-5.3-responses` reviewer to
   validate the entire milestone before closure.
5. Running workers are never stopped by these rules; spawn in parallel
   instead.

## Status

Status: in-progress

Sub-state: activated 2026-09-01 as the sole current SOW after SOW-0027
closed. Design is complete and approved; no product-design round is
needed. Performance scope: this SOW measures and reports Go/Rust
performance of the shipped executables but does NOT reopen the engine
optimization work of SOW-0027. Avoidable overhead introduced by the
new CLI/JSON-RPC adapters is a SOW-0028 defect and must be fixed under
this SOW. Existing engine-level performance residuals (reads, writes,
validation) remain owned by pending SOW-0030; this SOW neither inherits
nor is blocked by them.

Implementation status (2026-09-01):

- Milestone 1 (Rust JSON-RPC transport + read-only family) committed:
  transport at d5d0560b, wire-result schemas and golden corpus at
  b1f808fc, read-only handlers at a99329d8.
- Milestone 2 (publish/lifecycle/export/snapshot families) implemented by
  parallel workers and wired into the dispatch registry (commit after
  502cf032); 638 Rust tests and the 53-exchange golden corpus pass.
- Adversarial reviews of the delivered families returned FAIL with P1
  wire-contract findings (cursor start/family/IPv6 preflight, 65 KB
  response envelope, batch busy framing, live reader mode, strict Python
  schemas, runner/golden integrity). The recorded decisions above and the
  parallel fix batches implement them. Remaining: re-review, then Rust
  legacy CLI surface vs the C oracle, then the pure-Go implementation in
  the fixed delivery order.

## Requirements

### Purpose

Deliver standalone Rust and pure-Go executables named `iprange` that are
complete production interfaces for IP range processing and for the durable v4
database workflows a shell implementation of `update-ipsets` would require.
Both executables preserve the released legacy command-line interface and expose
one supported JSON-RPC 2.0 application API over a bidirectional stdin/stdout
pipe through `iprange --jsonrpc`.

The JSON-RPC API is a product contract, not a test protocol. Applications can
keep one `iprange` process open, issue correlated requests, keep production
reader handles where repeated access benefits from them, cancel long-running
work, and receive stable typed outcomes. The common external correctness and
benchmark suite is merely one client of this exact production API. No method,
field, counter, generator, or state transition exists only for tests.

Performance scope (2026-09-01): this SOW measures and reports Go/Rust
performance through the production executables and the external
benchmark client; it does not reopen existing SDK engine optimization
work. Avoidable overhead introduced by the CLI/JSON-RPC
adapters (framing, JSON, bounded bulk adapters, handle management) is a
SOW-0028 defect and must be fixed under this SOW. Engine-level
performance residuals from SOW-0027 remain owned by SOW-0030.

`update-ipsets` is the authoritative workload reference for required range,
retention, multi-feed, comparison, export, publication, and recovery
operations. This SOW does not choose its implementation language or authorize
changes in its repository.

### User Request

- Implement `iprange` as a production executable in both Rust and Go.
- Preserve the complete legacy `iprange` interface as-is.
- Support durable current-feed, first-seen, last-seen, multi-feed, snapshot,
  recovery, and exported-file workflows.
- Support every IP range operation that a shell implementation of
  `update-ipsets` would need, while leaving download, scheduling, and
  application orchestration outside this repository.
- Expose the production database interface through `iprange --jsonrpc`, using
  JSON-RPC 2.0 over a bidirectional stdin/stdout pipe rather than a `--v4`
  command namespace or test-specific NDJSON API.
- Use that production JSON-RPC interface from one implementation-neutral
  external correctness and benchmark suite for both binaries.
- Minimize test-only work. Keep test-only artifacts to the external client,
  declarative cases, independent expected-result model, and generated
  fixtures; do not add test behavior to production executables.
- Keep a WebSocket `--daemon` transport out of this SOW. It is tracked
  separately by SOW-0029 because listener security, authentication, path
  authority, quotas, and multi-client concurrency form another production
  boundary.

### Assistant Understanding

Facts:

- The ordinary command-line mode is an existing public contract. The C help
  defines IPv4/IPv6 selection, merge, common, except, diff, reduction,
  comparison, counting, parsing, DNS, output formatting, binary output,
  file-list/directory expansion, feature probes, help, version, diagnostics,
  and exit behavior at `src/iprange.c:97-400`.
- The user selected a single new production application surface:
  `iprange --jsonrpc`. There is no `--v4` mode and no second set of human v4
  command-line parameters.
- JSON-RPC 2.0 defines request identifiers, results, errors, notifications, and
  batch messages, but not stream framing. This contract uses one complete
  JSON-RPC message per UTF-8 line on stdin/stdout. Newline delimiting is
  transport framing, not a separate NDJSON application protocol.
- The v4 SDK owns exact current-feed construction, direct replacement,
  first-seen and last-seen refresh, named-feed lifecycle, multi-feed import,
  history projection, matching, aggregation, joins, algebra publication,
  snapshots, validation, recovery, commit resolution, reclamation, and
  maintenance.
- `update-ipsets` processes feeds independently, so a failed feed retains its
  previous membership and does not roll back unrelated successful feeds. The
  API must preserve this file-level failure boundary rather than inventing one
  cross-file atomic publisher transaction.
- Bulk range content already exists naturally as files in a publisher
  workflow. Sending millions of ranges as JSON values would add avoidable
  encoding, copying, allocation, and line-size problems. JSON-RPC therefore
  carries operation descriptors and file paths; handlers stream bulk source
  and destination files through bounded adapters.
- The Rust and Go binaries can both be named `iprange` because they are built
  into separate implementation-specific output directories. Selecting one for
  a distribution is packaging policy, not a semantic difference.
- Direct SDK, C ABI, mmap, crash, worker, architecture, and native-platform
  gates prove properties an external process client cannot observe. They remain
  mandatory and are not replaced by the JSON-RPC suite.

Inferences:

- The long-term-best minimal-complete shape is one method registry and one set
  of production handlers per language. Stdio JSON-RPC is a transport adapter;
  the future daemon must reuse the same registry rather than create daemon-only
  behavior.
- File-level high-level mutation methods are safer than exposing raw writer
  transactions. A dropped pipe must not strand an application-defined
  multi-request transaction.
- Connection-scoped immutable/live reader handles are production features:
  they avoid repeated open/registration work for lookups and scans and expose
  useful pinned-generation behavior. Internal page, root, feed-index,
  membership-ID, structure-ID, and allocator handles remain private.
- The legacy implementation in each binary must be self-contained. The C
  executable is the behavioral oracle, not a runtime dependency or delegated
  subprocess.
- The external suite needs independent expected outcomes as well as Rust/Go
  differential checks. Two copied adapters can otherwise agree on one defect.

Unknowns:

- Accepted release-performance ceilings must be measured on the finished
  executables. The specification provides a 5-10% comparison target where
  runtimes permit it, but no unmeasured threshold may be invented.
- Distribution policy for choosing Rust or Go as the installed default is
  outside this semantic SOW. Both artifacts must work under the basename
  `iprange`.

Neither unknown blocks implementation. Performance ceilings are acceptance
evidence, and distribution selection does not change either contract.

### Production Interface Contract

#### Invocation modes

- `iprange [legacy options and inputs]` executes the released legacy grammar.
- `iprange --jsonrpc` starts the production JSON-RPC 2.0 stdio service.
- `--jsonrpc` cannot be combined with legacy operations or inputs. Invalid
  mixtures fail before reading stdin.
- SOW-0028 adds no `--v4`, daemon, TCP, HTTP, WebSocket, or remote-listener
  mode.
- In JSON-RPC mode stdout contains protocol messages only. Diagnostics and
  optional progress logging use stderr without changing typed outcomes.

#### Stdio framing and lifecycle

- stdin and stdout use UTF-8.
- One physical line is one complete JSON-RPC 2.0 request object or batch array.
  Embedded unescaped CR or LF is invalid. LF and CRLF input endings are
  accepted; output uses LF.
- Input and output frames have a fixed 1,048,576-byte ceiling in API v1. There
  is no command-line override. An oversized inline result is refused before
  any response bytes are written; bulk data uses files or cursors.
- The server continues after a well-framed method or parameter error. A parse
  error receives the standard parse-error response with `id: null`. An
  over-limit frame receives one typed transport error when possible and closes
  the service so discarded bytes cannot be misframed as another request.
- Clean stdin EOF stops accepting work, cancels queued work, lets the active SDK
  operation reach a truthful terminal state, closes all connection handles,
  flushes its final response when stdout remains writable, and exits.
- Broken stdout cancels work and closes handles. Signals use the same bounded
  shutdown path. Mutation results still distinguish not-committed, committed,
  published, and outcome-unknown states.

#### JSON-RPC behavior

- Every method begins with `iprange.v1.`. The `v1` component versions the
  product API independently of JSON-RPC 2.0 and on-disk v4.
- Requests accept string or integral numeric identifiers and echo them without
  coercion. Fractional and null identifiers are rejected. Production operation
  notifications are rejected because silently losing a mutation result is
  unsafe.
- `iprange.v1.cancel` is the only accepted client notification. Its
  `request_id` cancels active or queued work through the SDK token.
  Cancellation races return the factual terminal result.
- One connection executes one ordinary request at a time and queues bounded
  additional requests. The read loop stays active for cancellation and
  shutdown. Batch elements execute in array order and return the standard
  response array with notification elements omitted.
- Success uses `result`. Failure uses standard JSON-RPC errors for
  parse/request/method/parameter/internal failures and the reserved server
  range for product errors. Product errors carry stable `data.code`,
  `data.outcome`, and factual details. No generic retryable boolean invents
  policy the SDK does not own. Human text is not the machine identity.
- Address-family cardinalities and values beyond JavaScript's exact integer
  range are decimal strings. IP addresses are canonical text. Timestamp
  refresh/cutoff values are unsigned 32-bit JSON integers. Opaque metadata is
  carried as exact UTF-8, base64, or file bytes; the engine does not parse,
  validate, normalize, or merge it.
- `iprange.v1.system.describe` reports product version, implementation,
  JSON-RPC API, exact v4 format identity, platform, families, production
  methods, export formats, limits, and fault-worker availability. It exposes no
  test fields or internal storage identifiers.

#### Paths, bulk data, and outputs

- Stdio JSON-RPC has the operating-system authority of its parent. Method paths
  name local files. The future daemon adds its own authority policy without
  changing method semantics.
- `-` is not a bulk input/output path in JSON-RPC mode because stdin/stdout are
  reserved. Legacy mode retains existing stdin/stdout behavior.
- Source descriptors select legacy-compatible text/legacy-binary input, one
  immutable v4 feed, one pinned live v4 feed, one direct-value file, or one
  multi-feed v4 selection. Text descriptors carry applicable legacy family,
  prefix, network-fixing, DNS, file-list, and directory semantics.
- Large sources are parsed and submitted in bounded batches. Large results are
  written to caller-selected files. Responses carry paths, exact digests,
  counts, and factual reports, not complete feed bodies.
- V4 outputs use the SDK's private-final-output and atomic durable publication
  policy. Text and legacy exports use same-directory private files,
  flush/sync/close, atomic replacement policy, and platform directory
  durability. Request handlers never generate missing files.
- Operations take every page/heap/output/open-file limit required by
  `iprange-jsonrpc-v1.md`; API v1 has no omitted default or zero/unlimited
  sentinel.

#### Connection-owned read handles

- `iprange.v1.reader.open` opens an immutable reader or registers a live reader
  and returns an opaque connection-local handle and public database facts.
- `iprange.v1.reader.close` releases it. EOF, broken pipe, and shutdown close
  all remaining handles.
- Reader methods cover information, inline-or-file metadata, point/batched
  lookup, bounded feed-catalog enumeration, matching feeds, and bounded
  direct/feed/structured range cursors.
- Cursor tokens are opaque connection capabilities with batch limits and
  deterministic end/close behavior. They never expose physical identifiers.
- Handles/cursors are never accepted by another process or connection.
  Wrong-owner, closed, stale, kind, and family errors are stable product errors.
- Mutation methods identify source files and source mode; a reader handle is
  not their sole durable input, so requests remain self-contained.

#### Required production method families

The normative schemas are fixed by
`.agents/sow/specs/iprange-jsonrpc-v1.md`. The method inventory below is a
readability index, not permission to reinterpret that specification. Renaming,
omitting, adding, or changing a method or field is a product-design change that
stops implementation for user approval.

1. `iprange.v1.system.describe`
   - Discover versioned capabilities and limits.
2. `iprange.v1.reader.open`, `.close`, `.info`, `.metadata`, `.lookup`,
   `.feeds.open`, `.feeds.next`, `.feeds.close`, `.matching_feeds`,
   `.ranges.open`, `.ranges.next`, and `.ranges.close`
   - Repeated production reads and bounded scans over immutable/live files.
3. `iprange.v1.database.create`, `.initialize_live`, `.reset_live`,
   `.create.resolve`, `.live_transition.resolve`, `.live_residue.resolve`,
   `.reclaim`, `.info`, `.metadata.get`, and `.metadata.replace`
   - Explicit lifecycle and metadata. Creation specifies family, value kind,
     tag, structured kind, and reader capacity. Creation uses the SDK's fixed
     creator-only security. Metadata replacement is one logical commit.
4. `iprange.v1.current.publish`
   - Parse unordered/duplicate/overlapping current input and publish one
     immutable single-feed v4 file with exact normalized statistics.
5. `iprange.v1.direct.replace`
   - Replace a generic direct database from bounded range/value input.
6. `iprange.v1.retention.first_seen.refresh` and
   `iprange.v1.retention.last_seen.refresh`
   - Refresh exact timestamp-tagged live files from complete current-feed
     coverage. First-seen optionally writes an exact removals file; last-seen
     requires a cutoff. Each returns its own commit outcome; there is no false
     two-file atomic wrapper.
7. `iprange.v1.feeds.create`, `.replace`, `.delete`, `.rename`, and `.import`
   - Maintain named-feed membership by name, including empty feeds and
     name-translating multi-feed import. Internal identifiers stay private.
8. `iprange.v1.history.project`
   - Project all caller-supplied named windows from one last-seen source scan
     into one membership-file transaction.
9. `iprange.v1.query.cardinalities`, `.overlaps`, and `.matching_feeds`
   - Exact named/all-scope aggregation and point membership results.
10. `iprange.v1.join.direct` and `.membership`
    - Ordered analytical joins with bounded result files and uncovered-feed
      reporting.
11. `iprange.v1.algebra.count`, `.compare`, and `.publish`
    - Same-name multi-file union/intersection/exclusion count, comparison, and
      immutable v4 publication in preserved-feed or caller-named flat mode.
12. `iprange.v1.export`
    - Export immutable or pinned-live direct, membership, or structured state;
      a named feed/selection; or an algebra result. Required formats are
      canonical CIDR/netset, single-address/ipset, explicit ranges, CSV,
      line-oriented JSON records, and released legacy binary where that format
      supports the selected family/value shape. Budgets prevent accidental
      address-space expansion.
13. `iprange.v1.snapshot`
    - Produce a compact unsigned immutable v4 snapshot from explicit source
      mode and publication policy.
14. `iprange.v1.validate`, `.recovery.inspect`, `.recover`,
    `.commit.resolve`, `.publication.inspect`, `.publication.resolve`, and
    `.publication.residue.remove` (with the database lifecycle resolvers in
    family 3)
    - Explicit proof, recovery, outcome resolution, and safe cleanup after
      corruption/interruption. Recovery never silently replaces its source or
      promotes an ambiguous candidate.
15. `iprange.v1.maintenance.list` and `.maintenance.remove`
    - List/remove only SDK-authenticated abandoned scratch, reservation,
      publication-temporary, and platform housekeeping artifacts using
      identities returned by the list operation.

The registry excludes HTTP download/cache, archive extraction, scheduler
policy, source configuration, application metadata schema interpretation,
website JSON/CSV generation, public HTTP routing, signing, and trust policy.

#### Fixed implementation ownership

The implementer follows this module boundary. Moving a responsibility across
these boundaries requires evidence that the listed shape cannot preserve the
approved contract; product behavior still requires user approval.

- Rust product crate: `v4/rust/iprange-cli/`, added to the existing Cargo
  workspace, with binary name `iprange`.
  - `main.rs`: process startup and exact legacy/JSON-RPC mode selection only.
  - `legacy/`: language-local released parser, ephemeral interval algebra,
    formatting, DNS, file expansion, binary compatibility, diagnostics, and
    exits. It contains no v4 persistence logic.
  - `rpc/framing.rs`, `rpc/schema.rs`, `rpc/dispatch.rs`, and `rpc/session.rs`:
    bounded transport, strict v1 decoding/encoding, fixed registry, one-request
    executor/queue, cancellation, readers, cursors, and shutdown.
  - `rpc/handlers/`: small method-family adapters over public
    `iprange-livedb` APIs.
  - `io/`: shared streaming legacy-compatible text input and atomic bounded
    text/result output. Persistence handlers never use it to read or write v4
    database bytes.
- Go product command: `v4/go/cmd/iprange/`, with reusable non-exported command
  packages under `v4/go/internal/cli/` mirroring the Rust responsibilities:
  `legacy`, `rpc`, `handlers`, and `fileio`. It imports the public Go module for
  v4 work and does not reach into `v4/go/internal/{reader,writer,...}`.
- Common external qualification: `v4/cli/`.
  - `schema/`: strict request, response, and declarative-case schemas derived
    from the approved protocol spec.
  - `golden/`: ordinary production JSON-RPC requests/responses and legacy
    input/output expectations; no internal state.
  - `cases/`: implementation-neutral operation sequences and expected facts.
  - `run.py`: Python-standard-library process client, independent scalar
    interval oracle, filesystem assertions, mixed-producer matrix, and report.
  - `bench.py`: the same client with explicit timed regions, repetitions,
    correctness checks, resource capture, and machine-readable results.
  - `README.md`: exact commands, executable selection, fixture generation,
    platform limits, timing interpretation, and release gate.

The external case contract is also fixed:

- Each `cases/*.json` object has `schema:"iprange-cli-case-v1"`, a unique
  `name`, optional capability `requires`, deterministic `fixtures`, ordered
  `steps`, and final filesystem `assertions`. Unknown members are rejected.
- A fixture declares a work-directory-relative path and exactly one source:
  UTF-8 text, canonical padded base64 bytes, or a named deterministic generator
  whose algorithm and seed live in the external runner. Production binaries do
  not generate fixtures.
- An RPC step contains the exact production `method` and `params`, one expected
  result or error object, optional JSON-pointer captures, and artifact
  assertions. A legacy step contains an argv array, stdin fixture, exact exit
  status, stdout/stderr expectation, and artifact assertions.
- `$WORK/path` is the sole path placeholder. `$CAPTURE/name` is the sole dynamic
  value placeholder and refers to a prior explicitly captured JSON pointer.
  Substitution occurs before strict production-schema validation; unresolved,
  recursive, or context-wrong placeholders fail the case.
- Expectations distinguish exact values, independently modeled interval/value
  state, platform-selected golden alternatives, and intentionally ignored
  unstable human diagnostics. Differential equality is an additional
  assertion, never the oracle.
- The runner executes the same case unchanged against one absolute binary. A
  matrix manifest selects `c`, `rust`, `go`, `rust_to_go`, or `go_to_rust`; case
  files contain no implementation-specific branch.
- Benchmark manifests reference qualified correctness cases and add fixture
  scale, warmup count, measured repetitions, cold/warm state, timed step names,
  and metric requirements. They cannot weaken correctness assertions.

Canonical entry points are:

```text
nice python3 v4/cli/run.py --c /ABS/C_IPRANGE --rust /ABS/RUST_IPRANGE --go /ABS/GO_IPRANGE --matrix all
nice python3 v4/cli/bench.py --rust /ABS/RUST_IPRANGE --go /ABS/GO_IPRANGE --manifest v4/cli/benchmarks/release.json --output /ABS/REPORT.json
```

All executable arguments are absolute existing regular files. The runner
creates a fresh private temporary work directory by default; `--work-dir`
accepts only an existing empty directory and is never deleted by the runner.
Reports record binary SHA-256, product/version/capabilities, platform, case and
fixture identities, command, environment allowlist, and outcome. Environment
inheritance is restricted to documented locale, DNS-test, sanitizer, and
platform variables so one implementation cannot be selected accidentally.

Production adapters use the following owners:

- lifecycle/reclaim/metadata: public live lifecycle, reader, writer, and typed
  transaction APIs;
- current immutable output: `create_immutable_feed_v4/v6` /
  `CreateImmutableFeedV4/V6`;
- direct, first-seen, last-seen, feed lifecycle/import, and history: their
  public high-level workflows;
- cardinality, aggregation, joins, and algebra: public membership query/scope
  and algebra APIs;
- snapshot, validation, recovery, resolution, and maintenance: the matching
  public SDK operation and its factual result type;
- text/CSV/JSONL/netset/ipset/released-binary output: CLI file adapters fed by
  public reader/cursor values; they never inspect mapped bytes or physical
  storage.

For live workflow metadata, `keep` performs no metadata stage. When the
workflow changes content, replacement/clear is staged in that same draft before
commit. When the high-level workflow reports no content change, the adapter
uses one fresh typed transaction of the database kind for the metadata-only
operation. Every replacement commits because the SDK intentionally does not
read/decompress/compare old bytes; clear commits only when metadata was present.
This is still one logical method outcome and never reports a commit that did
not occur.

#### Fixed delivery order and worker stop conditions

1. Complete the schema/golden artifacts and make a fake server prove that the
   external client rejects malformed framing, missing fields, wrong integer
   encodings, false outcomes, and incorrect rows. Do not write product handlers
   until this sensitivity gate passes.
2. Build the Rust mode router and JSON-RPC transport, then implement read-only
   handlers, immutable publication/export, live workflows, and destructive
   recovery/maintenance in that order. Qualify each family before proceeding.
3. Implement and qualify the complete Rust legacy surface against the C oracle.
4. Freeze only independently modeled or C-authoritative expectations, never
   Rust-produced expected answers, then implement Go in the same family order.
5. Run same-language, cross-open, and mixed-live matrices before performance
   acceptance and documentation.

The worker stops and returns evidence instead of improvising when:

- the approved JSON-RPC schema contradicts a public Rust/Go SDK contract;
- an operation would require direct access to an SDK internal package/module,
  mapped bytes, physical identifiers, or a duplicate persistent algorithm;
- exact released legacy behavior is ambiguous after C source, wiki, and tests;
- a durable outcome cannot be represented truthfully by the specified result;
- a requested platform requires an unapproved fallback or weaker durability;
- SOW-0027 changes a public owner after SOW-0028 activation.

#### Legacy contract

- Both binaries implement all documented/tested legacy aliases, parser/input
  expansion, DNS, modes, formatting, binary formats, diagnostics, feature
  probes, version/help behavior, and exit codes.
- The C binary and `tests.d/` are the first oracle. Additional adversarial
  fixtures cover ambiguous inputs and platform differences not already pinned.
- Rust and Go do not execute or dynamically link C. Each uses a language-local
  parser/formatter adapter and its production v4 SDK owners for normalization,
  algebra, counting, and comparison wherever semantics match. Narrow
  compatibility behavior such as legacy reduction or architecture-native
  binary encoding stays isolated from the v4 engine.
- Legacy mode creates no persistent v4 artifact unless a JSON-RPC production
  method is selected. Private workspace is removed on success, error, signal,
  and EOF.

### Acceptance Criteria

- Rust and Go each build a standalone executable whose basename is `iprange`.
- The legacy suite passes through `IPRANGE_BIN`; Rust and Go match the C oracle
  for stdout, stderr classes, files, and exit status where required.
- `iprange --jsonrpc` implements the framing, lifecycle, standard JSON-RPC
  behavior, cancellation, typed outcomes, limits, handles, and fixed method
  registry above in both languages.
- CLI/RPC code uses public SDK facades and contains no second persistent tree,
  allocator, membership dictionary, timestamp transition, join, algebra,
  snapshot, validation, recovery, or publication implementation.
- All publisher workflows compose from production methods: current-feed
  publication; independent first/last-seen refresh; central/provider multi-feed
  updates; history projection; aggregation; joins; algebra publication;
  text/legacy export; validation, snapshot, recovery, and cleanup.
- A complete current feed, scan, or database page is never encoded in one frame
  or materialized by the adapter. Files and cursor batches stay bounded.
- Results expose stable exact counts, digests, commit/publication/cleanup states,
  and domain codes without test-only observation.
- Cross-language cases create every file kind with each producer and
  open/query/export/validate/transform it with both consumers. Mixed live cases
  run in both language directions using production reader handles and mutation
  methods.
- Crash/cancellation cases prove no partial replacement, false success after
  unknown outcome, unrelated rollback, unbounded residue, or reopen failure.
- One external suite under `v4/cli/` imports neither SDK, selects executables by
  absolute path, and runs identical cases against Rust, Go, and mixed pairs.
- Correctness cases use declared expectations or an independent scalar interval
  model before differential comparison.
- The external benchmark client uses production requests and legacy invocations
  without special methods, counters, generated data, or test builds. Fixtures
  are externally prepared before timing.
- Benchmarks cover startup and persistent JSON-RPC separately; IPv4/IPv6;
  cold/warm readers; point/batched lookup; current publication;
  first/last-seen; 421-feed replace/import; history; aggregation; joins;
  algebra; export; snapshot; validation/recovery; and the complete workflow at
  one-million-range accepted scale.
- Every timing sample verifies output facts/digest first and records wall/CPU
  time, throughput, peak RSS, file sizes, handle/descriptor high-water where
  supported, counts, and residue. Measured regions are explicit.
- Direct SDK benchmarks remain diagnostic baselines. The common external suite
  becomes the production-interface benchmark authority; copied language-owned
  scenario lists do not remain independent release authorities.
- Material differences outside the design target are profiled and explained.
  Release ceilings are recorded only after repeated measurements.
- Direct SDK, C ABI, corpus, source-graph, mmap, worker, crash, sanitizer,
  race/checkptr, static review, and authorized native-platform gates remain.
- Specs, help, README/wiki, packaging, conformance, benchmarks, and project
  skills describe delivered production behavior.
- No `update-ipsets` code, rewrite, language, deployment, or integration choice
  changes under this SOW.

## Analysis

Sources checked:

- `.agents/sow/specs/iprange-jsonrpc-v1.md`
- `.agents/sow/specs/design-iprange-engine.md`
- `.agents/sow/specs/binary-format-v4.md`
- `.agents/sow/specs/update-ipsets-v4-adoption-findings.md`
- `.agents/sow/current/SOW-0027-20260826-go-rust-v4-sdk-parity-reconciliation.md`
- `.agents/skills/project-v4-rust/SKILL.md`
- `src/iprange.c:97-400`, `run-tests.sh`, and `tests.d/`
- `v4/conformance/README.md` and `v4/conformance/cases.json`
- Rust/Go public facades, tests, workers, and update-ipsets benchmarks.
- `firehol/update-ipsets @ f299ee780dc0`
  - `pkg/scheduler/processing_loop.go:47-74`
  - `pkg/engine/run_pipeline.go:40-136`
  - `pkg/engine/finalize.go:41-61`
  - `pkg/engine/binary_write.go:11-51`
  - `pkg/engine/feed_body_stage.go:400-523`
  - `pkg/engine/retention_update.go:111-619`
  - `pkg/engine/output_comparison.go:62-134,205-256`
  - `pkg/engine/public_compose.go:11-89`
  - `pkg/iprange/cli_runner.go:1-110`
  - `pkg/iprange/cli_inputs.go:14-142`

Current state:

- The design spec assigns the released CLI role to C while Rust/Go are SDKs.
  This SOW changes that statement after both executables qualify.
- No Rust/Go v4 product executable named `iprange` exists. The trees contain
  fault workers and language-owned benchmarks.
- The legacy runner supports `IPRANGE_BIN`, and 100 functional test directories
  cover the established surface.
- Cross-open corpus evidence exists, while mixed-process orchestration and
  benchmark matrices remain split by language.
- The Go facade exposes the high-level workflow families of the Rust authority,
  subject to SOW-0027 final acceptance.
- `iprange-jsonrpc-v1.md` now fixes exact invocation, framing, common types,
  budgets, methods, bounded inline/file results, factual outcomes, and
  unsupported surfaces. It remains unsupported until both binaries qualify.
- Adoption findings say parsing/text export remain application responsibilities.
  This SOW moves reusable parsing/export into the product executable while
  leaving source policy and website behavior in the application.

Risks:

- Legacy parity includes DNS, permissiveness, file expansion, binary encoding,
  output bytes, diagnostics, and exits—not only algebra.
- RPC adapters can become duplicate SDKs if handlers reimplement workflows.
- Bulk JSON would erase mmap/bounded-memory advantages and invite giant-frame
  denial of service.
- Network transport adds another trust boundary; SOW-0029 isolates it.
- Disconnect during mutation can leave an unknown durable outcome. Results and
  resolution must never simplify unknown into success or failure.
- Rust/Go agreement cannot detect a shared copied defect.
- Startup, fixture generation, cache state, JSON, and validation can distort
  timings unless regions are explicit.
- SOW-0027 closed 2026-09-01 with functional parity accepted and
  performance acceptance denied; this SOW no longer conflicts with it.
  Its engine-level performance residuals remain owned by SOW-0030.

## Pre-Implementation Gate

Status: ready

Problem / root-cause model:

- Rust/Go expose required SDK operations but no common shipped application
  interface. Current qualification drivers are partly language-owned, so a
  consumer cannot exercise the complete workflow through one stable contract.
- The earlier draft incorrectly treated NDJSON and SDK reachability as testing
  contracts. The approved design makes JSON-RPC a production API, exposes only
  production-useful handles and file-level jobs, and makes tests normal clients.
- The gate was blocked only while SOW-0027 was active; SOW-0027 closed
  2026-09-01. Product design, method schemas, stdio boundary, daemon split,
  implementation ownership/order, and validation are resolved.

Evidence reviewed:

- Production API contract: `iprange-jsonrpc-v1.md`.
- Legacy behavior: `src/iprange.c:97-400`, `wiki/`, `run-tests.sh`, `tests.d/`.
- Logical ownership: `design-iprange-engine.md:75-106`.
- Cross-language requirements: `design-iprange-engine.md:418-449`.
- Publisher sequence: `design-iprange-engine.md:451-490`.
- Retention/feed/query/algebra/recovery needs:
  `update-ipsets-v4-adoption-findings.md:15-245`.
- Complete workflow:
  `v4/go/cmd/iprange-v4-bench/scenario_sdk.go:681-964` and corresponding Rust
  benchmark.
- JSON-RPC 2.0 specification, `https://www.jsonrpc.org/specification`
  (accessed 2026-08-28).

Affected contracts and surfaces:

- Rust product CLI crate and Go product command/adapter packages.
- Legacy CLI grammar, parsing, DNS, output, binary, diagnostics, and exits.
- JSON-RPC framing, methods, schemas, errors/outcomes, cancellation, handles,
  cursors, budgets, and shutdown.
- Publisher-facing v4 workflows in the fixed method registry.
- External cases, oracle, fixtures, mixed driver, benchmarks, reports, and
  accepted ceilings.
- Build/packaging, help, README/wiki, specs, project skills, CI/release gates,
  and SOW-0029's daemon dependency.

Existing patterns to reuse:

- Public SDK facades as semantic owners.
- Typed high-level workflows/outcomes in existing Rust/Go benchmarks.
- `v4/conformance/cases.json` for neutral cases and cross-open artifacts.
- Rust mixed-live orchestration for bounded waits and both directions.
- `IPRANGE_BIN` for legacy alternate-executable qualification.
- SDK errors, outcomes, cancellation, budgets, workers, and maintenance IDs.
- `update-ipsets/pkg/iprange` as Go legacy reference evidence, never an import.

Risk and blast radius:

- Compatibility, data-integrity, and performance risk are high.
- Security is bounded to a same-user subprocess, but frames, paths, metadata,
  damaged files, and cleanup identities remain untrusted inputs. Network
  exposure is prohibited.
- Portability spans paths, signals, locks, workers, RSS, and descriptor metrics.
- No v4 byte change or `update-ipsets` migration is authorized.

Sensitive data handling plan:

- Use synthetic documentation addresses, ranges, feed names, paths, and
  metadata. Do not record production feeds, credentials, tokens, operational
  hosts, customer/community data, personal data, identifying addresses, private
  endpoints, or proprietary incidents.

Implementation plan:

1. Import the SOW-0027 final parity ledger and accepted direct benchmark
   inventory (SOW-0027 closed 2026-09-01 with functional parity passed;
   its performance residuals are NOT reopened). Verify every method family
   has public owners in both SDKs.
2. Read and verify the approved `.agents/sow/specs/iprange-jsonrpc-v1.md`, then
   derive strict machine schemas and golden request/response examples from it
   before product code. The implementer may correct a proven contradiction
   with public SDK/spec evidence only by stopping for user approval; field and
   option design is not delegated to implementation.
3. Create `v4/cli/` as an external Python-standard-library client area with
   strict declarative JSON cases, scalar IPv4/IPv6 oracle, deterministic fixture
   builders, fake-server sensitivity, and absolute executable selection. It
   imports neither SDK and contains no production code.
4. Add the Rust product crate split by mode routing, legacy behavior, framing,
   schema, dispatcher, handle/cursor registry, file adapters, SDK handlers,
   errors, cancellation/shutdown, and help/version.
5. Qualify Rust first against C legacy, JSON-RPC, corpus, publisher workflow,
   resource, crash/cancellation, and release benchmarks. Fix SDK gaps in the SDK.
6. Add the pure-Go product command with the same responsibility split and
   observable contract, calling only the public Go SDK for persistence.
7. Run Rust-only, Go-only, both cross-producer directions, and mixed-live
   matrices. Consolidate production-interface benchmarks under `v4/cli/` while
   retaining direct SDK diagnostics.
8. Profile regressions, set measured ceilings, run static/mmap/full-codebase and
   authorized native-platform gates, update the engine/adoption specs and all
   other affected artifacts to delivered reality, and complete independent
   final review.

Validation plan:

- Run legacy tests against C/Rust/Go and add focused uncovered cases.
- Contract-test framing, standard errors, unknown keys, numeric limits, batches,
  notification rules, cancellation races, queue bounds, EOF, broken pipes,
  handle ownership, cursor exhaustion, and cleanup.
- Generate valid v4 files through production methods and externally corrupt
  copies. Test both producer directions and all file kinds.
- Compose the complete publisher workflow only through JSON-RPC and filesystem
  artifacts; verify per-feed failure isolation.
- Prove runner sensitivity against dropped/split ranges, changed values, lost
  empty feeds, truncated counts, wrong errors, missing responses, false
  commit/publication success, and leaked handles.
- Retain SOW-0027 SDK gates, C ABI, source graph, formatting/lint, mmap, workers,
  crash, sanitizer, race/checkptr, and full-codebase reviews.
- Build release binaries; prepare fixtures outside timing; separate startup and
  persistent service; repeat samples; validate every result; record cache state.
- Run native Windows/macOS/FreeBSD only after explicit authorization.
- Record expected cost before commands over two wall-minutes or ten core-minutes
  and run builds/tests/benchmarks under `nice`.
- Run SOW audit, hygiene, artifact, follow-up, and `project-final-review` gates.

Artifact impact plan:

- AGENTS.md: update goals, commands, JSON-RPC boundary, qualification authority,
  and daemon exclusion after implementation.
- Runtime project skills: update `project-v4-rust` for delivered CLI/RPC,
  qualification, benchmarks, packaging, and reviews; add Go guidance only if
  concrete reusable procedure warrants it.
- Specs: `iprange-jsonrpc-v1.md` is the approved implementation authority;
  update engine/adoption specs with delivered behavior; never change v4 bytes
  without approval.
- End-user/operator docs: update README, wiki/help, installation, compatibility,
  JSON-RPC examples, exports, outcomes, limits, recovery, packaging, platforms,
  conformance, and benchmarks.
- End-user/operator skills: none exist; reassess before close.
- SOW lifecycle: SOW-0027 closed 2026-09-01; this SOW is current.
  SOW-0029 owns daemon (pending); SOW-0017 owns authenticated snapshots
  (paused); SOW-0030 owns engine performance residuals (pending).

Open-source reference evidence:

- `firehol/update-ipsets @ f299ee780dc0`
  - `pkg/scheduler/processing_loop.go:47-74`
  - `pkg/engine/run_pipeline.go:40-136`
  - `pkg/engine/finalize.go:41-61`
  - `pkg/engine/binary_write.go:11-51`
  - `pkg/engine/feed_body_stage.go:400-523`
  - `pkg/engine/retention_update.go:111-619`
  - `pkg/engine/output_comparison.go:62-134,205-256`
  - `pkg/iprange/cli_runner.go:1-110`
  - `pkg/iprange/cli_inputs.go:14-142`
- JSON-RPC 2.0 specification, `https://www.jsonrpc.org/specification`
  (accessed 2026-08-28).

Open decisions:

- None for SOW-0028 implementation.
- Measured performance ceilings are acceptance evidence, not an open design.
- Default distribution selection is outside scope and does not block both
  implementation-specific `iprange` binaries.

## Implications And Decisions

1. **Layered proof** - user decision 1A on 2026-08-28.
   - Selection: one external suite plus retained SDK/C ABI/storage/fault/platform
     gates.
   - Implication: executable PASS does not replace internal-invariant evidence.
   - Recommendation class: long-term-best.
2. **Legacy compatibility** - user decision 2A and clarification on 2026-08-28.
   - Selection: both standalone binaries preserve released legacy behavior; C
     is the oracle, not a runtime dependency.
   - Implication: parser, DNS, formats, aliases, diagnostics, and exits are in
     scope.
   - Recommendation class: long-term-best.
3. **Publisher production boundary** - user clarification on 2026-08-28.
   - Selection: current, retention, multi-feed, export, query, algebra, snapshot,
     validation, recovery, and maintenance support shell-driven composition.
   - Exclusion: no `update-ipsets` rewrite/language/integration choice.
   - Recommendation class: long-term-best.
4. **JSON-RPC, not a test NDJSON API** - user clarification on 2026-08-28.
   - Selection: `--jsonrpc` exposes JSON-RPC 2.0 over a double pipe; newlines
     only frame stdio messages. Tests are ordinary clients.
   - Implication: correlation, outcomes, cancellation, readers, and versioning
     are permanent. No `--v4` or duplicate human database namespace exists.
   - Recommendation class: long-term-best.
5. **WebSocket daemon split** - user decision 1A on 2026-08-28.
   - Selection: SOW-0028 builds the reusable dispatcher and stdio service;
     SOW-0029 separately designs/implements `--daemon`.
   - Implication: daemon security cannot weaken or fork method semantics.
   - Recommendation class: long-term-best.
6. **Minimal test-only footprint** - user clarification on 2026-08-28.
   - Selection: no test methods, fixtures, generators, expected results,
     benchmark commands, or counters in production binaries.
   - Implication: external timings include real production work.
   - Recommendation class: long-term-best.

## Plan

1. Activate this SOW (SOW-0027 closed 2026-09-01; done).
2. Derive machine schemas, golden messages, and the external case format from
   the approved JSON-RPC v1 specification.
3. Implement and qualify Rust.
4. Implement and qualify Go.
5. Complete cross-language, crash, resource, and publisher-workflow proof.
6. Consolidate production benchmarks and establish measured ceilings.
7. Complete platform, artifact, docs, skill, and final-review gates.

## Execution Log

### 2026-09-01

- SOW-0027 closed as `closed` (functional parity passed, performance
  acceptance denied); SOW-0028 activated as the sole current SOW.
- Implementation start, fixed delivery order step 1: build the
  `v4/cli/` external qualification area (strict schemas, golden
  messages, declarative cases, runner, fake-server sensitivity gate)
  before any product code.
- `v4/cli/` foundation built and verified (runs in ~0.2s, no test
  behavior in any production binary):
  - strict schema package `v4/cli/schema/`: declarative engine,
    shared types, JSON-RPC frame/envelope validation, 53-method params
    registry, 52 result schemas (`iprange.v1.cancel` is a notification
    and intentionally has no result), declarative case schema;
  - external runner `v4/cli/run.py`: case fixtures, `$WORK/`/`$CAPTURE`
    substitution, strict JSON-RPC stdio client (unknown response
    members and malformed error objects rejected), result method-echo
    enforcement, lookup match order, cursor lifecycle and
    range/feed ordering checks;
  - fake server `v4/cli/fake_server.py` (importable; `serve()` under
    `__main__` guard) and sensitivity gate `v4/cli/sensitivity_gate.py`:
    13 modes = 3 positive controls + 10 deliberate-brokenness cases,
    all green;
  - fixed during bring-up: runner never created its JSON-RPC service
    (`AttributeError` on any rpc step); CRLF terminator handling in
    `schema/frame.py`; `--matrix` crashed with `KeyError` when a
    consumer binary was absent.
- Wire-result schemas rebuilt from the Rust SDK types (semantic
  authority): every result models the snake_case conversion of its
  public SDK type with depth-1 strictness and typed scalars
  (u64 decimal strings, u32 integers, [u8;16] hex ids, result value
  tags as `{"hex": ...}`, file identities as volume/file). Recording:
  - plain SDK enums convert to lowercase snake_case strings (value
    sets not re-listed; the JSON-RPC spec does not enumerate them);
  - payload-carrying enum results (ReclaimResult) convert with an
    explicit lowercase `kind` discriminator;
  - `cause` is never a success field (it becomes the error message);
  - recovery preparation failures are -32010 errors whose details
    carry the failure facts (recover success = complete
    RecoveryResult conversion);
  - ranges records carry the semantic `value` for direct/structured
    views and none for feed views; lookup matches carry the
    kind-specific fields;
  - query.matching_feeds reports the aggregate count as
    `matching_feed_count` (from MatchingFeedsReport);
  - validate, recover, and maintenance.list require JSONL output
    descriptors (CSV is documented unsupported for their rows).
- Value tags, metadata inputs, and metadata deliveries tightened to
  the spec's exactly-one-of forms; responses now require an id and
  reject unknown members/malformed error objects
  (`schema/frame.decode_response`).
- Golden corpus `v4/cli/golden/*.json`: 53 exchanges covering all 52
  request methods plus the cancel notification, each schema-validated
  at generation time and by `v4/cli/check_golden.py`.
- Initial declarative cases `v4/cli/cases/`: system.describe and four
  fixture-free server-error cases (invalid_path, handle_not_found).
- `v4/cli/check_golden.py`: CI-grade wire gate (validates every golden
  exchange and case file in well under a second; no binary needed).
- Rust crate `v4/rust/iprange-cli/` added to the workspace (binary
  `iprange`). Transport milestone qualified:
  - `main.rs`: exact legacy/`--jsonrpc` mode selection;
  - `rpc/framing.rs`: LF/CRLF line transport, 1,048,576 input/output
    ceilings, 65,000 response-object ceiling, -32001 shutdown path;
  - `rpc/schema.rs`: strict envelope decoding (params required on
    every request including cancel, unknown members rejected, string
    or integral ids only, notification rule), response encoding;
  - `rpc/session.rs`: worker-thread execution, 1 active + 16 queued
    admission bound (-32002), immediate cancel application, EOF
    shutdown that drains admitted units (client that sends then
    closes stdin still receives the factual response);
  - `rpc/dispatch.rs`: fixed 53-entry registry, per-method params
    validators (-32602), unknown methods -32601;
  - `rpc/handlers/system.rs`: system.describe advertising the 52
    callable methods in bytewise order;
  - qualified by the external runner (`PASS system.describe [rust]`)
    and direct framing probes (batch, CRLF, cancel, over-limit,
    invalid envelope).
- Read-only family implemented and qualified (worker + lead review):
  - dev-only fixture producer `v4/rust/iprange-livedb/examples/v4-fixture.rs`
    (kinds: direct-v4, membership-v4, structured-v4; deterministic
    content via public SDK APIs; the production `iprange` binary
    contains no fixture generation);
  - handlers: reader.open/.close/.info/.metadata/.lookup/.matching_feeds/
    .feeds.open/.feeds.next/.feeds.close/.ranges.open/.ranges.next/
    .ranges.close plus database.info/.metadata.get, registered with
    strict params validators in rpc/dispatch.rs; connection state in
    rpc/state.rs (64 readers / 64 cursors, closed-cursor tombstones,
    done-closes-cursor semantics, reader-close cascades to cursors);
  - cursor contract: direct/structured cursors reopen and seek from
    address checkpoints; feed cursors reopen and skip by range count
    (the public SDK feed-range cursors expose no seek); reverse
    iterations use exclusive checkpoints; every page is bounded by the
    65,000-byte response-object ceiling and the batch size;
  - error mapping: canonical SDK ErrorCode names become data.code
    (wrong_value_kind, wrong_address_family, handle_wrong_kind,
    handle_closed, invalid_argument, io, ...); outcomes use the
    documented factual set (not_started/read_only_failure);
  - metadata file delivery: atomic same-directory temp + fsync +
    hard-link (fail_if_exists) or rename (replace_*), directory sync,
    output_limit before publication; rows:"1" per output-fact schema
    for a single opaque metadata blob (decision noted in code);
  - decision recorded: `system.describe.methods` advertises exactly
    the callable methods (15) so capability gating is honest;
  - runner: `--fixture-tool` + `v4_fixture` generator with stable seed
    mapping (0=direct-v4, 1=membership-v4, 2=structured-v4), nested
    expectation matching, and case capability gating via `requires`
    (one describe probe per binary; SKIP, not FAIL, for unshipped
    families);
  - two future-family cases declare `requires` and skip until their
    families land; all 7 delivered cases PASS, check_golden and the
    sensitivity gate stay green; cargo tests 13/13.

### 2026-08-28

- Replaced the test-oriented NDJSON design with the approved production model:
  legacy CLI plus JSON-RPC 2.0 over stdin/stdout.
- Recorded operation families derived from v4 specs and `update-ipsets`.
- Recorded the decision to keep WebSocket daemon work in SOW-0029.
- Inspected Rust/Go facades, the complete publisher benchmark, C grammar,
  legacy tests, v4 specs, SOW-0027, and upstream workflow evidence.
- Added `.agents/sow/specs/iprange-jsonrpc-v1.md` as the normative production
  transport, schema, method, outcome, limit, and bulk-file contract.
- A design-readiness audit found that the earlier draft delegated public field
  design to implementation, exposed nonexistent SDK options, and left large
  response frames unbounded. The fixed spec now maps real public SDK budgets
  and modes, bounds both response objects and frames, and routes large results
  through files/cursors.
- The same audit caught the misleading SDK `*MetadataJSON` name: the v4 format
  requires opaque arbitrary bytes. The contract now preserves exact metadata
  through UTF-8/base64/file encodings and performs no JSON validation.
- No product code, end-user docs, fixture, manifest, or benchmark changed.

## Validation

Acceptance criteria evidence:

- Pending implementation. Design/user decisions are complete; SOW-0027 closure
  is the only activation dependency.

Tests or equivalent validation:

- `nice .agents/sow/audit.sh`: SOW-0028 and SOW-0029 pass their
  status/directory, regression-placement, open-source-evidence, and
  sensitive-data checks. The repository-wide verdict remains partial only
  because completed historical SOW-0025 lacks a canonical top-level status;
  this SOW does not modify that unrelated record.
- Placeholder, personal-name, trailing-whitespace, and `git diff --check`
  hygiene scans: pass.
- No product test/benchmark is claimed for a pending design-only SOW.

Real-use evidence:

- Pending; no production executable or JSON-RPC service exists yet.

Reviewer findings:

- Design-readiness findings were corrected: no schema invention remains in the
  worker plan; nonexistent create/recovery/publication/reset options were
  removed; full scratch/query budgets and factual resolution modes were added;
  and metadata/catalog/query/maintenance results are bounded.
- Independent implementation final review remains pending because no product
  implementation exists.

Same-failure scan:

- No production JSON-RPC contract or Rust/Go product `iprange` exists.
- Reviews must search for duplicate persistence algorithms, complete-feed JSON,
  test-only fields, text-as-error identity, false cross-file atomicity,
  unbounded queues/frames, leaked handles, and accidental listeners.

Sensitive data gate:

- This SOW contains public paths/protocol references and synthetic product
  descriptions only; no secrets, credentials, tokens, SNMP communities,
  customer/community/personal data, identifying addresses, private endpoints,
  or proprietary incidents.

Artifact maintenance gate:

- AGENTS.md: unchanged; implementation must update it before completion.
- Runtime project skills: unchanged; implementation must update them.
- Specs: added `iprange-jsonrpc-v1.md`; implementation must update the engine
  and adoption specs to link the delivered public contract.
- End-user/operator docs: unchanged; delivery requires updates.
- End-user/operator skills: none exist; reassess before close.
- SOW lifecycle: SOW-0028 is current/in-progress (sole current SOW,
  2026-09-01); SOW-0027 is closed; SOW-0029 tracks daemon (pending);
  SOW-0017 remains paused; SOW-0030 owns engine performance residuals
  (pending).

Specs update:

- Added `.agents/sow/specs/iprange-jsonrpc-v1.md`. It is approved but explicitly
  marked unsupported until SOW-0028 delivers both binaries.
- Engine/adoption integration remains part of implementation because those
  specs must describe delivered, not merely proposed, behavior.

Project skills update:

- Pending delivered workflow.

End-user/operator docs update:

- Pending delivered behavior.

End-user/operator skills update:

- None currently exist; reassess before close.

Lessons:

- A machine interface used by tests should first be a coherent production API.
- JSON-RPC supplies application semantics; newlines only supply stdio framing.
- A remote daemon is not security-neutral when methods mutate local artifacts.

Follow-up mapping:

- SOW-0027 supplies final parity and direct-performance input.
- SOW-0029 owns daemon transport/security/path/concurrency.
- SOW-0017 owns authenticated public snapshots.
- `update-ipsets` migration remains outside scope; no architecture was selected.

## Outcome

Pending.

## Lessons Extracted

Pending implementation and final review.

## Followup

- SOW-0027 closed 2026-09-01; this SOW is the sole current SOW.
- Implement WebSocket daemon separately in SOW-0029 after this API is accepted.
- Keep authenticated publication in SOW-0017.

## Regression Log

None yet.

Append regression entries here only after completion/closure and a later
regression. Never prepend regression content above the original narrative.

## Recorded implementation decisions (2026-09-01)

1. **Live reader mode — implement now.** `reader.open` advertises and accepts
   `source.mode:"live"` per `iprange-jsonrpc-v1.md`; the SDK exports
   `LiveReader` with the same operations as `ImmutableReader`
   (`v4/rust/iprange-livedb/src/live_reader.rs`). Removing live from the
   contract would shrink the shipped API below the spec; the live lifecycle
   handlers already land in this milestone.Entry: register live readers in the
   connection state and route info/lookup/metadata/cursors/close through the
   same handler code paths as immutable readers.
2. **Export worker adjacency — accepted.** Export source identity comes from
   public `inspect_recovery_candidates`, which requires `iprange-v4-worker`
   beside the `iprange` binary. A stat-based identity would duplicate SDK
   internals and is inexact on Windows. The shipped executable documents the
   adjacency requirement; the external benchmarks run with the worker present.
3. **65 KB response envelope — enforce on the complete response object.** The
   frozen Python authority validates the full JSON-RPC envelope (`jsonrpc`,
   `id`, `result`) against `RESPONSE_OBJECT_LIMIT`, so the Rust session must
   apply the ceiling after building the final envelope, including the request
   id, and translate oversized successes into `output_limit` product errors.
5. **Mixed-language producer/consumer matrix — executes when the Go
   binary exists.** The runner owns a producer step that invokes the
   fixture/export producer binary and feeds its output to the consumer
   binary under test; a missing Go binary records SKIP (reported, not
   silently dropped) instead of failing the Rust-only rounds.
4. **Batch busy errors — one ordered response array.** A batch whose frame
   exceeds the queue admits some members and rejects the rest with
   `server_busy`; the spec requires a single response array per batch, in the
   same order, omitting notifications and excluding standalone busy frames. The
   session defers busy errors into the batch response even when the frame must
   be dropped per-request.
