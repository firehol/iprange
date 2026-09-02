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

### 2026-09-01 (continued) — round-7 review, decisions, and fix batches

- Review round 7 at HEAD 137032ed: five own-model adversarial reviewers
  (wire contract, SDK ownership, correctness/stop conditions,
  performance/bounds, registry/records). Verdict FAIL: 4 P1 + 8 P2 +
  2 P3 findings, reproduced in the SOW open-findings list below.
- User decisions (recorded 2026-09-01):
  - D1 = B: join.direct rows per feed ascending direct value with the
    uncovered cell LAST; Rust and Go SDKs both changed (spec:764 is the
    contract; the SDK sort key became (feed, direct==0, direct));
    covered cells with real wire value 0 are distinct from the null
    uncovered cell, pinned by new tests in Rust provider_joins.rs and
    Go join_direct_emit_test.go.
  - D2 = B: feeds.delete and feeds.rename return NO WorkflowReport
    (the SDK deliberately exposes none; WorkflowReport is limited to the
    six finish-input workflows). Results carry commit, metadata, and
    writer-close facts only; schema, goldens, and the spec now say so.
  - D3 = A: publication.resolve accepts a complete caller-supplied
    publication_result; the wire schema carries the complete mechanical
    PublicationAttempt (nested identities, previous-destination
    evidence, basename bytes, policy, creation-security evidence) so the
    SDK resolver can consume it as authority. Implemented by parallel
    worker (commit after 0bb7adc8).
  - Mandatory corrections accepted: value_tag.hex stays lowercase and
    the validator now accepts exactly 0-9a-f (the accepted defect was
    g-z, not uppercase); removal-output temporary cleanup is explicit on
    every terminal path and its failure is reported (no destructor
    guarantees); goldens are illustrative - oracle-driven declarative
    cases for every method family are required before the next review.
- Fix batch A committed at 3c3aab8d (SDK: join order + tests, worker
  availability probe, publication constructors; CLI: close facts,
  bounded metadata file reads, hex validator, housekeeping state,
  RemovalCollector explicit discard, fault_worker probe) and 0bb7adc8
  (wire base64 encoder). Validation: -D warnings build clean; Rust
  workspace 50 suites green (two consecutive runs); Go internal/reader
  58 tests green; golden + sensitivity gates pass.
- Review-round severity correction: the initial round-7 summary said
  "1 P1 + 8 P2 + 2 P3"; the verified counts are 4 P1 + 8 P2 + 2 P3
  (P1: unbounded metadata read, fabricated feed-delete report,
  unusable publication.resolve evidence path, join.direct order).

### 2026-09-01 (continued) — D3 evidence, hot paths, and case-driven bug fixes

- D3-A delivered at 6d9b6066: complete reversible publication evidence
  (publication_evidence.rs encoder/decoder with unit tests, publish and
  snapshot producers, publication.resolve supplied path, PUBLICATION_ATTEMPT
  result/param schemas, goldens). The adapter-owned removals publication
  carries only publication + destination_content (no fabricated SDK
  attempt; schema, spec, goldens updated). Hot row writers reuse one line
  buffer per sink instead of allocating per row (aggregation, joins,
  matching feeds, removals, direct CSV).
- Oracle-case authoring (round-7 P2-1: zero coverage for the 32 new
  methods) immediately found three real defects, fixed at 39a236e3:
  join.membership panicked on dense-scope lookup of an out-of-scope feed
  (0-based position underflow); algebra.count/compare/publish passed the
  outer source entry instead of the inner source member (missing-path
  panic); commit/create resolution decoded an empty cleanup ledger as
  requiring cleanup.artifacts while producers emit {}.
- Validation at 6d9b6066/39a236e3: -D warnings build clean; workspace 50
  suites green; runner 9 cases / 13 oracle checks; golden + sensitivity
  PASS.

### 2026-09-01 (continued) — round-7 close-out: oracle cases, budget codes, publisher dedup

- Per-family oracle cases delivered (round-7 mandatory correction): 15 new
  declarative cases under v4/cli/cases/ (join.direct with real wire value 0
  and null-last ordering, join.membership, query.cardinalities/overlaps/
  matching_feeds, algebra.count/compare/publish, feeds.lifecycle asserting
  delete/rename carry NO report member, live.lifecycle, direct.retention,
  maintenance, validate.recover, publication.json, history.project). The
  runner now executes 24 cases with 13 oracle-backed checks; authoring the
  cases exposed three real SDK/handler defects fixed at 39a236e3.
- commit.resolve result flattened (result IS CommitResolutionResult per
  spec; schema results.py + snapshot_validation.json golden updated); the
  runner exempts snapshot_to from the fabricated source_close requirement
  (the SDK result has no close facts).
- Budget-refusal wire codes (round-7 P2, Herschel) settled: SDK-domain
  budget refusals always use the canonical SDK code
  (`insufficient_resource_budget` / `work_limit_too_small` via
  reader::sdk_code); `output_limit` is used only where the spec names it
  (response-frame, inline metadata delivery, matching-feeds refusal, cursor
  rows, lookup) and for adapter-side guards on adapter-owned outputs
  (export expansion refusal, removals result budget, validation/recovery
  JSONL output mapping, which round-trips BudgetExceeded <-> output_limit
  through the same adapter).
- Duplicate publisher machinery (round-7 P3) removed: the algebra and feeds
  handler families each carried byte-identical copies of the prepared-draft
  finalization path (CommitDraft trait + PreparedWorkflow impl,
  publish_changed, publish_no_change, finish_publisher, workflow_failure,
  finish_writer_error, close_writer) plus duplicate fact converters
  (workflow_report, durability_outcome, logical_change, workflow_kind in up
  to five handler files). The single authority now lives in
  v4/rust/iprange-cli/src/rpc/handlers/workflow.rs; families keep only
  their own CommitDraft impls (PreparedFeedChange, PreparedHistoryProjection)
  and live.rs/maintenance.rs/algebra.rs/feeds.rs import the shared
  converters. durability_outcome moved next to the other commit-fact
  converters in lifecycle.rs. Net -197 lines; -D warnings clean; 50 Rust
  suites; runner 24 cases / 13 oracle checks; golden + sensitivity PASS.
- Validation at 0646eba5: full gate set green (Rust workspace 50 suites,
  -D warnings build, golden 53 exchanges, sensitivity 13 modes, runner
  24 cases / 13 oracle checks, Go suite incl. the D1-B uncovered-last
  order test).

### 2026-09-01 (continued) — round-8 re-review and fix wave

- Round-8 re-review at HEAD 91e3e57b (five own-model reviewers: wire
  contract, SDK ownership, performance/bounds, registry/records,
  coverage/oracles). Verdict FAIL with verified findings, all fixed:
  - P2 (performance): export row writers allocated per row; selection/
    pairs validators were O(n^2) Vec::contains. Fixed at 89715d23:
    one caller-owned line buffer per export format (push_address,
    push_ranges_line, write_json_value), HashSet uniqueness.
  - P2 x2 (coverage): the independent interval oracle fired only for
    reader lookups (13/13; algebra count/compare never against a real
    binary), and five methods plus the metadata.replace success path
    had no live case. Fixed at 061ded50: v4-fixture direct-csv /
    membership-csv build text-defined databases, the runner registers
    their intervals and runs the oracle self-test, and six new cases
    (reader.info, export.netset, snapshot, live.transition,
    database.metadata, algebra.oracle) execute every remaining method.
    Runner corpus is now 30 cases / 15 oracle checks.
  - P1 (wire/SDK): three independent PublicationResult encoders used two
    vocabularies (`destination_content` created|desired, `later_canonical`
    absent|none); publication.resolve rejected preserved snapshot and
    algebra.publish evidence, and cleanup/coordination shapes diverged.
    Fixed at the current HEAD: publication_evidence.rs is the single
    encoder/decoder authority with the spec-named vocabulary
    (desired/previous/absent/other/unclassified; none/...), matching
    coordination cleanup {"kind"|{}} and hex artifact basenames;
    goldens already documented the canonical vocabulary; oracle cases
    pinned and extended (snapshot.json now resolves its own preserved
    evidence through publication.resolve).
  - P1 (SDK ownership): RemovalCollector created its private temporary
    before fallible pre-work, leaking it on reader-open/info and
    writer-open failures. Fixed: the temporary is created after all
    fallible pre-work; the existing outcome discard covers every later
    path.
  - P2 (wire): history.project dropped the factual live reader close.
    Fixed: the close result threads through every projection outcome
    (source_closes on success; details on product errors).
  - P2 (SDK ownership): metadata replace_file stat-then-read TOCTOU
    window allowed unbounded reads. Fixed: read_bounded caps the read.
  - P2 (records): the round-7 findings list was promised but absent;
    the "Net -246 lines" claim was not reproducible. Fixed: "## Review
    round 7" section above reconstructs all 14 items with fix-commit
    mappings and provenance; the net claim corrected to -197.
  - P3: golden system.json fault_worker.protocol corrected to the
    control-protocol constant "1"; system.describe probes the worker
    once.
- Validation at current HEAD: -D warnings clean; Rust workspace 50
  suites (664 tests); runner 30 cases / 15 oracle checks; golden PASS;
  sensitivity 13 modes; Go suite green.

### 2026-09-01 (continued) — round-8 delta re-review (all five PASS)

- The five own-model reviewers re-audited the round-8 fixes at
  d324518c. Coverage, SDK-ownership, and registry/records scopes PASSed
  immediately; performance and wire scopes returned two residuals,
  both fixed and re-verified PASS:
  - P2 (performance, Turing scope): export structured/feed views still
    converted every segment to an owned serde_json Value (value_json)
    and deep-cloned it into the merge slot. Fixed at ec7b6f7f: the
    segment sink now moves one owned ExportValue per segment into a
    move-based pending slot (no conversion, no retention deep clone);
    write_row formats Direct/Structured/Feeds straight into the reused
    line buffer (push_json_string mirrors serde_json escaping, output
    byte-identical; runner export.netset and golden confirm).
  - P2 (wire, McClintock scope): history.project still dropped the
    live-reader close result when the projection error and the reader
    close failure coincided: workflow_failure replaced the error's
    existing details (struct-update), discarding the merged source_close
    one hop after the d324518c fix. Fixed at 54b099dc: workflow_failure
    and finish_writer_error now merge writer_close (and the completed
    report) into the existing details via merge_writer_facts, with two
    unit tests pinning that pre-existing source_close facts survive.
  - P3 (SDK-ownership scope): golden publisher.json pinned the
    adapter-owned removals destination_content as "desired"; the
    emitter and the oracle case produce "created". Fixed at ec7b6f7f:
    golden corrected to "created".
- Delta verdicts at ec7b6f7f/54b099dc: five of five reviewers PASS; no
  P0-P2 findings remain open.
- Validation at 54b099dc: -D warnings clean; Rust workspace 50 suites
  (671 tests, incl. two new workflow merge tests); runner 30 cases /
  15 oracle checks; golden 53; sensitivity 13 modes; Go suite green.

### 2026-09-02 — round 9: glm-5.3-responses whole-milestone review (FAIL) and fix wave

- The mandated glm-5.3-responses final review of SOW-0028 milestone 1 at
  3dc1b754 returned FAIL. All recorded gates were verified genuine; the
  review found defect classes the five own-model scopes had missed.
  Every finding below is fixed in this round; gates re-run green on the
  converged tree (workspace 683 tests / 0 failures, -D warnings clean,
  runner 30/15, golden 53, sensitivity 13, Go green, source graph 491).
- P1 transport (session.rs, one event-loop rework):
  1. Cancel with an unknown id permanently poisoned that request id for
     later requests; cancellation now tracks only admitted pending ids
     and prunes them at the terminal state.
  2. stdin EOF did not cancel queued requests (a fresh token replaced
     the cancelled one per unit); a shutting_down state now skips
     queued units while the active one completes factually.
  3. Broken stdout was ignored (writes discarded); write failures now
     raise a Fatal transport event that runs the EOF-equivalent
     cancellation/cleanup path and exits non-zero.
  4. No termination-signal handling existed; SIGINT/SIGTERM now feed the
     same Fatal path via a libc sigwait watcher (cfg(unix), libc dep
     added target-gated).
- P1 history.project wire contradiction (spec permits 4096 windows; a
  complete inline report cannot fit the 65,000-byte response object; a
  valid request committed then was misreported as
  output_limit/read_only_failure dropping commit facts). Fixed with the
  swarm-adjudicated option A: a pre-mutation worst-case preflight
  (algebra.rs preflight_response/preflight_history_result, real feed
  names, longest encodings, echoed request id) refuses with
  output_limit/not_started before any writer is opened; the spec now
  states the general refusal rule for mutating methods and names
  history.project and algebra.publish (same class: its result scales
  with the live-source count after the destination is published) as
  current instances; read-only methods keep the legal post-hoc bound.
  The projection-report file/cursor option is tracked in pending
  SOW-0031 instead of being implemented now.
- P1 full-IPv6 export cardinality: export address accumulation used a
  saturating u128, reporting 2^128-1 for ::/0; it now accumulates
  Cardinality129 exactly (ranges/netset serialize the exact decimal;
  legacy-binary refuses full-IPv6 up front because the released v6
  header stores unique-ips in u128) with a regression test.
- P2 wire strictness: optional evidence members accepted as JSON null
  across publication/lifecycle/resolution decoders; present-but-null is
  now rejected everywhere (absence is the only absent form), pinned by
  unit tests. The Python result schemas accepted invalid vocabulary
  and opaque shapes (e.g. publication "banana"); results.py now
  enumerates every normative enum (publication, destination_content,
  later_canonical, access policies, live lineage, durability, close and
  abort outcomes, workflow kinds, meta selection, artifact kinds,
  housekeeping states/roles/presence) and recursively types cleanup,
  coordination_cleanup, housekeeping, and commit artifacts; the
  commit.resolve param schema is the strict COMMIT_RESULT (golden and
  live.lifecycle case corrected to the complete commit_result the
  decoder already required).
- P2 arg parsing: `--jsonrpc` mixed with other arguments fell back to
  the legacy stub; it now fails startup with exit 1 before legacy
  dispatch.
- P2 factual-close gaps: first-seen removal-collector creation failure
  after writer open now closes the writer and reports writer_close;
  export double fault (export and source-close failures together) now
  merges the close fact into the export error details.
- P2 performance: export selection uniqueness is HashSet-based;
  per-row/per-segment export allocations eliminated (ExportValue::Feeds
  is Arc<[String]>; structured-CSV quoting reuses one caller-owned
  scratch buffer); reader.lookup uses the SDK point membership query
  instead of one catalog scan per address; read_bounded reserves only
  the observed file length (cap retained).
- P3: fail_if_exists export publishes remove the private temporary
  before the directory sync; the io domain sweep found no further
  request-scaled mutating inline results (recover writes its report to
  a file; read-only methods keep the post-hoc bound legally).

### 2026-09-02 (continued) — round-9 delta: error-path close sweep (335fac6c..6b1837f4)

- The own-model delta reviewer (Linnaeus) returned two fresh P2s after
  the round-9 wave at 3aedb22a; both are error-path evidence gaps in
  the same class as the round-9 close-fact finding:
  - P2-1 writer opened before the fallible source open: feeds.import,
    feeds.create/replace, and history.project returned the source-open
    error without closing the already-open live writer. Fixed: the
    source-open error now returns close_writer_facts(&mut writer,
    error), merging the factual writer_close into the error details.
  - P2-2 query/join/algebra error paths dropped factual live-reader
    closes: query.cardinalities/overlaps/matching_feeds, join.direct,
    join.membership, algebra.count/compare/publish, and open_sources
    returned product errors with the opened readers dropped unclosed
    (no source close fact, stale sidecar slot until the next
    claim/scan). Fixed with one shared error-path owner:
    reader::close_on_error closes every opened reader and merges the
    factual close results as `source_closes` into the error details;
    a failed close keeps its source_close fact with the primary error
    (double-fault merge, same pattern as export). Every handler wraps
    its post-open body in an immediately-invoked closure; success
    tails (close_reader/close_readers) are unchanged.
- Same-class instances found and fixed in the same sweep:
  - database.info / database.metadata.get error paths close the
    ephemeral reader (reader.rs).
  - retention first_seen/last_seen refresh: writer-open failure,
    reader.info() failure, removal-collector creation failure, and
    refresh begin/drain failures now close the source reader (and the
    writer where open) via close_refresh_facts, merging both facts.
  - reader.open: info() or handle-allocation failures after the open
    close the reader before responding.
  - feeds workflow family: collect_workflow_facts now closes the
    source reader on every outcome (mirrors collect_projection_facts),
    the factual close rides Failed/NoChange/Changed facts and is
    merged into publish-stage and workflow error details (source_close)
    alongside writer_close; the ReaderCloseFailed variant is now
    reachable; double-fault preserves the close result.
  - export: the product error now keeps the factual source_close
    whether the source close succeeded or failed.
- Feeds success results kept the _PUBLISHER_COMMON shape without a
  source_close member during round 9, so the feeds close fact appeared
  only in error details. That was a schema omission, not a frozen user
  decision; round 10 aligns the success schema with the spec
  factual-close rule (iprange-jsonrpc-v1.md:351-357) by adding the
  optional source_close member to feeds.create/replace/import and the
  retention refreshes.
- Pinned by tests (af728001): close_on_error merges factual live
  closes and omits facts for immutable readers; an end-to-end handler
  test proves query.cardinalities on a direct live database carries
  source_closes in its wrong_value_kind error.
- Five own-model delta reviewers re-verified the final HEAD
  6b1837f4: coverage, SDK-ownership, wire, and performance scopes PASS;
  no P0-P2 findings remain open.
- Validation at 6b1837f4: -D warnings clean; Rust workspace 50 suites
  (687 tests); runner 30 cases / 15 oracle checks; golden 53;
  sensitivity 13 modes; Go suite green; source graph 491.

### 2026-09-02 (continued) — round 10: glm-5.3-responses whole-milestone re-review (FAIL) and fix wave

- The mandated glm-5.3-responses whole-milestone review at HEAD
  fe220f17 returned FAIL with 6 P1, 13 P2, and 3 P3 findings. The
  round-10 fix wave is being committed by the lead across the W1-W5
  fix areas; the validation gates are re-run after the wave commits
  land (recorded in the Validation section).
- P1 findings:
  1. scope-surface: the review scope included later-order deliverables
     (the full Rust legacy surface, `Go cmd/iprange`, `v4/cli/README.md`,
     `bench.py`, and benchmark manifests). Adjudication: milestone 1 is
     the Rust JSON-RPC service, the scope of the round-8/9 reviews; the
     later surfaces are fixed delivery-order work of this SOW, not
     milestone-1 blockers: step 3 "Implement and qualify the complete
     Rust legacy surface against the C oracle."; step 4 "Freeze only
     independently modeled or C-authoritative expectations, never
     Rust-produced expected answers, then implement Go in the same
     family order."; step 5 "Run same-language, cross-open, and
     mixed-live matrices before performance acceptance and
     documentation." (fixed delivery order lines 454-458).
  2. EOF-loss: queued/active work state lost on stdin EOF or
     termination; fixed in the round-10 transport wave (W1).
  3. export post-mutation output_limit: the export result can exceed
     the response bound after the destination is published; fixed in
     the round-10 export wave (W3).
  4. EOF reader-close: open readers not factually closed on EOF;
     fixed in the round-10 session/reader wave (W1).
  5. feeds/retention source-close omission: success results of
     `feeds.create`, `feeds.replace`, `feeds.import`,
     `retention.first_seen.refresh`, and `retention.last_seen.refresh`
     now carry the optional factual `source_close` per the spec
     factual-close rule (iprange-jsonrpc-v1.md:351-357): Rust emission
     (W3) plus the result schema (W5). This was a schema omission, not
     a frozen user decision; the round-9 delta note above that called
     the `_PUBLISHER_COMMON` shape frozen is corrected.
  6. quadratic feed cursors: feed cursor enumeration work grows
     quadratically with pages; fixed in the round-10 cursor wave (W4).
- P2 findings:
  1. cancel validation: cancel admission/unknown-id handling; fixed in
     the round-10 transport wave (W1); the Python CANCEL params schema
     is correct and unchanged.
  2. stdin-io-as-EOF: stdin I/O errors were treated as clean EOF;
     fixed in the round-10 transport wave (W1).
  3. multi-reader close facts: some multi-reader error paths still
     dropped close facts; fixed in the round-10 close-facts wave (W2).
  4. schema OPAQUE shapes: lifecycle results still used generic OPAQUE
     housekeeping/cleanup members and the container housekeeping state
     used the artifact vocabulary; fixed in results.py (W5): every
     housekeeping/cleanup member in create/transition/residue/commit-
     resolution/publication-residue results now uses the typed
     HOUSEKEEPING/CLEANUP/COORDINATION_CLEANUP schemas, the container
     state vocabulary (crash_reappearance_possible/visible) is distinct
     from the per-artifact vocabulary, and negative schema self-tests
     reject fabrications such as {"housekeeping": {"banana": "jar"}}.
     iprange-jsonrpc-v1.md enumerates no value sets for the lifecycle
     enums (state, operation, reset_policy, status, kind, resolution,
     local_file_relation, coordination), so those members stay open
     strings by the documented modeling rule.
  5. run.py work-dir collisions: concurrent cases could share a work
     directory; fixed in the round-10 runner wave (W4).
  6. per-line allocations: an export row writer allocated per line;
     fixed in the round-10 export wave (W3).
  7. metadata inline materialization: inline metadata delivery
     materializes the full value; fixed in the round-10 reader wave
     (W2).
  8. export publish outcome: export publication outcome facts on
     failure paths; fixed in the round-10 export wave (W3).
  9. clippy: not a repository gate; adjudicated below (W5 record).
  10. rustfmt: not a repository gate; adjudicated below (W5 record).
  11. stale validation records: the canonical Validation section still
      described a pending design-only SOW; replaced with current
      milestone-1 evidence (W5, see Validation section).
  12. reviewer-record incompleteness: fixed by this round-10 record and
      the corrected round-9 delta note (W5).
  13. oversized-id: request-id bound not enforced; fixed in the
      round-10 transport wave (W1).
- P3 findings:
  1. Cardinality129 internal truncation: a private counter path
     truncates 129-bit values; fixed in the round-10 cardinality wave
     (W3).
  2. push_json_string UTF-8: the row writer escapes some UTF-8
     incorrectly; fixed in the round-10 export wave (W3).
  3. pycache untracked: `v4/cli/__pycache__/` and
     `v4/cli/schema/__pycache__/` appeared as untracked files; added
     `__pycache__/` to `.gitignore` (W5).
- Fix mapping: the finding numbers above map to the W1-W5 fix areas as
  marked. The complete round-10 wave landed as one commit: 061f8c71
  (v4/rust/iprange-cli transport/session/handlers, iprange-livedb
  feed_range_cursor.rs, v4/cli/schema/results.py, v4/cli/run.py,
  spec, SOW records, .gitignore).
- P2-9/P2-10 adjudication (clippy and rustfmt are NOT repository
  gates): the CI workflows `.github/workflows/v4-rust-performance.yml`
  and `.github/workflows/big-endian.yml` run no clippy or fmt job; the
  recorded "-D warnings clean" gate is rustc `-D warnings`
  (`v4/rust/check-source-graph.sh` sets `RUSTFLAGS=... -D warnings`),
  which passes. `v4/rust/README.md` lists `cargo clippy` and
  `cargo fmt --check` as developer commands only. The attested clippy
  `large_enum_variant` suppressions carry allocation-free rationale
  comments and remain accepted non-gate hygiene debt.
- The five own-model delta reviewer scopes (coverage, SDK-ownership,
  wire, performance, and the close-facts delta reviewer) returned PASS
  at 6b1837f4 and re-affirmed PASS at fe220f17; no P0-P2 finding from
  those scopes remains open.
- Feeds/retention source_close correction record: the optional
  source_close member is added to the result schemas of
  feeds.create/replace/import and the two retention refreshes; goldens
  use immutable fixtures and stay valid unchanged (absent member).

### 2026-09-02 (continued) — round 11: five own-model delta re-review (FAIL) and fix wave

- The five own-model delta reviewers re-verified the round-10 wave at
  HEAD 061f8c71 and returned verified P2 findings; the round-11 fix
  wave at HEAD b41550f3 fixes every finding:
  1. Retention refreshes dropped the captured factual `source_close`
     on error paths (close-facts and SDK-ownership scopes, identical
     finding). Fixed in live.rs: all four run_* drivers merge
     `source_close` into error `details` beside `writer_close` on
     drain/finish failures; `publisher_value` merges it on every error
     return (staging, metadata transactions, writer.close failure,
     commit/durability, close_incomplete) via one shared
     `merge_source_close` helper. Two new end-to-end tests: a live
     first-seen refresh whose removals budget fails
     `finish_input_with_removals_v4` after the reader closed carries
     both `writer_close` and `source_close`; `publisher_value` with a
     staging error preserves the fact.
  2. `feeds.next` catalog paging was quadratic (performance and
     SDK-ownership scopes). Fixed: `FeedCursor::seek_by_index` added
     to iprange-livedb (one bounded B+tree seek per page,
     at-or-after policy, `seeked` flag keeps the full-sweep count
     health check for unseeked cursors); `feeds_next` opens one cursor
     per page and seeks to last+1 instead of re-walking the prefix.
     Equivalence test: paging a 2000-feed catalog matches one
     unbounded sweep with one tree look-up per page.
  3. Structured enumeration was O(N x F) (performance scope):
     `threat_feed_names` opened a fresh catalog cursor per record and
     called `contains_index` for every feed. Fixed: one catalog
     snapshot per page/stream (`build_feed_snapshot`) plus one
     reusable membership-word buffer; names resolve from set bits in
     word order with catalog-order mapping. Used by structured
     `ranges.next`, structured export, `reader.lookup`, and
     `matching_feeds`; export stream pinned byte-identical. A
     `cfg(test)`-only counter proves one sweep per page.
  4. Closed-handle tombstones grew unboundedly (performance scope).
     Fixed: FIFO-bounded closed-reader/closed-cursor tombstones
     (1024 per family production, 8 under cfg(test)); an evicted
     closed handle answers `handle_not_found`/`cursor_not_found`
     (spec-permitted; handles are random 128-bit values never
     reused). Unit test pins the bound.
  5. Stale SOW validation records and unfilled fix-mapping SHA
     (coverage scope). Fixed in this record: the round-10 mapping now
     names commit 061f8c71 and the Validation section records the
     re-run at 061f8c71; the round-11 re-run is recorded in the
     Validation section below.
  6. `iprange.v1.cancel` had no live behavioral coverage (coverage
     scope). Fixed: notification-capable runner step (schema/cases.py
     `"notification": true`, cancel-only, no expect/capture/assert;
     run.py `notify()` never reads a response), new case
     `cancel.unknown_id` (cancel unknown id then correlated
     `system.describe` succeeds; cancel an already-terminal id then
     describe succeeds again), and sensitivity mode `cancel_replies`
     (server answers a notification; client rejects via stream
     desync). Runner is now 31 cases / 15 oracle checks; sensitivity
     gate is 14 modes.
  7. `check_source_close` missed nested `current.source` (coverage
     scope). Fixed: the checker now inspects `params.source`,
     `params.current.source`, and `params.last_seen.source`; requires
     the close member for live sources and forbids it for immutable
     ones (history.project uses `source_closes`); `live.lifecycle.json`
     pins `source_close` on the live feeds.create result and
     `history.project.json` pins `source_closes` presence on the live
     projection path.
  8. `HOUSEKEEPING_ARTIFACT.ordinal` schema/emitter mismatch (wire
     scope). Fixed: schema uses C.U32, consistent with the modeling
     rule (u32 values are JSON integers) and the SDK/emitter.
  9. Batch frame with one unanswerable request id silently dropped
     every sibling (wire scope). Fixed in session.rs: unanswerable
     elements answer -32001 with id null IN POSITION inside the batch
     array via a new WorkEntry::Unanswerable variant; siblings
     execute; single requests keep the standalone shape; unanswerable
     entries never occupy queue capacity and are never cancellation
     targets. Five unit tests added.
- Validation at b41550f3: Rust workspace 50 suites / 726 tests, 0
  failures; `-D warnings` clean; source graph 491 sources; runner
  31 cases / 15 oracle checks; golden 53; sensitivity 14 modes; schema
  self-tests pass; Go SDK suite green; mmap storage/runtime/
  architecture gates pass; SOW audit and hygiene scans clean.

- Five own-model delta reviewers re-verified the round-11 fixes at
  b41550f3 and 60b95b0c: wire (Boole), close-facts (Ohm),
  performance (Tesla), coverage/oracles (Pauli), and SDK-ownership
  (Ramanujan) scopes all PASS; no P0-P2 finding remains open after
  the round-11 wave. Pauli's follow-up record/pin fixes landed at
  60b95b0c (history.project live source_closes pin, cancel
  notification-flag schema requirement, round-11 SOW record); the
  acceptance-case count was corrected to 31 at 2c3e1058.

### 2026-09-02 (continued) — round 12: glm whole-milestone re-review (FAIL) and fix wave

- The glm-5.3-responses whole-milestone reviewer re-verified the
  round-11 result at 60b95b0c and returned three verified findings
  (one P1, two P2) plus a P3 hardening; the round-12 fix wave at HEAD
  a6640a9d fixes every finding:
  1. P1 — cancellation could never fire during active work
     (v4/rust/iprange-cli/src/rpc/session.rs). The worker held the
     entire SessionState mutex around every handler call, so
     apply_cancel (cancel notification) and begin_shutdown (EOF and
     SIGINT/SIGTERM) blocked behind that same mutex until the handler
     finished: long SDK work (export, cursor pages, live refresh)
     ran to completion uncancelled and Ctrl+C left the process alive
     until the work ended, while the reader thread stayed blocked and
     the events channel grew without admission backpressure. Fixed by
     splitting a SessionControl plane (pending/cancelled ids, active
     keys, the per-unit cancellation token, shutting_down,
     fatal_error) behind its own mutex that handlers never lock;
     SessionState now keeps only resources, the active-request id,
     and a control Arc. apply_cancel/begin_shutdown lock control
     only, the worker installs one fresh token per unit and the
     active-keys set in one control scope (closing the
     install-versus-cancel race; units queued at EOF keep the
     already-cancelled token), and handlers read the token through
     SessionState::token() (one short control lock plus an Arc
     clone). The lock graph is state->control only; no control->state
     or control->writer edge exists, so no interleaving can
     deadlock. New test
     cancel_and_eof_reach_an_active_handler_holding_the_state_lock
     simulates an in-flight handler holding the state lock, proves
     cancel+EOF complete promptly and the active token is cancelled,
     and drives a queued live reader.open under the shutdown-cancelled
     token to the factual data.code "cancelled" outcome.
  2. P2 — a same-batch cancel of an earlier sibling never fired.
     Cancels were applied during the frame scan but ordinary elements
     only became pending after the whole frame was scanned, so a
     sibling cancel always no-op'd. Fixed: handle_frame now admits
     each ordinary element and marks it pending immediately during
     the scan, so a later cancel element targets an earlier sibling
     (spec: "already queued from the same batch") and the worker
     omits its response; elements scanned after the cancel are not
     yet admitted and are not targets. Pinned at control, unit, and
     full-loop wire level
     (same_batch_cancel_marks_an_earlier_sibling_before_later_elements_scan,
     same_batch_cancel_before_an_element_is_not_its_target,
     same_batch_cancel_omits_the_sibling_on_the_wire).
  3. P2 — the Validation section recorded the full gate re-run at
     b41550f3 while 60b95b0c changed the corpus afterwards. The dated
     re-run entry below records the gate at the round-12 final HEAD
     and names the last corpus/schema-affecting commits (60b95b0c,
     a6640a9d).
  4. P3 — v4/cli/schema/methods.py typed batch_size as C.U32
     (0 allowed) while the spec and the Rust validator bound it to
     1..4096. Fixed: new C.BATCH_U32 ({u32, min 1, max 4096}) used by
     reader.feeds.open and reader.ranges.open, with negative
     schema.methods self-tests for 0 and 4097 and a positive 4096.
- Validation at a6640a9d: Rust workspace 50 suites / 730 tests, 0
  failures; `-D warnings` clean; source graph 491 sources; runner
  31 cases / 15 oracle checks; golden 53; sensitivity 14 modes;
  schema self-tests pass; Go SDK suite green; mmap storage/runtime/
  architecture gates pass; SOW audit and hygiene scans clean.
- The five own-model delta reviewers (wire, coverage, SDK-ownership,
  performance, close-facts) re-verified the round-12 fixes at
  a6640a9d and all PASS; the glm-5.3-responses whole-milestone
  reviewer PASSed the same revision on the condition that this
  close-out commit is strictly record-only (SOW-0028 records only;
  nothing under v4/, specs, or schemas), which this commit satisfies.
- Non-blocking follow-up recorded from the glm review: the
  cancellation token is installed per unit, so cancelling one id of
  a currently executing batch aborts the shared token and
  non-targeted siblings in that same unit end with factual
  "cancelled" outcomes (no false commit, every response truthful).
  Per-entry tokens or re-arming a fresh token after a mid-unit
  cancellation remain options for a later milestone; tracked in
  Followup below.

### 2026-09-02 (continued) — round 13: milestone-1 closure and legacy-surface start

- Milestone 1 (the Rust JSON-RPC service) closes: all gates green at
  a6640a9d (Rust 50 suites / 730 tests, 0 failures; `-D warnings`
  source graph 491 sources; runner 31 cases / 15 oracle checks;
  golden 53; sensitivity 14 modes; schema self-tests; Go SDK suite;
  mmap storage/runtime/architecture gates; SOW audit clean). The five
  own-model delta reviewers (wire, coverage, SDK-ownership,
  performance, close-facts) PASSed a6640a9d; the
  glm-5.3-responses whole-milestone reviewer PASSed the same
  revision, conditional on a record-only close-out commit, which
  landed at b26d8431 (round-12 record + dated Validation re-run
  entry; no v4/ code in that commit). Pushed to origin/master.
- Remaining milestone-1 scope clean-up: none. The runner batch-step
  coverage opportunity (Pauli P3 note) and the per-unit
  token collateral-cancellation observation (glm P3 note) are
  tracked non-blocking follow-ups in Followup below.
- Delivery-order step 3 starts: implement and qualify the complete
  Rust legacy surface against the C oracle. Scope: 6,932 lines of C
  (src/iprange.c + ipset{,6}_*.c), 101 test directories in tests.d/
  as the primary oracle (run-tests.sh through IPRANGE_BIN), and the
  13 wiki pages as the documented surface (Home, merge, common,
  exclude, diff, intersect, count-unique, compare, reduce,
  ipset-reduce, input-formats, output-formats, dns-resolution,
  ipv6). No C execution or dynamic linking: Rust reimplements the
  grammar, expansion, DNS, modes, formatting, binary formats,
  diagnostics, probes, help/version, and exit codes in
  v4/rust/iprange-cli/src/legacy/ (currently a stub).

### 2026-09-01 (continued) — complete handler registry

- All 32 remaining v1 methods implemented by three parallel workers and
  integrated at commit 137032ed: live workflow family
  (live.rs + lifecycle_live.rs), recovery/maintenance family
  (maintenance.rs + recovery.rs), and algebra/query/join/history/feeds
  family (algebra.rs + feeds.rs). dispatch.rs REGISTRY now has all 52
  callable methods; system.describe advertises them.
- SDK visibility widenings (pub(crate)->pub, signature-preserving):
  RecoveryCandidate fields (recovery.rs:88-93), LiveWriter::address_family
  (live_writer.rs:110), LocalBasename::from_path and
  CommitCleanupArtifacts::{clean,tail} (live_writer/result.rs:23,96,100).
- Validation at 137032ed: -D warnings build clean; Rust workspace tests
  50 suites 0 failures; golden corpus PASS; sensitivity gate 13 modes
  PASS; external runner 9 cases / 13 oracle checks PASS.
- Open findings carried into review round 7 (adjudication pending):
  (1) join.direct row order — spec text says uncovered cell last
  (spec:764), SDK iterates uncovered first; (2) feeds.delete/rename
  synthesize a zero-counter WorkflowReport because the SDK exposes no
  report; (3) publication.resolve rejects supplied publication_result
  (reservation-authority path only) because the result schema cannot
  reconstruct the SDK attempt object; (4) first-seen retention refresh
  publishes an adapter-owned same-directory JSONL after commit;
  (5) validate/recovery.inspect/recover require iprange-v4-worker beside
  the iprange binary (worker adjacency).

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

- Milestone 1 (the Rust JSON-RPC service) is implemented and
  qualified: `iprange --jsonrpc` registers 53 method names - 52
  callable methods plus the `iprange.v1.cancel` notification - and
  every callable method has a registered handler
  (`v4/rust/iprange-cli/src/rpc/dispatch.rs`).
- The qualification area ties every callable method to real wire
  exchanges: 53 golden exchanges (`v4/cli/golden/`) and 31 declarative
  cases (`v4/cli/cases/`, including the cancel notification case)
  cover every method family; the independent scalar interval oracle
  contributes 15 checks.
- Later acceptance evidence is milestone work of this SOW, not
  milestone 1: the complete Rust legacy surface against the C oracle
  (delivery-order step 3), the Go product executable (step 4), and the
  same-language/cross-open/mixed-live matrices plus consolidated
  benchmarks (step 5), including `v4/cli/README.md` and `bench.py`.

Tests or equivalent validation (re-run at HEAD b41550f3, the
round-11 fix wave commit; round-10 wave re-run was recorded at
061f8c71 with 50 suites / 710 tests, 30 cases, 13 sensitivity modes):

- Rust workspace: 50 suites / 726 tests, 0 failures.
- `-D warnings` build clean (rustc `-D warnings` via
  `v4/rust/check-source-graph.sh`); source graph 491 sources,
  4 supported targets, 1 runtime-compiled native fixture.
- Runner matrix green: 31 cases / 15 oracle checks
  (`nice python3 v4/cli/run.py --rust ... --matrix rust`), including
  the new `cancel.unknown_id` notification case.
- Golden corpus: 53 exchanges PASS (`nice python3 v4/cli/check_golden.py`).
- Sensitivity gate: 14 deliberate-brokenness modes PASS
  (`nice python3 v4/cli/sensitivity_gate.py`), including the new
  `cancel_replies` mode.
- Schema module self-tests PASS (`python3 -m v4.cli.schema.results`,
  `schema.cases`, `schema.frame`, `schema.methods`), including the
  typed housekeeping/cleanup negative tests added in the round-10 wave
  and the round-11 u32 `ordinal` correction.
- Go SDK suite green (`nice go -C v4/go test ./...`).
- mmap-only gates: `check-mmap-storage.sh` (343 production sources),
  `check-mmap-runtime.sh`, `check-architecture.sh` all PASS.
- SOW audit, placeholder/personal-name/trailing-whitespace scans and
  `git diff --check` pass (the audit status-parser false positive on
  the historical SOW-0025 `## Status` heading was fixed in the
  round-11 wave).

- Dated re-run at the round-12 final HEAD (fix wave a6640a9d plus
  this record commit): Rust workspace 50 suites / 730 tests, 0
  failures; `-D warnings` clean; source graph 491 sources; runner
  31 cases / 15 oracle checks; golden 53; sensitivity 14 modes;
  schema self-tests pass (including the new batch_size 1..4096
  negative cases); Go SDK suite green; mmap storage/runtime/
  architecture gates pass; SOW audit and hygiene scans clean. This
  entry supersedes the b41550f3 re-run and covers the corpus changes
  of 60b95b0c and the schema-bounds change of a6640a9d (the last
  corpus/schema-affecting commits before this record).

Real-use evidence:

- The qualification client (`v4/cli/run.py`) drives the real Rust
  binary over the bidirectional stdin/stdout pipe with ordinary
  production JSON-RPC requests; the golden exchanges were generated
  from real outputs.
- Publisher workflows, live lifecycle, export, snapshot, validation,
  recovery, and maintenance execute through the service in the
  declarative corpus and in the recorded review rounds 7-10.

Reviewer findings:

- Rounds 7-10 each returned FAIL with a verified numbered inventory and
  a matching fix wave; the round-10 glm-5.3-responses review at HEAD
  fe220f17 and its fix wave are recorded in the round-10 section above.
- The five own-model delta reviewers (coverage, SDK-ownership, wire,
  performance, close-facts) PASSed at 6b1837f4/fe220f17.

Same-failure scan:

- Each wave scans the full class of every finding before landing: the
  round-10 wave typed every OPAQUE housekeeping/cleanup member in the
  lifecycle results, added the optional source_close to every
  feed/retention result schema, and checked every CI workflow before
  recording clippy/rustfmt as non-gates.
- Reviews must continue to search for duplicate persistence algorithms,
  complete-feed JSON, test-only fields, text-as-error identity, false
  cross-file atomicity, unbounded queues/frames, leaked handles, and
  accidental listeners.

Sensitive data gate:

- This SOW contains public paths/protocol references and synthetic
  product descriptions only; no secrets, credentials, tokens, SNMP
  communities, customer/community/personal data, identifying addresses,
  private endpoints, or proprietary incidents.

Artifact maintenance gate:

- AGENTS.md: unchanged; must be updated to describe the delivered
  JSON-RPC service before SOW-0028 closes.
- Runtime project skills: unchanged; `project-v4-rust` must be updated
  with the delivered CLI/RPC qualification workflow before close.
- Specs: `iprange-jsonrpc-v1.md` is the normative approved contract and
  is updated when a wave corrects contract text; engine/adoption specs
  must describe delivered behavior at close.
- End-user/operator docs: `v4/cli/README.md` and wiki/ updates are
  scheduled by delivery-order steps 3-5 and the close.
- End-user/operator skills: none exist; reassess before close.
- SOW lifecycle: SOW-0028 is current/in-progress (sole current SOW);
  SOW-0027 is closed; SOW-0029 tracks the daemon (pending); SOW-0017
  remains paused; SOW-0030 owns engine performance residuals
  (pending); SOW-0031 owns the history-project report-output option
  (pending).

Specs update:

- `.agents/sow/specs/iprange-jsonrpc-v1.md` is the approved normative
  contract for the delivered milestone; the round-10 wave updates it
  where the fix wave corrected contract text.

Project skills update:

- Update `project-v4-rust` with the delivered CLI/RPC qualification
  workflow before SOW-0028 closes.

End-user/operator docs update:

- Pending delivery-order steps 3-5 and final close.

End-user/operator skills update:

- None currently exist; reassess before close.

Lessons:

- A machine interface used by tests should first be a coherent production API.
- JSON-RPC supplies application semantics; newlines only supply stdio framing.
- A remote daemon is not security-neutral when methods mutate local artifacts.

Follow-up mapping:

- SOW-0027 supplies final parity and direct-performance input (closed).
- SOW-0029 owns daemon transport/security/path/concurrency (pending).
- SOW-0031 owns the history-project report-output option (pending).
- SOW-0017 owns authenticated public snapshots (paused).
- SOW-0030 owns engine-level performance residuals (pending).
- `update-ipsets` migration remains outside scope; no architecture was selected.


## Outcome

Pending.

## Lessons Extracted

Pending implementation and final review.

## Followup

- Cancellation tokens are installed per work unit: cancelling one id
  of a currently executing batch aborts the shared token, so
  non-targeted siblings in that same unit end with factual
  "cancelled" outcomes. Re-arm per entry or use per-entry tokens if
  narrower cancellation is required (round-12 glm observation,
  non-blocking; tracked in SOW-0030).
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

## Review round 2 (2026-09-01) — delta re-review FAIL, fix batch 2

Wire findings (McClintock @ af9ac206):
1. P1 `reader.close` on already-closed returns `handle_closed`; spec
   requires `handle_not_found` for closed or unknown (spec ~404-410).
2. P1 Complete-envelope fallback reports a successful durable mutation as
   `read_only_failure` and drops the factual result (`database.create`
   commits then returns output_limit/read_only_failure with a huge id).
   Fix: refuse unanswerable ids BEFORE handler execution (`not_started`),
   never relabel durable facts as read-only.
3. P2 Huge-id fallback uses `-32001/id:null` without the transport
   condition; spec ties -32001/id:null to frame-over-limit followed by
   process shutdown (spec 44-52). Fix: emit -32001/id:null and shut down
   the process, matching the input-side frame-over-limit behavior.
4. P2 Metadata file delivery accepts zero budgets (max_open_files=0,
   max_output_bytes="0") that the frozen schema rejects; server must give
   -32602, not -32010/output_limit.
5. P2 Metadata read/file-delivery failures still use `not_started` after
   the read began; must be `read_only_failure` (output.rs:38-53,139-145).
6. P2 Integral request ids outside signed 64 are rejected; spec allows any
   integral JSON number (Python authority accepts u64 max). Fix: request
   id holds serde_json::Number; accept i64/u64, echo exactly.
7. P2 Entropy failure emits undocumented product code `internal`;
   adapter codes are a closed list ending in `io`. Fix: use `io`.
8. P2 Runner enforces lexicographic feed order instead of feed-catalog
   order (run.py:349-363); catalog order is insertion order.
9. P2 Path-length units mismatch: Python authority counts code points,
   Rust counts UTF-8 bytes; a 40k-char Greek path is schema-valid but
   server-rejected. Fix: Rust path validator counts chars.

Suite findings (Copernicus @ af9ac206):
1. P1 Python VALUE_TAG accepts hex "00" (NUL) in requests and results,
   and rejects every control char in `text` while spec/Rust forbid only
   NUL (spec 151-155).
2. P1 Python missing cross-field constraints: value_kind/structure_kind
   compatibility, `start` with feed view, family-dependent prefix and
   DNS-thread bounds, validation/recovery scratch enable rules, algebra
   max_sources bound, canonical IP text validation.
3. P2 system.describe fixed facts (format, families, export formats,
   limits) are structurally typed but not semantically constrained.
4. P2 Lookup result payload variant is not bound to the reader value
   kind; a membership reader can return a direct value and pass.
5. P2 Algebra oracle exists but is not wired into any runner assertion.
6. P2 Python decode_frame rejects a legal max-size frame when given an
   LF/CRLF-terminated line (length checked before stripping terminator).

## Review round 3 (2026-09-01) — FAIL, fix batch 3

Wire (McClintock @ aec5d155):
1. P1 Cursor paging measures the partial result object, not the complete
   envelope; a valid large cursor at its requested batch size returns
   output_limit instead of a reduced page (cursors.rs fits_next_item /
   array_response_base vs session fallback). Fix: size pages against the
   full envelope budget (jsonrpc + id + method) and reduce before refusing.
2. P1 `export` advertises and accepts `source.mode:"live"` but rejects
   live sources at runtime (export.rs:132-143). Live readers are now
   implemented; export must route live sources through the facade.
3. P2 Integral ids beyond i64/u64 (2**100) rejected; the spec and Python
   authority accept any JSON integer. serde_json loses exactness beyond
   u64 without arbitrary_precision; make id echo exact for any integral
   JSON number.
4. P2 Zero budget values are still accepted by writer/snapshot/feed/result
   budget validators (lifecycle.rs, publish.rs, snapshot.rs, export.rs);
   the frozen schema requires positive values. Zero must be -32602.

Suite (Copernicus @ aec5d155):
1. P1 Algebra compare oracle computes left_addresses/right_addresses as
   side-only counts; the SDK defines total per-side addresses (oracle.py
   131-146 + run.py 433-443 + self-tests).
2. P2 frame.decode_frame accepts UTF-16/UTF-32 bytes (json.loads byte
   auto-detect); transport is UTF-8 only. Decode strictly UTF-8 first.
3. P2 A payload terminated by bare CR is accepted as CRLF in BOTH Python
   frame.py and Rust schema.rs/framing.rs; only LF and CRLF terminate.
4. P2 Lookup/start addresses are canonical but not bound to the opened
   reader's address family; runner must track address_family and reject
   cross-family addresses.

## Review round 5-6 close (2026-09-01)

Round 5 findings (Copernicus @ 298c18c9): 4300-digit int limit in the
Python authority (fixed: set_int_max_str_digits lifted, ValueError ->
FrameError); source_close not correlated with live source mode (fixed:
runner requires source_close for live, rejects fabricated for
immutable); unknown-reader handles bypassed family/payload checks
(fixed: require_reader fails unknown handles; sensitivity gate opens
its fake reader through reader.open). Committed at c81e0ed2.

McClintock PASSed the chunk at 298c18c9. Copernicus's round-6
re-confirmation could not be obtained: the glm-5.3-responses endpoint
returned persistent HTTP 429 rate limits after three retries; per
SWARM.md the unavailable resident is skipped, not substituted. His
round-5 findings are fixed and independently reproduced by the lead.

Chunk qualified at c81e0ed2: Rust 664/664 (cli 72/72), -D warnings
clean, check_golden PASS 53/9, sensitivity 13/13, external runner 9/9
with 13 oracle checks. Read-only, publish/lifecycle, export/snapshot,
and transport are complete; next per the fixed delivery order: live
workflows, destructive recovery/maintenance, then algebra/query/join/
history families.

## Review round 7 (2026-09-01) — FAIL, adjudicated findings

Round 7 reviewed HEAD 137032ed in five adversarial scopes (wire contract,
SDK ownership, correctness, performance/bounds, registry/records). The raw
reviewer transcripts were delivered as session messages and were not
preserved in the repository; the items below are reconstructed from this
SOW's execution log, the recorded user decisions, and the fix commits that
implement them. Every item maps to a committed fix; the round-8 delta
re-review (below) re-audited each area at the fixed HEAD.

P1 findings (4):

1. `join.direct` row order: spec requires covered rows per feed ascending
   by direct value with the uncovered (null) cell LAST (spec:764); both
   SDKs iterated the uncovered cell first. Fixed by decision D1-B at
   3c3aab8d: Rust sort key `(feed, direct == 0, direct)`, Go equivalent,
   real wire value 0 pinned as a covered cell (provider_joins.rs,
   join_direct_emit_test.go, cases/join.direct.json).
2. `feeds.delete`/`feeds.rename` synthesized a zero-counter WorkflowReport
   the SDK deliberately does not expose, inventing untruthful statistics.
   Fixed by decision D2-B at 3c3aab8d: results carry commit, metadata, and
   writer-close facts only; `_FEED_CHANGE_COMMON` schema, spec 688-694, and
   cases/feeds.lifecycle.json (no report member) pin it.
3. `publication.resolve` rejected the caller-supplied publication_result:
   the wire schema could not reconstruct the SDK PublicationAttempt, so the
   D3 authority path was unusable. Fixed by decision D3-A at 6d9b6066
   (complete reversible evidence, publication_evidence.rs) and completed
   this round: one canonical PublicationResult encoder for every producer
   (current.publish, snapshot, algebra.publish, recover, inspect),
   single vocabulary, and an oracle case resolving a preserved snapshot
   result end to end.
4. Metadata `replace_file` read a caller file into heap without a bound
   (unbounded heap read above the 20 MiB SDK cap). Fixed at 3c3aab8d
   (stat pre-check) and hardened this round against the TOCTOU window:
   the read itself is capped (lifecycle.rs read_bounded).

P2 findings (8):

1. Zero runner coverage for the 32 new method families (golden-only
   evidence). Fixed at 4dfcc382 and 061ded50: 30 declarative cases cover
   every callable family; 15 independent oracle checks fire per run.
2. Budget-refusal wire codes were inconsistent (`output_limit` vs the
   canonical SDK snake_case names). Adjudicated at 91e3e57b records:
   SDK-domain refusals use canonical codes; `output_limit` only where the
   spec names it or for adapter-side output guards.
3. `value_tag.hex` validator accepted the wrong character range (g-z) and
   diverged from the lowercase-hex spec. Fixed at 3c3aab8d: exactly
   0-9a-f, NUL byte rejected (lifecycle.rs 730-752).
4. Removal-output temporary cleanup relied on destructors; explicit
   discard on every terminal path with reported failures is required.
   Fixed at 3c3aab8d and completed this round: the private temporary is
   created only after every fallible pre-work, so no early return can
   leak it (live.rs first_seen_refresh).
5. File-level query/algebra handlers dropped the factual live source
   close result. Fixed at 3c3aab8d for query/join/algebra
   (source_close/source_closes); the `history.project` residual is fixed
   this round (its success and error outcomes now carry source_closes).
6. The round summary miscounted severities ("1 P1 + 8 P2 + 2 P3" vs the
   verified 4 P1 + 8 P2 + 2 P3). Corrected in the round-7 execution log.
7. Goldens are illustrative, not independent correctness evidence; the
   mandatory correction required oracle-driven cases for every method
   family before the next review. Delivered at 4dfcc382/061ded50: text-
   defined fixture databases feed the scalar interval oracle, and the
   oracle self-test runs on every runner invocation.
8. Housekeeping state was guessed on the wire instead of carried and
   decoded exactly. Fixed at 3c3aab8d (carried Housekeeping state,
   exact decode in lifecycle_live.rs).

P3 findings (2):

1. The algebra and feeds handler families duplicated the publisher
   finalization machinery (CommitDraft adapters, publish_changed/
   publish_no_change/finish_publisher/workflow_failure/
   finish_writer_error/close_writer and the fact converters in up to five
   files). Fixed at 91e3e57b: handlers/workflow.rs is the single
   authority; families keep only their own CommitDraft impls.
2. The retirement first-seen removal artifact is adapter-owned; its
   publication facts shape was ambiguous. Adjudicated (decision recorded
   2026-09-01): facts carry publication + destination_content only; no
   fabricated SDK publication attempt (spec 670-673, 6d9b6066).

Round-7 close-out validation (HEAD 91e3e57b): -D warnings build clean;
Rust workspace 50 suites; Go suite incl. the D1-B order test; golden 53
exchanges; sensitivity 13 modes; runner 24 cases / 13 oracle checks.
The round-8 re-review (below) added the performance, coverage, wire,
SDK-ownership, and records findings listed there and re-verified the
round-7 fixes at the new HEAD.

### 2026-09-02 — milestone 2 (delivery step 3): complete Rust legacy surface implemented

- Implementation: five parallel own-model workers ported the released
  legacy surface into `v4/rust/iprange-cli/src/legacy/` (6,932 C lines
  across `src/iprange.c`, `src/iprange6_main.c`, `src/ipset{,6}_*.c`, the
  100-dir `tests.d/` oracle, and the 13 wiki pages). Integration fixes by
  the lead: v4 `parse_cidr` now parses the token's own `/prefix` (the
  worker draft used the caller default); `parse::load_all` returns
  `LoadedAll` with the group-B boundary in loaded-set units so `@file`/
  `@dir` expansion splits groups exactly like the C `read_second` chains;
  the diff/common/exclude walks build results unoptimized and flag them
  optimized at the end (C `ipset{,6}_create(name, 0)` +
  `flags |= OPTIMIZED`), preserving separate adjacent entries in diff
  output; the `Cannot understand line No N` counter counts fgets records
  (lineid off-by-one fixed); `convert_foreign` applies the C tail rule
  (scan `[0-9./]`, then whitespace/`#`/`;` to EOL); the C `-v` timing
  line prints in IPv4 mode; SIGPIPE and `strerror` are `cfg(unix)`
  gated for the Windows target; dead code (`count_prefixes`, unused
  mapped helpers, `Positional`, `SourceKind::Directory`) removed.
- Validation at 43bf8929 (all gates green):
  - `tests.d/` legacy suite: 100/100 pass through `IPRANGE_BIN`.
  - Rust workspace: 50 suites, 0 failures; `-D warnings` clean on the 4
    supported targets (linux amd64/arm64, windows, plus source-graph
    targets); `check-source-graph.sh`: 502 sources, 4 targets.
  - mmap storage/runtime and architecture gates: PASS.
  - JSON-RPC external runner: 31/31, 15 oracle checks.
  - Golden corpus: PASSED; sensitivity gate: 14 modes PASS.
  - Oracle differentials by the workers (worker-attested scratch
    evidence, not repo-reproducible): 36,408 IPv6 parse/format cases
    0 mismatches; 600 randomized text-mode output trials byte-identical;
    binary v1/v2 round-trips `cmp`-identical and cross-loadable in all 4
    directions; 10 malformed-binary diagnostics byte-identical. The
    classes are covered by committed gates: binary round-trip and
    malformed-binary cases live in `tests.d` 27/46/57/58/59/62/82.
- Scope guard: the legacy module uses only language-local grammar,
  algebra, DNS, and formatting; it contains no v4 persistence logic and
  creates no v4 artifact (JSON-RPC exclusivity enforced in main.rs).

### 2026-09-02 (continued) — review-delta fixes and DNS pool OOM root cause

- The five own-model reviewers and glm-5.3-responses reviewed the
  milestone-2 HEAD (d0f2afb3, implementation 43bf8929). Findings
  fixed in this delta: P1 text output was syscall-per-line (stdout
  `LineWriter`, not `BufWriter`; strace: 198,769 vs 666 `write`
  syscalls on 200k lines) — fixed in print.rs, re-measured at
  0.179 s vs C 0.212 s byte-identical; P2 the DNS pool never grew
  past one worker (the loader drained before the pool could grow)
  — fixed with a per-file batch drain in dns.rs/parse.rs; the pool
  now reaches `--dns-threads` exactly like C ("threads used 5 of 5");
  plus the P3 import fix and binary.rs family-generic writers.
- Resolved user decision (2026-09-02, "fix the OOM now"):
  hard-cap the DNS worker pool. Root cause of the 13:57 OOM kill
  (kernel: pid 3784613 `iprange`, 155 GB VSZ / 74.6 GB RSS): the C
  oracle and the port spawn one worker per pending request while
  `pending > workers && workers < --dns-threads`; a legal but large
  `--dns-threads` value with a large host file spawns tens of
  thousands of 2 MiB worker stacks (the C is worse: 8 MiB stacks).
  Fix: `DNS_POOL_HARD_MAX = 128` workers in dns.rs — unobservable
  wherever C survives (default 5, legacy suite uses at most 4),
  bounds worst-case stack reservation to 256 MiB. Plus C-identical
  spawn-failure handling: with no worker yet, roll back the request
  and fail the file like `dns_request() -1`; afterwards stop
  spawning (C prints one line per failed attempt, a storm we bound
  to a single line).
- Regression found by the 100-dir suite after the drain rework: the
  per-host DNS failure lines ("failed permanently", system, error)
  were swallowed because drain() returned Err before rendering.
  Fixed: drain() returns every reply and the loader renders the
  failure lines then fails the file (C order: worker prints, then
  dns_done() reports the failed count).
- Validation at HEAD:
  - OOM reproduction: `--dns-threads 100000` + 50k-host file now
    completes rc=0 with max RSS 49,708 KB (was kernel-OOM at
    74.6 GB RSS); default-threads 300k-host file rc=0; comparisons
    with C byte-identical on stdout.
  - tests.d legacy suite: 100/100 pass through `IPRANGE_BIN`;
    Rust workspace 50 suites / 0 failures, `--all-features` 50
    suites; `-D warnings` clean (all-targets build); source graph
    502 sources / 4 targets; new unit test
    `pool_is_hard_capped_with_huge_threads_max`.
  - Known residual, recorded: when the OS cannot satisfy an
    allocation at all (e.g. artificial `ulimit -v` 1 GiB while the
    pool has grown), the Rust process aborts (core dump, rc 134)
    instead of C's per-host graceful degradation — Rust's standard
    OOM behavior; the pool cap keeps real runs far from that point.

### 2026-09-02 (continued) — milestone 2 closes (delivery step 3)

- glm-5.3-responses whole-milestone final review at 51839fa5: FAIL
  with two P2s, both fixed and re-reviewed:
  - F1: `--help` printed a literal "iprange" for `%s`; C prints
    `usage(argv[0])`. Fixed at d96797e0: main.rs captures the real
    argv[0] and legacy::run substitutes it; help is byte-identical
    to C under the same argv0 (`exec -a` differential).
  - F2: three reviewer resolutions were uncommitted working-tree
    edits; committed at d96797e0 (mod.rs doc enumeration,
    check-architecture.sh legacy-isolation scan, SOW labels).
  - glm delta re-review at d96797e0: PASS; one cosmetic P3 recorded:
    a program name containing `%d`/`%s` literals would be re-scanned
    by the sequential replace (C passes argv0 as a printf argument).
    Disposition: not worth fixing — no realistic invocation uses such
    an argv0, and any fix would add a formatting pass to a cold path;
    recorded here as the rejection evidence.
- Review verdicts at milestone-2 HEAD, all handled: Tesla P1/P2/P3
  (buffered stdout, pool growth, import) fixed; Pauli F1/F2/F3
  (help %s/%d, unimplemented! scaffold removed, DNS waiting cadence
  accepted deviation) resolved; Ohm P3-1 (worker-attested counts
  labeled), P3-2 (cancellation follow-up tracked in SOW-0030),
  P3-3 (help argv0) resolved; Ramanujan P3-3/P3-4/P3-5 (doc
  enumeration, dual writers deliberate, isolation scan) resolved.
- All gates re-run at final HEAD d96797e0 (nice): tests.d legacy
  suite 100/100; Rust workspace 50 suites plain and --all-features,
  iprange-cli 251 tests, -D warnings clean; source graph 502/4;
  v4/cli runner 31/31 with 15 oracle checks; golden PASSED;
  sensitivity 14 modes; mmap storage 343 files, mmap runtime,
  architecture (incl. the new legacy-isolation scan), and the Go
  mmap-trace gate all PASS.
- Milestone 2 closes; delivery-order step 4 starts: implement the
  pure-Go product executable `v4/go/cmd/iprange` (legacy surface +
  JSON-RPC over stdin/stdout) with `v4/go/internal/cli/{legacy,rpc,
  handlers,fileio}`, importing only the public Go module, in the same
  family order as the Rust port, with C-authoritative expectations
  only (never Rust-produced expected answers).

### 2026-09-02 (continued) — milestone 3 starts (delivery step 4: pure-Go product executable)

- Scope: `v4/go/cmd/iprange` + reusable non-exported packages under
  `v4/go/internal/cli/` mirroring the Rust responsibilities: `legacy`
  (grammar, parse, IPSet algebra, DNS, formatting, binary v1/v2,
  diagnostics, help/version), `rpc` (JSON-RPC 2.0 framing, session,
  dispatcher, cancellation), `handlers` (method-family adapters over
  the public Go SDK only), `fileio` (streaming legacy-compatible text
  input and atomic bounded output; never reads/writes v4 database
  bytes). Imports only the public Go module; never
  `v4/go/internal/{reader,writer,...}`.
- Family order (fixed delivery order step 4, "same family order" as
  the Rust port): options/usage grammar first, then family/ipset
  algebra, parse, ops modes, print, binary, dns; then the JSON-RPC
  transport with handlers in the step-2 order (read-only, immutable
  publication/export, live workflows, destructive recovery/
  maintenance).
- Authority: only independently modeled or C-authoritative
  expectations (C source, wiki, tests.d, iprange-jsonrpc-v1.md);
  Rust output is never the oracle for Go. The Rust implementation is
  a structural reference for the responsibility split only.
- Qualification: `nice go build ./cmd/iprange`; Go unit tests;
  tests.d legacy suite through `IPRANGE_BIN=$PWD/v4/go/cmd/iprange/
  iprange`; v4/cli runner `--go` plus `rust_to_go` and `go_to_rust`
  matrices; golden/sensitivity for the Go transport; Go mmap-trace
  gate; five own-model adversarial reviews then glm-5.3-responses
  final review before milestone-3 closure.
- Workers: five parallel own-model workers port the legacy families
  over the lead-provided foundation (options/usage/family/ipset/
  run-dispatch), each with a disjoint write scope; the lead integrates
  seams, then the same for the JSON-RPC families.

### 2026-09-02 (continued) — milestone 3 legacy surface complete (Go product executable)

- The pure-Go legacy surface is implemented under `v4/go/internal/cli/
  legacy/` (7,565 production+test lines) over the lead foundation
  (family, ipset, options, usage, run dispatch) plus six parallel
  worker families: ipv6 (Beauvoir), parse (Franklin), ops (Curie),
  print (Kuhn), binary (Bacon), dns (Nash). Seams integrated by the
  lead: LoadedSet ownership in parse.go, DNS error gating through
  `(*DnsError).silentGated()`, unified `Sub128` in family.go, print
  delta fields, C-exact IPv4 netmask diagnostics, `writeUint128`
  quotient feedback (loop-body shadowing bug), and the missing
  compare-row comma after name2 (C `iprange_csv_write_compare_row`,
  src/iprange.c:52).
- Qualification at current HEAD: `nice go build ./cmd/iprange` and
  `nice go vet ./...` pass; `nice go test ./... -count=1` all pass;
  `env IPRANGE_BIN=/tmp/iprange-go nice ./run-tests.sh` → **100/100
  tests.d pass** (0.6s user) with the Go binary against the C oracle
  suite. DNS differential vs glibc and Rust: pool diagnostics byte-
  identical; the Rust reference hangs on a second DNS-using file
  (rc=124, reproduced) while the Go drain implements the C behavior
  and pins it with a regression test.
- Next: the JSON-RPC transport (rpc) and handler families in step-2
  order, then fileio for the export family, using only the public Go
  module; `v4/go/internal/cli/rpc/rpc.go` is currently the
  not-implemented stub.

### 2026-09-02 (continued) — milestone 3 JSON-RPC foundation (Go transport + shared handlers)

- The Go JSON-RPC transport is implemented under `v4/go/internal/cli/
  rpc/` as a strict mirror of the Rust `rpc/{framing,schema,dispatch,
  session,state}.rs` responsibilities, per the fixed step-2 family
  order:
  - `framing.go` — newline-delimited JSON framing: 1,048,576-byte
    ceiling, LF/CRLF, exact-limit edge cases; 64 KiB buffered writer,
    flush per line.
  - `schema.go` — strict envelope decode: `jsonrpc:"2.0"`, integral-
    only ids, prefixed methods, object params, batch 1..=16,
    cancel-only notification, NaN-free id validation,
    -32001/-32002/-32010 error codes.
  - `dispatch.go` — 53-entry static inventory (52 advertised
    methods plus the cancel notification), Register (panic on
    unknown/duplicate), Advertised method list.
  - `state.go` — Immutable/Live reader values, cursor values, bounded
    closed-handle tombstones (1024 FIFO), deterministic-ordered
    CloseAll.
  - `session.go` — reader goroutine + event loop + worker goroutine;
    queue bound 16, busy/unanswerable in-position batch members,
    cancel-during-scan (same-batch earlier sibling only), EOF
    cancels token then drains admitted units, fatal on broken
    stdout/stdin-read-error/signal, 65,000-byte bounded response
    (`output_limit` product error), preflight unanswerable id,
    session token.
  - `handle.go` — secure 32-hex handle via crypto/rand.
- Shared handler foundation under `v4/go/internal/cli/handlers/`
  (public-SDK-only adapters): `sdkcode` (SDK ErrorCode -> wire code
  map, boundedResult, preflightResponse, WidestU64/129),
  `params` (strict exact-object decoders, path/handle validation),
  `convert` (DatabaseInfoJSON, ValueTagJSON, CursorAddress, netip
  canonical IPv6), `output` (standard base64, MetadataOutput atomic
  file publish: hard-link fail_if_exists / rename replace),
  `lifecycle_facts` (MetadataValue, CommitResultJSON, CloseResultJSON,
  FileIdentityJSON, cleanup JSON), `workflow` (CloseWriter,
  PublishChanged/PublishNoChange, FinishPublisher outcome-preserving,
  WorkflowFailure, WorkflowReportJSON), `reader_helpers`
  (ReaderHandle, CloseOnError, PreserveCompletedReport,
  ValidateDelivery, MetadataResult, BuildFeedSnapshot,
  ThreatFeedNames, ParseAddress).
- `v4/go/internal/cli/fileio/export_writer.go` — streaming export
  writer: row+byte budgets checked before next write, flush -> fsync
  -> hard-link/rename -> dir sync, Abort, exact 129-bit cardinality
  accumulation, SHA-256 running digest, outcome_unknown
  publication-failure details.
- Public SDK additions: `v4/go/publication_public.go` exports the
  evidence-type aliases (PublicationAttempt, LaterCanonical,
  LiveLineage, AccessPolicy, ArtifactKind, DirectoryRole,
  HousekeepingState, ArtifactPresence, ...) plus
  `DecodePublicationResultJSON` -> the strict wire decoder at
  `v4/go/internal/publication/wire_decoder.go` (exact member sets,
  absent-only optional forms, lowercase hex, decimal strings for u64,
  kind+tailing-zero identity validation). Round-trip test at
  `v4/go/publication_wire_decode_test.go`. Parity manifest records
  38 new go-surface rows.
- Two parity gaps found by the SDK-surface explorer and fixed in this
  foundation: (1) `fault_worker` in system.describe is a CLI-local
  probe (candidate `iprange-v4-worker` executable beside the running
  binary, protocol "1"); (2) the Go root now exposes a strict
  publication-result wire decoder instead of hiding all evidence
  field types.
- Qualification at this point: `nice go build ./...`, `nice go vet
  ./internal/cli/...`, and `nice go test ./... -count=1` all pass;
  parity gate green; legacy transport tests pass incl. -race.
- Next: `system.go` (system.describe), then five parallel handler-
  family workers over this foundation in step-2 order (reader/
  cursors, algebra/query, export, live/feeds, lifecycle/maintenance/
  snapshot/recovery), then dispatch registration in
  `v4/go/cmd/iprange/`, then the external qualification matrix.

### 2026-09-02 (continued) — milestone 3: system.describe wired; five handler workers in flight

- `system.describe` is implemented and registered
  (`internal/cli/handlers/system.go`): static capability object with the
  registered v1 inventory, production limits, and the CLI-local
  fault-worker probe (candidate `iprange-v4-worker` beside the running
  binary; protocol "1"). The Go binary now answers the full capability
  exchange end-to-end (`implementation:"go"`, `product_version:"0.0.0"`,
  matching the Rust build's CARGO_PKG_VERSION).
- Dispatch wiring: `handlers.RegisterAll()` (register.go) is called by
  `cmd/iprange` before the session starts; families register through
  `rpc.Register` and system.describe advertises exactly the registered
  inventory so the external runner skips unshipped methods.
- Repo defect found and fixed: the root gitignore pattern `iprange` hid
  `v4/go/cmd/iprange/` entirely, so the product executable main was
  never committed although the legacy surface and transport depend on
  it. Anchored the C binary pattern to `/iprange`; committed main.go.
- Five parallel own-model handler workers are implementing the
  step-2 families over the foundation: reader/cursors (Confucius),
  algebra/query (Schrodinger), publish/export/input (Laplace),
  live/feeds/lifecycle_live (Singer), lifecycle/maintenance/snapshot/
  recovery (Descartes). Each owns disjoint new files; the lead
  integrates seams and the register.go registry wiring.

### 2026-09-02 (continued) — milestone 3: all 52 Go JSON-RPC methods integrated; wire-fix wave and full qualification

- All five handler families are integrated and registered (register.go
  RegisterAll): the Go binary at `v4/go/cmd/iprange` now implements
  every callable v1 method and `system.describe` advertises exactly
  the 52-method inventory, matching the Rust binary.
- First external qualification of the integrated surface
  (`nice python3 v4/cli/run.py --matrix go`): 25/31 PASS, 6 FAIL.
  The six failures were verified wire defects in the Go adapters and
  fixed in this wave:
  1. `dbfile identity` decode used the dotted path as the member key:
     `decimalU64FromWire(object, field+".volume")` looked up the
     nonexistent key `directory_identity.volume` inside the identity
     object, failing every replayed transition/commit result with
     "volume must be a string". Fixed in `decodeFileIdentity`
     (v4/go/internal/cli/handlers/lifecycle_live.go:188-194) to read
     the `volume`/`file` keys and prefix the field path on errors.
  2. Same member-key defect in the value-tag decoder
     (`wireString(object, field+".hex")`, lifecycle_live.go:313):
     `database.create.resolve` replayed a captured hex tag and failed
     with "value_tag.hex must be a string" instead of reaching the SDK
     conflict. Fixed to read the `hex` key.
  3. Same defect in the creation-security decoder
     (`u16IntegerFromWire(object, "creation_security.kind")` on the
     member object, lifecycle_live.go:520). Fixed to read `kind`.
  4. Go SDK publication-result wire decoder required the housekeeping
     `state` member unconditionally, while the encoder (and the Rust
     decoder) emit `{"artifacts": []}` for the none state. The decoder
     rejected every replayed snapshot result with `missing member
     "state"`. Fixed in
     v4/go/internal/publication/wire_decoder.go:432-463 to mirror the
     Rust semantics: `artifacts` required, `state` optional, and an
     absent state with non-empty artifacts maps to `visible`.
  5. Nil slices serialized as JSON `null` instead of empty arrays:
     `feeds.next` (cursors.go), `ranges.next` (cursors.go), and the
     structured-lookup `threat_feeds` member (convert.go
     NetworkEnrichmentJSON). All now emit non-nil empty arrays.
- Full external qualification after the wave:
  `--matrix go`: 31/31 PASS, 15 oracle checks;
  `--matrix rust_to_go`: 31/31 PASS; `--matrix go_to_rust`: 31/31
  PASS (all with `--allow-skips` for unshipped single-language C
  surface).
- Static gates: `v4/cli/check_golden.py` 53 exchanges PASS;
  `v4/cli/sensitivity_gate.py` 14 modes PASS.
- Go suites at this HEAD: `nice go -C v4/go test ./... -count=1`
  22 packages PASS incl. the CLI families and the publication wire
  decoder; `-race` on `internal/cli/...` PASS; `-tags v4work ./...`
  PASS; `go vet ./...` clean; gofmt clean.
- Legacy C-oracle suite with the Go binary
  (`env IPRANGE_BIN=/tmp/iprange-go nice ./run-tests.sh`): 100/100
  PASS.
- Go mmap trace gate: `nice ./check-mmap-trace.sh` PASS (fixtures
  mapped, never streamed; no read/write/lseek on v4 artifacts or
  worker-control descriptors).
- Next: five own-model adversarial reviewers (one focus each), then
  the glm-5.3-responses whole-milestone review, then milestone-3
  closure in one lifecycle commit.

### 2026-09-02 (continued) — milestone 3: first five-reviewer round (FAIL, 14 findings) and fix wave

- The first post-integration five-reviewer round ran at 048c09f4
  (lead's own model, one focus each: Rust parity, Go idioms,
  performance, wire integrity, API/docs). Verdicts: all five FAIL,
  with every executable gate verified green (all three matrices,
  golden, sensitivity, parity gate, Go suites, tests.d, mmap trace).
  Findings:
  - P1 (parity): snapshot preparation-failure error `details` drops
    the factual members (`cleanup`, `coordination_cleanup`,
    `housekeeping`, `visible_housekeeping`, `output`) because the Go
    SDK collapsed SnapshotPreparationFailure/ImmutableFeed-
    PreparationFailure/AlgebraPreparationFailure to {Cause, Cleanup};
    the Rust adapters emit all six (snapshot.rs:156-171,
    algebra.rs:2108-2130, publish.rs:208-241).
  - P1 (perf): every response object was fully unmarshaled and
    re-marshaled in boundedResponse to enforce the 65 KB ceiling
    (session.go) instead of an O(1) length check + parse-on-overflow.
  - P1 (perf): cursor paging re-positioned by consuming leading
    entries (O(n) per page, O(n^2) total); the Rust adapter seeks per
    page (catalog.seek_by_index, feed-range seek).
  - P1 (wire): reader.matching_feeds emitted `"feeds": null` on
    zero-match; the strict schema requires an array.
  - P1 (wire): the SDK publication-result evidence decoder was weaker
    than the Rust decoder in 10 proven cases (null optionals,
    `housekeeping: {}`, `artifacts: null`, non-canonical decimals,
    non-canonical base64, permissive problem objects).
  - P2 (idioms): duplicated decimal/u64/value-tag/identity/feed-name
    helpers across worker files with divergent rules; unguarded type
    assertion in workflow.go; three close-ephemeral-reader helpers of
    which two leaked immutable reader mappings; maintenance identity
    decoder missing the exact-member check; dead code (osCodeOf,
    CreationStateName, FileIdentityFromWire, asBytes16, MetadataResult,
    IsLive, snapshot errString pair, asUint64FromRaw, DurabilityOutcome
    alias, input firstLine, lifecycle `_ = text`).
  - P2 (wire): `value_tag: {"hex": ""}` refused by Go, accepted by
    Rust (a zero-byte tag is legal).
  - P2 (docs): `v4/cli/README.md` absent (scheduled by the fixed
    delivery order at step 5/close; produced in this wave).
  - P3: comment typos; SOW record "39 new go-surface rows" is 38;
    dispatch record wording; optional cleanup-artifact basename
    reverse-strictness.
- Fix wave (in progress at this entry): all P1s and the P2s above,
  plus the shared SDK preparation-failure fact threading, SDK cursor
  seeks (FeedCursor.SeekByIndex, feed-range seeks) with handler
  adoption, and the helper consolidation; re-validation and a delta
  review round follow.

### 2026-09-02 (continued) — fix wave landed: all P1/P2 findings closed, re-validation green

- Fix wave completed and committed. Every P1 and P2 finding from the
  first five-reviewer round above is closed:
  - Preparation-failure facts: SnapshotPreparationFailure,
    ImmutableFeedPreparationFailure, and AlgebraPreparationFailure now
    carry the full factual set (`output`, `cleanup`,
    `coordination_cleanup`, `housekeeping`, `visible_housekeeping`);
    the snapshot/algebra/feed CLI emitters print all six (five for
    feed, matching the Rust `output != nil || !cleanup.empty()` rule);
    new preparation_failure_test.go pins every emission path. A real
    snapshot failure now emits exactly Rust's shape: `{"cleanup":{},
    "cleanup_state":"clean","coordination_cleanup":{},"housekeeping":
    {"artifacts":[]},"output":null,"visible_housekeeping":[]}`.
  - Cursor paging: FeedCursor.SeekByIndex and FeedRangeProjection
    seeks implemented in the SDK (internal/reader/cursor.go,
    index.go, feed_range.go) and adopted by the CLI page loops
    (handlers/cursors.go, feeds.go) — per-page O(log n), no leading-
    entry consumption; parity manifest +3 rows; new cursor_seek and
    index_seek unit tests.
  - Response ceiling: boundedResponse now checks the marshaled length
    in O(1) and only parses/re-marshals on overflow (rpc/session.go).
  - matching_feeds zero-match now emits `"feeds": []`; a single
    session probe against both binaries returns `[]`/`'0'` for
    203.0.113.1 and `["alpha","beta"]`/`'2'` for 192.0.2.15, matching
    Rust exactly.
  - Wire decoder: the SDK publication-result decoder now matches Rust
    strictness in all 10 reviewed cases (null `coordination_cleanup`/
    `visible_housekeeping`/`artifacts` rejected, `housekeeping: {}`
    rejected, canonical decimals, strict canonical base64 via
    decodeCanonicalBase64 with Rust alphabet/padding rules, strict
    problem objects with exact members and i32 `os_code`, canonical
    wire-code vocabulary via internal/format ErrorCodeWireName/
    ErrorCodeFromWireName, optional cleanup-artifact basename,
    lowercase-hex identities). A 12-mutation CLI probe
    (publication.resolve with mutated publication_result objects)
    returns `-32602` on both binaries for every case.
  - value_tag empty hex accepted (`{"hex": ""}`), matching Rust.
  - Panic guard: workflow.go typed-assertion guard; the
    mustMemberObject helper removed at all 10 call sites.
  - Key-lookup bugs: decodeFileIdentity/valueTagFromWire/creation-
    security no longer use dotted paths as member keys.
  - Leaks: closeEphemeral helpers in feeds.go/algebra.go and
    export.go close immutable readers.
  - Cleanup: dead code removed (osCodeOf, CreationStateName,
    asBytes16, MetadataResult, IsLive, containsNUL, asUint64FromRaw,
    DurabilityOutcome alias, firstLine, `_ = text`); errUnexpected
    collapsed into errString; single authorities for canonical u64
    strings (params.go), value tags, and feed-name validation;
    maintenance identity exact-member check; RecordClosed simplified;
    typo fixes.
  - Docs: `v4/cli/README.md` added (both binaries, JSON-RPC stdio
    protocol, limits, runner commands, known limitations).
- Re-validation at the final tree (matrix dirs /tmp/cli-m3v1-3,
  fresh per matrix) — all green:
  - Matrix go 31/31, rust_to_go 31/31, go_to_rust 31/31 (fresh
    fixtures per matrix; oracle 15 in matrix go).
  - check_golden.py 53 exchanges PASS; sensitivity_gate.py 14 modes
    PASS.
  - Go suite 22 packages PASS; `-tags v4work` PASS; go vet clean;
    gofmt clean.
  - Legacy C-oracle suite (IPRANGE_BIN=/tmp/iprange-go): 100/100
    PASS; mmap trace gate PASS.
- Next: delta five-reviewer round at the new HEAD (same five scopes),
  then the glm-5.3-responses whole-milestone review, then milestone-3
  close-out record in one lifecycle commit.

### 2026-09-02 (continued) — delta five-reviewer round at 1ca05c28 (2 PASS, 3 FAIL) and second fix wave

- The post-fix-wave delta round ran the same five own-model
  reviewers at HEAD 1ca05c28 (after 44-file fix-wave commit). Verdicts:
  performance PASS (two negligible P3 notes), wire integrity PASS
  (one latent P3: private-output identity member), API/docs FAIL,
  Go idioms FAIL, Rust parity FAIL.
- Findings fixed in this wave:
  - P2 (parity): the Go "strict" canonical base64 decoder accepted
    non-canonical trailing bits in the final quartet where Rust
    rejects them (`AB==`, `Zh==`, `Zx==`; Rust lifecycle.rs
    decode_base64). Affected publication.resolve destination
    basenames, database.metadata replace_base64, and
    maintenance.remove entry basenames. One authority:
    internal/format.DecodeCanonicalBase64 now implements the exact
    Rust rules (multiple-of-four length, standard alphabet, end-only
    padding, zero trailing bits) and the CLI validator, the metadata
    blob decoder, and the SDK wire decoder all delegate to it. Live
    probes confirm both binaries now return -32602 on identical
    non-canonical inputs.
  - P2 (idioms): two identical 69-entry error-code wire tables
    (internal/format/codes.go and handlers sdkCode switch). The
    format table is now package-level (no per-call map build, O(1)
    reverse lookup) and sdkCode delegates to it; a parity test pins
    69 unique round-tripping names.
  - P2 (docs): v4/cli/README.md run.py examples used relative
    executable paths that the runner rejects; now absolute through
    $PWD variables. The README also named a nonexistent
    `transport_server_busy` error; corrected to `server_busy`.
  - P3: contradictory comment/assertion in the wire round-trip test
    corrected (explicit null later_attempt_or_sidecar_id now
    asserted to fail); a 15-case table-driven strictness test plus
    positive optional-member cases committed in
    publication_wire_decode_test.go; handler-level base64 strictness
    tests added (base64_strict_test.go); internal/format base64 unit
    tests added; dead isFeedEdge removed; maintenance
    private_output_attempt_value always emits identity (null when
    absent) matching Rust; session.go typo fixed; SOW stale counts
    amended (38 go-surface rows, 53-entry inventory with 52
    advertised); canonical u64 parsing consolidated in
    internal/format.ParseCanonicalUint64 with the CLI helper
    delegating.
- Re-validation at the final tree (fresh fixtures /tmp/m3f3-*,
  binary rebuilt from the working tree) — all green: matrices
  go/rust_to_go/go_to_rust 31/31 each (oracle 15 each), golden 53
  exchanges, sensitivity 14 modes, Go suite 22 packages plus root,
  v4work suite, legacy C-oracle 100/100, mmap trace PASS, vet/gofmt
  clean.
- Next: delta re-review of this wave by the same three FAIL
  reviewers, then the glm-5.3-responses whole-milestone review, then
  milestone-3 close-out.

### 2026-09-02 (continued) — delta-2 review at 299c5035 (2/3 PASS, 1 FAIL) and validator-strictness wave

- The delta re-review of the second fix wave at 299c5035 returned:
  Rust parity PASS, API/docs PASS, Go idioms FAIL (P2-C plus P3s).
- P2-C fixed (validator-stage canonical decimals): five (six with the
  delivery validator) param validators accepted non-canonical decimal
  strings at validation (non-digits like `abc`, leading zeros like
  `00`) that the Rust u64_string/positive_u64_string validators
  reject, deferring the refusal to the decode stage with a different
  message. All sites now parse through the single
  internal/format.ParseCanonicalUint64 authority at validation time:
  writer budget, validation/recovery budget, snapshot budget,
  recovery candidate transaction_id, maintenance tuple
  transaction_id and digest byte_length, and the file delivery
  max_output_bytes validator (which Rust validates with u64_string).
  The lax positiveDecimal helper is deleted; a table-driven
  validator test refuses `abc`, `00`, and overflow at every surface.
- P3 fixes: the base64-strictness CLI test now uses the real
  writer_budget shape (its negative arm was order-dependent on
  metadata-before-budget validation order); dead initializeReservation
  wrapper deleted; the 69-name wire-code round trip is now committed
  as an internal/format test; the CLI README no longer implies the
  empty benchmarks directory or bench.py exist.
- Re-validation at the final tree (fresh fixtures /tmp/m3f4-*): all
  green as recorded in the previous entry; no gate regressed.

### 2026-09-02 (continued) — delta-3 (5/5 own-model PASS), glm whole-milestone review (FAIL), and the exact-HEAD fix wave

- Delta-3 of the incremental five-reviewer rounds (same five
  own-model reviewers, one scope each) recorded 5/5 PASS:
  - Gauss (Rust parity) FINAL PASS after the canonical-decimal
    validator wave: a 13-case identical-input probe matched both
    binaries on every validator surface; error-code vocabulary
    69/69 identical.
  - Avicenna (Go idioms) DELTA3 PASS after P2-C (six validator sites
    now parse canonical decimals at validation through the single
    internal/format authority; lax positiveDecimal deleted) plus P3s
    (canonicalU64 alias removed, comments fixed, committed
    round-trip test).
  - Aristotle (performance) FINAL PASS: hot paths untouched by diff;
    smokes re-run (lookup ~29 us, flat paging).
  - Gibbs (wire integrity) DELTA PASS: no wire-shape drift; bases64
    trailing-bit rule and maintenance identity emission verified.
  - Locke (API/docs) FINAL PASS after README $PWD runner examples,
    server_busy name, committed strictness tests, and SOW count
    fixes; remaining P3 nits fixed in this wave (69-name committed
    test, benchmarks bullet rewording, delta-2 header count).
- The glm-5.3-responses whole-milestone review at 836df335 returned
  FAIL with proven findings; all were fixed in this wave:
  - P1 null handling: Go primitive decoders silently accepted
    explicit null (json.Unmarshal(null) semantics) for strings,
    numbers, booleans, arrays, and objects, while Rust's
    as_str/as_u64/as_object validators reject null. All primitive
    decoders (params.go asString/asBool/decodeUint64/asStringArray/
    asObjectArray/asOptionalObject/decodeObject, lifecycle_live
    wireString/wireBool) are now null-strict; the scratch_directory
    and maintenance artifact/problem null-as-absent special cases
    were removed. A 5-surface live probe (scratch_directory null,
    writer_budget.max_open_files null, reader.open source null,
    reader.open mode null, delivery.max_output_bytes null) now
    returns -32602 on both binaries; null-per-type negative tests
    committed.
  - P1 wire bytes: Go's default json.Marshal HTML-escaped <>& and
    U+2028/U+2029 while Rust emits raw UTF-8. A serde_json-compatible
    encoder (internal/cli/rpc/rustjson.go) is now the single byte
    authority for response envelopes, echoed ids, and generated JSONL
    rows; live probes show identical wire bytes for "<&>&" and
    U+2028 ids; committed byte-vector tests pin the escape set.
  - P2 parity inventory: parity_rust_public.tsv was frozen at the
    SOW-0027 closure; the Rust public surface added since is now
    inventoried and ledgered (CommitCleanupArtifacts::clean present
    via the Go zero value, ::tail removed, CleanupArtifacts::
    from_entries removed, FeedCursor::seek_by_index present,
    LiveWriter::address_family removed, LocalBasename::from_path
    removed, PublicationProblem::owned removed with recorded type
    divergence, validation.rs worker_availability removed). The
    full raw inventory was re-sorted; the gate passes all three
    directions.
  - P2 canonical-decimal test claim: the table now also refuses
    abc/00/overflow on the recovery candidate, maintenance tuple,
    and digest surfaces (TestCanonicalDecimalEverySurface) plus a
    valid maintenance.remove housekeeping entry and null
    artifact/problem negatives.
  - P2 records: this entry records the delta-3 outcomes, the glm
    review, and the exact-HEAD validation below; prepared binaries
    are rebuilt per run.
  - P3: stale alpha binaries replaced by exact-HEAD builds; dead
    commented imports removed from rpc/state.go.
- Exact-HEAD validation plan (final run happens after this entry is
  committed, at the commit revision): build product and worker
  together into one fresh directory; run the three 31-case matrices,
  golden, sensitivity, Go suite, v4work suite, legacy C-oracle
  100/100, mmap trace; record embedded vcs.revision and SHA-256 of
  both binaries in the close-out entry.

### 2026-09-03 — milestone-3 close-out: exact-HEAD validation evidence and glm final re-review

- The glm-5.3-responses whole-milestone re-review at e0bbd5a8
  returned FAIL with one new proven P1 (value-tag null bypass) and
  record/encoder nits; all were fixed in commit 82828999:
  - P1 value-tag: validator and replay value-tag decoders accepted
    `{"text":null}` / `{"hex":null}` as the zero-byte tag and created
    a durable database; Rust rejects with -32602. Both decoders now
    refuse present null; a live probe shows database.create with a
    null value-tag member returns -32602 on both binaries and creates
    nothing; committed negatives cover both members plus the legal
    empty forms.
  - Complete null sweep: every json.Unmarshal into a primitive string
    or slice in the CLI handlers now rejects null (cursor start, feed
    members, prefixes, algebra sources/addresses/windows/values,
    housekeeping/cleanup artifacts and state, and the last optional
    helper asOptionalString) - the full null-as-zero class is closed.
  - P3 encoder: rustjson adds direct []string/[]json.RawMessage/
    []int64/[]uint64 cases and a recursive re-emit fallback so no
    wire bytes fall back to encoding/json escaping; a []string
    byte-vector test pins the escape set.
  - P3 inventory: the module-only WorkerAvailability type is now an
    explicit inventory row with a recorded divergence.
  - P3 decimal matrix: every named decimal surface is now covered by
    all three bad values (abc, 00, overflow).
- Exact-HEAD validation evidence (revision 82828999, all gates run
  with the fresh pair built from that revision):
  - Product /tmp/iprange-final/iprange: vcs.revision
    8282899930ce02e823a352151ef687af0b2083b8, SHA-256
    4ae1673f52ade4324322193d37ec611c558d0aac83879afde206487e3ec1088c.
  - Worker /tmp/iprange-final/iprange-v4-worker: same embedded
    revision, SHA-256
    28f97d6c15fb7efc51caa0afed19b31498f3f096a6b49a92aeced34717a1b961.
  - Matrices: go 31/31 (oracle 15), rust_to_go 31/31 (oracle 15),
    go_to_rust 31/31 (oracle 15), 0 skips; fresh work dirs
    /tmp/m3f6-* and /tmp/m3f7-* (pre-commit tree) and /tmp/m3f8-go
    (exact 82828999 binary).
  - Golden 53 exchanges PASS; sensitivity 14 modes PASS; Go suite 22
    packages + root PASS; v4work PASS; legacy C-oracle 100/100 PASS
    (IPRANGE_BIN=/tmp/iprange-final/iprange); mmap trace PASS; vet
    and gofmt clean.
  - Cross-language probes at the exact binary: null-per-type and
    value-tag null requests return the identical machine outcome
    (-32602) with no residue on both binaries; "<&>&" and U+2028 id
    echoes are byte-identical; matching_feeds zero-match identical.
    (Human diagnostic message text differs on some refusal paths, as
    the spec's message field is explicitly human-diagnostic.)
- This milestone-3 entry is the delivery-step-4 close; the remaining
  SOW steps (consolidated benchmark harness and platform/artifact
  gates) continue as the next milestone after the glm final verdict
  and the five-reviewer consensus recorded above.

### 2026-09-03 (continued) — glm final re-review (third round) and record-proof corrections

- The glm-5.3-responses re-review at 5810eaf6 returned FAIL with
  three P2 record/proof findings and two P3s; the executable
  behavior was verified green (value-tag nulls refused with no
  residue on both binaries, all gates pass). Corrections in this
  wave:
  - P2-1 asOptionalString: the last direct primitive helper now
    rejects present null (absent-only), and its sole caller
    (reader.ranges.open view.feed) was already protected by
    validateView; the null-sweep claim is now exactly true.
  - P2-2 delivery matrix: delivery.max_output_bytes negatives now
    cover abc, 00, and overflow (full Cartesian matrix on every
    named surface).
  - P2-3 wording: the close-out record now says the value-tag/null
    probes return identical machine outcomes with no residue and
    notes that human diagnostic message text is not byte-identical
    on refusal paths (spec: message is human-diagnostic); the
    byte-identical claims are limited to the id/escape probes where
    they are true.
  - P3 WorkerAvailability: the inventory row remains the type-carrier
    convention (the inventory format carries types as lib-reexport
    rows or method owners) and its divergence note names it as a
    module-only public type; the gate passes.
  - P3 local-build-objects.stamp: the empty build-artifact stamp is
    now gitignored so the worktree stays clean for build provenance.
- Re-validation after corrections: Go suite, v4work, vet, gofmt,
  matrices, golden, sensitivity, C-oracle, mmap trace - all PASS
  (detailed results in the close-out entry above).
- Exact-HEAD binary record (after the final production-source
  correction in a4dd504c): fresh pair rebuilt at a4dd504c with
  vcs.modified=false — product SHA-256
  6cce9deb74ceef67471853fb6afd74f5cffda0f6e874a42e826e8e93a826167a,
  worker SHA-256
  e9ca6ff4d162d86a9f5362c544d422f7b839c28d8031c42200b80e9ab2bf411a.
  The earlier 82828999 pair (hashes above) was exact for its wave;
  a4dd504c changed only asOptionalString null-strictness (an
  already-unreachable-by-validation helper), so the pairs are
  behaviorally equivalent on every public request. The a4dd504c
  pair is the exact identity for milestone-3 close-out.

### 2026-09-03 (continued) — milestone-3 closed: glm whole-milestone final review PASS

- The glm-5.3-responses whole-milestone review PASSED at 5151992e
  after four review rounds (FAIL at 836df335; FAIL with value-tag P1
  at e0bbd5a8; FAIL with record-proof P2s at 5810eaf6; PASS after
  the record corrections). The final round verified: value-tag null
  refusals with no residue on both binaries, byte-identical id and
  escape vectors, the complete null sweep, the full decimal bad-value
  matrix, fresh exact-HEAD binaries with vcs.modified=false, clean
  worktree, and accurate close-out records.
- Milestone 3 (delivery step 4: the pure-Go JSON-RPC product
  executable at v4/go/cmd/iprange with the CLI-only SDK surfaces) is
  closed. Final review consensus: five own-model adversarial
  reviewers (Rust parity, Go idioms, performance, wire integrity,
  API/docs) PASS through incremental rounds, and the user-mandated
  glm-5.3-responses whole-milestone review PASS at exact HEAD
  5151992e.
- Remaining SOW steps (delivery step 6: consolidated benchmark
  harness and measured ceilings; step 7: platform/artifact/docs/
  skill/final gates) form the next milestone and continue in this
  SOW.
