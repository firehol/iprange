# iprange v4 CLI and JSON-RPC qualification suite

This directory contains the external qualification suite for the
`iprange` v1 production API and the JSON-RPC product services that
implement it.

Two product executables implement the same wire contract:

- `v4/rust/iprange-cli` — the Rust product binary (`iprange --jsonrpc`).
- `v4/go/cmd/iprange` — the pure-Go product binary (`iprange --jsonrpc`).

Both implement the same 52 callable JSON-RPC methods plus the
`iprange.v1.cancel` notification, and both answer `system.describe`
with the same capability object. The Go binary imports only the public
Go SDK; it never touches the engine internals.

## The JSON-RPC stdio service

Run either binary and drive it over stdin/stdout:

```bash
cargo build --release --manifest-path v4/rust/Cargo.toml
v4/rust/target/release/iprange --jsonrpc

env CGO_ENABLED=0 go build -C v4/go -trimpath -o /tmp/iprange-go ./cmd/iprange
/tmp/iprange-go --jsonrpc
```

`CGO_ENABLED=0` is the canonical, qualified Go build: a statically linked
binary using Go's pure resolver, which is the configuration every DNS
parity pin in this repository was qualified against. Accepted
compatibility boundary: C and Rust resolve hostnames through the platform
`getaddrinfo`, the canonical Go build through Go's pure resolver. Where
the host answers a name differently for those two paths — different
address sets, different failures, different duplicate counts, different
answer order — the C, Rust, and Go outputs may differ in the `-v` DNS
bookkeeping lines, `IPs got N`, `totals: N lines read`, and the `lines`
field of the legacy binary v1/v2 header (derived from those counters);
this difference is a documented limitation of the resolver boundary, not
a defect, and is defined in the legacy coexistence section of
`.agents/sow/specs/iprange-jsonrpc-v1.md`. Numeric-IP parsing, address
handling, accounting, and diagnostic ordering inside the engines remain
byte-pinned. A cgo-linked Go build uses the host NSS stack instead and can
add further ordering differences; it is not the qualified configuration.

The transport is one JSON-RPC 2.0 object per physical line (LF or
CRLF), with these limits:

- input and output frame ceiling: 1,048,576 bytes;
- response object ceiling: 65,000 bytes (`output_limit` product error);
- batches: 1..=16 requests, executed in array order;
- queued requests per connection: 16 (`server_busy`);
- ids: string or integral JSON number; only `iprange.v1.cancel` may be
  a notification without an id;
- reader handles and cursor handles: 64 each, bounded closed-handle
  tombstones (1,024 per family);
- lookup batch: 4,096 addresses; cursor page: 4,096 records.

`system.describe` reports the complete capability object, including
the limits above, the export formats, and the local
`iprange-v4-worker` availability probe (validation and recovery run in
the worker process when one is installed beside the product binary).

## The qualification suite

`run.py` drives a real executable through declarative cases and an
independent scalar-interval oracle:

```bash
# Durable-artifact policy: committed evidence must never carry the
# operator's home directory, and the checkout normally lives under it.
# Stage the products (and their version-matched iprange-v4-worker
# siblings) and the fixture tool outside the profile before running:
RUST_IPRANGE=/tmp/qualsvc/bin/rust/iprange
GO_IPRANGE=/tmp/qualsvc/bin/go/iprange
FIXTURE_TOOL=/tmp/qualsvc/bin/rust/v4-fixture
nice python3 v4/cli/run.py --matrix rust --rust "$RUST_IPRANGE" \
  --fixture-tool "$FIXTURE_TOOL" --work-dir /tmp/w
nice python3 v4/cli/run.py --matrix go --go "$GO_IPRANGE" \
  --fixture-tool "$FIXTURE_TOOL" --work-dir /tmp/w
nice python3 v4/cli/run.py --matrix rust_to_go --rust "$RUST_IPRANGE" --go "$GO_IPRANGE" \
  --fixture-tool "$FIXTURE_TOOL" --work-dir /tmp/w --allow-skips
nice python3 v4/cli/run.py --matrix go_to_rust --rust "$RUST_IPRANGE" --go "$GO_IPRANGE" \
  --fixture-tool "$FIXTURE_TOOL" --work-dir /tmp/w --allow-skips
```

`run.py` fails closed.  An engine that dies mid-matrix is recorded in
the report's `engine_deaths` list and a matrix that cannot be driven at
all records a `matrix_verdicts` entry, and either one forces exit 1 even
when every case row that did execute passed — a runner that silently
scores a partial or dead run as success is the failure mode this
prevents.  A case row for a case that no engine executed is an invented
row and fails.  `--json-report` and `--work-dir` inside the checkout are
refused at argument parsing (exit 2), so the runner cannot write evidence
into the tree it is measuring, and every binary argument must be an
absolute executable file.

The mixed matrices (`rust_to_go`, `go_to_rust`) are two-binary
cross-language proofs: every rpc step declares the service role that
runs it (`actor: producer` for artifact creation/mutation,
`actor: consumer` for observation and transformation), and a mixed
case executes its producer steps on the producer binary and its
consumer steps on the consumer binary, in separate `--jsonrpc`
processes sharing only the per-case work directory. A case that
cannot exercise both actors is skipped with its reason, so a mixed
PASS means both binaries genuinely served; the JSON report records
the SHA-256 and executed-step count of each actor per case. `--allow-skips` is
required for skipped cases (fixture-only and single-actor cases in the
mixed matrices, and the whole C single-language surface, which the v4
product binaries do not implement). The `work-dir` must already exist.

Other gates:

```bash
nice python3 v4/cli/check_golden.py --json-report v4/cli/evidence/golden.json
                                           # 55 golden wire exchanges
nice python3 v4/cli/sensitivity_gate.py --json-report v4/cli/evidence/sensitivity.json
                                           # 14 broken-server modes
nice python3 v4/cli/check_kind_coverage.py --matrix ... --crash ...
                                           # artifact-kind universe gate
nice python3 v4/cli/check_fifo_surface.py --go "$GO_IPRANGE" \
  --rust "$RUST_IPRANGE" --fixture "$FIXTURE_TOOL" \
  --work "$(mktemp -d)" --json-report v4/cli/evidence/fifo-surface.json
                                           # 17 named-pipe arms x 2 engines
nice python3 v4/cli/throughput_harness.py --go "$GO_IPRANGE" \
  --rust "$RUST_IPRANGE" --work "$(mktemp -d)" \
  --json-report v4/cli/evidence/throughput.json
                                           # busy-reply rate + thread census
```

Both new gates take the same staged binaries as the matrices, and every
work directory must be empty and outside the operator profile. Each
reports its own path arguments are refused unless absolute, because a
relative `--work` resolves the FIFO or fixture under the invocation
directory and can flip the verdict without changing anything else.

### The FIFO-surface gate

`check_fifo_surface.py` pins the refusal class of every user-path open
arm against a named pipe. A blocked open is invisible to the case
corpus (there is no stable expected value to assert), so the arm table
lives in this gate rather than in prose. Each arm records the transport
code, the `data.code` class, the elapsed time, the child exit status,
and the exact request frame; expected classes are what Rust answers, and
the same class is asserted for Go because arm-exact parity is a contract
term. The arms cover the read-only opens (`reader.open`,
`database.info`, `database.metadata.get`), the validation and recovery
arms in both their read-only and quiescent placements, and the writer
inputs (metadata `replace_file` source on both `direct.replace` and
`database.metadata.replace`, the direct CSV input, the `@file` list and
an entry it names, the feed source, and a snapshot destination under
`replace_existing`). Read-only opens answer `invalid_argument`, the
quiescent offline arms answer `wrong_state`, writer inputs answer
`invalid_path`, and a non-regular publication destination answers
`conflict`. `--self-test` runs offline and proves the verifier rejects a
flipped class, a dropped arm, an invented arm, a missing engine, a
blocked arm scored as a refusal, a refusal slower than the deadline,
stripped request bytes, a missing regular-file control, and a child that
exited non-zero.

### The refusal-class parity gate

`check_refusal_class_parity.py` drives both product binaries over an
arms-by-path-kind grid and compares the refusal each engine reports. The
FIFO-surface gate above pins one path kind against one expected class per
arm; this gate generalises the comparison to the whole
(arm, path-kind) surface, which is where Go and Rust were found to
disagree: the same refused request classified as `io` by one engine and
`wrong_state` by the other is invisible to a per-arm pin that only checks
one spelling.

Neither table is hand-maintained, so the gate cannot be satisfied by
deleting a row:

- the arm list and the path-kind list are crossed mechanically, the
  report's own cell count must equal `len(arms) x len(path_kinds)`, and a
  missing or invented cell fails;
- `MANDATORY_PATH_KINDS` names the path kinds a deletion would otherwise
  remove from the grid, and a table lacking any of them fails;
- `PINNED_REFUSALS` records the Rust-authority class for specific
  (arm, path-kind) cells, so a both-engine drift into a new shared class
  fails as well as a divergence;
- `MANDATORY_ARMS` (23) and `MANDATORY_PATH_KINDS` (21) are checked in
  both directions, so the grid is a fixed contract and not the table the
  run happened to build.  One crossing of them is
  23 arms x 21 path kinds = 483 cells per engine.
- A third axis applies the same comparison under descriptor pressure:
  `PRESSURE_ARMS` (9) against `PRESSURE_PROFILES` (42, all mandatory),
  each profile a distinct `RLIMIT_NOFILE` band and table state, with
  `PINNED_PRESSURE_CLASS_COUNT = 378` pinned refusals under SHA-256
  `314d8be5618e9775d9dd386ad6d7e521ff27ee9ba63d7c9501746de941ad1456`.
  `--pressure` takes `off`, `routine` (12 profiles) or `full` (42).  A
  cell the environment cannot build is reported as host state, and a
  report that counts a blocked or host-unsupported cell as coverage
  fails.  Two engines that both answered owe the same class unless the
  committed table itself separates them: the Go writer and worker arms
  reach success one or two bands above the Rust reference, so such a cell
  is counted as `band_gap` and printed as `BANDGAP (arm, profile)`, not
  as a divergence.  Whether a cell is one of those gaps is derived from
  `PINNED_PRESSURE_CLASSES` and the two recorded replies -- each engine's
  reply must be a class its own pin allows in its own band -- and never
  from a flag the report carries, so a genuine class divergence cannot be
  reported away as a gap.

The verdict has two halves, and they catch different things. The first
checks that a report describes its own execution honestly: the cell count
must match the derived grid, every cell whose records differ must appear in
the divergences list, and no cell may claim agreement while its two
recorded answers differ. The second is the contract term: the divergence
count must be zero, a cell that reached its deadline is a failure and not
an absence of evidence, a cell that changed its answer across retries is
not parity evidence for either engine, and every pinned refusal must be
satisfied. A report can therefore be internally honest and still fail,
which is the point — an earlier revision of this gate verified only the
first half and scored PASS on a divergent grid. The controls named
`honest single divergence`, `honest hang` and `honest flake` each build a
report whose only defect is the one under test, so those three verdict
terms cannot be dropped without a control failing.

Per cell the comparison is `(kind, transport code, data.code, outcome,
publication-evidence shape)` between the two engines. Message text is
deliberately not compared: it is a human diagnostic, and the machine contract
is the error `code`, the `outcome` member, and which publication facts the
reply carries.

Three arms put the probed path in the *destination* slot
(`metadata.file_delivery` on `database.metadata.get`, `export.destination`
on `export`, and `removals_output` on `retention.first_seen.refresh`), and
two path kinds give them their durability boundary:

- `dest-parent-unreadable` — the destination's parent directory carries
  owner mode `0311`. Write and search stay available, so the output owner
  creates its private temporary and publishes onto the destination; read is
  denied, so opening that parent to synchronize it fails with `EACCES`. That
  makes `sync_directory()` fail deterministically on both engines without a
  device, a filesystem, or an injected fault, which is the condition the
  outcome-ambiguity boundary of the publication path is defined on. Both
  engines must answer `-32010` with `data.code` `io` and `data.outcome`
  `outcome_unknown`, and the reply must carry the publication evidence
  (`stage`, a canonical 64-hex `sha256`, `destination_visible` true,
  `temporary_removed`, `publication_policy`, and the `outcome_unknown` of
  the auxiliary file itself). For a committed first-seen refresh the reply
  outcome is `committed`, because the transaction and the auxiliary removal
  output are separate facts; the file's own `outcome_unknown` travels inside
  `details.removals_publication_failure.publication`.
- `dest-collision` — the pre-visibility control: same destination shape and
  the same `fail_if_exists` policy, but a normal parent and a destination
  that already exists. The refusal is definite (`name_exists`, with
  `not_started` for the export writer and `read_only_failure` for the
  metadata delivery, which had already read its source) and the reply must
  carry **no** publication facts, because no destination name ever appeared.

The third cell of that triple is already in the grid: the `missing` path
kind gives these arms a normal parent with no collision, and they publish
and answer `RESULT`. Together the three cells separate "refused before
publishing" from "unresolved after publishing" from "published", per output
owner and per engine.

Deleting either durability kind, or answering the pinned class while
withholding the evidence, fails the gate: the kinds are in
`MANDATORY_PATH_KINDS`, their expectations are entries in
`PINNED_REFUSALS` with a `facts` obligation, the evidence shape participates
in the parity comparison, and `--self-test` carries controls for
`read_only_failure` after visibility, facts stripped from both engines,
facts present on one engine only, facts claimed before visibility, and a
pin whose evidence obligation was loosened against the table. Each attempt runs under its own bounded deadline
so no cell can hang the battery, and a differing repeat is retried so a
flake is reported separately from a divergence: `flaky` and `hangs` are
counted apart from `divergences` so a scheduler artefact is never mistaken
for a contract term, and each of the three is a verdict term of its own.

```bash
nice python3 v4/cli/check_refusal_class_parity.py \
  --go /tmp/qualsvc/bin/go/iprange --rust /tmp/qualsvc/bin/rust/iprange \
  --fixture /tmp/qualsvc/bin/rust/v4-fixture \
  --work "$(mktemp -d)" \
  --json-report /tmp/refusal-class-parity.json
nice python3 v4/cli/check_refusal_class_parity.py --self-test
```

`--self-test` runs offline. It injects a synthetic divergence into a
matching pair and requires the gate to FAIL; it removes every executed
cell and requires a report of zero executed cells to FAIL; it deletes
the fixture that pins the `validate.live` sidecar-fold shape
(`live_recovery_coordination_unavailable`) and requires the fold check to
FAIL, at both the report level and the table level; and it attacks the
publication-durability terms listed above.  It also attacks the pressure
axis: deleting a pinned pressure cell or a pressure arm, loosening a
pressure pin to a bare class, un-naming a profile obligation, reporting a
wedge as an answer, dropping a missing cell, a rollup that lies, and a
forged pressure-table digest must each FAIL.  The band-gap allowance is
attacked in both directions: a cell the table does not support must FAIL
when it claims the gap, a cell the table does support must FAIL when it
denies the gap, a rollup that scores the reported gaps as divergences must
FAIL, and the conforming pair must report them with a divergence count of
zero.  `SELF_TEST_CASES_TOTAL`
pins the count at 70 controls (`--self-test` reports "PASSED: 70 cases
(committed total 70)"), so a control deleted from the gate is a self-test
failure rather than a silent shrink.
Timing, stated by scope. The committed run is one gate invocation under
`--pressure full`: `evidence/refusal-class-parity.json` records
`elapsed_seconds` 7.434 for the 483 main-grid cells on both engines (23
arms x 21 path kinds), and the 378-cell pressure axis is timed apart in
`pressure.elapsed_seconds` (102.175 s at this host load) because the main
grid's figure never covers it. Neither figure is a battery cost: the
whole qualification battery is a separate, minutes-class step (the
committed closure run recorded 198.2 s wall; see the named-cost
disclosure in the active SOW), so no single figure here may be read as
the cost of the other.

The committed artifact is `evidence/refusal-class-parity.json`. It records
the SHA-256 and `system.describe` implementation label of each binary it
drove, the `git_head` of the tree, and a provenance note naming whoever
generated it: this is a measurement of specific executables, and a reader
must be able to tell which.

### The Go coverage measurement

`coverage_harness.py` measures Go coverage in two separable halves and
keeps them separate in the report:

- `unit` — the module's own `go test -cover` over every package.
- `integration` — the committed corpus executed against binaries built
  with `go build -cover`, which reaches the shipped wire surface that an
  in-process test cannot: a test calls the SDK, a case speaks JSON-RPC to
  a child service.
- `merged` — the two counter sets combined by `go tool covdata`.

```bash
nice python3 v4/cli/coverage_harness.py --go-module v4/go \
  --revision <commit> \
  --rust /tmp/qualsvc/bin/rust/iprange \
  --fixture-tool /tmp/qualsvc/bin/rust/v4-fixture \
  --work EMPTY_DIR --json-report /tmp/coverage-go.json
nice python3 v4/cli/coverage_harness.py --self-test
```

Three properties make the number worth reading:

- **The binaries are not the qualification binaries.** The instrumented
  build lives in its own staging directory under `--work`, and its
  digests are recorded separately from the qualification and throughput
  binaries. Coverage instrumentation adds a counter block per basic block;
  a rate measured on instrumented code would attest to nothing, so
  throughput and coverage never share an executable.
- **Killed runs are never merged.** A Go coverage binary that is killed
  does not write its counter block, so a crash-battery child contributes
  no data rather than a zero. The harness merges only complete runs and
  refuses a coverage directory that is empty instead of scoring it as 0%.
- **A merge cannot lose work.** Combining counter sets is a union, so the
  merged figure must be at least each input's figure; a merged percentage
  below one of its own inputs means that input never took part, and the
  harness refuses rather than publishing the number. This check is
  mutation-tested: reintroducing the repeated `-i=` spelling of
  `go tool covdata`, which silently keeps only the last directory, is
  caught.

`--revision` stages the measured tree from a commit with `git archive`
instead of from the working tree. Other workers edit the checkout
concurrently, and one in-flight file makes the instrumented build fail for
reasons unrelated to the revision under attestation; staging by commit
pins the measurement and records the commit it measured. The staged tree
must include the siblings the Go tests open: the `conformance` corpus
(`../conformance/...` from the module) and the committed harnesses in `../cli`
(the descriptor-pressure suite executes `../cli/fd_pressure_harness.py`, which
imports its own same-directory siblings). A staging that omits either is
refused rather than scored.

`run.py` forwards `GOCOVERDIR` to the product child, and only that one
coverage variable, so the harness can direct counter output without adding
an undocumented source of child state: a normal qualification run has no
`GOCOVERDIR` in its environment and forwards nothing.

The closure policy this serves is stated as policy, not as a number:
committed tests that demonstrably detect the defect classes they claim, a
mutation/forgery battery that proves the gates reject bad evidence, and
measured unit **and** integration coverage recorded as facts. There is no
arbitrary numeric floor, because a percentage is an input to judgement
and not the judgement.

### The busy-reply throughput attestation

`throughput_harness.py` is the committed rate record. Functional cases
cannot catch a thread-per-reply regression: every reply still arrives and
every budget still holds, only the wall clock moves. `resource_harness`
proofs A-D pin boundedness, not rate, so before this harness a throughput
claim had no evidence at all. It measures `replies_per_s` for
`system.describe` served in 30-frame bursts (10,000 requests, 3 rounds,
fresh child per round, rounds retained so the spread is visible) and,
under `strace`, the `clone`/`clone3` census at 3,000 and 6,000 requests.
The gate is the structure — every reply served, clean child exit, and a
thread-creation count that does not grow with the request count — not an
absolute rate: a replies/s floor is not portable across host load, core
count, or governor, and a floor that only passes on one machine becomes a
blocker people learn to ignore. The measured rates are recorded instead.

They are recorded as host-load-dependent observations, not as a reference
band: the rate window starts when the child is spawned and so includes
process start, interpreter/runtime init, and the first frame, and the
figures move with system load, core count, and governor. The spread is
visible inside a single committed report: for one pair of binaries on one
host, the three rounds of `evidence/throughput.json` give Go 7,755.4 /
13,789.9 / 16,001.2 and Rust 43,190.7 / 46,747.4 / 42,392.5 replies/s, so
the Rust rate varies by about 10.3% round to round with nothing but load
changing. No ratio between the two engines is therefore implied
by these numbers, and they are explicitly **not** usable as evidence for
the 1.3x relative-rate contract planned for milestone 5 — that contract
needs a load-isolated measurement protocol of its own (pinned cores, idle
host, repeated trials, a stated statistic), which this harness does not
implement and does not claim.
`--self-test` proves the structural check rejects thread-per-reply
growth, growth at the doubled request count, a census that parsed no
task ids, dropped replies scored as a rate, and a killed child scored as
a pass.

Every report the harnesses write records `git_head` — the commit OID of
the reviewed tree, from `git rev-parse HEAD` against the checkout that
owns the harness, or `null` when it is not a git checkout. What makes it
a binding rather than a comment is what `check_kind_coverage.py` now
enforces about it: each of the seven consumed reports — the four matrices
plus the crash, FIFO-surface, and throughput reports — must carry
`git_head` as a 40-hexadecimal object id; placeholder shapes such as
forty `0` or forty `1` characters are rejected; and all seven must name
the same commit, so a report copied forward from an older battery, or
swapped in from a different tree, fails the gate instead of silently
passing. Omitting the FIFO-surface or throughput report is a CLI error,
not a smaller claim. `git_head` is written by the harness, so forgery of
the field itself is not what this defends against; it defends against
mixing revisions and against a vacuous report set.

It is a separate member from `checkout_root`, which is the directory the
gate resolves checkout-relative command arguments against; putting an
object id in that field would break the identity binding rather than
record it.

- `cases/` — the declarative method-family cases (75 files). Every
  rpc step declares its service role explicitly (`actor: producer` for
  artifact creation/mutation, `actor: consumer` for observation and
  transformation), so a transformation can run on either binary in a
  mixed matrix. Producer-created cross-language cases:
  `mixed.direct-created`, `mixed.membership-created`,
  `mixed.transform-created` (the consumer binary snapshots a database
  the producer built and reads it back),
  `mixed.live-coordination` (a consumer AND a producer reader pinned
  on generation 1 while the other binary commits twice; both pinned
  readers look up and range-scan after the commits and still observe
  the generation-1 empty view, reclamation returns `no_change` while
  any reader pins and commits only after the last reader closes),
  `workflow.publisher` (the full
  update-ipsets six-step production sequence with interleaved
  cross-binary verification, per-feed failure isolation, and the
  downstream aggregation/join/algebra steps consuming the
  workflow-built live feed DB), and `recover.successful` (validate a
  damaged file, capture the exact `recovery.inspect` candidate,
  recover, read the preserved content back through the other binary,
  and validate the recovered file (valid=true, zero findings) with
  the other binary).
  `resource.limits` proves the product-boundary ceilings (response
  object limit, reader/cursor capacity, system limits report).
  Capture specs may alias handles (`{"name", "path"}` items; `[N]`
  list steps), so two handles on one result path coexist under
  distinct names.
  `params.negative.*` pin the JSON-RPC params validator: a step marked
  `expect_params_rejected` is sent WITHOUT client-side schema validation,
  so the corpus can express a request the committed schema itself
  rejects, and the step passes only when the service answers transport
  code `-32602`. `check_request_is_contract_invalid()` makes the mode
  fail if the committed schema accepts such a request, so the assertion
  cannot rot into a no-op when schema and product drift; `message_contains`
  is optional and is only used where both engines provably share the text
  (message wording is an accepted-P3 difference, not a contract term).
  `writer_budget.max_open_files` has no zero value in the contract, so
  `params.negative.{direct_replace,metadata_replace,feeds_create,reclaim}`
  assert that refusal on both engines, and each case follows the refused
  write with a read proving the target stayed untouched — a refused
  request that still created or mutated an artifact must not pass.
  `params.negative.grammar` covers the rest of the grammar from the same
  position: a non-canonical numeric (`1e2`), a string where a number
  belongs, and unknown members at both the top level and inside a nested
  object, then a valid export to prove the case is not vacuous.
  `metadata.replace_file.{direct,replace,publish}` and
  `mixed.metadata-replace-file` pin metadata byte-exactness: a known
  1 KiB source is published with `mode: replace_file`, read back through
  `database.metadata.get`, and compared against the committed fixture
  digest inline, on delivery to a file, and across the language boundary
  (a database carrying `replace_file` metadata that one engine published
  is read back by the other, and the digest must match).
  `input.expand_at_paths` pins `@`-expansion: a `@file` list whose entries
  are paths, and a `@directory` scan containing a symlinked regular file
  among regular files, with the expansion counts asserted identically on
  both engines and the published database then opened by the other binary
  to confirm it read what the producer wrote. A symlink whose target does
  not resolve inside the scanned directory is dropped by both engines, so
  the fixture's symlink names a committed target.
  `mixed.export-cross-language` closes the cross-language export
  obligation: the producer publishes a database, and BOTH roles export it
  (netset and CSV) — the consumer re-exporting over the producer's own
  destinations with `replace_existing`, which is what records the
  consumer's open of the producer's artifact. Each `export` step names a
  `digest_group`; the runner hashes the artifact the service reported and
  the kind gate requires one digest per group across the whole battery
  (so the two engines must emit identical bytes), both product languages,
  and both service roles.

- `golden/` — complete request/response exchanges generated from the
  Rust binary and validated against the strict Python schemas.
- `schema/` — the machine authority: framing, methods, results, case
  validation, and the scalar interval oracle. It imports no SDK.
  Capture pointers use an anchored grammar (`member` chains with
  optional `[index]` list steps); malformed pointers such as
  `candidates[0]]` are rejected by case validation and by the runner.
  Every `schema/` module ships a `_self_test()` (run it through a
  normal import, e.g. `nice python3 -c "from schema import cases as
  c; c._self_test()"`), and `run.py` runs its own, the oracle's, and
  the case-schema self-tests before any matrix. The kind-coverage
  gate ships its own doctored-report self-test, and it is opt-in:
  the gate CLI is always usable (`--help`, and a verdict on the
  reports handed to `--matrix`/`--crash`/`--fifo-surface`/
  `--throughput`), and `nice python3 check_kind_coverage.py
  --self-test` runs the full control battery -- including the
  committed-evidence control that the real `evidence/` set passes the
  gate, which is what makes evidence rotation drift loud. Running that
  self-test on every CLI invocation instead would let an in-flight
  rotation replace every verdict, `--help` included, with its own
  assertion. `--self-test` is therefore a required step of the wave
  battery, invoked on its own with its own exit code, not a side
  effect of the gate command.
- `benchmarks/` — reserved for the consolidated workload manifests
  and `bench.py` harness of SOW-0028 delivery step 6 (currently
  empty; also update the `cases/` bullet above and the matrix counts
  when a new case is added).

### Mechanical file-kind ledger and frame sizes

Every run report carries two additive evidence fields derived
mechanically from the executed steps — never a manually maintained
table:

- `file_kinds`: after each executed rpc step (success and
  expected-error) and each executed legacy step, the runner
  inventories the per-case work directory (fixture inputs excluded)
  and classifies every file it finds: v4 database main files plus
  snapshot/recovery destinations (`v4_main`); `.readers` live
  sidecars; `.iprange-reservation-*.tmp` publication reservations;
  `.iprange-scratch-*.tmp` authorized scratch; declared adapter
  outputs (csv/jsonl/netset/ipset/ranges/legacy_binary via `output`,
  `findings_output`, `report_output`, `removals_output`, and
  `export.destination`); metadata delivery files (`delivery.path`);
  anything else as `unknown`. A file that first appears by the end of
  a step is `created_by` that step's method; a file that already
  existed and whose path the step params reference is `opened_by`
  that method. Files that appear and disappear inside one step are
  transient and are not counted.
- `frame_sizes`: the JSON-RPC client measures the raw wire bytes of
  every request and response frame (LF terminator included, one
  physical line per frame; the same unit for both directions) and
  reports the per-method maximum request and response size.
- Every PASS case entry also carries the per-case `file_kinds`
  lineage (relative artifact path -> kind and the acting
  `actor.method` created_by/opened_by lists) and the executed-actor
  identity (per-actor SHA-256, product-declared `implementation`
  from `system.describe`, and executed-step count);
  `check_kind_coverage.py` enforces the cross-language file-kind
  contract on the matrix and crash reports: it requires all four
  matrix reports (rust, go, rust_to_go, go_to_rust) plus a positive
  crash report whose PASS scenarios span both language directions;
  language attribution comes exclusively from the per-case executed
  actor identities (the top-level `matrix` label is only
  cross-checked against the executed pair, never trusted — a report
  relabeled without re-running fails the gate); every required kind
  must be created by both product languages and, whenever any
  service opens the kind, opened by both languages too; PASS
  evidence containing any kind outside the required universe
  (v4_main, live_sidecar, publication_reservation, publication_temp,
  authorized_scratch, adapter_output, metadata_delivery) fails the
  gate. The gate consumes PASS-case lineage only (the report-root
  aggregate merges partial ledgers even for FAIL cases and is never
  trusted), rejects any matrix report with `failed != 0` and any
  crash report with `failed != 0` or leftover product processes, and
  counts only crash scenarios whose `"pass"` is true. Its committed
  self-test exercises doctored missing-matrix, unknown-kind,
  one-language, single-direction, all-failed, leftover-process,
  clone-and-relabel, missing-actors, and foreign-implementation
  reports.

### Step-5 product-interface gates

- `resource-record.md` — the delivery-step-5 resource record: the
  documented frame/response-object/batch/reader/cursor ceilings, the
  `resource.limits` case evidence, and the adapter memory evidence.
- `crash_harness.py` — process-level interruption proof at the product
  interface: drives the normal JSON-RPC client (reusing `run.py`'s
  service and fixture code — no production test hook), kills the
  producer at an observable process-crash marker, then proves with
  both consumer binaries in both directions that resolution is
  truthful, residue is bounded, and reopen succeeds.  Eight scenarios
  cover the approved crash scope: A1/A2 `current.publish`
  interruption at the reservation block (A3 is the foreign-destination
  negative control against the reservation digest), B
  `database.initialize_live` at the creating-state sidecar, C
  `recover` at the CRC-valid authorized-scratch header, D
  `direct.replace` interruption during uncommitted live-draft
  construction (a successful control run is recorded first; the
  exact commit/finish crash points are covered by the SDK fault
  gates — Go `v4/go/internal/live/lifecycle_crash_test.go`,
  `v4/go/internal/writer/crash_v4work_test.go`; Rust
  `v4/rust/iprange-livedb/src/live_crash_tests.rs`; the live-commit
  window has no sidecar write at the product boundary, recorded
  evidence), E `export` at the non-empty partial-output `.export.tmp`
  marker, F `validate` interruption during validation/findings
  delivery (the findings temporary carries real flushed bytes, the
  interrupted output is a strict prefix of the successful reference
  findings, and no destination replacement exists; the
  plan-recorded worker-scratch marker is impossible at the product
  boundary: validation never spills to authorized scratch, recorded
  evidence).  16 scenarios run per invocation (8 x both directions);
  markers are observable process-crash states (file growth or visible
  temporary bytes), never wall-clock assertions forged, and their
  existence alone is not claimed as proof of storage sync.  Committed
  evidence is produced from binary copies staged under
  `/tmp/qualsvc/` (version-matched product/worker pairs), so the
  recorded argv carries no personal paths; see `evidence/README.md`.
- `resource_harness.py` — the four Linux product-interface resource
  proofs (evidence `evidence/resource.json`): the >16-in-flight
  `server_busy` pipelining proof (one slow export + 19
  `system.describe` frames; both binaries must answer exactly 3
  `server_busy`, 16 results, the export -32010 `cancelled`, and exit
  0; the describes are pipelined only after the export's private
  temp appeared, making the split deterministic), the -32001 over-limit-frame close path (one response, null id,
  then stdout drains to EOF with zero further bytes, exit non-zero
  per the framing-failure contract), the
  `maintenance.remove` proof against a real reservation nonce of a
  publish killed at the reservation marker (the listed row is passed
  unchanged, never rebuilt), and the cancellation proof (a slow
  export, the `iprange.v1.cancel` notification naming it, and a
  `system.describe` pipelined in one stdin blob: the cancelled export
  never answers with a result — the session suppresses
  explicitly-cancelled ids; -32010 `cancelled` is the EOF-path
  answer — the describe answers with a result, exit 0).  All four
  run for both product binaries.
- `windows_housekeeping_harness.py` — the platform-bound
  `windows_housekeeping` maintenance-kind proof, in two parts on the
  authorized Windows validation host.  (1) Native refresh exercise:
  each product runs a real `retention.first_seen.refresh` with a
  `removals_output` while a live reader pins the previous main; the
  harness proves the refresh completes, publishes the exact removal
  log, leaves no private `.removals.tmp` residue, and that the
  pinning reader closes cleanly.  (2) Deterministic GC pair proof:
  product-written envelopes are timing-dependent (the retirement
  cleanup machine completes them best-effort), so the harness crafts
  one format-valid 8192-byte authenticated envelope plus its inert
  payload twin (`gc_envelope_windows.py` mirrors the committed codec
  and the creator-only Windows DACL the products install) and proves
  `maintenance.list` shows exactly the two candidate rows (envelope
  and inert payload, both clean, UTF-16LE basename encoding,
  authenticated directory identity equal across both products) and
  that `maintenance.remove` with the listed envelope row passed
  unchanged removes the pair with a proved-currently-absent
  result (both products truthfully report
  crash_reappearance_possible; no power-loss durability is
  claimed).  On other
  platforms it records the truthful `os_unsupported`/
  `read_only_failure` negative.  Committed Windows evidence:
  `evidence/windows-housekeeping.json` (produced on the authorized
  Windows validation host).

```bash
# Products and fixture staged outside the profile (same policy as the
# run.py matrix commands above).
RUST_IPRANGE=/tmp/qualsvc/bin/rust/iprange
GO_IPRANGE=/tmp/qualsvc/bin/go/iprange
FIXTURE_TOOL=/tmp/qualsvc/bin/rust/v4-fixture
nice python3 v4/cli/crash_harness.py --producer "$RUST_IPRANGE" --consumer "$GO_IPRANGE" \
  --fixture-tool "$FIXTURE_TOOL" --work-dir /tmp/w-crash --json-report /tmp/crash-report.json
nice python3 v4/cli/crash_harness.py --producer "$GO_IPRANGE" --consumer "$RUST_IPRANGE" \
  --fixture-tool "$FIXTURE_TOOL" --work-dir /tmp/w-crash --json-report /tmp/crash-report.json
```

  The first command runs the harness in both directions
  (producer=rust then producer=go) in one invocation; the report
  schema is `iprange-cli-crash-report-v1`.

## Committed identity records and the two producer verbs

Two artifacts in `evidence/` are identity records rather than
measurements of a product, and `command_sanitize.py` is their registered
producer because it is the module that knows how a committed report must
identify the tree it names.  Both reach disk only through
`command_sanitize.write_committed_report`, so the provenance and privacy
rules that bind a harness report bind them too.

`--emit-build-ids DEST` recomputes `IPRANGE_V4_BUILD_ID` for the
`iprange-livedb` package and commits the record.  The digest is a pure
function of `v4/rust/iprange-livedb/Cargo.toml` and every `.rs` file
under `v4/rust/iprange-livedb/src`, hashed in the order `build.rs` uses
— manifest first, then the sources sorted by logical name — as one
record per input: `u64le(name-length) name u64le(content-length)
content`.  The logical name is the file path split on both `/` and `\`,
with empty and `.` components dropped and the rest joined by `/`.  That
split is why one source state has one identity and the record's four
host entries (`darwin`, `freebsd`, `linux`, `windows`) agree; hashing
the build host's own path spelling instead makes the value differ
between a POSIX and a Windows checkout, and the record carries both of
those pre-normalizer digests as the evidence for why the split is
required.  The identity is what the Rust CLI and the co-located
`iprange-v4-worker` exchange during the worker handshake
(`v4/rust/iprange-livedb/src/worker/control.rs`), so a host-dependent
value could not attest that two binaries came from one tree.  With
`--built-cli` and `--built-worker` the recomputed digest is searched for
verbatim in the bytes of those executables; a role that is not named is
recorded `not measured` rather than inheriting an earlier run's claim.
`--rust-tree` selects a different package root.

`--commit-report SRC --commit-report-to DEST` promotes a staged report
into the evidence directory through the same writer.  The report bytes
stay the producing gate's: this verb adds only the identity members the
writer owns (`command`, `checkout_root`, `git_head`) and the derived
`privacy` block.  It refuses the write before anything is created when a
screened input lives under the operator's profile, when source and
destination name the same file (promoting a report onto itself would
hide which writer produced it), or when DEST is registered to another
writer (this module would then be recorded as the producer of a report
it did not measure).  `check_kind_coverage.py --emit-manifest` authors
`battery-manifest.json` and cannot import the writer, so promotion is
how that file reaches `evidence/`; because a promotion overwrites
`command`, the record names the promoting invocation, which keeps a
promoted artifact distinguishable from a writer that committed its own
measurement.

In `COMMITTED_REPORT_WRITERS` both files belong to the
`command_sanitize.py` entry: `owner` `lead`, tier `shared-writer`, and
`screened` the six path-valued options of these two verbs
(`--emit-build-ids`, `--rust-tree`, `--built-cli`, `--built-worker`,
`--commit-report`, `--commit-report-to`).  Every one is handed to
`require_paths_outside_profile` and recorded in the artifact's
`privacy.checked_inputs`, so dropping the screening of one option is a
gate failure rather than a silent leak;
`command_sanitize.py --audit-committed-reports` enumerates the directory
against that table, so an identity file from an unregistered producer
fails the way a registered writer that bypasses the helpers does.

```bash
# Regenerate the identity record from the package that built the
# binaries under test; the products are staged, so the recomputed digest
# is checked against their bytes.
nice python3 v4/cli/command_sanitize.py --emit-build-ids \
  /tmp/qualsvc/reports/build-ids.json \
  --built-cli /tmp/qualsvc/bin/rust/iprange \
  --built-worker /tmp/qualsvc/bin/rust/iprange-v4-worker

# Promote the battery's staged manifest into the evidence directory.
# The destination is checkout-relative, so the committed record carries a
# checkout-relative path and no personal prefix.
nice python3 v4/cli/command_sanitize.py \
  --commit-report /tmp/qualsvc/reports/battery-manifest.json \
  --commit-report-to v4/cli/evidence/battery-manifest.json
```

## Adapter outputs, outcomes, and the two input surfaces

### Adapter-owned outputs when durability cannot be established

An adapter-owned output (an `export.destination`, and the same rule for the
other declared output arms: `output`, `findings_output`, `report_output`,
`removals_output`) reports error code `io` with `data.outcome`
`outcome_unknown` once the destination name has become visible in the
namespace and durability could not be established. Before the destination
name appears, the same failure keeps its pre-work outcome
(`not_started`, or the pre-existing `name_exists`). This is the
outcome-ambiguity boundary the format specification defines for the
publication path: from the durable transition that makes publication
possible until the destination content and identity have been re-checked,
an unresolved failure is `outcome_unknown`, and the implementation must
not remove a possibly published destination
(`.agents/sow/specs/binary-format-v4.md:3915-3930`). Both engines
implement the same mapping in their export writers —
`v4/go/internal/cli/fileio/export_writer.go:249-262` and
`v4/rust/iprange-cli/src/io/export_writer.rs:252-270` — and the
publication evidence (destination, stage, policy, digest of what was
written, and whether the private temporary was removed) travels with it,
so the reply states what is known instead of guessing at what is not.

Two consequences for callers, both of them contract terms rather than
advice:

- A committed database transaction and an unresolved auxiliary
  removal-output are **separate facts**. A method whose main state
  transition committed and whose optional removal output could not be
  resolved reports each on its own terms; the auxiliary ambiguity does
  not retract the commit, and the commit does not resolve the auxiliary.
  Reading one field as if it described the other loses information the
  product took care to preserve.
- A caller must inspect the reported state rather than act on the error
  class. Retrying blindly is wrong in both directions: a
  `fail_if_exists` retry after an `outcome_unknown` legitimately reports
  `name_exists`, because the first attempt did publish, and treating that
  second reply as "nothing happened" inverts the truth. Deleting the
  destination is worse — it destroys bytes that may be the published
  artifact. Where the choice matters, ask for `replace_existing` (which
  proves the destination content) or resolve through the documented
  resolvers; the specification keeps removal of a possibly-published
  destination out of the failure path on purpose.

### The never-block contract is the session surface, not the one-shot argv surface

The bare-open audit covers the JSON-RPC session surface, and that scope
must be stated rather than inferred. Every user-path open on that surface
opens `O_NONBLOCK` and judges the opened descriptor inside the open owner,
which is what the FIFO-surface and refusal-class-parity gates pin: a
swapped-in named pipe is refused promptly with the arm-exact class, and
`hangs=0` is a measured result, not an expectation.

The one-shot legacy argv surface is deliberately outside that contract.
`v4/go/internal/cli/legacy/parse.go` reads its inputs whole through
`calleropen.Open` (O_RDONLY) plus `io.ReadAll`, and the Rust twin
`v4/rust/iprange-cli/src/legacy/parse.rs` uses `std::fs::read`; both
therefore block on a FIFO exactly as the C reference tool does — `src/iprange.h:57-71` opens the one-shot input with
plain `fopen(3)` and performs no type check before the read. That parity
with the released C CLI is inherited behavior, not a gap the audit missed:
the argv surface takes operator-typed arguments in a foreground process,
where a blocking read is the C tool's long-standing behavior and the
caller can see and interrupt it. Adding the never-block policy there would
change the released CLI's observable behavior for no threat-model gain,
since the surfaces that must never block are the ones that serve a
long-lived session a caller cannot watch.

The distinction is recorded here so that "the bare-open audit is complete"
is read as "complete over the session surface", and so that a future
reader does not mistake the argv surface's blocking read for an open
finding.

## Known limitations

- The Go binary currently reports `product_version "0.0.0"` to match
  the Rust build's unreleased package version.
- Windows: the Go product builds, and the SDK worker surface refuses
  live validation/recovery opens on platforms without proven
  coordination (the same honest stub the SDK documents).
- Error `message` text is a human diagnostic; the machine contract is
  the error `code` and `outcome` members.
