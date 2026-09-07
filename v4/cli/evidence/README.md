# SOW-0028 delivery step 5 (milestone 4) — qualification evidence

The fifteenth-wave evidence is regenerated at the final wave-15
revision `e21784ce` after the external whole-milestone control
review of the wave-14 revision (turn 2 review of the wave-15 fixes:
lifecycle identity platform kind, artifact-basename wire mapping,
drain-EOF, matrix fixture binding, conflict-order control, and
strict response-id correlation — all recorded in SOW-0028's
wave-15 section) plus the native Windows verification wave:

- the Rust CLI test suite now runs fully green natively on the
  authorized Windows validation host (711 tests across
  `iprange-livedb` and `iprange-cli`; the canonical
  `cargo test -p iprange-livedb -p iprange-cli` invocation builds
  the version-matched validation worker that the live-source
  identity inspection spawns — the previous
  `-p iprange-cli --bin iprange` form neither built nor matched the
  worker).  Test temp names no longer embed `SystemTime` debug
  output (colons are invalid in Windows path components), the
  publication-evidence round trips pin the platform identity kind,
  the legacy parse error test asserts the platform-neutral parts of
  the missing-file contract on Windows, and the immutable snapshot
  wire test pins the documented per-platform housekeeping state;
- worker availability: when no validation-worker candidate exists
  beside the running binary, requests now report the worker as
  unavailable instead of a raw file-not-found I/O error (matches
  the SDK `worker_availability` probe semantics);
- Windows housekeeping re-qualified at `e21784ce` on the
  authorized Windows validation host: 2/2 PASS with the native
  Windows Python 3.14.6.

Linux reports record the product identities `73cb0626…` (rust) and
`bdd06f6c…` (go), workers `9fd36146…` / `7784e830…`, fixture
`6c2c56b9…` (staged in `.local/shared/binaries/SHASUMS.txt`);
the Windows housekeeping report records the Windows-host products
`9f6107ae…` (rust) and `6b1540e1…` (go) at the same source
revision.  These identities include the role-round delta repairs:
the encoding-aware artifact-basename renderer in both products,
the Go worker-availability fallback, Go's proper UTF-16LE GC name
store, and the pinned resource-gate controls (all recorded in
SOW-0028's wave-15 section).

The fourteenth-wave (external whole-milestone control review
FAIL and repair) evidence is regenerated at the wave-14 revision
`e272c990` after the external control review of the wave-13
revision.  The reviewed findings and their repairs:

- held-open over-limit frames: a peer that wrote exactly LIMIT+1
  bytes whose last byte was not the CR of a CRLF terminator held
  both readers waiting for another byte forever (stdin open, no
  -32001, no exit).  The one-extra-byte allowance now applies only
  when the last byte is CR; any other LIMIT+1-th byte is already
  over the ceiling and is reported immediately (both products;
  held-open and CR-tail boundary tests in both languages);
- Windows basename round-trip: `LocalBasename` on Windows stores
  UTF-16LE units (encoding 2), but the Rust wire rendering passed
  the raw units through UTF-8 lossy and emitted NUL-interleaved
  mojibake that the resolvers reject.  `create_result`,
  `commit_cleanup_artifact`, and the live transition now render
  `LocalBasename` through an encoding-aware decoder (encoding 2 ->
  UTF-16LE text, else UTF-8 lossy); the Go product always stores
  encoding 1 and was unaffected.  The Windows report re-qualified
  at the new revision and now records a clean `main_basename` from
  both products;
- watchdog coverage: the full-stderr signal tests signalled an idle
  session and only exercised the graceful exit; they now wedge the
  session so the process-lifetime watchdog's detached-diagnostic
  path is genuinely exercised (Go helper mode + Rust wedged spawn);
  the Rust full-stderr fixture also wrote 4096 bytes from a
  one-byte buffer and treated any error as full-pipe proof and now
  fills from a real buffer requiring EAGAIN;
- proof and gate hardening: the resource proofs a/d drain stdout
  after the expected responses and require zero trailing bytes (a
  stray duplicate or malformed frame fails; new self-test control),
  and the kind gate rejects fixture identities recorded without a
  sha256 or contradicting an earlier identity for the same path;
- test-race repair: the Go signal tests no longer run `Wait`
  concurrently with `StdoutPipe` reads, and the full-stderr tests no
  longer close raw pipe fds behind an `*os.File` whose finalizer can
  then close a reused fd number (the pre-existing EBADF flakes
  across the suite under load).

Linux reports record the product identities `6ab63dfd…` (rust) and
`d228ebe5…` (go); the Windows housekeeping report is regenerated at
the same product source revision (recorded below).

Produced 2026-09-06 on the Linux workstation (x86_64) from staged
binary copies (no personal paths in the committed evidence) plus the
Windows-host housekeeping proof on the authorized Windows validation
host (access authorized for SOW-0028 qualification only).  The
tenth-wave evidence is regenerated at the wave-10 revision: the
termination-signal contract (a wedged transport can no longer ignore
SIGINT/SIGTERM; watchdog force-exit plus signal-wins-over-EOF),
spec amendments for the queue-bound distribution and the
id-less-invalid-notification response, kind-gate capability
enforcement for every kind (create and open, crash and matrix sides)
plus the per-case argv execution anchor, deadline-bounded client I/O
in the crash harness and conformance runner, and the golden corpus
now pins the invalid-notification error exchange.  The ninth-wave
repairs (broken-stdout shutdown deadlock, proof-b gaps, the shared
client deadline bypass, abbreviated-command and ordinal-attribution
gate bypasses, Windows `source_basename`) remain included; the full
finding lists are recorded in SOW-0028's ninth- and tenth-wave
sections.

- Rust product + worker + fixture tool: clean-release builds of
  `v4/rust/target/release/{iprange,iprange-v4-worker,examples/v4-fixture}`
  (rustc 1.97.1) staged as
  `/tmp/qualsvc/ev21/bin/rust/{iprange,iprange-v4-worker,v4-fixture}`:
  - product SHA-256
    `6ab63dfd72f498a4d2a41b856e2d94750adaa6598c16968c2489fcbe75ff8d93`
    (wave-14 build: the immediate held-over-limit report in
    `framing.rs`, the encoding-aware `LocalBasename` rendering in
    `handlers/lifecycle.rs` and `handlers/live.rs`, plus all earlier
    wave fixes);
  - worker SHA-256
    `cb9ad6cd82a03b7933d706de9e1b4e4c707836962b7f00e194c5d50cd4511e94`
    (unchanged; build-proven identity, not pinned by the committed
    reports);
  - fixture SHA-256
    `d615488f038fa59deea87e0ce3340b780380fe0f2122e8e1ad65edeb25d861f1`
    (source unchanged since the fixture-qualification wave; the
    identity is a fresh canonical release build at the wave-14
    revision with the recorded command - the earlier recorded
    identity predated the current release toolchain).
- Go product + worker: the documented `-buildvcs=false` qualification
  pair (go1.26 linux/amd64, no embedded vcs revision) staged as
  `/tmp/qualsvc/ev21/bin/go/{iprange,iprange-v4-worker}`:
  - product SHA-256
    `d228ebe5c024e9f8dc8cccfc31295f73f0ddf87a83fb2ee4c01a52336ef3467d`
    (wave-14 build: the immediate held-over-limit report in
    `framing.go`, plus all earlier wave fixes; the worker tree is
    unchanged since the eighth wave);
  - worker SHA-256
    `202a83ac92f5c8b85b44068a1553aef0dbf25a81fb2d888022592292d03b6141`
    (the worker source tree is unchanged since the eighth wave and
    the worker does not link `v4/go/internal/cli/rpc`; the identity
    is build-proven at this revision with the recorded command, not
    byte-reproducible across toolchains or build paths, and not
    pinned by the committed reports).
- Windows qualification binaries (built on the authorized Windows
  validation host at the wave-14 product-source revision
  `e272c990`, clean tracked tree, staged under
  `C:/Temp/qualsvc-win/ev20/`):
  - Go product SHA-256
    `fb6b503a46d151e0b52498e72041602782032b2a3e055f1284e8605a3b4318e5`
    (go1.26.5 windows/amd64, `-buildvcs=false`);
  - Rust product SHA-256
    `a6ec5b45427fe7e3af9cf27d567ad9aa7f09e65a45faa1cbb94985d915befc48`
    (rustc 1.97.1; carries the encoding-aware basename rendering and
    the immediate over-limit report, so the recorded Windows flow
    `main_basename` is now clean text from both products).
  Build commands, toolchain, and source revision are recorded in
  the report's `build_provenance` block; the harness runs under the
  native Windows Python 3.14.6 (embeddable distribution staged on
  the qualification host, no system install).

The external gap review of 2026-09-04 requested the D1-A crash scope,
the D2-A resource proofs (including the Windows-housekeeping kind),
the D3-B recovery wording, and the kind-gate provenance repair; this
evidence set is regenerated at that fix wave (external framework,
cases, records, and the product-source round-trip fix above).  The
whole-milestone gate review of 2026-09-05 returned FAIL and the user
approved amendment 1A for the crash interruption contract; this
evidence set is regenerated at the sixth fix wave: per-member queue
accounting in both session implementations, the hardened kind gate
with six negative controls, crash scenarios D/F per the amended
contract (successful control run + interrupted findings compared
against the reference), the oversized-frame sentinel proof, and
per-kind crash lineage records.  The whole-milestone gate review
of the sixth wave returned FAIL (two P1 session defects, one P1
kind-gate defect, and six P2 proof defects; the full finding list is
recorded in SOW-0028's seventh-wave section).  This evidence set is
regenerated at the seventh fix wave: per-member cancellation tokens
in both session implementations (cancelling a queued batch member no
longer touches unrelated active work), bounded Rust transport
channels with immediate all-rejected-batch answers, a non-zero exit
on the -32001 framing-failure close in both products, the hardened
kind gate (mixed matrices require both actors to execute every PASS
case, executed-operation records, effective/duplicate command option
validation with executable-to-binary-record binding, crash open
facts backed by recorded opens, fixture-created v4_main lineage,
required-opened kinds), truthful per-scenario sidecar and
adapter-output open lineage with per-actor executed operations, the
exactly-one export-temp orphan contract in crash scenario E, the
deadline-bounded resource harness, and the strict two-row / exact
50-record removal-log Windows checks (report schema v3).  The
five-reviewer round of the seventh wave found one Rust P1
(frame-over-limit deadlock with in-flight work), gate and resource
P2s, and record P1/P2s; the eighth fix wave (2026-09-05) repairs
them and regenerates this evidence: the Rust product above carries
the writer-guard scope, the kind gate now rejects matrix-side
fabricated cross-process opens, every harness deadline uses
`time.monotonic()`, resource proof a waits for the export's private
temp before pipelining the describes, and resource proof c kills
only after the reservation file reached its full 8,192-byte block
(2 x 4,096-byte v4 pages).  The ninth fix wave (2026-09-06)
repairs the broken-stdout shutdown deadlock in both session
implementations, the proof-b forced-kill acceptance (a harness kill
is never a product exit), the proof-b server-envelope and output-
ceiling validation (shared `frame.decode_response`), the missing
deadlines in the shared JSON-RPC client path (bounded read/write
with selectors when the resource harness configures bounds), the
kind-gate command-provenance bypass via argparse abbreviations
(recorded commands are replayed through the runner's own parser
with `allow_abbrev=False`; the fixture-tool argument is bound to the
battery's crash-recorded fixture), the crash operation-ordinal
attribution (opens and creations now reference the actual executed
operation, never a fabricated ordinal zero), and the Windows
cross-listing `source_basename` equality.  All Linux commands ran
under `nice` with work dirs under `/tmp/qualsvc/ev17/work2/`
(matrices, crash, resource); the mixed matrices
are invoked with `--allow-skips` (recorded truthfully in each
report command) so every battery command exits 0.  The Windows
housekeeping evidence is regenerated at the wave-10 product sources
(`e13be7ea`) on the authorized Windows validation host with the
deadline-bounded client running in its Windows thread mode; the
report schema is v3 with the exact 50-record removal log and
build provenance.

## Files

- `matrix-rust.json`, `matrix-go.json` — single-language matrices,
  38 case files: 38 passed, 0 failed in both languages; oracle
  checks 37.  Every PASS case entry carries the per-actor SHA-256,
  the product-declared `implementation` (rust|go from
  `system.describe`) and the executed-step count.
- `matrix-rust_to_go.json`, `matrix-go_to_rust.json` — two-binary
  cross-language matrices: 14 executed (both-actor cases), 24
  skipped (single-actor cases), 0 failed, oracle checks 22, in both
  directions.  The same per-actor identity is recorded for every
  PASS case; `check_kind_coverage.py` derives language attribution
  exclusively from those executed identities (the top-level `matrix`
  label is only cross-checked, never trusted).
- `crash.json` — `iprange-cli-crash-report-v1` in both directions
  (producer=rust with consumer=go, producer=go with consumer=rust):
  16/16 scenarios pass — A1/A2/A3 publication interruption and
  destination classification, B live-transition sidecar
  interruption, C authorized-scratch durability, D interruption
  during uncommitted live-draft construction with a recorded
  successful control run (range count and contents verified), E
  export interruption at the partial-output marker, F interruption
  during validation/findings delivery (interrupted findings output
  is a strict prefix of the successful reference; no destination
  replacement) — zero leftover processes, `failed: 0`, per-scenario
  per-kind actor lineage (`created_by`/`opened_by`).
- `crash-negative.json` — the `/bin/false` negative control: 0/16
  pass, `failed: 16`, zero leftover processes, empty per-scenario
  kind lineage; used as the harness sensitivity control, never as
  a kind-gate source.
- `resource.json` — `iprange-cli-resource-report-v1`: the four
  Linux product-interface proofs for both binaries — (a) the
  >16-in-flight `server_busy` pipelining proof (one slow export + 19
  `system.describe` frames; both binaries answer exactly 3
  `server_busy` (-32002) behind the single in-flight export, 16
  results in the 16-deep queue, the export -32010 `cancelled`, ids
  1..20 covered once, exit 0), (b) the -32001 over-limit frame
  close path (the oversized frame is followed by a valid
  `system.describe` sentinel in the same stdin stream; exactly one
  null-id -32001 response appears, the sentinel unanswered — trailing
  bytes are never parsed — stdout drains to EOF with zero further
  bytes, exit non-zero (startup/framing failure)), (c)
  `maintenance.remove` against a
  real reservation nonce (kill at the reservation marker, list,
  remove with the listed row passed unchanged, durable absence),
  (d) CLI cancellation (slow export id 1, `iprange.v1.cancel` naming
  it, `system.describe` id 2 in one stdin blob: the cancelled export
  never answers with a result — explicitly-cancelled ids are
  suppressed by the session — the describe answers with a result,
  exit 0).
- `windows-housekeeping.json` —
  `iprange-cli-windows-housekeeping-report-v3` produced on the
  authorized Windows validation host (Microsoft Windows 11, AMD64)
  at the same product-source revision: for each Windows-built
  product binary the report carries (1) the native refresh exercise —
  a real `retention.first_seen.refresh` with a `removals_output`
  behind a pinning live reader completes, publishes the exact
  removal log with the refresh value, leaves no private
  `.removals.tmp` residue, and the reader closes cleanly — (2) the
  two removal-collector abort/failure cleanup exercises (result-
  budget overflow and publish failure on an existing destination:
  no `.removals.tmp` residue, no destination replacement), (3) the
  deterministic GC pair proof — one format-valid 8,192-byte
  authenticated envelope (`.iprange-gcauth-<attempt>-<ordinal>.tmp`,
  artifact kind `private_output`, UTF-16LE source commitment,
  full-block CRC-32C, creator-only protected DACL) plus its inert
  payload twin, listed by `maintenance.list` as exactly two clean
  candidate rows (envelope and inert payload), cross-listed by the
  other product with an equal authenticated directory identity over
  every listing row, then removed with the listed envelope row
  passed unchanged, with durable absence and a zero-row
  after-listing — and (4) build provenance (source revision, clean
  tree, build commands, toolchain) with per-binary mtime/size.  The
  pair is built by `gc_envelope_windows.py` from the committed
  codec constants (`v4/go/internal/live/gc_codec.go`, `gc_name.go`,
  `identity_local_windows.go`, `v4/go/internal/security/
  security_windows.go`), not by any product test hook.  On
  non-Windows platforms the same script records the truthful
  `os_unsupported`/`read_only_failure` negative.

## Gate invocation

```bash
nice python3 v4/cli/check_kind_coverage.py \
  --matrix v4/cli/evidence/matrix-rust.json \
  --matrix v4/cli/evidence/matrix-go.json \
  --matrix v4/cli/evidence/matrix-rust_to_go.json \
  --matrix v4/cli/evidence/matrix-go_to_rust.json \
  --crash v4/cli/evidence/crash.json
```

The gate requires all four matrix reports and a positive crash report,
rejects failed/leftover reports and unknown kinds, enforces the
both-language creation (and, where any service opens the kind,
both-language consumption) contract per kind from the executed-actor
identities, and counts only PASS crash scenarios.  Its doctored-report
self-test runs before the CLI and covers the clone-and-relabel attack
(a `rust` report relabeled `go` fails), missing per-case actors, and
implementations outside rust/go.
