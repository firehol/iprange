# SOW-0028 delivery step 5 (milestone 4) — qualification evidence

Produced 2026-09-05 (seventh fix wave) on the Linux workstation
(x86_64) with staged binary copies (no personal paths in the
committed evidence) plus the Windows-host housekeeping proof on the
authorized Windows validation host (access authorized for SOW-0028
qualification only):

- Rust product + worker + fixture tool: clean-release builds of
  `v4/rust/target/release/{iprange,iprange-v4-worker,examples/v4-fixture}`
  (`cargo build --release --all-features -p iprange-cli --bin iprange
  -p iprange-livedb --bin iprange-v4-worker --example v4-fixture`,
  rustc 1.91.1) staged as
  `/tmp/qualsvc/rust/{iprange,iprange-v4-worker,v4-fixture}`:
  - product SHA-256
    `860938744b203a7684d5dbc96e2fff9a8601f7dfd1fca2484107f5bd3b746e8f`
    (changed by the seventh-wave per-member cancellation scope,
    bounded transport channels, immediate all-rejected batch answers,
    and the non-zero framing-failure exit in
    `v4/rust/iprange-cli/src/rpc/session.rs`);
  - worker SHA-256
    `cb9ad6cd82a03b7933d706de9e1b4e4c707836962b7f00e194c5d50cd4511e94`
    (unchanged; build-proven identity, not pinned by the committed
    reports);
  - fixture SHA-256
    `7c6167933d802fab89f33520198e35286dbdf7bd6e0e348ee03fea5457c93459`
    (source unchanged since the fixture-qualification wave; identity
    is build-proven at this revision with the recorded command).
- Go product + worker: the documented `-buildvcs=false` qualification
  pair (go1.26 linux/amd64, no embedded vcs revision) staged as
  `/tmp/qualsvc/go/{iprange,iprange-v4-worker}`:
  - product SHA-256
    `85488a0f26c85f85c243dbd3d19b10a86e18e14d842696f111a7ac689d49ae68`
    (changed by the seventh-wave per-member cancellation scope and
    the non-zero framing-failure exit in
    `v4/go/internal/cli/rpc/session.go`);
  - worker SHA-256
    `16236608325cb189e0fbe05603886bbe150fd1ae83e4a8b532bfb7dd07054b1e`
    (unchanged; build-proven identity, not pinned by the committed
    reports).
- Windows qualification binaries (built on the authorized Windows
  validation host at the same product-source revision `908026ab`,
  clean working tree, staged under `C:/Temp/qualsvc-win/` and
  `C:/Temp/wqual/`):
  - Go product SHA-256
    `e507b7a944750f37dd309e537f8c07c127390d0c9996c13c7c1c9f8044c175f1`
    (go1.26.5 windows/amd64, `-buildvcs=false`);
  - Rust product SHA-256
    `e2124f4abb3ff735747cbdaa0aa4a8b4b6bcf58a831c83079123ec605e10124d`
    (rustc 1.97.1).
  Build commands, toolchain, and source revision are recorded in
  the report's `build_provenance` block.

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
50-record removal-log Windows checks (report schema v3).  All
Linux commands ran under `nice` with work dirs under
`/tmp/qualsvc/ev9/`; the Windows evidence for this wave is produced
on the authorized Windows validation host at the final product
sources and recorded in the report with build provenance.

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
  `iprange-cli-windows-housekeeping-report-v2` produced on the
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
