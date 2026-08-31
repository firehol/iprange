# update-ipsets benchmark evidence (SOW-0027 milestone 4)

All runs: linux/amd64, release builds, Intel i9-12900K (12th gen),
under `nice`, single harness at a time. Go binary: cmd/iprange-v4-bench
at the recorded commit; Rust binary: iprange-livedb bench harness.

- `accepted-baseline.csv` (upstream, embedded into the bench binary):
  the accepted Go p50 medians and CI disaster limits (2x p50 + 100 ms
  runner noise, 500 ms for update-ipsets-workflow). Identity
  go-v4-local-20260828: re-based after the 4c/4d optimization slices
  (key-only tree search, single-edit import seams), the improved state
  is the accepted release baseline.
- `baseline-samples-20260828.csv`: the accepted Go run at the 4c/4d
  state (18 CI cases, 1 warmup + 5 samples), source of
  accepted-baseline.csv.
- `baseline-samples-final.csv`: the pre-4c accepted Go run (the
  20260827 baseline), kept as historical evidence of the improvement.
- `ci-go-v4-local-20260827.log`: the pre-4c CI gate run.
- `ci-go-v4-local-20260828.log`: the first 4c/4d CI gate run against the
  new baseline (1 warmup + 3 samples, enforce mode), 18/18 within-limit,
  ratios 0.933-1.049. Its header predates the baseline-identity constant
  update (labels go-v4-local-20260827); the values enforce the new
  accepted medians.
- `ci-go-v4-local-20260828b.log`: the gate run at the same identity
  after the delta-review fixes (selected-ranges by value, u32 prefix
  probes, abort branding, probe suffix validation), 18/18 within-limit,
  ratios 0.940-1.106. update-ipsets-workflow 1M median 4,878 ms (ratio
  0.964 against the accepted 5,058 ms).
- `ci-go-v4-local-20260828c.log`: the gate run after the external-review
  fixes (value-return segments, pointer-free gap probes, facade name
  cache and value arena), 18/18 within-limit, ratios 0.943-1.105,
  workflow 4,873 ms (ratio 0.964).
- `case-runs-4c4d-20260828.csv`: the preserved 1M single-case rows for
  the six milestone-4 headline cases (before 4c with the 3M-alloc
  import, after 4c/4d, and the post-review workflow runs), source of
  every median quoted in SOW-0027.
- `rust-ratio-acceptance-20260828.csv`: the Go-vs-Rust ratio acceptance
  table for the milestone-4 headline cases, medians of fresh matched
  samples of the final release binaries on the same host
  (`rust-ratio-go-samples-20260828.csv` /
  `rust-ratio-rust-samples-20260828.csv` hold the raw rows). This is
  the separate Rust-ratio report the delta review required: the
  Go-baseline CI gate proves regression stability only, the ratio table
  is the parity evidence. It is also the source of the pre-B "was"
  values quoted in the b-f entries below.
- `profiles-4c4d-summary.txt`: before/after CPU profile head evidence
  for the slice wins (lowerBound closure dispatch removed; write-seam
  allocations removed).
- `smoke-go-final.csv`: the Go smoke matrix at the 4c/4d state
  (55 cases).
- `smoke-rust-reference.csv`: the matched Rust smoke matrix
  (same fixture identity iprange-v4-update-ipsets-v1), baseline
  rust-v4-local-20260811, from the pre-milestone-4 matched pair.
  Elapsed ratio range vs the 4c/4d Go run: 0.34x-5.92x (was
  0.42x-7.59x at the pre-4c state; tiny-scale cases are dominated by
  fixed open/close and catalog costs, the 1M medians are the release
  evidence).
- `scale-validation-final.csv`: the pre-4c scale validation rows
  (live-validation 1M 28.8 ms, live-membership-validation 1M 29.8 ms,
  immutable-validation 1M 29.7 ms).
- `scale-validation-20260828.csv`: the 4c/4d scale validation rows
  (live-validation 1M 24.1 ms, live-membership-validation 1M 28.5 ms,
  immutable-validation 1M 22.1 ms).
- `ci-go-v4-local-20260828d.log`: the gate run at the B2 state
  (family-typed range records, no universal tree key on the writer hot
  paths, cached tree-key limbs, draft-private assign gap path), 18/18
  within-limit, ratios 0.395-1.071. update-ipsets-workflow 1M median
  3,465 ms (ratio 0.685), last-seen-refresh 1,048 ms (ratio 0.713).
- `rust-ratio-acceptance-20260828b.csv`: the post-B2 Go-vs-Rust ratio
  table for the same six milestone-4 headline cases, medians of fresh
  matched 5-sample runs of the final release binaries on the same host
  (`rust-ratio-go-samples-20260828b.csv` /
  `rust-ratio-rust-samples-20260828b.csv` hold the raw rows). The
  writer-side ratios dropped sharply versus the pre-B table
  (membership-import 3.506 -> 1.648, nested-overwrite 6.655 -> 4.152,
  update-ipsets-workflow 4.434 -> 3.141); read paths are unchanged
  (1.872/1.850/2.560 vs 1.929/2.058/2.631).
- `ci-go-v4-local-20260828e.log`: the gate run at the B3 state
  (Rust-parity once-validated fixed search probes, limb-first tree-key
  ordering, typed per-leaf validation order), 18/18 within-limit,
  ratios 0.417-1.072. Live-validation dropped to 0.928 against the
  accepted Go baseline.
- `rust-ratio-acceptance-20260828c.csv`: the post-B3 Go-vs-Rust ratio
  table for the six milestone-4 headline cases from interleaved matched
  5-sample runs (Go and Rust samples alternate per scenario on the same
  host; raw rows in `rust-ratio-go-samples-20260828c.csv` /
  `rust-ratio-rust-samples-20260828c.csv`). Ratios:
  membership-import 1.622, nested-overwrite 4.412,
  update-ipsets-workflow 3.212, live-direct-random-lookup 1.964,
  immutable-direct-random-lookup 1.999, live-validation 2.183
  (interleaved windows on a loaded host; the quiet-window runs measured
  reads 1.78-1.81 and validation 2.18-2.48, so the true deltas versus
  the pre-B baseline are: validation -12%, reads unchanged, write
  scenarios within run-to-run noise).
- `ci-go-v4-local-20260828f.log`: the gate run at the C1 state
  (closure-free family-typed reader probes over the shared once-validated
  format.FixedSearch, value-semantics selected-runs scan, allocation-free
  algebra boundary tracking, slices.Sort output, view-backed snapshot
  membership words), 18/18 within-limit, ratios 0.525-1.210.
- `rust-ratio-acceptance-20260828d.csv`: the post-C1 Go-vs-Rust ratio
  table from interleaved matched 5-sample runs (raw rows in
  `rust-ratio-go-samples-20260828d.csv` /
  `rust-ratio-rust-samples-20260828d.csv`). Ratios:
  membership-import 1.531 (was 1.622), nested-overwrite 4.451,
  update-ipsets-workflow 3.209, live-direct-random-lookup 1.821 (was
  1.964), immutable-direct-random-lookup 1.855 (was 1.999),
  live-validation 2.353. The update-ipsets workflow allocations dropped
  from 10,727,434 to 27,414 objects (369MB to 9.8MB) per the ci-f gate
  row (27,409 objects / 9.8MB in ci-g); the gate rows carry the
  allocation totals, the sample files are wall-time rows.
- `selected_ranges_test.go` (reader package): regression test pinning
  exact run-scan coverage for the all-catalog and named scope forms.
  The 4c "by value" refactor had aliased the pending field on the
  lookahead stop path (dropped and duplicated runs with three or more
  physical ranges) and allocated one heap object per emitted run; C1
  replaced the pointer with value semantics.
- `ci-go-v4-local-20260828g.log`: the gate run at the C2 state
  (limb-based numeric replacement validation over the NumericKeyCodec
  seam), 18/18 within-limit, ratios 0.467-1.012.
- `rust-ratio-acceptance-20260828e.csv`: the post-C2 Go-vs-Rust ratio
  table from interleaved matched 5-sample runs (raw rows in
  `rust-ratio-go-samples-20260828e.csv` /
  `rust-ratio-rust-samples-20260828e.csv`). Ratios:
  membership-import 1.666, nested-overwrite 4.184 (was 4.451),
  update-ipsets-workflow 3.231, live-direct-random-lookup 1.863,
  immutable-direct-random-lookup 1.863, live-validation 2.301.
  The C2 head-to-head measured nested-overwrite 1334ms -> 1247ms
  (-6.5%) in the same window; the residual write gap is the generic
  gap/replace machinery dispatching through interfaces per probe, which
  Go generics cannot monomorphize without duplicating the tree core per
  family (recorded in SOW-0027 as the remaining C2 trade-off).
- `ci-go-v4-local-20260828h.log`: the gate run after the five-reviewer
  round on the B/C delta (direct FixedSearch.Cell probes in the tree,
  dead-flag removal in the limb replacement validation, wider-key U32
  leading-bit semantics, snapshot shadowing rename, one-family runs and
  range-builder allocation), 18/18 within-limit, ratios 0.416-0.993,
  update-ipsets-workflow 3,446 ms (ratio 0.681), 27,406 objects
  (9.8MB).
- `rust-ratio-acceptance-20260828f.csv`: the post-review-round Go-vs-Rust
  ratio table from interleaved matched 5-sample runs (raw rows in
  `rust-ratio-go-samples-20260828f.csv` /
  `rust-ratio-rust-samples-20260828f.csv`). Ratios:
  membership-import 1.636 (was 1.666), nested-overwrite 4.044 (was
  4.184), update-ipsets-workflow 3.209 (was 3.231),
  live-direct-random-lookup 1.835 (was 1.863),
  immutable-direct-random-lookup 1.830 (was 1.863), live-validation
  2.235 (was 2.301). The six P3 fixes did not regress any measured
  case; nested-overwrite improved 4.184 -> 4.044 and validation 2.301
  -> 2.235 in the same interleaved window.
- `ci-go-v4-local-20260828i.log`: the gate run at the slice-F state
  (one-inode immutable feed builder; the update-ipsets benchmark now
  calls the public CreateImmutableFeedV4 instead of composing
  live-create plus snapshot), 18/18 within-limit, ratios 0.396-1.025.
  update-ipsets-workflow 1M median 2,468 ms (ratio 0.488 against the
  pre-swap accepted Go baseline; the workflow scenario's baseline
  re-bases at the g identity because the benchmark code path changed).
- `rust-ratio-acceptance-20260828g.csv`: the post-slice-F Go-vs-Rust
  ratio table from interleaved matched 5-sample runs (raw rows in
  `rust-ratio-go-samples-20260828g.csv` /
  `rust-ratio-rust-samples-20260828g.csv`). Ratios: membership-import
  1.682 (was 1.636), nested-overwrite 4.200 (was 4.044),
  update-ipsets-workflow 2.189 (was 3.209, previously not comparable:
  the benchmark substituted live-create plus snapshot; the g number is
  the first apples-to-apples one-inode comparison),
  live-direct-random-lookup 1.784 (was 1.835),
  immutable-direct-random-lookup 1.792 (was 1.830), live-validation
  2.299 (was 2.235). The four unchanged scenarios are within the
  rounds' run-to-run noise; the workflow Go elapsed dropped from
  3,478 ms to 2,450 ms, and the ratio from 3.209 to 2.189. VOIDED:
  the "inside the SOW 2-3.5x write envelope" acceptance of this entry
  is historical. The 2-3.5x write envelope was superseded on
  2026-08-29 by the binding <=1.3x acceptance (user decision 1A/2A,
  recorded in SOW-0027); every later ratio is measured against that
  binding and fails it as recorded in the entries below. This file is
  the archival trace; the acceptance-gate numbers live in SOW-0027.

- `rust-ratio-reader-inline-20260829.csv`: same-session matched Go-vs-Rust
  read ratios after the reader-accessor inlining slice
  (live-direct-random-lookup 1.67, immutable-direct-random-lookup 1.66;
  typed inlineable probes, exact-region measurement).
- `rust-ratio-writer-gap-20260829.csv`: same-session matched Go-vs-Rust
  nested-overwrite ratio 3.74 after the gap-machinery copy-elimination
  slice (Go 1,083.9 ms, Rust 290.0 ms), vs the earlier Rust baseline.
- `dispatch-removal-ab-20260830.csv`: interleaved Go-only A/B of the
  per-operation family-dispatch slice (nested-overwrite +10.52%,
  membership-import +6.06%, update-ipsets-workflow -0.78%); the
  measured regression rejected the family-dispatch removal and reused
  the result in the bounded safe-Go decision.
- `rust-ratio-writer-transport-20260830.csv`: same-session matched
  Go-vs-Rust nested-overwrite 2.31 (Go 723.7 ms, Rust 312.8 ms) after
  the result-transport and emitted split-path slices (exact-region
  profiling).
- `rust-ratio-reader-fixed-page-20260830.csv`: same-session matched
  Go-vs-Rust reads after the fixed-size `[4096]byte` page-view slice
  (live-direct-random-lookup 1.66, immutable-direct-random-lookup 1.63).
- `necessary-work-compare-20260830.csv`: the Go/Rust necessary-work
  counter comparison over the exact benchmark flows (36 active counter
  rows: 25 exact matches, 10 go-less, 1 go-more definitional reader
  leaf-validation, one genuine Go less-work win mapping_remaps 1 vs 3);
  the Go counters still under-count mapping-layer page visits and tree
  COW/cell bytes (tracked follow-up).
- `necessary-work-compare-20260830b.csv`: the SOW-0027 direction-item-2
  comparison at the mapped-page authority level (fresh Go and Rust
  harness runs at the final identity; supersedes the 20260830.csv
  under-count rows above): pages_visited 685,291 vs 688,025 (Go less;
  the previous row was 0 vs 688,025 "go-less unknown"; the delta is
  growth-step sequencing, mapping_remaps 1 vs 3, not per-edit fetch
  semantics), bytes_moved 69,145,640 vs 69,145,640 (EXACT MATCH) and
  bytes_zeroed 8,487,946 vs 8,487,946 (EXACT MATCH) after the final
  review fixed the same-size slotted replace phantom term (put.go) and
  instrumented the previously uncounted Meta.EncodeMapped
  (meta_encode.go, 230 moved + 4096 zeroed per encode mirroring Rust
  contract.rs), mapping_remaps 1 vs 3.
- `validation-phases-20260830.csv`: live-validation at 100k/1M/4M with
  the parent/worker/mapping/walk split (IPRANGE_WORKER_PHASES bench
  hook, v4work-only worker builds): elapsed 1.66x/1.62x/1.67x, peak RSS
  1.87x/1.40x/1.16x; the worker
  fixed cost is ~1.2-1.5 ms size-independent; worker pprof at 4M shows
  graph-walk frames only.
- `tree-header-parser-ab-20260830.csv`: interleaved same-session A/B
  of the authoritative expected-tree-header parser (slice retained):
  live/immutable lookups 0.931/0.911, nested-overwrite 0.992,
  membership-import 0.928. Column pairs are pristine/slice p50 ns,
  then slice/pristine allocated bytes, then slice/pristine peak RSS
  KiB (the middle labels were corrected 2026-08-30).
- `probe-key-u32-ab-20260830.csv`: interleaved same-session A/B of the
  width-guard-free fixed-search probe (slice reverted, neutral):
  live 1.010, immutable 0.994, nested-overwrite 1.003,
  membership-import 0.980; alloc calls equal within one call.
- `rust-ratio-final-20260830.csv`: the final-identity performance
  matrix of SOW-0027 direction item 1 - eight scenarios (membership-
  import, nested-overwrite, update-ipsets-workflow, IPv4 live and
  immutable direct lookups, IPv6 live and immutable direct lookups,
  live-validation), five alternating same-session release samples per
  scenario at commit 39df5b0b, medians and peak RSS. All eight CPU
  ratios fail the binding <=1.3x (1.322x-2.355x); two RSS ratios fail
  (1.351x, 1.402x). This CSV is the evidence package for the
  measured-performance decision that returns to the user.
- `rust-ratio-final-samples-20260830.csv`: the raw alternating
  Go/Rust rows behind the final matrix (p50 ns and peak RSS per
  sample, five per language per scenario); the validation rows show
  why the single-sample phase runs and the five-sample median differ
  (raw per-sample CPU ratios 1.16x-1.8x on a 12-16 ms operation).
