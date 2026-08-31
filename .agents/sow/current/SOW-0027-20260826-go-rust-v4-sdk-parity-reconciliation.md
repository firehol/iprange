# SOW-0027 - Go/Rust v4 SDK Parity Reconciliation

## Review Gate (user directive 2026-08-26)

Every milestone and slice gate under this SOW uses exactly five reviewers,
all of the lead's own model, running the final-review skill in adversarial
mode, each with one distinct scope. The Rust implementation is the baseline:
the reviewers check the Go work in contrast to Rust for all aspects, while
ensuring optimal Go idioms, performance, accuracy, interoperability, and
compatibility.

1. Rust parity: Go must do exactly what Rust does; the smallest divergence is
   a performance or behavior red flag. This includes mmap-only access and
   memory safety and lifetime ownership.
2. Go idioms: the work must be natural Go despite being identical in logic,
   system calls, philosophy, and structure to Rust.
3. Performance: the absolutely most performant way; even one unnecessary
   branch in the hot path is expensive.
4. Wire format and integrity: Rust/Go interoperability of the file format on
   disk, locking, coordination, and the shared conformance corpus.
5. APIs and records: public APIs, docs, errors, and records all in sync with
   Rust.

Reviewers are level-1 only (no level-2 tier). They are spawned once and kept
open for incremental delta re-reviews; they are restarted only between
milestones. No reviewer model other than the lead's own is used. These rules
live here at the top of the SOW, not in SWARM.md.


## Binding Performance Acceptance (user words, 2026-08-28; restored 2026-08-29)

The only user-set performance criterion on record, verbatim: "I think it is
reasonable to say that Go can be 20-30% slower [than Rust] and could use
20-30% more memory. But 4x, 5x, 7x, 9x I don't think is acceptable."

Therefore, binding for every gate in this SOW:

- Metric: all "CPU" acceptance ratios in this SOW are wall-clock
  elapsed-time ratios (Go time.Since / Rust Instant::elapsed on the
  same timed region); no instruction-level CPU metric is measured.
  The name "CPU" in the user decisions and older records means this
  elapsed-time metric.
- Elapsed time: Go/Rust same-session matched ratio must be <= 1.3x
  (the 20-30% envelope) for every measured scenario; >= 2x is an
  automatic failure.
- Memory: Go peak RSS must stay within +30% of Rust.
- Evidence: every acceptance claim needs same-session matched measurements
  (both harnesses on the same host, release builds, alternating runs, medians
  recorded) committed as a CSV under v4/go/cmd/iprange-v4-bench/evidence/.
- The numeric envelopes formerly recorded as "the accepted milestone-4
  mandate" (reads ~1.2-1.6x, validation ~1.5-2x, writes ~2-3.5x) were
  assistant-authored inferences from the user's words, never approved by the
  user; they are VOID and must not be cited as acceptance criteria. The
  regression review's finding "performance acceptance was bypassed" stands
  and is resolved only by this criterion.
- User-confirmed interpretation 2026-08-29 (requested by the external
  review because the exact gate does not follow mechanically from the
  quote): CPU acceptance is option A - <= 1.3x for EVERY substantial
  acceptance scenario, with no blended-overall fallback; memory
  acceptance is option A - peak RSS <= 1.3x, while allocation counts
  and allocation traffic remain diagnostic only and are not a gate
  (Rust performs zero allocations in the lookup path while Go performs
  one 16-byte setup allocation, so a count gate is not comparable).
- Current honest position against the binding criterion (final identity
  matrix, matched host, 2026-08-30, rust-ratio-final-20260830.csv, five
  alternating same-session release samples, medians): membership-import
  1.529x, nested-overwrite 2.355x, update-ipsets-workflow 1.925x,
  live-direct-random-lookup 1.525x, immutable-direct-random-lookup 1.582x,
  live-direct-random-lookup-v6 1.326x, immutable-direct-random-lookup-v6
  1.357x, live-validation 1.322x. ALL EIGHT scenarios fail the <=1.3x
  elapsed-time criterion; peak RSS passes six of eight (live v4 lookup 1.351x and
  live-validation 1.402x fail). This matrix is the evidence package for the
  measured-performance decision that returns to the user at the closure decision
  point; no scenario is inside the envelope, so the SOW stays in-progress
  until that decision is recorded.

## Status

Status: in-progress

Sub-state (2026-08-29): reopened by the regression review recorded in
the appended "Regression - 2026-08-29" section. The close was accepted
back into current/ because the external review's verified findings
(performance acceptance, worker containment, mixed-language evidence,
parity gate, final-review scope) invalidate the completed close.

User decision 2026-08-29 (performance closure, regression item 1):
option A - implement the remaining performance work. Read path:
per-width probe specialization inside the single tree core
(target lookups ~1.4-1.5x). Write path: per-family specialization of
the gap/replace machinery via a code generator (one authoritative
generic source, monomorphized per-family output like Rust; target
nested-overwrite inside the 2-3.5x envelope). Validation: re-measure
after the probe work and profile the worker-spawn share. Every step
is measured against the 18-case CI gate and the matched Rust-ratio
matrix at that identity.

User decision 2026-08-30 (language floor, closure decision point):
the bounded safe-Go leads were measured-and-rejected for the write
path, but the external single-sol review rejected the floor claim and
the user chose option 2 - keep SOW-0027 open for a BOUNDED continuation
(not open-ended optimization). Binding CPU <=1.3x Rust per scenario and
peak RSS <=1.3x remain in force; no unsafe code is authorized. The
bounded scope (recorded from the review direction):

1. One complete final-identity matrix: membership-import, nested-
   overwrite, update-ipsets-workflow, IPv4 live+immutable lookups,
   IPv6 live+immutable lookups, live-validation; CPU and peak RSS for
   every scenario; five alternating Go/Rust release samples, same host.
   Historical validation/RSS measurements are not acceptable for the
   final identity.
2. Complete necessary-work instrumentation: writer page visits,
   copied/moved bytes, zeroed bytes, probe/validation sites with
   equivalent Go/Rust definitions; "Go-less because Go does not count
   it" is unknown, not parity.
3. Profile validation properly: separate parent setup, worker
   startup/handshake, mapping, and graph walk; measure multiple sizes;
   recompute CPU and RSS at the final identity.
4. Two bounded safe-Go A/B experiments: (a) one authoritative
   expected-tree-header parser mirroring Rust inspect_tree_header so
   tree paths validate exact branch/leaf type, auxiliary value, level,
   and geometry without the general IsBranch classification followed by
   another exact-type check; (b) an IPv4 fixed-search loop with the
   safe fixed-page view and slot/key load integrated directly into the
   generated loop. Retain only if measurement and assembly show a real
   win. No unsafe code.
5. Repair SOW tracking: remove the stale "complete/accepted write
   envelope" Outcome; update SOW-0030 to cover every accepted residual
   under the binding <=1.3x CPU/RSS contract or keep the work inside
   SOW-0027; give the IPv6 benchmark and counter-parity work real SOW
   ownership.
6. Run the five-reviewer final round on the actual final commit after
   all evidence and record repairs; record explicit delta PASS
   verdicts.

Return for the measured-performance decision only after these items are
complete; the upper-bound calculation must cover all remaining
dominant Go-only costs, not only the estimated 2-3% store-dispatch
class.

Sub-state (2026-08-30, direction item 6 - five-reviewer delta round
on the final identity): the five level-1 reviewers (same scopes and
models as the standing Review Gate) re-reviewed the actual final
commit range 516f3fa8..33b3a8dd with the final-identity evidence.
Initial verdicts:

- Boole (Rust parity): FAIL - one fixable P1: the same-size slotted
  replace counted a phantom start-upper "moved" term unconditionally
  while Rust keeps copy_within inside the shrink != 0 guard
  (v4/go/internal/format/put.go; Rust slotted_page.rs). The P1 is
  fixed in the follow-up commit: Go now counts len(cell)+4 on a
  same-size replace, the pin is corrected (put_work_test.go), and
  the re-measured bytes_moved is 69,145,410 vs Rust 69,145,640
  (-230 bytes, 0.0003%; the +3.4% over-count is gone). Boole also
  corrected the pages_visited narrative (growth-step sequencing,
  not per-edit fetch semantics) and passed the parser, FinishEdit,
  IPv6-bench, and reverted-probe items. Re-review of the P1 fix is
  recorded in the next sub-state.
- Sagan (Go idioms): PASS - two P3s fixed in the follow-up commit:
  stale RestoreDirty doc-comment names replaced with FinishEdit
  across the tree/writer tests, and the ParseTreeHeader doc wording
  ("pass nil" -> "pass checkLevel=false"). Idempotence of the
  generator re-emission and gofmt were verified.
- Hegel (performance): PASS - the final matrix is internally
  consistent (every median recomputed from the raw alternating
  samples), the measured-result claim is evidence-backed (bounds-check
  elimination is the only remaining lever on the closest scenarios
  and requires unsafe), the retain rules were applied, and an
  independent 3-sample spot-check of nested-overwrite reproduced
  the committed medians within noise. P3 notes: the bytes_moved
  attribution, closed by Boole's P1 fix above, and the dual
  validation-ratio narrative, reconciled in the item-3 sub-state.
- Herschel (wire/integrity): PASS - conformance corpus (14 fixtures,
  both directions), the 9/9 mixed-language modes per direction, the
  FinishEdit Inspect-view audit, and the wire-format diff all clean.
  P3 notes accepted (not fixed, deliberate): unreachable defensive
  corruption messages remain as fail-dangerous guards, and
  corrupt-page error text changed at the parser migration while
  valid-page acceptance is identical.
- Turing (APIs/records): FAIL - one P2: the Milestone-5 deliverable
  record block still cited pre-final-identity numbers and the
  (voided) SOW-0030/SOW-0017 dependency. Fixed in the follow-up
  commit (final matrix numbers, dependency removed); the dual
  validation-ratio P3 is reconciled in the item-3 sub-state.
  Re-review of the record fixes is recorded in the next sub-state.

The record-only repairs (Boole P1, Sagan P3s, Turing P2/P3) land in
the follow-up commit with this sub-state; final delta re-review
verdicts are appended here after they are issued.

Delta re-review (commit 61eb4109):
- Turing (APIs/records): PASS. The stale Milestone-5 deliverable
  record cites the final-identity matrix and the voided SOW-0017
  dependency is gone; the validation-ratio reconciliation is
  recorded; stale-number greps (1.66/1.63/2.31/2.299, "blocked by
  SOW-0017", "is complete") return only historical/void-marked hits.
  P3 (non-blocking): the final-matrix raw samples were not committed
  - closed by committing rust-ratio-final-samples-20260830.csv
  (commit af397bc5), whose medians reproduce the matrix exactly and
  whose validation rows show the 1.16x-1.8x per-sample spread.
- Boole (Rust parity): VERIFIED, then two more findings found and
  closed. The same-size slotted replace phantom term (P1-1) is fixed
  and the re-measured bytes_moved dropped from +2,385,418 to -230;
  the -230 moved and -4,096 zeroed bytes were then both attributed
  to the one uninstrumented mapped-page authority left in the
  evidence window: format.Meta.EncodeMapped (Rust counts 230 moved +
  4096 zeroed per meta encode through PageMut). meta_encode.go now
  mirrors those counts (commit ce718360); re-measured: bytes_moved
  69,145,640 = Rust EXACT and bytes_zeroed 8,487,946 = Rust EXACT.
  Final authority sweep: PASS for the evidence-window authorities
  with two recorded P3s, both closed in the follow-up commit: (a)
  immutable-output seal counted nothing - the seal byte count moved
  into the single authority format.SealPageChecksum (BytesMoved(8)
  mirroring Rust seal_mapped's two put_u32s; the draft-path caller
  duplicate removed), keeping the nested-overwrite numbers exactly
  unchanged; (b) writer/structure_table.go has no work counters -
  it is outside every evidence window (structure_* rows are all
  zero), so it is recorded as a tracked counter-parity note in this
  SOW rather than instrumented blind, and the "identical at every
  authority" parity claim is scoped to the evidence-window
  authorities.
- Sagan (Go idioms): PASS. RestoreDirty identifiers fully renamed to
  FinishEdit (zero occurrences remain), the ParseTreeHeader doc
  wording matches the real bool parameter, generator re-emission and
  gofmt verified at 61eb4109.
- Hegel (performance): PASS at 33b3a8dd (matrix consistency, the
  assessment, retain rules, spot-check); no delta re-review was
  needed for 61eb4109/af397bc5/ce718360 because the counter
  instrumentation is test-only and does not touch the measured hot
  paths (counters are v4work-only no-ops).
- Herschel (wire/integrity): PASS at 33b3a8dd; the counter fixes
  (put.go, meta_encode.go) are test-only, so no wire re-check was
  required (meta_encode.go writes the same bytes; only the
  v4work-only counters were added).

Sub-state (2026-08-30, direction item 5 - tracking repair applied):
the stale close records are corrected in this same commit. The Outcome
section no longer claims completion or cites the voided write envelope; it
now states the true position (implementation delivered, performance
acceptance not met, measured-performance decision pending the user). The
"Current honest position" block above carries the final-identity numbers.
The Followup section de-stales the completed v6-bench and counter-parity
bullets (those are done inside this SOW; see the item-1 and item-2
sub-states) and keeps only the items that genuinely remain. Pending
SOW-0030-20260829-v4-read-and-validation-specialization.md is rewritten
to the actual residuals under the binding <=1.3x elapsed/RSS contract,
its voided 1.2-1.6x envelope and its false SOW-0017 dependency are removed,
and its scope now owns whatever the user does not accept at the
measured-performance decision point. The 4a A/B CSV columns that were labeled
alloc_calls/alloc_bytes but held alloc_bytes/rss_peak_kib are re-labeled
(tree-header-parser-ab-20260830.csv), and the bench evidence README lists
every artifact of the bounded continuation.

Sub-state (2026-08-30, direction item 1 - final-identity matrix
completed): IPv6 direct random-lookup scenarios were added to both
benches and the complete same-session matrix was measured at the final
identity (commit 39df5b0b + rust-ratio-final-20260830.csv with the raw
alternating rows in rust-ratio-final-samples-20260830.csv). Go bench
(cmds in scenario_read.go, registered as *-direct-random-lookup-v6):
readSeededDirectV6, randomPointsV6, countRandomPointsV6. Rust bench:
scenarios/read.rs and scenarios.rs helpers plus direct.rs
seeded_direct_v6. Matrix: eight scenarios x five alternating same-session
release samples on the same host (1M records, medians):

  scenario                         Go p50 ms   Rust p50 ms   Elapsed x   RSS x
  membership-import                    70.4         46.0    1.529   1.162
  nested-overwrite                    685.1        290.9    2.355   1.130
  update-ipsets-workflow             2084.2       1082.6    1.925   1.090
  live-direct-random-lookup           332.0        217.7    1.525   1.351
  immutable-direct-random-lookup      311.8        197.0    1.582   1.294
  live-direct-random-lookup-v6        509.2        384.1    1.326   1.140
  immutable-direct-random-lookup-v6   448.0        330.1    1.357   1.082
  live-validation                      16.2         12.3    1.322   1.402

All EIGHT elapsed-time ratios fail the binding <=1.3x; peak RSS fails two of eight
(live v4 lookup, live-validation). This matrix supersedes every earlier
acceptance table for the measured-result claim (the 20260828g historical matrix was
an earlier identity and is archival only). No bounded safe-Go lead remains
unmeasured: the direction-item-4 A/Bs (authoritative tree-header parser,
KeyU32 probe) were measured at this same identity and only the parser was
retained; the remaining Go-only costs are per-page header decode, probe
extent validation, the Go runtime/GC share of the write machinery, and
the validation walk - each quantified in the sub-states below and in the
validation-phase and necessary-work evidence. The measured-performance decision
returns to the user after the five-reviewer delta round (direction
item 6).

Sub-state (2026-08-30, direction item 3 - validation profiling):
the validation cost is now separated into parent orchestration, worker
startup/handshake, mapping, and graph walk at three sizes, with elapsed
and peak RSS recomputed at the current identity. Bench tooling:
IPRANGE_WORKER_PHASES=<path> makes the spawned iprange-v4-worker append
phase rows (verify, ready, running, handler, dispatch, mode) for the
control handshake, fault-handler install, and the measured operation
(cmd/iprange-v4-worker; bench-only, no SDK change). The hooks lived in
main.go during the measurement and moved to v4work-tagged files
(phases.go no-ops / phases_v4work.go) in the 2026-08-31 record repair,
so the production worker binary carries no profiling or phase
machinery (verified by strings on the built binary); the bench builds
the worker with -tags v4work so the evidence methodology is
unchanged. Matched
same-session runs (validation-phases-20260830.csv) at 100k / 1M / 4M:
Go p50 6.47 / 16.55 / 53.76 ms vs Rust 3.91 / 10.23 / 32.27 ms (elapsed
1.66x / 1.62x / 1.67x) and peak RSS 12.9 / 29.6 / 81.9 MiB vs 6.9 /
21.2 / 70.7 MiB (1.87x / 1.40x / 1.16x; the small-size RSS gap is the
Go runtime floor of the worker process and converges toward the 1.3x
envelope at scale). Worker phases are size-independent (~1.2-1.5 ms
fixed: verify ~40 us, ready ~75 us, running ~1.15 ms, handler ~60 us,
dispatch ~20 us) and the mode phase (mapping + walk) scales with the
graph: ~2.8 ms / 100k, ~15 ms / 1M, ~51 ms / 4M. Worker CPU profiles at
4M show only graph-walk frames (validation.Validate -> mapping.Probe ->
InspectLayout -> walkRangeLeaf4/inspectFixedExtents/markExtent); no
mmap, syscall, or handshake frames, so the mapping open is microsecond-
scale and the walk is the entire measured cost. The historical 2.299x
validation ratio (20260828g) is superseded at the final identity: the
validation-phases rows are single-sample runs (1.66x / 1.62x / 1.67x at
100k / 1M / 4M), while the acceptance matrix median for the 1M scenario
is 1.322x (rust-ratio-final-20260830.csv, five-sample; the committed raw
rows in rust-ratio-final-samples-20260830.csv show a per-sample
elapsed range of 1.16-1.8x on a 12-16 ms operation, which is why the medians
differ); the five-sample matrix number is the acceptance evidence. The residual vs the binding <=1.3x is the walk itself, not
containment overhead.

Sub-state (2026-08-30, direction item 2 - necessary-work parity
completed): the counter-coverage gaps are closed at the mapped-page
authority level, mirroring the Rust counting points (mapping.rs
page_visited; mapped_bytes.rs PageMut byte accounting; slotted_page.rs
mutation primitives). Changes: (a) work.PageVisit moved to the single
mapping-layer full-page view owner, mapping.Mapping.Page, so writer and
reader paths share one page-visit definition (reader.page keeps the
PageParse count); (b) BytesMoved/BytesZeroed are now counted inside every
mapped-page write authority with the Rust per-call formulas: format
InitializePageHeader (zero PageSize + 27 header bytes) and the Slotted
insert/replace/remove/remove-fixed-range/truncate/truncate-fixed/
builder/appender/adjustSlotsBefore family (identical lengths to
slotted_page.rs), tree CopyForCow (full page + born + checksum-clear),
updateField, draft claim/discard/seal, prepare reserve fills, bitmap word
and count writes, metadata chunks, blob leaf init, and immutable
workspace markers; (c) the edit tag stamp now reuses the Update page view
(new Store.FinishEdit(page, tag) replacing RestoreDirty(pageNumber, tag)
throughout the tree/bitmap/writer flows), mirroring Rust update_page/
copy_page closures that fetch the page exactly once per edit. Result
(necessary-work-compare-20260830b.csv, nested-overwrite 100k, fresh Go
and Rust harness runs): pages_visited 685,291 vs 688,025 (-2,734,
0.4%; the mapping-layer visit definition is identical on both sides and
both languages fetch exactly once per edit - the small delta is
growth-step sequencing, mapping_remaps 1 vs 3, remap-time page views;
the earlier "one fewer page per edit" wording was corrected at final
review); bytes_moved 69,145,640 vs 69,145,640 (EXACT MATCH) and
bytes_zeroed 8,487,946 vs 8,487,946 (EXACT MATCH) after two
counter-authority fixes found by the final-round Rust-parity reviewer:
(a) the same-size slotted replace counted a phantom start-upper
"moved" term unconditionally while Rust keeps copy_within inside the
shrink != 0 guard (fixed in v4/go/internal/format/put.go, pin
corrected in put_work_test.go); (b) format.Meta.EncodeMapped had no
work counters at all - Rust counts 230 moved + 4096 zeroed per meta
encode through PageMut (contract.rs encode_fields fill(0) plus the
field and CRC writes), which was exactly the previously "un-attributed"
-230 bytes and the -4,096 zeroed bytes (fixed in
v4/go/internal/format/meta_encode.go). Every other counter row matches
or stays go-less (mapping_remaps 1 vs 3; leaf_validations -199,997 and
cell_probes -1,361 report strictly less redundant validation/probe
work on identical semantics). Live-direct-random-lookup rows are
unchanged (3M pages visited/parsed on both sides). Pins updated in
draft_work_test.go, open_work_test.go, put_work_test.go to the new
authority counts. Both full suites (plain + v4work), vet, gofmt, and
genfamilies idempotence pass.

Sub-state (2026-08-30, direction item 4a - authoritative
expected-tree-header parser): one expected-tree-header parser now owns
every slotted tree path. format.ParseTreeHeader (inspect.go) validates
common header, born transaction, exact expected branch/leaf type and
aux by level, level bound/expected level, and canonical slotted shape
in one self-contained pass (Rust slotted_page::inspect_tree_header/
parse_tree); format.InspectTreeHeader composes the same authority for
the classified validation/recovery form. The reader range lookups and
walks, catalog name+index descents, membership-ID descent, cursor
readPage/resume, and the tree parse[T]/generated parseRange4/6 all
route through it, removing the general DecodePageHeader IsBranch
classification plus the second exact-type re-check/switch per page.
Escape verified from the A/B rows: allocated bytes and peak RSS
identical within noise (0.03% / ~1%), no new per-page allocation. Interleaved same-session A/B vs pristine HEAD
(5x1 each, evidence/tree-header-parser-ab-20260830.csv):
live-direct-random-lookup 0.931x, immutable-direct-random-lookup
0.911x, nested-overwrite 0.992x, membership-import 0.928x. Reads
improve ~7-9% and writes are neutral, so the slice is retained. Both
full suites (plain + v4work), vet both modes, gofmt, race on reader,
and genfamilies idempotence pass.

Sub-state (2026-08-30, direction item 4b - KeyU32 probe A/B): the
second bounded safe-Go experiment removed the per-probe cellLen guard
from the IPv4 fixed-search probe: format.FixedSearch.KeyU32 performs
the persistent-offset extent check but no width guard, mirroring Rust
lower_bound_fixed probing cell_at on every iteration while the loop
hoists the width proof once. The reader greatestFixedV4 loop and the
generated U32 tree probe (genprobe) switched to KeyU32. Interleaved
same-session A/B vs pristine HEAD (5x1 each,
evidence/probe-key-u32-ab-20260830.csv): live-direct-random-lookup
1.010x, immutable-direct-random-lookup 0.994x, nested-overwrite
1.003x, membership-import 0.980x. All four medians sit inside the
run-to-run noise band (+/-2%); inlined assembly shows the saved width
cmp/branch per probe is negligible. Per direction item 4's retain
rule ("only if measurement and assembly show a real win"), the slice
is reverted; the codebase keeps the single guarded U32 accessor. No
allocations or RSS change in either build. Full plain and v4work
suites, vet both modes, gofmt, and genfamilies/genprobe idempotence
pass at the reverted tree. The remaining read-path cost structure is
unchanged: per-page header decode and per-probe extent validation,
not the width guard. (Note: the 4a
evidence CSV's middle columns were initially labeled alloc_calls/
alloc_bytes but held alloc_bytes/rss_peak_kib; the header was corrected
2026-08-30 to slice_alloc_bytes/pristine_alloc_bytes/
slice_rss_peak_kib/pristine_rss_peak_kib. The 4a rows carry no separate
alloc-calls column; the 4b A/B at the same identity shows call counts
equal within one call.)

Sub-state (2026-08-29, read-path probe slice): the per-width probe
specialization (regression item 1, first half) is implemented and
committed. internal/tree/genprobe emits four width-specialized
fixed-cell search loops (fixedLowerBoundU32/U64/U64U32/U128) from one
authoritative template (gen.go: `go generate ./internal/tree`), and
lowerBound dispatches 4/8/12/16-byte codecs to them. The A/B
measurement (pristine HEAD vs the slice, alternating runs of the
direct-random-lookup scenarios) shows the probes are performance-
neutral on the v4 read path: live-direct-random-lookup 393.9 vs
391.0 ms medians, immutable 362.3 vs 359.9 ms (noise-level). Profile
evidence explains why: the direct-lookup reader path (LookupDirect4)
uses its own reader-side greatestFixedV4/V6 search loops in
internal/reader/search.go, not the tree probe; the tree probes serve
the writer and cursor paths. The remaining read-path costs are
per-page header decode / slotted-page open and per-probe cell
validation, not the compare.

REGRESSION FOUND AND FIXED IN THIS SLICE: the first generator draft
emitted the u128 compare in raw limb order (high limb at cell offset
0), but the wire u128 layout keeps the LOW limb at offset 0 and the
high limb at offset 8 (format.U128/PutU128). Every specialized v6 tree
search therefore inverted asymmetric limbs: IPv6 range tree lookups
were silently wrong and the writer's rangesV6 test walk looped
forever, allocating tens of GB until the OOM killer fired. Fixed: the
emitted compare decodes through format.U128. Pinned by two new tests
in internal/tree/probe_widths_test.go: TestWidthProbesMatchGenericSearch
(per-width specialized vs generic equivalence over asymmetric keys)
and TestV6EndpointWalkTerminates (the exact endpoint scenario that
previously OOM'd). Both fail with the buggy compare and pass with the
fix. The full plain and v4work suites pass with a 32 GB address-space
cap (the previous un-capped writer run peaked above 60 GB anonymous
RSS on this scenario).

Sub-state (2026-08-29, write-path generator design): the nested-overwrite
residual is mapped. Fresh profile at the probe slice (1M nested-
overwrite, 1.17 s total): insertPrivateGap cum 660 ms (56%), the gap
machinery (InsertIfLocalGap cum 620 ms, 53%) and the strict-replace
chain (ReplaceLocalPredecessorWith cum 260 ms) dominate; runtime itab
lookups alone cost 90 ms (7.7%). The Rust reference monomorphizes the
same chain (range_mutation.rs over RangeCodec<Ipv4Key>/<Ipv6Key>),
eliminating every per-record interface hop; Go pays rangeFamily,
LocalGap, Codec, and ReadKeyLimbs interface dispatch plus per-record
Key copies and double cell decodes at each seam.

Slice I-1 design (implementing user option A, item 2): a second
generator, internal/tree/genfamilies (mirroring the genprobe pattern),
emits per-family concrete gap/replace layers in internal/tree -
range_gap_v4.go and range_gap_v6.go - each carrying the concrete range
family (key/record encode-decode, Next/Previous/Less/Equal, KeyOf), the
private-gap probe, the gap selector, and family-specialized
insertIfLocalGap / insertIfCachedInteriorGap / insertRejectedGap /
replaceLocalPredecessorWith / replaceLocalRun entries with the codec
and probe calls inlined (Rust's range_tree.rs lives inside the crate
for the same reason). The writer keeps its generic family interface for
the non-emitted paths; the existing per-family DraftStore entry points
(assign4/6, assignInput4/6, clear4/6) route to the emitted entries
through thin writer-side wrappers. Behavioral equivalence is enforced
by differential tests (emitted versus generic on the memory store over
randomized assign/clear/transform runs) plus the existing work-counter
pins, conformance, and full suites. Success targets: nested-overwrite
inside the 2-3.5x Rust-ratio envelope (today 4.04x/1M, Go 1,166-1,247
ms vs Rust ~298-305 ms), then re-measure membership-import and
update-ipsets-workflow. Slice I-2 (follow-up): the strict-replace
writer chain (replaceStrictlyInside/replaceStrictCells/trim*/coalesce)
through the same emitted seam.

Slice I-1a delivered (2026-08-29): the canonical range family now lives
in internal/tree/range_family.go (RangeKey4/RangeKey6/RangeRecord/
RangeFamily/RangeCodec4/RangeCodec6/RangePrivateGap - Rust range_tree.rs
lives inside the crate for the same reason), and the writer's
range_codec.go aliases those types (key4/key6/rangeRecord/rangeFamily/
rangeCodec4/rangeCodec6/privateGap). The writer's field accesses were
renamed to the exported forms (from/from.From, to/To, value/Value,
hi/Hi, lo/Lo, family/Family, r/R) under compiler guidance; only
writer-local structs (change, segment, optionalValue, incomingRange,
valueKind) keep their unexported fields. Behavior-neutral: plain and
v4work full suites green, nested-overwrite timing unchanged. This
shared type identity is what lets the emitted per-family gap layer
return the same LocalInsert/LocalReject types the writer's generic
machinery consumes.

Slice I-1b delivered (2026-08-29): the second generator,
internal/tree/genfamilies (gen.go: `go generate ./internal/tree`), emits
the per-family concrete gap/replace layer - range_gap_v4.go and
range_gap_v6.go - from one authoritative template: the concrete
rangeProbe4/6 (LocalGap by value), gapSelector4/6, and the exported
family seams InsertIfLocalGap4/6, InsertIfCachedInteriorGap4/6,
InsertIfEdgeGap4/6, InsertRejectedGap4/6, ReplaceLocalPredecessorWith4/6,
ReplaceLocalRun4/6 (record-based probe construction, codec calls
inlined). The writer routes every gap-layer tree call through the
per-family seam of internal/writer/range_specialize.go (gapLocalInsert,
gapCachedInterior, gapEdgeInsert, gapRejectedInsert, predecessorReplace,
localRunReplace), with the generic gap.go entries kept as the reference
fallback; the pre-widened ctx.storeView is used at all tree-core call
sites and insertBranch's common path no longer allocates its two
branch-cell buffers (the split path keeps the general replacement
machinery). Equivalence is enforced by the new v4work differential
tests TestRangeFamilyEmittedMatchesGenericV4/V6: 800 randomized
mutations per family replayed on two memory stores (local gap, cached
interior, edge, rejected-completion, predecessor-replacement and
local-run operations), each step agreeing on outcome, per-operation
work counters (all Snapshot fields), and byte-identical page state.
Full plain and v4work suites pass with the 32 GB address-space cap.
Measured at this identity (matched host, run 2026-08-29): nested-
overwrite 1M Go 1,194-1,238 ms (5 runs, median ~1,210) vs Rust ~303 ms
(4.0x, unchanged from 1a); membership-import and the read paths
unchanged. Fresh profiles show the residual is not dispatch: the
emitted layer and the generic layer profile within noise of each other,
and the wall is spread across the writer wrapper chain (insertPrivate-
InputGap/insertPrivateGap/gapLocalInsert flat ~280 ms total), the
by-value LocalInsert/LocalReject/privateInputInsert copies through
those wrappers, and the descent/search/slot machinery itself. Slice
I-2 (strict-replace chain through the same family seam) remains the
next step; a chain-folding pass (fewer 300-byte by-value reject copies
per record) is expected to be the larger lever of the two.

Slice I-1b follow-up, pointer-folded rejection proof
(2026-08-29): profiling after I-1b showed the residual was not
dispatch - the ~340-byte LocalReject proof returned by the gap layer
was copied by value at every wrapper frame of the overwrite chain
(insertPrivateInputGap/insertPrivateGap/gapLocalInsert -> assignWithHint
-> rangeReplaceWithHint -> replaceStrictlyInside -> replaceStrictCells
-> predecessorReplace), roughly eight 340-byte copies per record.
The writer now threads the rejection by pointer: privateInput owns one
reused rejectSlot, insertPrivateInputGap copies the proof into it once
and returns *LocalReject, the whole replace chain passes the 8-byte
pointer, and noteRejection takes only the presence boolean through the
new pointer-receiver LocalReject.HasLocalNeighbor (no value-receiver
copies). Range assignments and clears pass nil. Measured at this
identity (matched host, 1M, same-session medians): nested-overwrite
Go 1,083.9 ms (7 runs) vs Rust 290.0 ms (3 runs) = 3.74x (3.44x
against the older accepted-baseline.csv reference of 314.9 ms). Both
ratios FAIL the binding <=1.3x criterion restored 2026-08-29 (the
2-3.5x write envelope was assistant-authored and voided); the
improvement is real - down from 4.04-4.2x and from 1,166-1,247 ms to
a 1,074-1,092 ms band - but the write gate stays FAIL. No other scenario regressed: membership-import 1.73x,
update-ipsets-workflow 2.21x, live-validation 1.21x, live and
immutable direct lookups 1.74-1.91x. Against the binding <=1.3x
criterion every scenario except live-validation fails; the read
residual is under active optimization (FixedSearch value-receiver and
per-probe 64-byte copies were identified by the external review and
are being A/B-tested, not declared a floor). Evidence:
v4/go/cmd/iprange-v4-bench/evidence/rust-ratio-writer-gap-20260829.csv.
Full plain and v4work suites pass with the 32 GB address-space cap.

Read-path inlineable probe slice (2026-08-29, binding-criterion
tracking): the external review's read-path leads were implemented and
measured instead of declaring a floor. format.FixedSearch now uses
pointer receivers; the typed accessors U32/U64/U64U32/U128 return
(value, bool) with a pre-built errFixedCellOutside so the compiler
inlines them into the search loops (verified via objdump: no per-probe
call; U128 stays 7 cost over the inline budget and remains a call, v6
follow-up). The reader's greatestFixedV4/V6 consume the typed probes
and the tree's emitted fixedLowerBoundU32/U64/U64U32 loops were
regenerated on the same accessors (validated codecs of any width keep
the generic validated loop). Measured at this identity (matched host,
same-session medians, evidence
rust-ratio-reader-inline-20260829.csv): live-direct-random-lookup Go
373.0 ms vs Rust 223.0 ms = 1.67x (was 1.74-1.91x); immutable-direct-
random-lookup Go 340.7 ms vs Rust 205.7 ms = 1.66x (was ~1.91x).
PGO builds measured as noise (~1%, rejected). Both full suites pass
under the 32 GB address-space cap. Honest position against the
binding <=1.3x criterion: reads improved ~10-13% but are still 1.66x;
the residual is the per-lookup descent + header decode + per-probe
persistent-slot validation and safe-slice overhead against Rust's
unchecked loads (slotted_page.rs cell_at is unsafe with the same
validation); reaching 1.3x in safe Go is not evidenced and likely
requires either unsafe or a different search structure - tabled for
the user with the evidence when the remaining slices are done.

Read-path U128 inline completion (2026-08-29, same slice): the v6
follow-up recorded above is resolved. FixedSearch precomputes maxStart
(PageSize - cellLen) once at construction, and the U128 probe drops its
standalone cellLen<16 early return in favor of the width guard folded
into the single extent check (start < upper || start > maxStart ||
cellLen < 16), keeping the Rust cell_at extent semantics and the
no-out-of-page guarantee with one return path. Compiler-verified: the
U128 probe is now inlineable (cost 80, was 84-87) and
greatestFixedV6/fixedLowerBoundU128 contain no per-probe call
(objdump: only runtime.panicBounds guards remain; the remaining
bounds checks are the documented safe-slice overhead vs Rust's unsafe
cell_at loads). The benchmark matrix is IPv4-only for read scenarios
(readSeededDirect seeds AddressFamilyIPv4 in both languages), so the
v6 probe fix has no numeric matrix delta; the latest matched
interleaved samplings (5 Go + 5 Rust each) sit inside the variance of
the committed reader evidence: live Go 370.8 ms / Rust 234.3 ms =
1.58x and immutable Go 348.3 ms / Rust 199.4 ms = 1.75x vs the
committed 1.67x / 1.66x (Rust drift 223-247 ms live across sessions).
A v6 read scenario for both benches is a listed follow-up, not part
of this slice. Both full suites pass under the 32 GB address-space
cap.

Slice I-2 first pass, emitted descent and replacement application
(2026-08-29): the profile after the pointer-fold showed the residual
was inside the EMITTED entries' generic helpers, not the seam: the
family entries called the generic privatePathSelect (cum 310 ms),
lowerBound wrapper, branchChild/codecCell, and replaceTarget/
applyReplacement, and the runtime itab lookups cost ~100 ms on their
own. The generator (internal/tree/genfamilies) now emits the
tree-core pieces with the family constants folded: rangeCellSize,
parseRange (type/discriminator checks close over the family), branchChild
and replaceBranchChild (fixed key-plus-child entry decode/rebuild),
privateRangePath (the full descent: emitted parse + direct
fixedLowerBoundU32/U128 + emitted branchChild; COW touch stays
shared), replacementFits, applyCells, applyReplacement (fits path
inlined; the rare split path keeps the generic reference machinery),
and replaceTarget with family routing. InsertIfLocalGap routes its
descent and leaf bound through the emitted path; ReplaceLocalPredecessorWith
routes its replacement through the emitted replaceTarget. Equivalence
is enforced by the existing differential tests
(TestRangeFamilyEmittedMatchesGenericV4/V6: 800 randomized mutations
per family, per-operation work counters and byte-identical page state)
plus the full plain and v4work suites, vet, and gofmt. Measured at
this identity (matched host, 1M, runs 2026-08-29): nested-overwrite
Go 1,014-1,046 ms (median ~1,020 ms, down from the 1,074-1,092 ms
band) vs Rust 285.6-306.1 ms (median 296.4 ms) = ~3.5x (was 3.74x).
Fresh profile at this identity: InsertIfLocalGap cum down from 550 to
~340 ms, the writer wrapper chain (gapLocalInsert/
predecessorReplace flat ~80-90 ms each) and the seam's 340-byte
by-value LocalReject interface assertions are now the largest flat
items; the descent is ~180 ms and the emitted replacement application
~200 ms. The remaining wall is per-op machinery overhead in safe Go
(one draft txn, so no COW page copies are in the profile): store-
interface calls, family-interface dispatch in the generic writer
chain, bounds checks, and header decodes. Write scenario stays FAIL
against the binding <=1.3x criterion; next bounded step is the
chain-folding of the remaining by-value reject/insert proof copies
through the seam (SOW-predicted larger lever), then re-measure
membership-import and update-ipsets-workflow, and the store-concrete
writer layer remains the larger structural option if the user
authorizes it.

Slice I-3, result-transport and emitted split path (2026-08-30,
external-review direction): the floor claim was premature; the
external review showed compiler-emitted struct copies (MOVUPS) at
scale - gapLocalInsert 1,024, InsertIfLocalGap4 645, predecessorReplace
360 - and the old whole-scenario profile included database
construction. Evidence tooling first: IPRANGE_CPU_PROFILE now brackets
exactly the timed operation() callback instead of the whole case
(cmds/iprange-v4-bench measure.go + driver.go), so profiles cover only
the measured region (98%+ sample coverage of the timed loop). Then the
result-transport slice: LocalGapOutcome carries the compact
inserted/page result; the emitted InsertIfLocalGap4/6 write the
~340-byte LocalReject through a caller-owned slot (the private input's
rejectSlot on the locator path); privateRangePath4/6 populate a
caller-owned *PrivateLeaf; ReplaceLocalPredecessorWith4/6 and
ReplaceLocalRun4/6 take *LocalReject so the seam's any(*rejected)
assertion (a 340-byte deref+copy per record) became an 8-byte pointer
assertion; the generic gap/replace entries stay as the differential
reference (default seam branch). Escape analysis verified: allocation
counts unchanged (27.7k for 1M ops, same as before). Second part of
the slice: the split path was NOT rare in this workload - the generic
splitReplacement/buildReplacement showed 100-120 ms cum (12%) - so the
generator now emits requireLeaf (fixed leaf size + direct decode),
codecCell, truncate, replacementCell, buildReplacement,
replacementSplitIndex, keepLeftReplacement, firstKey, and
splitReplacement (which drops the redundant page Inspect entirely);
applyReplacement routes the not-fits branch to the emitted splitter and
every emitted entry validates through requireLeaf. Measured at this
identity (same-session matched, 1M, exact-region): nested-overwrite
Go 723.7 ms vs Rust 312.8 ms = 2.31x (was 3.74x at the 2026-08-29
pointer-fold, 3.5x after I-2, 2.71x after the result-transport part;
MOVUPS in the hot chain down from 3,082 to ~1,650 total). differential
tests (800 randomized mutations, work counters + byte-identical pages),
both full suites, vet, and gofmt pass. Evidence:
v4/go/cmd/iprange-v4-bench/evidence/rust-ratio-writer-transport-20260830.csv.
membership-import measured 74-78 ms Go vs 44-47 ms Rust (~1.65x,
within session noise of the 1.44-1.73x band) and update-ipsets-workflow
~2.0x; the read scenarios are untouched by this slice. Direction item 3
(remove the per-operation writer family dispatch) is the next
measured step.

Direction item 3 measured and REJECTED - per-operation family dispatch
removal via codec type parameter (2026-08-30): the slice F-threaded
the whole writer range chain ([K any, F tree.RangeGapOps[K]] through
range_specialize/range_edit/range_locator/range_coverage and the six
emitted RangeGapOps methods on RangeCodec4/6), replacing every
switch any(ctx.family).(type) seam with compile-time-resolved direct
calls. Interleaved A/B at the same identity (5+5 matched runs per
scenario; pristine HEAD 726e205f binary built from git archive vs the
F-threaded worktree binary): nested-overwrite 703.0 ms (base) vs
777.0 ms (slice) = +10.5%; membership-import 75.9 ms vs 80.5 ms =
+6.1%; update-ipsets-workflow 2,191.9 ms vs 2,174.7 ms = -0.8%
(noise). Profiles of both binaries show the monomorphized chain
increases flat time in the emitted entries (InsertIfLocalGap4 flat
120 ms vs 70 ms) and loses the old binary's inlining of parseRange/
privateRangePath; the single type switch per seam cost less than the
code-bloat and inlining loss of full monomorphization. The slice was
reverted: the working tree is byte-identical to 726e205f (git diff
clean after restoring the 18 slice files), nested-overwrite back to
718.9 ms, both full suites + vet + gofmt green. Evidence:
v4/go/cmd/iprange-v4-bench/evidence/dispatch-removal-ab-20260830.csv.
Direction item 5 delivered - fixed-size [4096]byte reader page view
(2026-08-30): FixedSearch carried the mapped page as a []byte slice,
so every probe built a slice header and re-checked against the
mapping slice length, and the word loads went through
encoding/binary.littleEndian call frames (profiled flat 140 ms,
32%, in the exact-region direct-lookup profile). The view now carries
page *[PageSize]byte after the one explicit len(page)==PageSize check
in NewFixedSearch, and the typed accessors compose the little-endian
words from adjacent array bytes (no slice headers, no
littleEndian frames, constant-4096 bounds). The flattened byte
compositions exceeded the inliner budget (U32 cost 101 > 80, causing
a real call per probe); the kept form uses array-pointer slicing with
binary.LittleEndian (all accessors inlineable at cost 51-71).
Interleaved A/B vs pristine 726e205f (10+10 same-session runs):
live-direct-random-lookup 365.3 -> 355.9 ms (-2.55%),
immutable-direct-random-lookup 351.3 -> 347.1 ms (-1.21%),
nested-overwrite neutral (+0.37%). Matched Go/Rust ratios at this
identity (5+5): live-direct-random-lookup 1.66x, immutable
1.63x (was 1.67/1.66 committed; Rust drifts 210-262 ms across
sessions). Both full suites, vet, gofmt green; work counters
unchanged at every call site. Evidence:
v4/go/cmd/iprange-v4-bench/evidence/rust-ratio-reader-fixed-page-20260830.csv.

Direction item 6 delivered - test-only Go/Rust necessary-work
comparison (2026-08-30): deterministic workload harnesses dump the
full counter snapshot for nested-overwrite (100k live-writer
replacement) and live-direct-random-lookup (1M lookups over an
identical seeded 100k map) -
v4/go/cmd/iprange-v4-bench/necessary_work_v4work_test.go (go test
-tags v4work -run TestDumpNecessaryWork -v) and
v4/rust/iprange-livedb/src/necessary_work_tests.rs (cargo test
--lib necessary_work_tests -- --nocapture). Both sides use the exact
benchmark flows (same permutation, nested pattern, splitmix64 point
shuffle, reader_work repetitions). Result (36 active counter rows):
25 exact matches; 10 go-less (no Go counter exceeds Rust); 1
go-more (reader leaf_validations 1M vs 0 - Go counts the selected
leaf-record decode, Rust counts leaf_validation only on write-side
decodes: definitional, byte-identical page state proven by the tree
differential tests). Exact matches include tree_lookups, descents,
page_parses, write key_probes, edit_fit_probes, pages_created,
pages_copied, pages_split, first_fence_updates, leaf-locator hits,
pages_sealed, ranges_consumed/emitted/split, mapping_growths/
flushes, file_syncs, source passes, and every read topology counter
(1M lookups: 1M lookups, 2M descents, 3M page visits/parses on both
sides). go-less deltas are counter-coverage gaps in Go (writer
mapping-layer page visits 0 vs 688,025; COW/cell page bytes 444,092
vs 69,145,640 and zeroed 44,160 vs 8,487,946 - Go tree writes are
not byte-counted, Rust mapped_bytes counts every byte) and
counting-point differences (split-path probes -1,361, write-side
leaf-validation sites -199,997, reader extra-probe class -1,656,640
at identical topology); one genuine less-work win (mapping_remaps 1
vs 3). Evidence:
v4/go/cmd/iprange-v4-bench/evidence/necessary-work-compare-20260830.csv.
Follow-up (tracked): complete the Go counters to full Rust parity -
count mapping-layer page visits on writer paths and page bytes in
the tree COW/cell paths (internal/tree + draft_store CopyPage) -
updating the Go and Rust pins (draft_work_test.go, draft_store
tests) in one bounded slice; the performed-mapped-bytes comparison
is already negative on the Go side, so the counters will prove
equality only after that slice.

Direction item 4 measured and REJECTED - concrete-store writer A/B
(2026-08-30): the remaining write-side lead was the Store-interface
dispatch (ReadPage/TouchPage/Allocate/CopyPage) inside the emitted
tree entries. Exact-region profile at the current identity
(nested-overwrite 1M, bench process only - the earlier apparent
validation cost was an artifact of globbing the bench and spawned
worker profile files together; worker validation runs outside the
measured region via validate_output): the top costs are
fixedLowerBoundU32 flat 120 ms (17.6%), privateRangePath4 cum
220 ms (32%), the replacement cursor machinery, DecodePageHeader,
and parseRange; no itab/dispatch node appears in the top 22, so the
entire Store-interface dispatch class is bounded at ~2-3% by its
call count per operation, far below the 1.3x write gap (723.7 ms ->
~407 ms needed, -44%). Making the emitted entries concrete-store
would additionally repeat the monomorphization-bloat mechanism that
the codec type-parameter slice (measured +10.5% regression, recorded
above) already demonstrated. Sol direction items 1-6 are therefore
all delivered or measured-and-rejected; the write path stands at
2.31x and the read path at 1.63-1.66x with no bounded safe-Go lead
remaining that can close the 1.3x gap. Per the direction, the
measured-performance decision returns to the user with this evidence
package at the SOW-0027 closure decision point; meanwhile the
remaining milestone-5 closure items (mixed-language coordination
matrix, real Rust-export parity gate, final five-reviewer round,
one-commit close, lessons) continue. The pre-existing 35 Rust lib
test failures (recovery/immutable_output/worker, identical on
pristine HEAD, environment-dependent) are recorded for the closure
round; Go plain + v4work suites, vet, gofmt all green at 30a30549.

Mixed-language coordination matrix completed (2026-08-30): the
external-review gap (sol: the five mixed modes did not explicitly
cover stale reader slots and sidecar replacement) is closed by
extending both directions of the battery
(v4/go/mixed_live_test.go + v4/rust/iprange-livedb/tests/
mixed_live.rs). The pinned mode now holds the foreign child's slot
across TWO parent replacements (generations 3 and 4), asserting
Reclaim NoChange after each and progress after release: the stale-slot
reservation survives repeated sidecar replacements. A new sidecar
mode in both directions proves the external reader sidecar is a full
replacement, not an append: with a held reader slot the sidecar
content changes across a commit while its length stays canonical (one
header page + capacity 16-byte slots = 4160 bytes at capacity 4; the
no-reader case rewrites identical bytes in place, so the held-slot
shape is the observable replacement). Full matrix per direction:
reader interop, writer exclusion (locks), stale slots (pinned across
two replacements), sidecar replacement, transition/reservation states
(the READY-held-release protocol spans reserve, hold-through-
replacement, and release), publication inspection (transaction-id and
value read-back after two foreign commits), commit resolution, and
snapshot opening - six parent tests per direction, all green with
IPRANGE_V4_MIXED_LIVE=1 in both directions on linux/amd64.

Parity gate overhauled to consume the full Rust export inventory
(2026-08-30): closing the sol P2 closure blockers ("the parity gate does
not prove Rust->Go parity - it inspects only Go functions/methods,
excludes types/constants, never consumes parity_rust_public.tsv, and
lets missing rows with an empty Go symbol pass"). The raw inventory
(v4/go/parity_rust_public.tsv) was regenerated from the frozen original
and deduplicated to 495 clean rows (0 brace fragments, 344 operations +
151 type rows, sorted); the new TestParityRustInventoryIsFullyRecorded
enforces three directions against the compiled surface: (A) every
inventory operation must be ledger-recorded - 344 ops all covered by
exact keys, "same-name Rust method" short keys with owner pinning, or
"<module.rs> <function>" keys; (B) every inventory type must exist as an
exported Go type or be listed in the expanded rustTypeDivergence set
(34 recorded Go/Rust type-shape differences, each naming its Go
adaptation); (C) every exported Go type/constant must be
ledger-recorded or inventory-matched. rootSymbols now collects exported
types and constants as well as functions, and follows exported type
aliases one level into internal packages (34 alias-visible methods such
as Cardinality129.Add from internal/format) so the gate sees the true
compiled surface - fixing the recorded false claim sol found in the old ledger
("lib-reexport Cardinality129 | missing | public typed cardinality
re-export; Go keeps Cardinality129 internal" while types.go:142
exports the alias). The ledger grew from
294 to 816 rows: 693 present (including the 408 go-surface
type/constant/method rows: 377 exported types/constants plus 31
alias-visible methods, grouped by semantic note - error-code
constants, validation-reason constants, adapted shapes), 4
required-missing, and 119 deliberate absences (82 Rust C ABI binding
absences class c-abi; c-abi-v4.md #[doc(hidden)] c_abi_support
exports with their Go owners recorded in the notes; 37
closed-by-decision Go-adaptation removals across the surface and
writer-offcontract classes). The breakdown sums: 693 + 4 + 119 = 816.
The old
TestParityLedgerMatchesTheGoSurface keeps the ledger-vs-Go closure
(now type-aware: present rows accept an exported type/constant).
Tripwire mutation-proven at this identity: deleting one go-surface type
row fails Direction C (unrecorded-type), deleting one divergence entry
fails Direction B (inventory-type-missing), deleting one upgraded
operation row fails Direction A (inventory-operation-unrecorded).
Validation: go test ./..., go test -tags v4work ./..., go vet ./...,
gofmt all green (linux/amd64). Files: v4/go/parity_gate_test.go,
v4/go/parity_manifest.tsv, v4/go/parity_rust_public.tsv.

Milestone-5 deliverable record (2026-08-30, current state):
SOW-0027 delivers the Go/Rust v4 SDK parity reconciliation - the
normative live-only writer surface (off-contract Writer removed;
advanced direct, membership, and structured transactions; feed
lifecycle; exact direct replacement; first/last-seen refresh; history
projection; membership import; cancellation), one-inode immutable feed
construction (CreateImmutableFeedV4/V6), exact commit-outcome
resolution, bounded oldest-reader-safe reclamation, clean-writer
read-your-writes metadata, metadata buffer APIs, reusable
zero-allocation streaming facades, the parity ledger covering the
complete Go function surface, worker containment that fails closed on
every platform without a worker build (routing_other.go), and the
shared symmetric conformance corpus (6 Rust + 8 Go fixtures, all
cross-opening in both implementations). Binding user decisions:
activation sequencing vs SOW-0017 (Open Decision 1, 2026-08-26),
milestone-4 extension B (universal-key elimination, 2026-08-28;
recorded verbatim under Historical milestone records), and the
performance acceptance (1A/2A, 2026-08-29): elapsed time <=1.3x Rust
for every substantial acceptance scenario, peak RSS <=1.3x, no unsafe
ever.
The 2-3.5x write envelope of the superseded -g acceptance is VOIDED
by that binding. Current committed evidence against the <=1.3x
binding is the final-identity matrix (rust-ratio-final-20260830.csv,
five alternating same-session release samples at HEAD 39df5b0b):
membership-import 1.529x, nested-overwrite 2.355x,
update-ipsets-workflow 1.925x, live/immutable direct lookups
1.525x/1.582x (v4) and 1.326x/1.357x (v6), live-validation 1.322x;
peak RSS fails two of eight (live v4 1.351x, live-validation 1.402x).
EVERY scenario FAILS the binding and is recorded as such; earlier
tables (reader-fixed-page 1.66/1.63x, writer-transport 2.31x,
20260828g) predate the final identity and are archival. Residual
performance ownership is the rewritten pending SOW-0030 (startable
only after the measured-performance decision; no SOW-0017 dependency); the
bounded safe-Go leads are measured-and-rejected unless retained with
evidence (recorded below); the measured-performance decision returns to the
user at this SOW's closure decision point. The milestone-5 package (slices A-F), its
five-reviewer close rounds, and the four-reviewer full-codebase
mmap/file-I/O gate are recorded below; all earlier milestone records
are superseded and kept verbatim under Historical milestone records.

Final-round record (5 level-1 reviewers of the lead's model, final-review
skill adversarial, at HEAD fcf6973e; reviewers kept open for the delta
re-review):
- Boole (Rust parity): FAIL - P1-1 the validation bitmap WordCache never
  pinned its cache key on an absent-leaf probe (word_cache.go), so a
  later probe of a previously loaded region reused the stale empty answer
  (false MembershipActiveFeedInvalid + full per-word descent divergence
  from Rust word_cache.rs); P2-1 insert.go shadowed SlottedReplace's real
  error behind the generic no-longer-fits class; P3-1 the gap probe
  decoded the probing leaf cell one extra time vs Rust LocalGap.
- Sagan (Go idioms): FAIL - P2 (one composite) plus P3s; all stated
  as small idiomatic deviations, grouped and fixed together with the
  Rust-parity findings.
- Hegel (performance): PASS for evidence/code consistency; P3s: the
  same gap-probe decode (fixed with P3-1) and a writer probe-isolation
  A/B record note (accepted; the A/B is not run because the bounded
  safe-Go leads are exhausted - recorded in the direction-item 4
  measured-and-rejected status above).
- Herschel (wire/integrity): FAIL - P2 the spec-mandated cross-language
  publication-reservation and live creating/ready transition phases
  (binary-format-v4.md section 21) were not covered by the mixed
  battery.
- Turing (APIs/records): FAIL - P2 stale close-out paragraph citing the
  VOIDED 2-3.5x envelope and old numbers; two tracked follow-ups (v6
  read bench, Go counters to full Rust parity) not represented in
  Followup; Followup's in-process stance contradicted the shipped
  fail-closed routing; SOW-0030 claimed this SOW completed; P3:
  ledger/counter/alias counts and the evidence README gap (6 newer CSVs
  undocumented, 20260828g envelope citation still live).
Fixes, all verified (2026-08-30):
- P1-1 fixed: the absent-child branch pins w.leafBase/leafPage and the
  cache key (v4/go/internal/validation/word_cache.go), plus regression
  test TestBitmapWordCacheAbsentRegionCacheKey.
- P2-1 fixed: applyEdit returns SlottedReplace's own error from the
  replace branch (v4/go/internal/tree/insert.go).
- P3-1 fixed: LocalGap[T any] carries the already-decoded record (Rust
  LocalPrevious::Reject/LocalNext::Reject); the generic selector and the
  genfamilies-emitted rangeProbe4/6 never re-decode the probing cell
  (v4/go/internal/tree/gap.go, range_family.go, genfamilies/main.go,
  regenerated range_gap_v4.go / range_gap_v6.go, gap_test.go).
- Herschel P2 fixed: the mixed battery now prepares each SHA-512-bound
  publication reservation and each live creating/ready intermediate in
  one language and inspects/resolves it in the other, in both
  directions (publication.after_reservation_directory_sync ->
  InspectPublicationResidue/ResolvePublication; create.after_sidecar_
  sync -> Rollback -> Removed; create.after_ready_sync -> Complete ->
  openable txn-1). Go-parent fabrication runs the internal/publication
  and internal/live v4work test binaries; Rust-parent fabrication runs
  the lib's own unit-test binary with the existing crash children;
  files v4/go/mixed_live_test.go, v4/rust/iprange-livedb/tests/
  mixed_live.rs, v4/conformance/README.md.
- Turing P2/P3 fixed: stale close-out paragraph rewritten to the
  <=1.3x binding with current numbers; Followup below; ledger 408
  go-surface rows (377 types + 31 methods), 36 counter rows (25 match /
  10 go-less / 1 go-more), 34 alias-visible methods; evidence README
  voids the -g envelope citation and documents the 6 newer CSVs;
  SOW-0030 status line corrected.
Validation at the fixed identity: go test ./... and go test -tags v4work
./... (32 GB address-space cap), go vet ./... both modes, gofmt,
genfamilies regeneration idempotent, rustfmt, and both directions of
the mixed battery (IPRANGE_V4_MIXED_LIVE=1) - 9 Go-parent modes and 9
Rust-parent tests, all green on linux/amd64.

### Milestone-5 (2026-08-28): closure-parity work package

External sol audit at HEAD 093ddb64 verified against the code and this
SOW's own closure contract (lines above): every reported P1/P2 item is
real and maps to a recorded closure requirement. SOW-0027 stays
in-progress; milestone-4 acceptance is deferred to the end of this
package. Verified item ledger (evidence -> closure contract -> fix):

1. Worker containment (P1, sol): routing_other.go routes validation/
   recovery in-process on every non-linux/amd64 platform; the closure
   contract requires a version-matched worker on every supported
   OS/architecture (routing_other.go:1, binary-format-v4.md:3155).
   FIXED (slice E): the worker-routed surface (routing_worker.go) now
   covers every platform the worker binary cross-builds on (linux,
   darwin, freebsd, windows on amd64 and arm64) with the same
   no-silent-fallback semantics; routing_other.go keeps only the
   platforms without a worker build. The routed facade tests and the
   worker harness follow the same matrix (worker_harness_build_test.go
   builds the real worker natively, .exe suffix on windows). Local
   linux/amd64 validation green and the native rounds on the
   authorized hosts completed: plakam4mini (darwin/arm64) plain +
   v4work green, freebsd (amd64) plain + v4work green (after clearing
   a root-owned stale Go module cache that had filled the disk), and
   costa-win11 (windows/amd64) plain + v4work green after two
   windows-only fixes: the routed recovery double needs the .exe
   suffix that go test -c appends, and cmd/iprange-v4-bench touched
   syscall.Stat_t directly (the physical block count now comes from
   build-tagged statBlocks helpers). The routed facade equivalence
   tests (validation, inspection, recovery, guard-pending cleanup)
   run through the real worker on every worker-supported platform.
2. Immutable-feed builder (P1, sol): Go had no one-inode immutable
   feed construction (parity_manifest.tsv immutable_feed row missing;
   scenario_sdk.go substituted live+snapshot; Rust lib.rs exports
   create_immutable_feed_v4/v6), so the update-ipsets-workflow
   benchmark was not apples-to-apples. DONE (slice F): public
   CreateImmutableFeedV4/V6 (immutable_feed_public.go) mirror Rust
   immutable_feed.rs over the writer machine
   (internal/writer/immutable_feed.go + immutable_workspace.go): the
   unordered source drains into a mapped workspace at high page
   offsets of the private attempt inode, the ordered cursor streams
   the normalized coverage tree into the append-only output pages,
   and the reservation machine publishes. The benchmark sources now
   feed the real builder through the RangeSource4 seam; the parity
   rows are present; a Go-produced conformance fixture
   (go/immutable-feed-ipv4.iprdb) cross-opens in the Rust corpus
   verify. Re-measured at the g identity (apples-to-apples workflow
   ratio 2.189, see item 9).
3. Commit outcome resolution (P1, sol): CommitResult (writer_public.go:
   61) lacks DirectoryIdentity/MainIdentity that Rust CommitResult
   carries (live_writer/result.rs:129) and ResolveCommit accepts only
   LiveCommitResult, so advanced workflows cannot resolve an unknown
   outcome. Fix (slice B): add the two identities to CommitResult,
   populate them at the commit terminal, and let ResolveCommit accept
   the unified attempt shape.
4. Invalid inputs (P2, sol): nil MembershipScope entries and nil
   DirectJoinSource dereference without a typed error
   (membership_algebra_public.go:79, join_public.go:116) and undefined
   OfflineQuiescenceCertification values are accepted (recovery_public
   .go:201) although the closure contract requires typed errors and
   certification rejection. Fix (slice A): guards at the public
   boundaries with tests.
5. Metadata buffer APIs (P2, sol): ImmutableReader exposes only the
   allocating MetadataJSON; Rust also provides metadata_json_len and
   read_metadata_json (reader_public.go:527, database.rs:165). The
   live surfaces already have the pair. Fix (slice C): mirror the two
   methods on ImmutableReader.
6. Streaming facade allocations (P2, sol): aggregation/join docs
   promise reusable zero-allocation buffers but the facades allocate
   a fresh output slice per callback batch and the zeroalloc tests
   permit it (aggregation_public.go:95, join_public.go:135,
   streaming_facades_zeroalloc_test.go:85). DONE (slice C): all five
   facade yields (aggregate cardinality/overlap, direct join, cross and
   uncovered membership join) reuse one growable output slice per
   yield, the uncovered-feed names join the per-distinct-name cache
   (removing the former per-record string allocation), the docs state
   the reuse contract, and the allocation-pin comment now matches the
   measured behavior.
7. Parity gate completeness (P2, sol): the manifest holds 86 curated
   rows (61 present, 25 informational missing) against 507 Rust
   exports; missing rows with an empty Go symbol never fail
   (parity_gate_test.go:147). Fix (slice D): required-missing rows
   fail unless recorded as planned with evidence, and a surface
   coverage tripwire closes the Rust export list. DONE (slice D):
   the manifest now records the complete Go function surface (266
   present rows: 65 curated operations plus 201 surface rows
   generated from the compiled root package), the 17 slice-2c
   deliberate absences carry the explicit `removed` status with
   their evidence notes, and the gate enforces (a) every exported
   Go function and method is recorded, (b) every `missing` row sits
   on the embedded closure-required list with a note and flips to
   present only in the implementing commit, (c) `removed` rows carry
   evidence and stay absent. The 7 required-missing rows are the
   tracked work: reclaim, immutable feed, from_poll, scratch-removal
   export, worker containment, apple filesec re-export, Cardinality129
   re-export. (At close: 5 remain - immutable feed flipped to present
   in slice F and reclaim to removed with its LiveWriter owner in the
   close commit.)
8. Closure records (P2, sol): validation, real-use evidence, final
   documentation, the mandatory full-codebase mmap/page-copy/file-I/O
   adversarial reviews, outcome, and lessons are pending; superseded
   milestone sub-states still read as active at the top of Status.
   Fix (slice G + close): status rewrite, mandatory four-reviewer
   full-codebase gates, final 5-reviewer round, and the close-out
   sections.
9. Performance mandate (sol): committed ratios show nested-overwrite
   4.044x (target 2-3.5x), lookups 1.835/1.830x (target 1.2-1.6x),
   validation 2.235x (target 1.5-2x), and the workflow comparison was
   not comparable until slice F landed. MEASURED (slice F, g identity,
   rust-ratio-acceptance-20260828g.csv, interleaved matched 5-sample
   runs): membership-import 1.682, nested-overwrite 4.200,
   update-ipsets-workflow 2.189 (first apples-to-apples one-inode
   measurement; Go elapsed 2,450 ms, inside the 2-3.5x write
   envelope), live-direct-random-lookup 1.784,
   immutable-direct-random-lookup 1.792, live-validation 2.299. The
   four unchanged scenarios moved within the rounds' run-to-run noise
   (f to g deltas -2.8% to +3.9%). The CI gate (i log) is 18/18
   within-limit, ratios 0.396-1.025, workflow 0.488 (the workflow
   baseline re-bases at the g identity: the scenario code path
   changed). Remaining cost-effective residuals (slice G): the
   nested-overwrite/validation targets stay open; the duplication/
   codegen option re-opens only if measurements show it is the
   cheapest remaining win.


### Slice F implementation decisions (2026-08-28, recorded before code)

Resolved design decisions for the one-inode immutable feed builder
(Rust immutable_feed.rs parity), all bounded by the review-gate rule
(Rust is the authority; Go idioms and absolute performance apply):

1. Public API shape: CreateImmutableFeedV4/V6 with the Rust argument
   order and terminals. The Go source seam mirrors Rust
   source::RangeSource::next_batch as two interfaces,
   RangeSource4{NextBatch() ([]AddressRange4, error)} and
   RangeSource6{NextBatch() ([]AddressRange6, error)}: nil,nil ends,
   an empty batch is refused with ErrorInvalidArgument, and batches are
   caller-owned (zero copy). The failing terminal mirrors
   SnapshotPreparationFailure: *ImmutableFeedPreparationFailure{Cause,
   Cleanup}. The successful terminal is ImmutableFeedResult{Report,
   Publication} with CleanupState(), and the report carries
   InputRecordCount, NormalizedIntervalCount, and the exact
   Cardinality129 address count (Rust ImmutableFeedReport).
2. Machine ownership: the build machine lives in internal/writer
   (immutable_feed.go + immutable_workspace.go) and imports
   internal/publication for the attempt lifecycle. Rationale: normalize
   reuses the writer-owned coverage-union machinery
   (pushPrivateUntracked/finishInputUntracked/UnionInput/range bulk
   builder) and the tree Store surfaces directly; no writer internal is
   exported, and the writer -> publication import is acyclic. The
   workspace is a fresh Go tree.Store/RetiringStore/RangeStore/
   ForwardStore over mapping pages [max_output_pages, total_pages) of
   the attempt inode with the Rust FREE_MAGIC/FREE_NEXT/FREE_TXN free
   list (immutable_output/unordered/workspace.rs parity), so the page
   allocation, inspect, update, copy, and discard paths run through the
   single generic tree core exactly like DraftStore.
3. OutputBuilder extent: a writer-internal constructor extends the
   empty attempt file to (output + workspace) pages and maps the full
   extent read-write through a duplicated descriptor (Rust
   new_owned_with_extent + Mapping::read_write over clone_file); the
   workspace mapping is a second mapping of the same inode and is
   unmapped before Finish shrinks and seals (drop(workspace) parity).
   The append-only reserve_page authority and the BudgetExceeded bound
   on max_output_pages stay in OutputBuilder unchanged.
4. Budget: ImmutableFeedBudget{MaxHeapBytes, MaxOutputPages,
   MaxWorkspacePages, MaxOpenFiles}; max_output_pages < 2 or
   max_open_files < 3 is InsufficientResourceBudget, output +
   workspace over MaxPageCount (or arithmetic overflow) is
   PageSpaceExhausted, each Rust-verbatim at prepare_budget position
   (before any destination artifact). The reference batch charges the
   operation heap exactly like the snapshot (membership batch only:
   the feed output has ValueKindMembership).
5. Bench: scenario_sdk.go sdkCreateImmutableFeedV4 and the workflow
   sources call the new public API directly; the bench source types
   implement the new interfaces; the live+snapshot substitute and the
   sdkImmutableFeedReport/Budget adapter types go away.
6. Conformance: one new Go-produced fixture
   (conformance/go/immutable-feed-ipv4.iprdb, produced through
   CreateImmutableFeedV4) cross-opened by the Rust corpus verify; the
   producer split (6 rust / n go) stays explicit in cases.json.
7. Parity ledger: the single missing row splits into two present rows
   (create_immutable_feed_v4 -> CreateImmutableFeedV4,
   create_immutable_feed_v6 -> CreateImmutableFeedV6) and the
   required-missing list entry is removed in the same commit; surface
   rows cover the new exported methods.

Slice plan and validation: A input guards (tests), B commit
identities (gate + cross-open), C metadata + streaming (allocation
pins), D parity gate (gate test), E worker matrix (native hosts:
costa-win11, plakam4mini, freebsd), F immutable feed builder + bench
swap (conformance fixture + apples-to-apples ratios), G mmap/file-I/O
adversarial reviews + final 5-reviewer round + closure records.
Every slice: gofmt/vet, plain + v4work tests under nice, race/checkptr
on the touched packages, gate/ratio evidence refresh when behavior or
hot paths change.

Slice F delivery (2026-08-28, commit f4e43df9): the one-inode
immutable feed builder, its public CreateImmutableFeedV4/V6 surface,
the bench swap, the parity flip, and the conformance fixture shipped
in one commit. Validation: gofmt/vet clean, plain + v4work suites
green, full race/checkptr green, the Go conformance suite and the Rust
corpus verify cross-open the new Go-produced fixture, and the six
headline ratio rows re-measured at the g identity (item 9). The
immutable-feed-random bench row drops the live+snapshot substitute:
100k ranges build in ~95 ms with 1,179 alloc calls and a 355-page
output (live+snapshot previously produced ~1.45 MB output and the
workflow ran ~1 s slower). Slice G (mmap/file-I/O adversarial
reviews, final 5-reviewer round, closure records) remains.


Slice G delivery (2026-08-28): mandatory full-codebase gate
completed. Four adversarial reviewers of the lead's model, fresh
context, read the ENTIRE production Go tree file by file (457-476
files, ~103k lines; test files and the bench/worker command tools
reviewed for the same hazards but excluded from the verdict). Two
hunters for complete-page copies into owned memory (Helmholtz,
Schrodinger): both PASS, no P1/P2 - the only 4096-byte owned buffers
in the tree are test fixtures and the recovery external-sort logical
record staging blocks (12/36-byte values, never a database page; the
Rust authority does the byte-identical stream copy).
Two hunters for file I/O on persistent content outside the mmap
(Parfit, Lovelace): both PASS, no P1/P2 - production uses os.File
only for Close/Fd/Stat/Truncate/Sync/Chmod/Name plus the mapping,
namespace, identity-probe, and coordination-machine exceptions;
zero Read/Write/Seek/ReadAt/WriteAt/ReadFile/WriteFile/io.Copy on
persistent content; every persistent byte flows through
mapping.Mapping views. P3 notes accepted as non-blocking: digest and
membership word chunk copies are sub-page (512 B/1024 B) mapped
chunks mirroring Rust; metadata zlib decodes straight from mapped
chunk views; the 4062-byte membership record scratch is bounded
workspace, not a page; bench/worker pprof and .ack harness files are
test tooling, not database content. The four verdicts are recorded
in Validation. Remaining for close: the final five-reviewer round on
the milestone-5 delta (093ddb64..607d2086), the status compaction
(the superseded milestone sub-states below are historical and will
be moved under an explicit history heading), and the close-out
records.




### Historical milestone records (milestones 1-4 and extension B; superseded by the milestone-5 closure, preserved as the audit trail)

The records below are historical: they narrate milestones 1-4
(parity ledger freeze, live-writer consolidation, symmetric
conformance, performance slices 4c/4d) and milestone-4 extension B
(universal-key elimination, the binding user decision of
2026-08-28: option B, recorded in the section below). They are
preserved for audit, with only the pre-close status marker line
superseded by the Status summary above; the extension-B baseline
statement stays valid: the accepted milestone-4 evidence
(rust-ratio-acceptance-20260828.csv) remains the pre-optimization
baseline for the extension. The present state of the SOW is the
milestone-5 package and the close-out records above and in
Validation/Outcome.

#### Milestone-4 extension B (user decision 2026-08-28): universal-key elimination

User decision: option B from the 2026-08-28 discussion (the single sol
review's P1 "universal keys" finding) - make the Go engine structurally
optimal like Rust: eliminate the universal 88-byte `tree.Key` from the hot
paths so decoded IPv4 range records shrink from 184 bytes to 12 bytes and the
per-record interface dispatch disappears. Wire format, public APIs, and
cross-open compatibility must NOT change. Long-term-best, minimal-complete,
absolute-performance, clean code, one authoritative implementation.

Requirement model (evidence): the Go range writer carries every decoded leaf
as `rangeRecord` with two 88-byte `tree.Key` values (184-byte records) and
dispatches per record through `rangeFamily` / `tree.Codec` /
`RangeStore` / `tree.Store`. Rust carries `Record<K>` with `K = Ipv4Key`
(12-byte IPv4 records, 36-byte IPv6) and monomorphized static code
(`v4/rust/iprange-livedb/src/range_tree.rs` Record/Codec,
`range_mutation.rs` PrivateGap). Result: clamping per-record value copies
from 3 cache lines to 1, eliminating the per-record key construction
conversions, and removing the 88-byte values from the mutation machinery.
The measured deltas this targets (1M medians, rust-ratio-acceptance-
20260828.csv): nested-overwrite 6.7x, update-ipsets-workflow 4.4x,
membership-import 3.5x, lookups 1.9-2.1x.

Design (decided, implementation authority):

1. `tree.Key` is redefined as the canonical-compare-bytes key: fixed
   numeric keys hold their compare bytes big-endian in a `[40]byte` value
   with a size tag (4/8/12/16), hash keys hold digest bytes plus each
   suffix word big-endian (same order as Rust `HashKey` derived Ord), and
   variable keys (catalog names) keep a byte slice. All key compares in
   the tree core become static bytewise compares (no fields to
   materialize, no per-probe conversion; the width-specialized
   `fixedLowerBound` probe keeps its existing static form, reading the
   canonical bytes instead of `Key.Hi`/`Key.Lo`/`Key.Raw`).
2. The writer's range family becomes generic over the key type exactly
   like Rust: `rangeRecord[K]`, `rangeFamily[K]`, `rangeCtx[K]`, and the
   mutation machinery (`range_edit`, `range_merge`, `range_locator`,
   `range_coverage`, `range_draft`, `range_bulk`, comparison, feed merge,
   import paths) parameterized by K with `K = uint32` for IPv4
   (12-byte records) and the IPv6 pair for IPv6 (36-byte records).
3. One authoritative tree core stays; no per-family core copies. The
   core's public shape (`Codec[T]`, gap/insert/delete/read/cursor/walk)
   keeps its function signatures; only the value representation of `Key`
   changes inside.
4. The reader (0 `tree.Key` uses), the on-disk layout, the codec cell
   encodings, and the public surfaces are unchanged; validation, retire,
   and the public entry points mechanically adapt to the compact key.

Validation plan (recorded before implementation):

- Every slice commit: `nice go -C v4/go test ./...`, `go vet`,
  `gofmt -l`, the v4work suite, race/checkptr on the final slice, and the
  cross-open conformance battery.
- Re-measure the matched Rust-ratio evidence with the final release
  binaries (same host, both sides): rerun the 18-case CI gate and refresh
  the committed evidence CSVs. Targets: read paths ~1.2-1.6x Rust,
  write paths ~2-3.5x, validation ~1.5-2x (the accepted milestone-4
  mandate).
- Five-reviewer level-1 round on the delta (scopes: Rust parity, Go
  idioms, performance, wire/integrity, APIs/records), then SOW-0027
  milestone-4 acceptance and SOW close.

Sub-state (2026-08-28): extension-B slices B1 and B2 delivered.
B1 (7f34d161) compacted tree.Key to 48-byte canonical compare bytes; B2
(affacd57 + 17914dbc) made the range writer family-typed (12/36-byte
rangeRecord[K], no universal key and no per-record interface dispatch on
the hot paths), cached the fixed-key numeric limbs (17914dbc), and
restored the draft-private single-descent assign gap path so the private
assign arms match Rust assign_private. Validation: gofmt/vet clean,
plain + v4work + race/checkptr suites green, 18-case CI gate 18/18
within-limit (ci-go-v4-local-20260828d.log, ratios 0.395-1.071;
workflow 3,465 ms vs accepted 5,058 ms = 0.685). Fresh matched 5-sample
Go/Rust ratios (rust-ratio-acceptance-20260828b.csv): membership-import
1.648 (was 3.506), nested-overwrite 4.152 (was 6.655),
update-ipsets-workflow 3.141 (was 4.434), live-direct-random-lookup
1.872, immutable-direct-random-lookup 1.850, live-validation 2.560.
B3 (uncommitted in the working tree at this point; committed with the
next evidence refresh) applied the sol-review verified fixes: the tree
fixed search validates the page shape once and probes through the
Rust-style index-bounded cell view (no per-probe index re-checks), tree
Key ordering compares numeric keys through their cached limbs, and the
range validation leaf ordering works in the family key space (no
per-record general-key materialization). Measured effects (interleaved
matched samples): validation ratio 2.56 -> 2.18, membership-import
1.65 -> 1.62, reads 1.85-1.87 -> 1.96-2.00 (run noise band), write
scenarios unchanged within noise. Sol review verdict: NEEDS CHANGES;
verified P1/P2 findings remaining: (1) the writer still crosses the
tree-core boundary through per-op tree.Key conversions and the gap
machinery dispatches through the frozen interface-shaped signatures
(nested-overwrite 4.15-4.41x; fixing requires a design change to the
frozen core), (2) the reader search still dispatches through closures
per probe and re-checks slot geometry (lookups 1.85-2.0x; reader scope,
outside extension B), (3) the update-ipsets workflow still allocates
10.7M objects (reader algebra: selectedRanges.next 5.5M, sort.Swapper
1.2M, snapshot.membershipWords 1.0M; outside extension B).

User decision (2026-08-28, "1B"): continue optimization to maximum
performance within the current milestone. Two reviewed slices: C1 =
reader + workflow (closure-free typed read probes with once-validated
page views; eliminate the top workflow allocation sources
selectedRanges.next / sort.Slice reflection swapper /
snapshot.membershipWords), C2 = tree-core typed fixed-key entry points
(a deliberate refinement of the frozen core design: the core keeps one
authoritative implementation and its Codec/T/cycle semantics, but its
entry points accept the family-typed fixed key instead of constructing
tree.Key per operation, and the gap/replace machinery dispatches
without the per-record interface hops). C2 explicitly supersedes the
2026-08-28 design item 3 "the core's public shape keeps its function
signatures" for fixed-key codecs, to the extent needed to remove the
per-op key construction and interface dispatch; wire format, public
APIs, work counters, and corruption semantics stay identical. After C1
and C2: 5-reviewer level-1 round, milestone-4 acceptance, SOW close.
C1 delivered (pushed as part of this status write): the reader range
searches are closure-free family-typed probes over the shared
once-validated format.FixedSearch (lookups 1.96-2.00 -> 1.82-1.86),
the 4c selected-runs scan aliasing defect was found by a new coverage
regression test and fixed with value semantics (5.5M -> 0 per-run heap
objects), the algebra boundary position no longer escapes per event
(1.8M objects), the output sort uses slices.Sort instead of the
reflection swapper (1.2M objects), and the snapshot copy streams
membership words through the writer word-source seam without per-record
materialization (1.9M objects). The update-ipsets workflow allocation
count dropped from 10,727,434 to 27,414 objects (369MB to 9.8MB) per
the ci-f gate row (27,409 / 9.8MB at ci-g), with wall-time ratios
unchanged within noise (3.21x) because the tiny-object allocations were
not the CPU bottleneck; the write-path ratio remains nested-overwrite
~4.45x pending the C2 typed tree core.
C2 delivered (pushed with this status): the tree replacement validation
runs through a numeric limb seam (NumericKeyCodec.ReadKeyLimbs on the
range codecs; tree.RequireReplacement no longer materializes a general
tree key per cell). Interleaved head-to-head on the same window:
nested-overwrite 1334ms -> 1247ms (-6.5%), final matched ratio 4.184
(was 4.451). The remaining write-path gap is the generic gap/replace
machinery dispatching through interfaces per probe (codec.ReadLeaf +
LocalGap + rangeFamily calls); Go generics compile to dictionary-based
dispatch and cannot monomorphize these calls statically, so closing the
last ~20% requires either duplicating the tree gap machinery per family
(a trade-off against the one-authoritative-core principle) or code
generation. Recorded as an open decision for the milestone close.

Sub-state (2026-08-28): five-reviewer level-1 round on the B/C delta
(7f34d161..ff0c4fe5) complete. Verdicts: Rust parity PASS,
Go idioms PASS, performance PASS, wire/integrity PASS,
APIs/records FAIL (two P2 record defects, both verified against the
committed files and fixed in this round). Verified fixes applied:

- P2-1 (APIs/records): the evidence README quoted wrong ratio ranges
  for the ci-e/f/g gate logs (0.682-1.072, 0.691-1.050, 0.407-1.050)
  while the committed logs measure 0.417-1.072, 0.525-1.210,
  0.467-1.012; the README entries now quote the measured ranges.
- P2-2 (APIs/records): the workflow allocation claim "27,406 objects
  (369MB to 138MB)" matched no committed artifact; the ci-f/ci-g rows
  show 27,414/27,409 objects and 9.8MB (not 138MB). The SOW and README
  now quote the committed gate rows.
- P3 (performance, verified): the tree fixed probe called the
  non-inlinable fixedCellAt wrapper per probe; fixedLowerBound now
  calls format.FixedSearch.Cell directly (one call layer removed,
  identical behavior and counters).
- P3 (verified): dead havePrevious flag removed from
  requireReplacementLimbs (the index==0 branch already guards the first
  cell; the 2-3 cell length check makes the flag always true).
- P3 (verified): tree.Key.U32 restored its documented leading-32-bits
  semantics for wider keys (uint32(k.hi >> 32), matching the pre-B2
  beU32(data[:4]) contract; no current caller uses wider keys, the doc
  is now accurate).
- P3 (verified): snapshot copyStructuredV4/V6 no longer shadow the
  immutable-reader source parameter with the membership word source.
- P3 (verified): collectHistoryWindows and assembleOutputBuilder now
  allocate only the address-family-selected runs slice and range bulk
  builder instead of both siblings.
- P3-1 (APIs/records, false positive): the "was" pre-B ratios
  (3.506/6.655/4.434) do match the committed
  rust-ratio-acceptance-20260828.csv table (the 4c/4d state); the
  README now names that table as their source.

Re-validation after the fixes: gofmt/vet clean, plain + v4work suites
green, full race/checkptr suite green, conformance cross-open 13/13
plus invalid-mutation cases pass. Gate ci-go-v4-local-20260828h.log:
18/18 within-limit, ratios 0.416-0.993, workflow 3,446 ms (ratio
0.681), 27,406 objects / 9.8MB (the six P3 fixes removed 8 objects
against the ci-f row). Fresh interleaved matched ratios
(rust-ratio-acceptance-20260828f.csv):
membership-import 1.636 (was 1.666), nested-overwrite 4.044 (was
4.184), update-ipsets-workflow 3.209 (was 3.231),
live-direct-random-lookup 1.835 (was 1.863),
immutable-direct-random-lookup 1.830 (was 1.863), live-validation
2.235 (was 2.301). No case regressed; nested-overwrite and validation
improved in the same interleaved window.

Recorded opportunities (not implemented in this round; all P3,
non-blocking, verified): (1) range_bulk finishLevel returns an untyped
any with a caller type-switch; a typed (node, isRoot, err) return would
remove the switch (Go-idiom polish, moderate churn in the bulk
builder); (2) the locator/bulk/history concrete-type family switches
could invert into a Family()/DecodeKey interface on rangeFamily
(Go-idiom polish across four files); (3) ValidateProbeCell still copies
the 32-byte hash digest that the probe discards and the ProbeValidator
method value is an indirect call per probe on membership/structure
trees (micro-optimization on the hash-probe path only, deferred to
avoid touching parity-verified validation code at the gate);
(4) tree.Key grew 16 bytes with the limb cache; all hot-path copies
stay on the stack, no heap escape, net win from B1 already measured.

Open decision for the milestone close (user decision requested): the
nested-overwrite residual is 4.044x Rust after all slices and the
review fixes. Closing the last ~20% requires per-family duplication of
the tree gap/replace machinery (against the one-authoritative-core
principle) or code generation; both contradict the standing working
principles, so the recommendation is to accept the current ratios and
record the trade-off as a follow-up. The user is deciding between
accept (recommended), per-family duplication now, or codegen.


Sub-state (2026-08-26): activated by user decision (Open Decision 1, option A);
SOW-0017 is paused as blocked on this SOW's prerequisite. The static gap
analysis at repository revision `07c0147bf18b4d1e976f460fc2624e90e91be8cc` was
independently re-verified by the lead against the code and the normative specs
(2026-08-26); every material claim was confirmed, including the off-contract
public writer, the direct-only live writer facade, the missing operations
(membership import, one-inode immutable feed, commit resolution, bounded
reclaim, clean-writer metadata read-your-writes), the omission of cancellation
and live join sources, the error/nil/enum contract defects, the in-process
non-Linux worker routing, the private Windows housekeeping, the asymmetric
fixture producers, the same-language immutable-only subprocess tests, the
missing benchmark matrix, and the incorrect SOW-0025 acceptance record.
Implementation starts with the parity ledger freeze (plan step 1).

Sub-state (2026-08-26): milestone 1 (parity ledger freeze) delivered at
`bff1f550`. Milestone 2 slice 2a (live-writer consolidation) is delivered at
`11ff5848` and its five-reviewer gate closed at `6853e4fd` (all five
reviewers PASS on round 3; rounds 1-2 findings all fixed and recorded below): all advanced transactions and
high-level workflows bind to the sidecar-bound `LiveWriter` through the
shared `mutationHost` (core owner, open/healthy probe, abort, discard, and
commit terminals); the live public surface is `LiveWriter.BeginDirect`,
`BeginMembershipTransaction`, `BeginStructuredTransaction`,
`BeginCreateFeed`, `BeginReplaceFeed`, `BeginDirectReplacement`,
`BeginFirstSeenRefresh`, `BeginLastSeenRefresh`, `ProjectHistory`,
`RenameFeed`, `DeleteFeed`, `Abort`, and `Close` with the captured
cancellation carried through every begin, mutation, metadata stage, commit,
and cleanup checkpoint; the `FinishedHistoryProjection` commit path is
unified through the host terminal (reader-table gate on the live path); the
end-to-end live test chain (CreateLive -> OpenLiveWriter -> BeginCreateFeed
-> FinishInput -> Commit -> SnapshotTo -> OpenImmutable) plus live
lifecycle, direct workflow, structured transaction, and live-source history
projection tests pass. The parity ledger flipped the implemented rows from
missing to present (68 present/remove-planned, 18 missing) and the gate test
now closes both the `Writer` and `LiveWriter` method surfaces. The full
battery (plain, v4work, vet, gofmt, race+checkptr) passes at the slice
commits.

Review round 1 (five reviewers: Rust parity, Go idioms, performance,
wire/integrity, APIs/records) found and the lead fixed:

- P1 (Rust parity, F1): `LiveDirectTransaction.Commit` passed a nil
  checkpoint; the captured cancellation token now checkpoints the commit
  attempt, the prepare-and-lock sequence, and the publication loop (Rust
  DirectTransaction::commit -> commit_operation parity).
- P2 (Rust parity, F2): the live direct range ops lacked the Rust
  run_transaction post-mutation checkpoint and the metadata stages lacked
  both checkpoints; pre+post checkpoints are now applied to
  AssignV4/AssignV6/ClearV4/ClearV6/SetMetadataJSON/ClearMetadataJSON.
- P2 (APIs/records): the parity ledger omitted the
  `LiveWriter.BeginDirectReplacement` row and had no tripwire for
  unrecorded `LiveWriter` methods; the row is added and the gate now closes
  both method surfaces (it also caught and recorded `LiveWriter.Abort`).
- P2 (APIs/records): the status record still claimed slice 2a was "under
  way at working tree `bff1f550+`" while the slice was delivered; rewritten
  to present state here.
- P3 (APIs/records): the structured begin probed healthy before the Rust
  structure-kind gate and its doc claimed cancellation-first; the shared
  helper now runs closed-probe, structure-kind, cancellation, healthy,
  value-kind (Rust begin_structured_transaction order) and the doc matches
  both hosts.
- P3 (wire/integrity): a stale Rust test-file citation in
  `live_workflow_public_test.go` was replaced with the real Rust suite name.
- P3 (Go idioms): dangling doc fragment, double coreOf probe, and
  plan-narrative sentences fixed in the slice commits.

Four outcomes remain recorded as accepted Go-shape divergences
(documented in code, not yet resolved SOW decisions): workflow
`CommitResult`/`AbortResult` terminals carry fewer fields than Rust
(workflow commits omit directory/main identity; workflow abort returns
`error`); mixed-language live fixtures stay a recorded slice-2c gap; and
the Windows cleanup-result classification (recorded 2026-08-27 during
the slice 2d native round): Go `EarlyDiscard.Clean` and
`ScratchCleanup.Clean` (`internal/worker/wire_cleanup.go`) accept
`HousekeepingCrashReappearancePossible` as clean when every artifact is
proved absent, where the Rust client predicates
(`worker/client/recovery.rs` discard_clean / scratch_clean) require
`Housekeeping::None` strictly. The divergence is Windows-only (the
POSIX discard and scratch arms emit None on both hosts) and exists
because the Rust Windows machine is self-inconsistent: Rust
`resolver::finish_housekeeping` itself emits the crash-possible class
on a fully absent removal, which the strict Rust client predicates
would then reject, turning every clean Windows discard into a failure;
Rust Windows has never been natively validated (the Rust client suite
is cfg(all(test, unix))). Go implements the proved-absence terminal so
the Windows recovery and cleanup machinery is usable; whether the Rust
client contract should be relaxed on Windows is a cross-SDK decision
for the user, not silently adopted here; and the Windows
worker-control unlink/reopen shape (recorded 2026-08-27 in the slice 2d
gate round): Go unlinks the worker control on every
platform (Rust `remove_path` is `#[cfg(unix)]` at both the call site,
`worker/client.rs`, and the definition, `worker/control.rs`, so Rust
Windows leaves the control file behind), and Go therefore reopens the
control with FILE_SHARE_DELETE in `openControlFile`
(`worker/control_open_windows.go`), where Rust `open_worker`
(`worker/control.rs`) uses stdlib `OpenOptions`, whose read+write-only
share mode would make the Go unlink fail with a sharing violation. Go
unlinks so Windows temp-dir controls do not accumulate per spawn;
whether Rust should adopt the same removal semantics on Windows is a
cross-SDK decision for the user, not silently adopted here.
And the public identity-kind encoding (recorded 2026-08-27 in the
milestone-3 gate round): the Go public `FileIdentity` is the portable
kind+bytes shape matching Rust `LocalFileIdentity`, but the Go live
surface hard-codes kind 1 where Rust emits the platform
`IDENTITY_KIND` (2 on Windows); Go is self-consistent (decode ignores
the kind) and the cross-language battery is linux/amd64-only, so no
host failure exists; a Windows cross-SDK fact exchange would see kind 1
vs Rust's 2 - recorded as an observation in the same family as the
commit-result-terminal divergence, remedy deferred to the user.

Sub-state (2026-08-26): slice 2b part 1 delivered at `532fa582`
(clean-writer metadata read-your-writes and bounded reader-safe
reclamation; gate pending at `7f627c6f`). Slice 2b part 2 (commit
resolution) delivered (working tree `7f627c6f+`, gate pending):
`ResolveCommit` with the `CommitResolutionMode` / `LocalFileRelation` /
`CommitResolution` / `CommitResolutionResult` public types (Rust
commit_resolution.rs parity) proves one exact attempted transaction
and nonce against both meta pages without validating either page
graph, in Live mode (shared main lifetime lock, ready reader-table
gate of the attempted database, writer-lease claim, slot scan,
classify-sync-classify stability proof, tail trim) and Immutable mode
(sidecar-absent, any-link identity rule); the bootstrap authority
gained `ResolveCommitAttempt` (exact two-meta transaction/nonce
classification) and the mapping owner gained the file-level
`ShrinkFileOrRetain` tail primitive; the Rust commit_lifecycle tests
are ported (live resolution facts, WriterBusy while the lease is
held, same/different local-file relation, superseded-unknown old
attempts, tail-only removal, invalid-attempt refusal). The parity
ledger flipped the commit-resolution row to present (73
present/remove-planned, 13 missing) and the gate test passes. Full
battery green (plain, v4work, vet, gofmt, race+checkptr) at the slice
commit. Slice 2b part 3 (live join sources) delivered (working tree
`9f0a9c93+`, gate pending): `DirectJoinSource` with the
`DirectJoinSourceImmutable` / `DirectJoinSourceLive` constructors
(Rust DirectJoinSource parity) lets `MembershipScope.JoinDirect` merge
a membership scope with the pinned generation of a live direct
provider; the variant resolution proves the live reader's open state
before any page is touched (Rust Source::new over LiveReader::core),
the zero source is refused as InvalidArgument, and the live-provider
test pins the exact report facts, result cells, closed-reader
WrongState, wrong-kind, and empty-source arms. The parity ledger
flipped the join row to present (74 present/remove-planned, 12
missing) and the gate test passes. Full battery green at the slice
commit. Slice 2b part 4 (membership import) delivered (working tree
`9f0a9c93+`, gate pending): `LiveWriter.BeginMembershipImport` with the
`MembershipImportSource` / `MembershipImportSourceImmutable` /
`MembershipImportSourceLive` constructors and the `MembershipImport`
handle (Rust live_writer/membership_import.rs parity) imports a
complete name-based membership database from an explicitly pinned live
or immutable reader into the preserved destination: the source
resolution and compatibility proof (membership kind, same family and
value tag, a different local file via the live writer's main identity),
the catalog sweep re-proving every source feed by name, the v4/v6
range sweeps with the last-translation fast path, the import cache
(the draft-private fixed tree with page types 240/241 and the aux
discriminator, Rust draft_store/import_cache.rs), the ordered
preserve-without-input union through `begin_import_merge` carrying the
already-translated membership pair in the merge record exactly like
Rust `Incoming<TranslatedMembership>` (draft_store/import_merge.rs
parity: the merge never re-resolves a translation - the ordered merge
input became generic over the policy's value type like the Rust
`OrderedMerge<K, V, P>`, with history/timestamp inputs staying u32),
the exact six-way classification
report, the empty-feed catalog change, and the full IPv6 space with
the exact 2^128 cardinality. All abort arms are atomic and leave the
writer reusable: cancellation, same-file InvalidArgument, incompatible
family/tag/kind, zero-budget TransactionAborted wrapping
InsufficientResourceBudget, and corrupted-source TransactionAborted
wrapping FormatInvalid (the reader cursor now maps every header
problem to the Corrupt class exactly like Rust slotted_page
parse_tree). The parity ledger flipped both membership-import rows to
present (76 present/remove-planned, 10 missing) and the gate test
passes; the full battery (plain, v4work, vet, gofmt, race,
checkptr=2, mmap trace, Rust membership_import authority) is green at
this state. With this commit slice 2b (live workflows) is complete;
slice 2c (remove/privatize the off-contract `Create`/`OpenWriter`/
`Writer` surface and regenerate fixtures through the live path)
follows.

Sub-state (2026-08-26): slice 2c (remove the off-contract writer surface)
delivered (working tree `5e458c37+`, gate pending): the public `Create`,
`OpenWriter`, and `Writer` types and every `Writer.*` method (direct, feed,
membership, and structured transactions; direct-replacement and first/last-
seen workflows; create/replace/rename/delete feed; project history; abort;
close; info) are removed; every operation's sole owner is the sidecar-bound
`LiveWriter` or `LiveDirectTransaction`. All warm-path, workflow, zero-alloc,
lifecycle, and subprocess/cross-open suites were migrated to
`CreateLive` + `OpenLiveWriter` (+ `OpenLiveReader` for read-backs and
`SnapshotTo` with `SnapshotSourceLive`, `PolicyFailIfExists`, and the proven
budget for publish), and the parity ledger flipped all 17 `remove-planned`
off-contract rows to `missing` (59 present, 27 missing); the gate now also
enforces that no public `Writer` symbol exists. The five Go conformance
fixtures are regenerated through the live writer path and re-verified by the
staging subprocess conformance and the Rust conformance suite. The live
projection-commit allocation pin was recalibrated to 400 with both measured
floors recorded (plain 233, race+checkptr 375); the abort pin stays 100. Full
battery green at this state: plain and v4work Go suites, vet, gofmt,
race+checkptr, cross-builds (windows/darwin/freebsd), mmap trace gate, Rust
source graph, full Rust livedb/capi suites, and Rust conformance on the
regenerated fixtures. Slice 2c is complete; slice 2d (platform boundary)
follows.

Sub-state (2026-08-26): slice 2d (platform boundary) part 1 delivered
(working tree `8f20e367+`, gate pending): the Windows housekeeping
surface is public (`ListWindowsHousekeeping` / `RemoveWindowsHousekeeping`
with `WindowsHousekeepingCandidateKind` / `WindowsHousekeepingEntry` /
`WindowsHousekeepingList` / `WindowsHousekeepingPayloadIdentity` /
`WindowsHousekeepingRemoval`, Rust maintenance.rs parity): the internal
live GC machine is wired through exported internal/publication arms with
the Rust control surface (sink Stop -> StoppedBySink, sink error ->
SinkFailed; cancellation-first at every entry like the rest of the
maintenance surface), the non-Windows arms refuse with the exported
OS-unsupported class, and the parity ledger flips both housekeeping rows
to present. The macOS creator-only machine is implemented in pure Go
(internal/security acl_darwin.go + acl_darwin_algo.go): the darwin arm
calls the raw fchmod_extended (283) and fstat_extended (281) syscalls the
libc filesec/fchmodx_np/acl_get_fd_np wrappers use (public XNU
syscalls.master/kauth.h evidence; no cgo), with the Rust apple.rs outcome
classes; the classification is unit-tested on every host and the syscall
glue cross-compiled for darwin amd64/arm64. `CreatorOnlySupported` now
reports true on darwin so the live and publication platform gates open
there; the ledger platform row keeps `missing` until the authorized
native macOS round proves the arm (the remaining slice 2d item is the
version-matched worker on every supported OS/architecture, which needs a
user decision because the POSIX SIGBUS containment machine is
assembly-based on linux/amd64 and each new target requires a new raw
trampoline or another native mechanism). Full battery green at this
state.

Sub-state (2026-08-27): slice 2d (platform boundary) part 2 - the portable
worker - delivered (working tree `1ac7dcdf+`, gate pending): every worker
file is retagged to the four supported OSes (`linux || darwin || freebsd ||
windows`); the package and cmd platform negations are updated to
`!linux && !darwin && !freebsd && !windows`. The mapped-control atomics
became per-target raw assembly (`atomic_{os}_{arch}.s`: plain aligned
32-bit ops on amd64, LDAR/STLR acquire/release plus LDAXR/STLXR CAS on
arm64) so the parent and the naked handler share one authority on every
target. Process reaping is portable (one-shot reaper goroutine + buffered
channel replacing wait4/GetExitCodeProcess), and the worker executable
candidates gained the darwin/freebsd `.exe`-suffix arm (exe_posix.go /
exe_windows.go).

The POSIX SIGBUS containment machine is now implemented on all six POSIX
targets with raw per-ABI syscalls in the naked handler, mirroring posix.rs
exactly: linux amd64 (existing) plus new linux arm64 (SVC, x8, generic
numbers; SA_RESTORER stub), darwin amd64/arm64 (SYSCALL plain numbers /
SVC #0x80 with the number in X16 - the macOS ABI proven on the M4 host;
4-byte sigset, si_addr@24, SA flags 0x40/0x1/0x10/0x4, SS_DISABLE 0x4, no
SA_RESTORER - the kernel sigtramp returns), and freebsd amd64/arm64 (SVC,
x8, FreeBSD 14 numbers 416/417/340/341/53; 128-bit sigset, sigaction
layout handler@0 flags@8 mask@12, si_addr@24, BSD SA flags). The Go-side
install surface (sigaltstack, sigaction query/set, handler address) is a
per-OS twin of the linux machine with the ABI-verified struct layouts
(macOS sigaction is 16 bytes with a 4-byte mask; FreeBSD sigaction is 32
bytes with a 16-byte mask at offset 12; both stack_t are sp@0 size@8
flags@16). Windows uses the pure-Go VEH machine (kernel32
AddVectoredExceptionHandler via system DLL, EXCEPTION_IN_PAGE_ERROR with
the documented parameter layout, same control record, TerminateProcess
with ownedFaultExit). The Linux/Windows amd64+arm64 and darwin/freebsd
amd64+arm64 worker binaries all cross-build; `go vet` passes on all eight
targets including the test files. The handler subprocess proofs
(owned-fault record + runtime chaining) are portable (sigbus_posix_test.go
for linux/darwin/freebsd, sigbus_windows_test.go for the VEH path); the
15-case previous-disposition matrix stays linux/amd64 v4work (its naked
symbols are linux/amd64-specific by design). Full battery green at this
state: plain and v4work Go suites, vet, gofmt, race+checkptr, eight-target
cross-builds and vets, mmap trace gate, Rust conformance on the generated
fixtures. Native rounds on the three authorized hosts follow this commit;
their results and any fixups are recorded here before the slice gate.
Evidence posture for the remaining targets: linux/amd64 is proven by the
committed suite; darwin/arm64, freebsd/amd64, and windows/amd64 are
proven by the authorized native rounds; linux/arm64, darwin/amd64,
freebsd/arm64, and windows/arm64 are cross-compile and vet proven only
(the authorized darwin host is arm64 and has no Rosetta, and no
authorized hosts exist for the remaining three; the darwin amd64 raw
`syscall` convention is the same classless ABI the macOS kernel accepts
from libc `syscall()`).

Sub-state (2026-08-27): slice 2d native round delivered (commits
`76765f21`, `87140582`, `42bee736`, `c162ad34`, `b444712b`, plus the
signed slice commit recorded at the end of this entry; gate pending the
five-reviewer round): the authorized native rounds on the approved hosts
(Option 1: costa-win11 windows/amd64 Go 1.26.5, plakam4mini darwin/arm64
Go 1.26.3, freebsd/amd64) are complete and green, with the following
correctness defects the rounds exposed and the lead fixed:

- Darwin SIGBUS delivery: the kernel stores the installed sigaction's
  `sa_tramp` and jumps every real-handler delivery through it (sendsig
  sets PC to SIGTRAMP), so zero/garbage tramp was fatal at delivery. The
  darwin install/restore arms now supply a 24-byte `__sigaction` input
  (handler@0, tramp@8, mask@16, flags@20; output 16 bytes handler@0
  mask@8 flags@12) with the worker's own sigtramp that calls the catcher
  and sigreturns the interrupted context; proven on the M4.
- Windows worker control sharing: Go unlinks the worker control on every
  platform (Rust's `remove_path` is `#[cfg(unix)]`, so this is a Go-side
  cleanup obligation), and Windows DeleteFileW therefore requires every
  later handle to advertise DELETE sharing while the creator-only
  FILE_ALL_ACCESS handle exists; `OpenWorker` now reopens the control
  through the platform arm (`openControlFile`) with
  read/write/delete share, and the executable-candidate fallthrough also
  accepts `exec.ErrNotFound` (CreateProcess ERROR_FILE_NOT_FOUND).
- Windows delete-blockers in tests: the cmd fixture leaked a plain
  `os.Open` observation handle (no FILE_SHARE_DELETE) that blocked
  TempDir removal, and the import-precondition test leaked a live writer
  (Rust drops it; the Go port now closes it) whose mapped main file
  blocked deletion with ACCESS_DENIED.
- Windows GC/wire kinds: the worker cleanup codecs validated the
  checkpoint security against the hardcoded unix kind (1), rejecting
  the Windows kind-2 wire facts with FormatInvalid; the kinds are now
  platform arms (kind_posix.go / kind_windows.go), the scratch-checkpoint
  fixture creates the artifact through the platform creator-only
  authority (the Windows GC observe proof compares the live
  creator-only commitment, so a bare OS file classified as a
  names-or-identities conflict), and the retained-residue fold uses the
  platform basename-encoding kind.
- Windows fault-record validation: the parent fault-record check
  hardcoded the POSIX rule (si_code > 0); Windows NTSTATUS codes are
  negative int32, so the check is now the Rust platform split
  (unix `code > 0`, windows `code != 0`).
- Windows amd64 mapped-CAS assembly: `mapAtomicCas32` loaded `old` into
  EDX while CMPXCHG compares EAX (left holding the base pointer), so the
  CAS always failed and the vectored handler never claimed an owned
  fault; all four amd64 atomic files were corrected (old into EAX, new
  into EDX, base into SI). The arm64 LDAR/STLXR arms were already
  correct.
- Windows in-page fault arming: hardware EXCEPTION_IN_PAGE_ERROR cannot
  be armed deterministically on Windows (CreateFileMapping refuses a
  maximum size above the file extent with ERROR_COMMITMENT_LIMIT,
  MapViewOfFile cannot exceed the section, and SetEndOfFile refuses
  while a section is open with ERROR_USER_MAPPED_FILE), so the Windows
  subprocess proofs deliver the in-page error synthetically
  (kernel32 RaiseException with the documented parameter layout:
  access type, the accessed page-2 address inside the armed region, and
  the NTSTATUS), exercising the real vectored dispatch, the real
  EXCEPTION_POINTERS record, the record write, and the owned-fault
  termination. The real-binary source-fault fixture truncates the
  source under the worker's open mapping, which Windows refuses by
  design, so that fixture stays POSIX-only exactly like the Rust
  authority (client_tests.rs is cfg(all(test, unix))); Windows proves
  the same record machinery through the synthetic subprocess proofs and
  the declared-page restart through the real-binary refusal test.
- Windows cleanup terminal: the POSIX discard arms always produce
  HousekeepingNone, while the Windows GC machine classifies a fully
  absent removal as CrashReappearancePossible (Rust
  resolver::finish_housekeeping does the same, because Windows
  unlinking is not power-loss durable); `EarlyDiscard.Clean()` and
  `ScratchCleanup.Clean()` now accept the crash-possible terminal when
  every residue and visible artifact is absent, which is the proved-
  absence class. This unblocked the checkpointed-scratch, real-binary
  discard, and fault-retry terminals on Windows.
- FreeBSD race zeroalloc: the FreeBSD -race runtime bills its
  shadow-arena growth through the Go GC (exactly 31 x 4KiB Go-heap
  blocks per publish/count iteration, deterministic on Go 1.26.5), so
  the publish-set page-copy evidence window skips under
  `raceEnabled && GOOS == freebsd`, with the plain FreeBSD build
  proving the SDK path; the same window stays green under race on
  linux and darwin.
- Windows alloc ceiling: the ProjectHistory commit floor is 407
  objects per run on windows/amd64 Go 1.26.5 (vs 233 on linux/amd64),
  so the pinned ceiling moved 400 -> 450 with the platform floors
  recorded; no per-record or per-insert allocation was introduced.
- Windows wire vectors: the worker wire is same-host and same-OS and
  the Rust wire kinds are platform constants (unix.rs 1, windows.rs 2),
  so the Rust-produced vector fixtures are now a per-OS pair; the
  Windows pair was generated on the authorized host with the unchanged
  Rust generator (its POSIX output verified byte-identical to the
  committed POSIX vectors first).

Native proof record at the slice commit: linux/amd64 full battery
(plain, v4work, vet, gofmt, race+checkptr) green; darwin/arm64 (M4,
Go 1.26.3) plain, v4work, and race+checkptr green; freebsd/amd64 plain,
v4work, and race+checkptr green; windows/amd64 (Go 1.26.5) plain,
v4work, and vet green, with race+checkptr on the Windows host blocked by
the host's broken mingw toolchain (mingw-w64-x86_64-gcc 16.1.0-5 exits 1
silently on trivial inputs; no clang present; Go -race on Windows needs a
working cgo CC; the code-level race/checkptr gate remains the committed
Linux battery). Rust suite green at the same tree; all eight targets
cross-build and vet clean including the final tree.

Review gate round 2 (2026-08-27, HEAD `82e022f1`): gate stayed open.
Verdicts: Go-idioms PASS (one P3 record-truth note: the worker row in
`parity_manifest.tsv` still carried the stale `linux/amd64-only`
clause, contradicting the round's own comment fixes); wire/integrity
PASS (P3-2 vector provenance carried); APIs/records PASS (round-1 P2-1
cleanup classification now recorded, comments restored); performance
FAIL with P2-1: the Windows VEH callback first-ever fault executed
`LazyProc.Call` for GetCurrentProcess/TerminateProcess, which inside
`golang.org/x/sys v0.35.0` takes the per-proc mutex, allocates, and
calls GetProcAddress on first resolve - contradicting the handler's
own allocation-free invariant and the Rust import-table authority (the
handler is only ever run once per process, on the terminal fault);
Rust-parity FAIL with P2-1: the Windows worker-control unlink/reopen
is the fourth accepted Go-shape divergence (recorded above) and the
`control_open_windows.go` / `control.go` parity comments omitted the
Rust `#[cfg(unix)]` split, plus P3-1 that the fixes-narrative should
name the unlink as the divergence driver. The fix commit resolves all
of them: `InstallHandler` now pre-resolves the two kernel32 addresses
in ordinary context and the callback terminates through pre-resolved
`syscall.SyscallN` addresses only (runtime-mediated, verified
allocation-, lock-, and panic-free on this path); the control comments
and the divergence
enumeration now disclose the Rust `cfg(unix)` split; the manifest row
lost the stale clause; the narrative names the unlink.

Review gate round 3 (2026-08-27, HEAD `cf07eff2`): all five reviewers
PASS on the same HEAD; slice 2d gate closed. The round-2 P2-1s and
record-truth notes are verified fixed: the Windows VEH callback now
terminates through pre-resolved kernel32 addresses only (performance
PASS; GetCurrentProcess retained by decision to mirror the Rust
authority, pseudo-handle substitution explicitly not required); the
Windows worker-control unlink/reopen is enumerated as the fourth
accepted divergence with the Rust `#[cfg(unix)]` split disclosed in
code comments and narrative (Rust-parity PASS); the manifest worker
row lost the stale clause (Go-idioms PASS); the wire surface is
untouched (wire/integrity PASS, P3-2 vector provenance carried); the
records verify against both trees (APIs/records PASS). Carried
non-blocking P3 notes, no failure scenario: alloc-ceiling sensitivity
at 450 vs the linux floor 233 (performance P3-1, a per-OS ceiling
would restore sensitivity), vector provenance resting on the recorded
generator process (wire P3-2), and the runtime-mediated SyscallN path
as the absolute terminal floor (performance P3-3, a naked-asm stub
would only remove the remaining cgocall entry, not required).

Sub-state (2026-08-27): milestone 3 (symmetric conformance proof, plan
step 6) started after slice 2d's gate closed. Slice plan:
- 3a symmetric corpus: Go-produced membership-ipv4 and membership-ipv6
  fixtures through the public `BeginMembershipTransaction` workflows
  (mirroring generate.rs membership_ipv4/membership_ipv6 op sequences
  exactly: 70 feeds with delete + feed-index reuse on IPv4, global +
  special feeds across the whole IPv6 space with the 1 MiB repeated-
  byte metadata on IPv6), published through the existing public live
  `SnapshotTo` path (regenPublish), added to cases.json and both
  reader inventories; both readers re-verify the extended corpus.
- 3b mixed live cooperation: cross-language subprocess tests cooperate
  on the same live database in both directions (Go parent spawns the
  Rust test binary, Rust parent spawns the Go test binary; children are
  env-gated entries of the other language's test binary, built at test
  time - `cargo test --no-run` / `go test -c` - exactly like the
  same-language subprocess pattern and the explicit regeneration
  entry). Coverage maps the acceptance list: registration/release,
  writer exclusion, reclamation, stale slots, sidecar replacement,
  transition/reservation states, publication inspection, and commit
  resolution. Gate: linux/amd64 with both toolchains available; runs
  documented in the conformance README (explicit env, like fixture
  regeneration) so plain suites stay fast.
- 3c commit-resolution + snapshot cross proofs (same-transaction /
  different-nonce attempts cross-language, compact-snapshot cross-open)
  and README/records updates (the v4/conformance README currently
  carries stale producer-path wording; the Go fixture publisher has
  been SnapshotTo-based since slice 2a).

Sub-state (2026-08-27): milestone 3 slice 3b (mixed live cooperation)
delivered in this commit, working tree green both directions. Child
entries and parents follow the same-language subprocess pattern: the Go
parent builds the Rust `mixed_live` test binary with incremental
`cargo test --no-run` and spawns `mixed_live_rust_child`; the Rust
parent builds the Go test binary with incremental `go test -c` and
spawns `TestMixedLiveGoChild` (cwd set to the Go module root because
the Go TestMain harness resolves the module from the working
directory). Each direction covers: generation read-back incl. the
generation-2 residue (publication inspection + sidecar replacement
across two commits), writer exclusion while the other language holds
the writer (Rust `Error::WriterBusy` / Go `ErrorWriterBusy`), and
pinned-read reclamation (the holding side's `Reclaim` returns NoChange
while the foreign-language reader is pinned and reclaims after its
release - stale-slot release and oldest-reader safety across
languages). The protocol is stdin-held for the pinned reader with a
READY line, 90 s deadline, unique temp paths, and full cleanup; the
battery is env-gated (`IPRANGE_V4_MIXED_LIVE=1`, linux/amd64-only,
both toolchains, documented in the conformance README) so plain suites
stay green and fast without the other toolchain. Plain Go (incl.
v4work) and Rust suites stay green at this state.

Observation recorded for the gate (APIs/records scope): the public live
open surface returns internal `*format.Error` values directly (e.g.
`OpenLiveWriter`), which external consumers cannot name or classify
through the exported `Error` type; the test convention `isPubCode`
works by matching the internal type from inside the module. Whether
the public surface must wrap live-opening failures into the exported
`Error` is deferred to the gate reviewers (acceptance criterion: public
failures must be accessible as the exported error type).

Sub-state (2026-08-27): milestone 3 (symmetric conformance proof, plan
step 6) delivered in this commit, slice by slice: 3a added the
Go-produced membership-ipv4/ipv6 fixtures through the public
membership transactions and the live SnapshotTo publish (13-fixture
corpus, both readers re-verify); 3b added the mixed live cooperation
battery in both directions (read-back, writer exclusion, pinned-read
reclamation); 3c added the cross-language commit-resolution attempt set
(committed / same-transaction different-nonce / superseded-unknown,
each SDK committing its own generation on the same live database and
classifying identically) and the compact-snapshot cross-open (each SDK
snapshots through its public snapshot path, the other reader opens and
verifies). Five modes per direction, all green with
IPRANGE_V4_MIXED_LIVE=1 on linux/amd64; plain suites stay green
without the env or the other toolchain. The public live-open error
shape observation recorded at 3b stays open for the gate reviewers.
Milestone 3 gate pending the five-reviewer round at this HEAD. Plan
steps 7-8 (matched update-ipsets release benchmark matrix; artifacts,
docs, and full-scope acceptance) remain after the gate.

Review gate round 1 for milestone 3 (2026-08-27, HEAD `58532bfc`):
gate stayed open. Verdicts: Rust parity PASS (generators and mixed
battery mirror generate.rs and the canonical live/commit tests
instruction-for-instruction; one P3 identity-kind observation recorded
above); Go idioms PASS (one P3 wording label for the IPv6 union range,
fixed in this commit); performance PASS (delta is test/corpus/records
only, no production hot-path surface); wire/integrity PASS (13-fixture
corpus verified by both readers, resolve classes and snapshot
cross-open proven in both directions; wording P3 fixed here).
APIs/records FAIL with P1-1: the public live surface (OpenLiveWriter,
LiveWriter facade, transactions, workflows) returned internal
`*format.Error` / `classedError` values raw, so external consumers
could not classify failures through the exported `Error` type - a
direct acceptance-criterion violation; plus P3-1 (README still said
eleven fixtures). The fix commit converts the seven live public files
through the existing `publicError` boundary (bare-error returns and
`&format.Error{...}` constructions become the exported `Error`), the
abort helpers return the public error, `publicError` preserves the
original error as `Cause` (classed wrappers keep their unwrap chain)
and passes through already-public errors, `isPubCode` classifies both
shapes, and `abortCauseCode` walks the public-classed-cause chain.
Gate pending the five-reviewer round 2 at the fix HEAD.

Review gate round 2 for milestone 3 (2026-08-27, HEAD `25ec4c84`):
gate stayed open. Verdicts: Rust parity FAIL P1 (the live result
surface still exposed internal error types: the `Abort()` incomplete
path returned `result.Cause` raw and `commitPrepared`'s `Err` plus
`publicCommitResult`/`publicAbortResult`/`publicCloseResult` `Cause`
fields carried unclassified `*format.Error`/`classedError` values,
`live_writer_public.go:219/245/727/738/749`, so external consumers
still could not classify those failures through the exported `Error`);
Go idioms FAIL P1 and APIs/records FAIL P2-1 with the same five sites;
performance PASS (remaining work is failure-path-only; P3: the
`Abort()` incomplete path returned the raw cause); wire/integrity PASS
(error typing only, zero on-disk or coordination change). The fix
converts all five sites through `publicError`, and `publicError` now
consumes the outermost classed class into the exported `Error` and
converts the rest of the chain recursively, so no internal error type
crosses the public boundary (the previous preserve-Cause passthrough
also let a `classedError` wrapping a public cancellation cause escape
unchanged). The test classifiers (`lifecycleCode`, `isPubCode`,
`pubCode`) classify only the top of the chain, so an inner class
never shadows the outer one (Rust abort_after_source nesting:
TransactionAborted -> CleanupInProgress -> original cause). Validation
at the fix HEAD: gofmt, vet, `go test ./...`, `go test -tags v4work
./...` (including the outcome-unknown fail-closed nested-class
chain), race/checkptr, the seven-target cross-build matrix, and the
env-gated mixed live battery on both sides are green. Gate pending
the five-reviewer round 3 at the fix HEAD.

Review gate round 3 for milestone 3 (2026-08-27, HEAD `085edea9`):
gate stayed open. Verdicts: Rust parity PASS (all-public chain mirrors
`abort_after_source` nesting, verified against sdk_error.rs), Go
idioms PASS (classifiers now document the boundary-authority
convention), performance PASS (failure-path-only conversion, bounded
recursion), wire/integrity PASS (zero on-disk or coordination change,
13-fixture corpus re-verified). APIs/records FAIL with P1-1 + P2-1:
the live-coordination lifecycle facade never used `publicError` -
`CreateLive`, `InitializeLive`, `ResetLiveCoordination`,
`ResolveLiveTransition`, `ResolveCreateLive`, and
`ResolveInterruptedLiveTransition` returned hard failures raw, and
`publicCreateResult`/`publicTransitionResult`/`publicResidueResult`
copied internal `*format.Error`/`classedError` values into the public
`Cause` fields (`lifecycle_public.go:163/180/351/366/382/397/205/234/
417`), so external consumers could not classify lifecycle failures
through the exported `Error`; the same scan also found the exported
`NewFeedName` returning a raw internal `*format.Error`
(`feed_name.go:23`). The round-2 closing claim that no internal error
type crosses the public boundary was scoped to the live-writer surface
and missed the lifecycle facade; this entry supersedes it. The fix
routes all nine lifecycle sites and `NewFeedName` through `publicError`
(the same boundary rule as the live-writer surface). Validation at the
fix HEAD: gofmt, vet, `go test ./...`, `go test -tags v4work ./...`,
race/checkptr, the seven-target cross-build matrix, and the env-gated
mixed live battery on both sides are green. Gate pending the
five-reviewer round 4 at the fix HEAD.

Review gate round 4 for milestone 3 (2026-08-27, HEAD `0e70e9b9`):
all five reviewers PASS. Rust parity: the lifecycle facade exposes
every failure as the exported `Error` with the Rust `live_lifecycle`
classes preserved (InvalidArgument, WrongStructureKind, WrongState,
CleanupConflict, IO, CleanupInProgress), no internal error type
crosses the public boundary. Go idioms: the boundary convention is
uniform across the live, lifecycle, workflow, reader, snapshot,
recovery, and publication surfaces; P3 noted that `NewFeedName`
blends the cleanest when it constructs the exported `Error` directly -
applied as the one-line cleanup included in the gate-close commit.
Performance: failure-path-only conversion on cold lifecycle surfaces,
no hot-path or success-path cost. Wire/integrity: error-typing only,
internal calls argument-identical, resolve paths never read `Cause`,
zero on-disk or coordination change. APIs/records: independent
same-defect scan of the root package found zero raw internal error
escapes (the remaining `format.Error` constructors feed the converting
abort helpers, internal-direction callbacks, or the `publicError`
machinery itself). Milestone 3 gate: CLOSED at the fix HEAD (after
the `NewFeedName` cleanup, re-validated: gofmt, vet, `go test ./...`,
`go test -tags v4work ./...`, race/checkptr, the seven-target
cross-build matrix, and the env-gated mixed live battery on both
sides are green). Plan steps 7-8 (matched update-ipsets release
benchmark matrix; artifacts, docs, and full-scope acceptance review)
remain for milestone 4.

Milestone 4 started (plan step 7): the Go `update-ipsets` benchmark
matrix port. Slice 4a/4b delivered: `v4/go/cmd/iprange-v4-bench/`
mirrors the Rust harness (v4/rust/iprange-livedb/benches/update_ipsets)
- CLI modes smoke/scale/local/ci/sample/case/header, subprocess-per-case
isolation, elapsed + allocation (MemStats Mallocs/TotalAlloc deltas) +
Linux RSS before/after/peak (VmRSS/VmHWM) + fd counts + logical/physical
file size + private-artifact refusal, the optional
IPRANGE_PERF_CONTROL/IPRANGE_PERF_ACK protocol, per-case validation of
every output database, and the accepted-baseline CI comparison with the
same CSV contracts. All 57 Rust scenario arms ported
(scenario_direct.go 6, scenario_membership.go 16, scenario_read.go 11,
scenario_structured.go 11, scenario_sdk.go 13), registered under the
exact Rust names. Matched smoke evidence on linux/amd64 (both harnesses,
same host, release builds): all 55 smoke cases run green on both sides
and the semantic facts (work units, emitted units, range records, feeds,
file logical bytes) are identical 55/55 after aligning the one deviating
scenario (live-membership-validation now ports membership::populated_rotating
exactly). Go runtimes at smoke scale measured 0.34x-88.5x of Rust in the
pre-fix pair (direct-replace 1000 at 0.34x, live-membership-validation
4000 at 88.5x from the then-unfixed external-poll ping-pong); at the
milestone-4 fix HEAD the smoke range (evidence/smoke-go-final.csv vs
the Rust reference) is 0.42x-7.59x: Go faster on direct-replace 1000
(0.42x) and snapshot/live-open-256 4000 (0.76-0.90x), Go slower on
structured-assign-random 4000 (7.59x) and every other case. The material
deltas need profiling at scale and actionable-waste removal before the
release evidence. Baseline CSV intentionally empty (status untracked)
until the matched scale run populates it.

Scale measurement runs (2026-08-27, linux/amd64, release builds, both
harnesses, under `nice`): the full scale matrix is about 90
subprocess-isolated cases at 10k-1M records per language; expected cost
is roughly 15-30 wall-minutes and 15-30 core-minutes per language
(single-core harness design, same as Rust). The runs produce the
matched release evidence: semantic facts, elapsed, allocations, RSS,
fds, file sizes. Material per-scenario deltas then get profiled with
pprof/cargo flamegraphs to separate actionable Go waste from designed
costs (for example the isolation-worker spawn in validation).

Milestone-4 scale catch (2026-08-27): the Go scale matrix exposed a
real structured-table defect that smoke never reaches (smoke caps
structured-build-random at 4,000 ids; the corruption starts at ids
>= 25,600). `structured-build-random` at 28k/421 validated with 2,401
ReasonStructureInvalid findings on the structure dictionary; Rust
passes the same case at 1M. Root cause (fixed): the Go validation and
recovery table walkers scaled a directory's child bases by
`StructureSpanOfLevel(level-1)` (the 50-id level-0 span) where the Rust
authority uses `table::coverage(level-1)` - the full 25,600-id span of
a level-2 child. The writer and reader already used the correct span;
the stored tables were always correct, and the two walkers mis-derived
every implied id above the first level-1 coverage. The bug was a
permanent-regression gate gap: level-2 roots (ID limits above 25,600)
were never walked by any Go test. Fix: validation
`structure_table.go` and recovery `structure_index.go` now scale
branch bases with `StructureSpanOfLevel(header.Level)`, the test
descender in validation/structure_test.go aligns to the same rule, and
two new regression tests pin the boundary:
`TestValidateStructureLevelTwoRootClean` and
`TestRecoveryStructureCountLevelTwoRoot` (synthetic three-level
generations with records at ids 1 and 25,600; both fail on the old
span and pass on the fixed span). The temporary validator
instrumentation used to localize the mismatch was removed. After the
fix: `structured-build-random 28000 421` validates clean (repro exit
0), and the full Go suite (`go test ./...`) is green. The complete
scale evidence below supersedes the partial 48-case run that stopped
at this defect.

Milestone-4 semantic probe catch (2026-08-27, after the walker fix):
cross-language membership probing of the matched scale artifacts found
a second real defect the validator cannot see: the Go name-based
membership import translated every source bitmap word with three or
more set bits wrongly. The Go `mapSourceWord` bit walk re-counted the
bit position from zero while shifting the word right, so bits at
positions 2 and above collapsed onto the second-lowest bit; every
imported membership lost feeds silently while staying internally
consistent (the Go validator passes the wrong file, because it proves
dictionary/hash/bitmap consistency, not import semantics). Repro at
membership-import 10k/421: the source's full 421-feed union imported as
a 14-bit bitmap ({0,1,64,65,...}) instead of 421 bits; the Rust import
of the identical source produces the exact 421-feed union. The Rust
authority (draft_store/import_cache.rs map_source_word) uses
`word.trailing_zeros()` with `word &= word - 1`; the Go port's shifting
variant was the only such loop in the codebase. Fix: rewrite the Go
walk with `math/bits.TrailingZeros64` and the same clear-lowest-bit
step (`v4/go/internal/writer/import_cache.go`). Regression test
`TestLiveImportPreservesWideMembershipBitmap` (public import of a
four-feed one-word union) fails on the old loop exactly at
"address 0 lost feed gamma" and passes on the fixed loop; the existing
two-feed import tests sat below the corruption threshold (a two-bit
word is handled correctly by both loops), which is why the defect
shipped unnoticed. After the fix, the repro destination reports
421/421 feeds at every probed address, byte-equivalent to the Rust
output's membership semantics. The matched scale matrices and their
fact comparisons were re-run at the fix HEAD and supersede the numbers
above for the affected membership-import cases.

Matched scale evidence at the fix HEAD (2026-08-27, linux/amd64,
release builds, both harnesses, under `nice`): the full Go scale
matrix is green 90/90 after the two fixes above (the earlier 48-case
partial run stopped at the structure-table defect). Go and Rust agree
on every semantic fact (work units, emitted units, range records,
feeds, file logical bytes) in all 90 cases; the remaining 29
differences are physical-only (unpublished tail granularity), matching
the authoritative commitment granularity difference already recorded.
Elapsed ratios Go/Rust (p50, same host, release, verified against the
matched CSVs): direct and membership lookups 1.9-4.0x (live-direct
100k=3.96x, live-membership 100k=3.41x, random variants 1.9-2.4x),
structured random lookups 1.6-2.0x (scalar 1M=1.98x, threat 1M=1.65x),
structured scan 3.6x, membership-import 8.8x, update-ipsets-workflow
5.4x, feed-replace 6.8x, nested-overwrite 8.9x, membership-cardinalities
1.9x, and the worker-mediated validation paths 445-640x
(live-validation 1M: Go 4.54 s for 4,113 checked pages vs Rust 10.2 ms;
live-membership-validation 1M: Go 9.88 s vs Rust 15.4 ms).
Milestone-4 perf root cause found and fixed (2026-08-27): the
445-640x validation ratios were not Go engine cost at all. The gdb
goroutine dump of a mid-validation isolation worker proved the real
cause: the worker's main goroutine was sleeping in
`Control.RequestExternalPoll`'s CancelPoll spin (control_wire.go:298)
from the checkpoint hook, called once per checked page (direct) and
once per structure slot (structured). External polling was enabled
because the public entries passed `cancellation.check` for a nil
token: `CancellationToken.check` is a nil-receiver method, and the
method value `(*CancellationToken)(nil).check` is non-nil, so
`RequiresExternalPoll` returned true and every checkpoint became a
~1-2 ms parent round trip (the "hang" was this ping-pong: 100k
structure slots x ~2 ms = minutes). Rust's fresh token has no poll
hook (`cancellation.rs requires_external_poll` = poll.is_some(), false)
and disables external polling. Fix: `CancellationToken.hook()` returns
nil for a nil token and the three worker-routed public entries
(`Validate`, `InspectRecoveryCandidates`,
`ValidateOfflineCandidate`) use it; the Recover arms already used
`internalCheck`, which handles nil correctly. Regression test
`TestNilCancellationHookIsNil`. Measured impact (release, same host,
under `nice`): live-validation 1M 4.54 s -> 24.2 ms (188x; Rust
10.2 ms, now 2.4x), live-membership-validation 1M 9.88 s -> 28.9 ms
(342x; Rust 15.4 ms, now 1.9x), live-structured-threat-random-lookup
100k case wall ~3.5 min -> 6.6 s total (measured op 5.88 s for 10M
lookups, ~1.25x Rust). The full Go suite is green at the fix. Profile
tooling note: IPRANGE_CPU_PROFILE now writes one pprof file per
process (PID-suffixed), so the bench case process and its worker each
produce their own profile instead of clobbering one path.

Accepted-baseline population completed at the fix HEAD (2026-08-27,
linux/amd64, release Go build, under `nice`): all 18 CI cases (1
warmup + 5 samples each) completed in about 8 wall-minutes
single-core (the pre-fix binary needed 50+ minutes to reach case
12/18 and was projected at 1.5-2 hours). The accepted-baseline.csv in
v4/go/cmd/iprange-v4-bench/ is populated from the p50 medians with
the disaster limits (2x p50 + 100 ms runner noise, 500 ms for the
complete workflow). The `ci` gate re-run at the fix HEAD (1 warmup +
3 samples, enforce mode) passes 18/18 within-limit; the observed CI
median ratio spans 0.957-1.109 across the 18 cases in the final gate
log (committed under v4/go/cmd/iprange-v4-bench/evidence/); the
highest limit utilization is 51.5% (live-structured-scalar-random-
lookup), so all cases hold with more than 2x disaster headroom.
Validation medians at the fix HEAD: live-validation 1M = 25.5 ms and
live-membership-validation 1M = 30.6 ms (Rust 10.2/15.4 ms), so the
CI gate additionally proves the nil-token fix holds under gate
conditions.

Final milestone-4 evidence at the fix HEAD (2026-08-27, linux/amd64,
release builds, under `nice`): re-run after the nil-token fix. Smoke
matrix: 55/55 semantic facts identical to the pre-fix run (work
units, emitted units, range records, feeds, logical file bytes), with
the validation rows now at 4-5 ms at the 4k scale (pre-fix these
rows carried the external-poll ping-pong). Scale validation rows:
live-validation 1M 28.8 ms, live-membership-validation 1M 29.8 ms,
immutable-validation 1M 29.7 ms (Rust 10.2/15.4 ms for the live
rows), i.e. the validation family went from 408-640x to 1.9-3.2x
(immutable-validation 1M 29.7 ms vs Rust 9.3 ms = 3.2x).
The remaining material Go/Rust p50 deltas at scale, with the pprof
attribution (IPRANGE_CPU_PROFILE over the bench `case` child, one
profile per process):
- live direct/membership lookups 1.9-4.0x and structured random
  lookups 1.25-2.0x: fixed-tree probe cost. The shared
  `internal/tree.lowerBoundBy` calls a keyAt closure per probe with
  bounds-checked codec cell decodes (profiles: lookupRange4 +
  lowerBoundBy + SlottedPage.Record); Rust's generic binary search
  inlines the comparator. The lookup path stays allocation-free (0-1
  allocs, 0-16 bytes, vs Rust 0).
- membership-import 1M 8.85x (scale Go 412 ms vs Rust 46.6 ms; the
  standalone profile run measured 496 ms): the same
  lowerBoundBy probes dominate the profiles, and the Go write seam
  allocates per record: 3,001,943 alloc calls / 1,536 MB vs Rust
  20 calls / 474 B (the per-range Mutate closures in the import
  loop; the import cache itself is draft-page backed like Rust).
- nested-overwrite 1M 8.9x, feed-replace 1M 6.8x,
  update-ipsets-workflow 1M 5.4x: profiles show the same two causes
  (lowerBoundBy/InsertIfLocalGap probes + per-operation mutation
  seam); the workflow additionally runs the full user pipeline
  (feeds, imports, publishes) so the delta is the sum of the same
  unit costs.
The single recurring hot symbol across all five profiled deltas is
the probe path of the one shared fixed-tree search (lowerBoundBy +
its generic comparator closure); the second is the per-record write
seam allocation. Both are explained designed costs of the Go
abstraction layer (one shared tree codec over four record layouts;
one lock-scoped Mutate per record like the Rust mutable brace, but
a closure escape per call). They are tracked as follow-up
optimizations below, not silent debt: the reader hot paths ship at
1.25-4.0x with zero allocations, and the write paths ship
subsecond-to-low-seconds at the 1M scale.
Accepted CI gate record: final gate run at the fix HEAD with the
accepted Go baseline identity (baselineID go-v4-local-20260827)
re-verifies 18/18 within-limit.

Milestone-4 extension (user decision 2026-08-28, in scope before
acceptance): (1) slice 4c specializes the fixed-tree search per
record codec (inline key comparators, no per-probe closure) - the
old external-review recommendation that now has profile evidence
across every remaining delta; (2) slice 4d batches the
membership-import mutation seam to cut the per-record allocation
class; (3) slice 4e re-measures, re-baselines, and re-gates the
accepted evidence. The accepted milestone-4 evidence remains valid
as the pre-optimization baseline for the 4c/4d deltas.

Milestone-4 five-reviewer gate round (2026-08-28, HEAD 81620b63):
scope reviews were PASS for Go idioms, performance, and wire/integrity,
and FAIL for records (two falsifiable P2 claims) and Rust parity (one
P2 harness guard). All findings fixed in the follow-up commit:
- Records P2-1: the CI median-ratio claim "0.98-1.09" was wrong for
  the final gate artifact; corrected to the committed final gate log
  (ratio range 0.957-1.109, highest limit utilization 51.5%), and the
  gate log is now committed as evidence. P2-2: the smoke ratio range
  claim ("0.4x-9.7x, none faster than noise") predated the aligned
  run; corrected to the measured pre-fix pair (0.34x-88.5x) and the
  fix-HEAD range from the committed final smoke CSV (0.42x-7.59x;
  earlier 0.66x-6.64x belonged to the 23:29 pre-batch-reuse run and
  is superseded). P3s: membership-import parenthetical now separates
  the scale ratio (8.85x, 412 ms) from the profile run (496 ms); the
  validation family range is 1.9-3.2x including immutable-validation;
  the baseline cost is stated as ~8 wall-minutes for the sample run.
- Rust parity P2: the bench source guard accepted count+phase up to
  2^32-1 where Rust require_address_space refuses above 2^30, so
  oversized user-invoked sample/case inputs wrapped the uint32
  address arithmetic instead of erroring. The Go guard is now
  (math.MaxUint32-1)/4+1 with the same refusal message; regression
  tests pin the boundary. Rust parity P3: the streaming sources now
  reuse one [batchCapacity] backing slice per source like Rust's
  [T;1024], removing the per-batch allocations from the measured
  region (feed-first-ascending 1M: 1,222 calls / 8,048,152 bytes in the
  first accepted-run sample artifact dropped to 227 calls / 40,048
  bytes in the final gate log); a test pins the reused backing
  buffers.
- Recording-only: the batch reuse improves the harness allocation
  facts for streaming scenarios, so the accepted baseline and the CI
  gate were re-run with the final harness and the accepted-baseline.csv
  and the committed evidence logs were refreshed at the final HEAD.
- Standing measurement caveats recorded (non-blocking): rssPeak uses
  VmHWM so setup-heavy cases overbound the measured region; rssBefore
  is sampled before the pre-baseline GC; worker-mediated rows exclude
  the worker subprocess's allocations (the only cross-language alloc
  comparison cited, membership-import, is in-process on both sides);
  Go allocation capture is GC + MemStats deltas (inherent Go shape);
  immutable-feed-random's measured region differs structurally
  (one-inode feed creation is a recorded pending gap) and is excluded
  from the CI 18; the worker pprof hook PID suffix noted by one
  reviewer was already present in the committed fix; scenario_sdk.go
  at 1,668 lines exceeds the soft 500-line guideline but is cohesive
  (one workflow per scenario, clear sections).

Milestone-4 performance mandate (recorded 2026-08-28; envelope VOIDED
2026-08-29 per the Binding Performance Acceptance above): the recorded
4-9x write-path deltas and 2-4x read-path deltas are NOT acceptable as
explained designed costs; Go must not be significantly inferior to
Rust. Both harnesses were re-verified as optimized production builds
(Rust bench profile opt-level 3, same binary that produced the
evidence; Go plain go build, gc, GOAMD64=v1; GOAMD64=v3 changes
nothing). Milestone 4 extends with three optimization slices before
acceptance: 4c specialized key-only fixed-tree search probes (4d
write-seam allocation removal, 4e re-measure + re-baseline + re-gate +
evidence refresh. The numeric targets formerly listed here (reads
~1.2-1.6x, validation ~1.5-2x, writes ~2-3.5x) were assistant-authored
inference, not user approval, and are replaced by the binding <=1.3x
criterion; the 4-9x class must be eliminated in any case.

Slice 4c/4d implementation record (2026-08-28, commits 483fc37f and
follow-ups): the per-probe closure plus interface ReadKey plus
materialized 88-byte Key was the top CPU symbol in every remaining
delta. lowerBound is now closure-free; codecs whose fixed cells carry
the ordered key as a plain prefix opt into an inline width-specialized
probe (u32/u64/u128 little-endian or raw digest+suffix words) that
keeps the persistent slot-table read per probe; every codec implements
Codec.CompareKey without building a Key. The membership dictionary
transactions and the membership-import range seams bind one WriterEdit
per workflow instead of allocating a fresh DraftStore per record
(Rust borrowed-&mut-WriterEdit parity); the import loop also stores
the previous canonical range by value, removing the per-record
escape. fixedPositions writes into a bounded int16 scratch
([u16; MAX_SLOT_COUNT] parity) and branch cells encode into
caller-owned CellBufs, removing the truncate and split allocations.
Bench harness: heap-profile hook for the delta work and reused batch
buffers in the measured regions. Measured medians at 1M records,
same host, matched optimized release binaries (Go gc plain build,
Rust bench profile); every quoted median is reproducible from the
committed evidence rows (evidence/case-runs-4c4d-20260828.csv for
the headline case runs, evidence/ci-go-v4-local-20260828b.log for
the final gate):

- membership-import: 170.5 ms vs Rust 47.1 ms (3.6x; was 430.8 ms /
  9.1x with 3,001,958 allocs / 1,536.7 MB, now 2,311 allocs /
  0.7 MB; Rust has 20 allocs / 470 B).
- nested-overwrite: 1,932.9 ms vs Rust 301.7 ms (6.4x; was
  2,887.6 ms).
- update-ipsets-workflow: 4,878 ms vs Rust 1,112 ms (4.4x; was
  5,780.7 ms, then 5,038 ms at the 4c/4d commits; the delta-review
  value-return of the selected-ranges feed cut the run allocation
  volume from 21.2M calls / 752 MB to 10.7M calls / 369 MB).
- live-direct-random-lookup: 492.1 ms vs Rust 236.8 ms (2.1x; was
  596.5 ms).
- immutable-direct-random-lookup: 420.2 ms vs Rust 217 ms (1.9x).
- live-validation: 25.6 ms vs Rust 14.1 ms (1.8x; was 24.6 ms).

The 4-9x class is eliminated for the lookup, validation, and import
families; the remaining write-path outliers are nested-overwrite
(6.4x) and update-ipsets-workflow (4.4x), both deep in the generic
gap/replace machinery where the residual cost is Go bounds checks,
interface indirection, and GC, not structure (verified against the
Rust range_mutation assign paths). Slice 4e re-baselined and
re-gated the acceptance evidence at the improved state: the accepted
baseline identity is now go-v4-local-20260828 (rebuilt
accepted-baseline.csv from 18 CI cases x 5 samples; the pre-4c
20260827 samples stay in evidence/ as the improvement baseline), the
final CI gate run is 18/18 within-limit with ratios 0.940-1.106
(update-ipsets-workflow ratio 0.964 against the accepted 5,058 ms),
and the smoke pair range improved to 0.34x-5.92x. pprof head
evidence before/after is committed at
evidence/profiles-4c4d-summary.txt.

Milestone-4 delta review (2026-08-28, five level-1 reviewers of the
lead model, adversarial mode; scopes: Rust parity Linnaeus, Go idioms
Darwin, performance Hooke, wire/integrity McClintock, APIs/records
Plato). Verdicts: Darwin PASS (P3: keycmp.go hand-rolled compares
vs cmp.Compare; optional probe-set enumeration test), Hooke PASS
(P3-1: the u32 prefix codecs fell back to the record-decode compare
path - fixed by declaring PrefixKeyProbe on idCodec/indexCodec,
commit dbcf0008; P3-2 per-width probe specialization noted as
moderate optional; P3-3 workflow seam - fixed by the value-return
sweep, commit 6f66e32c). Three FAILs with actionable findings, all
fixed and re-tested:

- Linnaeus P2 (Rust parity): the raw prefix probe dropped the
  per-probe cell validation of the membership and structure hash
  codecs; corrupt cells (zero id, out of range word count) were
  compared blindly where Rust decode_hash refuses them. Fixed with
  an optional tree.ProbeValidator applied per probe only on the two
  hash codecs (nil for the plain numeric probes, keeping the hot
  u32/u64/u128 paths dispatch-free), commit 93f81997; regression
  tests corrupt the root page middle cell and require
  CodeFormatInvalid with healthy controls.
- McClintock P2 (wire/integrity): the 4c sweep wrapped the edit
  cause in publicError before abortEdit/abortAfter, hiding the
  internal class from isFatalClass; a CodeFormatInvalid mid-edit
  left the writer falsely healthy (Rust abort_after brands the
  writer unusable on Io/Format). The abort paths pass the raw
  internal error again with the public conversion at the boundary
  (the structured transaction already did), commit 90b4d885;
  regression test drives a v4work fault point inside
  deleteCurrentFeedMembership and asserts the writer fails closed.
- Plato P2-1 (records): the committed 20260828 CI log header still
  named the 20260827 identity. Fixed by committing the final gate
  log at the final identity as ci-go-v4-local-20260828b.log (the
  old file stays with an explanatory README note).
- Plato P2-2 (records): six quoted before/after medians were not
  reproducible from the preserved case runs. Fixed by correcting
  the table to the preserved artifact rows and committing them as
  evidence/case-runs-4c4d-20260828.csv.

The re-gate after the four fix commits is 18/18 within-limit at
ratios 0.940-1.106; all local tests, v4work, race, vet, and gofmt
pass at commits 6f66e32c..93f81997.

Independent external review (2026-08-28, single sol reviewer, read-only,
session 7ade436af6fb434ea2e25b78a806b02e): verdict NEEDS CHANGES with
four P1, three P2, and one P3 finding. Adjudication:

- P1 records: the Go-baseline CI gate (2x Go p50 limits) proves
  regression stability only, not Rust parity; milestone 4's mandate
  compares Go against Rust. Fixed by committing the separate
  Rust-ratio acceptance report: evidence/rust-ratio-acceptance-
  20260828.csv with matched fresh medians of the final release
  binaries (same host, both sides; raw samples committed alongside).
  Measured ratios: membership-import 3.5x (165.3 ms vs 47.1 ms),
  nested-overwrite 6.7x (1,931.5 ms vs 290.2 ms),
  update-ipsets-workflow 4.4x (4,895.1 ms vs 1,104.1 ms),
  live-direct-random-lookup 1.9x (466.3 ms vs 241.7 ms),
  immutable-direct-random-lookup 2.1x (436.0 ms vs 211.9 ms),
  live-validation 2.6x (28.9 ms vs 11.0 ms). Write-path ratios
  remain above the mandate's 2-3.5x target.
- P1 gap machinery copies: the 752-byte descent Path was passed by
  value into every gap probe, and the selector used value receivers.
  Fixed with pointer receivers and a *Path (nil keeps the vacuous
  edge semantics of the cached-leaf probe), commit fc9df1ea.
- P1 universal keys: verified - Go range records carry two 88-byte
  universal tree.Key values (184-byte decoded records) where Rust
  uses a u32 IPv4Key (12-byte records), plus interface dispatch per
  assignment. Structural fix (key-typed tree core or generated
  specializations) is high effort and high integration risk under
  the one-authoritative-implementation rule; NOT implemented -
  recorded for a user design decision.
- P1 transform segments: segmentAt returned pointer segments; fixed
  by value (compiler escape analysis had already kept them on the
  stack; measured impact noise-level, escape hazard removed).
- P2 rejected-neighbor decodes: the RequireReplacement validation
  volume is exact Rust parity (3 ReadLeaf + 4 ReadKey per 3-cell
  replacement); the selector's third neighbor decode is a real but
  ~2-4% upper-bound inefficiency that needs a generic LocalGap
  interface refactor - recorded, not implemented.
- P2 probe-width switch: implemented as monomorphized width compares,
  measured 1-3% REGRESSION (the generic-interface method call per
  probe is not inlined), reverted; the per-probe switch stays.
- P2 streaming facades: verified. The per-record string conversions
  measure at most one object per distinct delivered name (Go escape
  analysis stack-allocates the short-lived conversions under the
  current toolchain); the join direct-value pointers allocated one
  heap scalar per mapped cell. Fixed: one owned string per distinct
  feed name shared across batches, a per-batch value arena for the
  optional direct values (documented batch-lifetime contract), and
  slices.SortFunc in the join emitter (no reflect). Two allocation
  gates pin the classes (real-sink aggregate test with a names+96
  bound; internal emit test at one allocation per 300-cell run),
  commit fc9df1ea.
- P3 heap optionals: PrivateEdge pending first-key and RemovedRun
  following are inline values now (commit fc9df1ea).

Measured impact of the adjudicated fix set: all headline cases within
run noise of the pre-review state (the remaining ratio class is the
structural universal-key design). All tests, v4work, race, vet, and
gofmt pass at fc9df1ea.

## Design Record - 2026-08-31 (structural performance analysis)

Status: analysis-only deliverable of the measured-performance decision
loop (sol direction 2026-08-31: keep the <=1.3x gate, reject the
validation-only attempt, repair the closure package, then design-only
structural analysis before authorizing more implementation). No
implementation is authorized by this record; the user decides which
designs to pursue.

### Exact-final-commit five-scope review (b1a3a92d)

The review sol required on one exact final commit was issued after the
record/observability repairs (worker hooks moved to v4work-tagged
files, metric renamed to elapsed time, stale +3.4% claims removed).
All five scopes PASS at b1a3a92d:

- Boole (Rust parity): PASS. Counter parity exact (bytes_moved
  69,145,640 = Rust, bytes_zeroed 8,487,946 = Rust); the seal recount
  (bdf642b2) counts exactly once per sealed page on both paths; the
  observability move leaves the worker protocol/control surface
  byte-identical; no stale current claims remain.
- Sagan (Go idioms): PASS. The phases.go (prod no-op) /
  phases_v4work.go (real hooks) split matches the work-package build
  tag convention and leaks no observability imports into the
  production binary; meta/seal counters follow the single-authority
  pattern with no double counting; vet/gofmt clean. Two P3s fixed in
  this record commit: the pages_visited attribution named
  mapping_growths where the CSV row is mapping_remaps (1 vs 3), and
  a stale workerPhases identifier in the run() doc.
- Hegel (performance): PASS. The final matrix data rows are
  byte-identical to the 33b3a8dd verification (header-only rename to
  elapsed_ratio); every median recomputed from the committed raw
  samples; all counter instrumentation compiles to no-ops in
  production so no measured hot path changed; the evidence-backed
  read/write/validation plateaus stand. His P3-1 (bytes_moved
  attribution) is closed by the counter fixes; P3-2 (dual validation
  numbers) is reconciled in the records.
- Herschel (wire/integrity): PASS. Diff 33b3a8dd..b1a3a92d over
  format and worker shows counters, comments, and records only - no
  constant, offset, layout, opcode, or state value changed; the seal
  counter move writes the same bytes; conformance re-run green in
  both directions (14 fixtures + invalid mutations, Go and Rust).
- Turing (APIs/records): PASS. The +3.4% sweep leaves only the dated
  historical narrative; every current-claim position uses elapsed
  time and exact counter parity; verdicts are anchored per commit;
  audit shows only the pre-existing done/SOW-0025 mismatch. P3s
  fixed in this record commit: six leftover "CPU" label positions,
  the validation-phases CSV unquoted note fields, and a one-line
  worker-observability note added to the item-3 sub-state.

NOTE on anchoring: the round above was anchored to b1a3a92d. Per the
project-final-review policy, any later commit invalidates the anchored
verdicts (what changed after b1a3a92d is record-only or bench-tooling,
but the policy is strict). The verdicts therefore do NOT constitute an
exact-final-commit review; a fresh five-scope round at the actual
final HEAD is recorded in the amendment below.

### The measured structural plateaus (evidence)

Every figure below is elapsed time unless marked as a pprof CPU
profile (those are literal CPU usage).

1. Reads (IPv4 1.525x / 1.582x, IPv6 1.326x / 1.357x).
   Cost structure at the final identity (p50 medians, 1M lookups):
   - Per-page: one authoritative ParseTreeHeader pass (18-22% of the
     page costs per profiles), plus the greatestFixed loop call (the
     loops are not inlineable: cost 126-152 vs the 80 budget; ~3M
     page visits per 1M lookups => the call overhead alone is ~1%).
   - Per-probe: slot U16 load + persistent-offset extent check + key
     load, each under safe-slice bounds checks (FixedSearch.U32 21%
     flat, U128 36% flat).
   Already measured in the bounded continuation: single-pass
   header parser retained (reads -7-9%); width-guard removal neutral
   and reverted; PGO ~1% rejected; U128 accessor now inlines at HEAD
   (verified by -m dump).
   Remaining design options:
   a. Inline the greatestFixed loops by splitting the probe loop into
      a skeleton with an already-inlineable probe call. Expected:
      <=1-2% on reads; low certainty (the loop body, not the call,
      dominates).
   b. Whole-page "checked window" access (one bounds proof per page,
      then pointer-free arithmetic). Requires unsafe in Go; the 4b
      A/B showed the extent check itself is near-free, so even with
      unsafe the win would be small. NOT authorized; recorded for
      completeness.
   c. Accept the read plateau: the residual is byte-for-byte Rust
      work plus Go's per-access safety checks; no safe structural
      change with a credible >=5% win is known.

2. Writes (nested-overwrite 2.355x; membership-import 1.529x).
   Cost structure (pprof at the final identity): InsertIfLocalGap4
   13.2% flat / 47.1% cumulative; predecessor-replacement chain 30.9%
   cumulative; GC is NOT dominant.
   Already measured: per-family generated gap/replace layer retained;
   result-transport slice kept (2.31x at that identity); per-op
   family-dispatch removal measured as a +10.52% regression and
   rejected; concrete-store A/B rejected by the same measurement
   class.
   Remaining design options:
   a. Remove the per-operation any() type-assertion seams at the
      writer boundary (range_specialize.go:28-59 converts ctx.family,
      r, reject through any() once per operation, not per record).
      Expected: <1% (per-op cost), low risk; cheap to do as part of
      any writer slice.
   b. Stack-resident edit state in the emitted layer: audit escape
      analysis of range_gap_v4/v6 (profiles show no dominant GC, so
      expected <=1-2%).
   c. Accept the write plateau with the machinery-bound gap
      documented (interface hops ~2-3%, struct copies, algorithm
      work; the per-record WORK is at Rust parity by the counters).

3. update-ipsets workflow (1.925x; Go 2084 ms vs Rust 1083 ms).
   The measured region (scenario_sdk.go sdkWorkflowExecute) composes
   prior-round seeding (out of measurement), direct-provider join,
   membership application, history windows, a 10% shift replacement
   (the nested-overwrite-shaped work), commit, and output.
   Design prerequisite (no code yet): phase-attributed measurement of
   the measured region - import vs replacement vs windows vs commit
   vs output - using the same bench harness that produced
   validation-phases-20260830.csv. The workflow ratio is bounded
   below by its slowest phase; the phase split decides whether the
   write-plateau work (2a/2b) transfers to the workflow or a
   distinct phase (e.g., membership import at 1.529x, join, window
   aggregation) needs its own design.

4. Validation walk (1.322x; Go 16.2 ms vs Rust 12.3 ms at 1M).
   Cost structure: worker fixed cost ~1.2-1.5 ms (Go worker startup
   is heavier than Rust's; both SDKs route validation through a
   version-matched worker, but the bench comparison is the public
   API on both sides); the walk is ~14.8 ms of the Go total and is
   the entire worker profile (Validate -> Probe -> InspectLayout ->
   walkRangeLeaf4 / inspectFixedExtents / markExtent). The walk is
   family-specialized and allocation-free per cell.
   Remaining design options:
   a. Parallel graph walk inside the worker (multi-goroutine page
      scan over disjunct page ranges, findings merged in page order
      to preserve sink ordering). This is the only option with a
      plausible >5% win; validation is a batch scan with
      page-independent per-page findings, so the parallelism is
      natural. Prerequisites: verify the mapping session-probe
      arming semantics under concurrency (session_probe.go) and
      define the deterministic merge contract. Cross-check whether
      the spec/worker control wire imposes single-threaded
      semantics (control.rs state machine is per-wire-mode, not
      per-goroutine). Risk: the finding-order contract of the
      ValidationSink; worker memory with GOMAXPROCS; measured A/B
      against the 1.3x line.
   b. Per-record decode fusion in the walk (DecodeRangeFieldsV4
      inlined into the cell iterator; the walk's inner loop is
      decode + order/neighbor checks). Expected <=2%.
   c. Reduce per-page state passing (the walk context escapes to the
      heap today; stack-resident state via explicit fields).
      Expected <=1%.

### Amendment 2026-08-31 (sol decision 3A - analysis and measurement only)

sol rejected the parallel-validation proposal as implementation-ready
(validation is an ordered graph traversal with shared ownership,
refcount, neighbor, finding, restart, and sink state, not a
page-independent physical scan), rejected the micro-items 1a/2a, and
required: stabilize the validation measurement, phase-split the
workflow, and replace the proposal with a feasibility design covering
nine listed contracts. No implementation is authorized.

Stabilized validation measurement (31 alternating Go/Rust pairs per
size, single samples, same host, same binaries as the final matrix;
evidence/validation-stabilization-20260831.csv, ~7 wall-minutes under
nice; record cost: 31 pairs x 3 sizes):

  size      Go median (range)        Rust median (range)      paired ratio median (95% CI)
  100k      6.52 ms (5.00-7.55)      4.05 ms (3.83-5.07)      1.555 (CI 1.406-1.607)
  1M        17.20 ms (15.71-27.02)   10.42 ms (10.18-14.47)   1.651 (CI 1.569-1.790)
  4M        55.15 ms (52.42-71.25)   32.30 ms (31.23-38.58)   1.717 (CI 1.690-1.792)

  RSS medians: 1.295x / 1.357x / 1.149x. Conclusions: (a) the
  final-matrix 1.322x median was a noise-LOW draw on five pairs; the
  stabilized estimate is 1.51-1.79x and every 95% CI excludes 1.300x,
  so the gate is missed by ~0.25-0.49x, not 0.022x - the "0.27 ms
  gap" framing is void (the real gap is ~6.8 ms at 1M vs the 0.27 ms
  implied by 1.322x -> 1.300x); (b) the benchmark cannot distinguish
  1.322x from 1.51x with five pairs, which is exactly why the matrix
  median misled; the 31-pair CI is the reliable evidence.

Workflow phase split (update-ipsets-workflow 1M, 5 samples, bench-only
phase recorder added to scenario_sdk.go under IPRANGE_WORKFLOW_PHASES;
evidence/workflow-phase-split-20260831.csv + raw log; measured wall
p50 2125 ms):

  create-current (one-inode feed build)  698 ms  33.9%
  publish (algebra output feed build)    314 ms  15.3%
  history (windows projection)           215 ms  10.5%
  base-feed                              191 ms   9.3%
  last-seen                              181 ms   8.8%
  first-seen                             173 ms   8.4%
  join-direct                            130 ms   6.3%
  join-membership                        121 ms   5.9%
  aggregate                               32 ms   1.6%
  tail (opens/checks/closes)             ~69 ms   3.4%

  The 1.925x workflow residual is dominated by the two one-inode
  feed builds (49% combined), not by the gap/replace machinery
  (history is 10.5%) and not by the joins (12%). Any workflow-targeted
  design must therefore attack the feed-build phases first; the
  membership-import and nested-overwrite scenario ratios bound those
  phases (1.529x and 2.355x respectively), so the workflow ratio is
  consistent with its phase composition.

Parallel-validation FEASIBILITY design (replacing the page-independent
scan proposal; assessment only - no implementation):

  Model: the sequential validator is an ordered DFS over the tree
  graph (selected-root order and first-encounter page/key order,
  binary-format-v4.md section 19; validation/validation.go:227
  serial root order; range.go cross-leaf neighbor state). The only
  parallelism that preserves the observable order is SUBTREE
  PARTITIONING WITH ORDERED REDUCTION: each goroutine validates a
  disjoint subtree and produces a summary; a cheap sequential reduce
  consumes the summaries in DFS carry-chain order. All nine
  contracts sol listed are covered as follows:

  1. DFS encounter-order preservation: carry-in (the previous
     subtree's last record, order state, value-kind counts) is
     threaded through the reduce in DFS order; findings are emitted
     only from the reduce, in encounter order, so the sink sees the
     exact sequential order.
  2. Subtree summaries and deterministic reduction: each subtree
     summary is {first key, last key, range count, order-valid bit,
     bounded findings vector, value-kind counts, refcount deltas};
     the summary function is pure in (subtree pages, carry-in), so
     the reduce is deterministic and reproducible.
  3. Global page claims, aliases, refcounts, cross-leaf neighbors:
     page claims use a concurrent visited set keyed by page number
     (the sequential walk visits each page once; aliasing across
     roots is detected identically); refcount and neighbor checks
     that cross subtree boundaries run only in the reduce, where the
     boundary records of adjacent subtrees are available.
  4. Bounded result buffering charged to MaxHeapBytes: each worker
     goroutine owns a fixed-size findings buffer (constant, e.g. 256
     entries); the total is charged to the validation heap budget;
     healthy walks emit no findings, so the common path allocates
     only the summary structs.
  5. Synchronous sink Stop/Error behavior: the reduce delivers
     findings to the sink synchronously in order and honors Stop;
     on Stop or Error, all worker goroutines are cancelled and join,
     then the reduce returns - identical observable behavior to the
     sequential validator.
  6. Cancellation and fault-restart prefix reproduction: cancellation
     is per-page checked in every goroutine; a restart reproduces the
     same findings prefix because summaries are pure functions of
     (pages, carry-in) and the reduce is deterministic. The design
     must explicitly accept that speculative subtrees beyond the
     first finding are validated but their findings are discarded -
     the observable prefix is preserved, the internal work is a
     superset (documented, bounded by the pool size).
  7. Fixed/bounded concurrency: a fixed worker pool (default
     GOMAXPROCS, capped by an explicit maximum, env-tunable), never
     uncontrolled spawning; one goroutine per subtree.
  8. Cross-platform fault containment: the whole scan runs inside
     one outer source-mapping probe (validation.go:261); the
     SIGBUS handler is process-wide (sigaction) with a naked asm
     claim of the armed mapping region; a fault in any worker
     goroutine is therefore indistinguishable from a sequential
     fault (owned fault record, worker exit 197) - the same
     fail-stop semantics. This must be PROVEN by the cross-platform
     fault-injection tests on the 8-target matrix (linux/darwin/
     freebsd/windows x amd64/arm64), including faults raised from
     non-coordinator goroutines; the one-armed-region rule of the
     spec is preserved because all goroutines read the same
     source mapping inside the same outer probe.
  9. Expected break-even size and coordination overhead: the
     sequential walk is ~96% of the Go validation time at every
     size; the stabilize measurement shows the gate needs only a
     1.3-1.4x walk speedup at all three sizes (1M: 15.9 ms walk to
     ~12.2 ms; 4M: 52 ms to ~38 ms). Coordination cost is per-
     subtree (channel handoff + reduce, ~microseconds); the carry
     chain reduce is per-subtree constant work. Break-even is below
     100k records. Expected: a memory-bound walk over 4-8 cores
     plausibly reaches 1.4-2x, which would move validation from
     1.65x to ~1.0-1.2x (estimate, not evidence). Even in the best
     case, seven elapsed-time scenarios remain outside the gate, so
     this design CANNOT close SOW-0027's performance acceptance; it
     only removes one of eight failures.

  Verification plan (if the user ever authorizes implementation):
  unit-scale A/B at 100k/1M/4M with the 31-pair methodology, the
  deterministic-reduction differential test (parallel vs sequential
  summaries byte-identical over the conformance corpus and the
  mutation set), the cross-platform fault-injection matrix, and
  MaxHeapBytes accounting. The micro-items 1a and 2a (greatestFixed
  loop inlining, any() seam removal) are explicitly NOT authorized
  per sol direction 3A: their estimated gains cannot close their
  gaps.

### Recommendation (amended)

Keep the <=1.3x gate. Authorize NO implementation now. The next step,
if the user wants to keep the gate, is the parallel-validation
FEASIBILITY design above (this record) - reviewed, not implemented -
plus optionally the workflow feed-build phase analysis (create-current
698 ms / publish 314 ms) as a design-only investigation. The honest
expectation: even a fully successful parallel validation leaves seven
scenarios failing, so the user should decide now whether the gate is
waived for those seven (option 2 in the user decision), or whether the
analysis continues (option 3) with the understanding that no known
safe-Go design closes reads or writes.

## Requirements

### Purpose

Make the pure-Go v4 SDK a true public-behavior peer of the Rust v4 SDK under the
exact current unsigned v4 contract. The result must support the same logical
operations, live coordination, failure outcomes, recovery containment,
platform boundary, bounded-resource behavior, and cross-language evidence
needed by `update-ipsets` and downstream SDK consumers.

This work must correct implementation and evidence gaps rather than weaken the
normative specification to match the incomplete Go surface.

### User Request

Create a new SOW containing the verified static gap analysis of feature parity
between the Rust and Go v4 SDK implementations.

### Assistant Understanding

Facts:

- Rust was implemented first and is the reference implementation for v4
  semantics; the normative specifications and shared conformance contract are
  the cross-language authorities.
- The design requires completed Rust and Go SDKs to be public-behavior peers.
  Physical page placement, tree shape, internal IDs, and compressed bytes may
  differ when observable semantics agree.
- Both readers open and semantically check all eleven committed immutable
  conformance files. This is real file-format and reader-interoperability
  evidence for the cases represented by the corpus.
- Go's sidecar-bound `LiveWriter` exposes only information, one advanced direct
  transaction, and close. Most Go mutation operations are attached to the
  separate sidecar-free `Writer`, which blocks readers for its lifetime.
- The public sidecar-free mutable Go writer contradicts the exact v4 contract,
  which requires every public writer to be live and explicitly forbids offline
  in-place mutation.
- Go has no public membership-import, one-inode immutable-feed construction,
  exact commit-resolution, or bounded reader-safe reclamation operation.
- Go validation, candidate inspection, and recovery use the required worker
  process only on Linux/amd64. Other targets execute faultable mapped-file work
  in the application process.
- Go deliberately refuses creator-only artifact creation on macOS because its
  Darwin ACL machine is unavailable. Rust supports the specified macOS
  creator-only policy.
- The existing subprocess tests spawn another test process of the same language
  and inspect immutable files. They do not run cooperating Go and Rust
  processes on the same live database.
- SOW-0025 claimed the missing behavior and conformance evidence as completed,
  but its own delivery record states that the Go `LiveWriter` shipped only the
  direct transaction surface. The reviewed gap is an incorrect acceptance
  record, not evidence that later code removed formerly implemented behavior.
- SOW-0017 currently treats Go/Rust parity and platform completion as proven
  prerequisites for snapshot-signing design. This static review invalidates
  that prerequisite.

Inferences:

- The long-term-best correction is to converge Go on the Rust public contract,
  not to preserve two Go writer modes or narrow the specification after the
  fact.
- The existing Go mapped reader, writer core, live coordination, workflow,
  publication, validation, and recovery owners contain substantial reusable
  implementation. The correction should rewire and complete those owners
  rather than introduce another storage path.
- Static source presence is insufficient evidence of semantic parity. The
  repaired surface needs independent public-API tests, symmetric producer
  fixtures, true mixed-language live tests, native platform evidence, and
  matched release benchmarks.
- Snapshot signing should not proceed until this SOW restores the prerequisite
  unsigned SDK contract and evidence.

Unknowns:

- Whether macOS creator-only ACL removal and proof can be implemented under the
  existing pure-Go/no-cgo constraint without a new native boundary. Official
  Darwin interfaces and viable pure-Go mechanisms must be investigated before
  that platform chunk is designed.
- The smallest portable worker implementation that can preserve the exact
  POSIX `SIGBUS` and Windows in-page-exception containment rules on Darwin,
  FreeBSD, Windows, non-amd64 Linux, and supported architectures without
  violating the pure-Go boundary.
- The final Go/Rust performance delta after the public writer is consolidated
  and per-result adapter allocation is removed. Static inspection cannot
  establish the measured result.

### Acceptance Criteria

- The public Go writer contract exposes only the normative live writer. A
  sidecar-free immutable file cannot be opened for mutation, and no public
  `Create`/`OpenWriter` offline mutation path remains.
- Go `LiveWriter` exposes Rust-equivalent advanced direct, membership, and
  structured transactions; feed create/replace/rename/delete; exact direct
  replacement; first-seen and last-seen refresh; history projection; metadata
  mutation/read-your-writes; abort, close, commit, and bounded reclamation.
- Every advanced or high-level Go begin operation accepts and retains its own
  cancellation token through ingestion, normalization, metadata, commit, and
  cleanup checkpoints.
- Go implements name-based membership import from explicitly pinned immutable
  and live readers with the Rust compatibility, source-borrow, report,
  cancellation, and no-change semantics.
- Go implements one-inode immutable IPv4 and IPv6 feed construction with the
  Rust resource budget, normalization, metadata, publication, report, and
  cleanup contract.
- Go implements exact commit-outcome resolution for immutable and live modes,
  including same-file proof, superseded/unknown classification, and truthful
  cleanup evidence.
- Go implements bounded, transaction-grouped, oldest-reader-safe reclamation
  through the live writer and proves lowest-free-page reuse while readers are
  pinned.
- Go clean-writer metadata mutation and transaction metadata access provide
  the same staged read-your-writes semantics as Rust.
- Direct provider joins accept both immutable and live direct readers. Public
  mapped source adapters provide bounded reader-to-writer chaining equivalent
  to the Rust named-feed and direct-range sources.
- All public Go failures that belong to the stable SDK taxonomy are
  programmatically accessible as the exported Go error type. Public APIs never
  leak an unnameable `internal` error type.
- Invalid public Go inputs return typed errors rather than panic. Enum-like
  certifications reject undefined values before path access or mutation.
- Validation, candidate inspection, recovery, and retained-cleanup retry use a
  version-matched worker on every supported OS/architecture. No supported
  target silently executes faultable scans in the application process.
- Go creates and proves creator-only live and immutable publication artifacts
  on Linux, macOS, Windows, and the specified FreeBSD immutable boundary, or
  implementation stops for an explicit user decision if the pure-Go macOS
  contract is proven infeasible.
- Go publicly exposes the required Windows housekeeping list/removal APIs and
  their exact identity-bound evidence and terminal outcomes.
- Both implementations independently produce the required symmetric direct,
  membership, structured, metadata, cardinality, snapshot, and invalid-case
  corpus coverage through public normative paths. Go fixtures are produced
  through `CreateLive`, `LiveWriter`, close, and live `SnapshotTo`, not through
  an offline mutable writer.
- Mixed Go/Rust subprocess tests cooperate on the same live database in both
  directions and cover registration/release, writer exclusion, reclamation,
  stale slots, sidecar replacement, transition/reservation states,
  publication inspection, and resolution.
- Commit-resolution and compact-snapshot cases required by the normative
  conformance contract are cross-proven, including same-transaction/different-
  nonce attempts.
- Go aggregation/join adapters meet their documented bounded/allocation
  behavior. Public steady-state performance claims are backed by allocation
  pins and profiles of the actual public adapters.
- Go ports the representative Rust `update-ipsets` release benchmark matrix.
  Matched release measurements record throughput, allocation, RSS, file size,
  file descriptors, necessary work, and semantic results for both languages.
  Material differences are explained and accepted by the user.
- Current SDK and conformance documentation accurately describes implemented
  behavior, worker deployment, platform support, and evidence. Superseded
  milestone reports no longer appear to be active work records.
- The full Go codebase passes the mmap-only/page-copy/file-I/O adversarial gates,
  all supported compiler graphs, tests, race/checkptr analysis, static checks,
  native authorized platform tests, and independent final review with no open
  P0, P1, or P2 finding.
- SOW-0017 remains blocked until this SOW completes and the restored unsigned
  SDK prerequisite is accepted by the user.

## Analysis

Sources checked:

- `.agents/sow/specs/design-iprange-engine.md`
- `.agents/sow/specs/binary-format-v4.md`
- `.agents/sow/done/SOW-0025-20260811-pure-go-exact-v4-port.md`
- `.agents/sow/done/SOW-0026-20260825-v4-platform-completion.md`
- `.agents/sow/current/SOW-0017-20260717-v4-snapshot-signing-phase2.md`
- `v4/rust/iprange-livedb/src/lib.rs` and its exported reader, writer,
  lifecycle, workflow, query, publication, validation, recovery, and snapshot
  modules
- All compiled production Go files under `v4/go/`, including public facades and
  the internal writer, live-coordination, publication, routing, security,
  validation, recovery, and worker owners
- `v4/conformance/README.md`, `v4/conformance/cases.json`, Go/Rust fixture
  generators, conformance tests, and mixed-subprocess tests

Current state:

- **Substantial parity:** exact immutable format identity; reading committed
  direct, membership, and structured fixtures; metadata and cardinality
  observations; broad live-reader surface; membership queries, aggregation,
  global algebra, snapshots, lifecycle transitions, publication, validation,
  and recovery facades on the platforms where their required machinery exists.
- **Partial parity:** direct live transaction; direct-provider joins; public
  error translation; cancellation; platform creation; worker deployment;
  producer-fixture coverage; resource/performance evidence.
- **Not parity:** live membership/structured/feed/timestamp/history mutation;
  membership import; immutable single-feed creation; commit resolution;
  reader-safe reclamation; live writer metadata read-your-writes; public Windows
  housekeeping; real mixed-language live cooperation.

Evidence:

- Normative all-writers-live rule and offline-mutation prohibition:
  `.agents/sow/specs/binary-format-v4.md:1272-1279`.
- Explicit constructor contract:
  `.agents/sow/specs/binary-format-v4.md:2413-2421`.
- Cross-language public-behavior requirement:
  `.agents/sow/specs/design-iprange-engine.md:57-73,418-438`.
- Go's sidecar-free creation and mutation API:
  `v4/go/writer_public.go:52-97`.
- Go's real live writer is direct-only:
  `v4/go/live_writer_public.go:42-100`.
- Go workflows attached to sidecar-free `Writer`:
  `v4/go/direct_workflow_public.go:60-80`,
  `v4/go/feed_workflow_public.go:62-73,520-575`,
  `v4/go/membership_transaction_public.go:83`,
  `v4/go/structured_transaction_public.go:62`, and
  `v4/go/history_projection_public.go:64`.
- Rust `LiveWriter` equivalents:
  `v4/rust/iprange-livedb/src/live_writer/direct.rs:29`,
  `membership.rs:91`, `structured.rs:41`, `direct_workflow.rs:66-96`,
  `feed_workflow.rs:58-71`, `feed_lifecycle.rs:11-36`, and
  `history_projection.rs:57`.
- Missing-in-Go Rust operations:
  `v4/rust/iprange-livedb/src/live_writer/membership_import.rs:60-93`,
  `live_writer/reclaim.rs:24-37`, `commit_resolution.rs:86-99`, and
  `immutable_feed.rs:67-119`.
- Rust clean-writer metadata/read-your-writes:
  `v4/rust/iprange-livedb/src/live_writer.rs:143-189,211-229`.
- Go live direct operation omits cancellation:
  `v4/go/live_writer_public.go:68-79,270-282` and
  `v4/go/internal/live/live_writer.go:196-201`.
- Go direct joins accept only immutable sources:
  `v4/go/join_public.go:63-89`; Rust accepts immutable and live sources at
  `v4/rust/iprange-livedb/src/membership_query/join.rs:20-25,108-128`.
- Go public writer paths leak internal typed errors:
  `v4/go/writer_public_test.go:17-24`; the correct conversion boundary exists at
  `v4/go/reader_public.go:769-787`.
- Go public nil-input panic and unchecked certification paths:
  `v4/go/join_public.go:71-78,114-122`,
  `v4/go/membership_algebra_public.go:79-86`, and
  `v4/go/recovery_public.go:27-37,205-211`.
- Go aggregation/join adapter allocation contradicts its public comments:
  `v4/go/aggregation_public.go:1-10,80-108` and
  `v4/go/join_public.go:1-8,89-101,124-151`.
- Worker required for faultable operations:
  `.agents/sow/specs/binary-format-v4.md:3155-3206`.
- Linux/amd64 Go worker routing and other-platform in-process routing:
  `v4/go/internal/routing/routing_linux_amd64.go:1-40`,
  `v4/go/internal/routing/routing_other.go:1-50`, and
  `v4/go/cmd/iprange-v4-worker/main_other.go:1-15`.
- Go Darwin creator-only refusal:
  `v4/go/internal/security/acl_darwin.go:9-28` and
  `v4/go/internal/live/public.go:23-38`.
- Windows housekeeping is private in Go but public in Rust:
  `v4/go/internal/publication/maintenance_gc_windows.go:10-29`,
  `v4/go/publication_public.go:179-225`, and
  `v4/rust/iprange-livedb/src/publication.rs:36-47`.
- Normative mixed-language live conformance:
  `.agents/sow/specs/binary-format-v4.md:4145-4170`.
- Existing same-language immutable subprocess tests:
  `v4/go/subprocess_cross_open_test.go:27-171` and
  `v4/rust/iprange-livedb/tests/mixed_subprocess.rs:23-140`.
- Rust fixtures use live creation/writing/snapshot; Go fixtures use the
  sidecar-free writer:
  `v4/rust/iprange-livedb/tests/conformance_support/generate.rs:36-73` and
  `v4/go/conformance_generate_test.go:48-64,107-125,147-194,226-240,310-324`.
- Corpus asymmetry and stale snapshot statement:
  `v4/conformance/README.md:10-43`; Go `SnapshotTo` exists at
  `v4/go/snapshot_public.go:101`.
- Go has reader microbenchmarks but no port of the public Rust
  `update-ipsets` benchmark matrix:
  `v4/go/internal/reader/bench_test.go:25-141` and
  `v4/rust/iprange-livedb/benches/update_ipsets.rs:1-43`.
- SOW-0025 claimed complete parity while recording a direct-only live facade:
  `.agents/sow/done/SOW-0025-20260811-pure-go-exact-v4-port.md:2785-2817,7478-7484`.
- SOW-0026 records non-Linux worker refusal as accepted behavior:
  `.agents/sow/done/SOW-0026-20260825-v4-platform-completion.md:122-133`.
- SOW-0017 relies on the disproven parity prerequisite:
  `.agents/sow/current/SOW-0017-20260717-v4-snapshot-signing-phase2.md:7-25,114-118`.

Risks:

- Keeping the sidecar-free writer public would establish two incompatible Go
  mutation contracts and allow callers to build on behavior forbidden by the
  v4 specification.
- Consolidating the public writer affects every mutation facade, transaction
  handle, result type, test fixture, benchmark, and downstream caller. Partial
  migration could silently keep an off-contract path alive.
- Incorrect live workflow integration could publish partial drafts, reclaim
  pages still visible to readers, lose outcome-unknown evidence, or deadlock
  reader/writer coordination.
- In-process damaged-file scans can terminate or corrupt the consuming process
  on mapping faults. Worker portability is a correctness and containment
  requirement, not optional hardening.
- Incorrect ACL handling could publish artifacts with inherited access or make
  the macOS SDK unusable. A weakened creator-only policy is not acceptable.
- Removing exported Go APIs before all internal tests and callers migrate can
  cause build failures. The format is unreleased, so compatibility must not be
  preserved at the cost of the exact contract, but the migration must still be
  complete and atomic within this repository.
- Incomplete mixed-language testing can leave lock, sidecar, retirement, and
  resolver incompatibilities undiscovered even while immutable fixtures
  cross-open successfully.
- Benchmark work is potentially expensive. Every run expected to exceed two
  wall-minutes or ten core-minutes must be recorded with cost before execution
  and run under `nice`.

## Pre-Implementation Gate

Status: resolved (2026-08-26, user decision A on Open Decision 1)

Problem / root-cause model:

- SOW-0025 implemented a large Go semantic surface over the mapped writer core,
  then added a separate sidecar-bound live writer containing only direct
  transactions. The broader workflows were never migrated onto the live writer.
- The completion gate treated sidecar-free exclusive mutation as an accepted
  implementation mode even though the normative contract explicitly forbids
  it. The conformance fixtures and subprocess tests then exercised that
  incomplete architecture rather than the required live cross-language path.
- Several Rust exports, platform requirements, and conformance cases were
  recorded as completed without compiled Go public entry points or equivalent
  evidence.
- Later platform work preserved the accepted gaps, including non-Linux worker
  refusal, while SOW-0017 was unblocked on the resulting completion claims.

Evidence reviewed:

- Exact current design and binary-format specifications.
- Complete public Rust export inventory and the implementation modules owning
  every material operation.
- Complete compiled Go public facade inventory and targeted internal owners for
  writer/live coordination, security, routing, workers, publication,
  validation, and recovery.
- Shared corpus manifest, producer paths, immutable conformance tests, and
  subprocess tests in both languages.
- SOW-0025, SOW-0026, and the active SOW-0017 prerequisite record.
- Repository revision
  `07c0147bf18b4d1e976f460fc2624e90e91be8cc`; the working tree was clean before
  this SOW file was created.

Affected contracts and surfaces:

- Go public package API, public types, typed errors, cancellation, writer and
  transaction handles, lifecycle, recovery, validation, publication, and
  platform behavior.
- Internal Go writer core, live sidecar owner, mapped sources, immutable output,
  worker client/server, security, publication maintenance, and platform seams.
- Rust and Go fixture generators and conformance tests.
- Shared immutable corpus files and manifest.
- Mixed-process coordination harnesses.
- Go/Rust benchmarks, work counters, profiles, and release evidence.
- SDK README/package documentation, worker deployment documentation, platform
  support statements, specifications if investigation exposes a real contract
  ambiguity, runtime project skills, AGENTS.md gates, SOW-0017 prerequisites,
  and SOW lifecycle records.
- `update-ipsets` suitability and any downstream code compiled against the
  unreleased Go API.

Existing patterns to reuse:

- Rust `LiveWriter` and its direct, membership, structured, workflow, metadata,
  reclaim, and result modules are the semantic reference.
- Go `internal/writer.Core` and `internal/live.LiveWriter` are the existing
  physical and coordination owners; workflows must compose them rather than
  copy persistent logic.
- `HistoryProjectionSource` already demonstrates a Go tagged source that can
  bind immutable or live readers.
- `publicError` is the established root-package conversion boundary for typed
  errors.
- Rust public source enums demonstrate explicit immutable/live modes for
  membership import, joins, snapshots, validation, and recovery.
- Existing Go lifecycle/publication result translations demonstrate how
  platform-neutral public evidence should wrap internal owners.
- Rust fixture generation through live writer plus `snapshot_to` is the
  normative producer pattern.
- The project full-codebase mmap-only/page-copy/file-I/O reviewers and the
  `project-final-review` skill are the acceptance-review pattern.

Risk and blast radius:

- Behavioral/API compatibility: high inside the unreleased Go SDK because the
  off-contract public writer must disappear or become private.
- Data loss/corruption: high if commit, abort, reclaim, resolver, or workflow
  integration is incorrect.
- Concurrency: high for reader slots, gates, writer lease, close, and page reuse.
- Platform: high for signal/exception containment, ACLs, file identity, locks,
  durability, and Windows housekeeping.
- Security: high for creator-only access and untrusted damaged-file handling.
- Performance: high on writer, query adapter, worker, and publisher hot paths.
- Released compatibility: none for v4; the format and SDK remain unreleased.
- Existing legacy C CLI: expected to be unaffected; validation must confirm no
  build or packaging coupling changed.

Sensitive data handling plan:

- Use only synthetic database paths, ranges, feed names, metadata, identities,
  fault cases, and benchmark inputs in durable artifacts.
- Do not record operational FireHOL infrastructure, credentials, access tokens,
  production feed names, literal operational ranges, customer/community data,
  personal data, private endpoints, or identifying IP addresses.
- Real-use `update-ipsets` replays, if explicitly authorized, record only
  sanitized aggregate counts, sizes, timings, and profiles.

Implementation plan:

1. **Activate and freeze the parity ledger.** Resolve SOW sequencing, mark
   SOW-0017 blocked on SOW-0027, inventory every Rust public export and normative
   operation against a compiled Go symbol, owner, test, platform, and benchmark,
   and record the exact API removals/additions before code changes.
2. **Consolidate one live writer.** Extend `internal/live.LiveWriter` over the
   existing `internal/writer.Core`; bind all advanced transactions and
   high-level workflows to the live writer; carry per-operation cancellation;
   migrate result/cleanup translations; then remove or privatize public
   `Create`, `OpenWriter`, `Writer`, and their duplicate transaction surface.
3. **Complete missing semantics.** Port membership import, one-inode immutable
   feed construction, commit resolution, bounded reclaim, clean-writer metadata
   mutation/read-your-writes, immutable/live mapped source adapters, and live
   direct-provider joins through the existing owners.
4. **Repair the public Go contract.** Convert every stable SDK failure to the
   exported error type, validate nil and enum-like inputs, remove per-callback
   result/name allocation where the documented contract requires reuse, and add
   external-package compilation/behavior tests that cannot import Go `internal`
   packages.
5. **Complete platform containment and security.** Implement and package the
   version-matched worker for every supported platform/architecture, port exact
   POSIX/Windows fault ownership, implement the specified macOS creator-only
   policy within the approved pure-Go boundary, and expose Windows housekeeping.
   Stop for a user decision before introducing cgo, assembly, another native
   boundary, weakening creator-only security, or dropping a specified platform.
6. **Replace conformance evidence.** Regenerate symmetric Go fixtures through
   live creation/writer/snapshot, add the missing producer and invalid cases,
   and build true Go-to-Rust and Rust-to-Go live subprocess harnesses covering
   the complete section-21 contract.
7. **Prove bounded performance.** Port the representative Rust benchmark matrix,
   add public-adapter allocation pins and necessary-work counters, profile every
   material delta, remove actionable waste, and record matched release evidence.
8. **Close the full artifact and review gate.** Update current SDK docs,
   conformance docs, affected specs/skills/instructions, retire misleading
   pending milestone reports, run the complete Go and Rust gates, obtain native
   platform evidence with explicit authorization, run the required four
   full-codebase mmap/I/O reviewers plus independent semantic/platform/API
   reviewers, resolve all P0-P2 findings, and complete SOW lifecycle records.

Validation plan:

- Generate a machine-checkable Rust-export/normative-operation to Go-symbol
  parity manifest and fail CI for missing or extra prohibited public writer
  operations.
- Run all Go tests under `nice`, with and without `v4work`, plus `-race`,
  `checkptr=2`, vet, formatting, supported cross-builds, mmap storage/runtime
  gates, worker protocol/crash gates, and external-package API tests.
- Run Rust architecture, mmap storage/runtime, source-graph, complete workspace
  tests, Clippy, formatting, documentation, MSRV, and affected sanitizer/Miri
  gates under the project-v4-rust workflow.
- Regenerate all Go and Rust conformance artifacts only through explicit public
  fixture-generation commands; verify both readers open and semantically compare
  every file and reject missing/extra/stale/malformed entries.
- Run true mixed-language live processes in both directions for the complete
  normative coordination/resolution matrix.
- Add deterministic scalar/property models for every newly live-bound Go
  transaction/workflow and compare results with Rust authority vectors.
- Add crash/fault matrices for commit outcome, reclaim, transitions,
  reservations, publication, validation, recovery, worker identity/protocol,
  SIGBUS, and Windows in-page errors.
- Add same-failure searches for sidecar-free public mutation, leaked internal
  errors, unvalidated enum conversions, nil dereferences, in-process faultable
  routing, unexported maintenance operations, and per-result adapter allocation.
- Run authorized native tests on Windows, macOS, and FreeBSD only after the user
  explicitly approves access to those systems. Cross-compilation is recorded as
  compilation evidence only.
- Before each performance command expected to exceed two wall-minutes or ten
  core-minutes, record the exact command, purpose, expected cost, and stop
  criteria here; run every build/test/benchmark under `nice`.
- At each implementation milestone, run four complete Go-codebase adversarial
  reviews: two for complete-page copies into/out of mappings and two for
  persistent content I/O outside mappings. Run independent API/semantic,
  platform/security, conformance, and performance reviews at the final gate.
- Final acceptance uses `.agents/skills/project-final-review/SKILL.md` and
  requires no open P0, P1, or P2 finding.

Artifact impact plan:

- AGENTS.md: update only if this work establishes a new durable parity,
  conformance, platform, or benchmark guardrail not already recorded.
- Runtime project skills: update `project-v4-rust` if the symmetric Go/Rust
  conformance or worker workflow becomes reusable project procedure; add no
  generic skill solely for framework completeness.
- Specs: preserve the current contract when implementation is brought into
  conformance; update specs only for a user-approved contract decision or a
  real ambiguity discovered during implementation.
- End-user/operator docs: update Rust README, Go package documentation, worker
  installation, platform support, public APIs, and conformance instructions to
  describe the delivered present state.
- End-user/operator skills: none currently exist; record whether any are added
  before close.
- SOW lifecycle: keep SOW-0027 open in `pending/` until SOW-0017 is paused,
  completed, or superseded by user decision. Preserve SOW-0025 and SOW-0026 as
  historical evidence of incorrect acceptance rather than rewriting their
  narratives. When activated, SOW-0017 must be marked blocked/paused on this
  prerequisite. Remove or relocate the unnumbered SOW-0025 milestone reports
  from `pending/` only with explicit user approval because deletion is outside
  this SOW's automatic authority.

Open-source reference evidence:

- No external open-source repository was used for this static parity finding.
  The exact local normative specifications and the two implementations are the
  authoritative evidence. Platform implementation planning must review official
  OS/runtime documentation and relevant open-source language-runtime patterns
  before code changes.

Open decisions:

1. **SOW sequencing — RESOLVED (2026-08-26, user decision A).**
   Pause SOW-0017 and activate SOW-0027. Signing decisions and implementation
   wait; no signed-format work builds on a false SDK premise. Options B and C
   recorded but not selected.

2. **Worker port scope — RESOLVED (2026-08-27, user decision 1-Option 1).**
   Full worker port to every supported OS/architecture: linux amd64+arm64,
   darwin amd64+arm64, freebsd amd64+arm64, windows amd64+arm64. New
   assembly is approved for the POSIX SIGBUS containment machines (the
   SOW stop condition is satisfied by this decision); the Windows arm uses
   the pure-Go vectored exception handler path (kernel32
   AddVectoredExceptionHandler through LazyDLL, no assembly).

3. **Native validation hosts — RESOLVED (2026-08-27, user decision 2).**
   Authorized: `costa-win11` (Windows housekeeping public surface + GC
   machine + worker in-page-exception containment), `plakam4mini` (darwin
   creator-only arm + live suites + worker SIGBUS containment),
   `freebsd` (immutable/offline reads + publication + worker SIGBUS
   containment). Native rounds run only over pushed commits.

Resolved decisions:

- The user requested a new SOW rather than modifying SOW-0025. SOW-0027 is the
  corrective tracker because the evidence shows an incorrect acceptance at the
  time of SOW-0025 completion, not a later removal of previously delivered
  behavior.
- The recommendation class is long-term-best: match the Rust authority and
  current specification; do not retain off-contract public compatibility for
  an unreleased SDK.
- The Go implementation remains pure Go with no cgo. If platform investigation
  proves the exact contract infeasible within that boundary, implementation
  stops and presents evidence-backed options to the user.

## Implications And Decisions

1. **New corrective tracker** — resolved by user request on 2026-08-26.
   SOW-0027 records the complete gap and preserves SOW-0025/SOW-0026 as
   historical acceptance evidence.
2. **Implementation order** — pending user selection from Open Decision 1.
   No production code may change under SOW-0027 until this is resolved and
   recorded.
3. **Contract direction** — resolved as long-term-best. Go converges on Rust and
   the normative specifications; specifications are not weakened to legitimize
   incomplete implementation.
4. **Pure-Go boundary** — resolved from SOW-0025. No cgo or new native boundary
   is introduced without a new explicit user decision supported by feasibility
   evidence.

## Plan

1. Resolve SOW sequencing and activate SOW-0027 without running two SOWs.
2. Freeze a complete public API/semantic/platform/evidence parity ledger.
3. Consolidate Go mutation behind the one sidecar-bound live writer.
4. Port every missing operation and repair public cancellation/error/input
   contracts.
5. Complete portable worker isolation, macOS creator-only security, and Windows
   housekeeping.
6. Replace asymmetric/off-contract fixtures and same-language subprocess smoke
   tests with the complete symmetric conformance proof.
7. Prove resource and performance parity on public `update-ipsets`-shaped
   workloads.
8. Update all affected artifacts and pass independent full-scope acceptance
   review before closing.

## Execution Log

### 2026-08-26

### Status (2026-08-26) - milestone 1 (parity ledger freeze) delivered

- Delivered in this commit: the parity ledger and its enforcing gate.
- `v4/go/parity_manifest.tsv` (81 curated rows, tab-separated:
  class, Rust reference, Go symbol, status, note) records every material
  Rust public operation and its Go symbol or missing status, organized by
  class: writer-offcontract (19 rows, remove-planned or missing), live-writer
  (21 rows), live-direct (7), reader (8), live-reader (4), cancellation (4),
  join (1), publication/scratch maintenance (7), snapshot (1), validation (1),
  recovery (4), worker (1), platform (1), cards/types (1). Statuses:
  present = the Go symbol exists and is the correct owner; remove-planned =
  the symbol exists but is off-contract and must disappear when the live
  writer consolidation lands; missing = the operation has no public Go
  equivalent (or only a wrong-owner off-contract one, recorded in the note).
- `v4/go/parity_gate_test.go`: the machine-checkable tripwire (SOW-0027
  acceptance: "fail CI for missing or extra prohibited public writer
  operations"). It parses the compiled root-package surface (go/parser, no
  subprocess) and fails when: a present/remove-planned symbol is absent; a
  missing row's symbol appears; or any public `Writer.*` method exists that
  is not recorded in the ledger (the off-contract writer surface is closed).
- `v4/go/parity_rust_public.tsv`: the regenerable raw Rust public export
  inventory (507 symbols) that the curated manifest maps; the uncurated
  long-tail rows are explicitly out of the gate's scope until later
  milestones record them.
- Ledger facts confirmed by the gate at this revision: 54 present/
  remove-planned, 32 missing. The gate additionally exposed two acceptance
  records that did not match reality, now recorded truthfully: the Go public
  surface has no `RemoveCheckpointedScratch` export (the internal recovery
  machine exists; SOW-0026's D record claimed the public export), and
  `Writer.DeleteFeed`/`Writer.RenameFeed` exist on the off-contract writer
  (live-owner equivalents absent).
- Battery: root plain and `-tags v4work` suites green (the gate runs in both),
  `go vet ./...` clean, `gofmt -l` empty for the new files, under `nice`.
- Next milestone 1 item: extend the ledger to the full 507-row Rust export
  inventory with name-variant resolution (the long tail), then milestone 2
  starts the live-writer consolidation with the removal list fixed by this
  ledger.


- User decision: activate SOW-0027 (option A), pause SOW-0017 as blocked on the
  unsigned-SDK prerequisite; recorded in Open Decision 1 and in SOW-0017.
- Lead re-verification of the static audit: every material claim confirmed
  against the code and the normative specs; evidence list recorded in the
  status sub-state above.
- Performed a read-only static parity audit at
  `07c0147bf18b4d1e976f460fc2624e90e91be8cc`.
- Reviewed the normative specifications, Rust public authority, all Go public
  production facades, relevant Go internal owners, shared corpus, producer
  paths, subprocess tests, completed SOW-0025/SOW-0026, and active SOW-0017.
- Created this pending corrective SOW at the user's request.
- No production code, specification, SDK documentation, skill, or active SOW
  was changed. No test or benchmark was run because the request was to record
  the completed static analysis.
- Ran `nice .agents/sow/audit.sh`. It validates SOW-0027 as `open` under
  `pending/`, the pre-implementation gate, regression-section placement,
  sensitive-data guardrail, and project-skill structure. The overall audit
  remains partial because the pre-existing SOW-0025 document has no canonical
  top-level status block and is reported as `Status: ###` in `done/`; this SOW
  records that historical artifact but does not rewrite it.
- Checked the new file for template placeholders, personal names, and trailing
  whitespace with `rg`; none was found.

## Validation

Acceptance criteria evidence (mapped to the criteria under Requirements):

- Live-only writer surface: the off-contract public Writer type and its
  mutation methods are gone (milestone 2c, writer-offcontract rows
  removed with evidence); LiveWriter exposes the full advanced surface
  (milestone 2, parity gate closes the LiveWriter method set).
- Cancellation: every Begin* and the direct/feed/history workflows
  capture the token through ingestion, metadata, commit, and cleanup
  checkpoints (milestone 2; cancellation tests in the root suite).
- Membership import: LiveWriter.BeginMembershipImport with pinned-source
  compatibility, report, and no-change semantics (milestone 2; import
  tests + parity row present).
- One-inode immutable IPv4/IPv6 feed construction: public
  CreateImmutableFeedV4/V6, budget/report contract, FREE_MAGIC workspace
  free-list parity with Rust, one-inode final-offset page construction,
  publication through the reservation machine (slice F; parity rows
  present; conformance fixture go/immutable-feed-ipv4.iprdb cross-opens
  in the Rust corpus verify).
- Exact commit-outcome resolution: CommitResult carries
  AttemptedDatabaseID/DirectoryIdentity/MainIdentity/AttemptedTxnID/
  CommitNonce/Status/Cause/Cleanup field-for-field with Rust
  live_writer/result.rs; ResolveCommit accepts any commit value with the
  same validate_attempt order (slice B; root tests).
- Bounded reclamation: LiveWriter.Reclaim with auto-publish and
  oldest-reader safety (present row; reclaim tests).
- Read-your-writes metadata: clean-writer metadata mutation and
  transaction metadata access staged like Rust (milestone 2 tests).
- Direct provider joins, mapped source adapters, and public error
  taxonomy: parity rows present; publicError boundary never leaks
  internal types (gate tripwire + tests).
- Typed errors instead of panics: nil scopes/partners/sources and
  undefined OfflineQuiescenceCertification refused with
  ErrorInvalidArgument before any dereference or path access (slice A +
  final-round nil-receiver guards + tests).
- Worker containment: routed surface covers the full worker cross-build
  matrix (linux/darwin/freebsd/windows x amd64/arm64), no silent
  in-process fallback; native green rounds on costa-win11
  (windows/amd64), plakam4mini (darwin/arm64), and freebsd (amd64)
  (slice E; routed equivalence tests run through the real worker).
- Windows housekeeping: public list/removal APIs with identity-bound
  evidence (implemented; native windows round green).
- Symmetric conformance: 14-fixture corpus (6 Rust + 8 Go producers),
  every fixture cross-opens in both implementations (Go conformance
  suite + Rust conformance test).

Tests or equivalent validation (all under `nice`, at the close identity):

- Root plain and `-tags v4work` suites: green (go test ./... and
  -tags v4work, both including the parity gate tripwire).
- `go vet ./...` clean; `gofmt -l` empty for the whole v4/go tree.
- Race detector + checkptr on the root package and the touched internal
  packages (writer, reader, recovery, validation, routing): green.
- Rust suites: cargo test --test conformance green (the 14-fixture
  corpus verify, including the Go-produced immutable-feed fixture);
  the Rust correctness suites passed at the frozen reference identity.
- Cross-compilation: the worker matrix cross-builds (linux/darwin/
  freebsd/windows x amd64/arm64); native rounds executed on the three
  authorized hosts (see Real-use evidence).
- .agents/sow/audit.sh: SOW-0027 status/directory and required gates
  pass (run at the close identity).
- Final-round delta (2026-08-30): go test ./... and go test -tags
  v4work ./... green under the 32 GB address-space cap, go vet ./...
  both modes, gofmt clean, genfamilies regeneration idempotent
  (range_gap_v4/v6 regenerate byte-identically), rustfmt clean on the
  mixed battery; the mixed battery runs green in both directions with
  IPRANGE_V4_MIXED_LIVE=1 (Go parent: 9 modes incl. reservation,
  transition, transition-ready; Rust parent: 9 tests incl. the three
  new cross-language residue cases), linux/amd64.

Real-use evidence:

- Final-identity performance matrix (acceptance evidence for the
  measured-performance decision, 2026-08-30): eight scenarios - membership-
  import, nested-overwrite, update-ipsets-workflow, IPv4 live and
  immutable direct lookups, IPv6 live and immutable direct lookups,
  live-validation - measured as five alternating same-session release
  samples on the same host at commit 39df5b0b, medians and peak RSS in
  v4/go/cmd/iprange-v4-bench/evidence/rust-ratio-final-20260830.csv.
  All eight elapsed-time ratios fail the <=1.3x binding (1.322x-2.355x); peak
  RSS fails two of eight (live v4 lookup 1.351x, live-validation
  1.402x). The historical 20260828g matrix is archival; it measured an
  earlier identity (and its workflow number used the composed
  benchmark substitute).
- Necessary-work parity (direction item 2, final state): fresh Go
  and Rust harness runs at the final identity
  (necessary-work-compare-20260830b.csv) reach EXACT counter parity:
  bytes_moved 69,145,640 = Rust and bytes_zeroed 8,487,946 = Rust
  after the final review fixed the same-size replace phantom term,
  the uninstrumented meta encode, and the output seal authority;
  pages_visited 685,291 vs 688,025 (Go less, growth-step sequencing,
  mapping_remaps 1 vs 3).
- Validation cost split (direction item 3, commit 375d78ab): parent/
  worker/mapping/walk phases at 100k/1M/4M
  (validation-phases-20260830.csv); elapsed 1.66/1.62/1.67x, RSS
  1.87/1.40/1.16x, worker fixed cost ~1.2-1.5 ms size-independent,
  worker pprof at 4M shows only graph-walk frames. The historical
  2.299x validation ratio is superseded by 1.322x at the final
  identity.
- update-ipsets workload end to end: the benchmark now drives the real
  one-inode builder and the full SDK surface (scenario_sdk.go);
  apples-to-apples Go-vs-Rust matched 5-sample ratios at
  v4/go/cmd/iprange-v4-bench/evidence/rust-ratio-acceptance-
  20260828g.csv (raw samples committed alongside; archival).
- 18-case CI regression gate at the close identity:
  ci-go-v4-local-20260828i.log, 18/18 within-limit, ratios 0.396-1.025,
  update-ipsets-workflow 2,468 ms (ratio 0.488 vs the accepted Go
  baseline) with 27,009 allocations / 9.65 MB for the 1.1M-record
  workflow (the pre-m5 path, ci-h, measured 27,406 allocs / 9.8 MB;
  the 21.2M allocs / 752 MB figure is the pre-4d state, cut by the
  milestone-4 slice C1 allocation work).
- Native platform rounds: windows/amd64 (costa-win11), darwin/arm64
  (plakam4mini), freebsd/amd64: plain + v4work green after the two
  windows-only fixes (worker .exe suffix handling; portable physical
  block-count probe).
- Cross-language proof: the Go-produced feed fixture and all other Go
  fixtures are verified by the Rust corpus verify; the Rust fixtures
  are verified by the Go conformance suite.

Reviewer findings (final round, five level-1 reviewers of the lead
model, adversarial; scopes: Rust parity Linnaeus, Go idioms Darwin,
performance Hooke, wire/integrity McClintock, APIs/records Plato;
delta 093ddb64..607d2086):

- Linnaeus (Rust parity): PASS. Three P3 fixes applied: (1) the stale
  writer-offcontract LiveWriter::reclaim missing row (contradicted the
  present live-writer row; flipped to removed with the slice-2c
  ownership note and the gate's required-missing entry shrunk in the
  same commit); (2) CreateImmutableFeedV4/V6 documented feed-name
  boundary guard that did not exist (guard added next to the
  budget/source guards; zero-value names now refuse with
  ErrorNameInvalid before any destination artifact); (3) nil-receiver
  join/aggregation/query methods panicked (nil-receiver guards added
  mirroring the right-nil guard; count/getter methods return zero
  values). Note accepted: the supported Go matrix stays 64-bit for the
  metadata addressability trap.
- Darwin (Go idioms): PASS. P3 fixes applied: dead `work := t`
  statement removed from the flagship immutable-feed test; the
  "4-11A wire shapes" hidden-context shorthand in the routing package
  comment rewritten to name the worker-boundary wire records of
  internal/worker. Noted (no change): TestMain worker-build cost on
  every root test invocation (documented fail-stop stance; lazy
  arming possible later).
- Hooke (performance): PASS for closure. P3 residuals accepted as
  tracked follow-up with evidence: nested-overwrite 4.200x (per-width
  probe specialization would land ~3.3-3.6x, not reliably under the
  3.5x envelope; the duplication/codegen option is not cost-justified
  at closure); live-validation 2.299x (embeds the designed worker
  containment spawn cost; profile the parent/worker split as
  follow-up); lookups 1.784/1.792x (per-width fixedLowerBound
  specialization is the cheapest remaining read win, ~1.4-1.5x,
  justified as prioritized follow-up). No new hot-path waste from the
  delta (workflow allocations 27,406 -> 27,009; the 21.2M/752 MB
  figure is the pre-4d state cut by milestone-4 slice C1).
- McClintock (wire/integrity): PASS. Verified the fixture cross-open
  both directions, FREE_MAGIC workspace parity, CommitResult wire
  shape parity, and the worker control/wire surfaces untouched by
  slice E. P3 notes accepted (fixture host identities are an inherent
  corpus-model property; LiveWriter::reclaim row now records the
  live-writer owner).
- Plato (APIs/records): FAIL at HEAD on the stale manifest note and
  the unexecuted status compaction; both fixed in the close commit
  (worker row note rewritten to the implemented matrix; the
  milestone-1..extension-B records moved under the explicit
  Historical milestone records heading). P3 fixed: stray shell
  fragment first line removed from ci-go-v4-local-20260828i.log.
  His verified-PASS items stand: README ratio ranges and workflow
  allocation numbers reproduce the committed logs exactly; the API
  surface parity (feeds, commit resolution, metadata buffers, guards)
  matches Rust.

Full-codebase gate (user mandate, four fresh-context reviewers of the
lead model, at the close identity): two page-copy hunters and two
file-I/O hunters read the entire production tree (~457-476 files,
~103k lines; test tools scanned and excluded from the verdict). All
four PASS, no P1/P2. Accepted P3 notes: the recovery external-sort
[4096]byte run buffers stage logical 12/36-byte range records, never a
database page (byte-identical to the Rust authority); digest and
membership word chunk copies are sub-page (512 B/1024 B) mapped
chunks; metadata zlib decodes straight from mapped chunk views; the
4062-byte membership record scratch is bounded workspace; bench/worker
pprof and .ack harness files are test tooling, not database content.

Same-failure scan:

- Nil-receiver and nil-input sweeps: every public *MembershipScope
  method and every nil source/partner/budget/certification boundary
  was checked in the final round; the receiver guards close the
  class.
- Stale-record sweep: every parity row and every README/evidence claim
  cited in the SOW was re-derived from the committed artifacts;
  the two stale rows (worker matrix note, reclaim duplicate) were
  fixed, and the gate's duplicate-rustRef blind spot is covered by
  the full-surface tripwire plus the final-round sweep.
- mmap-only / no-file-I/O: the two-domain full-codebase gate covers
  the entire production tree at the close identity.
- Performance class: the 18-case CI gate plus the matched Rust-ratio
  matrix cover the measured scenarios at the close identity.

Sensitive data gate:

- This SOW and the committed evidence contain repository paths, public
  API names, synthetic contract descriptions, benchmark artifacts, and
  public source revisions only; no secrets, credentials, bearer tokens,
  SNMP communities, community member names, customer names, personal
  data, customer-identifying IP addresses, private endpoints, or
  proprietary incident details. Fixture and evidence files were
  scanned for personal names and template placeholders.

Artifact maintenance gate:

- AGENTS.md: unchanged; this SOW applies the existing project rules
  (review gate, resource budget, mmap-only gate) rather than changing
  them.
- Runtime project skills: project-final-review and project-v4-rust
  unchanged; no new reusable workflow beyond what they already record.
- Specs: unchanged; binary-format-v4.md and design-iprange-engine.md
  are the normative contract this SOW conforms to, and no behavior
  change requiring a spec edit landed.
- End-user/operator docs: v4/go/doc.go now states the worker
  cross-build matrix and the fail-closed stance of non-worker
  platforms (ErrorOSUnsupported before any scan; no in-process
  fallback); the bench evidence README documents every committed
  artifact.
- End-user/operator skills: none exist.
- SOW lifecycle: SOW-0027 moves to done/ as completed in the close
  commit; SOW-0017 remains blocked until this SOW's completion is
  accepted; pending SOW-0028/0029 are parallel-session records and
  were not touched.

Specs update:

- No spec update: the delivered behavior conforms to the current
  normative specs; the only doc change is the Go package platform
  note above.

Project skills update:

- No skill update: the review-gate, full-codebase gate, and resource
  budget rules are already recorded in AGENTS.md and this SOW's
  Review Gate section.

End-user/operator docs update:

- Go package doc (doc.go): worker platform matrix note added.
- Bench evidence README: close-identity artifacts listed with
  reproduction instructions.

End-user/operator skills update:

- None exist; nothing in the delivered surface creates a downstream
  operator workflow.

Lessons:

- The five acceptance-time lessons are recorded in the Lessons
  Extracted section below (measure real operations; keep records in
  the implementing commit; allocation elimination is the
  high-leverage Go win; the full-codebase review gate is cheap at
  this scale; nil-receiver guards are part of the typed-input
  contract).

Follow-up mapping:

- Recorded in the Followup section below: the reviewer-adjudicated
  performance residuals (lookups, validation, nested-overwrite), the
  accepted divergences, SOW-0017 sequencing, and the explicit
  statement that no untracked deferred implementation item remains.

## Outcome

NOT COMPLETE - in-progress, reopened (regression record below). The
implementation surface of this SOW is delivered: the Go v4 SDK matches
the Rust surface and wire contract (normative live-only writer, one-inode
immutable feed construction, exact commit-outcome resolution, bounded
reclamation, metadata buffer APIs, zero-allocation streaming facades,
the parity ledger, worker containment on every worker-supported platform,
and the shared 14-fixture conformance corpus that cross-opens in both
implementations). The earlier close record that claimed the update-ipsets
workflow at 2.189x "inside the accepted write envelope" is VOID: that
envelope was superseded 2026-08-29 by the user-confirmed binding <=1.3x
elapsed-time and peak-RSS acceptance (user decisions 1A/2A), and the
final-identity matrix (rust-ratio-final-20260830.csv, five alternating same-session
release samples) shows every one of the eight acceptance scenarios
outside that binding: elapsed 1.322x-2.355x, and peak RSS failing two
of eight scenarios. All bounded safe-Go leads available under the no-unsafe
constraint were measured at the final identity and are either retained
(authoritative tree-header parser) or measured-and-rejected (KeyU32
probe, dispatch removal, result transport); the remaining costs are
profiled and quantified in the Status sub-states. The measured-performance
decision (accept the measured result as-is, or pursue further work) is
the user's, and this SOW stays in-progress until it is recorded; the
five-reviewer delta round on the actual final commit (direction item 6)
runs before that decision is presented.

## Lessons Extracted

- A benchmark substitute (live-create+snapshot for the immutable
  feed) can mask the real workflow cost even when every headline
  ratio is honest; the apples-to-apples one-inode measurement moved
  the workflow ratio from 3.209x to 2.189x and Go elapsed from
  3,478 ms to 2,450 ms. Measure the real operation, not a composed
  proxy.
- Records drift with code: parity manifest rows and evidence notes
  must be updated in the same commit as the behavior they describe
  (the worker row and the reclaim duplicate both survived multiple
  slice commits because the gate checked presence, not truth).
  Reviewer sweeps that re-derive every cited number from the
  committed artifacts catch this class.
- The m5 package proved the highest-leverage Go performance win is
  eliminating per-record write allocations (21.2M -> 27K allocs);
  the remaining read-path gap is a bounded, profiled,
  allocation-free residual with a known cheapest fix (per-width
  probe specialization).
- The mmap-only gate is cheap at this scale: two fresh-context
  reviewers per domain can read the entire production tree and give
  an auditable PASS, replacing the rejected scan corpora.
- Nil-receiver guards are part of the typed-input contract in Go:
  Rust's value types cannot express a nil self, so the Go boundary
  must mirror its nil-argument guards on every public receiver.

## Followup

- Measured-performance decision (the only blocker, user's call): the
  final-identity matrix (rust-ratio-final-20260830.csv) fails the
  binding 1A/2A acceptance in all eight elapsed-time scenarios
  (1.322x-2.355x) and in two RSS scenarios (1.351x, 1.402x). Every bounded safe-Go
  lead under the no-unsafe constraint was measured and either
  retained (authoritative expected-tree-header parser, reads -7-9%)
  or rejected (KeyU32 probe neutral; dispatch removal regression;
  result transport measured and kept as the 2.31x state). The
  dominant Go-only costs at the final identity are per-page header
  decode, per-probe extent validation, the Go runtime/GC share of the
  tree machinery, and the validation walk - all quantified in the
  Status sub-states and in validation-phases-20260830.csv. The
  measured-performance decision (accept as-is, or pursue a specific further
  lead) is presented to the user after the five-reviewer delta round;
  this SOW stays in-progress until decided.
- Residuals the user does not accept (tracked): pending
  SOW-0030-20260829-v4-read-and-validation-specialization.md is
  rewritten (2026-08-30) to own the actual residuals under the binding
  <=1.3x elapsed/RSS contract - the read-path residuals (v6 1.326/1.357x,
  v4 1.525/1.582x), the write residual (nested-overwrite 2.355x),
  the validation walk residual (1.322x), and the out-of-window
  structure_table.go counter gap - with the voided 1.2-1.6x envelope
  and the false SOW-0017 dependency removed. It is startable only after this
  SOW closes and only if the user chooses to continue; if the user
  accepts the measured result, the residuals close as accepted limitations.
- Explicitly accepted divergences (recorded, not defects): Rust
  CancellationToken::from_poll is not portable to Go (nil token is
  the uncancellable form); the apple filesec creator-only machine and
  Cardinality129 stay internal with evidence notes in the parity
  ledger; platforms without a worker build refuse validation,
  inspection, and recovery fail-closed with ErrorOSUnsupported
  (v4/go/internal/routing/routing_other.go; no in-process fallback).
- SOW-0017: snapshot signing remains dependent on accepted completion
  of this unsigned SDK parity SOW; it unblocks after the measured-performance
  decision closes this SOW.
- No untracked deferred implementation item remains: the v6 bench
  scenarios and the necessary-work counter parity (the two items this
  SOW previously tracked as follow-up) were delivered inside this SOW
  (commits 39df5b0b and 0abb7488); the only items that remain are the
  measured-performance decision above, the SOW-0030 residual ownership below
  it, and one tracked evidence-scope note: writer/structure_table.go
  is outside every necessary-work evidence window and carries no work
  counters; it joins the counter evidence set (with Rust-mirroring
  per-call formulas) in the first SOW that exercises structured-table
  counters, per the final-round scope ruling.

## Regression Log

None. This SOW records an incorrect prior acceptance and the
reconciliation that closed it, not a later regression of previously
proven behavior.


## Regression - 2026-08-29

The completed close (commits 770484eb and 39bf2c66) was revisited by
an external single-sol reviewer (read-only). Every material claim of
that review was re-verified against the code, the specs, the CI
workflows, and this SOW's own records. The close is reopened for five
verified defects:

1. P1 - Performance acceptance was bypassed. The user-set mandate
   (read 1.2-1.6x, validation 1.5-2x, write 2-3.5x Rust) is still
   exceeded at the close identity: nested-overwrite 4.200x,
   live/immutable lookups 1.784/1.792x, live-validation 2.299x
   (v4/go/cmd/iprange-v4-bench/evidence/rust-ratio-acceptance-
   20260828g.csv). This SOW recorded an open user decision for the
   residual acceptance (the milestone-close decision paragraph in the
   milestone-4 records) and no user decision was recorded before the
   close; the close substituted a reviewer's P3 adjudication for the
   required product acceptance. SOW-0030 confirms no user decision
   exists. The earlier conversation direction (user: make this
   optimal; pick the best-performance option) was not recorded in the
   SOW either.
2. P1 - Worker containment violates the specification.
   binary-format-v4.md section 19 requires faultable validation and
   recovery scans to run in a version-matched worker and to fail
   before scanning when the worker is unavailable.
   v4/go/internal/routing/routing_other.go runs the in-process
   machines on every platform outside (linux||darwin||freebsd||
   windows) x (amd64||arm64), and v4/go/doc.go documents that as
   supported behavior. The worker binary cannot even compile on those
   platforms today (internal/worker/atomics.go has assembly-backed
   mapped-control atomics only for amd64/arm64), and CI actively runs
   the full Go suite on linux/s390x (.github/workflows/big-endian.yml),
   so the faultable scans run in the application process on a
   CI-supported target. Unsupported targets must fail closed, per the
   spec.
3. P1 - Required mixed-language evidence is incomplete. The accepted
   criteria require cross-language coverage of stale reader slots,
   sidecar replacement, transition/reservation states, and publication
   inspection. The implemented battery has five modes (reader,
   exclusion, pinned, resolve, snapshot; v4/go/mixed_live_test.go); the
   four coordination cases above do not exist as cross-language tests
   in either direction.
4. P2 - The parity gate does not prove Rust-to-Go parity. The gate
   enumerates exported functions and methods only and explicitly
   excludes types and constants (v4/go/parity_gate_test.go), it never
   consumes the Rust export inventory (v4/go/parity_rust_public.tsv,
   which is malformed around the lib-reexport rows), and it recorded
   Cardinality129 as missing/internal while the type is publicly
   exported as an alias (v4/go/types.go). A green gate therefore
   cannot detect a newly missing Rust operation.
5. P2 - The final reviewers did not review the final code. The
   recorded final round covered 093ddb64..607d2086; the close commits
   changed eleven Go files afterwards, and only two of the five
   reviewers re-verified the working tree. No five-reviewer round ran
   on the actual final revision.

Why the previous validation missed these: the close treated reviewer
adjudication as user acceptance on a recorded product decision; the
supported-platform matrix was self-defined by the worker build state
instead of the spec and the CI matrix; the parity gate verified
Go-surface presence only, never the Rust inventory; and the final
review round predated the final commits.

Repair plan (recorded before implementation):

- Performance: RESOLVED 2026-08-29 (user decision A): implement the
  per-width read-probe specialization, the code-generated per-family
  write-path specialization, and the validation re-measure; re-measure
  at the new identity.
- Worker containment: fail closed with a typed error before any scan
  on platforms without a worker build; update doc.go, the parity
  records, and the s390x-relevant tests/CI expectations.
- Mixed-language matrix: add the missing coordination modes (stale
  slots, sidecar replacement, transition/reservation states,
  publication inspection) to both directions of the mixed battery.
- Parity gate: extend the surface enumeration to exported types and
  constants, fix the Cardinality129 record, repair the Rust export
  inventory, and make the gate consume it so a newly missing Rust
  operation fails CI.
- Final review: repeat the full five-reviewer round on the actual
  final revision, then close in one lifecycle commit (status change,
  artifact updates, and done/ move together).

Validation for the repair: every slice keeps the standing battery
(plain + v4work suites, vet, gofmt, race/checkptr, parity gate, Rust
conformance corpus, matched Rust-ratio evidence), all under nice.
