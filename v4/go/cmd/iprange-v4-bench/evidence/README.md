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
- `ci-go-v4-local-20260828.log`: the 4c/4d CI gate run against the new
  baseline (1 warmup + 3 samples, enforce mode), 18/18 within-limit,
  ratios 0.933-1.049.
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
