# SOW-0028 - Production `iprange` CLI, JSON-RPC API, And External Qualification

## Standing Review Rules (user-mandated, read after every compaction)

0. When the user asks to use the swarm, read and follow the user's
   swarm rules file in whole (`~/.codex/SWARM.md`); never skim it.
1. Prefer workers and reviewers in the lead assistant's own model.
2. Use `glm-5.3-responses` for the final review of the whole milestone.
3. Parallelize with the lead's own model as much as possible (the more
   the better); never block on a single worker.
4. Spawn multiple reviewers of the lead's own model, each with a
   different focus, then run one `glm-5.3-responses` reviewer to
   validate the entire milestone before closure.
5. Running workers are never stopped by these rules; spawn in parallel
   instead.

### Role-based review protocol (user-approved 2026-09-06; overrides
the generic swarm template for SOW-0028 rounds)

Seven standing reviewer roles, each with a permanent sandbox under
`.local/<role>/` (gitignored) and a `ROLE.md` containing its mission,
responsibilities, way of working (adversarial audit), and instructions.
The lead writes each ROLE.md once and reminds the role to read it in
whole on every invocation:

- `.local/tester/ROLE.md` — tester: milestone acceptance criteria and
  core claims; every claimed contract needs a test that would fail if
  the claim were false (missing detecting test = P1); mutation battery
  of the gates/harnesses; coverage floor measured and reported.
- `.local/operations/ROLE.md` — operations: what can go wrong that is
  not handled; transport failure composition, process lifecycle and
  crash windows, deadlines and stalls, boundary and
  resource-exhaustion conditions.
- `.local/parity/ROLE.md` — parity: Go/Rust wire-format and semantic
  equivalence; queue/cancellation/exit-code parity; spec authority.
- `.local/portability/ROLE.md` — portability: native-language idioms
  (Go goroutines/mutexes, Rust ownership, no unsafe beyond approved
  boundaries), cross-OS behavior (Windows host, FreeBSD, macOS), the
  mmap-only/zero-copy/test-only-observability policies.
- `.local/security/ROLE.md` — security: untrusted-input handling,
  temp-file and identity races, fault containment, secrets in durable
  artifacts; records: evidence/SOW/README truthfulness, SHAs,
  verification of exact-revision verdicts, documentation completeness.
- `.local/performance/ROLE.md` — performance: allocations, copies,
  mmap policy, adapter-level overhead (SOW-0028 scope), benchmark
  methodology for milestone 5; engine residuals are SOW-0030-owned.
- `.local/glm/ROLE.md` — glm-5.3-responses final whole-milestone
  validator: redundant adversarial validation of the entire milestone
  at the exact revision after the six roles report; same sandbox
  rights.

Shared, read-only review material lives under `.local/shared/`
(`binaries/` with SHASUMS, `probes/` with the accumulated failure
reproducers, `README.md`).  The tree is read-only for every role;
roles run probes, stub products, and mutate evidence only inside their
own sandbox.  The repository reviewers' own-model roles are spawned
with the lead's model except `.local/glm/`, which uses
glm-5.3-responses.

Binding severity convention for all roles (from the user): P0 = data
corruption/crash/security; P1 = wrong behavior on valid input, OR a
contract the milestone explicitly claims with no test that would
detect its violation (weak assertions that accept invalid input count);
P2 = contract/records/measurable-performance defects or bypassable
gates; P3 = cosmetic.  The static-only clause of older reviewer
briefs is repealed: every role may run anything inside its sandbox.

Every role's verdict and numbered findings are recorded in the SOW
wave section (lead verifies each finding independently before fixing,
as before).  glm's verdict closes or reopens the milestone gate; a
sol/astra-style external control review may still be requested by the
user as an independent check.

## Status

Status: in-progress

Wave-10 state (2026-09-06): the first role-based review round
FAILed with eleven verified findings; all four user decisions
(D1-A, D2-A, D3-A, D4-A) were approved; the second round FAILed with
ten verified findings, all repaired and validated; the third round
found and closed the Go clean-EOF signal race and the Rust
eof-first contract gap (the Rust EOF tail now polls the watcher's
recorded flag for the same 25 ms grace Go uses), plus one
committed-test coverage gap.  ALL SEVEN ROLES PASS the final
revision `ca3fad02` (tester, operations, parity, portability,
security, performance, and the glm-5.3-responses whole-milestone
validator; verdicts recorded in the Tenth wave section).  Linux
evidence and the Windows-host regeneration at the final identities
(`de597e18…` rust, `b3a359c8…` go, Windows `877824f0…`/`984d0e9d…`)
are committed; the only shared residual is a signal the runtime
delivers after the 25 ms grace window and before process exit — the
bounded TOCTOU class now identical in both products, disclosed in
code and records.
Wave-13 state (2026-09-06): the whole-milestone control review wave
(waves 11-12) repaired the seven-wave finding set (full-stderr
shutdown hang on both fatal paths, held-open and EOF-resolved
over-limit frame handling, drain byte ceiling, duplicate-id
self-test control); the wave-13 role round then closed the last
Go/Rust framing divergence (Rust exited 0 on an exactly-LIMIT+1
frame at EOF).  ALL SEVEN ROLES PASS the final wave-13 revision
`b947d8a6` (tester, operations, parity, portability, security,
performance, and the glm-5.3-responses whole-milestone validator;
verdicts recorded in the "Role round verdicts -- wave 13"
section).  Milestone 4 (delivery step 5) is RE-CLOSED at
`b947d8a6`: functional parity and qualification PASS; the <=1.3x
performance requirement FAILED and is not waived, owned by pending
SOW-0030; milestone 5 (delivery step 6, dual-language CLI conformance
benchmarks) remains unstarted per user decision 1A.  The wave-13
Linux product identities are Go `7f88bb7c…` and Rust `24733db0…`; the
Windows housekeeping evidence is at Go `42173bb7…` / Rust
`de902a73…` built at `5346f716` (the recorded identity of the
wave-13 committed report).

Wave-14 state (2026-09-07): the external whole-milestone control
review of the wave-13 revision returned NEEDS CHANGES with nine
findings (held-open LIMIT+1 non-CR frames wedged both products
without the -32001 + close; the Rust Windows `main_basename`
round-trip emitted NUL-interleaved UTF-16LE bytes; the full-stderr
signal tests no longer exercised the watchdog; a Rust full-stderr
fixture read past a one-byte buffer; kind-gate and proof-a/d gate
gaps; Go StdoutPipe/Wait test races; two record P3s).  All repair
work is recorded in the "Wave 14" section below; the wave-14
product-source revision is `e272c990` with Linux identities Go
`d228ebe5…` and Rust `6ab63dfd…` (worker and fixture are
`202a83ac…`/`cb9ad6cd…`/`d615488f…`), Windows identities Go
`fb6b503a…` and Rust `a6ec5b45…`, and every battery gate green at
the wave-14 revision (matrices 38/38 single, 14+24 mixed; crash
16/16; resource 8/8; golden 55; sensitivity 14; kind gate PASS;
Windows housekeeping 2/2 with clean `main_basename` from both
products).  The milestone-4 closure recorded at `b947d8a6` is
re-opened pending the wave-14 external control re-review (the
wave-14 role round PASSed at `3d090ccf`; verdicts are recorded in
the Wave-14 delta section); milestone 5 remains unstarted per
user decision 1A.



User decision (2026-09-06, recorded before the milestone-4 closure
record below): milestone 4 (delivery step 5) is CLOSED at the final
wave-10 revision; milestone 5 (delivery step 6, consolidated
benchmark harness and measured ceilings) is NOT started; SOW-0028
remains the sole active SOW.

Wave-11 state (2026-09-06): the external whole-milestone control
review of the wave-10 closure revision `155459b0` returned FAIL with
one production shutdown defect and seven qualification-framework
defects (recorded in the Eleventh-wave section below).  All eight
findings are repaired, validated, and the Linux evidence and Windows
host qualification are regenerated at the new product identities
(`eb08c3d4…` rust linux / `19474f14…` rust windows, `c0204ade…` go
linux / `fce7acf5…` go windows).  Milestone 4 acceptance is REOPENED
by this wave and is re-closed by the Eleventh-wave record once the
internal role round passes the exact final revision.

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
  parallel fix batches implement them.
- All delivery steps 1-4 are implemented: the Rust JSON-RPC transport and
  read-only families, publish/lifecycle/export/snapshot families, the Rust
  legacy CLI surface vs the C oracle, and the pure-Go JSON-RPC product
  executable (v4/go/cmd/iprange). Milestone 3 closed at 92b5e6f9 and
  a71b2010 starts milestone 4; milestone-3 closure was reopened on
  2026-09-03 because the cross-language matrices ran only one binary
  per case (see the reopened closure record below).
- Milestone 3 was re-closed at d956d8f2 after the actor-semantics
  rework of the cross-language matrices (five-reviewer round and
  glm-5.3-responses review PASS). A second gate review by the named
  reviewer then rejected the recorded review claim (the five reviewers
  passed before the d956d8f2 production fix) and the global method-
  class actor routing as insufficient for milestone-4 transformations.
  The explicit-actor decision (1A) was adopted: every rpc case step
  declares actor producer|consumer. The explicit-actor wave is
  committed (fb6f5d8c) with the five-scope rerun and glm-5.3-responses
  confirmation PASS; a third gate review removed the last routing
  fallback (single-authority model, sensitivity steps declare actors)
  and corrected the binary/review identity records. Milestone 3 is
  re-closed. Milestone 4 (delivery step 5) was closed at 37721d8b and
  reopened on 2026-09-04 by an external gap review (six recorded
  findings: successful recovery untested, weakened mixed-live
  reclamation proof, file-kind ledger without per-case lineage and a
  missing-kind gate, crash scenario A2 accepting a foreign
  destination, disconnected downstream workflow steps, and an
  overclaimed resource proof). The fix wave re-qualified the
  milestone (records below): successful recovery is covered by the
  new recover.successful case with captured inspect candidates,
  mixed live coordination now pins a Rust and a Go reader
  simultaneously and proves reclamation waits for the last close
  (aliased captures), the ledger keeps per-case path/actor lineage
  and `v4/cli/check_kind_coverage.py` fails the battery when a
  required kind was never observed, crash A2 compares the
  reservation digest (with the new A3 foreign-destination negative
  control) and the new C scenario proves authorized-scratch
  durability at a CRC-valid header marker, the publisher workflow's
  downstream aggregation/join/algebra consume the workflow-built
  live feed DB, and the resource record keeps its NOT-PROVEN items
  explicit. Milestone 4 was re-closed at d6c9990b after the
  targeted delta review and the glm-5.3-responses whole-milestone
  re-review (records in the reopen section below), and was reopened
  again on 2026-09-04 by a second external gap review with five
  verified findings (kind-gate contract under-enforcement, crash
  scope per the recorded plan, resource deferral without a user
  decision, workflow recovery composition, and review identity plus
  personal paths in the evidence).  Four of the five are fixed in
  the third fix wave recorded below; the three scope items
  (additional crash scenarios, the four resource NOT-PROVEN items,
  in-workflow recovery) were resolved by the user on 2026-09-04 as
  D1-A, D2-A, and D3-B (recorded in the decision section below),
  together with the two remaining repairs (kind-gate provenance and
  the personal path in the SOW itself).  The fourth fix wave
  (recorded below) implements the three new crash scenarios
  (commit/finish, export, validate), the four resource proofs at the
  product interface (pipelined `server_busy`, the -32001 over-limit
  close path, `maintenance.remove` against a real reservation nonce,
  and the `windows_housekeeping` kind on the Windows validation
  host), the truthful in-workflow recovery wording for D3-B with a
  zero-residue maintenance assertion, and the executed-actor
  provenance repair of the kind gate; it also fixed the two
  same-class Go adapter temporary-remove defects found by the Windows
  qualification.  The five own-model delta reviews and the
  glm-5.3-responses whole-milestone review all PASSED at the exact
  final revision `9b3af7d9` (records in the wave section below), so
  milestone 4 (delivery step 5) is re-closed at that revision.
  Delivery step 6 (consolidated benchmark harness and measured
  ceilings) is the next milestone, recorded below.
  Milestone 4 was reopened a third time on 2026-09-04 by the
  whole-milestone review at `14ce284e` (four P1 defect classes and
  seven P2 findings; see the fifth fix wave below).  The fifth fix
  wave repairs the Go JSON-RPC queue/cancellation contract, the
  opaque `maintenance.remove` row contract, the crash markers D/E/F
  (with the recorded D impossibility evidence), the bypassable kind
  gate, and the resource/Windows P2 items, then re-qualifies the
  milestone.
  The sixth fix wave pinned the busy-batch transport corner (a slow
  first member frees one queue slot at a time, so 1 active + 16
  queued hold exactly), hardened the kind gate's
  executed-command/provenance binding, recorded scenario D/F crash
  markers per the ratified amendment, and repaired the
  resource/Windows proof gaps; the whole-milestone gate review of
  that wave FAILed (two P1 session defects, one P1 kind-gate defect,
  and six P2 proof defects; records below).  The seventh fix wave
  installs per-member cancellation in both sessions (cancelling a
  queued batch member no longer touches unrelated active work),
  bounded Rust transport channels with immediate all-rejected batch
  answers, the non-zero framing-failure exit in both products,
  the mixed-matrix two-actor kind gate with executed-operation and
  binary-record binding, truthful sidecar/adapter-output open
  lineage, the exactly-one export-temp orphan contract, the
  deadline-bounded resource harness, and the strict two-row /
  exact-50-record Windows housekeeping checks (report schema v3).
  The five own-model scope reviews of the seventh wave ran at the
  exact final revision `26ce667c`: three scopes PASSed and two FAILed
  (Rust P1, gates P2, records P1/P2; the eighth fix wave below
  repairs them and regenerates the evidence at the new canonical
  binaries).

  The eighth fix wave is committed (`90a935b2` repairs, `f73968a2`
  Windows qualification); the delta five-reviewer round and the
  glm-5.3-responses whole-milestone review both PASSed at the exact
  final revision `f73968a2` with zero P1/P2 findings (verbatim
  verdicts in the wave section below).  The external whole-milestone
  gate review of the record revision `700e7de9` then returned FAIL
  with one P1 transport defect and six P2 qualification defects, all
  independently reproduced and repaired in the ninth fix wave below:
  the broken-stdout shutdown deadlock in both session
  implementations, the proof-b forced-kill acceptance and
  response-envelope gaps, the missing deadlines in the shared
  JSON-RPC client path, the abbreviated command-override provenance
  bypass, the crash operation-ordinal attribution, and the Windows
  cross-listing `source_basename` comparison.  A new five-reviewer
  + glm-5.3-responses round runs at the exact final revision of the
  ninth wave; milestone 4 is NOT closed and awaits both that round
  and the user decision recorded below.

Wave-15 state (2026-09-07): the external whole-milestone control
turn-2 review of the wave-14 revision returned NEEDS CHANGES with
seven product/gate findings and one record P3; all are repaired in
commit `2ddeb751` (lifecycle identity platform kind in both
products, byte-preserving artifact-basename wire mapping and its
decoders, drain-EOF proof enforcement, matrix fixture-identity
binding with control #44, conflict-order control #43 hardening,
strict response-id correlation, and the wave-13 Windows identity
record correction).  A follow-up native-Windows verification wave
then made the Rust CLI suite fully green on the authorized Windows
validation host (711 tests across `iprange-livedb` and
`iprange-cli`; test temp names are Windows-valid, evidence tests
pin the platform identity kind, the missing-file parse error test
is platform-aware, and the snapshot wire test pins the documented
per-platform housekeeping state), reported the validation worker as
unavailable instead of a raw file-not-found I/O error when no
matching worker executable exists, and re-qualified Windows
housekeeping 2/2 at the final wave-15 revision `e21784ce` (Go
`eec23536…`, Rust `dd2d0668…`).  The wave-15 role-round delta
repaired four verified findings (encoding-aware artifact-basename
rendering, Go worker-availability fallback parity, the pinned
resource-gate self-test controls, and the refreshed
resource-record identities).  Final Linux identities at the
role-delta revision `13a1982e`: Go `9e78de86…`, Rust `73cb0626…`,
workers `6012ad6e…`/`9fd36146…`, fixture `6c2c56b9…`, Windows Go
`857b84af…` / Rust `9f6107ae…`; every battery gate is green at
these identities (matrices 38/38 single and 14+24 mixed, crash
16/16 both directions with the /bin/false negative control failing
as designed, resource 8/8, golden 55, sensitivity 14, kind gate
PASS, all harness self-tests including the new CRLF and
id-correlation controls), and the Windows housekeeping harness
passes 2/2 at the same revision.  The closure record and the
role-round delta verdicts are recorded in the "Wave 15" section
below.


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
  "cancelled" outcomes.  RESOLVED by the seventh fix wave: both
  sessions now install a fresh cancellation token per executing
  member and track only that member as active, so cancelling a
  queued or unknown id can never cancel an unrelated executing
  sibling; both languages carry regression tests (round-12 glm
  observation).
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
  surface) — the mixed 31/31 claim is a false positive (each case ran
  only the consumer binary); see the 2026-09-03 reopen record.
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
    fixtures per matrix; oracle 15 in matrix go) — the mixed 31/31
    claim is a false positive (each case ran only the consumer
    binary); see the 2026-09-03 reopen record.
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
  go/rust_to_go/go_to_rust 31/31 each (oracle 15 each) — this mixed
  31/31 claim is a false positive (each case ran only the consumer
  binary; see the 2026-09-03 reopen record below) — golden 53
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
    go_to_rust 31/31 (oracle 15), 0 skips — the mixed 31/31 claim is
    a false positive (each case ran only the consumer binary); see
    the 2026-09-03 reopen record — fresh work dirs /tmp/m3f6-* and
    /tmp/m3f7-* (pre-commit tree) and /tmp/m3f8-go
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
  skill/final gates) continue after delivery step 5 in this SOW; the
  immediate next milestone (4) is delivery step 5, started at
  a71b2010 below.

### 2026-09-03 — milestone 4 starts (delivery step 5: cross-language, crash, resource, and publisher-workflow proof)

- Step-5 scope per the plan and acceptance criteria (expanded
  2026-09-03 to the full update-ipsets integration set that the
  design spec assigns to the SDK surface and to the coordination
  cases the earlier scope omitted):
  - Crash/cancellation proof at the product interface: kill the
    producer mid-workflow (publish/commit/finish/export/validate) and
    prove with both consumers in both directions that the outcome is
    truthful — no partial replacement, no false success after unknown
    outcome, no unrelated rollback, bounded residue, reopen succeeds.
    The crash harness is a separate external script that drives the
    normal JSON-RPC client over the product interface only; it adds no
    production test methods or hooks. It proves process-level
    interruption and subsequent resolution; exact internal crash-point
    coverage stays with the SDK crash gates.
  - Cross-language file-kind coverage audit: every persistent file
    kind created by each producer and opened/queried/exported/
    validated/transformed by both consumers. The kind ledger is
    mechanically derived from the executed both-actor case steps
    (producer mutation methods per case -> artifact kinds they create;
    consumer methods per case -> kinds they open/transform); it is not
    a manually maintained table.
  - Complete publisher-workflow proof: the full six-step
    update-ipsets production sequence composed only through JSON-RPC
    and filesystem actions in both language directions — (1) current
    feed -> immutable published v4 file; (2) first-seen and
    last-seen refresh of the same coverage; (3) serialized named-feed
    replacement in the membership database with prior-feed preservation
    on failure; (4) history projection of every configured window from
    one last-seen scan; (5) one-scan overlap aggregation, both provider
    joins (direct and membership), and global-name algebra with
    result publication; (6) snapshot of the live database plus
    validation, recovery, and cleanup. Per-feed failure isolation is
    proven: one failed feed does not roll back unrelated feeds that
    already committed successfully.
  - Resource proof at the product interface: bounded response frames,
    bounded cursor batches, bounded reader/cursor counts, no
    file-sized heap state in the adapters (the 65 KB ceiling and
    trace gates exist; a step-5 resource record consolidates them).
  - Simultaneous mixed live coordination in both directions: a Rust
    live reader and a Go live reader pinned on the same committed
    generation while a Go (and then a Rust) writer commits updates,
    proving live slot coordination, generation pinning, and reclamation
    across language boundaries.
- Cross-language/legacy correctness gates were green at milestone 3
  close: golden 53, sensitivity 14, tests.d 100/100, mmap trace. The
  matrix baseline was reworked on 2026-09-03 after the cross-language
  matrices proved to be false positives; the current actor-semantics
  baseline is go/rust 33/33 (oracle 23) and mixed 10 executed /
  23 skipped per direction (oracle 8) — see the reopen record.
- Next actions: (1) crash harness as a separate external runner
  script reusing the case definitions and the normal JSON-RPC client;
  (2) mechanically derived file-kind ledger; (3) resource record;
  (4) six-step publisher workflow script; (5) mixed-live coordination
  cases; (6) five-reviewer + glm final rounds before step-5 close.

### 2026-09-03 — milestone-3 closure reopened: cross-language matrix false positive (reviewer sol P1)

- The named reviewer (sol) proved the rust_to_go / go_to_rust
  matrices are false positives: run.py starts exactly one service per
  case with the consumer binary (run.py:1202-1216); the producer
  label is stored but never spawned; csv_db/generator fixtures come
  from the separate v4-fixture tool. Empirical reproduction:
  `rust_to_go` with `/bin/false` as the Rust binary passes 31/31.
- Decision (recorded before implementation): rework the runner with
  explicit producer/consumer actors per step.
  - Each matrix case runs real producer steps (artifact creation,
    publication, feed mutations) on the producer binary and consumer
    steps (open, query, export, validate, transform) on the consumer
    binary, in separate service processes sharing only the work
    directory.
  - Steps are assigned to actors by method class (production methods
    -> producer; observation methods -> consumer) — superseded by
    Decision 1A (second gate review below): every rpc step now
    declares its actor explicitly. A case that cannot exercise both
    actors is recorded as a skip with its precise reason
    ("not cross-producer: case has no producer step" / "... no consumer
    step"), so every mixed-direction PASS means both binaries served.
    Cross-actor handle/result captures are rejected (only filesystem
    paths may cross); a mixed direction that executes no both-actor
    case at all fails with "matrix executed no cross-producer case".
  - The mixed matrices FAIL when either actor binary cannot serve
    (sensitivity: /bin/false as either actor exits 1 with case FAILs).
  - The report records the SHA-256 and executed-step count of each
    actor binary per executed case (report schema v3).
  - Single-language matrices skip JSON-RPC cases when the binary is a
    legacy CLI-only executable (C iprange has no --jsonrpc surface);
    inside a mixed matrix an unavailable binary can never be hidden:
    the mixed matrix exits 1, with its executable cases failing and
    required-method cases skipping as "requires unadvertised method"
    (verified with /bin/false as either actor, both directions).
- The milestone-3 closure record is withdrawn pending the reworked
  matrices, the exact-final-code five-reviewer round, and the glm
  re-review.

### 2026-09-03 — milestone-3 re-close wave: runner actor rework implemented

- v4/cli/run.py now runs a real producer service and a real consumer
  service for every mixed-matrix case (separate `--jsonrpc`
  subprocesses sharing only the per-case work directory). Actor
  routing is a method-class map (PRODUCER_METHODS /
  CONSUMER_METHODS in run.py) — superseded (Decision 1A, second
  gate; single-authority declared-actor model, third gate); captures
  are actor-scoped and a
  cross-actor reference is an assertion error; cases are
  pre-classified so mixed PASS requires both actors to have executed
  steps. The report schema is v3 and records per-actor SHA-256 and
  step counts. Matrix runs on the exact pre-rework binaries:
  - `go` 33/33 (oracle 23), `rust` 33/33 (oracle 23);
  - `rust_to_go` 10 PASS / 23 SKIP (oracle 8), `go_to_rust` 10 PASS /
    23 SKIP (oracle 8) - every PASS executes producer steps on the
    producer binary and consumer steps on the consumer binary;
  - `c` 33 SKIP (legacy CLI-only binary, needs --allow-skips);
  - `/bin/false` as either actor in a mixed matrix: exit 1 with case
    FAILs (sensitivity verified both directions).
- Two new cases prove producer-created artifacts are read by the other
  language: `mixed.direct-created` (producer database.create +
  direct.replace, consumer live reader.open/lookup/ranges/close) and
  `mixed.membership-created` (producer database.create + feeds.create
  from the shared fixture, consumer live reader.open/lookup/
  matching_feeds/ranges/close). Snapshot, publication,
  database.metadata, join.direct, history.project, live.lifecycle,
  maintenance, and validate.recover also run both binaries per
  direction. The runner's `check_source_close` now exempts
  `reader.open` (the reader handle owns the source lifetime; live
  close facts ride `reader.close`, per iprange-jsonrpc-v1.md).
- Re-validation at this wave (exact binaries from the withdrawn
  close-out): go/rust 33/33, mixed 10/10 executed, golden 53,
  sensitivity 14, C legacy tests.d 100/100, mmap trace: all PASS.
- Still required before re-closing milestone 3: the exact-final-code
  five-reviewer round, the glm-5.3-responses whole-milestone re-review
  on that exact HEAD, then one lifecycle commit and push.

### 2026-09-03 — milestone-3 re-close wave: exact-final-code five-reviewer round PASS

- Round basis: the code of the 63a6c001 wave — the runner actor
  rework (v4/cli/run.py), the two producer-created mixed cases, the
  history.project consumer tail, and the product fixes (-0 numeric id
  echo normalization in schema.go, rustjson bytes.Buffer encoder,
  dead test scaffolding removal), plus README and SOW record
  corrections. NOTE (second gate review, 2026-09-03): this round
  predates the glm-found -0 cancellation fix in d956d8f2; the
  five-scope rerun on the final functional tree is recorded below.
  All five own-model adversarial reviewers reviewed this content in
  their scopes:
  - Gauss (Rust parity): first-round FAIL — P2: history.project was
    classified CONSUMER although it is a LiveWriter mutation (Rust
    authority algebra.rs:877-923 opens LiveWriter and commits); P3:
    Go echoed a client id literal -0 while Rust normalized it to 0.
    Fixes: history.project moved to PRODUCER_METHODS and the case now
    ends with genuine consumer steps (live reader.open of the
    producer-projected histdb, gamma ranges, reader.close with
    source_close; the 400-window second projection is refused
    output_limit/not_started, so the DB stays at transaction 3 with
    the single gamma interval 192.0.2.0-19); validID normalizes -0 to
    0 and both binaries now echo id 0 byte-identically. Confirmed
    PASS.
  - Avicenna (Go idioms): PASS; two P3 cosmetics (dead base map in
    TestValueTagNullRejected, strings.Builder+String() copy in the
    encoder) both fixed. Confirmed PASS.
  - Aristotle (performance): first-round FAIL — P3: the mixed PASS
    path re-hashed both actor binaries on every executed case
    (~600 MB redundant disk reads per full run). Fixed: per-case
    SHA-256 now reuses the startup describe_capabilities hash.
    Encoder copy also removed (bytes.Buffer). Confirmed PASS.
  - Gibbs (wire integrity): PASS. P3 observations only: disconnect-
    cancellation message text differs between binaries (unpinned
    human-diagnostic class, code/outcome identical); reader.open
    live-close facts ride reader.close with no current corpus gap.
  - Locke (records): first-round FAIL with six record-precision items
    (stale 31/31 baseline and close-out claims, milestone-3 close
    commit identity, unavailable-actor wording, next-milestone
    cross-reference); all fixed and the 82828999 close-out bullet
    annotated as false positive. Confirmed PASS.
- Gate list at this wave (all under nice): go test ./... and
  -tags v4work PASS; go vet both modes PASS; gofmt clean; cargo test
  --all-features 851 passed / 0 failed (856 listed); Rust source
  graph PASS; legacy tests.d 100/100; mmap trace PASS; golden 53
  exchanges PASS; sensitivity gate 14 modes PASS; matrices go/rust
  33/33 (oracle 23), rust_to_go and go_to_rust 10 executed /
  23 skipped per direction (oracle 8), every PASS with both actor
  binaries serving (per-actor SHA-256 and step counts in report v3);
  /bin/false as either actor exits 1 with case FAILs in both
  directions.
- The user-mandated glm-5.3-responses whole-milestone re-review on
  this exact HEAD is the last gate; the re-close record follows it.

### 2026-09-03 — milestone-3 re-closed: glm-5.3-responses whole-milestone review PASS

- The user-mandated glm-5.3-responses whole-milestone re-review of the
  exact final code ran in two rounds at HEAD d956d8f2 (working tree
  committed; tree clean):
  - Round 1 FAIL with three findings, all fixed in d956d8f2: P1 — Go
    normalized a request id literal -0 to 0 for the response echo but
    the cancel correlation key kept the raw text, so a same-batch
    cancel of a -0 sibling missed on Go while Rust cancelled it; fixed
    with one canonicalIntegralText used by both the request-id echo
    and numberCancelKey, plus unit tests
    (TestRequestIDMinusZeroEchoedAsZero,
    TestSameBatchCancelOmitsMinusZeroSibling,
    TestNumberCancelKeyNormalizesMinusZero) and a live cross-binary
    probe (both binaries now omit the cancelled -0 sibling). P2 — two
    remaining stale mixed 31/31 claims (SOW 2298-2302 and 2422-2426)
    now carry the explicit false-positive annotation. P3 — the
    runner's generic source-close check now reads history.project
    params.last_seen as the database source it is.
  - Round 2 PASS at exact HEAD d956d8f2 with byte-identical fresh
    rebuild and all gates re-verified (matrices go/rust 33/33 oracle
    23, mixed 10 executed / 23 skipped oracle 8 per direction,
    /bin/false exit 1 both directions, go plain+v4work suites, vet
    both modes, gofmt, golden 53, sensitivity 14, tests.d 100/100,
    mmap trace).
- Exact-HEAD binary identity for the re-close (built from clean
  d956d8f2, vcs.modified=false): product (v4/go/cmd/iprange) SHA-256
  8a30e703e5988da698954bb0c47e1d8364010f6b81f6b3c0d68ec00eea334de6;
  worker (v4/go/cmd/iprange-v4-worker) SHA-256
  7033f26bfd459b555d6a610538fe1cab2347bbc2c84154adc26254e5ee335eee.
  The Rust binary is v4/rust/target/release/iprange built from
  d96797e0 (release build 2026-09-02, no Rust source change in this
  wave).
- Final reviewer consensus for milestone 3 (delivery step 4, the
  pure-Go JSON-RPC product executable): five own-model adversarial
  reviewers (Rust parity, Go idioms, performance, wire integrity,
  API/docs) all PASS — NOTE: this consensus line predates the
  production fix d956d8f2; the five-scope rerun on the final
  functional tree is recorded below — and the glm-5.3-responses
  whole-milestone review PASS at exact HEAD d956d8f2. Milestone 3 is
  re-closed with the cross-language matrices running real producer and
  consumer services in both directions.
- Milestone 4 (delivery step 5) proceeds with the expanded scope above
  (crash harness, mechanically derived file-kind ledger, resource
  record, complete six-step publisher workflow, mixed-live
  coordination), then delivery steps 6-7.

### 2026-09-03 — second gate review (named reviewer sol): explicit-actor decision adopted, five-scope rerun recorded

- The named reviewer verified the functional repair (go/rust 33/33 at
  that revision; 10/10 genuine mixed cases per direction; /bin/false
  detection; golden 53; sensitivity 14; targeted Go tests) but kept
  the milestone-3 gate FAIL because:
  1. The recorded five-reviewer round (2833) predated the production
     fix d956d8f2 (the glm-found -0 cancellation defect changed the Go
     RPC implementation, tests, and runner; 81 insertions / 17
     deletions across four files), so the record's claim that all
     five reviewers passed "the exact final code" was unsupported.
  2. The global method-class actor map (PRODUCER_METHODS /
     CONSUMER_METHODS by method name) cannot express milestone-4
     transformations such as "Rust creates this database, then Go
     transforms it": snapshot, recover, history.project,
     database.initialize_live, and algebra.publish were always routed
     to the producer binary, repeating the history.project failure
     pattern at the design level.
- Decision (sol Decision 1A, adopted before implementation): every
  rpc case step declares `actor: producer|consumer`; the declared
  actor is the single routing authority in every matrix. Single-
  language matrices run both roles on the same executable; mixed
  matrices run them on the two real binaries. Only filesystem paths
  may cross actors; handles stay actor-local in mixed mode. The
  method-name map was kept only as a fallback for tool-built step
  dicts that bypass the case schema (sensitivity gate) — superseded
  by the third gate review below, which removed every fallback and
  made the declared actor the single authority.
- Implementation of the decision:
  - v4/cli/schema/cases.py: rpc steps require `actor` (enum
    producer|consumer).
  - All 34 case files migrated: 135 rpc steps declare their actor
    (129 migrated by method class + 6 new transform-case steps).
    The corpus diff is exactly one actor line per rpc step; no other
    content changed.
  - v4/cli/run.py: routing, actor_requirements, and report step
    counts use the declared actor; cross-actor capture refusal
    applies only when two services exist (single-language matrices
    keep one shared service namespace); the mixed `requires`
    capability check needs the method on both product binaries and
    never hides an unavailable binary.
  - New case mixed.transform-created.json proves consumer-side
    transformation: producer creates and replaces a direct database
    (2 steps), the consumer binary snapshots it (iprange.v1.snapshot
    declared consumer) and opens/looks-up/closes the snapshot (4
    steps). Verified in both directions with per-actor step counts
    (producer 2 / consumer 4).
- Resulting matrix evidence (same product binaries as the re-close,
  since this wave changes only runner/schema/cases/docs/records):
  go 34/34 (oracle 23), rust 34/34 (oracle 23), c 34 skipped, and
  rust_to_go / go_to_rust 11 executed / 23 skipped (oracle 8) with
  every executed case serving both binaries; /bin/false as either
  actor exits 1 with 11 case FAILs (every executed case) in both
  directions; golden 53; sensitivity 14.
- The five-scope rerun on this final functional tree and the
  glm-5.3-responses confirmation are recorded below; the milestone-3
  re-close evidence record is updated by that rerun.

### 2026-09-03 — second gate review wave: five-scope rerun and glm-5.3-responses confirmation

- Reviewed tree identity: the explicit-actor wave (schema, runner,
  cases, README, records) staged on top of HEAD ccdda588; product
  code byte-identical to the d956d8f2 binary identity (product
  8a30e703e5988da698954bb0c47e1d8364010f6b81f6b3c0d68ec00eea334de6,
  worker 7033f26bfd459b555d6a610538fe1cab2347bbc2c84154adc26254e5ee335eee).
- Five-scope rerun verdicts (all five own-model reviewers re-reviewed
  the final functional tree in their scopes):
  - Gauss (Rust parity): PASS. Verified the d956d8f2
    canonicalIntegralText/numberCancelKey parity against Rust
    schema.rs:133-144 and session.rs:348-365, the declared-actor
    routing against Rust method semantics (snapshot declared consumer
    only in mixed.transform-created; Rust snapshot.rs:80-111 opens the
    source read-only; history.project stays producer), matrices
    34/34 and 11/23, /bin/false exit 1 with 11 case FAILs.
  - Avicenna (Go idioms): PASS. Verified schema.go/session_test.go
    idioms and the cases.py/run.py changes; P3 duplicate
    declared-actor fallback in actor_requirements/step_actor - fixed
    with one declared_actor helper.
  - Aristotle (performance): PASS. canonicalIntegralText is two
    call sites of trivial cost; step_actor is one dict lookup; no
    per-case binary I/O, no redundant service spawns.
  - Gibbs (wire integrity): PASS. Live probes confirm -0 echo and
    same-batch cancel byte-parity both binaries, cancel key
    collisions n:0/n:-0 identical, cancel-not-found unchanged; the
    transform case wire facts byte-accurate; snapshot file bytes may
    differ between implementations while each binary's reported
    sha512 matches its own file (truthful facts, no wire-contract
    requirement for byte-identical snapshot files). P3: the SOW
    /bin/false claim corrected from 10 to 11 case FAILs.
  - Locke (API/docs/records): PASS. Verified d956d8f2 stat exactly
    "4 files changed, 81 insertions(+), 17 deletions(-)", sol's two
    FAIL grounds recorded, Decision 1A recorded before
    implementation, counts match the tree (34 files, 135 steps,
    51 producer / 84 consumer), the first five-scope consensus line
    annotated as predating d956d8f2, README benchmarks corrected to
    delivery step 6. P3s fixed: consensus-line annotation, supersede
    note on the old method-class decision bullet, README declared-
    actor wording.
- glm-5.3-responses confirmation round on the same tree: one FAIL
  round (records P2s: rerun verdicts were not yet appended; the wave
  was staged not committed; two stale method-class comments in run.py)
  then this record + the wave commit make the reviewed content the
  final committed revision. The two stale comments were corrected
  (run.py: the method-class sets are documented as the fallback only;
  run_one documents declared-actor routing) and the wave is committed
  as one lifecycle commit with the record below.
- Final matrix/gate evidence for the explicit-actor wave (all under
  nice): go 34/34 (oracle 23), rust 34/34 (oracle 23), c 34 skipped;
  rust_to_go and go_to_rust 11 executed / 23 skipped (oracle 8) with
  every executed case serving both binaries; /bin/false as either
  actor exits 1 with 11 case FAILs; golden 53; sensitivity 14; go
  plain + v4work suites, vet both modes, gofmt clean; tests.d
  100/100; mmap trace PASS; rust 851 passed / 0 failed.
- Review identity (precise): the five-scope rerun reviewed the staged
  explicit-actor tree over ccdda588 whose exact content is commit
  fb6f5d8c831410565b7df7a528de40a1e56a686e (the wave commit). The
  glm-5.3-responses whole-milestone round FAILed on that staged tree
  (records/lifecycle P2s), and PASSed at
  65fc9b75ad5b212f3e0995df993fe96ecd265664, the record-only delta
  that named fb6f5d8c as the final lifecycle commit. The third gate
  review (named reviewer sol) then required: (1) the removal of the
  method-class fallback so the declared actor is the single routing
  authority, (2) sensitivity steps to declare actors, (3) precise
  binary identity, (4) stale status correction - each addressed by
  the final correction wave recorded below, which also runs one final
  exact-tree review on its committed revision.
- Binary identity (precise): the close-out binaries are now
  qualification builds with -buildvcs=false (no embedded revision),
  so their bytes are stable for the identical v4/go source regardless
  of lifecycle HEAD. Source identity proof: git diff d956d8f2..HEAD
  -- v4/go is empty (v4/go has not changed since the last
  product-source revision d956d8f2); the earlier recorded hashes
  (product 8a30e703..., worker 7033f26b...) were builds that embed
  vcs.revision d956d8f2, and a rebuild at any later lifecycle commit
  embeds that commit's revision and therefore hashes differently -
  those hashes remain valid for their exact builds, but they are not
  the close-out identity.

### 2026-09-03 — third gate review wave: single-authority actor model, final exact-tree review PASS

- The named reviewer's four findings and their corrections:
  1. P2 two routing authorities: run.py fell back to the method-name
     classification when a step omitted actor, and the sensitivity
     gate's synthetic steps used that bypass. Correction: deleted
     PRODUCER_METHODS, CONSUMER_METHODS, and method_actor() from
     run.py; declared_actor() now requires step["actor"] and raises
     on absence; all 9 synthetic sensitivity steps declare
     "actor": "consumer" (commit 65479dc2). No routing authority
     other than the declared actor exists anywhere in v4/.
  2. P2 recorded binary identity: go build embeds the current
     commit's vcs.revision, so any lifecycle commit changes binary
     hashes even with unchanged Go source; the close-out record must
     not describe later builds as byte-identical. Correction: the
     close-out identity is now the qualification build
     `nice go -C v4/go build -buildvcs=false -o ... ./cmd/iprange`
     (and ./cmd/iprange-v4-worker) with go1.26.4 linux/amd64, which
     embeds no vcs metadata and is byte-stable for identical v4/go
     source (rebuild reproduced the exact product SHA-256). Close-out
     identity: product
     4f8fb7b82fe4bcba7c7d039e77be1672c28c89cc110d641e3bffc76e799c86fa,
     worker
     16236608325cb189e0fbe05603886bbe150fd1ae83e4a8b532bfb7dd07054b1e.
     Source identity proof: git diff d956d8f2..HEAD -- v4/go is empty
     (the last product-source revision is d956d8f2). The earlier
     hashes (8a30e703..., 7033f26b...) remain valid only for their
     exact vcs-embedded builds and are not the close-out identity.
  3. P2 exact-final-review claim: the five-scope rerun reviewed the
     staged tree whose content is commit fb6f5d8c; glm FAILed on
     that staged tree (records/lifecycle) and PASSed at 65fc9b75,
     the record-only commit that named fb6f5d8c. The third-gate
     corrections were reviewed at their own committed revision
     65479dc2 by the same five reviewers (verdicts below) and by the
     glm-5.3-responses whole-milestone confirmation below.
  4. P3 stale status: the Status block now states the explicit-actor
     wave and the third-gate corrections are finished and milestone 3
     is re-closed.
- Final exact-tree review verdicts at commit 65479dc2 (tree clean;
  v4/go diff empty since d956d8f2): Gauss (Rust parity) PASS,
  Avicenna (Go idioms) PASS, Aristotle (performance) PASS, Gibbs
  (wire integrity) PASS, Locke (API/docs/records) PASS. P3s fixed in
  this record: close-out hashes + build command recorded (above),
  supersede annotations on the Decision-1A fallback sentence and the
  method-class routing claim in the re-close wave record.
- Gate evidence at 65479dc2 with the qualification binaries (all
  under nice): go 34/34 (oracle 23), rust 34/34 (oracle 23), c 34
  skipped, rust_to_go and go_to_rust 11 executed / 23 skipped
  (oracle 8), /bin/false as either actor exits 1 with 11 case FAILs
  (no capability-masking skips), golden 53, sensitivity 14, go plain
  + v4work suites, vet both modes, gofmt clean.
- glm-5.3-responses confirmation on the same committed revision is
  recorded below; with it, milestone-3 closure evidence (functional
  repair, exact-final-code five-scope rerun, glm whole-milestone
  confirmation, binary identity) all targets one committed revision.

### 2026-09-03 — milestone-3 re-close: final record (third gate wave complete)

- Final functional commit: 65479dc219bcc3fb8db7c5194cd75a49f202e771 (single-authority actor model, all case files, runner and qualification changes). Final record-only HEAD for this wave: the commit carrying this entry (this record), which is the only delta after 65479dc2 beside the records named below. Tree clean; v4/go unchanged since d956d8f2 (git diff d956d8f2..HEAD -- v4/go empty).
- Close-out binary identity (qualification build, go1.26.4 linux/amd64, -buildvcs=false, rebuild byte-stable): product 4f8fb7b82fe4bcba7c7d039e77be1672c28c89cc110d641e3bffc76e799c86fa; worker 16236608325cb189e0fbe05603886bbe150fd1ae83e4a8b532bfb7dd07054b1e. Rust binary: v4/rust/target/release/iprange built from d96797e0 (no Rust source change in any re-close wave).
- Review chain (all on the final functional content 65479dc2): five-scope exact-tree rerun PASS (Gauss, Avicenna, Aristotle, Gibbs, Locke; P3s fixed), glm-5.3-responses whole-milestone confirmation PASS (single-authority routing, sensitivity declarations, qualification identity, five-reviewer verdicts, gate evidence all verified; the only FAIL items of that round were the absence of this record and the unnamed final HEAD, both resolved by this entry).
- Status: milestone 3 (pure-Go JSON-RPC product executable) is re-closed with real two-binary cross-language matrices; milestone 4 (delivery step 5) proceeds per the expanded scope recorded above. All earlier stale claims are annotated; no fallback routing exists in v4/.

### 2026-09-03 — milestone 4 implementation plan (recorded before implementation)

Exploration summary (three parallel investigation passes,
2026-09-03): the runner `v4/cli/run.py` already executes per-step
declared actors on
persistent per-actor JSON-RPC services with captures, `assert_files`,
strict protocol/counter checks and report schema v3 (per-actor
sha256+step counts); it has no mid-case process control, so the
crash proof needs a separate external harness. Both product binaries
advertise the identical 52-method surface including the live
coordination methods; the runner can interleave producer and consumer
steps on one DB, and deterministic transaction ids make cross-binary
generation equality assertable. The case corpus (34 files) proves the
six steps only piecemeal: the full six-step composition, per-feed
failure isolation, multi-window projection, one-scan counter
equality, successful recover, real maintenance entries, live reader
pinning across commits, and resource-limit boundary behaviors are not
yet covered.

Implementation plan (each item minimal-complete, all validation under
`nice`, each wave validates before the next starts):

1. W1 runner evidence (v4/cli/run.py, v4/cli/README.md):
   - Mechanical file-kind ledger: after each executed rpc step the
     runner inventories the work directory and classifies every
     observed file against the spec kind table (v4 main file, live
     sidecar `<main>.readers`, publication reservation
     `.iprange-reservation-*.tmp`, authorized scratch
     `.iprange-scratch-*.tmp`, adapter outputs jsonl/csv/netset,
     metadata delivery file); the report gains two additive fields:
     `file_kinds` (kind -> methods that created it and methods that
     opened/transformed it, derived from executed case steps plus the
     observed inventory) and `frame_sizes` (max request/response
     bytes per method measured by the JSON-RPC client).
   - No schema change to the case format; additive report fields only.
2. W2 resource proof (new case `v4/cli/cases/resource.limits.json`,
   new record `v4/cli/resource-record.md`, README anchor): proves at
   the product boundary, in both single-language matrices, the
   documented ceilings — response object `output_limit` beyond the
   65,000-byte ceiling, `server_busy` at 17 queued requests, reader
   capacity exhaustion at the 65th open immutable reader, cursor
   capacity exhaustion at the 65th open cursor, and one
   `system.describe` limits report equal in both binaries. The record
   consolidates these observations with the existing 1 MiB frame
   caps, 4,096 lookup/cursor-page limits, the 65 KB
   response-object ceiling, the mmap trace gate, and the adapter
   memory evidence (bounded RSS, no file-sized heap state).
3. W3 six-step publisher workflow (new case
   `v4/cli/cases/workflow.publisher.json`, two actors so both mixed
   directions execute it): composes the full update-ipsets sequence
   through JSON-RPC and filesystem actions — (1) `current.publish`
   from a text fixture plus consumer open/lookup of the published
   file; (2) `retention.first_seen.refresh` and
   `retention.last_seen.refresh` plus consumer lookup of refreshed
   content; (3) `feeds.replace` twice with one deliberate failure in
   between, proving per-feed failure isolation and prior-feed
   preservation via consumer reader lookups (one failed feed does not
   roll back unrelated feeds already committed); (4) `history.project`
   of at least two window cutoffs from one last-seen scan plus
   consumer ranges read-back of both windows; (5) one-scan
   aggregation `query.overlaps`, both joins (`join.direct`,
   `join.membership`) and `algebra.publish` over the same source with
   cross-method counter/cardinality equality and consumer open of the
   published algebra output; (6) `snapshot` + `publication.resolve`,
   `validate`, `recovery.inspect`, `maintenance.list`,
   `database.reclaim`, and residue cleanup. Superseded detail: the
   plan's "maintenance.list with real entries" became a zero-residue
   assertion in the delivered case (all publish/journal artifacts are
   cleaned up by the preceding steps); real retained reservation
   entries are proved by the W5 crash harness instead.
4. W4 mixed-live coordination (new case
   `v4/cli/cases/mixed.live-coordination.json`, two actors): producer
   creates a live DB; consumer opens a live reader pinned on the
   committed generation; producer commits two updates while the
   reader is pinned; the pinned reader still reads the pinned
   generation (no partial replacement); the reader closes; producer
   `database.reclaim` then proves the retired generations become
   reclamation-eligible. Runs in both mixed directions (Rust writer +
   Go reader, Go writer + Rust reader). Superseded detail: the plan's
   "producer opens a second live reader" is not expressible when both
   readers are opened through one capture slot per result path (the
   runner keeps one handle per path); the delivered case pins one
   cross-binary reader and exercises the second live slot through the
   producer's transient `database.info` open, as recorded in the
   implementation wave.
5. W5 crash harness (new external script `v4/cli/crash_harness.py`,
   README anchor): a separate gate script that drives the normal
   JSON-RPC frame client (reuses `run.py`'s `JsonRpcService` and
   fixture building; adds no production method or hook). It sends a
   producer workflow request, waits for the engine's durable
   side-effect marker for that operation (publication reservation
   file for `current.publish`; live sidecar/transition state for live
   commits), kills the producer process group mid-operation, then on
   a fresh producer process resolves the outcome with the documented
   resolvers and asserts: no partial replacement (a consumer reads
   the previous content), no false success (resolution reports the
   truthful outcome), bounded residue (`maintenance.list` /
   `publication.inspect` bounded), and reopen succeeds. Proven for
   `current.publish` and one live commit operation, both directions
   (producer Rust with consumer Go; producer Go with consumer Rust),
   with the exact internal crash-point inventory left to the SDK
   crash gates already recorded.
6. W6 close-out: full matrix and gate battery under `nice`, five
   reviewers in their scopes, an independent whole-milestone review,
   SOW records (including the mechanical ledger counts and the
   updated mixed-matrix counts), one lifecycle commit, push.

Validation plan and expected cost (recorded before running): the
full gate battery is Go+Rust product builds (~1-3 wall-minutes under
`nice`), four matrices (rust, go, rust_to_go, go_to_rust) on the
extended corpus (~2-5 wall-minutes each with the new two-actor
cases), golden 53 + sensitivity 14 + tests.d + mmap trace (~1-2
wall-minutes), and the crash harness (~1-2 wall-minutes). Total
worst case under `nice` ~15-25 wall-minutes with bounded memory; each
wave runs only its own validation until green, and the full battery
runs once per gate.

### 2026-09-04 — milestone 4 (delivery step 5) implementation wave

Implemented in five parallel waves, each validated under `nice`
before the next started (commits 31552397, 64f81e74, 9674b96c,
f0e242ad):

- W1 runner evidence (v4/cli/run.py, v4/cli/README.md): after every
  executed rpc or legacy step the runner inventories the work
  directory and classifies each file against the spec kind table
  (`v4_main`, `live_sidecar` `<main>.readers`, `publication_reservation`
  `.iprange-reservation-*.tmp`, `authorized_scratch`
  `.iprange-scratch-*.tmp`, `adapter_output`, `metadata_delivery`,
  `unknown`; transients that vanish inside a step never count). The
  report gains two additive fields: the mechanical `file_kinds`
  ledger (kind -> methods that created it / opened it, aggregated
  over executed steps only) and `frame_sizes` (per-method max
  request/response wire bytes measured by the JSON-RPC client). The
  ledger is byte-identical between the Rust and Go matrices and has
  zero `unknown` files. Every PASS case entry also carries its
  per-case `file_kinds` lineage (relative artifact path -> kind and
  the acting "actor.method" created_by/opened_by lists), so the
  producer/consumer identity survives the root aggregation. The
  kind-universe completeness gate (`v4/cli/check_kind_coverage.py`)
  reads every matrix report and the crash report and fails the
  evidence battery when any required artifact kind (`v4_main`,
  `live_sidecar`, `publication_reservation`, `authorized_scratch`,
  `adapter_output`, `metadata_delivery`) was never observed.
- W2 resource proof (v4/cli/cases/resource.limits.json,
  v4/cli/resource-record.md): a 134-step consumer case proving at the
  product boundary, in both languages: the complete 9-member
  `system.describe` limits report (1 MiB frames, 65,000-byte response
  object, batches/queued 16, reader/cursor 64, lookup 4,096, cursor
  records 4,096); `output_limit`/`read_only_failure` for a legal
  4,096-address lookup whose inline result cannot fit 65,000 bytes;
  reader capacity exhaustion at the 65th open and cursor capacity
  exhaustion at the 65th cursor (both return
  `server_busy`/`not_started`, verified identical in Rust and Go).
  A 17-element batch is rejected by both products at the frame layer
  with transport -32600 before queue admission, so the queued
  >16-in-flight `server_busy` race is not assertable through the
  serial runner; the record documents that as step-6 territory.
- W3 six-step publisher workflow (v4/cli/cases/workflow.publisher.json,
  53 steps, both actors; passes go, rust, rust_to_go, go_to_rust):
  (1) current feed -> immutable published file with consumer
  read-back; (2) first-seen and last-seen refresh with consumer
  lookup of the refreshed values; (3) named-feed replacement with
  per-feed failure isolation - a deliberate failed `feeds.replace`
  (missing source path, `invalid_path`/`not_started` in both
  binaries) commits nothing; feed A keeps its NEW committed content
  and feed B keeps its OLD content (verified by consumer lookups and
  by the unchanged transaction id); (4) two history windows with
  distinct cutoffs projected from one last-seen scan and read back
  through both binaries; (5) one-scan aggregation
  (`query.overlaps`), both joins (`join.direct` against the
  producer-built live DB, `join.membership`), and `algebra.publish`
  over the workflow-built live feed DB (the named-feed database of
  step 3), with the same range/address counters asserted identically
  across methods (3 ranges / 31 addresses in overlaps, join left
  side, join membership side and algebra source), plus consumer open
  of the published algebra output; the downstream steps consume the
  artifacts this workflow produced (no fixture substitution for the
  aggregation/algebra surface); (6) live snapshot + resolve +
  consumer read-back,
  `validate` (0 findings), `recovery.inspect`, `database.reclaim`
  and `maintenance.list` (0 real entries at that point).
- W4 mixed-live coordination (v4/cli/cases/mixed.live-coordination.json,
  10 steps, both actors; passes all four matrices): a live direct DB
  (created at generation 1) has its reader pinned by the CONSUMER
  binary; the PRODUCER binary commits two replacements
  (generations 2 and 3) while the reader stays pinned; the pinned
  reader still reads generation 1 (zero ranges, no partial
  replacement) while the producer's fresh `database.info` view shows
  generation 3; the reader closes with full live `source_close`
  facts; `database.reclaim` then proves the retired generations
  become reclamation-eligible. rust_to_go proves a Go pinned reader
  under a Rust writer; go_to_rust proves a Rust pinned reader under
  a Go writer. Empirical constraints recorded: database.create
  already produces a live DB (born-live, `.readers` sidecar present;
  `initialize_live` on it is `wrong_state`) and the runner carries
  one capture slot per result path, so two simultaneously held
  reader handles cannot both be closed by case steps; the case uses
  reader_capacity 2 and exercises the second slot via
  `database.info`'s transient open during pinning.
- W5 crash harness (v4/cli/crash_harness.py, report schema
  iprange-cli-crash-report-v1): an external gate script that drives
  the normal JSON-RPC client (reuses run.JsonRpcService and fixture
  building; adds no production method or hook). It SIGKILLs the
  producer process group at a durable engine marker mid
  `current.publish` (reservation file with valid IPR4RSV1 state-1
  block) and mid `database.initialize_live` (new `<main>.readers`
  sidecar on an immutable fixture input), then on a fresh producer
  resolves truthfully: `publication.resolve` completes the
  interrupted publication with the reservation as sole authority
  (`publication: "published"`, destination content desired,
  destination SHA-512 equals the attempt output SHA-512); live
  transition residue resolves with `database.live_residue.resolve`
  (`status: "completed"`, `kind: "canonical"`,
  `residue_possible: false`); pre-resolution residue bounded
  (reservation=1, publication_temp=1; post-resolution 0/0/0);
  consumer reopens succeed after resolution (no half-published file
  ever opens; before resolution the destination is truthfully
  not_started). The review wave (recorded below) added scenario A3
  (foreign-destination negative control: after the kill the
  destination is poisoned with a valid-but-unrelated v4 fixture and
  must classify `foreign` against the reservation digest) and
  scenario C (recover killed at the authorized-scratch marker: the
  durable marker is the CRC-valid 128-byte ownership header, the
  fresh producer lists and removes the abandoned scratch by attempt
  ID, and the never-published recovery destination stays closed).
  The final battery passes 10/10 scenarios in both directions; the
  negative control (consumer=/bin/false) fails 10/10; zero leftover
  processes. Exact internal crash-point coverage remains with the
  SDK crash gates as the SOW states.

Gate battery at HEAD f0e242ad (all under `nice`; evidence in
v4/cli/evidence/): rust 37/37 (oracle 37), go 37/37 (oracle 37),
rust_to_go and go_to_rust 13 executed / 24 skipped (oracle 22) per
direction, 0 failed; golden 53; sensitivity 14; go plain + v4work
suites PASS; tests.d 100/100 with the Go product; go mmap trace PASS;
rust mmap storage (343 sources) and runtime PASS; crash harness 6/6.
The earlier mixed counts (10 executed / 23 skipped, then 11 / 23)
are superseded by the current corpus: 37 case files, 13 both-actor.
Product source identity AT THIS REVISION: v4/go diff empty since
d956d8f2 and the `-buildvcs=false` qualification builds reproduced
the milestone-3 close-out hashes byte-for-byte (product 4f8fb7b8...,
worker 16236608...); the Rust product binary is unchanged. The
reviewer fix wave (recorded below) later changed one Go handler, so
the final product identity supersedes this paragraph.

### 2026-09-04 — milestone 4 reviewer rounds and fixes

Round 1 (five reviewers, exact tree f0e242ad) found two P1s plus
P2/P3s; the disposition fix wave is commits 58a772b4 and 3408c64c
plus the final label-derivation fix in this wave.

- Gauss (Rust parity) round 1 FAIL: P1 — Go `recover` error details
  omitted the `scratch`/`output` members when absent while Rust
  always emits them as null (Rust recovery.rs:263-271 and 310-317
  vs Go recovery.go:893-902 and 940-946). Fix commit 3408c64c: Go
  emits the full member set via recoveryScratchValueOrNil and
  privateOutputAttemptValueOrNil; live probes on both binaries
  confirm identical 7-member details ("cleanup",
  "coordination_cleanup", "housekeeping", "output", "report",
  "scratch", "visible_housekeeping") with "scratch": null for both
  failure paths. Regression guard: the case schema and the runner
  now enforce an exact `expect_error.details` member set (cases.py
  "details" property; run.py check_expected_error set-equality),
  and validate.recover.json pins the 7-member set with
  "scratch": null and "output" present. Rounds 2 and 3: PASS.
- Avicenna (Go idioms / Python harness) round 1 PASS with three
  P3s, all fixed: the dormant runner `_self_test` is now wired into
  main() next to oracle._self_test(); KillableJsonRpcService
  delegates to the shared JsonRpcService through a new
  `start_new_session` kwarg (one spawn-wiring authority); failed-
  scenario work bases are registered up front so failures are
  cleaned up too. Rounds 2 and 3: PASS.
- Aristotle (performance) round 1 PASS; measured the final battery:
  4-matrix ~3.9 s, crash positive ~12-13 s, negative ~8 s, total
  step-5 gate ~33 s under `nice`; ledger inventory, frame tracking
  and member-set checks are O(small) with no measured impact.
  Rounds 2 and 3: PASS.
- Gibbs (wire integrity) round 1 PASS with six P3 observations;
  dispositions: windows_housekeeping is recorded as probe-only and
  moved to NOT PROVEN in resource-record.md; the -32001 over-limit
  close path and the >16-in-flight queue race are recorded as NOT
  PROVEN (step-6 territory); commit_nonce stays `$ignore` (random
  per creation, equality property verified); frame_sizes are
  documented as run-specific maxima. Rounds 2 and 3: PASS; round-3
  P3 documented: the harness targets the rust/go product pair, and
  a same-implementation pair (two Go products) would collide work
  bases and fail loudly (out of contract, no false-positive risk).
- Locke (API/docs/records) round 1 FAIL: P1 — the crash harness
  never swapped binaries; the per-iteration tuples closed over the
  fixed argparse values, so the committed crash.json "go->rust"
  entries were mislabeled rust->go re-runs. Fix in commit
  58a772b4 (swapped binaries per iteration) and in this wave
  (direction labels are now derived from each binary's
  system.describe `implementation` probe, with a probe-failure
  fallback label for the /bin/false negative control; any argv
  order yields truthful labels). Evidence regenerated at the final
  revision: crash.json 6/6 in both real directions with zero
  leftover processes; crash-negative.json 6/6 fail with honest
  fallback labels. P2s: README case count corrected 38->37; the
  evidence README identity updated to the fix-wave Go product
  (below); this review-record entry closes the missing fix-wave
  record. P3s: resource-record.md lists the maintenance.remove
  real-nonce and -32001 items as NOT PROVEN; the negative-control
  wording corrected; plan wording neutralized; the capability
  probe now runs under the scenario deadline with a widened
  exception set so a silent-but-alive broken binary cannot hang
  the harness. Rounds 2 and 3: PASS.

Final product identity after the fix wave (supersedes the
pre-review paragraph): v4/go changed at 3408c64c (recover
error-details parity); qualification build `nice go -C v4/go build
-buildvcs=false -o ... ./cmd/iprange` (go1.26.4 linux/amd64):
product SHA-256
1612646fdbfc54e4c9fe99378806dcc271a2f852c634d4f149d6220bf63b07b9,
worker unchanged
16236608325cb189e0fbe05603886bbe150fd1ae83e4a8b532bfb7dd07054b1e.
The Rust product binary is unchanged. Gate battery at the
final fix revision (all under `nice`; evidence in v4/cli/evidence/):
rust 37/37 (oracle 37), go 37/37 (oracle 37), rust_to_go and
go_to_rust 13 executed / 24 skipped (oracle 22) per direction,
0 failed; golden 53; sensitivity 14; go plain + v4work suites PASS;
tests.d 100/100; go mmap trace PASS; rust mmap storage (343
sources) and runtime PASS; crash harness 6/6 in both real
directions with zero leftover processes; negative control 6/6 fail.

### 2026-09-04 — milestone 4 close (delivery step 5 complete) [superseded by the reopen record below]

Superseded annotation: the external gap review recorded below
reopened this close on 2026-09-04; the fix wave and the re-close
record supersede every "final" or "complete" claim of this section.

- Final functional and evidence revision: 37721d8b (product source:
  v4/go at 3408c64c, v4/rust unchanged). Final record-only HEAD for
  this milestone: the commit carrying this entry (a record commit
  cannot name its own SHA; the commit identity is the durable
  record). Tree clean; all milestone-4 evidence committed.
- Whole-milestone review chain at the exact final tree: five-scope
  adversarial review rounds 1-3 (Rust parity, Go idioms/runner
  quality, performance, wire integrity, API/docs/records) all PASS
  at 37721d8b after the round-1 P1 fix wave; glm-5.3-responses
  whole-milestone review PASS at 37721d8b with no remaining
  findings (four P3 hardening items fixed in 37721d8b itself).
- Outcome: milestone 4 (delivery step 5) is complete. Qualified at
  the product interface in both language directions: the complete
  six-step update-ipsets publisher workflow (current feed publish,
  first/last-seen refresh, serialized named-feed replacement with
  per-feed failure isolation, multi-window history projection,
  one-scan aggregation with both joins and algebra publication,
  snapshot/validate/recover/cleanup) composed only through JSON-RPC
  and filesystem actions; mixed live coordination (a live reader in
  one binary pinned on its generation while the other binary
  commits, generation advance under pin, close facts, reclamation
  after close); the mechanical file-kind ledger (derived from
  executed steps, identical across languages, zero unknown); the
  resource proof (documented ceilings, output_limit, 64+1 reader
  and cursor capacity, limits report equality); and the process
  level crash harness (SIGKILL at durable engine markers for
  current.publish and database.initialize_live, truthful
  resolution, bounded residue, clean reopen, zero leftover
  processes, negative control fails 6/6). The recover error-details
  member-set parity defect found by the review (Go omitting
  scratch/output when absent) was fixed and pinned by a corpus
  assertion.
- Gate battery at the final revision (all under `nice`; evidence in
  v4/cli/evidence/): rust 37/37 (oracle 37), go 37/37 (oracle 37),
  rust_to_go and go_to_rust 13 executed / 24 skipped (oracle 22)
  per direction, 0 failed; golden 53; sensitivity 14; go plain +
  v4work suites PASS; vet + gofmt clean; tests.d 100/100 with the
  Go product; go mmap trace PASS; rust mmap storage (343 sources)
  and runtime PASS; crash harness 6/6 plus 6/6 negative; measured
  step-5 gate cost ~33 wall-seconds.
- Product identity: Go qualification build (-buildvcs=false,
  go1.26.4 linux/amd64) product
  1612646fdbfc54e4c9fe99378806dcc271a2f852c634d4f149d6220bf63b07b9
  at product source 3408c64c; worker
  16236608325cb189e0fbe05603886bbe150fd1ae83e4a8b532bfb7dd07054b1e;
  Rust product c13866378040e0524711b8c92b4acdb6ca7f89b3f4db375fd5419bccd7b71eb8
  (unchanged source); fixture tool d615488f038fa59deea87e0ce3340b780380fe0f2122e8e1ad65edeb25d861f1.
- Follow-up: delivery step 6 (consolidated benchmark harness and
  measured ceilings; the remaining NOT-PROVEN step-5 items:
  >16-in-flight server_busy race, -32001 over-limit close path,
  reservation-nonce maintenance.remove, windows_housekeeping kind;
  scratch-nonce maintenance.remove is proven by crash scenario C)
  is the next milestone in this SOW; SOW-0030 remains the engine
  performance tracker.

### 2026-09-04 — milestone 4 reopened by the external gap review; framework fix waves and re-close

An external adversarial review of the milestone-4 close (revision
c10cad04) returned FAIL with six verified findings.  Each finding was
confirmed against the committed evidence before the fix wave; none
was accepted on authority.

1. P1 — Successful recovery is not tested.  The delivered workflow
   stopped at `recovery.inspect`, and the only `recover` invocation
   used fabricated identities and expected the
   `recovery_candidate_changed` failure (validate.recover.json);
   the SOW still claimed successful recovery was qualified.
   Dispose: new case `v4/cli/cases/recover.successful.json` (both
   actors, passes all four matrices) validates a truncated final-page
   v4 file (`valid=false`, findings 2, one unbounded unknown
   subgraph), captures the exact inspect candidate through the new
   aliased capture syntax (`{"name": "candidate", "path":
   "candidates[0]"}`), recovers with the captured candidate
   (publication "published"; deterministic recovery report:
   catalog 4 accepted, ranges 4, membership 3, 41 verified
   addresses, 1 io-unreadable page), and the consumer reopens the
   recovered file and reads back the preserved membership content
   (feeds "alpha" on 192.0.2.0 and 198.51.100.35).
2. P1 — The mixed-live reclamation contract was weakened.  The
   delivered case pinned one consumer reader and reclaimed only
   after its close; the SOW recorded the reduction (one capture slot
   per result path) as an implementation constraint, which is not a
   product decision.  Dispose: the runner and case schema now accept
   aliased captures (items may be `{"name", "path"}`; pointers
   support `[index]` list steps).  mixed.live-coordination.json now
   opens a consumer AND a producer reader on generation 1,
   `database.reclaim` returns `no_change` while both are pinned
   (before and after two producer commits to generations 2 and 3),
   both pinned readers still observe generation 1, closing the
   consumer reader alone does not enable reclamation (the producer
   reader still pins), and only after the last reader closes does
   reclamation commit (transaction 4, 1 page).  Verified
   independently on both product binaries before the case was
   written.
3. P1 — The file-kind ledger cannot prove its acceptance claim.
   Case ledgers were merged into a root aggregate (losing case,
   actor, path, and producer/consumer lineage), transient files were
   invisible by design, no completeness gate compared observed kinds
   against the required universe, and the mixed evidence never
   observed reservations or authorized scratch.  Dispose: every PASS
   case entry now carries its per-case `file_kinds` lineage
   (relative path -> kind, `actor.method` created_by/opened_by
   lists); the new `v4/cli/check_kind_coverage.py` gate reads all
   matrix reports and the crash report and fails the battery when
   any required kind (`v4_main`, `live_sidecar`,
   `publication_reservation`, `authorized_scratch`,
   `adapter_output`, `metadata_delivery`) has zero observed
   evidence; retained reservations (crash A1/A2/A3) and abandoned
   scratch (crash C) now contribute per-scenario kind evidence.
4. P1 — Crash scenario A2 accepted any foreign file as the completed
   attempt.  `classify_destination` treated every existing
   non-prior file as `attempt_complete` without comparing the
   reservation digest; an adversarial probe with arbitrary foreign
   bytes reproduced the defect (the committed A2 evidence had
   coincidentally observed `prior_complete`).  Dispose:
   `classify_destination` now takes the reservation-recorded output
   SHA-512 and returns `foreign` for any non-prior digest mismatch;
   the A2 call site was also fixed to read the reservation from the
   per-scenario work directory (it previously passed the shared
   base directory, so the digest was never actually compared); new
   scenario A3 poisons the crash-left destination with a
   valid-but-unrelated v4 fixture and requires the `foreign`
   classification as a negative control.
5. P2 — The "complete workflow" was partly disconnected.  The
   aggregation, membership join, and algebra steps consumed a
   fixture-created membership file instead of the membership
   database the workflow itself built.  Dispose: steps 37-40 of
   workflow.publisher.json now consume the workflow-built live feed
   DB (`feeddb.iprange`); every exported counter (overlaps 3 ranges
   / 31 addresses / 1 pair; join.direct 31 mapped / 0 unmapped;
   join.membership 3 left ranges / 11 overlap; algebra union 2
   output ranges / 31 addresses) was re-derived from the connected
   workflow and asserted identically in Go and Rust; the
   aggregation/algebra surface no longer uses any fixture.
6. P2 — The resource qualification was explicitly incomplete while
   the close record claimed the resource proof complete.
   Dispose: the resource record keeps its four NOT-PROVEN items
   explicit (queued >16-in-flight `server_busy` race, -32001
   over-limit close path, real-reservation-nonce
   `maintenance.remove`, `windows_housekeeping` kind) and the
   milestone-4 close record below does not claim them; the C
   scenario additionally proves `maintenance.remove` against a real
   abandoned-scratch attempt ID (list -> remove -> durable absent),
   which moves the scratch half of the maintenance-removal contract
   from NOT PROVEN to proven; the reservation-nonce half remains
   step-6 territory.

Supporting framework fixes in the same wave:

- The base64 fixture branch tolerates binary blobs (a damaged v4
  fixture is embedded in the recovery case; it was previously
  decoded as UTF-8 text unconditionally).
- The crash harness waits for the scratch durability marker: a
  scratch file counts as durable only with its complete 128-byte
  ownership header, whose CRC-32C (computed over the whole header
  with the CRC field zeroed, standard reflected Castagnoli check
  value 0xe3069283) validates; a kill at a partial header would
  leave an unremovable lookalike, which is not the tested contract.
- Capture names must be unique within one step; aliases make
  multi-handle cases expressible.

Second fix wave of the same reopen (glm-5.3-responses gap review at
f04a3dc9; findings verified before implementation):

1. P2 — The re-close record was anticipatory: the Status section and
   the reopen record claimed the five-scope review and the
   glm-5.3-responses whole-milestone review PASS "recorded below"
   although no review records existed after the reopen.  Dispose:
   the records now state milestone 4 remains in progress until both
   reviews pass at the exact final tree; the real verdicts will be
   appended only after they exist.
2. P2 — The kind-universe gate accepted failed evidence.  The gate
   consumed the report-root aggregate although the runner merges
   partial ledgers even for FAIL cases, and it consumed crash
   scenario kinds without checking `"pass"`.  An all-failed doctored
   battery passed the gate.  Dispose:
   `v4/cli/check_kind_coverage.py` now consumes PASS-case per-case
   lineage only, rejects matrix reports with `failed != 0` and crash
   reports with `failed != 0` or leftover product processes, and
   counts only crash scenarios whose `"pass"` is true; the crash
   report schema now carries the `failed` count; the gate ships a
   committed doctored-report self-test (all-failed matrix, all-failed
   crash, leftover processes, root-aggregate-only, and green
   PASS-lineage cases) that runs before its CLI.
3. P2 — The mixed-live record overclaimed "both pinned readers still
   observe generation 1": only the consumer reader was queried after
   the two commits; the producer reader was closed without any
   post-commit read.  Dispose:
   `v4/cli/cases/mixed.live-coordination.json` now reads the pinned
   producer reader after the commits (lookup with the same four
   addresses plus a direct-range scan, both expecting the empty
   generation-1 view) before any reader closes; the case passes all
   four matrices.
4. P3 — Capture-pointer validation was not anchored:
   `candidates[0]]` passed schema validation and resolved as
   `candidates[0]`.  Dispose: `schema/cases.py` now defines one
   anchored pointer grammar (`member` chains with optional `[index]`
   steps) shared by case validation and the runner resolver, with
   `_self_test()` covering the accepted and rejected pointer sets;
   run.py now runs the case-schema self-test before every matrix, so
   the negative pointer cases are wired into the gate, not dormant.

Fix-wave evidence at this revision (all under `nice`; evidence
regenerated in v4/cli/evidence/):

- Matrices: rust 38/38 (oracle 37), go 38/38 (oracle 37),
  rust_to_go and go_to_rust 14 executed / 24 skipped (oracle 22)
  per direction, 0 failed.  The corpus is 38 case files: the
  milestone-3 surface plus resource.limits, workflow.publisher
  (connected), mixed.live-coordination (dual pinned readers), and
  recover.successful.
- Crash harness: 10/10 scenarios pass in both directions (A1, A2,
  A3, B, C; rust-to-go and go-to-rust), zero leftover processes;
  the negative control (consumer=/bin/false) fails 10/10 with no
  false pass.  crash.json carries the per-scenario kind evidence;
  the negative-control scenarios record empty kind lists because
  they fail before the artifact inventory runs.
- Kind-universe gate (`nice python3 v4/cli/check_kind_coverage.py
  --matrix ... --crash ...`): PASS; every required kind has
  observed evidence (v4_main, live_sidecar,
  publication_reservation, authorized_scratch, adapter_output,
  metadata_delivery).
- Golden corpus 53 PASS (38 case files validated by the anchored
  schema); sensitivity gate 14 modes PASS; every schema module
  self-test PASS (`schema/common,engine,frame,methods,oracle,results,
  cases` via normal import; `run.py` also runs its own and the
  oracle self-tests before every matrix); the kind-coverage gate
  self-test PASS; the runner self-test PASS (exercised by the four
  matrix runs above).  Data-only steps under
  `nice`: four matrices (~2.7 s wall for rust+go together), crash
  battery both directions + negative control (~35 s), golden (~2 s),
  sensitivity (<1 s); well inside the resource budget.
- Product identity: unchanged by this wave (the fixes are external
  framework, cases, and records; no product source changed).
- Outcome: the re-close records below are superseded by the second
  external gap review (third fix wave, also below): milestone 4
  (delivery step 5) is in progress again pending the three numbered
  user decisions at the end of this section.

#### Final review records (from the second fix wave; superseded by the third wave below)

- Delta review (own-model reviewer Avicenna, external-qualification
  harness/gate/record scope) at b4f882ca: PASS with one P3 (wired
  into the runner at d6c9990b); glm-5.3-responses whole-milestone
  re-review at d6c9990b: PASS with no findings (full battery re-run
  from fresh qualification builds).  Both verdicts were superseded
  when the second external gap review invalidated the re-close
  (per project-final-review skill: any later commit invalidates the
  verdict); the review records remain as historical evidence of the
  second fix wave only.

### 2026-09-04 (continued) — third fix wave (second external gap review; four fixed, three user decisions pending)

The second external gap review returned five verified findings at
345e4565.  Verification and dispositions:

1. P1 — The kind-universe gate under-enforced the recorded
   cross-language file-kind contract.  The gate read only
   `created_by`, accepted any subset of reports, required one global
   occurrence per kind, accepted unknown kinds, and omitted
   `publication_temp` (a production maintenance kind per
   `iprange-jsonrpc-v1.md`).  Reproducers confirmed: the Rust matrix
   plus crash alone passed, and injecting `kind: "unknown"` into
   every PASS case passed.  Dispose (implemented in this wave):
   `v4/cli/check_kind_coverage.py` now requires all four matrix
   reports (each report carries a top-level `matrix` identity) and a
   positive crash report whose PASS scenarios span both language
   directions; every required kind must be created by both product
   languages and, whenever any service opens the kind, opened by
   both languages too (crash scenarios attribute creation to the
   producer language and consumption to the consumer language;
   outbound-only kinds — adapter outputs and metadata deliveries —
   are never opened by any service); PASS evidence containing any
   kind outside the required universe, which now includes
   `publication_temp` (also classified by the runner ledger), fails
   the gate; the doctored-report self-test covers missing matrix,
   missing matrix identity, unknown kind, one-language creation,
   one-language opens, single-direction crash, all-failed, and
   leftover-process cases.  Committed evidence passes the new gate.
2. P2 — The recorded crash scope enumerates interruptions during
   publish/commit/finish/export/validate; the harness currently
   proves publish (`current.publish`), live-transition
   (`database.initialize_live`), and recovery (`recover`).  Fixing
   this requires either three new product-interface interruption
   scenarios (commit/finish combined, export, validate) or an
   explicit user re-scope; recorded as decision D1 below.
3. P2 — Moving the four NOT-PROVEN resource items to delivery step 6
   was never approved by the user.  Recorded as decision D2 below;
   until decided, the milestone-5 start section must not claim them.
4. P2 — Recovery composition: `workflow.publisher` performs
   `recovery.inspect` only)Skip, and `recover.successful` did not
   validate the recovered output.  Dispose (implemented): a new
   final step in `recover.successful` validates the recovered file
   with the consumer binary (`valid=true`, zero findings, exact
   generation and progress facts pinned).  Whether the publisher
   workflow must contain a full damaged-file recovery cycle is
   recorded as decision D3 below.
5. P2 — Review identity and privacy.  The re-close reviewed
   d6c9990b while the record commit 345e4565 followed (invalidated
   per the project-final-review skill), and the committed evidence
   carried the user's personal home path in every argv record
   (contradicting the SOW's sensitive-data gate).  Dispose
   (implemented): the re-close records above are marked superseded;
   this wave commits everything before any final review, and the
   final reviews will run at the exact final tree with no later
   commits; the evidence was regenerated from binary copies staged
   under `/tmp/qualsvc/` (version-matched product/worker pairs
   recorded in `v4/cli/evidence/README.md`), so the committed
   reports contain no personal paths (verified: zero matches for
   the home directory in all six evidence files).

Third-wave evidence at this revision (all under `nice`, binaries
staged at `/tmp/qualsvc/`): rust/go matrices 38/38 (oracle 37),
mixed 14 executed / 24 skipped per direction (oracle 22), crash
10/10 both directions plus the `/bin/false` negative control failing
10/10, kind gate PASS under the strengthened contract, golden 53,
sensitivity 14, all schema module self-tests and the runner
self-test PASS (the latter exercised by the four matrix runs).
Product source unchanged; no personal paths in the committed
evidence.

#### User decisions (third fix wave) — resolved 2026-09-04

- D1 — Crash interruption scope.  The milestone-4 plan records
  product-interface interruption during publish/commit/finish/
  export/validate.  The harness proves publish, live-transition, and
  recovery interruption.  Options: A) implement three additional
  product-interface scenarios now (commit/finish at a
  durable sidecar marker, export at a partial-output marker, and
  validate at the worker scratch marker) — full recorded scope,
  more harness surface; B) approve the current set as the
  representative product-interface proof, with the SDK crash gates
  (writer commit, publication attempts/reservations, live
  lifecycle, worker client, in both languages) covering the exact
  internal crash points per the plan's own sentence — smaller,
  already-green wave.  Recommendation: A (matches the recorded
  plan; sol's finding is concrete).

  **Decision (user, 2026-09-04): A — implement all three additional
  product-interface scenarios (commit/finish, export, validate) in
  this wave.**  The user also authorized running tests with `nice`
  and required the scenarios to be deterministic at durable markers
  (never wall-clock assertions).
- D2 — The four resource NOT-PROVEN items (the >16-in-flight
  `server_busy` race, the -32001 over-limit close path,
  `maintenance.remove` against a real reservation nonce,
  `windows_housekeeping` kind).  Options: A) implement all four in
  this wave, including the Windows-kind proof on the Windows
  validation host (requires access authorization); B) implement the
  three Linux-provable items in this wave (a pipelining-client
  harness mode, a raw oversized-frame harness mode, and a
  reservation-nonce `maintenance.remove` crash scenario) and defer
  only `windows_housekeeping` to delivery step 6; C) approve the
  existing deferral of all four to delivery step 6.  Recommendation:
  B (mechanically provable items get proven; the Windows-kind item
  is platform-bound and needs the Windows host).

  **Decision (user, 2026-09-04): A — prove all four resource items
  now, including the Windows-kind proof on the authorized Windows validation host.**
  Access to the authorized Windows validation host is authorized restricted to SOW-0028 Windows
  qualification (compile the two product binaries at the qualified
  revision and run the housekeeping proof); the host is used only
  for that purpose.
- D3 — Publisher-workflow recovery composition.  Options: A) make
  the workflow perform a full damaged-file recovery cycle
  (deterministic truncation of its own snapshot via a new
  test-only filesystem action, then inspect/recover/validate/cleanup
  inside the workflow) — the strongest reading of step 6
  "validation, recovery, and cleanup", at the cost of a new runner
  action; B) keep `recovery.inspect` (no candidates) in the
  workflow — the truthful outcome for a healthy snapshot, since the
  `recover` RPC requires a candidate object — and rely on the now
  validate-closed `recover.successful` for the successful path.
  Recommendation: B (minimal-complete; recover.successful now
  validates the recovered output end to end and the workflow's
  snapshot is healthy by construction).

  **Decision (user, 2026-09-04): B — keep successful recovery as a
  separate damaged-file path.**  The healthy publisher workflow
  keeps snapshot → validate → inspect (truthfully finding no
  recovery candidate); the damaged-file path is inspect → recover →
  reopen/query → validate, covered by `recover.successful`.  This
  SOW wording distinguishes the two paths and `recover.successful`
  additionally asserts truthful cleanup facts (zero scratch,
  reservation, and publication_temp residue after a completed
  recovery).

  Plus two mandatory repairs from the same review, also approved:
  (1) the kind gate must derive language attribution from the
  executed actors' declared identities (per-case hashes and
  `system.describe` implementation), never from the top-level
  matrix label; (2) the personal home path in this SOW file must be
  removed (done in this wave).

### 2026-09-04 — milestone 5 scope recorded (delivery step 6: consolidated benchmark harness and measured ceilings)

- Step-6 scope: the consolidated benchmark harness announced in the
  plan (`v4/cli/benchmarks/` reserved) with workload manifests for
  the update-ipsets surface and measured Go-vs-Rust ceilings at the
  product interface.  The four resource items formerly deferred to
  step 6 were proven in the fourth fix wave of step 5 (below):
  the >16-in-flight `server_busy` race (pipelining-client proof),
  the -32001 over-limit close path (raw oversized frame),
  `maintenance.remove` against a real reservation nonce handle,
  and the `windows_housekeeping` kind (Windows-host proof; removal
  against real abandoned-scratch attempt IDs was already proven by
  crash scenario C).  Ceiling
  methodology and acceptance will be recorded in the step-6
  implementation plan before any implementation starts, following
  the SOW-0027 performance-gate lessons (matched, alternating,
  same-host samples; measured ceilings per the user's 1.3x CPU /
  peak-RSS acceptance contract).


### 2026-09-04 (continued) — fourth fix wave: D1-A crash scope, D2-A resource proofs, D3-B recovery wording, and kind-gate provenance

User decisions D1-A / D2-A / D3-B and the two mandatory repairs are
recorded above.  This wave implements them at the external-qualification
surface; the Rust product source is unchanged and the two in-scope Go
adapter fixes (export-writer and removal-output temporary cleanup on
Windows) are recorded below.

1. **Crash scenarios D, E, F** (`v4/cli/crash_harness.py`): the
   recorded crash scope names publish/commit/finish/export/validate.
   A1-A3 cover publish, B covers the live transition, C covers
   recovery; the wave adds:
   - D — commit/finish: `direct.replace` on a live database killed
     at the durable main-growth marker.  A fresh process truthfully
     reports the pre-crash committed transaction in `database.info`
     live (the interrupted write never became a generation), the
     immutable mode truthfully refuses, a consumer live reader
     observes the pre-crash transaction, a fresh replace commits the
     next transaction, and no scratch/reservation/publication_temp
     maintenance residue exists.
   - E — export: export of a 500,000-range immutable database killed
     at the partial-output `.export.tmp` marker.  The destination is
     absent after the kill, the export temp is the bounded residue,
     a fresh export to the same destination completes and matches
     the pre-crash reference bytes, and the orphan never blocks new
     exports.
   - F — validate: validation of a byte-damaged database killed at
     the findings-output `.export.tmp` marker.  The findings output
     stays absent, the main file is byte-identical, a fresh
     validation reports the same `valid=false` finding set, and a
     consumer reader still opens the damaged main truthfully.
   Every scenario runs in both language directions, uses only
   durable markers (no wall-clock assertions), and reports the
   observed marker latency as evidence.
2. **Resource proofs** (`v4/cli/resource_harness.py`): three
   product-interface proofs run for both binaries — (a) one slow
   export plus 19 pipelined `system.describe` frames on one stdin
   blob: exactly three answers carry transport error -32002
   (`server_busy`) and the remaining seventeen succeed, proving the
   >16-in-flight queue bound; (b) one >1 MiB frame: exactly one
   -32001 response with a null id, then EOF, then a clean exit,
   proving the over-limit close path; (c) `maintenance.remove`
   against the real reservation nonce of a publish killed at the
   reservation marker: the list row built from the raw reservation
   (policy, phase, output and previous identities and digests)
   removes the reservation durably.
3. **`windows_housekeeping` proof** (`v4/cli/windows_housekeeping_harness.py`):
   on the authorized Windows validation host (access authorized
   by the user for SOW-0028 qualification only), both product
   binaries are built at the qualified revision and the script
   proves `maintenance.list` kind `windows_housekeeping` succeeds on
   Windows, reports `entries 0` on an empty directory and lists a
   synthesized canonical GC-envelope candidate
   (`.iprange-gcauth-<attempt>-<ordinal>.tmp`) with its authenticated
   directory identity; on non-Windows platforms the same script
   records the truthful `os_unsupported`/`read_only_failure`
   negative.
4. **D3-B records and `recover.successful`**: the healthy publisher
   workflow keeps snapshot → validate → inspect (no candidate,
   truthful for a healthy snapshot); the damaged-file path is
   inspect → recover → reopen/query → validate.  `recover.successful`
   gains a final `maintenance.list` step proving zero
   scratch/reservation/publication_temp residue after a completed
   recovery (truthful cleanup facts).
5. **Kind-gate provenance** (`v4/cli/run.py`,
   `v4/cli/check_kind_coverage.py`): every PASS matrix case now
   records per-actor `sha256` and `implementation`
   (`system.describe` result) regardless of matrix kind; the gate
   derives language attribution exclusively from those executed
   identities and fails a report whose top-level matrix label
   contradicts the executed actor pair, a PASS case without actors,
   or an implementation outside rust/go.  The doctored-report
   self-test covers the clone-and-relabel attack (a `rust` report
   relabeled `go` must fail), missing actors, and unknown
   implementations.
6. **Privacy**: the personal home path in this SOW file is removed
   (line 6 now names the swarm-rules file without an absolute home
   path), and all regenerated evidence stays free of personal paths
   (verified: zero matches for the home directory).

Validation results for this wave (all under `nice`, Linux binaries
staged at `/tmp/qualsvc/`, evidence in `v4/cli/evidence/`):

- Matrices: rust 38/38 (oracle 37), go 38/38 (oracle 37), rust_to_go
  14 executed / 24 skipped (oracle 22), go_to_rust 14 / 24 (oracle
  22); every PASS case records the executed-actor SHA-256 and
  `system.describe` implementation, and the kind gate passes under
  the provenance rule.
- Crash battery: 16/16 PASS (A1/A2/A3/B/C/D/E/F x rust->go and
  go->rust), zero leftover processes; negative control 0/16 PASS
  (16 FAIL, exit 1) exactly as designed.  Wall time ~3 min (the
  deterministic feed writes and the two 500,000-range export windows
  per direction dominate).
- Resource harness: 6/6 PASS (proofs a/b/c x both binaries; Rust
  answers exactly 3 pipelined `server_busy` refusals behind one slow
  export, Go serializes and answers all 20 — the Go negative is
  recorded truthfully, since Go never queues beyond the documented
  bound).
- Windows housekeeping: on the authorized Windows validation host (Microsoft Windows 11,
  AMD64) both Windows-built product binaries PASS the
  `windows_housekeeping` listing proof (0 entries on an empty
  directory, exactly one listed GC-envelope candidate with UTF-16LE
  basename encoding and authenticated directory identity on the
  candidate directory); on Linux both products truthfully refuse
  `os_unsupported`/`read_only_failure` (recorded by the same
  script).
- Gates: golden 53, sensitivity 14, all schema self-tests and the
  kind-gate doctored self-test PASS; committed evidence contains no
  personal paths.

Two product defects of the same class were found and fixed by the
Windows qualification (in scope: SOW-0028 adapter defects).  Go files
do not share DELETE, so removing a still-open temporary fails on
Windows; the affected writers now close their temporary before
publication and removal:
- `v4/go/internal/cli/fileio/export_writer.go` — the export/findings/
  maintenance-list output writer (publication, Abort, and Finish
  cleanup paths);
- `v4/go/internal/cli/handlers/live.go` — the first-seen refresh
  removal-output collector (discard on every abort path and the
  publish-failure cleanup).
The Linux Go qualification binary hash changed to `f9c7d50e…`
(worker unchanged), and the full Go suite passes in both build modes.
Rust product source is unchanged (`c1386637…`).

Recorded identities at this wave: Linux Go product
`f9c7d50e67475cae04a5793529d118ab76c5142f61384c977e0af56ee9030461`,
worker
`16236608325cb189e0fbe05603886bbe150fd1ae83e4a8b532bfb7dd07054b1e`,
Rust product
`c13866378040e0524711b8c92b4acdb6ca7f89b3f4db375fd5419bccd7b71eb8`,
fixture `d615488f038fa59deea87e0ce3340b780380fe0f2122e8e1ad65edeb25d861f1`;
Windows Go product
`20b9244d47154476cc5932c9cfc504c12b285cefb5eaea23e370858cbb3c686c`,
Windows Rust product
`5e91d9048f210958d78d935f403cfd41ac6ad587c5b8af8c22c0ba2d352524e8`.

### 2026-09-04 (continued) — fourth fix wave reviews and re-close

The five own-model scoped reviewers ran their full rounds at the
functional revisions of this wave and their incremental deltas at the
exact final functional revision `9b3af7d9` (working tree clean,
pushed).  First-round results: three PASS (wire-behavior, provenance, and
identity/privacy scopes) with four P3 findings, one P1 FAIL (the
second same-class Go temporary-remove defect in `live.go`, fixed in
the hardening commit), and one gate-integrity FAIL caused by the tree
moving under the reviewer (the committed fix wave landed mid-review).
The P1 and all P3s were fixed and re-verified; all five delta reviews
then PASSED at `9b3af7d9` with no new findings:

1. crash/wire-behavior scope: PASS — scenarios D/E/F and proofs a/b/c
   drive only the normal product interface with durable markers;
   `require_open` makes the E/F consumer reopen mandatory and F pins
   the fresh findings bytes to the reference bytes.
2. kind-gate provenance scope: PASS — attribution from executed
   actors only, label cross-check, and the actor-SHA anchoring in the
   report binaries block; all doctored classes fail the gate.
3. recovery wording and records scope: PASS — `recover.successful`
   step 7 is truthful and non-vacuous in all four matrices; SOW
   numbers and identities match the committed evidence.
4. Windows evidence and Go adapter-fix scope: PASS — both
   close-before-remove fixes verified with no write-after-close and
   no remaining remove-while-open site in the Go adapters; the Linux
   qualification build reproduces `f9c7d50e…`; the Windows report
   and the Linux negative control verified.
5. identity/privacy/hygiene scope: PASS — no personal paths, hashes
   coherent across records and staged binaries, diff-check clean,
   no premature closure claims.

The glm-5.3-responses whole-milestone review at the exact final
revision `9b3af7d9` returned **PASS** (no P1/P2): it reproduced the
exact tree identity, the qualification binary identities, all gates
(fresh crash battery 16/16 both directions, resource 6/6,
recover.successful 1/1 x four matrices, kind gate, golden 53,
sensitivity 14, schema self-tests, Go suite and vet in both build
modes, Rust source graph), verified the Windows evidence internal
consistency and the records, and confirmed no personal paths and no
premature closure claims.  Milestone 4 (delivery step 5) is re-closed
at `9b3af7d9`; the review records name exactly that revision, and
this SOW section is the record-only commit that follows it.

Non-blocking P3s recorded as tracked follow-ups (owned by the
remaining delivery step 6 milestone of this SOW; none affects the
committed evidence, which the whole-milestone review verified
directly):
- `v4/cli/resource_harness.py` proof b reads exactly one response
  and then waits for process exit without draining stdout; a future
  product regression emitting a second response would go unnoticed.
  Hardening: drain stdout to EOF with a bounded deadline and assert
  no further bytes.
- `v4/cli/windows_housekeeping_harness.py` records stronger facts
  than it enforces: require `basename_encoding == 2` on Windows,
  require row count == reported entries, and cross-compare the
  directory identity across both products (the committed evidence
  already satisfies all of these).

### 2026-09-04 (continued) — fifth fix wave: milestone-4 reopened by the whole-milestone review at 14ce284e

An external whole-milestone review of the exact final tree `14ce284e`
(the record commit that followed the `9b3af7d9` re-close) returned
FAIL with four P1 defect classes and seven P2 findings.  Per the
project-final-review skill, the later record-only commit invalidates
the earlier verdicts, so milestone 4 (delivery step 5) is reopened.
No new product decision is required: the approved user decisions
(D1-A/D2-A/D3-B), the frozen JSON-RPC specification, and the recorded
milestone-4 plan already determine the required behavior.  Product
sources are repaired under this wave where in-scope adapter defects
exist; the Rust product source changes only for the wire-contract
defect below (both languages must conform to the same frozen spec).

P1 blockers and their required corrections:

1. **Go JSON-RPC queue/cancellation contract violation**
   (`v4/go/internal/cli/rpc/session.go`).  The frozen contract
   (`.agents/sow/specs/iprange-jsonrpc-v1.md:82`) requires one active
   request, at most 16 queued, excess requests answered `-32002
   server_busy`, and cancellation/EOF observable while a slow request
   runs.  Go uses an unbuffered worker channel
   (`session.go:181`), and `handleFrame` blocks handing each frame to
   the worker (`session.go:498`, batch at `:535`), so while a slow
   operation runs the dispatcher stops reading stdin: cancellation
   and EOF wait, and frames accumulate in the OS pipe instead of the
   documented 16-entry queue.  Committed evidence shows Go answering
   all 20 pipelined requests and the export completing
   (`v4/cli/evidence/resource.json:30`), and the harness recorded
   that violation as PASS (`v4/cli/resource_harness.py:220`).
   Required correction: mirror the Rust session architecture
   (`v4/rust/iprange-cli/src/rpc/session.rs:222` unbounded channel,
   `in_flight` admission bound of 16, dispatcher never blocks): make
   the Go worker channel buffered at the queue limit while keeping
   the `admitOne` bound, then prove through the real CLI that Go
   answers exactly three `server_busy` refusals behind one slow
   export) and that cancellation and EOF land while the slow
   operation runs.
2. **`maintenance.list` row cannot be passed unchanged to
   `maintenance.remove`**.  The contract requires the unchanged
   opaque list row
   (`.agents/sow/specs/iprange-jsonrpc-v1.md:968`).  Both languages
   omit the optional `previous` evidence member when absent
   (`v4/go/internal/cli/handlers/maintenance.go:859`,
   `v4/rust/iprange-cli/src/rpc/handlers/maintenance.rs:686`), but
   both removal handlers require `previous` present and reject null
   (`maintenance.go:1230`, `maintenance.rs:956`).  The resource
   harness hid the defect by decoding private reservation block
   offsets and fabricating a zero-valued `previous`
   (`v4/cli/resource_harness.py:412,:450`).
   Required correction: accept an absent optional `previous` in both
   removal handlers (present must remain an object with `identity`
   and `digest`; null stays invalid), and change the harness proof to
   feed the listed row byte-for-byte unchanged with no private-format
   knowledge.
3. **Crash scenarios D, E, F do not use the approved interruption
   points**.  The approved plan
   (`.agents/sow/current/SOW-0028-20260828-dual-language-iprange-cli-conformance.md:3851`)
   names commit/finish at a durable sidecar marker, export at a
   partial-output marker, and validate at the worker scratch marker.
   - D (`v4/cli/crash_harness.py:1463`) watches main-file growth
     during input loading.  Investigation for this wave established
     that a durable sidecar write does not exist in the live-commit
     window at the product boundary: both engines' commit paths only
     scan and verify the `.readers` sidecar and mutate the main
     mapping (Go `v4/go/internal/live/live_writer.go` commit path via
     `v4/go/internal/writer/publication.go` Publish; Rust
     `v4/rust/iprange-livedb/src/live_writer/commit.rs`); sidecar
     writes exist only in creation/transition paths already covered
     by scenario B.  The worker therefore returns this finding with
     evidence and keeps the deterministic pre-publication durable
     marker (draft-growth), with the marker and the impossibility
     recorded truthfully in the scenario.
   - E fires when an empty `.export.tmp` merely exists (`:1678`);
     the corrected marker is real partial output (the export
     temporary is non-empty, since the writer emits in 64 KiB
     chunks).
   - F disables scratch and watches the findings-output temporary
     (`:365`, `:1820`); the corrected marker is the worker
     authorized-scratch header (CRC-valid 128-byte ownership header,
     the same durable marker scenario C proves), with the validation
     heap calibrated so the damaged scan spills to scratch.  If
     validation cannot reach that marker at the product boundary,
     the worker must return with evidence instead of silently
     weakening the marker.
4. **The kind-coverage/provenance gate remains bypassable**
   (`v4/cli/check_kind_coverage.py:133,:178,:223`).  Reproduced
   false PASSes: a Rust matrix cloned and relabeled as Go (its own
   binaries block defeats the per-report SHA anchor), Rust→Go crash
   scenarios duplicated and relabeled Go→Rust (label-trusted
   producer/consumer), actor `steps: 0` reports, and failed cases
   with an aggregate `failed: 0` counter.  Required correction: bind
   SHA→implementation consistently across all reports, validate
   command/case identities and counters, require executed steps,
   require crash evidence with real bidirectional identity (not
   labels), and keep per-kind actor lineage instead of assuming every
   consumer opened every artifact.

P2 findings (all corrected in this wave; numbered as the
whole-milestone review of 14ce284e listed them):

1. **Oversized-frame proof did not drain stdout to EOF**
   (`v4/cli/resource_harness.py:366`): proof b read one response
   but never drained stdout to EOF and sent no trailing sentinel,
   so extra parsing or responses went undetected.  Fixed: the proof
   drains stdout to EOF with a bounded deadline and asserts zero
   further bytes; evidence: `v4/cli/evidence/resource.json` proof b.
2. **Windows harness could pass with `entries=1` and zero rows, or
   with UTF-8 encoding** (`v4/cli/windows_housekeeping_harness.py:200,:336`):
   it recorded stronger facts than it enforced.  Fixed: the harness
   now requires `entries == rows` and `basename_encoding == 2` on
   Windows (the stronger assertions are no longer deferred);
   `windows-housekeeping.json` is regenerated with both outcomes
   PASS.
3. **The Windows run never exercised
   `retention.first_seen.refresh`** (the removalCollector Windows
   fix had no native test).  Fixed: the harness natively drives the
   refresh completion, the exact removal log, and the
   private-temporary cleanup; evidence: `windows-housekeeping.json`
   refresh flow steps.
4. **The harness trusted caller-provided `rust=`/`go=` labels**
   without `system.describe` identities or auditable build
   provenance.  Fixed: each outcome records the product-declared
   identity and the binary SHA-256 produced by the authorized
   Windows validation host build, instead of the labels.
5. **The committed GC envelope was malformed** (the previous
   revision's 23-byte synthesized envelope made both products
   truthfully report `cleanup_conflict`; see
   `v4/cli/evidence/windows-housekeeping.json:65` of that
   revision).  Fixed: list/remove is proven on a deterministic
   format-valid 8,192-byte synthesized pair
   (`v4/cli/gc_envelope_windows.py`, mirroring the committed codec
   and the creator-only protected DACL; product-written envelopes
   are timing-dependent at the product boundary and cannot be
   relied on as leftover artifacts).
6. **Review identity invalidated the previous verdicts**: the
   recorded five-reviewer round covered 9b3af7d9 while HEAD was the
   later record commit 14ce284e.  Fixed: the reviews for this wave
   run at one exact final revision with no later repository commits
   (including this record commit).
7. **The host alias of the authorized Windows validation host was
   committed in public artifacts**.  Fixed: forward-sanitized to
   "the authorized Windows validation host" in this SOW and the
   `v4/cli/` documentation, per the sensitive-data gate;
   repository history is not rewritten without user approval.

### Fifth wave implementation (this wave)

All four P1 corrections and the P2 corrections landed:

1. Go JSON-RPC session: `v4/go/internal/cli/rpc/session.go` now
   buffers the worker channel at the 16-entry queue limit, keeps the
   dispatcher responsive (every admission decision returns
   `server_busy` for the 17th in-flight unit; a batch whose units
   are all rejected is answered immediately), and proves
   cancellation/EOF observability while a slow request runs
   (`session_test.go`: pipelined-busy, cancellation, and EOF cases).
2. `maintenance.remove` row round-trips: the reservation `previous`
   evidence member and the windows_housekeeping `artifact`/`problem`
   members are optional exactly as `maintenance.list` emits them, in
   both languages; null stays invalid and unknown members stay
   refused.  The windows_housekeeping fix was found by the
   deterministic pair proof (list rows without a `problem` member
   were refused by both removal decoders with `invalid_argument`).
3. Crash scenarios D/E/F use their approved interruption points,
   and the two plan-recorded markers that are impossible at the
   product boundary are returned with evidence instead of silently
   weakened: D records the sidecar-marker impossibility (the
   engines' commit paths never write a sidecar during the
   live-commit window; sidecar writes exist only in
   creation/transition paths already covered by scenario B) and
   keeps the deterministic pre-publication durable draft-growth
   marker; E waits for real partial export output (non-empty
   `.export.tmp`, since the export writer emits 64 KiB chunks);
   F waits for the private `<id>.export.tmp` findings-output
   temporary (the findings stream is the same buffered export
   writer, so the temp may stay 0 bytes for the whole run) and
   records the scratch-marker impossibility: neither engine's
   validate ever spills to authorized scratch — the scratch budget
   fields are API-parity only in both validation engines, and heap
   sizes 16 B through 256 MiB produce either a truthful budget
   refusal or a full in-memory completion, never a scratch file
   (the calibration is recorded in `crash_harness.py`
   `scenario_f`).
4. `check_kind_coverage.py` derives language attribution from a
   global SHA→implementation map built from every supplied report,
   validates per-case command identities, executed-step counts,
   crash-identity directions and failed counters, requires crash
   evidence, and records per-kind actor lineage.  The five
   reproduced bypass attacks and new clone/relabel/foreign attacks
   fail; the committed evidence passes.
5. Windows qualification (`windows_housekeeping_harness.py` +
   `gc_envelope_windows.py`): native refresh exercise plus a
   deterministic format-valid GC pair proof (see the P2 bullet
   above).  The harness runs under the mingw64 Python of the
   authorized Windows validation host; it uses ctypes only for file
   creation/identity (the MSYS2 Python build cannot call
   `GetSecurityInfo` — documented in the module — so the creator
   commitment is derived from the effective token SID, the same
   source the products prove the live descriptor against).
6. Evidence battery regenerated at the final product sources:
   Rust 38/38 and Go 38/38 single-language matrices, 14+24 both
   mixed directions, crash 16/16 both directions with the negative
   control 0/16, resource 8/8 (Go: exactly 3 `server_busy`, 16
   results, export cancelled), golden 53, sensitivity 14, kind gate
   PASS, Windows housekeeping PASS for both products.

### Fifth wave validation (this wave)

- Go plain and `v4work` suites, both vet modes, and gofmt: green;
  the queue-full busy-batch transport corner is pinned by a
  non-vacuous pipelined test (`TestBatchBusyMembersAnswerInPosition`
  in `v4/go/internal/cli/rpc/session_test.go`: one slow execute, 16
  queued executes, then a 16-member batch that must answer all
  `-32002` from the dispatcher while the worker is still busy) —
  the previous admission-counter-seeded version of this test could
  not fail, and the pipelined version stalls against the pre-fix
  unbuffered dispatcher.
- Rust `cargo test` (plain and `--all-features`): green, including
  the new housekeeping round-trip tests in both languages.
- Linux battery regenerated from staged binaries at the final
  product sources (Go product `2f1d2bba…`, worker `16236608…`;
  Rust product `86056181…`, worker `cb9ad6cd…`) — see
  `v4/cli/evidence/README.md` for the full SHA ledger.  Worker
  identities are build-proven from the staged version-matched
  pairs; the committed reports pin product actors and the fixture
  tool only.
- Windows battery regenerated on the authorized Windows validation
  host (Go product `854abf3a…`, worker `7d81deb7…`; Rust product
  `927dbd47…`, worker `ce757fcb…`) — `evidence/windows-housekeeping.json`
  with both outcomes PASS; the same worker-identity labeling
  applies.
- Record correction: the fourth-wave narrative's intermediate Linux
  Go product hash `f9c7d50e…` is withdrawn.  No committed tree of
  this repository reproduces it (a rebuild at the wave-4 functional
  revision `9b3af7d9` with go1.26.4 and `-buildvcs=false` yields
  `87804648…`), and no committed evidence report pins it.  The
  milestone-4 binary identities are the fifth-wave committed
  evidence ledger, regenerated at the final product sources and
  rebuild-verified at this wave's exact final revision.
- The windows_housekeeping removal round-trip defect found by this
  wave is fixed in both product languages with unit tests
  (`v4/go/internal/cli/handlers/maintenance_test.go`,
  `v4/rust/iprange-cli/src/rpc/handlers/maintenance.rs` tests
  module); the same-failure search covered every other
  list→remove field decoder: scratch rows always carry their
  `authentication` member, and reservation/publication_temp rows
  omit evidence only when the artifact's certification was never
  captured — removal of those is refused by design, because the
  remove API requires the certified evidence
  (`maintenance.go` reservationRemoveFields/publicationTempRemoveFields
  and the Rust twins), unlike the housekeeping envelope row whose
  complete removal identity is present even when `artifact`/`problem`
  are absent.
- Sensitive-data gate: regenerated evidence contains no personal
  paths and no host alias; the SOW, `v4/cli/` documentation and
  evidence are sanitized.
- Artifact gate: end-user docs (`v4/cli/README.md`),
  `v4/cli/evidence/README.md`, and `v4/cli/resource-record.md`
  updated with the final flows and SHA ledger; no spec change is
  required (the optional-member acceptance implements the frozen
  round-trip contract); no project-skill change is required.

### Fifth wave reviews and re-close decision (this wave)

Per the project-final-review skill, the reviews run at one exact
final revision of this wave with no later repository commits.  The
five own-model scope reviews reproduce the evidence battery and the
adversarial gate attacks at that revision, and the whole-milestone
review (glm-5.3-responses, per the user's standing instruction for
this SOW) validates the same exact revision.  Their verdicts, any
repairs they require, and the milestone-4 re-close decision are
recorded here when the reviews land.

### 2026-09-05 (continued) — sixth fix wave: external gate review of da82b571

The external whole-milestone gate review of the exact final revision
`da82b571` returned FAIL.  The lead independently verified every
finding before recording this wave:

- P1 — queue admission undercounts an executing batch in both
  languages: the worker subtracts the whole batch from the
  in-flight counter before executing its first member
  (`v4/go/internal/cli/rpc/session.go` workerLoop,
  `v4/rust/iprange-cli/src/rpc/session.rs` worker_loop), so a slow
  first member leaves the remaining members queued but uncounted
  and excess requests are admitted (reproducer: 10 pipelined
  singles during a slow batch member all admitted instead of 1).
- P1 — the kind-coverage gate still accepted forged evidence: six
  mutation classes return PASS (per-case matrix label lies, report
  command lies, crash binary paths absent from the report root
  binaries table, no crash report at all with crash-only kinds
  fabricated through matrices, zero-step producer credited with
  artifact creation, fabricated consumer opens).
- P1 — crash scenarios D/F do not implement the approved D1-A
  markers and the substitution was not returned for a user
  decision: D interrupts early main-file growth instead of
  commit/finish, and its feed generator is broken (37,500 of
  50,000 rows are duplicates, 12,172 rows carry invalid octets
  such as `10.10.0.256`); F interrupts at the findings-output
  temporary, which is created before validation starts, so the
  kill can precede any validation work.
- P2 — the oversized-frame proof never appends a trailing valid
  request, so "trailing bytes are never parsed" is unproven.
- P2 — Windows evidence gaps: abort/failure cleanup paths of the
  removal-output collector are not exercised, binary hashes carry
  no source-revision/toolchain/build-command provenance, and the
  cross-language listing check inspects only the first row.

**Decision (user, 2026-09-05): 1A — amend the crash interruption
contract, with these conditions:**

- Scenario D becomes "crash during uncommitted live-draft
  construction", with a successful control run first: the repaired
  feed runs through `direct.replace` without interruption in both
  languages and the expected contents and counts are verified;
  only then is interruption during draft construction tested.  The
  exact SDK tests that separately cover commit/finish crash points
  in both languages are recorded.
- Scenario F proves interruption during validation/findings
  delivery, not an exact internal walk point: the interrupted
  findings output must be compared against a successful reference,
  the interrupted output must be incomplete, and no destination
  replacement may exist.  A non-empty temporary alone is not
  sufficient (both products buffer 64 KiB and flush after
  completion).
- The validation-scratch expectation is removed: neither engine's
  validation ever spills to authorized scratch (scratch budgets
  are API-parity only in both validation engines).
- Durability wording: file growth or visible temporary bytes are
  observable process-crash markers; their existence alone does not
  prove they were synced to storage.  All marker wording uses
  "observable process-crash marker", never implied storage-sync
  durability.
- The remaining repairs proceed under the existing approved scope;
  each gate repair gains a negative control proving the previously
  accepted bad evidence now fails.
- Closure pattern (record-then-review): after the repairs and the
  regenerated evidence land in one commit, the five own-model
  scope reviewers and the glm-5.3-responses whole-milestone review
  run at that exact final revision; the resulting verdict is
  reported outside the repository (no later record commit).

This section is the decision record; the implementation and
validation of this wave are recorded below as they land.

### Sixth wave implementation (this wave)

All verified defects were repaired; the user decision 1A (recorded
above) governs the crash-scope wording:

1. Queue accounting (both languages): the worker loops now release
   one queue slot per batch member when that member starts
   executing, instead of subtracting the whole batch on pickup, so
   "one active request plus at most 16 queued" holds while an
   executing batch still has unexecuted members (`session.go`
   workerLoop, `session.rs` worker_loop).  Regression tests:
   `TestActiveBatchMemberFreesOneSlotAtATime` (Go) and
   `active_batch_member_frees_one_slot_at_a_time` (Rust) — a slow
   first batch member plus 10 pipelined singles must admit exactly
   1 and answer 9 with `-32002`; both tests fail against the
   pre-fix wholesale decrement.
2. Kind gate: `check_kind_coverage.py` now requires crash evidence
   (crash-only kinds must come from crash scenarios), validates the
   per-case `matrix` field, the report `command` `--matrix`
   argument, crash binary root-table membership, rejects kind
   credits to zero-step actors, and consumes per-kind actor
   lineage (`created_by`/`opened_by`) from crash scenarios instead
   of assuming every consumer opened every artifact.  Negative
   controls for all six verified forgery classes were added to the
   module self-test and verified end-to-end against doctored copies
   of the genuine evidence (each exits 1).
3. Crash scenarios D/F per decision 1A:
   - The direct-CSV feed generator was repaired (four-octet
     monotonic, non-overlapping ranges with distinct values; the
     previous generator produced 37,500 duplicate rows and 12,172
     invalid rows out of 50,000).
   - D now first records a successful control run (repaired feed →
     `direct.replace` uninterrupted in the producer language: range
     count == 200,000, values spot-checked, transaction advanced),
     then interrupts during uncommitted live-draft construction and
     proves the committed generation still reflects the
     pre-transition state.  Exact commit/finish crash points remain
     covered by the SDK fault gates, recorded in the scenario: Go
     `TestLiveWriterCommitCrashPointsSelectOnlyACompleteGeneration`
     (v4/go/internal/live/lifecycle_crash_test.go:201),
     `TestCrashCommitSelectsCompleteGeneration`
     (v4/go/internal/writer/crash_v4work_test.go:199),
     `TestLiveWriterOutcomeUnknownFailClosed`
     (lifecycle_crash_test.go:365),
     `TestLiveWriterCommitCancellationAbortsDraft`
     (v4/go/internal/live/live_writer_test.go:401); Rust
     `live_crash_tests::commit_crashes_select_only_a_complete_generation`
     (v4/rust/iprange-livedb/src/live_crash_tests.rs:232, fault
     points in writer_core/publication.rs:72-115, driven by
     tests/mixed_live.rs:182).
   - F now proves interruption during validation/findings delivery:
     deterministic leaf-page damage (`F_DAMAGED_LEAF_PAGES = 1400`
     in the harness) yields 1,402 findings / 125,057 bytes for both
     engines on the final binaries; the interrupted run kills after
     the findings temporary carries real flushed bytes and asserts
     the temporary is a strict byte prefix of the reference
     findings with no destination replacement.
   - Marker wording across the harness now says "observable
     process-crash marker" (file growth / visible temporary bytes
     are observable states; existence alone is not claimed as proof
     of storage sync).
4. Oversized-frame proof: proof b now appends a valid
   `system.describe` sentinel after the oversized frame in the same
   stdin stream and asserts exactly one response (-32001, id null)
   with the sentinel unanswered and zero further bytes — trailing
   bytes are provably never parsed.
5. Windows harness: `windows_housekeeping_harness.py` now exercises
   both removal-collector abort/failure cleanup paths
   (result-budget overflow and publish failure on an existing
   destination), records build provenance (`--provenance` JSON:
   revision, clean-tree status, build commands, toolchain) and
   per-binary mtime/size, and validates the cross-language listing
   over every row; report schema v2.
6. Evidence regenerated at the final product sources (Linux): Rust
   38/38, Go 38/38, mixed 14+24 x2, crash 16/16 both directions
   with the negative control 0/16, resource 8/8 (proof b with the
   sentinel), golden 53, sensitivity 14, kind gate PASS.

### Sixth wave validation (this wave)

- Go plain and `v4work` suites, both vet modes, gofmt: green;
  the new queue-accounting regression test fails against the
  pre-fix wholesale decrement and passes now (rpc suite, `-race`).
- Rust `cargo test --workspace --all-features`: 50/50 suites green
  (261 iprange-cli tests); the new session regression test verified
  against the pre-fix behavior.
- Kind gate: module self-test green (existing + six new negative
  controls); the genuine evidence set passes; every forgery class
  verified to exit 1 against doctored genuine-evidence copies.
- Linux battery above regenerated from the staged binaries at the
  final product sources: Go product `8015bc3f…` (changed by the
  per-member queue fix), worker `16236608…` unchanged; Rust product
  `389d01b9…` (changed by the per-member queue fix), worker
  `cb9ad6cd…` unchanged; fixture `322e8e69…`.
- Windows battery completed on the authorized Windows validation
  host at the same product-source revision `908026ab` (clone at
  that exact revision, clean tree): Go product
  `e507b7a9…`, Rust product `e2124f4a…`; the v2 report
  (`evidence/windows-housekeeping.json`) records both outcomes
  PASS with the native refresh exercise, the two removal-collector
  abort/failure cleanup exercises (result-budget overflow and
  publish failure — no `.removals.tmp` residue, no destination
  replacement), the deterministic GC-envelope pair proof
  (list/remove round-trip), full-row cross-language listing with
  matching directory identity, per-binary mtime/size, and the
  build provenance (revision, clean tree, build commands, go1.26.5
  windows/amd64, rustc 1.97.1).
- Resource proof b manual verification: both binaries answer
  exactly one -32001 line when the oversized frame is followed by
  the sentinel; the sentinel never parses.
- Sensitive-data gate: regenerated evidence contains no personal
  paths and no host alias.



### 2026-09-05 (continued) — seventh fix wave: external gate review of aaeb1eef

The external whole-milestone gate review of the exact final revision
`aaeb1eef` (review artifact `/tmp/iprange-aaeb-review.vZvGjc/review.md`)
returned FAIL with two P1 defect classes, one P1 qualification defect,
and six P2 proof defects.  The lead independently verified every
finding before recording this wave, using the review's production
probes against the staged binaries whose hashes match the committed
evidence (Go product `8015bc3f…`, Rust product `389d01b9…`) and the
gate CLI itself:

- P1 — cancelling a queued batch member cancels unrelated active
  work in both languages: the worker installs every batch member as
  active and cancels one shared per-batch token
  (`v4/go/internal/cli/rpc/session.go` workerLoop/applyCancel,
  `v4/rust/iprange-cli/src/rpc/session.rs` worker_loop/apply_cancel).
  Production probe: batch [slow export id=active, describe id=queued],
  cancel(queued) while the export runs → export answers -32010
  cancelled in both binaries.
- P1 — Rust transport retention is unbounded and fully-rejected
  batches are queued behind active work: events and work channels are
  unbounded `mpsc::channel`s (session.rs), so with stdout unread the
  reader accepted all 12,582,912 bytes offered (RSS grew from
  ~5.7 MiB to ~18.7 MiB) and ten all-busy batches produced zero
  responses before a later dispatch marker; Go applied pipe
  backpressure after ~168 frames and answered the rejected batches
  immediately.
- P1 — the kind-coverage gate still accepts mixed matrices with zero
  consumer execution: the per-case check sums actor steps
  (`check_kind_coverage.py` ~:466), so setting consumer.steps=0 and
  removing consumer credits in every mixed PASS case still exits 0.
- P2 — command provenance accepts internally contradictory reports:
  `_argv_value` reads the first repeated flag while the runner's
  argparse uses the final value; `--go /bin/false`, a trailing extra
  `--matrix go`, and a trailing `--producer /bin/false` all pass the
  gate CLI.  Additional lineage mutations verified by the lead to
  pass: fabricated consumer opens, nonexistent actor/operation
  references, contradictory scenario state, unknown-matrix-actor
  credits.
- P2 — artifact lineage omits real sidecar and adapter-output opens:
  scenario B opens a live reader through the consumer
  (`crash_harness.py` ~:1247), which both engines open read-write
  (with slot-claim writes) into `<main>.readers`, yet
  `observed_kinds` hardcodes `live_sidecar opened_by: []` (~:2482)
  and scenario E's consumer open of the completed export destination
  is likewise unrecorded; `run.py` record_ledger counts opened paths
  only when explicitly declared in step params, so implicit sidecar
  opens are absent from matrix lineage; the gate then vacates the
  both-language opened requirement when opened_by is empty
  (`check_kind_coverage.py` ~:908).
- P2 — crash scenario E accepts unlimited orphan residue: the
  assertion is only `len(orphans) >= 1` (~:1977); 21 surviving export
  temporaries pass; the retry residue set is recorded without a bound.
- P2 — the resource harness deadline is bypassed by a partial
  response: `read_responses` calls blocking `readline()` after a
  readable wake (`resource_harness.py` ~:200); a 0.1 s deadline
  blocked 2.01 s and a child that never completes the line hangs the
  harness forever; synchronous stdin writes have no deadline at all.
- P2 — Windows cross-listing accepts extra and false rows
  (`check_synthesized_pair_rows` ~:807, cross branch ~:1116): a third
  alien row, nonexistent inert basename, and wrong identities all
  pass; the exact removal-log proof accepts a single
  `{"removed_at":123456}` record (~:588) although the deterministic
  200/150-record inputs must produce exactly 50 complete records.
- P2 — the frame-over-limit path exits zero in both products while
  the normative spec requires non-zero for startup/framing failure
  (iprange-jsonrpc-v1.md:96-105): both sessions route the oversize
  close through `shutdown()` (exit 0) and the resource proof b
  mandates 0.

**Decision (lead, recorded before implementation): repair all nine
failure classes within SOW-0028.  The exit-status contract is
resolved by the design-authority order: the normative specification
(`iprange-jsonrpc-v1.md:96-105`, framing failure exits non-zero)
prevails over the implementation/test-pinned exit 0; both products,
the Go session regression test, and resource proof b are conformed to
it.**  All other repairs are gate-hardening and defect fixes under
the existing approved scope; every gate repair gains a negative
control proving the previously accepted bad evidence now fails, per
the 2026-09-05 user decision.  The Windows report is regenerated on
the authorized Windows validation host at the final product sources
(D2-A authorization covers SOW-0028 Windows qualification).

Repair ownership (six parallel workers, disjoint write scopes):
- Go session (`v4/go/internal/cli/rpc/session.go`,
  `session_test.go`): per-member cancellation token and active
  identity; oversize frame exits non-zero; regression tests for
  cancelling active, queued, completed, and unknown ids while other
  batch members remain.
- Rust session (`v4/rust/iprange-cli/src/rpc/session.rs`): identical
  per-member cancellation; bounded events and work channels;
  all-rejected batches answered immediately; oversize exits non-zero.
- Kind gate (`v4/cli/check_kind_coverage.py`): mixed per-case
  two-actor execution; executed-operation records; effective/duplicate
  command option rejection and executable→binary-record binding;
  crash scenario shape validation; sidecar/adapter-output opened
  coverage enforcement; negative controls for every rule.
- Runner and crash lineages (`v4/cli/run.py`,
  `v4/cli/crash_harness.py`): truthful implicit sidecar and
  adapter-output opens; per-actor executed-operation records;
  scenario E exactly-one orphan bound with retry residue assertion.
- Resource harness (`v4/cli/resource_harness.py`): monotonic-deadline
  nonblocking reads and writes; proof b requires the non-zero framing
  exit; deadline negative controls.
- Windows harness (`v4/cli/windows_housekeeping_harness.py`): strict
  two-row cross-listing with identity/basename/digest verification;
  exact 50-record removal-log proof with independent derivation;
  removal log captured in the report; self-test negative controls.

Implementation and validation of this wave are recorded below as they
land.

### Seventh wave implementation (this wave)

All nine verified findings were repaired by six parallel workers with
disjoint write scopes (worker ownership is recorded in the decision
section above); the lead then reviewed every diff, fixed the
remaining fixture-ownership clause, and integrated:

1. Go session (`v4/go/internal/cli/rpc/session.go`,
   `session_test.go`): the worker now installs a fresh cancellation
   token and an active-identity set containing only the currently
   executing member's request id (cleared between members), so
   cancelling a queued sibling only marks it cancelled and never
   touches unrelated active work, and a cancelled member's token can
   never poison later siblings; the frame-over-limit path still
   drains queued work and closes resources but returns a framing
   error, so the process exits non-zero per
   `iprange-jsonrpc-v1.md:96-105`.  New regression tests fail
   against the pre-fix code:
   `TestCancelQueuedBatchMemberDoesNotCancelActiveSibling`
   (10/10 deterministic), `TestCancelActiveMemberDoesNotPoisonLaterSibling`,
   `TestCancelUnknownOrCompletedIdWhileOtherMembersRun`,
   and the updated `TestFrameOverLimitFailsWithIDNullAndCloses`
   (now expects the framing error).
2. Rust session (`v4/rust/iprange-cli/src/rpc/session.rs`): the
   identical per-member cancellation scope; the events channel is now
   `sync_channel(64)` and the work channel `sync_channel(QUEUED_LIMIT)`
   so the reader applies pipe backpressure instead of retaining
   unbounded input (the 12 MiB probe now retains ~64 KiB-worth of
   frames); fully-rejected batches are answered immediately in the
   frame handler instead of being queued behind active work (Go
   parity); the worker releases the writer lock before reporting a
   Fatal write failure (a bounded-channel deadlock hazard found
   during the change); the frame-over-limit path exits non-zero.
   New tests: `cancel_queued_batch_member_does_not_cancel_active_sibling`,
   `cancel_active_member_does_not_poison_later_sibling`,
   `cancel_unknown_or_completed_id_while_other_members_run`,
   `frame_over_limit_exits_nonzero` (35/35 module tests pass).
3. Kind gate (`v4/cli/check_kind_coverage.py`): mixed matrices
   require `producer.steps >= 1` AND `consumer.steps >= 1` per PASS
   case; per-case/per-scenario `operations` records are mandatory and
   every lineage ref must index them; command validation uses
   final-value argparse semantics, rejects duplicate identity
   options, and binds `--rust/--go/--producer/--consumer` values to
   the report's binary records (`/bin/false` and unlisted paths
   fail); PASS scenarios must keep `destination_state` and
   `reopen_outcome`; crash open refs must be backed by the scenario's
   recorded `live_reader_opens`/`adapter_output_opens` facts (and
   v4_main opens by the recorded consumer reopen), so fabricated
   opens on scratch/reservation/temp kinds fail;
   `REQUIRED_OPENED_KINDS = ("live_sidecar", "adapter_output")`
   cannot vacate on empty opened coverage; fixture-created v4_main
   (scenario flag `fixture_created_main`) truthfully records no
   product creator.  Self-test controls grew to 35 (new controls for
   every rule, including the fixture-created accept/reject pair and
   the fabricated-open rejection); all nine previously accepted
   mutations now exit 1 through the real gate CLI and the genuine
   regenerated evidence passes.
4. runner and crash lineages (`v4/cli/run.py`,
   `v4/cli/crash_harness.py`): per-actor executed-operation records;
   implicit sidecar opens are derived for live-opening methods from
   the declared main path (`<main>.readers`, `LIVE_OPEN_METHODS` and
   per-method reader-source slots verified against the Rust handlers
   and the case files); crash scenarios record sidecar and
   adapter-output opens at the actual call sites (the false
   "the sidecar is never opened by the consumer" comment is
   removed); scenario B/D record `fixture_created_main` so their
   fixture-created mains carry no product creator (the reviewer's
   fixture-ownership clause); scenario E asserts exactly one
   private `.export.tmp` orphan matching the interrupted attempt and
   that the successful retry introduces no new residue (negative
   control at harness startup).
5. Resource harness (`v4/cli/resource_harness.py`):
   `read_responses` reads incrementally with `os.read` under a
   monotonic deadline (`readline()` removed — a partial line can no
   longer stall the harness); `write_all_bounded` applies the same
   deadline to stdin writes; proof b now requires the non-zero exit
   code of the framing-failure close; `--self-test` proves both
   controls (0.1 s read deadline returns in ~0.101 s, a 1 MiB write
   to a non-draining child fails in ~0.502 s).
6. Windows harness (`v4/cli/windows_housekeeping_harness.py`):
   strict pair-row validation (exactly two rows, candidate-kind
   whitelist, decoded basenames vs the synth names, real-file
   existence, row identities vs the synth recording — including a
   newly recorded `envelope_identity` — and envelope sha256) plus
   exact cross-listing equality; the native refresh proof derives the
   exact 50-record removal log independently from the deterministic
   200/150-record inputs and the first-seen policy (the derived
   digest reproduces the adopted product log byte-for-byte:
   `sha256 96c8ab…`, 4 752 bytes, 50 records), asserts every record
   field and the RPC-advertised identity, and captures the full log
   in the report (schema v3); `--self-test` covers every known
   mutation.  The report is regenerated on the authorized Windows
   validation host at the final product sources.
7. Evidence regenerated at the final product sources (Linux battery,
   work dir `/tmp/qualsvc/ev9/`): Rust 38/38, Go 38/38, mixed
   14+24 x2, crash 16/16 both directions with the negative control
   0/16, resource 8/8 (proof b records the non-zero exit for both
   products), golden 53, sensitivity 14, kind gate PASS with the
   truthful lineage (live_sidecar and adapter_output opened by both
   languages, publication kinds truthfully unopened).  During
   integration the lead found that a previous staged Rust product
   artifact had been built from an intermediate source state (the
   canonical `--bin iprange` release build was not reproducible from
   the recorded command); the canonical clean-release build command
   (`cargo build --release --all-features -p iprange-cli --bin
   iprange -p iprange-livedb --bin iprange-v4-worker --example
   v4-fixture`) is now recorded in `evidence/README.md` with the new
   product SHA-256
   `860938744b203a7684d5dbc96e2fff9a8601f7dfd1fca2484107f5bd3b746e8f`
   and the whole battery was re-run against it.
- Sensitive-data gate: regenerated evidence contains no personal
  paths and no host alias.
- Artifact gate: `v4/cli/README.md`, `v4/cli/evidence/README.md`,
  and `v4/cli/resource-record.md` updated (proof b exit status,
  canonical build identity, seventh-wave narrative); no spec change
  is required (the non-zero framing-failure exit conforms to the
  existing normative shutdown contract; the fixture-ownership and
  open-lineage records are qualification facts, not format
  contracts); no project-skill change is required.

### Seventh wave validation (this wave)

- Go plain and `v4work` suites, vet, gofmt: green; the six session
  regression tests fail against the pre-fix code and pass now (the
  cancellation trio includes the stream-level
  queued-sibling reproducer, 10/10 pre-fix deterministic).
- Rust `cargo test --workspace --all-features`: 50/50 suites green
  (35 iprange-cli session tests including the four new regressions;
  the whole workspace compiles warning-free).
- Kind gate: self-test green (35 controls); every formerly-accepted
  mutation (nine classes) exits 1 through the real gate CLI; the
  genuine regenerated evidence passes with both-language opened
  coverage for `live_sidecar` and `adapter_output`.
- Linux battery above regenerated from the canonical staged
  binaries: Go product `85488a0f…`, worker `16236608…`; Rust product
  `86093874…`, worker `cb9ad6cd…`; fixture `7c616793…`.
- Resource harness self-test: read control 0.1 s deadline returns in
  ~0.101 s (previously blocked the full stall); write control fails
  in ~0.502 s; proof b both products exit non-zero with exactly one
  -32001 and the sentinel unanswered.
- Windows harness self-test: all five P2-5 mutations and all eight
  P2-6 doctored-log controls fail; the synthetic complete set and the
  derived exact log identity pass.
- Windows battery: rerun on the authorized Windows validation host
  at the final product sources after the harness changes (recorded
  in the report below).
- Sensitive-data gate: regenerated evidence contains no personal
  paths and no host alias.

The five own-model scope reviews and the glm-5.3-responses
whole-milestone review run at the exact final revision of this wave
with no later repository commits; their verdicts and the milestone-4
close decision are recorded here when they land.


### Eighth wave (2026-09-05) — five-reviewer round at 26ce667c, repairs, regenerated evidence

The mandated five own-model scope reviews of the seventh wave ran at
the exact final revision `26ce667c` with no repository commits after
it.  Verdicts: authority/contracts PASS (three P3 record-precision
notes); Go implementation PASS (three P3 notes); Rust implementation
and parity FAIL (one P1); evidence and gates FAIL (three P2, three
P3); records/privacy/identity FAIL (two P1, three P2).

Consolidated findings fixed in this wave:

- P1 — Rust frame-over-limit close deadlocks with in-flight work:
  the run loop held the writer mutex guard across `shutdown()`
  (`v4/rust/iprange-cli/src/rpc/session.rs` ~:311-321), and
  `shutdown()` joins the worker while the worker waits for that same
  lock to flush an admitted unit's response; the process then hangs
  forever (SIGTERM-proof; only SIGKILL recovers).  Reproduced on the
  exact committed release binary: 2/20 runs of 18 pipelined
  `system.describe` frames followed by one oversized frame hung,
  while Go (which unlocks before shutdown) exited 1 in 3/3 runs.
  Fixed: the guard is scoped to the -32001 write only, matching Go
  and the worker's own write-failure pattern; the new deterministic
  gated regression test
  `frame_over_limit_with_admitted_work_drains_and_exits_nonzero`
  fails pre-fix (run() hangs, 15 s timeout) and passes post-fix in
  under one second.
- P2 — matrix-side fabricated opens still passed the kind gate: the
  "no v1 open contract opens this kind" rejection existed only on
  the crash path, so an `opened_by` ref on `publication_temp` in a
  doctored matrix ledger exited 0.  Fixed: `matrix_evidence` mirrors
  the crash-side open-contract check (only `v4_main`,
  `live_sidecar`, and `adapter_output` may carry openers); the
  self-test suite builder's `metadata_delivery` ledger truthfully
  records no openers; new self-test control 21b proves the mutation
  now fails.
- P2 — harness deadlines and elapsed measurements used the wall
  clock: every `time.time()` computation in `resource_harness.py`
  (12 sites) and `crash_harness.py` (3 sites) switched to
  `time.monotonic()`; the docstrings already promised monotonic
  deadlines, so they are now truthful.
- P2/P3 — resource-proof determinism: proof a (16-admit/3-busy
  split) flipped to 15-admit/4-busy under CPU contention on Go
  because the worker's member-start slot decrement can lag frame
  admission; proof c.rust listed 0 reservation rows once because the
  kill at the first magic byte can land between the reservation's
  header and evidence page writes, and the maintenance collectors
  list a reservation only at the exact full block size (2 x
  4,096-byte v4 pages = 8,192 bytes).  Fixed: proof a writes the
  export frame alone and waits for the export's private
  `.<handle>.export.tmp` (the member-executing marker) before
  pipelining the describes; proof c kills only after the reservation
  file reaches its full block size.  The resource battery is now
  8/8 in five consecutive runs (previously 2/3).
- P1/P2 — records: the evidence README Windows block still indexed
  the sixth-wave report (v2 at `908026ab` with the sixth-wave
  hashes) while the committed report was v3 at `295ee992`; the SOW
  Status summary ended at the fifth wave; the Followup still listed
  the per-work-unit cancellation item the seventh wave implemented;
  `resource-record.md` quoted stale Linux hashes in the present
  tense.  Fixed in this section and below; the README Windows block
  is regenerated with the eighth-wave report.

Record corrections (forward-recorded here, per record hygiene):

- The seventh-wave finding text that scenario E's consumer open of
  the completed export destination is "likewise unrecorded" is
  corrected: scenario E's consumer probe opens the intact v4 main
  source; adapter outputs are plain-text exports that no v1 reader
  method opens cross-process, so no consumer adapter-output open
  exists to record (`crash_harness.py` scenario E and
  `observed_kinds` document the corrected truth).
- The seventh-wave self-test control counts ("35 controls") are
  corrected to the verified counts: 37 numbered control groups / 47
  asserts at `26ce667c`; the eighth wave adds control 21b -> 38
  groups / 48 asserts (the three reviews counted 33/36/37 under
  different conventions; the group-marker count here is
  authoritative).
- The "six session regression tests fail against the pre-fix code"
  claim is corrected: the Go differential measured exactly three Go
  tests failing pre-fix (the two cancellation regressions and the
  frame-over-limit test; the unknown/completed-id test guards
  already-correct behavior); the aggregate spans both languages.

Open user decision (pre-existing privacy debt, not a wave defect):
the private Windows host alias string remains in the historical
completed SOWs `SOW-0026` (20 occurrences) and `SOW-0027` (6
occurrences) under `.agents/sow/done/`; it is absent from SOW-0028
and from `v4/`.  Per the repository sensitive-data rule, history is
not rewritten without user approval; forward-sanitizing the two
historical records to "the authorized Windows validation host" is
offered to the user together with the milestone decision below.

Eighth-wave implementation landed as:

1. Rust session (`v4/rust/iprange-cli/src/rpc/session.rs`): writer
   guard scoped to the -32001 write in the FrameTooLarge arm, plus
   the gated regression test above.
2. Kind gate (`v4/cli/check_kind_coverage.py`): matrix open-contract
   check mirroring the crash side, suite-ledger truthfulness, and
   self-test control 21b (38 groups / 48 asserts).
3. Harnesses (`v4/cli/resource_harness.py`,
   `v4/cli/crash_harness.py`): monotonic clocks; deterministic
   proof-a export-start marker and proof-c full-reservation-block
   kill point in `resource_harness.py`.

Validation (this wave):

- Rust: `cargo test -p iprange-cli --all-features` 266 passed;
  workspace 866 passed; the new regression test fails pre-fix (hang,
  15 s timeout) and passes post-fix (<1 s); the release product
  SHA-256 is `807d5295…` (was `86093874…`); binary repro —
  oversized-only frame exits 1 with exactly one -32001 id:null
  (3/3), 18 pipelined describes + oversized frame exits 1 with 19
  response lines and no hang (3/3), EOF exits 0 (3/3).
- Kind gate: self-test exit 0 (38 groups / 48 asserts); genuine
  regenerated evidence PASS; the M4b fabricated-open mutation exits
  1; historical mutations (`/bin/false` as `--go`, zero-step mixed
  consumer) still exit 1.
- Resource harness: self-test read control ~0.100-0.101 s, write
  control fails ~0.502 s; the battery is 8/8 in five consecutive
  runs on both products (proof a and proof c deterministic).
- Linux battery regenerated from the canonical staged binaries
  (work dirs `/tmp/qualsvc/ev11` matrices and crash,
  `/tmp/qualsvc/ev12` resource): Rust 38/38, Go 38/38, mixed
  14 executed + 24 skipped per direction — now invoked with
  `--allow-skips` so the recorded report commands exit 0 truthfully
  — crash 16/16 both directions with the `/bin/false` negative
  0/16, resource 8/8 with non-zero proof-b exits, golden 53,
  sensitivity 14, kind gate PASS.
- Evidence identities: Rust product `807d5295…`; Go product
  `85488a0f…` (unchanged); workers `16236608…`/`cb9ad6cd…`
  (unchanged); fixture `7c616793…` (unchanged).
- Go product code is untouched by this wave (the Go scope PASSed at
  `26ce667c` and the regenerated battery re-proves it).

Windows evidence for this wave is regenerated on the authorized
Windows validation host at the final product sources and recorded in
the report below with build provenance.

Sensitive-data gate: the regenerated evidence contains no personal
paths and no host alias; the historical done/ SOW alias debt is
recorded as an open user decision above.

Artifact gate: `v4/cli/evidence/README.md` (Linux identities, proof
determinism wording; Windows block updated with the eighth-wave
report), `v4/cli/resource-record.md` (current hash identities),
`v4/cli/README.md` (proof-a marker wording), and this SOW updated;
no spec change is required (the deadlock fix conforms to the
framing-failure contract; the marker waits and gate checks are
qualification facts, not format contracts); no project skill change
is required.

The five own-model scope reviews and the glm-5.3-responses
whole-milestone review run at the exact final revision of this wave
with no later repository commits; their verdicts and the milestone-4
close decision are recorded here when they land.

### Verbatim review verdicts — eighth wave (delta at `f73968a2`)

The five own-model scope reviews and the glm-5.3-responses
whole-milestone review were run against the exact final revision of
the eighth wave, `f73968a2`, with no later repository commits.  All
six reviews returned PASS with zero P1/P2 findings.  The individual
verdicts and their non-blocking notes follow.

- Authority / contracts (Newton) — PASS, three P3 notes:
  - the Rust 266-test and workspace 866-test counts appear in the
    SOW narrative but were not logged as a reviewer-measured repeat
    run;
  - the 2/20 pre-fix deadlock reproduction is not independently
    re-verifiable after the fix commit;
  - the five-run resource-battery streak is recorded as a claim,
    not as per-run logs.
- Go implementation (James / Ptolemy) — PASS, one P4 note:
  - `v4/cli/resource_harness.py:21` module docstring still says
    "timing race"; the harness now uses monotonic, deadline-bounded
    clocks.  The Go scope additionally verified that
    `git diff 26ce667c..f73968a2 -- v4/go` is empty and the Go
    product SHA-256 is unchanged (`85488a0f...`).
- Rust / parity (Copernicus / Gibbs) — PASS, one P3 note:
  - Rust lacks a committed full-transport test for the immediate
    all-rejected-batch answer branch (Go has
    `TestBatchBusyMembersAnswerInPosition`); the branch is covered
    by the committed binary probes (3/3 no-hang), the product
    rebuilt reproducibly, and 266 `iprange-cli` tests plus 866
    workspace tests are green.
- Gates / evidence (Carver / Arendt) — PASS, zero findings:
  - the mutation battery (fabricated open lineage, `/bin/false` as
    a producer/consumer, zero-step mixed consumer) exits 1 on the
    forged inputs; the genuine regenerated evidence PASSes the kind
    gate; the resource battery re-ran 8/8; Windows, README, and
    privacy records are consistent.
- Records / privacy (Goodall / Locke) — PASS, two P3 notes:
  - the evidence README Go staging path (`/tmp/qualsvc/go`)
    differs from the report argv path (`/tmp/qualsvc-a8/go`), a
    documentation-path inconsistency with no evidence impact;
  - the SOW "exact final revision" sentence did not name
    `f73968a2` literally (resolved by this entry).
- Whole-milestone review (glm-5.3-responses) — PASS at `f73968a2`,
  zero P1/P2, with seven non-blocking notes held as deferred items:
  1. `v4/cli/resource-record.md:49-55` describes proof a as one
     blob with racing boundary IDs; it should describe the
     export-start marker two-phase write.
  2. `v4/cli/resource_harness.py:109-114` comment says "durable
     marker"; it should say "observable export-start marker".
  3. `v4/cli/README.md:52-63` quick-start uses the same `/tmp/w`
     work directory for all four matrices; the runner requires an
     empty work dir, so the printed sequence fails after the first
     run — use distinct work dirs or note the cleanup.
  4. The evidence README Go staging path differs from the report
     argv path (same note as the records review above).
  5. The focused Rust test command needs the worker prebuilt (a
     clean target yields 262/266 until the worker binary is built
     adjacent).
  6. Linux Go provenance should record the `go1.26.4` patch level
     (the README says `go1.26`).
  7. The historical Windows-host alias debt in `done/SOW-0026` and
     `done/SOW-0027` remains an open user decision; history is not
     rewritten without user approval.

Milestone-4 closure conclusion: with zero P1/P2 findings across all
six reviews at `f73968a2`, milestone 4 (delivery step 5) is
closure-ready at that revision.  Per the user's standing rule, the
P3/P4 documentation notes above are deferred to milestone 5 or the
SOW close and must not be committed now; they remain recorded here
as the deferred-item ledger.  The milestone-4 closure and the start
of milestone 5 (delivery step 6: consolidated benchmark harness and
measured ceilings, scope recorded above) require the user decision
recorded in the Status section.


## Ninth wave (2026-09-06) — external gate review of the record revision: broken-stdout deadlock and six qualification gaps

The external whole-milestone gate review of the exact revision
`700e7de9` (the eighth-wave record commit) returned FAIL with one P1
product defect and six P2 qualification defects.  The lead
independently reproduced every finding before recording this wave;
the review's own reproducers are preserved under
`/tmp/iprange-final-review.gifaf4/` and
`/tmp/iprange-crash-recheck-sPgsEj/` (temporary, not committed).

### Verified findings

1. P1 — broken stdout deadlocks both products under input pressure.
   Pipelining 2,000 `system.describe` requests while stdout writes to
   `/dev/full` left both binaries alive indefinitely (reproduced on
   the first trial in both languages).  The Go stack proves the
   cycle: the main loop waits for the worker in
   `Session.fatal`/`shutdown` (`v4/go/internal/cli/rpc/session.go`)
   while the worker blocks sending its fatal event into the full
   64-slot events channel; the Rust worker blocks on the bounded
   `events.send` for the same reason.  Shutdown stops draining
   events, but worker termination depends on one more successful
   send.
2. P2 — the oversized-frame proof treated a forced kill as a
   successful product exit (`v4/cli/resource_harness.py` proof b):
   a stub that answered the -32001 envelope and then slept was
   accepted after 10 s with exit -9.
3. P2 — the same proof accepted a response missing the JSON-RPC
   envelope (`{"error":{"code":-32001}}`) and a 2,100,072-byte
   response above the 1 MiB frame ceiling and 65,000-byte object
   ceiling.
4. P2 — ordinary RPC phases bypassed the resource harness's bounded
   deadlines: source preparation, maintenance listing, and removal
   used the shared client's blocking `stdout.readline()`
   (`v4/cli/run.py`); a stub that answered `{` and stalled blocked
   proof a far beyond the configured 0.1 s read bound.
5. P2 — command provenance accepted abbreviated overrides: the
   runners' argparse accepts unambiguous prefixes, so `--mat go`,
   `--g /bin/false`, `--prod /bin/false`, and a forged
   `--fixture-tool /bin/false` changed the effective executables
   while the gate's literal `--flag` scan still PASSed the reports.
6. P2 — artifact-operation attribution referenced the wrong
   operations: `v4/cli/crash_harness.py` wrote ordinal zero for
   every open and creation, so the committed evidence credited
   scenario E's exports to `producer.0` (`current.publish`),
   scenario C's `recover` scratch to `producer.0`, and scenario
   A1's failed initial open as a successful main open; the gate also
   accepted empty operation lists with positive step counts and
   invented `legacy` operations.
7. P2 — the Windows cross-listing comparison ignored the required
   `artifact.source_basename` field: changing the inert artifact's
   source basename to a different schema-valid name passed both row
   validators in both committed directions.

### Repairs (minimal-complete)

Production transports (P1):

- Go `v4/go/internal/cli/rpc/session.go`: the session now has a
  shutdown signal channel closed by `beginShutdown`; the worker's
  terminal failure report and the termination-signal report select
  on it, so a full events channel can never deadlock the
  shutdown/fatal join.  New regression
  `TestBrokenStdoutWithPipelinedInputTerminates` fails pre-fix
  (3 s hang) and passes post-fix (<10 ms); the product-level flood
  probe (2,000 pipelined describes into `/dev/full`) is now 30/30
  clean per language with a non-zero exit on every run.
- Rust `v4/rust/iprange-cli/src/rpc/session.rs`: the worker's fatal
  report retries `try_send` while checking the recorded shutdown
  flag, so the wakeup is lossless before shutdown and abortable
  after it; `fatal_error` in the control record still drives the
  non-zero exit.  New regression
  `broken_stdout_with_pipelined_input_terminates` fails pre-fix
  (3 s hang) and passes post-fix.

Resource harness (P2 1-3):

- proof b: a `proc.wait` timeout now fails the proof (the harness
  kill is recorded as cleanup, never as the product exit); the
  single -32001 answer is validated with the shared
  `frame.decode_response` plus the 65,000-byte object ceiling, and
  `read_responses` bounds one accumulated line at the 1,048,576-byte
  output ceiling.  `--self-test` gained four proof-b stub controls
  (missing-envelope, oversized-response, hang-after-close must fail;
  valid-envelope must pass).
- Shared client deadlocks (P2 3): `JsonRpcService` accepts optional
  `read_deadline`/`write_deadline`; when configured, the raw pipe
  fds are read/written under selectors with monotonic deadlines and
  a bounded cleanup wait, so `current.publish` preparation and
  `maintenance.list`/`remove` can no longer block past the
  harness-configured bounds.  proof a/c/d services pass the resource
  harness deadlines.  `--self-test` gained shared-path read and
  write deadline controls (a partial-line stub fails in ~0.2 s; a
  non-draining child fails the bounded write in ~0.25 s).
- The resource battery is 8/8 for both products with real work
  recorded (500,000-line feed publish, 20-response admission split,
  -32010 cancellation, exact reservation removal).

Kind gate and crash lineage (P2 4-5):

- `v4/cli/check_kind_coverage.py` replays every recorded command
  through the runner's own argparse construction (mechanically
  lifted from `run.py`/`crash_harness.py` `main()`) with
  `allow_abbrev=False`: any abbreviated or unknown option makes the
  command non-canonical and fails the report; the effective
  rust/go/matrix values bind to the report's binary records and the
  `--fixture-tool` argument must name the battery's crash-recorded
  fixture binary.  The `legacy` operation exemption is removed,
  empty operation lists with positive executed steps fail, crash
  ordinal refs are range-checked, and `v4_main` open refs must index
  an open-capable method.  `--self-test` gained the eight mutation
  controls (all previously accepted mutations exit 1).
- `v4/cli/crash_harness.py` records the actual executed-operation
  ordinal at every open/creation call site (successful consumer main
  opens only), and `observed_kinds` emits `actor.<ordinal>` refs
  with the real values; the regenerated evidence shows A1 opening at
  `consumer.1`, C scratch at `producer.2` (`recover`), E
  adapter-output at `producer.2`, B live sidecar at `producer.5` +
  `consumer.0`.

Windows comparison (P2 6):

- `v4/cli/windows_housekeeping_harness.py` now compares
  `artifact.source_basename` against the synthesized source name
  (UTF-16LE wire) in the pair validator and across local/cross
  listings; the required-field sweep added the remaining missing
  schema members (`directory_role`, `kind`, `source_presence`,
  `inert_presence`, `creation_security`,
  `selected_envelope_sequence`) to the equality checks.  The
  self-test gained control M6 (mutated source_basename must fail);
  the previously accepted mutation now fails for both producers.

### Validation (this wave)

- Go: `go test ./...` green under the recorded toolchain
  (`GOTOOLCHAIN=go1.26.4`); the new broken-stdout regression fails
  pre-fix and passes post-fix.
- Rust: `cargo test -p iprange-cli --all-features` 267 passed
  (266 + the new regression); workspace 867 passed (866 + 1).
- Product probes: oversized-inflight 3/3 per language (exit 1, one
  -32001 id:null, sentinel never answered); broken-output flood
  30/30 per language with zero timeouts and non-zero exits.
- Resource harness: `--self-test` PASS (read/write controls + four
  proof-b controls + shared-path deadline controls); proof-b stub
  triads REJECT (missing-envelope via envelope validation,
  oversized via the frame ceiling, hang-after-close via the
  self-exit requirement); proof a stall fails in ~0.30 s with the
  configured 0.1 s bounds; real-product battery 8/8.
- Kind gate: `--self-test` exit 0 (48 original asserts + 9 new
  controls); all eight follow-up mutations REJECT with the intended
  reasons; genuine regenerated evidence PASSes (all 7 kinds, both
  languages).
- Crash harness: 16/16 PASS on the regenerated battery; the
  `/bin/false` negative is 0/16; the regenerated
  `v4/cli/evidence/crash.json` carries truthful per-kind ordinals.
- Matrices: rust 38/38, go 38/38, rust_to_go 14 executed + 24
  skipped (with `--allow-skips`), go_to_rust 14 + 24.
- Golden corpus 53; sensitivity gate 14 modes; Windows harness
  self-test PASS.
- Binary identities at the wave's canonical staging paths: Rust
  product `58036aee…`, Go product `f3e9f1e4…`, Go worker
  `202a83ac…` (changed with the product: it links
  `internal/cli/rpc`), Rust worker `cb9ad6cd…` and fixture
  `7c616793…` unchanged.
- Windows host qualification for this wave is regenerated on the
  authorized Windows validation host with provenance in the report.

### Pre-existing environment finding (not caused by this wave)

With the machine's default `go1.27.0` toolchain,
`TestMetadataDeflateHeapOverheadCoversWorkspace`
(`v4/go/internal/writer`) fails at HEAD unchanged: go1.27's flate
workspace measures ~1.06-1.08 MiB against the declared 840 KiB
honest charge.  The same test passes under the recorded canonical
toolchain `go1.26.4` (`GOTOOLCHAIN=go1.26.4`), which is also what
the staged qualification binaries are built with.  The constant
lives in the writer engine (SOW-0025 scope), not in SOW-0028's
transport/harness scope; it is recorded here and flagged to the
user, and the wave's Go validation runs under the canonical
toolchain.

### Sensitive-data gate

The regenerated evidence and this SOW contain no personal paths and
no host alias; the historical done/ SOW alias debt remains recorded
as the open user decision above.

### Artifact gate

`v4/cli/evidence/README.md` (ninth-wave narrative + new identity
block), `v4/cli/resource-record.md` (current hashes), this SOW
updated; no spec change is required (the fixes conform to the
framing-failure, envelope, and boundedness contracts); no project
skill change is required.

The five own-model scope reviews and the glm-5.3-responses
whole-milestone review run at the exact final revision of this wave
with no later repository commits; their verdicts and the
milestone-4 close decision are recorded here when they land.

## Tenth wave (2026-09-06) — first role-based review round at `d6f757c3`

This is the first round run under the approved role-based review
protocol (seven standing roles with sandboxes under `.local/`,
recorded above).  The round covered the exact ninth-wave revision
`d6f757c3` with the staged binaries in
`.local/shared/binaries/SHASUMS.txt` (rust `58036aee…`, go
`f3e9f1e4…`, go worker `202a83ac…`, rust worker `cb9ad6cd…`,
fixture `7c616793…`).

### Verdict: FAIL

Six roles returned: parity (FAIL), performance (FAIL), operations
(FAIL), security (FAIL), tester (FAIL), portability (FAIL).  The
glm-5.3-responses whole-milestone validator confirmed the Windows
provenance finding and no additional distinct issue.  Every blocking
finding below was independently reproduced by the lead at `d6f757c3`
before being recorded.

### Verified blocking findings

1. P1 — termination signals are ignored while the transport is
   wedged, in both products (performance; lead-reproduced 4/4).
   Trigger: ~2,000 pipelined `system.describe` frames, stdout
   unread, stdin held open.  Within ~1 s the worker is blocked on
   the full 64 KiB stdout pipe, the main loop on the full work
   queue, the reader on the full 64-slot events channel.  In that
   state SIGINT/SIGTERM never terminates the process (only SIGKILL
   works).  Cause: every fatal report crosses the full events
   channel; the ninth-wave repair makes reports abortable only once
   shutdown begins, which cannot happen in the wedge.  Go:
   `v4/go/internal/cli/rpc/session.go:279-290, 428-435`; Rust:
   `v4/rust/iprange-cli/src/rpc/session.rs:966-979`.  No committed
   test sends a termination signal to either session.

2. P2 — Rust fatal-report retry is a no-yield busy-spin
   (performance; code-verified).  `session.rs:597-614` retries
   `try_send` in a tight loop with no `yield_now()`; in the
   Finding-1 state it can burn one core indefinitely.

3. P2 — Go broken-stdout exit is a runtime SIGPIPE death, not the
   session fatal path (performance; lead-reproduced).  Close stdout
   mid-flood: Go dies `rc=-13` with no stderr, Rust exits 1 with
   `iprange: Broken pipe (os error 32)`.  Non-zero either way, but
   the wave narrative attributes the Go exit to `control.fatalWrite`
   and cleanup, which is EPIPE-false (`/dev/full` floods do use it).

4. P2 — busy/reject accounting diverges ~2.4× between products on
   identical pipelined floods (parity; lead-reproduced).  3,000
   frames: Rust 446 result / 2,554 busy; Go 1,087 / 1,913.  512
   frames: Rust 113/399, Go 143/369.  Both products stay within the
   documented 1-active + 16-queued bound at every instant, and the
   deterministic slow-member case (resource proof-a) is identical in
   both; the distribution of which requests are rejected is not a
   committed contract and has no detecting test.

5. P2 — the kind gate accepts a fabricated cross-language matrix
   whose "rust consumer" never executed (parity, tester; lead-
   reproduced, gate exit 0).  Genuine Go-run case records relabeled
   to `go->rust` with a genuine rust sha from the same report's
   binaries block pass every check.  `check_kind_coverage.py`
   derives attribution from attacker-controlled report labels with
   no per-case execution anchor.

6. P2 — the kind gate accepts fabricated crash-side lineage
   (operations; lead-reproduced 2/3 mutation classes).
   `authorized_scratch.created_by = ["producer.0"]` (`current.publish`
   credited as recovery-scratch creator) and
   `live_sidecar.opened_by = ["producer.3"]` (`maintenance.list`
   credited as a live open) pass.  Capability enforcement exists
   only for `v4_main` opens
   (`check_kind_coverage.py:192-200`); the create side and the
   multi-writer open kinds have none.  (The operations report's
   third claim — a matrix `system.describe` creator credit — is NOT
   reproducible: the gate rejects it at `d6f757c3`.)

7. P2 — a fully consistent fixture-tool forgery is accepted (tester;
   lead-reproduced).  Changing the crash root table, the crash
   command, and every matrix command to one nonexistent fixture path
   with sha `"4"*64` passes; the gate never hashes or stats any
   binary and the fixture binding is cross-report path equality
   only.

8. P2 — the deadline-bounded shared client is wired only into the
   resource harness (operations; code-verified).  Crash-harness
   call sites and the conformance runner construct
   `run.JsonRpcService` without `read_deadline`/`write_deadline`
   (`v4/cli/crash_harness.py:314-317, 668-1497`;
   `v4/cli/run.py:1189-1199, 1660`); a product stall hangs those
   gates forever — the exact class the ninth wave repaired on the
   resource path.

9. P2 — spec contradicts pinned behavior for id-less non-cancel
   requests (operations; lead-reproduced).  The spec
   (`iprange-jsonrpc-v1.md` ~line 87) says such a request "produces
   no response"; both products answer one `-32600` with `id: null`
   and reject the whole batch if embedded.  The shared schema pins
   the product behavior (`v4/cli/schema/frame.py:136-144`); no test
   pins either side.

10. P1 records — the Windows qualification claim for this wave is
    contradicted by the committed evidence (security, glm, tester,
    portability; lead-verified).  `v4/cli/evidence/
    windows-housekeeping.json` is byte-identical at `700e7de9` and
    `d6f757c3` (sha `353265d4…`), records
    `build_provenance.revision = 90a935b2` (eighth wave) and 2026-09-05
    builds; the SOW ninth-wave section and
    `v4/cli/evidence/README.md:126-129` claim regeneration at the
    final product sources, which is false.  The ninth-wave session
    repair is therefore unproven on the Windows host.

11. P2 records — the Go worker hash-change explanation is false
    (security; lead-verified).  The wave says Go worker `202a83ac…`
    "changed with the product: it links `internal/cli/rpc`"; the
    staged worker has 0 `internal/cli/rpc` symbols and the worker
    tree is unchanged `90a935b2..d6f757c3`.  Worker identity is
    build-proven, not pinned, so evidence is unaffected; the
    explanation must be corrected.

### P3 notes (recorded; fixed in passing or reported)

- Parse-error `message` text differs between products (parity F3);
  `code`/`id`/exit behavior identical; diagnostic-only.
- Signal + EOF race can exit 0 (parity F4, operations mid-drain
  note): identical race in both products, untested; the deterministic
  daemon-idle case exits 1.
- Rust `signals` module uses `unsafe` FFI outside the frozen
  boundaries (portability, pre-existing, idiomatic
  `pthread_sigmask`/`sigwait`; recorded for the boundary ledger).
- `resource-record.md` peak-RSS measures runner + product child
  together; milestone 5 must separate product attribution.
- Crash-command binding uses `realpath(named)` vs matrix realpath-
  vs-realpath (tester P3, genuine evidence unaffected).

### Open decisions for this wave (user decision required before repair)

- D1 — signal-wedge repair design (Finding 1/2).
- D2 — busy/reject split parity contract (Finding 4).
- D3 — id-less non-cancel contract (Finding 9).
- D4 — Windows qualification regeneration (Finding 10).

### User decisions (2026-09-06, recorded before implementation)

- D1: A — graceful fatal path plus a bounded watchdog in both
  products; the watcher records the termination signal, attempts the
  normal cancellation/cleanup path, and force-exits non-zero with a
  stderr diagnostic if shutdown has not begun within ~500 ms.  The
  Rust fatal-report retry gains `yield_now()`.  Add committed
  signal tests for idle and wedged transport states, and make a
  signal observed before EOF always win over the EOF exit-0 path.
- D2: A — the busy/reject distribution under sustained pressure is
  declared non-contractual (scheduler-dependent) in the spec and
  SOW; normative bound (1 active + 16 queued), exactly-once id
  coverage, deterministic slow-member ordering stay tested;
  the split is recorded as diagnostic evidence.
- D3: A — amend the spec to the pinned behavior for id-less
  non-cancel requests (one -32600 with id null; whole-batch
  rejection inside a batch) and add a conformance case pinning it.
- D4: A — regenerate the Windows host qualification on the
  authorized validation host from the repaired wave-10 HEAD at the
  end of the wavebinary shipment, and correct the SOW/README claims.

### Repairs implemented (wave 10, committed after validation)

D1-A — termination-signal contract (findings 1-3):
- Go `v4/go/internal/cli/rpc/session.go`: the signal watcher no longer
  selects on reader EOF, records the signal in the control plane
  (`terminationSignal`), and arms a 500 ms watchdog that prints a
  diagnostic and `os.Exit(1)` when the graceful fatal report cannot
  be delivered (wedged transport).  The EOF exit path returns the
  recorded signal error when a signal raced EOF, so a signal observed
  before/during EOF always wins over the exit-zero path.
- Rust `v4/rust/iprange-cli/src/rpc/session.rs`: the signal watcher
  records `termination_signal` in the control plane and retries the
  fatal report with explicit `yield_now()` and a 500 ms force-exit
  deadline; the worker's fatal-report retry gains the same yield +
  deadline; the EOF branch reports non-zero when a signal was
  recorded.
- Committed regressions: Go helper-process tests
  (`TestTerminationSignalIdleExitsNonZero`,
  `TestTerminationSignalWedgedSessionForcesNonZeroExit`,
  `TestTerminationSignalDuringDrainWinsOverEOF`) and Rust process
  tests (`idle_session_signal_exits_nonzero`,
  `wedged_session_signal_forces_nonzero_exit`); the wedged-transport
  case reproduced the P1 hang pre-fix and exits 1 post-fix at product
  level (4/4 signal trials per product).
- Record correction: the ninth-wave narrative attributed the Go
  close-stdout exit to `control.fatalWrite`; the Go product dies by
  runtime SIGPIPE (`rc=-13`, no stderr) on fd-close while Rust exits 1
  with `Broken pipe`.  Both are non-zero transport failures; the
  records now state the Go mechanism truthfully (evidence README
  identity block).

D2-A — busy/reject distribution (finding 4):
- Spec `iprange-jsonrpc-v1.md` requests section: the 1-active +
  16-queued bound is normative; the exact busy/reject distribution
  under sustained pressure is scheduler-dependent and not part of the
  cross-language parity contract; every request is answered exactly
  once and accepted requests execute in admission order.
- Wave-10 diagnostic split (3000-frame flood, 4 runs): Rust
  468-607 result / ~2400-2500 busy; Go 1105-1289 / ~1700-1900 busy —
  recorded as diagnostic; the deterministic slow-member ordering
  remains covered by resource proof-a (identical in both products).

D3-A — id-less non-cancel requests (finding 9):
- Spec `iprange-jsonrpc-v1.md`: an id-less non-cancel request is an
  invalid notification: not executed, answered with one `-32600`
  whose id is null; inside a batch the whole batch is rejected with
  one `-32600` id:null.
- Shared schema `v4/cli/schema/frame.py`: `decode_response` now
  blesses id:null for the -32600 invalid-notification response (it
  previously allowed only -32001), matching both products and the
  request-side schema (frame.py:136-144).
- Pinned by committed tests in both products (Go
  `TestIdlessNonCancelIsInvalidNotification`; Rust
  `idless_non_cancel_is_an_invalid_notification`,
  `batch_with_idless_member_is_rejected_as_a_whole`) and by two new
  golden exchanges (`v4/cli/golden/errors.json`, corpus 53 -> 55;
  `check_golden.py` extended with the `expect_error` exchange family).

D4-A — Windows qualification (finding 10): regeneration on the
authorized Windows validation host runs at the wave HEAD after the
role round (Section below).

Kind-gate hardening (findings 5-7):
- `v4/cli/check_kind_coverage.py`: per-kind per-actor
  method-capability maps for created_by and opened_by refs on both
  the crash and matrix sides (derived from the genuine evidence and
  the harness recording call sites); crash-command binding is now
  realpath-vs-realpath; `--self-test` gained controls 35-38.
- Per-case `actors.<role>.argv` execution anchor recorded by the
  runner (`v4/cli/run.py`) and enforced by the gate for argv-era
  batteries; the relabeled-matrix forgery (finding 5) is REJECTED
  against the regenerated evidence (30 problems; pre-regen evidence
  passes by the documented conditional rule).
- The crash-side fabricated lineage mutations (finding 6) are
  REJECTED (verified: `fabricated-create-credit` 3 problems,
  `fabricated-sidecar-open` 2 problems).
- The gate docstring now states the honest guarantee: mechanical
  consistency and identity anchor; a fully consistent offline forgery
  of ALL reports is caught by adversarial review reruns, and fixture
  identity is a cross-report consistency anchor (no on-disk hash is
  possible for committed evidence whose build paths are gone).

Deadline-bounded client wiring (finding 8):
- `v4/cli/run.py` (RUNNER_IO_DEADLINE_SECONDS=120,
  PROBE_IO_DEADLINE_SECONDS=30) and `v4/cli/crash_harness.py`
  (CRASH_IO_DEADLINE_SECONDS=120) pass read/write deadlines at all 30
  service construction sites (3 runner + 27 crash harness); a stalled
  product now fails the proof instead of hanging forever.  Named
  constants are documented as stall guards, not performance budgets.

Records corrections:
- Go worker identity: the ninth-wave claim "changed with the product:
  it links internal/cli/rpc" is false (0 rpc symbols; worker tree
  unchanged `90a935b2..d6f757c3`; rebuilding the unchanged worker
  reproduces `202a83ac…`, so the wave-8-to-wave-9 hash move was a
  build-environment sensitivity, not a source change).  Corrected in
  the evidence README.
- The ninth-wave "no spec change is required" sentence is superseded
  by the D3 spec amendment above.
- P3 notes from the round: parse-error `message` text differs between
  products (diagnostic-only; code/id/exit identical; accepted);
  Rust `signals` `unsafe` FFI is pre-existing, idiomatic
  `pthread_sigmask`/`sigwait` and remains inside the documented
  exception for the signal handler boundary; the wave-10 signal
  changes did not add unsafe.

### Validation (wave 10, all at the wave HEAD with the committed binaries)

- Go: `go test ./...` (canonical `go1.26.4`) — all packages PASS,
  including the three new termination-signal tests and the id-less
  tests.
- Rust: `cargo test -p iprange-cli` — 269 lib/bin tests + 2 process
  termination-signal tests PASS.
- Products (release, recorded SHASUMS): Rust `2315e28a…`, Go
  `3bf33dfd…`; worker and fixture identities unchanged (`cb9ad6cd…`,
  `202a83ac…`, `7c616793…`).
- Signal wedge at product level: 4/4 trials per product exit 1
  (pre-fix: ignored indefinitely).
- Matrices (regenerated, argv-era): rust 38/38, go 38/38,
  rust_to_go 14+24, go_to_rust 14+24; mixed runs with `--allow-skips`
  recorded truthfully in each report.
- Crash battery: positive 16/16 (both directions); negative
  (/bin/false) 0/16; a concurrent-battery transient failure of the
  detached crash run was reproduced and isolated — standalone runs
  pass 16/16 consistently (twice), and the committed reports come
  from the standalone runs.
- Resource battery: 8/8; golden corpus 55 exchanges; sensitivity
  gate 14 modes; Windows harness self-test PASS.
- Kind gate: PASS on the regenerated genuine evidence; the three
  verified forgery classes now FAIL (relabeled matrix, crash
  create-credit, crash sidecar-open); `--self-test` exit 0.
- Peak-RSS attribution note (M5): `resource-record.md` records that
  the measured peak RSS includes the runner child together with the
  product; milestone 5 must measure the product child separately
  before the 1.3x ceiling claim.

### Sensitive-data gate (wave 10)

No personal paths, host aliases, or secrets in the regenerated
evidence or this SOW; all probe scratch stays under `.local/` and
`/tmp/qualsvc/ev17/`; the committed evidence paths point at the
recorded staging identities only.

### Artifact gate (wave 10)

- Specs: `iprange-jsonrpc-v1.md` amended (D2 distribution sentence,
  D3 invalid-notification sentence).
- End-user docs: none affected (transport contract only).
- Evidence: `v4/cli/evidence/*.json` regenerated; README identity
  block and narrative updated; `resource-record.md` updated
  (identities + M5 attribution note).
- Project skills: `project-final-review` and `project-v4-rust`
  unchanged (no workflow change).
- SOW lifecycle: this section records the wave; the role round and
  Windows regeneration are tracked below.


### Repairs (wave 10, second role round at `639b529f`, re-fix in progress)

The first wave-10 fix commit `639b529f` was sent through the second
role round (all seven roles, same brief: delta re-review at the
repaired revision with the staged binaries `2315e28a…` rust /
`3bf33dfd…` go).  The round returned FAIL with ten verified findings;
the re-fix for every finding is implemented and validated below.

#### Verified round findings at `639b529f`

1. P1 — signal-wedge design flaw (tester/operations).  The D1-A
   watchdog required the Fatal-event *send* to block.  With a
   partially-filled events channel (≤ 63 free slots) the
   `try_send` succeeds guaranteed, so the watchdog's force-exit
   deadline disarmed, and the wedged main loop (blocked on the full
   work queue or joining a worker blocked on the full stdout pipe)
   never processed the delivered event: the process ignored the
   signal forever.  Two reproduced sub-states: the partial-flood
   wedge (60 frames, stdin open, stdout unread; 13 s+ hangs) and
   the drain-wedge (worker blocked mid-write on the full stdout
   pipe at EOF, main loop joining it; 18/18 trials hung).
2. P1 — Go loses the concurrent signal+EOF race about half the time
   (parity).  Go delivers signals to `signal.Notify` channels
   asynchronously via the runtime; Rust's process-mask + `sigwait`
   cannot lose the signal.  Leader reproduced: ~50% of concurrent
   signal+EOF runs exited 0, contradicting the wave's
   signal-wins-over-EOF contract.
3. P1 — Rust export worker panics on a frame-valid wrong-name budget
   (glm).  `export` params carrying `max_workspace_bytes` (instead
   of the canonical `max_open_files`) passed validation and panicked
   the worker at `v4/rust/iprange-cli/src/rpc/handlers/export.rs:1189:25`
   ("no entry found for key"), killing every later response; Go
   answered the same frame `-32602` and continued.
4. P1 records — `v4/cli/evidence/README.md:140-143` still contained
   the wave-9 false sentence claiming the Windows evidence was
   "produced at the final product sources" (security/glm/
   operations).  The committed Windows evidence is the eighth-wave
   set (`90a935b2`); the wave-10 regeneration had not happened.
5. P2 — Go signal tests are not Unix-guarded (portability):
   `cmd.Process.Signal` fails to compile on Windows.  The signal
   test block had to move behind `//go:build unix`.
6. P2 — Rust lacked committed detections for the mid-drain
   signal-wins-over-EOF rule and for both wedge sub-states
   (tester/performance).  The wave narrative claimed them "in both
   languages"; only Go had the mid-drain detecting test at
   `639b529f`, and neither product had the partial-wedge or
   drain-wedge sub-state tests.
7. P2 — kind gate argv anchor still bypassable (security/parity).
   Dropping `path` from the report binary records, or stripping
   argv from every report (the pre-regen escape hatch), made the
   forgery probes pass with exit 0.
8. P2 — Windows housekeeping harness constructs its RPC client with
   no deadlines (operations): `windows_housekeeping_harness.py:1769`
   could hang the qualification host forever on a stalled product.
9. P2 — Go deflate charge is pinned to the go1.26.4 toolchain
   (portability): `v4/go/internal/writer/metadata.go:31` documents an
   ~840 KiB flate workspace that measures ~1.08 MiB under go1.27.
   This is a writer-engine (SOW-0025/0030) concern, not a SOW-0028
   adapter defect; flagged to the user, tracked in the SOW-0030
   queue, no product change here.

Also recorded: Go watchdog diagnostic formatting ("terminated by
signal terminated"); the Rust watcher's channel-holder is a one-shot
CLI lifetime (accepted); the README "recorded below when it lands"
dangling pointer.

#### Re-fix (wave 10, second round)

- D1-A deepening — process-lifetime bound, not delivery-bound: both
  watchers force-exit non-zero 1 s after the signal is consumed,
  whether or not the Fatal delivery succeeded (Go
  `signalForceExitTimeout = 1s`, Rust watcher 1 s deadline + sleep
  pending the graceful path).  The partial-flood and drain-wedge
  sub-states both terminate now.  Go additionally polls `sigCh` for
  25 ms at clean EOF before accepting the exit-zero result, closing
  the runtime-delivery race (finding 2).
- Rust export canonical member-set validation
  (finding 3): the `result_budget` member is accepted only when the
  params carry exactly the canonical export members; a wrong-name
  budget member now yields `-32602` like Go, with a unit regression
  and a product-level repro (both products answer `-32602`, the
  trailing `system.describe` still answers, no panic).
- Records (finding 4): the false README sentence is deleted and
  replaced with the truthful eighth-wave identity plus the pending
  D4 regeneration note; the dangling "recorded below when it lands"
  pointer now names SOW-0028's tenth-wave section.
- Portability (finding 5): the Go signal tests moved to
  `v4/go/internal/cli/rpc/session_signal_unix_test.go` with
  `//go:build unix`; `session_test.go` keeps the non-signal tests
  and no longer imports `os`/`os/exec`/`syscall`.
- Signal regression coverage (finding 6): Rust unit test
  `signal_recorded_during_eof_drain_wins_over_exit_zero` plus
  process tests `partial_wedge_signal_forces_nonzero_exit` and
  `drain_wedge_signal_forces_nonzero_exit`; Go helper modes
  `partial-wedge` and `drain-wedge` with
  `TestTerminationSignalPartialWedgeForcesNonZeroExit` /
  `TestTerminationSignalDrainWedgeForcesNonZeroExit`.
- Kind gate (finding 7): the per-case argv anchor is unconditional
  (`argv_required_here = True`); actor argv must be an absolute
  path; the report binary record matched by the actor sha256 must
  carry a path.  `--self-test` gained the pathless-binary-record and
  relative-argv controls; the argv-strip battery control now asserts
  FAILURE.  The pre-regen escape hatch is closed.
- Windows harness (finding 8): `RPC_READ_DEADLINE_SECONDS = 300.0` /
  `RPC_WRITE_DEADLINE_SECONDS = 120.0` wired into every
  `HarnessJsonRpcService` construction.
- Deflate charge (finding 9): no SOW-0028 product change; flagged to
  the user, tracked for SOW-0025/0030.

#### Validation (wave 10, second round, uncommitted re-fix tree)

- Go: `go build ./...` PASS; `go test ./...` (canonical go1.26.4)
  PASS including the six signal tests (idle, wedged, mid-drain,
  partial-wedge, drain-wedge, helper).
- Rust: `cargo test -p iprange-cli` — 270 lib/bin tests PASS
  (including the mid-drain unit test and 23 export tests) + 4
  termination process tests PASS.
- Products (release, rebuilt after re-fix): Rust `de597e18…`, Go
  `b3a359c8…` (third round: the clean-EOF sigCh FIFO race fix
  rebuilt the Go product, and the Rust EOF grace fix rebuilt the
  Rust product; Linux and Windows evidence regenerated at the
  final identities); worker and fixture identities unchanged
  (`cb9ad6cd…`, `202a83ac…`, `7c616793…`).
- Product-level probes: signal wedge 4/4 trials exit 1 per product
  (SIGINT+SIGTERM); partial-wedge 60-frame probe: 8/8 trials exit 1
  in ~1.015 s across both products (pre-fix: 13 s+ hang); export
  wrong-name-budget repro: both products `-32602`, trailing describe
  answered, no panic.
- Kind gate: `--self-test` exit 0 (controls updated to the
  unconditional semantics); genuine committed evidence PASS; the
  argv-strip and pathless-record mutations FAIL (self-test controls
  35/35b/35c).  Caveat recorded after the third round: the
  `.local/parity/wave10/forged-consistent-*.json` probes are
  pre-argv-era clones whose actor maps predate the executed-identity
  schema; substituted into the 'go' slot they fail only by
  label/duplicate composition, and in their own declared slot they
  PASS — that residual (a fully consistent one-report fork with
  rewritten shas, argv, and paths) is the gate's documented
  limitation, mitigated by the battery reruns and the adversarial
  rounds, not by the mechanical gate.
- Evidence regeneration with the re-fixed binaries and the D4
  Windows run are completed (recorded below).

#### D4 — Windows qualification regeneration (completed 2026-09-06)

On the authorized Windows validation host (SOW-0028
qualification only), the wave-10 product sources at `e13be7ea`
(clean tracked tree; initially `f67fc728`, rebuilt after the
third-role-round Go signal fix and the Rust EOF grace fix) built:
- Go `984d0e9d…` (go1.26.5 windows/amd64, `-buildvcs=false`);
- Rust `877824f0…` (rustc 1.97.1).

The wave-9 deadline-bounded RPC client used select() on pipe fds,
which Windows rejects (WinError 10038/10093); the first host run
failed at the first bounded write.  The shared client now applies
deadlines with worker threads on Windows (buffered wrappers kept, a
timed-out thread poisons the service because its bytes can no longer
be correlated) while POSIX keeps the selector path.  With that
repair, `windows_housekeeping_harness.py` (mingw64 Python 3.14) ran
2/2 PASS on the host with the deadline-bounded client and recorded
`windows-housekeeping.json` at schema v3 (exact 50-row removal log,
removal-log sha256, build provenance) — the native refresh exercise,
both removal-collector abort/cleanup proofs, the deterministic GC
pair proof and cross-listing, and 200-row/150-row refresh flow.
Windows binary hashes, build commands, toolchain, tree-clean state,
and source revision are recorded in the report's
`build_provenance` block.


#### Third round (wave 10) — delta re-review findings and re-fix

The seven roles re-reviewed the second-round repairs at `747fc1c6`
with the staged binaries `4b9683b5…` rust / `42270270…` go.
glm-5.3-responses returned FAIL with one P1 (recorded below);
operations, parity, and the glm validator PASSed the delta; the
portability role PASSed with one P2 — no committed regression test
exercised the eof-first supervisor shape (close stdin + signal
back-to-back), so the sigCh-poll defect could silently return.
That P2 was first addressed by committed tests in both languages
(commit `383c7d42`, test-only; product binaries unaffected):
- Go: `TestTerminationSignalEOFFirstWinsOverExitZero` — the helper
  writes the describe response on stdout and the parent signals the
  moment the response line appears, i.e. inside the EOF tail.  Kept:
  the Go EOF tail's 25 ms grace window makes the shape deterministic
  (pre-fix exit 0 ~100%; committed test 3/3).
- Rust: an `eof_first_signal_wins_over_exit_zero` process test was
  committed at `383c7d42`, then removed at `5dd1ac6d` (security role
  finding; lead reproduced 1/3 full-suite failures), because at that
  point the assertion was stronger than the product guarantee — the
  Rust EOF tail checked the recorded signal once after the worker
  join with no grace window, so a parent kill landing after the
  check legitimately observed exit 0 (sub-millisecond TOCTOU,
  measured 6/20 single-test failures under load).  The glm-5.3
  final validator then FAILed the closure: the approved D1-A
  contract requires the signal to win at any point, and the removal
  had silently re-scoped an approved contract.  The product was
  fixed instead (commit `e13be7ea`): the Rust EOF tail now polls
  the watcher's recorded flag for the same 25 ms grace window Go
  uses (75/75 non-zero at 0-15 ms offsets; the residual past the
  grace window is the same bounded class as Go).  The
  `eof_first_signal_wins_over_exit_zero` process test is restored
  and deterministic (5/5 process tests, 3/3 consecutive full-suite
  runs green).  Rust product identity changed to `de597e18…`.

tester, security, and performance were still reviewing the earlier
delta; their re-verdicts at the final revision `383c7d42` are
recorded after this section.

P1 (glm, lead-reproduced 103/112 at 3-26 ms offsets) — the Go
clean-EOF grace poll was dead code: the EOF exit path received from
`sigCh` directly, but Go serves channel receivers FIFO and the
watcher goroutine has been parked on `sigCh` since session start, so
the poll could never win the receive.  A signal landing in the
post-drain window was recorded by the watcher and then ignored while
the process exited 0.  The `sigDone` channel of the first-round
design was likewise dead (its close is deferred, and the watcher
only exits via `os.Exit`, which never runs defers).

Re-fix (commit `7bc59597`, Go product `b3a359c8…`): the watcher
closes `sigRecorded` immediately after writing the control plane
(before the graceful delivery), and the EOF exit path waits on that
channel through the unit-tested `waitSignalRecorded` primitive for
the 25 ms grace window, then re-reads the recorded error.  Re-run
of the exact repro at 3/6/8/12/20/26 ms offsets: 72/72 non-zero
(pre-fix 103/112 exit zero).  The remaining residual — a signal the
runtime delivers after the grace window and before process exit —
is the sub-millisecond TOCTOU class the Rust implementation has
natively; documented in the code and matched by parity analysis.

Also folded into the round: the dead `_report_carries_argv` detector
removed from the kind gate (no callers after the unconditional argv
rule) and the SOW status update.

This round's Linux evidence and the Windows host run were
regenerated at the final identities (`de597e18…` rust, `b3a359c8…`
go Linux, `984d0e9d…` go Windows and `877824f0…` Windows rust at
`e13be7ea`); case statuses and oracle counts unchanged (38/38 both
languages, 14+24 both mixed directions, 16/16 crash positive, 0/16
negative, 8/8 resource, Windows 2/2, golden 55, sensitivity 14,
kind gate PASS with the argv-strip/relabel mutations FAIL — the
consistent argv clone in its own declared slot remains the gate's
documented one-report-fork residual, mitigated by battery reruns
and adversarial rounds, as caveated above).


### Milestone 4 (delivery step 5) — closure record (2026-09-06)

User decision (recorded above in the Status section): option 1A —
milestone 4 is closed; milestone 5 is NOT started.  This record is
the last repository commit before the external whole-milestone
control review at the exact revision below; the control review's
verdict is appended after it lands.

- Final revision: the HEAD carrying this closure record (the
  all-seven-PASS revision it extends is `cbb373bf`; pushed; working
  tree clean at record time).
- Internal gate: ALL SEVEN standing roles PASS the exact final
  revision (tester, operations, parity, portability, security,
  performance, glm-5.3-responses whole-milestone validator); every
  verdict starts with the reviewed HEAD and its delta evidence is in
  `.local/<role>/report*.md`.  The round restarted twice on verified
  findings (Go clean-EOF FIFO signal race; Rust eof-first contract
  gap) and both fixes are at the final product identities.
- Product identities (staged, SHASUMS-recorded): Rust `de597e18…`
  (linux) / `877824f0…` (windows), Go `b3a359c8…` (linux) /
  `984d0e9d…` (windows); workers and fixture unchanged
  (`cb9ad6cd…`, `202a83ac…`, `7c616793…`).
- Evidence: matrices 38/38 both languages, 14 executed + 24 skipped
  both mixed directions, crash positive 16/16 and negative 0/16,
  resource 8/8, golden 55 exchanges, sensitivity 14 modes, Windows
  housekeeping 2/2 (schema v3, 50-row removal logs, build
  provenance at `e13be7ea`); kind gate PASS on the genuine committed
  evidence with the argv-strip/pathless/relative forgery classes
  REJECTED.
- Disclosed residuals (accepted, recorded in code and records):
  1. A termination signal the runtime delivers after the shared
     25 ms EOF grace window and before process exit observes exit 0
     — the bounded TOCTOU class, now identical in both products.
  2. The kind gate's documented one-report-fork residual: a fully
     consistent single-report rewrite (shas, argv, paths) passes in
     its own declared slot; mitigated by the battery reruns and the
     adversarial rounds, not by the mechanical gate.
  3. The fixture-tool identity is a cross-report consistency anchor;
     the gate does not hash the fixture binary on disk (documented in
     the gate module).
  4. Go deflate workspace charge pinned to the go1.26.4 toolchain
     (`v4/go/internal/writer/metadata.go:31`, ~840 KiB vs ~1.08 MiB
     under go1.27) — flagged to the user; owned by SOW-0025/SOW-0030,
     no SOW-0028 product change.
- Deferred ledger (mapped): parse-error `message` text differences
  across products (P3, diagnostic-only, recorded; code/id/exit
  behavior identical); the busy/reject split under sustained pressure
  (D2-A: scheduler-dependent, non-contractual, diagnostic);
  milestone 5 (benchmark harness and ceilings) NOT started per the
  user decision, with the ~25 ms clean-EOF session floor recorded in
  `resource-record.md` for its latency methodology; engine
  performance residuals owned by pending SOW-0030; SOW-0017 (snapshot
  signing) stays paused; SOW-0029 (WebSocket/daemon) stays pending.
- Closure statement: milestone 4 (delivery step 5) is CLOSED.  The
  next committed record is the external whole-milestone control
  review verdict at the exact revision of this record; the internal
  process claims no further repository change before that verdict.

### Wave 11 (2026-09-06) — external whole-milestone control review FAIL and repair

The external whole-milestone control review of the milestone-4
closure revision `155459b0` returned FAIL with one production
shutdown defect and seven qualification-framework defects.  All eight
findings were independently verified (finding 1 reproduced against
both canonical binaries with a full diagnostic pipe; findings 2-8
reproduced through the review's probes against the committed code).
No product-design decision was required: user decision 1A already
governs (reopen M4 acceptance, keep M5 unstarted), and every repair
is a bounded SOW-0028 qualification or durability defect inside the
approved scope.  The milestone-4 closure record above is reopened by
this section.

#### Findings (control review at `155459b0`)

1. P1 — both products hang at shutdown when the diagnostic pipe is
   full: the watchdog writes the forced-exit message synchronously
   to stderr before `os.Exit(1)` / `process::exit(1)` (Go
   `v4/go/internal/cli/rpc/session.go:294-296`, Rust
   `v4/rust/iprange-cli/src/rpc/session.rs:1071-1074`, and Rust's
   wedged-channel fatal diagnostic at `session.rs:658`).  With an
   8192-byte full stderr both remained alive past 2.5 s and exited
   only after the pipe was drained, violating the recorded
   termination bound.
2. P2 — the threaded (Windows) client can hang in `close()` after a
   write timeout: the timed-out writer still holds the buffered
   stdin lock while `close()` closes that same buffered stream
   (`v4/cli/run.py` `_write_bounded_thread` and `close`).
3. P2 — response ingestion allows invalid qualification results:
   `read_responses` is called without size/envelope/duplicate-id
   enforcement (`v4/cli/resource_harness.py:867`), so a response
   missing `jsonrpc`, a 2.1 MB response, and duplicate response ids
   were all accepted by proof d; the shared client's unterminated
   output accumulation (`v4/cli/run.py` no-deadline readline and
   threaded `readline`) is unbounded.
4. P2 — cleanup hides a failed EOF shutdown: `v4/cli/run.py`
   waits 0.2 s, kills a stalled EOF peer, and returns success, so a
   peer that did not finish its normal shutdown is reported clean
   (observed returncode -9 accepted).
5. P2 — the kind gate ignores exact operation ordinals: creation
   and opening refs were checked only for list bounds and method
   capability, so crediting a failed earlier operation, a ref that
   contradicted the recorded open ordinal, a ref that contradicted
   `created_ordinals`, and omitting `created_ordinals` entirely all
   passed (`v4/cli/check_kind_coverage.py:1495,1564,1580`).
6. P2 — command and actor executable identities can disagree: the
   command binding and the actor binding were not joined, so a
   report with two Go records passed when the command selected one
   while the actor argv/sha named the other, and a contradictory
   matrix fixture hash also passed (`check_kind_coverage.py:777,
   :889, :963`).
7. P2 — per-actor capability tables reject valid explicit-actor
   workflows: the role-inverted `database.metadata` case (consumer
   creates the database and replaces metadata, producer reads) runs
   correctly on both real products in both directions, but the gate
   reported five capability errors (`check_kind_coverage.py:282,
   :328`).
8. P2 — the Windows row comparison omits the top-level removal
   ordinal: changing an envelope-row ordinal 1 to 42 passed both
   checks (`windows_housekeeping_harness.py:1285,1298`); the ordinal
   is parsed and passed to removal by
   `v4/go/internal/cli/handlers/maintenance.go:1446,1485,1499`.

#### Repairs (commit `b7be670b`)

- Finding 1 (both products): the forced-exit diagnostics are now
  best-effort.  Go emits the message from a detached goroutine and
  `os.Exit(1)` runs after a bounded 50 ms grace
  (`v4/go/internal/cli/rpc/session.go`, const
  `forceExitDiagnosticGrace`); Rust spawns a detached thread for the
  same write and exits after 50 ms, and the wedged-channel fatal
  diagnostic (`session.rs`) uses the same pattern.  A full
  diagnostic pipe can no longer block the forced exit; measured:
  both products self-terminate within ~1.05 s with a full 8192-byte
  stderr, exit 1 (pre-fix both hung past 2.5 s).  Committed
  regressions: Go
  `session_signal_unix_test.go` → `TestTerminationSignalFullStderrForcedExit`
  (raw-pipe fill; the runtime poller must be bypassed with raw
  syscalls) and Rust `tests/termination_signals.rs` →
  `full_stderr_signal_forces_nonzero_exit` (both green).
- Finding 2 (`v4/cli/run.py`): `close()` tracks deadline-thread
  workers; for a poisoned threaded service it reaps the child before
  touching the buffered wrappers whose locks a blocked worker may
  still hold, then joins the workers and closes wrappers under
  bounds.
- Finding 3 (`v4/cli/run.py`, `v4/cli/resource_harness.py`): every
  response read path enforces a per-frame and an accumulated byte
  ceiling, the shared envelope validator (`jsonrpc 2.0`, exactly one
  of result/error, well-formed error), the 65,000-byte
  response-object ceiling, and unique ids; `read_responses` applies
  the checks by default at every proof; the no-deadline readline is
  bounded and an oversized frame poisons the service.
- Finding 4: `close(allow_forced=False)` reports a peer that this
  close had to force-terminate as a qualification failure;
  deliberate-stall self-test controls pass `allow_forced=True`; a
  peer already terminated externally (crash scenario process groups)
  is reaped silently.
- Finding 5: crash creation/opening refs must match the scenario's
  recorded `created_ordinals` / `live_reader_opens` /
  `adapter_output_opens` / `consumer_main_open_ordinal` exactly; a
  report that omits `created_ordinals` fails.
- Finding 6: each matrix actor must be the exact executable the
  recorded command selected for its language; the matrix
  `fixture_tool` sha256 must equal the crash report's recorded
  identity for the same path.
- Finding 7: capability maps are per-kind method sets independent of
  actor role (creation, transformation, and reading may run on
  either actor); the actor remains the execution and language
  attribution authority.
- Finding 8: the Windows housekeeping checks compare the top-level
  row ordinal against the synthesized facts, the nested artifact
  ordinal, and across listings; the self-test gains mutation M7.

#### Validation (wave 11)

- Go: `go test ./...` (go1.26.4) PASS, including the new full-stderr
  signal test; Rust: `cargo test -p iprange-cli` PASS including the
  new full-stderr process test.
- Product identities (release, staged, recorded): Rust `eb08c3d4…`
  (linux) and `19474f14…` (windows), Go `c0204ade…` (linux) and
  `fce7acf5…` (windows); worker and fixture identities unchanged
  (`cb9ad6cd…`, `202a83ac…`, `7c616793…`); the Linux evidence is
  regenerated at the new identities in `v4/cli/evidence/`.
- Full battery at the new identity (sequential, one host): matrices
  rust 38/38, go 38/38, rust_to_go 14+24, go_to_rust 14+24; crash
  positive 16/16 both directions, negative 0/16 (expected negative);
  resource 8/8; golden 55 exchanges; sensitivity 14 modes; kind gate
  PASS on the regenerated evidence.
- Kind-gate adversarial probes (the control review's six forgery
  classes) now REJECT on genuine evidence: failed main-open ordinal,
  failed sidecar-open ordinal, wrong creation ordinal, missing
  `created_ordinals`, alternate-actor-binary, and fixture-hash
  contradiction; the actor-swapped `database.metadata` ledger
  (finding 7 positive control) is ACCEPTED; controls 1-42 of the
  gate `--self-test` pass.
- Windows housekeeping on the authorized validation host at
  `b7be670b` (go1.26.5 / rustc 1.97.1, clean tracked tree): 2/2
  PASS with the deadline-bounded client; report schema v3 records
  `fce7acf5…` (go) / `19474f14…` (rust), the exact 50-row removal
  logs, and build provenance.
- Framework self-tests: resource_harness `--self-test` PASS
  (proof-b envelope/oversize/hang controls, deadline controls),
  windows_housekeeping_harness `--self-test` PASS (M1-M7),
  `run.py` protocol self-tests PASS.

#### Sensitive-data and artifact gate (wave 11)

No personal paths, host aliases, or secrets in the regenerated
evidence or this SOW; the committed evidence uses the recorded staged
binary paths and the neutral "authorized Windows validation host"
wording.  Specs: no spec amendment (durability and qualification
behavior only; the transport contract is unchanged).  End-user docs:
none affected.  Evidence: `v4/cli/evidence/*.json` regenerated at
the new identities and `README.md` / `resource-record.md` updated
with the wave narrative.  Project skills (project-final-review,
project-v4-rust): unchanged.  SOW lifecycle: this section reopens
the milestone-4 acceptance and re-closes it at the final wave-11
revision after the internal role round; the role verdicts and any
re-repair are recorded after this section.

Closure statement: milestone 4 acceptance is REOPENED by this
section and re-CLOSED once the internal role round passes the exact
final revision of this wave; milestone 5 (delivery step 6) remains
unstarted per user decision 1A.  The role round's verdicts are
recorded in the next section.

### Wave 12 (2026-09-06) — internal role round FAIL (graceful fatal path and harness ceilings) and repair

The wave-11 closure was submitted to the seven standing role
reviewers at HEAD `41f66ccf`; all seven returned.  Three roles
returned PASS (glm-5.3 whole-milestone validator, parity, security);
four roles returned FAIL (performance, operations, portability,
tester) with one P1 closed-loop defect, one new P1 framing defect,
two P2 harness ceilings, and three P3 repairs:

1. **P1 — graceful fatal path still blocked on a full diagnostic
   pipe (both products, performance + operations + tester roles).**
   `rpc.Run` (Go `v4/go/internal/cli/rpc/rpc.go`) and `rpc::run`
   (Rust `v4/rust/iprange-cli/src/rpc/mod.rs`) wrote the
   session-failure diagnostic synchronously on the main thread
   before exiting.  A session failure (broken stdout, framing
   failure) with a full undrained stderr pipe blocked the exit
   forever: the wave-11 repair covered only the signal/wedged fatal
   paths.  Reproduced at `41f66ccf` by the lead: both products
   stayed alive past 4 s (killed), while a readable-stderr control
   exited in ~3 ms — isolating the blocked diagnostic write.  The
   spec (`.agents/sow/specs/iprange-jsonrpc-v1.md` shutdown
   section) requires unrecoverable stdout failure to exit non-zero.
   Repair: the diagnostic is now emitted from a detached
   goroutine/thread and the exit is bounded by the same 50 ms grace
   the forced signal path uses (Go `forceExitDiagnosticGrace`, Rust
   50 ms sleep).  Regression tests: Go
   `TestGracefulFatalFullStderrForcedExit` (helper runs the real
   `Run()` with stdout=/dev/full and a full stderr pipe; exits 1
   within 3 s) plus `TestGracefulFatalDiagnosticStillReported`
   (drained-stderr control: the message still lands); Rust
   `graceful_fatal_full_stderr_exits_nonzero` (stdout whose read end
   is closed) plus `graceful_fatal_diagnostic_still_reported`.
   Measured: both products exit 1 in ~0.06 s on this path (was an
   indefinite hang); the signal-path forced exit remains ~1.05-1.07
   s (1 s watchdog + 50 ms grace), and the wave-11 ~1.05 s
   full-stderr claim is scoped to the signal path in the records.

2. **P1 — unterminated over-limit frame wedged both sessions
   indefinitely (tester role).**  The frame readers
   (`v4/go/internal/cli/rpc/framing.go`, Rust
   `v4/rust/iprange-cli/src/rpc/framing.rs`) waited for LF or EOF
   after the ceiling was exceeded (discard-the-rest).  A peer
   streaming more than 1 MiB without LF and holding stdin open
   pinned the session forever: no -32001, no exit (memory bounded,
   time unbounded).  The spec (`.agents/sow/specs/iprange-jsonrpc-v1.md`
   framing section) requires a frame over the limit to produce
   -32001 with id null and then close; the frame is invalid once
   the accumulated bytes exceed the ceiling even if never
   terminated.  Repair: both readers report `FrameTooLarge`
   immediately at the byte that makes the payload definitely over
   the ceiling (after the CRLF CR-strip allowance); the session's
   existing -32001 + close path then runs, and the remaining bytes
   are dropped by process close (never parsed — the spec's
   shutdown-discard rule).  Regression tests: Go
   `TestOversizedUnterminatedFrameAnswersAndExits`, Rust
   `oversized_unterminated_frame_answers_and_exits` (LIMIT+2 bytes,
   no LF, stdin held open: exactly one null-id -32001 response and
   exit 1 within 3 s).  Consequence for proof b: the product now
   closes its stdin side before the harness finishes writing a
   >1 MiB frame, so `resource_harness.py` proof b accepts the
   resulting EPIPE on the oversized-frame write — the remainder of
   the frame and the sentinel are dropped by the closed pipe, which
   is exactly the contract; the proof still verifies the single
   -32001 response, stdout EOF, zero further bytes and the non-zero
   exit.

3. **P2 — `drain_stdout` had no accumulated-byte ceiling
   (operations role).**  `v4/cli/resource_harness.py` `drain_stdout`
   accumulated every byte until its 15 s deadline; a flooding peer
   that never reached EOF could accumulate gigabytes (host OOM).
   Repair: the drain is now byte-capped at the residue already
   retained by the caller's last `read_responses` plus one output
   frame; overflow raises `ResourceFailure` (the same ceiling
   pattern `read_responses` uses).  Probe with a 3 MB flooding stub:
   rejected at the ceiling in <0.01 s.

4. **P2 — the duplicate-id rejection had no detecting control
   (portability role).**  The check exists but a regression would
   pass silently.  Repair: `resource_harness.py --self-test` now
   runs a duplicate-id negative control (two identical-id responses
   to one expect=2 read must raise "duplicate response id").

5. **P3 — `run.py` threaded-poisoned `close()` called `kill()`
   without a `poll()` guard (portability role).**  A poisoned peer
   that self-exited before `close()` made `kill()` raise and mask
   the original failure.  Repair: kill only when `poll()` is None;
   the forced flag is set only when this call actually killed.

6. **P3 — `run.py` `describe_capabilities` swallowed the
   forced-close `AssertionError` as "legacy-only" (tester note).**
   Repair: a force-terminated probe re-raises instead of
   misclassifying a stalled JSON-RPC service as legacy-only.

7. **P3 — `windows_housekeeping_harness.py` pair-row check had
   mangled spacing** (readability only).

#### Validated fixes, before the role-round delta

- Go: `go test ./...` (go1.26.4) PASS, including the four new
  wave-12 regression tests; Rust: `cargo test` (workspace) PASS,
  including the three new wave-12 process tests.
- Product identities (release, staged, recorded), Linux:
  Rust `f6926c1c…`, Go `7f88bb7c…`; worker and fixture identities
  unchanged (`cb9ad6cd…`, `202a83ac…`, `7c616793…`); the Linux
  evidence is regenerated at the new identities in
  `v4/cli/evidence/`.
- Full battery at the new identity (sequential, one host, staged
  under `/tmp/qualsvc/ev18/`, every matrix invoked with both
  product binaries in argv per the command-to-actor join):
  matrices rust 38/38, go 38/38, rust_to_go 14+24, go_to_rust
  14+24; crash positive 16/16 both directions, /bin/false negative
  control 16/16 failed (the sensitivity artifact is never a
  kind-gate source; the gate consumes the positive crash report
  only, per `v4/cli/README.md`); resource 8/8 (proof b now
  tolerates the product's spec-conformant early stdin close);
  golden 55 exchanges; sensitivity 14 modes; kind gate PASS on the
  regenerated evidence.
- Framework self-tests: resource_harness `--self-test` PASS (now
  including the duplicate-id control), windows_housekeeping_harness
  `--self-test` PASS (M1-M7), kind-gate `--self-test` controls
  1-42 PASS, run.py protocol self-tests PASS (run at every matrix
  start).
- Windows housekeeping on the authorized validation host at the
  wave-12 product-source revision `40c1c046` (go1.26.5 / rustc
  1.97.1, clean archived tree, staged under
  `C:/Temp/qualsvc-win/ev18/`): 2/2 PASS with the deadline-bounded
  client under the native Windows Python 3.14.6; report schema v3
  records Go `42173bb7…` / Rust `6dcf2cb2…`, the exact 50-row
  removal logs (identical bytes across both products), the
  two-row deterministic pair listing with ordinal 1 equal across
  the synthesized facts, the nested artifact, and the used row, and
  build provenance at `40c1c046` with a clean tree.

#### Sensitive-data and artifact gate (wave 12)

No personal paths, host aliases, or secrets in the regenerated
evidence or in this SOW; the committed evidence uses the staged
binary paths under `/tmp/qualsvc/ev18/` and the neutral "authorized
Windows validation host" wording.  Specs: no spec amendment (the
transport contract is unchanged; the framing reader now simply
enforces the already-specified over-limit close without waiting for
a terminator that may never arrive).  End-user docs: none affected.
Evidence: `v4/cli/evidence/*.json` regenerated at the new
identities and `README.md` / `resource-record.md` updated with the
wave narrative.  Project skills (project-final-review,
project-v4-rust): unchanged.  SOW lifecycle: this section records
the role round and the repair; the role-round delta verdicts at the
final wave-12 revision are recorded in the next section.

Closure statement: milestone 4 acceptance was REOPENED by the
wave-11 section pending the internal role round; the role round
failed the wave-11 revision, this section repairs every verified
finding, and milestone 4 is re-CLOSED once the role-round delta
passes the exact final revision of this wave.  Milestone 5
(delivery step 6) remains unstarted per user decision 1A.

### Wave 13 (2026-09-06) — role-round delta FAIL (EOF framing boundary and drain control) and repair

The wave-12 closure was submitted to the seven standing role
reviewers at HEAD `21b62a66`.  Five roles returned before the tree
changed (the remaining two were interrupted): tester and security
returned PASS; parity returned FAIL with 1 P1, operations returned
FAIL with 1 P2 (the same boundary class, independently confirmed),
and performance returned FAIL with 1 P2:

1. **P1 — Rust exited 0 on a framing failure at the LIMIT+1-at-EOF
   boundary; Go exited 1 (parity role, independently confirmed by
   operations; reproduced by the lead on the staged binaries).**
   `v4/rust/iprange-cli/src/rpc/framing.rs` — the `read_line` EOF
   arm returned the accumulated buffer without the ceiling check
   that Go's EOF arm has (`v4/go/internal/cli/rpc/framing.go`).  A
   final unterminated frame of exactly `INPUT_FRAME_LIMIT+1` bytes
   (no LF), with or without a trailing CR, is over the ceiling (at
   EOF no terminator exists to strip a CR for); both products emit
   the byte-identical -32001 (id null) wire response, but Go exits 1
   through the framing-failure path while Rust answered at the
   schema layer, continued, and then exited 0 through the clean-EOF
   path — an exit-status consumer could mistake a framing failure
   for success, violating the spec's framing-failure non-zero exit
   and the Go/Rust parity claim.  Repair: the Rust EOF arm now
   checks `self.buf.len() > INPUT_FRAME_LIMIT` exactly like Go and
   reports `FrameTooLarge`.  Regression tests: Rust
   `overflow_at_eof_exits_nonzero` (plain and CR-tail, two trials
   each; one null-id -32001 and exit 1 within 3 s), Go
   `TestOversizedEOFExitsNonZero` (pins the same shape in both
   variants).  Measured post-fix: both products exit 1 in ~0.05 s
   on both variants.

2. **P2 — the drain_stdout byte-cap repair had no committed
   detecting control (performance role).**  The wave-12 cap exists,
   but `drain_stdout` is called only by proof b whose real product
   reaches EOF in milliseconds, so no committed test could detect a
   cap regression.  Repair: `resource_harness.py --self-test` now
   runs a drain-flood control (a peer flooding stdout without EOF;
   must raise `ResourceFailure` at the accumulated ceiling within a
   bounded window).  Measured: rejected at the ceiling in ~0.002 s.

#### Validated fixes, before the role-round delta (wave 13)

- Go: `go test ./...` (go1.26.4) PASS, including the new
  `TestOversizedEOFExitsNonZero`; Rust: `cargo test` (workspace)
  PASS, including the new `overflow_at_eof_exits_nonzero`.
- Product identity (release, staged, recorded), Linux: Rust
  `24733db0…` (changed by this wave's framing EOF-arm check); Go
  `7f88bb7c…` and the worker and fixture identities are unchanged
  (`cb9ad6cd…`, `202a83ac…`, `7c616793…`); the Linux evidence is
  regenerated at the new identity in `v4/cli/evidence/`.
- Full battery at the new identity (sequential, one host, staged
  under `/tmp/qualsvc/ev19/`): matrices rust 38/38, go 38/38,
  rust_to_go 14+24, go_to_rust 14+24; crash positive 16/16 both
  directions, /bin/false negative control 16/16 failed; resource
  8/8; golden 55 exchanges; sensitivity 14 modes; kind gate PASS on
  the regenerated evidence.
- Framework self-tests: resource_harness `--self-test` PASS (now
  including the read, write, duplicate-id, and drain-flood
  controls), windows_housekeeping_harness `--self-test` PASS
  (M1-M7), kind-gate `--self-test` controls 1-42 PASS, run.py
  protocol self-tests PASS.
- Windows housekeeping on the authorized validation host at the
  wave-13 product-source revision `5346f716` (go1.26.5 / rustc
  1.97.1, clean archived tree, staged under
  `C:/Temp/qualsvc-win/ev19/`): 2/2 PASS with the deadline-bounded
  client under the native Windows Python 3.14.6; report schema v3
  records Go `42173bb7…` (unchanged) / Rust `de902a73…` (carries
  the EOF ceiling check), the exact 50-row removal logs
  (byte-identical across both products), the two-row deterministic
  pair listing with ordinal 1 equal across the synthesized facts,
  the nested artifact, and the used row, and build provenance at
  `5346f716` with a clean tree.

#### Sensitive-data and artifact gate (wave 13)

No personal paths, host aliases, or secrets in the regenerated
evidence or in this SOW.  Specs: no spec amendment (the framing
reader now enforces the already-specified ceiling at EOF; the
transport contract is unchanged).  End-user docs: none affected.
Evidence: `v4/cli/evidence/*.json` regenerated at the new Rust
identity and `resource-record.md` / `evidence/README.md` updated.
Project skills: unchanged.  SOW lifecycle: this section records the
delta findings and repair; the role-round verdicts at the final
wave-13 revision are recorded in the next section.

Closure statement: milestone 4 acceptance remains REOPENED pending
the role-round delta at the exact final revision of this wave;
milestone 5 (delivery step 6) remains unstarted per user decision
1A.

### Role round verdicts — wave 13 (2026-09-06): all seven roles PASS

At HEAD `b947d8a6` (the exact final wave-13 revision), all seven
standing role reviewers returned PASS with no P0-P2 findings:

- tester: PASS (both wave-13 repairs verified at the binary level;
  the round-2 duplicate-`.stdin()` cosmetic note retracted as a
  false positive from a display artifact);
- operations: PASS (EOF boundary re-probed plain and CR-tail: both
  products -32001 + exit 1; boundary sweep incl. legal
  payload-LIMIT controls green; drain-flood control verified);
- parity: PASS (full boundary matrix at the new identities:
  LIMIT+1-at-EOF, CR-tail, LF-terminated, held-open, and legal
  LIMIT/CRLF controls all Go/Rust identical including stderr exit
  parity; drain-flood control verified);
- portability: PASS (regression tests green, same-failure search
  found the two framing readers arm-for-arm identical; the round-2
  "stuck child" observation resolved as a Python-side
  `Popen.poll()` reaping artifact, not product behavior);
- security: PASS (all classes re-probed, evidence genuine at
  `24733db0`/`7f88bb7c` and Windows `de902a73`/`42173bb7`,
  records truthful, secrets clean);
- performance: PASS (drain cap control verified at the exact
  ceiling; no request-path impact from the delta);
- glm-5.3 whole-milestone validator: PASS (identity and battery
  counts verified; no later commit than `b947d8a6`).

Non-blocking notes recorded with the round: the viewer
observability flake seen once by the glm-5.3 role in round 2
(Rust `graceful_fatal_full_stderr_exits_nonzero`) never recurred
across ~43 full-suite runs and ~500 direct probes and is tracked
here; the exact-LIMIT-at-EOF accepted boundary is pinned by
identical `>` ceiling code in both readers rather than a dedicated
test (covered by the parity boundary sweep).

Milestone-4 closure (delivery step 5): milestone 4 is now
RE-CLOSED at `b947d8a6` under the wave-9 accepted decision set:
functional parity and qualification PASS for both product
implementations; the ≤1.3x performance requirement was FAILED and
is not waived; engine-level performance residuals remain owned by
SOW-0030; this SOW-0028 remains open for delivery step 6
(dual-language CLI conformance benchmarks, milestone 5) which stays
unstarted per user decision 1A.

### Wave 14 (2026-09-07) — external whole-milestone control review FAIL (held-over-limit framing, Windows basename round-trip, watchdog coverage) and repair

The external whole-milestone control review of the wave-13 revision
(`b947d8a6`) returned NEEDS CHANGES.  The findings and their
disposition:

1. P1 — a held-open frame of exactly LIMIT+1 bytes whose last byte
   is not the CR of a CRLF terminator wedged both products: the
   readers retained the bytes and awaited another one forever, so a
   peer holding stdin open never saw the -32001 + close (Go
   `framing.go`, Rust `framing.rs`).  The one-extra-byte allowance
   now applies only when the last byte is CR; any other LIMIT+1-th
   byte makes the payload definitively over the ceiling and is
   reported immediately, without waiting for the terminator or EOF.
   Regression tests: Go `TestOversizedHeldNonCRFrameAnswersAndExits`,
   Rust `oversized_held_non_cr_frame_answers_and_exits` (held-open
   stdin, -32001 + exit 1); unit tests pin the non-CR immediate
   shape (`held_limit_plus_one_non_cr_is_immediate`) and the CR-tail
   /LF legal boundary (`held_limit_plus_one_cr_tail_resolves_on_lf`).
   Measured on the held shape: both products answer -32001
   within ~0.01 s (Go ~0.007 s, Rust ~0.002 s) and exit 1 in
   ~0.07 s (the documented fatal-exit grace).

2. P1 — the Rust Windows `main_basename` round-trip was broken:
   `LocalBasename` stores UTF-16LE units (encoding 2) on Windows but
   the wire rendering passed the raw units through UTF-8 lossy,
   emitting NUL-interleaved mojibake that the resolvers rejected
   (`decode_main_basename` compares against the clean destination
   basename).  `create_result`, `commit_cleanup_artifact`, and the
   live transition now render `LocalBasename` through an
   encoding-aware decoder (encoding 2 -> UTF-16LE text, else UTF-8
   lossy) in `handlers/lifecycle.rs` and `handlers/live.rs`.  The
   Go product always stores encoding-1 bytes and was unaffected.
   Regression tests: the encoding-2 decode path is exercised on
   Linux with raw UTF-16LE units (`utf16le_encoding_renders_to_clean_text`,
   lossy odd-tail, and encoding-1 from_path).

3. P1 — the full-stderr signal tests signalled an idle session,
   which exits through the graceful path before the watchdog fires;
   the wedged-session force-exit that the tests claim was not
   exercised.  The Go helper gained a `full-stderr-wedged` mode and
   the Rust spawn now wedges the session (fill stdout, never
   drained) before signalling, so the process-lifetime watchdog's
   detached-diagnostic path is genuinely tested.

4. P2 — the Rust full-stderr fixture read 4096 bytes from a
   one-byte buffer and treated any write error as full-pipe proof.
   It now fills from a real buffer and requires EAGAIN at the
   terminal error.

5. P2 — the kind gate's fixture-identity anchor was bypassable: a
   fixture path recorded without a sha256 skipped the matrix
   comparison, and two crash reports naming the same path with
   different hashes silently overwrote.  The gate now fails both
   shapes and adds the corresponding records checks.

6. P2 — resource proofs a and d ignored stdout residue after the
   expected responses: a stray duplicate or malformed trailing
   frame could pass.  Both proofs now drain stdout to EOF and
   require zero trailing bytes; a new `resource_harness.py
   --self-test` control pins the detection.

7. P2 — the Go signal tests raced `Wait()` against `StdoutPipe`
   reads (the StdoutPipe contract forbids it), and the full-stderr
   tests closed raw pipe fds behind an `*os.File` whose finalizer
   could later close a reused fd number, causing intermittent EBADF
   flakes across the suite under load (reproduced at the wave-13
   revision).  The oversized-frame tests now read the response
   before starting `Wait`, and both full-stderr tests close through
   the `*os.File` rather than a raw `syscall.Close`.

8. P3 — `v4/cli/README.md` still said 53 golden exchanges; the
   corpus has 55.  Fixed.

9. P3 — historical commit messages contain AI tool names.  History
   is not rewritten (needs user approval); noted here for the
   record.

#### Validated fixes, before the wave-14 role-round delta

- Go: `go vet` clean, `gofmt` clean, `go test ./...` PASS at the
  wave-14 revision, including the new held-open non-CR test and the
  wedged full-stderr signal test; the full-stderr suite ran three
  consecutive clean `-count=2` full-suite passes with the fd
  finalizer repair (previously ~50% flaky under load).
- Rust: `cargo test` workspace PASS (880 suites), including the
  framing unit tests, the new held-open non-CR integration test, the
  wedged full-stderr test, and the basename decode tests.
- Framework self-tests: `resource_harness.py --self-test` PASS
  (read, write, duplicate-id, drain-flood, and the new
  trailing-residue controls); kind-gate `--self-test` PASS.
- Product identity (release, staged, recorded), Linux: Rust
  `6ab63dfd…` (framing + basename rendering), Go `d228ebe5…`
  (framing), worker `202a83ac…` (go) / `cb9ad6cd…` (rust, unchanged),
  fixture `d615488f…` (fresh canonical release build at the wave-14
  revision; the earlier recorded fixture identity predated the
  current release toolchain).  HEAD `e272c990` reproduces every
  staged identity byte-for-byte.
- Full battery at the wave-14 revision (sequential, one host,
  staged under `/tmp/qualsvc/ev21/`): matrices rust 38/38, go
  38/38, rust_to_go 14+24, go_to_rust 14+24; crash positive 16/16
  both directions, /bin/false negative control 16/16 failed;
  resource 8/8; golden 55 exchanges; sensitivity 14 modes; kind gate
  PASS on the regenerated evidence.
- Windows housekeeping on the authorized Windows validation host at
  the wave-14 revision `e272c990` (go1.26.5 / rustc 1.97.1, clean
  archived tree, staged under `C:/Temp/qualsvc-win/ev20/`, native
  Python 3.14.6): 2/2 PASS; the recorded `database.create`
  `main_basename` is clean text from BOTH products now (Rust
  previously recorded NUL-interleaved units); the maintenance-list
  artifact basenames keep the documented opaque UTF-16LE-per-byte
  wire form and its exact 50-row / ordinal validation.

#### Sensitive-data and artifact gate (wave 14)

No personal paths, host aliases, or secrets in the regenerated
evidence or in this SOW; evidence JSONs were scanned for personal
tokens.  Specs: the framing reader now enforces the
already-specified ceiling for the held-open shape (no ceiling
amendment); the `main_basename` wire semantics (decoded text on
create/transition results) and the maintenance artifact basename
opaque per-byte form are now pinned normatively in
`iprange-jsonrpc-v1.md`.  End-user docs: `v4/cli/README.md`
golden count corrected to 55.  Evidence:
`v4/cli/evidence/*.json` regenerated at the wave-14 revision with
`evidence/README.md` updated and its staging labels corrected to
the qualified wave-14 paths.  Framework records: wave-14 section
and Status appended; `v4/cli/resource-record.md` updated with the
wave-14 identities and the proofs a/d residue drain.


#### Wave-14 role-round delta (2026-09-07) — records and gate controls

The wave-14 role round (tester, operations, parity, portability,
security, performance, glm roles at HEAD `805eaf54`) returned three
verified findings from the performance role; all were verified by
the lead before fixing (file:line and fresh measurements below).

1. P2 — `v4/cli/resource-record.md` cited the wave-13 canonical
   Linux identity hashes (Go `7f88bb7c…`, Rust `24733db0…`) as
   "current" while HEAD evidence (`v4/cli/evidence/README.md`,
   `.local/shared/binaries/SHASUMS.txt`) records the wave-14
   identities (Go `d228ebe5…`, Rust `6ab63dfd…`).  The record now
   states the wave-14 identities and keeps the wave-13 EOF-ceiling
   and wave-14 repair history as dated facts.

2. P2 — the wave-14 fixture-identity rejections in
   `v4/cli/check_kind_coverage.py` (crash fixture path recorded
   without `fixture_tool_sha256`; two crash reports naming the same
   fixture path with different hashes) had no committed detecting
   controls: deleting the guards still passed `--self-test` and the
   genuine gate.  Two controls were added to `_self_test` (#42
   crash-fixture-missing-sha, #43 cross-crash fixture conflict);
   `--self-test` PASS and the genuine kind gate PASS on the
   committed evidence.

3. P3 — the wave-14 record's "both products exit 1 in ~0.02 s on
   the held shape" conflated the -32001 response with process exit.
   Fresh measurement at the staged wave-14 binaries: both products
   answer -32001 within ~0.01 s (Go ~0.007 s, Rust ~0.002 s) and
   exit 1 in ~0.07 s (the documented fatal-exit grace).  The SOW
   wording was corrected.

P3 dispositions from the same round (verified, fixed in the same
wave): the SOW "~0.02 s" wording (item 3 above); the wave-14
narrative in `v4/cli/evidence/README.md` referenced the stale
`ev18`/`ev19` staging paths instead of the qualified `ev21` paths
(corrected); the `main_basename` wire semantics and the
maintenance artifact basename opaque per-byte form were committed
to `iprange-jsonrpc-v1.md` (the wave-14 record previously called
the round-trip "documented" without a normative home);
`v4/cli/resource-record.md` now names the proofs a/d zero-residue
drain.  Role observations left unaddressed as out-of-scope or
pre-existing: Go's encoding-1-only basename stores (the Rust
encoding-2 defect has no Go mirror), Rust lossy-by-truncation for
an odd trailing UTF-16 unit (unit-tested by design), a test-only
bounded Wait/read race window, and the 505-byte LocalBasename
storage cap.

Validation at the fixed tree: kind-gate `--self-test` PASS (all
controls incl. the two new ones), genuine kind gate PASS,
held-shape probe PASS on both staged products (Go `d228ebe5…`,
Rust `6ab63dfd…`).

#### Role round verdicts — wave-14 delta (HEAD `3d090ccf`)

All seven roles PASS at `3d090ccf` (product source unchanged since
`e272c990`; the reviewed delta is `744d62d9` + `3d090ccf`):
tester, operations, parity, portability, security, performance,
and the glm-5.3-responses whole-milestone validator.  Each role
re-verified the two P2 repairs with its own adversarial probes:

- tester: the two new fixture-identity forgery classes and all
  prior forgery classes rejected; the actor-swapped positive
  control accepted; gate self-test and genuine gate PASS;
  held-shape probe matches the corrected record.
- operations: same-class stale-identity hunt clean; the spec pins
  match both products' handlers and the committed evidence.
- parity: self-test PASS; product and spec wire semantics
  consistent for both languages; no stale hashes remain live.
- portability: restored the pre-fix guard behavior in a sandbox
  copy and showed `--self-test` fails exactly at control #42, so
  the controls detect removal of the guard class.
- security: the missing-sha and cross-crash-conflict rejections
  were reproduced on the real committed evidence (not only
  synthetic reports); two identical crash reports are accepted
  (no false failure).
- performance: bounded probe at the staged wave-14 binaries —
  response within ~0.1 ms of the boundary byte, exit 1 at
  63.5-63.8 ms; the corrected timing record is truthful.
- glm: mutated the committed evidence in-sandbox; both new
  rejection shapes fail with the exact diagnostic; PASS.

Non-blocking P3 carry-overs recorded as out-of-scope or
pre-existing (no product action): Go encoding-1-only basename
stores, Rust odd-tail lossy-by-truncation (unit-tested by
design), the bounded test-only `Wait`/read race window, the
505-byte `LocalBasename` storage cap, synchronous startup stderr
diagnostics, and the signal-bounded over-limit close on a full
undrained stdout.  The closure records commit below adds the
`commit.cleanup` `main_basename` decoded-text rule to the spec
and records this PASS set; the roles and the external control
re-review that exact final revision.


### Wave 15 (2026-09-07) — external control turn-2 repairs and native-Windows verification

The wave-14 closure at `874d3566` was submitted to the external
whole-milestone control (session b5dd923d…).  Turn 2 returned NEEDS
CHANGES; every verified finding is repaired in commit `2ddeb751`:

1. **P1 — Windows identity kind mismatch.**  Rust
   `handlers/lifecycle_live.rs decode_file_identity` and Go
   `handlers/lifecycle_live.go decodeFileIdentity` hardcoded kind 1
   for the local file identity; the Windows SDK records kind 2, so
   unchanged create/transition facts failed
   `DirectoryIdentityMismatch`.  Repair: platform kind in both
   decoders, pinned by `decode_file_identity_kind_is_posix` /
   `decode_file_identity_kind_is_windows` (Rust) and
   `TestDecodeFileIdentityKind` (Go).
2. **P2 — basename round-trip test failed natively on Windows.**
   The POSIX raw-bytes rendering assertion was `#[test]`-only;
   split into `#[cfg(not(windows))]` and a Windows UTF-16LE twin in
   `handlers/lifecycle.rs`.
3. **P2 — housekeeping artifact basenames were lossy for
   UTF-16LE.**  Rust `basename()` used `String::from_utf8_lossy`
   (replacement characters for non-ASCII units) and Go
   `publication_evidence.go` used a raw string.  Repair:
   byte-preserving per-byte wire mapping
   (`char::from(byte)`, encoding 1 = raw UTF-8 bytes, encoding 2 =
   per-byte, rejecting values above 0xff) with decode-side
   `decode_artifact_basename` / `decodeArtifactBasename` in both
   products, wired for envelope/source/inert basenames; eight new
   round-trip and rejection tests across the two languages.
4. **P2 — proofs A/D discarded the drain EOF flag.**  A
   deadline-only drain could end without EOF and let late frames
   pass.  Repair: shared `require_clean_drain` (zero residue AND
   real EOF) used by proofs A/D; the new "drain-eof control"
   self-test keeps the peer alive and must fail.
5. **P2 — kind-gate matrix fixture metadata was not bound to the
   command-selected fixture.**  Repair: `matrix_evidence` returns
   the command fixture realpath and `assess()` fails on any report
   whose fixture metadata path differs; new control #44 (a
   doctored internally-consistent fixture-B crash report combined
   with a matrix claiming fixture B must fail).
6. **P2 — control #43 was only detective in genuine-first order.**
   Repair: both orders (`conflict-first` and `genuine-first`) must
   produce the "contradicts the earlier identity" diagnostic;
   removing the guard fails `--self-test`.
7. **P2 — resource harness A/D response correlation and framing
   were weak.**  Repair: `read_responses` rejects CRLF-terminated
   payloads, proof-b rejects raw CRLF lines, and `by_id` keys by
   the exact id type (numeric echo no longer satisfies a string
   id); busy/ok/export classification requires exact id matches.
8. **P3 — wave-13 Status recorded the pre-rebuild Windows Rust
   identity `6dcf2cb2…`; the committed wave-13 evidence records
   `de902a73…`.**  Corrected in `2ddeb751` (the wave-12 narrative
   block keeps its own dated record).

#### Native-Windows verification wave (final revision `e21784ce`)

- The Rust CLI suite is now fully green on the authorized Windows
  validation host: 711 tests across `iprange-livedb` and
  `iprange-cli`, 0 failures.  The canonical invocation
  `cargo test -p iprange-livedb -p iprange-cli` builds the
  version-matched validation worker that live-source identity
  inspection spawns; the earlier `--bin iprange` form failed on
  Windows (worker missing) and on Linux with a stale worker
  (version conflict).
- Product-visible fixes in this wave:
  - the live-source export failure for a missing worker previously
    surfaced as `I/O error: ... (os error 2)`; the spawn fallback
    now skips non-existent candidates and reports
    `SDK validation/recovery worker is unavailable`
    (`worker/client.rs`), matching the SDK `worker_availability`
    probe;
  - the immutable snapshot wire test asserts the shared
    no-artifact fact on both platforms and pins the documented
    per-platform housekeeping state (`crash_reappearance_possible`
    on Windows: the GC pair removal is not power-loss durable, a
    state both SDKs implement identically — Go
    `v4/go/internal/live/gc_resolver.go
    gcFinishHousekeeping`, Rust
    `publication/gc/resolver.rs finish_housekeeping`);
  - the legacy parse missing-file test is `#[cfg]`-split: POSIX
    pins the exact C error text, Windows asserts the stable
    path-identified contract with any system error code;
  - eleven test temp names replaced `SystemTime` debug output
    (invalid Windows path components) with epoch-nanos suffixes;
  - the publication-evidence test identity helper uses the
    platform kind.
- Battery at the final identities (staged in
  `.local/shared/binaries/SHASUMS.txt`): matrices rust 38/38,
  go 38/38, rust_to_go 14 PASS + 24 legitimate skips, go_to_rust
  14 + 24; crash positive 16/16 both directions; /bin/false
  negative control failed as designed (rc 1); resource proofs
  8/8; harness self-tests PASS (resource, kind-gate controls
  1-44, sensitivity 14); kind gate PASS on the regenerated
  evidence; golden 55 exchanges / 38 case files.
- Windows housekeeping on the authorized validation host at
  `e21784ce` (native Windows Python 3.14.6): 2/2 PASS; report
  `v4/cli/evidence/windows-housekeeping.json` records Go
  `eec23536…` and Rust `dd2d0668…` with build provenance
  (go1.26.5 windows/amd64, rustc 1.97.1, clean tree).
- Final Linux identities: Go product `a6148994…`, Go worker
  `8fa44afa…`, Rust product `15a6ce76…`, Rust worker
  `9fd36146…`, fixture `6c2c56b9…`; `v4/cli/evidence/*` and
  `evidence/README.md` regenerated at these identities.

#### Sensitive-data and artifact gate (wave 15)

No personal paths or secrets: the regenerated evidence records
staged binary paths and the neutral "authorized Windows validation
host" wording (`C:/Temp/...` staging paths are non-personal).
Specs: no spec amendment (worker availability and housekeeping
state semantics are already recorded SDK behavior; the per-platform
housekeeping pin is now pinned by tests).  End-user docs: none
affected (CLI behavior unchanged; the improved missing-worker
diagnostic is an error-message improvement).  Evidence:
`v4/cli/evidence/*.json` regenerated and README updated.  Project
skills (project-final-review, project-v4-rust): unchanged.  SOW
lifecycle: this section records the wave; the role-round delta
verdicts at `e21784ce` are recorded below.

#### Role-round delta (wave 15) at `ac7291a1` — verified findings and repairs at `13a1982e`

The wave-15 evidence revision `ac7291a1` was submitted to the seven
standing role reviewers; tester, performance, and portability
returned FAIL with four verified findings (the other four roles were
interrupted with the verified list and reported no new distinct
issues):

1. **P2 — the Rust artifact-basename renderer was encoding-unaware
   (tester, performance, portability independently).**
   `lifecycle.rs basename()` mapped every stored byte to U+00xx for
   all encodings, while the encoding-1 decoders
   (`decode_artifact_basename` / `decodeArtifactBasename`) treat the
   wire as the text's UTF-8 bytes; for stored bytes above 0x7f the
   render/decoder pair was not an inverse (each U+00xx character
   re-encoded as two UTF-8 bytes), and Go rendered the same
   encoding-1 fact as raw text, so the products' wire output
   diverged for this class.  Repair: `basename(bytes, encoding)`
   now renders encoding 1 as the raw UTF-8 text and encoding 2 as
   the per-byte projection at all four call sites (housekeeping
   rows, maintenance/algebra/publish private-output attempts); new
   round-trip tests compose render and decoder for non-ASCII names
   under both encodings, and the POSIX test pins raw-text rendering.
2. **P2 — the Go validation-worker spawn still surfaced a raw
   file-not-found I/O error when no worker candidate exists
   (portability), while Rust reports the worker unavailable.**
   Repair: `worker/client.go SpawnWorker` skips on-disk candidates
   that do not exist and returns the OS-unsupported worker-unavailable
   class when none remains, matching the Rust fallback and the
   `worker_availability` probe; new regression test
   `TestSpawnWorkerUnavailableWhenAllCandidatesMissing`.
3. **P2 — the resource harness CRLF rejection and exact-type
   response-id correlation had no committed detecting control
   (tester).**  Removing either guard still passed `--self-test`.
   Repair: two new self-test controls — a CRLF-terminated response
   stub must fail `read_responses`, and a numeric response id must
   not satisfy the string id lookup of the proof classification;
   both print result lines, and the drain-eof control now prints
   too.
4. **P2 — `resource-record.md` still carried the wave-14
   identities (portability).**  Repair: refreshed to the wave-15
   identities (Go `9e78de86…`, Rust `73cb0626…` at `13a1982e`).

Re-qualification at `13a1982e`: Rust workspace and Go suites PASS;
full battery PASS at the new staged identities (matrices 38/38
single and 14+24 mixed; crash 16/16 both directions; resource 8/8;
kind gate PASS on the regenerated evidence; golden 55; sensitivity
14; harness self-tests PASS including the two new controls);
Windows housekeeping 2/2 on the authorized Windows validation host
(Go `857b84af…`, Rust `9f6107ae…`, native Python 3.14.6, clean
tree at `13a1982e`).  Evidence and identity READMEs are regenerated
at these identities.
