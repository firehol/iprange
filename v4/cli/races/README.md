# v4/cli/races — stat→open swap-race battery

TL;DR: this directory holds the committed, reproducible battery that races a
file-name replacement against the moment a production engine opens that name.
It replaces the throw-away wave-19.22 operations probe
(`swap_race_arms.py` + `swapper.py`), which reported `attempts=30 hangs=0` and
exited 0 while its helper had never started and while its "positive" control
answered an error. The battery fails closed: every claim it makes is backed by
a control that has to fire, and `runner.py --self-test` proves each control
fails when weakened.

Provenance: built under the approved external-reviewer ruling
`2A, including the complete reproducible runner and detecting controls`, which
required the design to be recorded before implementation. It carries no
requirement waiver and no closure approval.

## What is being proved

Both engines refuse a named pipe (FIFO) wherever a user-supplied path is
opened, and the refusal must still hold when the name changes between the
engine's pre-open `stat` and its `open(2)`. A pre-open check alone cannot hold,
because `rename(2)` can install a different node in that window; what holds is
a non-blocking open plus a post-open identity/regular-file check, or a refusal
that survives the swap. This battery measures exactly that window.

The deterministic half of the same boundary — "the FIFO already exists when
the request is written" — is covered by the committed FIFO-surface gate
(`v4/cli/check_fifo_surface.py`) and by the case corpus. Those deterministic
file-opening tests stay there; they pin stable expected values and must not be
diluted with the timing-sensitive work below.

## Roles

- **runner.py** — the only entry point. Owns arguments, fixture setup, helper
  lifecycle, per-arm execution, control evaluation, aggregation, report
  writing, and the process exit code. `--self-test` runs its offline mutation
  controls.
- **arms.py** — the arm table: for each arm, the fixture files it needs, the
  name that gets swapped, the JSON-RPC request that opens that name, and the
  answer shapes each phase must produce. Rust is the semantic authority and Go
  must match exactly, the same arm-exact parity rule the FIFO-surface gate
  applies. Fixtures follow the case corpus: live databases come from
  `iprange.v1.database.create` served by the binary under test, the immutable
  snapshot for the reader arm comes from the v4 fixture tool, and publications
  seed their destination with `fail_if_exists` so the raced request can use
  `replace_existing` and still succeed on a regular name.
- **aggregate.py** — the judgment, kept apart from execution: `judge_arm()`
  and `judge_detector()` turn collected observations into failures, and
  `Battery` decides the exit code. Keeping the decision pure is what lets
  `--self-test` mutate a record and require that the battery notices. This
  module is also the registered producer of `race-battery.json`: it hands the
  report to `command_sanitize.write_committed_report()`, the shared author of
  committed evidence, and never serializes the report itself.
- **service.py** — one bounded JSON-RPC session per attempt (the raced open
  happens once per request, so every attempt is a fresh product process in its
  own session), the shared answer classifier, and wedge confirmation.
- **swapper.py** — the racing helper. Repeatedly replaces the target name with
  `rename(2)`, alternating a regular node and a FIFO, and publishes its own
  progress through an atomic state file.
- **sentinel.py** — the detector control's engine: a deliberately naive client
  (`stat`, then a bare blocking `open(2)` with no `O_NONBLOCK` and no post-open
  check), which is the shape the engines used to have.
- **hostcontext.py** — the host snapshot recorded with every attempt: the
  system run queue and load averages, the CPU count, and the effective answer
  deadline. Recorded facts only; no judgment reads them.

## Verified helper execution (no silent helper failure)

The reported defect was that the probe was copied into a scratch directory
without `swapper.py`; the helper died at exec (`can't open file swapper.py`),
nothing ever raced, and the run still logged `attempts=30 hangs=0` and exited
0, because the exit status came only from `hangs`. Guards:

1. Helpers are executed from this directory by absolute path
   (`os.path.join(os.path.dirname(__file__), ...)`); nothing is copied, so a
   helper cannot go missing in a scratch directory, and `main()` refuses up
   front if either helper file is absent.
2. Each helper must publish an explicit marker before its output is trusted.
   The swapper writes an atomic state file carrying **its own pid** before the
   race loop starts, and the runner requires that pid to equal the child it
   spawned, requires the child to still be alive, and re-reads the file during
   the race so counters must keep moving. The sentinel writes a startup marker
   and prints `ARMED` before calling `open(2)`, so "started then blocked" is
   distinguishable from "never started".
3. The helper must close its own race: a nonzero exit, a phase other than
   `stopped`/`expired`, or a state file that never appears is an aggregate
   failure (`helper-execution`).
4. Additional anti-vacuity gates: an arm must complete every requested attempt
   unless a hang stopped it early, and the answer universe it observes must be
   inside the frozen arm shapes.

## Controls, and the two claims that stay separate

- **replacement activity occurred** — swapper-side counters `to_fifo` and
  `to_regular`: the helper completed real `rename(2)` transitions into both
  node kinds. An arm where the helper only reached one kind, or none, is a
  dead race and fails.
- **timing window exercised** — `in_flight_to_fifo`: a replacement into the
  FIFO state completed *inside a request window*. The runner raises an
  in-flight marker before writing the request and clears it after reading the
  answer; the swapper samples that marker on both sides of each rename and
  counts the replacement only when both samples see it. Zero means the arm
  never tested the window, so the battery fails.
- **detector fires on real replacement activity** — the sentinel control. The
  naive opener runs against the same kind of racing swapper, and each attempt
  that goes silent is confirmed by attaching a writer to the FIFO inode it is
  parked on: `open(O_WRONLY|O_NONBLOCK)` fails with `ENXIO` unless a reader is
  blocked on that inode, so a successful attach names the exact FIFO with a
  waiter, and the payload that follows releases it. The battery requires at
  least one confirmed wedge. A detector that cannot see a wedge that is
  certainly present cannot certify `hangs=0` on a product arm, so this control
  failing is a battery failure.
  To make that check reachable after the raced name has moved on, the swapper
  rotates a bounded pool of permanent FIFO inodes (`--pool-size`, default 8)
  and hard-links one into the raced name. A blocked reader waits on the inode,
  not the name, so the pool keeps every parked reader reachable without
  allocating one inode per toggle.
- **product-arm controls**, per arm and per engine, with the swapper stopped:
  a stable regular name must answer `RESULT` (the workflow is genuinely wired
  up — the wave-19.22 arm answered `name_not_found` here, which proved
  nothing), and a stable FIFO must answer the arm's frozen refusal class.
- **race phase** — the product arms must produce zero hangs. A timeout is
  classified, never swallowed: thread wait channels are captured from
  `/proc/<pid>/task/*/{wchan,syscall}` and wedge confirmation is attempted, so
  the report says which FIFO had a parked reader or why none did.

### Frozen arm expectations

Measured against the wave-19.24 qualification binaries, 150 raced attempts per
arm per engine, identical on both engines, zero blocks:

| arm | target of the raced open | stable regular | stable FIFO | allowed while raced |
|-----|-------------------------|----------------|-------------|---------------------|
| `meta` | `direct.replace` metadata source | `RESULT` | `-32010/invalid_path` | those two |
| `csv` | `direct.replace` CSV input | `RESULT` | `-32010/invalid_path` | those two |
| `feed` | `current.publish` input path | `RESULT` | `-32010/invalid_path` | those two |
| `atlist` | `current.publish` `@`-list | `RESULT` | `-32010/invalid_path` | those two |
| `reader` | `reader.open` snapshot | `RESULT` | `-32010/invalid_argument` | also `-32010/wrong_state` |

`wrong_state` ("live path no longer names a regular file", ~8% of raced reader
attempts) is the post-open identity refusal that a lost race alone produces, so
it is allowed only in the raced phase; it is counted separately as
`raced_only_refusals_observed`. Anything outside the listed universe —
including a hang — is a battery failure.

## Host context: recorded facts, never an escape hatch

Every attempt is judged against `--deadline` (default 1.5 s). Before a product
process can refuse anything, the scheduler has to run it, so on a host whose
run queue is longer than its CPU count a deadline can expire while the engine
is still starting. The 2026-09-15 wave-19.25 run showed exactly that: three
`rust` attempts were classified `engine-hang` at load average 2576 on 24 CPUs,
each with `wedge=no-parked-reader-at-detection` and a wait channel in process
startup/teardown rather than a FIFO park — the signature of a starved process,
not of a lost race. Nothing in the artifact said so.

Each arm record and the detector record therefore carry:

| field | meaning |
|-------|---------|
| `host_context.source` | `proc/loadavg`, `getloadavg`, or `unavailable` |
| `host_context.nproc` | online CPUs; 0 is refused rather than guessed |
| `host_context.deadline` | the effective per-attempt answer deadline these samples belong to |
| `host_context.samples` | attempts sampled (must be >= 1) |
| `host_context.run_queue` | min/median/max runnable tasks at attempt start |
| `host_context.loadavg_1m` | min/median/max one-minute load average |
| `host_context.over_cpus_samples` | attempts that started with run queue > CPUs |
| `host_context.oversubscribed` | derived: median run queue > CPUs |

Every silent attempt additionally carries the two samples that decide the case:
`host_at_attempt_start` (taken before the process is spawned) and
`host_at_detection` (taken after wedge confirmation). The detector control
counts its hangs, so it carries a `hang_observations` list matching that count
one-for-one. The two stable controls carry the same pair, and a
`control-unexpected` finding quotes it, because a control that answers nothing
is as ambiguous as an attempt that answers nothing.

The rule that keeps this honest: the samples are read by the report and by the
step-[14] verifier, never by the verdict. An `engine-hang` is a failure whether
the run queue was 1 or 10,000, and a record whose host context is missing,
unsampled, or self-contradictory is its own failure (`host-context-missing`,
`host-context-inconsistent`) rather than a pass. That is the same discipline
the refusal-class parity gate and the descriptor-pressure harness apply to host
state: an environment that was not measured is a battery defect, never a
waiver.

## Output policy

`--report-dir` is mandatory and must be an empty (or absent) directory outside
the repository checkout and outside the operator's profile. The battery writes
`race-battery.json` plus one transcript per arm there. It never writes a
committed evidence path: placing the report in `v4/cli/evidence/` is an
explicit battery step, so a routine run cannot overwrite accepted evidence.

`race-battery.json` is committed evidence, so it is authored through the
shared committed-report writer. `Battery.write()` validates the location,
refuses a report whose own strings name the operator's profile path (a typed
`ReportPolicyError`, raised before anything exists on disk), and then calls
`command_sanitize.write_committed_report()`. That call owns the identity
members the artifact audit expects — `command`, `checkout_root`, `git_head` —
and derives the `privacy` block recording which path-valued options were
screened (`--rust`, `--go`, `--fixture-tool`, `--report-dir`,
`--provenance-note`). Those five names must match the `screened` set of this
module's entry in `COMMITTED_REPORT_WRITERS`; if either list is edited alone,
the self-test and the artifact audit both fail rather than quietly narrowing
the check. The report carries the engine SHA-256 identities, the per-arm record
with both counter families, and the failure list that produced the exit code;
binary identity comes from the digests, never from the path a file was read
from.

## Usage

```text
nice python3 v4/cli/races/runner.py --report-dir EMPTY_DIR \
    --rust BIN --go BIN --fixture-tool BIN \
    [--arms meta,csv,feed,atlist,reader] [--attempts 20] \
    [--deadline 1.5] [--stop-after 3] [--interval 0.0002] \
    [--provenance-note TEXT]

nice python3 v4/cli/races/runner.py --self-test
```

Exit status: `0` only when every arm, every control, and every helper check
passed. Anything else — including a helper that never started — is nonzero,
with the reasons printed and written to `race-battery.json`.

## Cost

Measured on the qualification workstation, both engines, all five arms,
`--attempts 20`: ~6 s wall, and the swapper stays throttled by its
`--interval` sleep instead of spinning. A larger sweep used to certify the
frozen table (150 raced attempts per arm per engine, no hang, no block) took
under 10 s of wall time per engine under `nice`. Every run belongs to the
battery's bounded-work class: well inside the per-step budget, always run with
`nice`, and never by repeating a whole-program analysis.

## Mutation expectations

The battery is only trustworthy if weakening it is visible. `--self-test`
asserts these offline by mutating a collected record; the same outcomes were
demonstrated live by mutating the tree.

| weakening | required outcome |
|-----------|------------------|
| `swapper.py` absent from the battery directory | `helper-missing`, exit nonzero |
| helper killed before publishing its state | `helper-execution`, exit nonzero |
| helper counts in-flight replacements wrongly (always 0) | `timing-window-unexercised`, exit nonzero |
| helper only ever installs a regular node | `replacement-activity-incomplete`, exit nonzero |
| hang classifier treats a timeout as an answer | `detector-blind`, exit nonzero |
| stable-regular control answers an error shape | `control-unexpected`, exit nonzero |
| stable-FIFO control goes silent | `control-unexpected`, exit nonzero |
| an arm answers a class outside its frozen table | `unexpected-answer`, exit nonzero |
| an arm quietly runs fewer attempts than requested | `attempts-incomplete`, exit nonzero |
| a product attempt stops answering | `engine-hang`, exit nonzero |
| a product attempt hangs while the host is massively oversubscribed | still `engine-hang`, exit nonzero — a load number never excuses a hang |
| an arm record omits its host context | `host-context-missing`, exit nonzero |
| an arm record's host context omits the sampled run queue | `host-context-missing`, exit nonzero |
| a hang was recorded without its attempt-start or detection-time sample | `host-context-missing`, exit nonzero |
| a host sample's `over_cpus` disagrees with its own run queue and CPU count | `host-context-inconsistent`, exit nonzero |
| a record's `oversubscribed` flag disagrees with its sampled median | `host-context-inconsistent`, exit nonzero |
| the detector counts hangs it has no observation for | `host-context-missing`, exit nonzero |
| report location inside the checkout | refused before any write |
| this writer's entry removed from `COMMITTED_REPORT_WRITERS` | the committed report is named as coming from an unregistered writer |
| a registered writer that serializes its own `json.dump()` beside the shared call | named as serializing a report outside `write_committed_report()` |
| committed bytes with the `privacy` block removed | refused: missing the block only the shared writer can write |
| committed bytes that stopped screening one path option | refused: `privacy.checked_inputs` no longer matches the registered option set |
| committed bytes whose arm record dropped its host context | refused by the install gate: the record cannot say what the host was doing |
| committed bytes that count hangs without observations | refused by the install gate: each claimed silent attempt owes its samples |

`--self-test` currently runs 22 mutation controls (15 arm, 7 detector) plus 6
committed-report writer controls; both counts are printed by the self-test
itself and derived from the control lists, so they cannot drift from what is
actually executed. The shared provenance controls in `command_sanitize` are
executed here as well and asserted against the number that module pins.

### How the helper-missing failure stays reportable

The preflight check has to name the directory it looked in, and the report
writer refuses any artifact carrying the operator's profile path. A message
that embedded the absolute checkout path therefore destroyed the evidence of
its own failure: the process still exited nonzero, but no artifact was left
behind. The artifact records the battery location as the stable relative
`v4/cli/races`, and the console additionally prints the absolute path. Both
halves are asserted by `--self-test`.

### Live mutation demonstrations

Each row was produced by mutating the tree or the staged copy, running the
battery, and reading the class out of `race-battery.json` rather than trusting
the exit code alone. A control that is nonzero for an unrelated reason is not
evidence, so the class name is the assertion.

| mutation applied | observed class | exit |
|------------------|----------------|------|
| `swapper.py` renamed away, then restored | `helper-missing` | 1 |
| `swapper.py` startup pid replaced by a foreign pid | `helper-execution` | 1 |
| `swapper.py` `in_flight()` forced to report no in-flight marker | `timing-window-unexercised` | 1 |
| `sentinel.py` open widened to `O_NONBLOCK` (can no longer wedge) | `detector-blind` | 1 |

The in-flight and sentinel rows are the two claims that must stay separate: a
run can record thousands of completed replacements (`to_fifo`, `toggles`) while
never once having them land inside a request window (`in_flight_to_fifo`), and
`hangs=0` on a product arm means nothing unless the same detector reported a
`confirmed_wedges` count above zero on the naive opener.

## Battery integration

`/tmp/qualsvc/battery-w1925.sh` step `[14]` runs this directory as committed
source (no copy), passes `--report-dir` to a private directory under its own
scratch root, and prints, from `race-battery.json`:

```text
RACECHECK verdict=<PASS|FAIL> arms=N activity_observed=N window_exercised=N detector_hangs=N detector_wedges=N failures=N
```

Product arms are the entries that name an `engine`; the detector control is the
entry that carries `subject_label` and no engine. The battery installs the
artifact into `v4/cli/evidence/race-battery.json` at its explicit rotation step,
the same way every other harness report is staged, and that install is gated on
`races/aggregate.py::committed_artifact_problems()` — the same function the
producer applies to its own bytes. It joins the shared artifact audit's rules
with this report's record contract, so a staged report carrying any of these is
refused at the install and the previously accepted artifact stays in place:

- no `privacy` block, or one that stopped screening a registered path option;
- a `command` that does not re-sanitize to itself, or an absolute
  `checkout_root`;
- an arm or control record with no host context, a host context missing its
  sampled run queue, or a hang counted without the observations that show it.

A refused install records its step as a mismatch rather than staying silent,
because an artifact the producer cannot answer for is a battery defect. Promotion through `command_sanitize --commit-report` is deliberately
not the path: it refuses a destination whose artifact belongs to a registered
writer other than the caller, because a promotion would record the copy as the
measurement that produced the report.

### The provenance note carries no workstation path

`--provenance-note` becomes a string inside the report, and the shared privacy
writer (`v4/cli/command_sanitize.py`) refuses any report whose strings name the
operator's profile. This battery's checkout lives inside that profile, so a
note spelling the staged-binary ledger as its absolute path
(`/home/<user>/…/.local/shared/binaries/SHASUMS.txt`) made the whole battery
exit 2 and write no `race-battery.json` -- after all ten product arms and the
detector control had already passed, and after the per-arm transcripts had been
written. The screening is correct and stays; the note is what changed.

The battery composes the note from `command_sanitize.sanitized_path_value()`,
which renders a checkout-contained path checkout-relative (so the ledger is
named as `.local/shared/binaries/SHASUMS.txt`), and names only paths that
already live outside the profile for everything else (the staging root and the
ledger copy under `/tmp`). It is then re-checked with the same
`personal_path_in_report()` the writer applies before any gate receives it.

### What step [14] requires on top of the runner's verdict

The runner owns the verdict; the battery step owns the claim that the verdict is
worth its evidence. Step [14] reads `race-battery.json` and records `MISMATCH`
unless all of these hold, printing each unmet one as `RACECHECK PROBLEM …`:

- `verdict == PASS` and an empty `failures` list.
- One arm record for every engine in `binaries` crossed with every arm in
  `options.arms` (10 records for five arms on two engines). Without this, an arm
  that wrote no record at all is indistinguishable, in the printed line, from an
  arm that passed.
- The detector control reported at least 2 hangs and at least 1 confirmed wedge.
  `hangs=0` on a product arm is only as strong as the detector that proved it,
  and a detector that fires exactly once is marginal rather than working.
- Every recorded engine and fixture-tool identity is a file under the staging
  root whose SHA-256 equals what the staged-binary ledger carries for that
  staging-relative name, and the two ledger copies the provenance note names
  agree with each other. Paths in the report are claims; the digests are the
  binding.
