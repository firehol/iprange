Every measurement report in this directory records its own provenance,
and reading that provenance is the only way to know what a committed
artifact measures.  The set is now a completed rotation: all twenty
measurement reports, `build-ids.json`, and `battery-manifest.json` carry
`git_head` `38aea8fc5777d1cf985ab2e53e7a66e503f59469` — the committed
chunk-W revision — because the wave-19.26 full-tier closure battery ran
with the work tree checked out at exactly that commit and the two
Windows reports were authored natively on the authorized validation host
from a fresh detached checkout of the same published commit (transferred
as a complete-history git bundle; tree `8f977554212f952fa8df83f943684e1c606aa9ee`
equal to the qualification workstation's, `git status --porcelain` empty
before and after; the fifty-three files the commit changes against its
parent are pinned in the reports by commit-blob SHA-256).  The earlier
mid-rotation state (Linux reports at `7c2d2cf7`, Windows reports at the
throwaway snapshot, a manifest binding zero live files) is what the kind
gate (`check_kind_coverage.py`) refuses, and the manifest — emitted by
that same gate over the fresh reports and promoted through the shared
committed-report writer — is what makes relabelling detectable without
trusting the producing machine: it binds every report by role, byte
length, content SHA-256, and revision, plus the eleven-entry staged
binary ledger by digest.  Both kind-gate passes (fresh and committed,
after rotation) returned rc 0 at the revision they name.  The parity
class now carries ONE committed artifact: the chosen axis — at the
milestone tier the 378-cell full sweep, which replaces the routine
subset — executes once per gate under one committed name (astra gate
finding P2-6; an earlier wave paid for the same axis twice under two
names, which is exactly the rule "an expensive axis runs at most once
per gate and replaces its overlapping subset" forbids).  The manifest
committed here is the one that battery emitted, and it is therefore
true of its own revision `38aea8fc`: it lists two `refusal-class-parity`
entries, because that tree still carried
`refusal-class-parity-full.json`.  That file is deleted at this
revision and the next battery emits a one-entry binding; editing a
committed manifest to describe a tree it never measured would be the
forgery the audit exists to refuse, so the two-entry state is a
designed pre-rotation lag — the same condition this directory's corpus
paragraph documents for the matrix reports, and the kind gate's
rotation tolerance classifies.  The post-rotation pass is what attests
the single-name set.
Against the previously fully-qualified revision `2c788b8e`, this set's
revisions DO change engine sources — the legacy parse/DNS paths of
`v4/rust/iprange-cli` and `v4/go/internal/cli/legacy` (the
Windows-portability repairs, the DNS numeric-form platform scoping with
its cross-language effective-build-set gate, and the android/bionic
umbrella correction) — plus the `v4/cli` gates and records.  What
carries over is the **worker-handshake build identity**: `build-ids.json`
hashes only `iprange-livedb` inputs, and none changed since `7c2d2cf7`,
so the identity below names the same digest for every artifact in the
set.

Rotation contract.  No report here is authored by hand and none is
expected to be edited in place.  The qualification battery builds the
products from one tree, runs every gate against those binaries, writes
each report into a scratch directory, and rotates the complete set into
this directory as one unit; `check_kind_coverage.py --emit-manifest`
then produces `battery-manifest.json`, which records for every report
its role, its byte length, the SHA-256 of its content, and its
`git_head`, together with the staged binary ledger as digest-to-name
entries.  The gate refuses a report set whose members disagree about
the revision they describe, and the manifest is what makes a uniform
rewrite of every `git_head` — relabelling old evidence as new — detect
without access to the machine that produced it.  The manifest binds
ledger entries, not workstation paths: no committed artifact carries a
path into an operator profile, and the shared writer refuses such a
write.  This head block stays the present-state record of the CLI
qualification surface: gates, corpus, evidence identity, toolchain and
binary provenance.

Battery outcome at the revision the committed reports describe.  Every
step of the wave-19.26 full-tier closure battery
(`/tmp/qualsvc/battery-w1926.sh`, script SHA-256
`6c593beb26aa181bc9a29f437d34bc03b42386c0c8d146e353f3a46975ce781f`,
140,668 bytes, run under `nice` through the self-identifying launcher,
whose first console-log action is the SHA-256 of the exact bytes about to
execute) recorded rc 0 on its first attempt: clean-staging builds of
both engines from the checked-out revision, both unit suites, the Rust
warning gate, the GOOS cross-compilation matrix (19 steps ran, 1 skipped
as unsupported by the toolchain), the four matrices, the crash battery
in both mixed directions and its two `/usr/bin/false` negatives, the
resource and throughput proofs with their self-tests, golden,
sensitivity, the refusal-class parity gate sweeping its chosen
pressure axis exactly once (at the milestone tier that is the 378-cell
full product, which replaces the 108-cell routine subset in the same
invocation — one ledger-bound run, one committed name, one manifest
digest; astra gate finding P2-6 retired the duplicate second sweep),
the POSIX guard, the FIFO surface gate, the committed swap-race battery
replay, `tests.d` for both engines (121 of 121 groups green per engine),
the ledger reconciliation, the kind gate on fresh and on committed
reports, the forgery battery, and the coverage harness.  The console
recorded 321 checked steps, 0 mismatches, and 1 deferral: the rotation
of the two Windows reports, which the native Windows leg owns — it
authored them on the same revision, and the Linux leg refuses to restate
an artifact it did not measure.  Wall time: 319.9 s at pooled 12 jobs
(the pre-pool serial estimate of ~65 minutes is obsolete; the milestone
pressure sweep alone is ~100 s of it).  The `recheck-*` logs present in
the battery reports directory are the kind gate's deliberate second pass
against the rotated files, not repairs.  The race probes execute from
their own scratch directories, so the battery creates no
repository-root scratch.

Matrices and corpus.  `v4/cli/cases/` holds 75 case files: the 71 the
committed matrix reports were generated over at the revision under test
(closing the 63-versus-71 disclosure of earlier waves), plus the four
publication cases this gate-fix wave adds —
`publish.dns_overflow_batch` and `publish.legacy_lexical_forms` (astra
P1-1/P1-2), `publish.binary_v6_full_range` (astra P1-3, the wrapping
full-universe payload) and `publish.binary_v6_asymmetric_limb`
(parity round-9 F1: a cross-producer v2 payload whose record carries
distinct lo/hi limbs, pinned through the export's netset rows and
sha256 so a limb-order swap in either engine is caught on content, not
just on the reply); the rotation battery regenerates the matrix reports
over all 75, and the kind gate treats that lag as the designed
pre-rotation condition (report behind tree only).  The eight
cases added after the first 63 are the two feed outcome cases, the four
`maintenance.roundtrip.*` cases, and the two `params.negative.budget_*`
cases named below.  Expectations are written
from the staged Rust binary answers because Rust is the recorded
authority for v4 semantics: seven budget objects from the JSON-RPC specification
each take the seven `params.negative.budget_*` cases (`snapshot_budget`,
`validation_budget`, `recovery_budget`, `algebra_budget`,
`algebra_output_budget`, `result_budget`, `immutable_feed_budget`); each
case sends the budgets of a real call with one `u32` member set to an
out-of-range or wrong-typed value and asserts `expect_params_rejected`
(transport `-32602`), pinning the refusal at the params boundary;
twenty-five refused requests are asserted in total and each was verified
refused by both engines.  `validation_budget` and `recovery_budget` omit
zero for `max_scratch_files` because the specification gives zero a
meaning there — disabled — so asserting a refusal would assert something
the contract does not say
(`.agents/sow/specs/iprange-jsonrpc-v1.md:241-246`).  The other seven
cases pin refusal classes plus one success path:
`writer.symlink_live_direct_replace` and `writer.symlink_live_feeds_create`
(`wrong_state` for a writer aimed through a symlink at a live database),
`snapshot.zero_byte_destination` (a zero-length destination under
`replace_existing` succeeds with published bytes and no previous
destination), `recovery.inspect_live_junk`
(`live_recovery_current_generation_unprovable`), `recover.live_junk`
(`recovery_candidate_changed`), `feeds.missing_feed_outcomes`
(`name_not_found` with `read_only_failure`), and
`validate.live_sidecar_fold` (`live_recovery_coordination_unavailable`).
Committed results against the integrated binaries: `matrix-rust` 71 PASS
/ 0 FAIL / 0 skipped (37 oracle checks); `matrix-go` 71 PASS / 0 FAIL / 0
skipped (37 oracle checks); `matrix-rust_to_go` 40 PASS + 31 skipped (22
oracle checks); `matrix-go_to_rust` 40 PASS + 31 skipped (22 oracle
checks).  The mixed-matrix skips are cases without a cross-producer or
cross-consumer step, which those matrices cannot exercise by design.
The `known-defects.json` ledger is committed empty, and the kind gate
enforces it in both directions — an unlisted FAIL fails the gate, and a
listed defect that passes fails
it.  `check_golden.py` reports 55 golden exchanges PASS; the committed
`golden.json` records 71 case files for its own revision, and the tree
here holds 75 — the four publication cases this wave adds — which the
rotation re-walks (the kind gate's corpus-drift tolerance covers the
lag).  `sensitivity_gate.py` reports 14 modes PASS.

Refusal-class parity gate (committed).
`v4/cli/check_refusal_class_parity.py` drives both binaries over a
mechanically derived grid of 23 arms x 21 path kinds = 483 cells, each
cell compared Go-vs-Rust on `(kind, transport code, data.code,
outcome)` plus the publication-evidence shape; a third axis sweeps 9
arms over 42 descriptor-pressure profiles (12 in `--pressure routine`)
against a committed table of 378 pinned pressure classes under
`PINNED_PRESSURE_CLASSES_SHA256`
`314d8be5618e9775d9dd386ad6d7e521ff27ee9ba63d7c9501746de941ad1456`.
Message text is not compared because it is a human diagnostic and the
machine contract is the code and outcome.  Every attempt runs
under its own bounded deadline (4 s) and a differing repeat is retried
(2 retries), so a flake is counted apart from a divergence and a hang
apart from both.  Budget: 55 s per grid; measured 7.9 s with the pooled
grid runner (12 jobs) — the milestone pressure axis runs beside the
grid and reports its own `pressure.elapsed_seconds` (101.2 s for the
committed sweep).  `evidence/refusal-class-parity.json` carries each
binary's SHA-256 and `system.describe` implementation label plus
`git_head`.  The verdict is two-layer on purpose: the report must first
describe its own execution honestly (cell count against the derived
grid, every disagreeing cell named in the divergences list, no cell
claiming agreement with differing answers), and the contract term is
applied on top — zero divergences, no unanswered cell, no flaky cell,
every pinned refusal satisfied.  Committed result in
`refusal-class-parity.json` at its recorded `git_head`: 483/483 cells
executed, 0 divergences, 0 hangs, 0 flaky, 36/36 pinned refusals
satisfied, verdict PASS, plus 378/378 pressure cells (`mode` full, all
42 milestone profiles) with 0 blocked, 0 missing, and 0 vacuous.  One
sweep, one artifact, one manifest digest: the milestone tier sweeps the
full axis as the regular artifact (astra gate finding P2-6 retired the
second committed name and the duplicate sweep that justified it).  The
418-cell/29-pin state recorded in earlier waves is closed by this
rotation.  `--self-test`
(70 controls, pinned by `SELF_TEST_CASES_TOTAL`, offline)
includes the three anchors this gate exists to provide: an injected
synthetic divergence must FAIL, a report with zero executed cells must
FAIL, and deleting the fixture that pins the `validate.live`
sidecar-fold shape (`live_recovery_coordination_unavailable`) must FAIL.
It also attacks the attempt fold directly (astra gate finding P2-4):
the same physical [wedge, pass] mix must grade disagreement in either
order, and a truthfully disagreed pressure cell must FAIL the report
verdict — the fold's disagreement is now an obligation of the verdict,
not only a console line, so a committed zero-flake report means every
attempt of every cell agreed.
The mid-wave 42-divergence state those product workers were created for
is closed: the writer-symlink, recovery-order, feeds-outcome, zero-length,
procfs/sysfs, and unix-socket classes all agree cell-for-cell now.

Kind-coverage gate (hardened).  Its `--self-test` runs 133 rejection controls plus 5 acceptance controls
offline; each records its outcome and the battery judges the run once at
the end, so an assertion removed from a single helper cannot turn an
accepted forgery into a passing self-test, and the control count is
compared against an exact constant rather than a floor, so deleting a
control fails instead of lowering the requirement.  The self-test is a
deliberate opt-in invocation (a ruling this wave), and the battery runs
it every time.  The known-defects ledger is consulted on every run, not
only when a matrix reports failures, and its staleness control cannot be
satisfied by the unrelated `undeclared or stale failed case(s)` counter.
`git_head` is load-bearing: each of the seven consumed reports — four
matrices, crash, FIFO-surface, throughput — must carry it as a 40-hex
object id, placeholder shapes (`0`*40, `1`*40) and any disagreement
between the seven are rejected, and omission of a required input is a
CLI error.  PASS-row deletion is defended: the gate derives the expected
executed-case inventory from `v4/cli/cases/*.json` plus the matrix skip
rules and compares it against each report's rows, so a report that drops
PASS rows, omits a case, invents one, or moves a row between status
classes fails instead of passing on a smaller claim.  Committed kind-gate
verdict on both the fresh battery reports and the rotated files in this
directory: rc 0, every required artifact kind carries both-language
created-by evidence (reader-sidecar kinds carry created-by only, which is
their contract shape), no unknown kind.

Evidence identity binding.  `throughput.json` and `fifo-surface.json`
are validated the way the matrix and crash reports are: each executed
actor's binary is identified by SHA-256 and by its `system.describe`
implementation label bound to the executed-actor provenance (not to a
free-text label), must appear in the staged SHASUMS ledger when one is
supplied (`--sha256-ledger`), and its on-disk digest is re-hashed when
binary verification is enabled.  Throughput additionally re-derives its
own arithmetic: replies must equal requests in every round, the recorded
rate must equal `requests/seconds` within 2%, the median must equal the
median of the round rates, and the thread census must be internally
consistent.  The crash battery reports 16 scenarios PASS in each mixed
direction, and its two `/usr/bin/false` negative controls each record 0
of 16 scenarios passing, proving a substituted non-product binary is
detected.  The resource harness records 8/8 proofs PASS under bounded
read and write deadlines, and the throughput attestation (committed
artifact figures) records go median 15,394.0 replies/s (rounds
10,595.5 / 15,394.0 / 17,105.2) and rust median 45,751.7 replies/s
(rounds 44,010.1 / 45,751.7 / 53,368.4), each with its 12-case
self-test.  These are host-load observations, not a contract; the
census terms are below in the throughput entry.

Coverage (measured, not asserted).  `evidence/coverage-go.json`, produced
by `v4/cli/coverage_harness.py`, records unit and corpus-driven
integration coverage of the Go module.  At this revision (instrumented
build of the `38aea8fc` working tree, `go version go1.27.0
linux/amd64`, covermode `atomic`): unit 28,154/57,481 statements =
48.98%, 3,906/5,954 functions = 65.60%, 19,122/42,077 blocks = 45.45%;
corpus-driven integration 22,286/54,062 = 41.22%, 3,453/5,742 = 60.14%,
14,828/39,747 = 37.31%; merged 34,760/57,493 = 60.46%, 4,817/5,955 =
80.89%, 23,447/42,084 = 55.71%.  The closure policy this serves is:
committed detecting tests, a mutation/forgery battery that proves the
gates reject bad evidence, and measured unit AND integration coverage —
with no arbitrary numeric floor, because a percentage is an input to
judgement and not the judgement.  Two separations are load-bearing and
recorded in the artifact: the instrumented binaries are built in their
own staging directory and are never the binaries used for the throughput
or performance attestation, and killed runs (the crash battery kills its
children, and a killed Go coverage binary writes no counter block) are
never merged into the coverage evidence — only complete matrix runs
contribute (each recorded matrix run shows `rc 0` or the contract's
`rc 1` with its full 71/0/0 or 40/0/31 tallies intact).  `run.py` forwards
`GOCOVERDIR` to the child only because the harness sets it; the
allowlist entry adds no child state to an ordinary qualification run.
The harness self-test (14 cases) includes a source pin requiring every
recorded `command` field to pass through the shared sanitizer, and the
committed artifact records `v4/cli/run.py` checkout-relatively — no
report in this directory records an operator home path.

Unit suites.  `go test ./... -count=1` over `v4/go` (canonical
`CGO_ENABLED=0`): 25 packages ok, 7 with no test files, 0 failures.
`cargo test` over the Rust workspace: 1,043 tests passed across 63
suites, 0 failed, from a fresh `CARGO_TARGET_DIR` with the warning gate
rc 0 and the Linux pin tally exported and empty (0 UNSCORED lines: every
platform-gated pin scored on this host).  `tests.d` (the legacy-compatible suite, run against
each staged engine): 121 of 121 groups pass for both engines, including
`121-legacy-fifo-input` (renamed from `102-` when parallel suite groups
were added), which proves each of
C, Rust and Go waits for a delayed FIFO producer, consumes its
addresses, and finishes under a bounded `timeout` — the legacy stream
input contract is preserved, and the regular-file requirements are
scoped to the JSON-RPC surface (`.agents/sow/specs/iprange-jsonrpc-v1.md`
"Legacy coexistence").  The never-block caller-path class was re-raced
this wave on every arm (operations 30 attempts x 8 arms, parity
120-trial input race, glm and performance swap probes): hangs=0,
wedged=0 on both engines.

Toolchain, staging, and binary identities.  Linux:
`go version go1.27.0 linux/amd64`; `rustc 1.91.1 (ed61e7d7e 2025-11-07)`,
host `x86_64-unknown-linux-gnu`, LLVM 21.1.2; harness interpreter
CPython 3.14.7 on Linux 7.1.9-1-MANJARO x86_64.  The Go products were
built `CGO_ENABLED=0 -trimpath -buildvcs=false` from a clean staging
copy at `/tmp/iprange-w1926f2/go-stage`, so they embed neither local
paths nor a VCS revision and their digests are layout-independent: a
`CGO_ENABLED=0` rebuild of the source commit reproduces them byte for
byte on any host.  The Rust products were built `--release
--all-features --bins --examples` from the fixed staging path
`/tmp/iprange-w1926f2/rust-stage` with a fresh
`CARGO_TARGET_DIR=/tmp/iprange-w1926f2/rust-target` and the
repository-local `CARGO_HOME` under `.local/int-prep`, then copied to
the qualification paths recorded in each report; the non-worker Rust
binaries embed staging and registry source paths, so their digests are
environment-bound artifacts reproducible only with that staging layout
and that CARGO_HOME, while the Rust worker binary carries no such
paths.  This is why the Linux identities below differ from the
round-5 kit-staged pair (`436b1e9c…`): the battery's clean staging is a
different environment for the path-embedding binaries, not a different
build — the same source, the same recipe, the same build identity, and
each artifact is identified by digest in the ledger, never by name
alone.  Linux identities (SHA-256):
go `iprange` `8a3a45a80b9bbafbde24246af71466c2cf93bb6c90c064d93643ad76d7bb12ce`,
go worker `c744e02fd72a8d5dddb5999bfca992e4d72d46b72afed3f7d829b8901e028a84`,
rust `iprange` `0142338a00e6bd32427fec468a9a03e759f214e31fcb3d73e8165b2146db8626`,
rust worker `8f81ec8328a195f30b7dabc3771e8d52a4a3deb7390653e231673a93c698d4a8`,
v4-fixture `77e0970e1e992578aac01d8c522348d79907a2a94e14d98b26fb23849b094ac3`.
The host-invariant engine build identity for every artifact in this set
is `66ddf70b52640f4baedda9313d2a13fecaa6735ce85a6bbd1211016d69de0e9d`
(computed by `v4/rust/iprange-livedb/build.rs` over the package manifest
and 357 source files with the host-invariant logical-name algorithm, so
one source state has one identity across hosts; `--emit-build-ids`
recomputed it at the rotation and found it verbatim in the bytes of the
battery's built Rust products, equal across the four
`build-ids.json` platform entries; the native Windows leg verified the
same digest in both of its built products).  `iprange-livedb` has not
changed since `7c2d2cf7`, which is why the identity survived every
chunk-W revision; the previously qualified `a72ae911…` changed at that
point (`worker/client.rs`), not here.  The staged ledger
`.local/shared/binaries/SHASUMS.txt` lists 11 members (5 Linux, 6
Windows: `win/go/*.{2 exes}`, `win/rust/*.{2 exes}`,
`win/v4-fixture.exe`, `win/fixture-w1924b.iprange`) and verifies 11/11
OK, including the Windows artifacts installed from the native leg
below.

Windows status.  Native Windows re-qualification ran on the authorized
Windows validation host from a fresh clone of a git bundle transferred
over ssh with `cat` (rsync and scp/sftp are unavailable on that host),
detached at the published revision under test with `git status
--porcelain` empty before and after; the bundle verifies as recording a
complete history, and the checked-out tree object equals the
qualification workstation's tree for that commit, so tree hashing —
which covers every tracked file — proves the checkout byte-identical to
the source the Linux evidence measures.  The 53 files the commit changes
against its parent (evidence included, by design: the two reports this
leg authors are its outputs) are each pinned in `build_provenance` by
the SHA-256 of the COMMIT BLOB, verified in one batched `git cat-file
--batch` pass — blob digests, so no host checkout configuration can
break the comparison.  Host toolchains: `go version go1.26.5
windows/amd64`; `rustc 1.97.1 (8bab26f4f 2026-07-14)` host
`x86_64-pc-windows-msvc`, LLVM 22.1.6; CPython 3.14.6 (mingw64,
`os.name=nt`).  The scored driver run recorded 30 battery steps (36 rows
total with the report leg) each green on its first attempt, zero red,
zero retried; `flake_history` and `driver-invocations.log` disclose the
two unscored invocations that preceded it (one aborted at the
manifest-verify step when the workstation's manifest carried post-commit
worktree digests — the manifest now hashes commit blobs; one aborted on
a transient `go-vet` `STATUS_DLL_INIT_FAILED` whose attempt 2 ran green,
recorded with flake=yes and the reason in `flake-reasons.tsv`) and
isolate their surviving files outside the scored `logs/`.
Native `go test ./... -count=1`: 32 packages — 24 ok, 8 with no test
files, 0 failures — scored with no concurrent compile, plus the
maintenance removal round-trip
`TestMaintenanceListRowsRoundTripIntoRemoveForEveryRemovableKind` run
separately with `-v`: 4 of 4 PASS rows (the test plus its scratch,
reservation and publication_temp subtests), and the five wave-19.25
touched packages each re-run `-v -count=1` rc 0.  Native
`cargo test -p iprange-cli --no-fail-fast`: 350 passed, 0 failed, 0
ignored across the 13 test targets, with the recorded
worker-colocation step (copy the release `iprange-v4-worker.exe` into
`target/debug/deps` after `cargo test --no-run`, which must create that
directory first — without the colocation the six worker-dependent
`live_source_tests` answer `os_unsupported` again, so any Windows gate
command must carry the copy step), and the pin tally file present with 0
lines and 0 UNSCORED entries.  The six `windows_judgment_tests` pass
individually and as a filtered group, including
`directory_is_refused_as_not_regular` and
`character_device_is_refused_as_not_regular` — the zero-access
`CreateFileW` + `GetFileAttributesW` classifier is proven natively — and
the reader-sidecar pins pass.  All three shared self-tests print
`executed=57 expected=57` natively, closing the mingw64 fold defect the
previous wave found and this revision fixed: on that interpreter
`os.name == 'nt'` while `os.sep == '/'` and `ntpath.normcase` folds case
only, and the profile matcher had been comparing two spellings of one
directory.  The chunk-W numeric-form scoping is measured natively, not
carried by tag reading: the `cross-engine-numeric` consensus step fed
`0x7f000001` to both Windows products and requires rc and stdout to
agree — both exit 1 with empty stdout, so both refuse the form exactly
as `dns_numeric_refuse.go` scopes Windows to do; `TestDNSNumericFormsAreNotAnsweredHere`
executed the refusal half of the shared case table natively green, the
answering half (`TestDNSNumericFormsMatchC`) and the hex bookkeeping
cases scoped out with their named reasons (15 recorded SCOPED rows),
and the Rust hex pin compiled to zero tests by construction — its
`running 0 tests` line is cited in the report rather than the case
omitted.  `windows-guard.json`: PASS for both products, 50 case keys
each (44 refusals, 3 allowed controls, plus the reopen/sidecar/source
facts), `all_ok=true`, fixture digest matching the staged database.
`windows-housekeeping.json`: 2 passed / 0 failed, `windows_qualified=true`,
50 removal-output rows and 50 removal-log rows per product with the
shared log digest
`96c8ab39679692f3a70afe878681c5217adf75125164d36e9035cd989cb6d328`
unchanged across waves, cross-listing row equality, shared volume
identity, zero temp residue.
Each report embeds the six Windows artifact digests verified against the
host builds — go `iprange.exe`
`5752f017b675b0611dc9895aae70b7d5faa9609af7981f558bd10c482bca3e5f`,
go worker `fe961413c803ce89b96b5f1e5ac121f533603af811a79507dcb8eed248b82660`,
rust `iprange.exe` `abeecb3b0ad1e92b809be9cd592b16dd71e9e54d6bd480399852df864a8f81b1`,
rust worker `c9e62a9ce2bc1ec4fe17eec753daa5e369b8fe244e1070267eeabaa653bc2b7a`,
fixture tool `37819aefc648ceccad221b9e06d8e6bf4f1674fb22e66f098dad0390d3c842c6`,
fixture database `03da3fde96e014f59d02175fe71702225389817f8cc30f37e2a92b7cc8a38415`
— plus `build_provenance` with the host toolchain lines, the native
test tallies, and the build commands.  The Go Windows `iprange.exe`
digest differs from the previous wave's (`bbd37c63…`) because chunk W
changed `v4/go` sources (the platform-scoped numeric-form split); the
worker is unchanged byte-for-byte, and both digests are the ones the
Linux ledger stages, so the two legs describe one source.  Both reports
are sanitized: no host login name, no personal home path (the on-host
privacy gate scans every string in every reading — raw, case-folded,
separator-folded, MSYS-mount-translated — and refuses any artifact that
names the operator's profile; its recorded rc 0 step is in the report),
every drive path is under the host's `C:/msys64/tmp/` scratch except the
mingw64 interpreter named in the recorded toolchain line, the nodename
is redacted from the toolchain strings, and `checkout_root` is `null`.

Which reports name which revision.  Twenty measurement reports,
`build-ids.json`, and `battery-manifest.json` in this directory carry
`git_head=38aea8fc5777d1cf985ab2e53e7a66e503f59469`: the four matrices,
`crash.json`, `crash-go_to_rust.json`, `crash-negative.json`,
`crash-negative-producer-false.json`, `fifo-surface.json`,
`throughput.json`, `refusal-class-parity.json`, `coverage-go.json`,
`golden.json`, `sensitivity.json`, `resource.json`, `guard-posix.json`,
`race-battery.json`, `windows-guard.json`, and
`windows-housekeeping.json`.  `known-defects.json` is a ledger rather
than a measurement and carries no revision field.  One disclosure about
what `git_head` means in these reports: the harnesses stamp the HEAD
commit the build ran from, and the battery ran with the revision under
test checked out and this evidence set rotating in the working tree; the
wave lands as one integration commit carrying the rotated files, so the
stamp is one commit behind the file's own commit — the same limitation
recorded by earlier waves and the reason the binaries are identified by
SHA-256 in the staged ledger, not by an embedded revision.

Current evidence regenerated by the wave-19.26 full-tier closure battery
and the native Windows leg at revision
`38aea8fc5777d1cf985ab2e53e7a66e503f59469`
(product identities in the identity block below).  The paragraphs that
follow record the product defects repaired along the road to this
revision; each remains true of the current tree:  Wave 19.23 repairs the product defects
the eight-role review round reported at the wave-19.22 final
revision `cfbee788` (verdict FAIL from every role: tester,
operations, parity, portability, security, performance, glm,
closure):

- Go metadata source read (parity P0; tester/glm/performance P1):
  `readMetadataFile` issued a single `Read` sized by `stat`,
  discarded the returned count, and committed fabricated bytes
  (NUL padding or silent truncation) for procfs/sysfs sources.  It
  now opens `O_RDONLY|O_NONBLOCK` through a dedicated metadata open
  owner and loops to EOF, appending only bytes actually read and
  enforcing the 20 MiB cap against bytes read
  (`v4/go/internal/cli/handlers/lifecycle_facts.go`,
  `metadata_open_unix.go`, `metadata_open_windows.go`).  Verified
  byte-identical to Rust for `/proc` and `/sys` sources and for
  metadata get/replace_file round-trips.
- Regular-to-FIFO swap wedge class (operations/parity/portability/
  glm/performance P1): every remaining caller-path open opens
  `O_NONBLOCK` and judges the opened descriptor inside the open
  owner, refusing with the arm-exact class byte-identical to that
  arm's pre-check.  Go owners:
  `v4/go/internal/cli/fileio/{opened_regular,input_open_unix}.go`,
  `v4/go/internal/cli/handlers/{opened_regular,metadata_open_unix,csv_open_unix}.go`
  (`csv_open_unix.go` is under `handlers/`, not `fileio/`); Rust: one owner
  `v4/rust/iprange-cli/src/io/caller_open.rs` covering the text
  input, `@file-list`, direct-CSV, and metadata arms.  Every swap
  race now answers promptly (hangs=0 on all race arms), strace
  confirms `O_RDONLY|O_NONBLOCK|O_CLOEXEC`, and harness self-tests
  prove the pins fail when the flag is removed.
- Refusal-class parity (closure F1; parity/security P1-P2): Go
  aligned to the Rust authority on the 22 divergent (arm, path-kind)
  combinations — symlinked mapping sources, quiescent recovery and
  validate rw opens through the retained parent directory
  (`v4/go/internal/live/open_retained.go`,
  `recovery/source_open_*.go`), `publication.resolve` missing
  ancestor, directory output destinations via raw `rename(2)`
  (`v4/go/internal/cli/fileio/rename_unix.go`,
  `rename_windows.go`), refresh `data.outcome` `not_started`, and
  `-32602` refusal of `writer_budget.max_open_files = 0` on every
  writer method (`handlers/writer_budget_zero_test.go` pins).  Rust
  changed on one arm under recorded user ratification: the quiescent
  validate live-sidecar open folds to
  `live_recovery_coordination_unavailable`
  (`v4/rust/iprange-livedb/src/validation/source.rs`).  The
  closure class sweep reports DIVERGENCES: 0.
- Legacy CLI `@directory` classification (parity P1):
  `v4/go/internal/cli/legacy/parse.go` classifies entries with
  `os.Stat` (follows symlinks) as the C reference and Rust do, so a
  symlinked regular file inside an `@directory` is no longer dropped
  from the merge.  Committed case `tests.d/101-directory-symlink-
  input`; the then-full `tests.d` passed 101/101 for both engines
  (the suite is 121 groups at this writing).
- Corpus and gate gaps (tester P1/P2, security P2, closure F2/F6):
  eleven new committed cases — `cases/params.negative.*` (five,
  including the writer-budget refusals and grammar),
  `cases/metadata.replace_file.{direct,replace,publish}` and
  `cases/mixed.metadata-replace-file` (byte-exact metadata
  persistence on all three publishers plus the mixed direction),
  `cases/input.expand_at_paths`, and
  `cases/mixed.export-cross-language` (consumer exports the
  producer's artifact, digest-compared) — and two new committed
  gates: `v4/cli/check_fifo_surface.py` (17 arms x 2 engines,
  self-test 18 cases) and `v4/cli/throughput_harness.py` (self-test
  10 cases).  The kind-coverage gate now iterates the full
  `REQUIRED_OPENED_KINDS` (`adapter_output` and `metadata_delivery`
  carry both-language open evidence), every harness report carries
  `git_head`, and the known-defects ledger
  (`v4/cli/evidence/known-defects.json`) is enforced in both
  directions.  It was reported empty here; at the wave-19.23 binaries the
  Go matrix had 4 undeclared FAIL rows, so the empty-ledger claim did not
  match the evidence.  See `## Declared engine defects`.  The `schema/results.py`
  `_self_test()` no longer raises, restoring the README claim that
  every `schema/` module ships a self-test.
- Windows native re-qualification (portability F5, closure F4):
  `windows-guard.json` and `windows-housekeeping.json` regenerated
  natively on the authorized Windows validation host from the
  wave-19.23 tree (go1.26.5, rustc 1.97.1-msvc, harness CPython
  3.14.6 mingw64); `GOOS=windows go vet ./...` is a committed gate
  in `check_goos_matrix.sh`; `samefile_test.go` was made
  Windows-safe (worker path gains the `.exe` suffix under
  `runtime.GOOS`, pinned readers close before temp-dir cleanup).  The
  claim made here that the native Windows Go suite ran green was wrong:
  `windows-guard.json` in this directory records 3 `samefile_test.go`
  failures in its `build_provenance.native_go_test`, and the battery built
  from a `go-stage` copy that predated the committed fix.  See the
  Windows status paragraph in this file's head block.

Fresh identities (wave-19.23, Linux, measured from one clean
battery): go product
`d791b1d2e73493fa546ae06873a7c781d5ffc5978ff9e9924f7a7cacc6f533f4`
(rebuilt with `-trimpath -buildvcs=false`), go worker
`6442c2598e88df5e4f3c2537cd69a5bdad36bfbcc4d2f5c0d9a9151cb3bb9f86`
(rebuilt with `-trimpath -buildvcs=false`), rust product
`47679f559a84960c2911cf402f9e71b90b7fe434e25a3476b96fff44857f2753`,
rust worker
`e72d7d4578cc47536bc4b36b4d1640be8e118c8bc3e03abac58149ca21f3979f`,
rust fixture
`a37312882baf78ff18230619b61d2b7766921ab6ef8ed131fc7f3a73f858a3b2`
(rebuilt from the fixed tree with a fresh `CARGO_TARGET_DIR`).
Windows identities regenerated natively on the authorized host: go
product
`45f388b2238e91482b75d58f63a40d6b76b5cf9bb9c1d4db9272c2cadfaf4026`,
go worker
`ea0e974c6d81d7cb423d7c52217d117502a5b6c4c555373c86f880cab6836708`,
rust product
`69962ed19fa836cb6d3d8d087e79fa24dbeeea69d98e7e47c52db6e01cd71933`,
rust worker
`9df3efac61655ff005e368913301f90d69d78473251a02e76a2df36e5f505061`,
fixture tool
`99223976f3df5d0ba08a7787824e76cc70b4c0abd8142ba45caf7261ce4da37c`,
fixture database
`1e4a316e445a3db003ae483e1cd9f98f37819274ca3ab5999f16e3f827dc0a8f`.

Battery (all steps rc 0, run under `nice`): Go module tests rc 0
(24 packages); Rust workspace tests rc 0 (fresh
`CARGO_TARGET_DIR`); GOOS matrix 7 PASS / 1 SKIP (dragonfly/arm64
unsupported by the installed Go toolchain) plus
`windows/amd64 vet ./...` PASS; matrices rust 49/49, go 49/49,
rust_to_go 25 PASS + 24 skips, go_to_rust 25 PASS + 24 skips; crash
16/16 scenarios in both directions with both `/bin/false` negatives
rejected (0 passed, 16 failed each); resource proofs 8/8 with the
harness self-test PASS; throughput PASS (busy-reply median 15,394.0
replies/s for go with 17 clone calls and 11/13 unique child tids at
3,000/6,000 requests, 45,751.7 for rust with 4 clone calls and 4 unique
child tids; attestation, not a threshold); golden 55 exchanges / 71 case files;
sensitivity gate 14/14; guard POSIX negative control PASS for both
products; kind-coverage gate PASS on the fresh reports; FIFO
surface gate PASS (17 arms x 2 engines) with its self-test PASS
(18 cases); refusal-class sweep DIVERGENCES: 0; regular-to-FIFO
swap races hangs=0 on every probed arm; legacy `tests.d` 101/101
for both engines; forgery battery PASS on genuine evidence; Rust
source graph complete (506 sources);
`.local/shared/binaries/SHASUMS.txt` re-verified (11/11, including
the native Windows builds).

Identity note: the battery executed against the wave-19.23 working
tree at `cfbee788` (the wave-19.22 final revision plus this wave's
batch), so each report embeds `git_head=cfbee788`; the product
source of this evidence is this wave's single integration commit,
which the eight-role re-anchor round reviews exactly.

Previous wave blocks below (wave-19.22 and earlier) remain part of
the historical record; they are not superseded, only
superseded-in-position by this head.

Current evidence regenerated at the wave-19.22 final revision
(SOW-0028 "Wave 19 round 19.22", 2026-09-12; product identities in
the identity block below).  Wave 19.22 repairs the product defects
the eight-role review round reported at the wave-19.21 final
revision (FAIL: tester, operations, parity, portability,
performance, glm, closure; security PASS):

- Go mapping-owner FIFO wedge (operations/performance/portability,
  P1): the mapping owner's `openNoFollow` opened without
  `O_NONBLOCK`, so a FIFO swapped in between the pre-stat and the
  open wedged every mapping-based arm (`reader.open`,
  `database.info`, `database.metadata.get`, `validate.live`,
  `recovery.inspect` live, `export`, `OpenLiveReader`).  It now
  opens `O_NONBLOCK` and refuses non-regular files through the
  authoritative opened fd with the arm-exact refusal class per
  Rust (read-only `invalid_argument` via `require_regular_file`,
  read-write `wrong_state` via `open_rw`).  The same class was
  applied to the snapshot destination probe, the feed input open,
  and the direct CSV input open (`v4/go/internal/mapping/`,
  `snapshot/snapshot_unix.go`, `cli/fileio/input*.go`,
  `cli/handlers/{live,feeds}.go` open helpers).
- Go `readMetadataFile` FIFO wedge (operations/glm, P1): the Go
  metadata reader now mirrors Rust `read_file_exact` exactly
  (stat before open; `invalid_path` for not-found and non-regular,
  `invalid_path` again if the path disappears between stat and
  open, `invalid_argument` over the 20 MiB cap)
  (`cli/handlers/lifecycle_facts.go`).
- Go validation-layer parity (parity, P1, found by the FIFO
  surface probe): the Go validators for `direct.replace` and the
  retention refresh methods called the full decoders, which read
  the metadata replace_file during validation, so a FIFO or
  missing replace_file surfaced as `-32602 invalid_params` instead
  of the handler domain error `-32010 invalid_path` (Rust
  validators are schema-only).  The Go validators are now
  schema-only, mirroring Rust; the metadata reads stay in the
  handlers (`cli/handlers/live.go`, `cli/handlers/feeds.go`).
- Go recovery quiescent-arm parity (parity/glm/closure, P2): the
  Go recovery read-write arm now maps non-regular refusals to
  `wrong_state` like Rust `open_rw`; the Rust `open_rw` FIFO pin
  was added (`live_namespace.rs`).
- Records corrections (tester/parity/closure, P2): wave-19.21's
  written "18/18 `invalid_argument`" claim is corrected (17
  `invalid_argument` + the Rust `recovery.inspect` offline arm
  `wrong_state`, all `-32010`, all prompt); the probe is rewritten
  with a per-arm expected-code table covering the offline and
  metadata arms.

The FIFO surface probe (12 arms x 2 engines, per-arm expected
`data.code`) PASSes on the fresh binaries: 16 `invalid_argument`
(read-only arms), 6 `wrong_state` (Go+Rust offline quiescent arms),
2 `invalid_path` (direct.replace metadata replace_file); all
prompt `-32010`, the regular-file controls unaffected.

Fresh Linux battery at this revision (all steps under
`nice`): Go module tests rc 0 (24 packages); Rust workspace tests
rc 0 (from a fresh `CARGO_TARGET_DIR`, because the shared
incremental target had produced a stale `iprange` binary earlier);
GOOS matrix 7 PASS / 1 SKIP (dragonfly/arm64 unsupported by the
installed Go toolchain); matrices rust 38/38, go 38/38,
rust_to_go 14 PASS + 24 skips, go_to_rust 14 PASS + 24 skips;
crash positive 16/16 scenarios in both directions with both
`/bin/false` negatives rejected (rc 1, `failed: 16`); resource
proofs 8/8 with the harness self-test rc 0; golden exchanges 55 /
38 case files; sensitivity gate 14/14; guard POSIX negative
control PASS for both products; the kind-coverage gate PASS on
the fresh reports; the FIFO surface probe PASS (24 arms + 2
regular controls, all prompt `-32010`, per-arm expected codes);
`.local/shared/binaries/SHASUMS.txt` re-verified 10/10.

Fresh identities (wave-19.22, Linux, measured from one clean
battery): go product
`4fd67c3ae91c9b36c3134a8a3b4418806c2e4cc5d5953f33cca177ee3870f66d`
(rebuilt with `-trimpath -buildvcs=false`), go worker
`94d115abb0782a65b9817f4173689e6400417b18a95029cc00063c8a36f54095`
(rebuilt with `-trimpath -buildvcs=false`), rust product
`2c2dd942079646f2a8c7c7bf4c4510cab34a22a9560688420177714ed58008ab`,
rust worker
`e81c56fb47b8751e49f7ab5583c48ce2f2e9f5b53ec47b749dccf72ced342b82`,
rust fixture
`e130971f3ee80434d382cb70ec9499dc7e21ac3ebe9fea3d8a40839c49a6e7d8`
(rebuilt from the fixed tree with a fresh `CARGO_TARGET_DIR`).
Windows identities are unchanged from the wave-19.19 record
(native host not re-run in this wave).

Previous wave blocks below (wave-19.21 and earlier) remain part of
the historical record; they are not superseded, only
superseded-in-position by this head.


Current evidence regenerated at the wave-19.21 final revision
(SOW-0028 "Wave 19 round 19.21", 2026-09-12; product identities in
the identity block below).  Wave 19.21 repairs the one product
defect (P1) found by the glm-5.3 whole-milestone validator at the
wave-19.20 final revision `9c111954`/`ff5bc3f9`, plus the record
defects the eight-role round reported as records-only FAIL:

- Go FIFO hang (glm P1): `validate`, `recovery.inspect`, and
  `recover` blocked forever on a FIFO database path because the
  read-only open waited for a writer and wedged the Go session's
  single worker goroutine; `cancel` could not unblock it.  Both Go
  unix open helpers (validation and recovery) now open with
  `O_NONBLOCK` and refuse non-regular files through the
  authoritative opened fd with the invalid-argument SDK error for
  the read-only arms, mirroring Rust `open_read_only`/
  `require_regular_file` (`v4/go/internal/validation/
  source_open_unix.go`, `v4/go/internal/recovery/
  source_open_unix.go`).  Pinned by
  `TestOpenReadOnlyFifoIsRefusedWithoutBlocking` and
  `TestOpenSourceFifoIsRefusedWithoutBlocking` (both arms).  A
  cross-binary surface probe on the fresh executables (nine
  user-path arms x both engines) now refuses instantly with
  `-32010` rc 0 on all 18 combinations — 17 with
  `invalid_argument` and the Rust `recovery.inspect` offline arm
  with its canonical `wrong_state` — including the three Go arms
  that hung at the wave-19.20 revision; regular files are
  unaffected (O_NONBLOCK is ignored).
- Records corrections (tester/portability/security/performance/
  closure at the wave-19.20 anchor): the closing record mixed two
  busy-flood probe shapes into one "1.8x" claim and the same-probe
  figures (Rust 85,709 vs 45,703 replies/s, 1.88x; Go flat at
  ~72k) were restated at `ff5bc3f9`; wave-19.20 and wave-19.21
  state paragraphs now sit in the SOW status area; the wave-19.20
  finality sentences name `ff5bc3f9`; scratch-root paths were
  removed from the SOW wave-19.20 section and from this README's
  wave-19.20 prose (raw probe and trial logs remain in the
  wave-19.20 battery scratch root, not committed).

Fresh Linux battery at this revision (all steps under `nice`; Go
module tests 24 packages rc 0; Rust workspace tests rc 0; GOOS
matrix 7 PASS / 1 SKIP dragonfly/arm64); matrices rust 38/38, go
38/38, rust_to_go 14 PASS + 24 skips, go_to_rust 14 PASS + 24
skips; crash positive 16/16 scenarios in both invocation labels
with both `/bin/false` negatives rejected (rc 1); resource proofs
8/8 with the harness self-test rc 0; golden exchanges 55 / 38 case
files; sensitivity gate 14/14; guard POSIX negative control PASS
for both products (8 expected-allowed case keys + 3 facts per
product, zero refusals, zero Go<->Rust mismatches); the
kind-coverage gate PASS on both the fresh reports and the rotated
committed evidence; the forgery battery PASS (every falsification
class rejected); `--self-test` PASS; SHASUMS.txt verifies 10/10;
the FIFO cross-binary surface probe and the free-lock full-pipe
probe self-exit on both engines (full pipe: rc 1, `WEDGED=`
empty).  Evidence JSONs were regenerated in the wave-19.21 battery
scratch root and rotated into this directory; all recorded binary
paths stay under the authorized scratch root of that battery.

Fresh identities (wave-19.21, Linux, measured from one clean
battery): go product
`124ed9f7553bf462a6d51f4e77a3746b4fe84d6a52f5c187c5fa389d9d59de31`
(rebuilt by this wave with `-trimpath -buildvcs=false`), go
worker
`1fff6a3a63d4f9cf2d26b0490f6d167c9893b2645388ad4abbb6ba4544b541f5`
(rebuilt), rust product
`8ce0cd6eae34d813417c3e7d95a801fb4a9339a313724a0f18539230acc372a2`,
rust worker
`169ec999ca44d98c78acf3aa78565111874fa16424878f8a067b9e6158d33d12`,
rust fixture
`85e00d616b7fcefeb57d8bc01313b9d0d28e50d1005c75296173b473d2efb0d1`
(unchanged by this wave).  Windows identities are unchanged from
the wave-19.19 record (native host not re-run in this wave).

Previous wave blocks below (wave-19.20 and earlier) remain part of
the historical record; they are not superseded, only
superseded-in-position by this head.

Current evidence regenerated at the wave-19.19 final revision
(SOW-0028 "Wave 19 round 19.19", 2026-09-12, pushed to
origin/master; product identities in the identity block below).
Wave 19.19 repairs the free-lock fast path of the "bounded"
session-loop reply write in both engines: with the writer lock free
and the stdout pipe full undrained, the next session-loop reply
(-32001, envelope error, busy, all-rejected batch, unanswerable)
previously blocked the session loop forever ahead of shutdown
(wave-19.18 P1; FAIL from operations, portability, security,
performance, and the glm-5.3 whole-milestone validator).  Every
session-loop reply is now delivered from the detached bounded path;
the Rust final-drain deadline also reports a worker transport
failure recorded before wedging instead of dropping it.  Full
record: the wave-19.19 section of SOW-0028.  The wave-19.18
GLOBALROOT repairs summarized below remain part of this evidence
set.  Identities below are freshly measured at this revision; the
wave-19.18-recorded Linux rust product identity was a cached
pre-fix artifact (see the identity note).  That gate now covers the
whole device-namespace family case-insensitively, its literals are byte-verified single-separator
raw strings, and the family/probe tables are pinned by
platform-independent unit tests so a broken literal or a
case-sensitive comparison fails Linux CI instead of surviving to a
Windows host run:

- The wave-19.17 harness case
  `relative_source_unc_loopback_sidecar` spelled its destination
  with a single leading backslash (`\localhost\C$...`), a rooted
  relative path under `C:\localhost\...` that is a genuinely
  distinct destination; the published `io`/`read_only_failure`
  observation was the OS "path not found" error on that distinct
  path, not a guard miss.  Fresh-process probes and in-session
  sequences with the real UNC spelling
  (`\localhost\C$...`) refuse `invalid_argument`/`not_started`
  10/10 in both engines.  The harness literal now carries the same
  four-source-backslash spelling as the other loopback-UNC cases.
- The NT device-root namespace
  (`\\?\\GLOBALROOT\\Device\\HarddiskVolumeN\\...` and its
  `\\.\\GLOBALROOT` and `\\??\\GLOBALROOT` twins) names the same
  real files as the drive-letter spelling, but Go's `EvalSymlinks`
  cannot walk the intermediate `\\Device` component, so the split
  walk reported `ok=false` and publication delivered metadata over
  the live sidecar while Rust refused canonically (wave-19.17
  security P1, fourth recurrence of the over-the-sidecar
  destructive class).  `canonicalSplitPath` now falls back to an
  `os.Stat` existence proof on the probe when `EvalSymlinks` fails
  and the probe is GLOBALROOT-family: `os.Stat` opens the same
  spelling the publication path opens, and the ancestor arm
  compares the kernel identity of the existing ancestor, so the
  guard refuses the spellings in Go exactly like Rust.  The
  fallback is gated by `globalrootFamilyProbe`, which compares the
  three namespace heads (`\\?\\GLOBALROOT\\`,
  `\\.\\GLOBALROOT\\`, `\\??\\GLOBALROOT\\`) with
  `strings.EqualFold` and requires the trailing separator, so the
  NT namespace's case-insensitive resolution is matched and the
  look-alike device component `\\?\\GLOBALROOTX\\` stays
  outside the gate (wave-19.18 parity P1: the case-sensitive gate
  let all-lowercase `globalroot` spellings deliver metadata over
  the live sidecar while Rust refused; native probes on the authorized Windows validation host refuse
  all six spellings on both engines).  An ungated form regressed
  the relative symlink-plus-".." canonicalAbsolute pin (wave-19.17
  portability P1), which is restored and pinned by the same
  committed corpus.  Pinned natively by
  `TestRefuseOutputOverSourceWindowsGlobalrootSidecar` (guard +
  session call site, six prefix spellings: three heads in
  canonical and all-lowercase form), platform-independently by
  `TestGlobalrootFamilyProbe`, and by the harness cases
  `globalroot_sidecar` and `globalroot_device_sidecar`.
- The NT namespace resolves the verbatim and object-manager UNC
  heads (`\\?\\UNC\\`, `\\??\\UNC\\`) case-insensitively,
  so `windowsUncProbe` presents those spellings to the walk in the
  ordinary form under `strings.EqualFold` (wave-19.18 parity P1:
  the case-sensitive `CutPrefix` gate let lowercase
  `\\?\\unc\\localhost\\...` bypass the rewrite and deliver
  metadata over the live sidecar while Rust refused).  Pinned by
  `TestWindowsUncProbeCaseFold` (platform-independent; the rewrite
  runs on every platform) and by the lowercase loopback-UNC rows of
  the native sidecar-spelling suite.
- The `6de5b630` Go same-ancestor anchor fed both spellings through
  `canonicalAbsolute`, whose extended-length prefix strip turns a
  verbatim or volume-GUID absolute destination into a bare relative
  path re-anchored at the drive root; the volume-GUID sidecar
  spelling regressed (allowed).  The anchor is narrowed to raw
  spellings only (Rust `canonical_split` parity:
  `filepath.IsAbs` + `pathname.Push`, no strip), restoring the
  volume-GUID arm; pinned natively by
  `TestRefuseOutputOverSourceWindowsVolumeGuidSidecar` and by the
  harness `volume_guid_sidecar` case.

Re-qualification at the final wave-19.18 tree (fresh Linux battery
on the rebuilt Go and Rust products; native runs on the rebuilt
executables on the authorized Windows validation host):

- Linux battery (go1.27.0 / rustc 1.91.1, clean staging): go tests
  green (24 packages), Rust workspace 918 passed 0 failed, guard
  selftest + POSIX negative control PASS, matrices rust 38/38 /
  go 38/38 / rust_to_go 14 PASS + 24 legitimate skips /
  go_to_rust 14 + 24, crash positive 16/16 both directions and the
  /bin/false negative control fails as designed (rc 1), resource
  proofs 8/8 with self-test controls PASS, golden exchanges 55 /
  38 case files, sensitivity gate 14/14, kind-coverage gate PASS.
- Windows native (authorized Windows validation host): at HEAD
  `b9e49132` the native go-test re-run measures 70 PASS / 0 SKIP
  with exactly the three documented host-environment failures
  (msys-`TMPDIR` worker-spawn `%PATH%`; two immutable-file-lock
  `TempDir` cleanups), the extra PASS being the wave-19.18 pin
  `TestWindowsUncProbeCaseFold`.  The Windows-gated GLOBALROOT pin
  PASSes for all six spellings, and the lowercase refusal matrix
  (six GLOBALROOT plus six lowercase UNC/volume-GUID spellings)
  REFUSED on both engines through the production JSON-RPC surface
  (attestation verified by the review-sandbox native probes
  `.local/parity/w1920/` and `.local/security/w1918-win/` and
  pinned by the committed unit/native tests).  Final-wave native
  qualification (2026-09-12, mingw64 CPython 3.14.6): guard
  harness PASS for both products (50 case keys per product: 44
  canonical refusals + 3 allowed controls + 3 success-fact
  booleans, matching the committed `windows-guard.json`),
  housekeeping PASS (`windows_qualified=true`, `skipped=false`,
  `failed=0`), and both harness self-tests exit 0 on the host and
  on Linux.

Linux identities at the final wave-19.19 revision (fresh battery,
go1.27.0 / rustc 1.91.1, Go built with `-trimpath` and
`-buildvcs=false`, all five binaries from one clean battery build):
go product
`7e6af62bdd3913c664cb71075f4ceab04ccc89b4d2b5131b07fb8ed31927c334`,
go worker `f4af92048e612b9e413b5009d98bd4771eb89b4807ef2d50fe54ef207e641e4b`,
rust product `bdbf10d8d13a51c6424cc8287ab3824352d869b22c4fd82f935d3b73e86efd25`,
rust worker `cfe604d262f8ea871ca56c21bc54f390e92945695ffdaadf7c4f302ed32e7824`,
rust fixture `e071f3cd849f62db1abe3fbfda8ce15596a5c4d2a828ba5cedffbcf7337c10e7`.

Windows identities at the final wave-19.19 revision (measured on
the authorized Windows validation host, prose-recorded per user
decision 2 of 2026-09-11): go product
`1c0297bbee18a06685e06e4d652b8eacd893d4b1773d2850a5a7e9bce4c69fad`,
go worker `9b9e2658bb2aedbad3a2bbf78760c20ffd7860416a24278b46eff46760b6ec39`,
rust product `4c4f20b854bbaec0db4004b48ad6185ca39e6e8f59388d7182f70b453e59272f`,
rust worker `0100c4252ca851707e3be855e1789d098b9a008bb80c3174ea223015ed1927cb`,
fixture `898b1c84f0f128b9007c2d1b69d88885c32e9a8d13b16672645f93fc728ebc2c`;
fixture database created natively by the fixture tool sha256
`d1d0275be06736535d8f63e3231f29de5067b838ee6d749f9c58777265486353`.

Identity note: the Linux Rust product identity embeds absolute
build inputs (standard cargo release build, no
`--remap-path-prefix`); fresh builds are deterministic for a given
source-checkout path and toolchain, but a different checkout path
or environment yields a different hash, so byte-identity claims are
limited to the exact recorded build inputs.  The Linux Go
product and worker reproduce byte-exactly from any clean staging
only when built with `-trimpath`; without it they embed the staging
path (verified in the 2026-09-12 repair wave: two byte-identical
clean stagings built without `-trimpath` produced different hashes,
with `-trimpath` byte-identical).  The final-wave Linux Go
identities `7e6af62b…`/`f4af9204…` were built from a clean staging
with `-trimpath` and reproduce byte-exactly; they supersede the
earlier `d7973a90…`/`f4af9204…` (wave-19.18) and the
`e2377a2c…`/`ee213ca1…` values (built without `-trimpath` from the
live tree, not re-derivable from an arbitrary clean staging).  The
Rust worker and the fixture reproduce from any clean staging.  The
wave-19.18-recorded Linux rust product identity `79fd1cd0…` was a
cached pre-fix `[[bin]]` artifact: the Linux battery's Rust step
previously ran `cargo build --release --examples`, which rebuilds
examples but not `[[bin]]` targets, so the release bins were
silently re-staged from older builds.  The wave-19.19 battery builds
with `cargo build --release --bins --examples`; the rust product at
this revision is `bdbf10d8…`, differing from `79fd1cd0…` exactly by
the wave-19.19 session fix, and the free-lock full-pipe probe on the
fresh binaries self-exits on both engines (no wedge).  The
Windows Go executables embed their build
directory (no `-trimpath`), so their identity is tied to the exact
staging directory of the recorded build (verified by dependency
closure: the Windows worker links zero `cli/handlers` packages, so
its hash shift between rounds is partly the staging-path artifact,
partly the repair-wave code changes to the session loop).

### Host interpreter robustness (F1, 2026-09-12)

mingw64 CPython 3.14.6 on the authorized Windows validation host
reports `os.name == "nt"` while `os.sep == "/"`, and its patched
`ntpath` emits forward slashes; `command_sanitize.py` previously
used `os.sep == "/"` to discriminate POSIX, so on the host both
branches fired, verbatim/UNC/device spellings escaped detection,
and the POSIX-only pins misfired (18 host self-test failures).
`command_sanitize.py` now derives `_IS_WINDOWS = os.name == "nt"`
and `_IS_POSIX = (os.sep == "/" and not _IS_WINDOWS)`, gates every
doubled-leading-separator rule (checkout-root collapse,
`_effective_absolute`, `_resolve`, `_checkout_suffix`) on
`_IS_POSIX`, and canonicalizes Windows spellings independently
(`_fold_windows` folds `/` to `\` and lowercases before
device-prefix stripping and profile matching; `_self_test` kernel
resolution is POSIX-gated because mingw64 collapses `link/..`
lexically before `stat`/`realpath`).  The four POSIX-only pins in
`windows_housekeeping_harness.py` are gated on `os.name != "nt"`.
Both harness self-tests now exit 0 on the host and on Linux.
GLOBALROOT discovery on the host reports the same volume-GUID
destination as the committed evidence
(`{6df78126-8d52-4afa-ac58-1b1925131887}`).

The final-wave native guard report and the POSIX negative control
`guard-posix.json` were regenerated from the wave fixture database
(sha256 `9ad6279b79810f19f623475e55a4e7f58f612daf850c3c7ccdb02c18f4652b88`,
16 KiB) so that every Linux report in this wave shares one fixture
identity.

### Building the Go binaries (corrected 2026-09-12)

The module root of `v4/go` is the library `iprangedb`, not a main
package.  Both `CGO_ENABLED=0 go -C v4/go build -buildvcs=false`
(no package argument) and the multi-package form
`go build -buildvcs=false ./cmd/iprange ./cmd/iprange-v4-worker`
exit 0 writing NO executable (multiple packages are compiled and
discarded) — an auditor following either recipe can hash a stale
binary and "confirm" an identity that was never built.  Build one
main package per invocation, or direct both into a directory:

```bash
CGO_ENABLED=0 go -C v4/go build -buildvcs=false ./cmd/iprange
CGO_ENABLED=0 go -C v4/go build -buildvcs=false ./cmd/iprange-v4-worker
# outputs: v4/go/iprange and v4/go/iprange-v4-worker

CGO_ENABLED=0 go -C v4/go build -buildvcs=false -o ./bin/ ./cmd/iprange ./cmd/iprange-v4-worker
# outputs: v4/go/bin/iprange and v4/go/bin/iprange-v4-worker
```

Long-term-best recipe (reproducible from any clean staging):

```bash
CGO_ENABLED=0 go -C v4/go build -trimpath -buildvcs=false ./cmd/iprange
CGO_ENABLED=0 go -C v4/go build -trimpath -buildvcs=false ./cmd/iprange-v4-worker
```

`-trimpath` removes the staging path from the binaries; two
byte-identical clean stagings then produce byte-identical binaries,
while without `-trimpath` each build embeds its own staging path (a
warm shared build cache can mask the divergence by serving cached
artifacts, so identity checks must use a cold full build).  The
qualification re-run uses the `-trimpath` recipe; the resulting
hashes differ from the pre-repair identities, so SHASUMS.txt and the
evidence hashes are re-recorded by that wave (not edited here).

All recorded hashes are the measured values from the canonical
staging (portability-role verification and lead reproduction,
recorded with wave-19.17).

Previous wave blocks below remain part of the historical record;
the wave-19.16, wave-19.15, wave-19.14, and earlier blocks are not
superseded, only superseded-in-position by this head.

Current evidence regenerated after the wave-19.16 cross-family
guard completion (SOW-0028 "Wave 19 round 19.16", 2026-09-11).  The
guard round that followed the wave-19.15 record closed the remaining
namespace-spelling arm of the destructive same-source class and
corrected the Rust cross-family pin to the real NT object-manager
spelling.  Product source revision `e576d4e8` (pushed, origin/master):

- Loopback-UNC and volume-GUID namespace spellings
  (`\\localhost\C$\...`, `\\127.0.0.1\C$\...`,
  `\\?\UNC\localhost\C$\...`, `\\?\Volume{...}`) name the
  same real file as the drive-letter spelling, resolved by the
  kernel, while the canonical identities stayed in the caller's
  namespace and never matched: `metadata.get` published over the
  absent live sidecar and made the source unreadable (third
  recurrence of the wave-19.9/19.14 destructive class).  Both
  engines now add the same-ancestor arm: the deepest EXISTING
  ancestor of both spellings is the same real directory with one
  kernel file identity (volume serial + file index), which no
  lexical mapping can forge; refuse when the ancestor identities
  match (`530b7548`).  The Go probe re-spells the verbatim-family
  UNC prefixes (`\\?\UNC\...` and its NT object-manager twin
  `\??\UNC\...`) as `\\server\share` because EvalSymlinks
  cannot open the verbatim "server" prefix alone (`3540edc9`).
  Pins: five native harness cases (`unc_loopback_sidecar`,
  `unc_loopback_ip_sidecar`, `verbatim_unc_loopback_sidecar`,
  `nt_unc_loopback_sidecar`, `volume_guid_sidecar`) plus Go/Rust
  unit tables.
- The Rust cross-family pin built its native path with two leading
  backslashes (`\\??\UNC\...`), which Windows parses as a UNC
  server named `??` that resolves to nothing; the real NT
  object-manager spelling has ONE leading backslash
  (`\??\UNC\...`).  The native Windows run proved both release
  products refuse the real spelling; the pin row now builds the
  same spelling as the harness, the Go pin, and the product probe
  (`e576d4e8`).  Product code was not changed by this fix.

User decision 2026-09-11 (decision 2): the Windows native evidence is
recorded with the actual measured binary identities in prose (the
harness JSONs carry the per-product sha256 inline while
`build_provenance` stays null) without a `--provenance` re-run,
matching the wave-19.15 recording practice.

Product source revision `e576d4e8` (pushed, origin/master).  Linux
toolchain go1.27.0 / rustc 1.91.1 stable (Go product and worker one
main package per invocation with
`CGO_ENABLED=0 go -C v4/go build -buildvcs=false ./cmd/iprange` and
`CGO_ENABLED=0 go -C v4/go build -buildvcs=false ./cmd/iprange-v4-worker`,
writing `v4/go/iprange` and `v4/go/iprange-v4-worker` — the
package-less module-root recipe and the multi-package form both exit
0 writing nothing, see the build note below; Rust product,
worker, and fixture with `cargo build --release --all-features` on
`iprange-cli`, `iprange-livedb`, and the `v4-fixture` example);
Windows toolchain go1.26.5 windows/amd64 / rustc 1.97.1 on the
authorized validation host, built from a clean tree at `e576d4e8`.

Linux identities at `e576d4e8`:

- go product `ad402735a5009eb255a3e2385138e3d2c3fdbc948170399cd3338d6a29b6b241`, worker `ee213ca1eb4e008f5e446b6ad0f56ddbb09bb63a75dfd1c3802241f735de6ea0`
- rust product `e59c0f08bc58bf9a95d841e0acb5d29d6d873a984c219bb8d2dce7f18f855a77`, worker `d7a599886eaecccbef00f0a683e2c481d52c2a24352c533b3f7ad5c2d257775e`, fixture `24401226902e2050d9377322649758c86826ab9290298185c3c4abba3b5e0637`

Windows identities at `e576d4e8`:

- go product `850e70ec61d2ef306f55cf6e26ce8600067bde417b0d8fa878a30f1c36e80de0`, worker `cf3b4d2c7b10caca80600a3f3e95f3a5efea76208018f91b763b919a3683a240`
- rust product `2d5ea513bd0f15fc6eebe86da904496f1e128267c7ec604663fd1ea966d01c87`, worker `1cf2f694e91bbbd3196fab4810d33e9e6e7c9e31091b8cda5989c20d6d695318`, fixture `570e81cdabd38fe132a84b8d07f2a7ea6256eef050d2d7765319677d074d5050`
- fixture database created natively by the fixture tool: sha256 `d7126fc04b502f3aee316ef98d192514246ec39adecf68c747b35ce43e1eabd4`

Re-qualification at the final Linux identities (fresh battery):
matrices rust 38/38, go 38/38, rust_to_go 14 PASS + 24 legitimate
skips, go_to_rust 14 PASS + 24 skips; crash positive 16/16 both
directions and the /bin/false negative control fails as designed
(rc 1); resource proofs 8/8 with all self-test controls PASS;
golden exchanges 55 / 38 case files; sensitivity gate 14/14;
kind-coverage gate PASS; operations wave-19 probe 39/39 OK; the
guard harness selftest PASS and the POSIX negative control
(`guard-posix.json`) PASS for both products.  The Go suite (24
packages) and the Rust workspace (918 passed, 0 failed) are green on
Linux.

Wave-19.19 requalification (2026-09-12, final wave-19.19 tree):
the complete Linux battery was re-run on freshly built binaries
(go `7e6af62b...`, rust `bdbf10d8...`), and the Windows native
evidence was regenerated on the authorized host (go `1c0297bb...`,
rust `4c4f20b8...`).  Results: Go module tests 24 packages rc 0;
Rust workspace tests rc 0; GOOS matrix 7 PASS / 1 SKIP; matrices
rust 38/38, go 38/38, rust_to_go 14 PASS + 24 skips, go_to_rust
14 PASS + 24 skips; crash positive 16/16 scenarios (8 producer-rust + 8
producer-go) with both `/bin/false` negatives rejected (rc 1);
resource proofs 8/8 with self-test rc 0; golden exchanges 55 / 38
case files; sensitivity 14/14; guard POSIX negative control PASS
both products (8 expected-allowed case keys + 3 facts per product,
zero refusals, zero Go/Rust mismatches); Windows guard PASS (50
keys: 44 refused + 3 allowed + 3 facts) and housekeeping PASS
(`windows_qualified=true`, `skipped=false`, `failed=0`); the
kind-coverage gate, the forgery battery, and `--self-test` PASS
on the rotated evidence; SHASUMS.txt verifies 10/10.  The
free-lock full-pipe probe on the fresh binaries self-exits on both
engines with `WEDGED=` empty.  Evidence JSONs were regenerated
under `/tmp/iprange-w1919/` and rotated into this directory; all
recorded binary paths stay under that authorized scratch root.

Windows native at `e576d4e8`: guard harness PASS for both products
with 22 report keys per product (`windows-guard.json`, all_ok true)
— 19 completed case checks including the five added cross-family
names plus 3 success-fact controls; the wave-19.15 evidence held
17 keys (14 cases + 3 facts); housekeeping PASS with
`skipped=false`, `failed=0`, `windows_qualified=true`
(`windows-housekeeping.json`, refresh flow 150 rows / 123456 /
200 rows); the Rust cross-family guard test now PASSES natively
(corrected single-backslash spelling); the Go native handler suite
(Windows / SameCanonical / Strip tables) PASSes (13 tests,
including `TestSessionMetadataGetWindowsSidecarSpellings`,
`TestRefuseOutputOverSourceWindowsSidecarSpellings`, and
`TestWindowsStripExtendedWindows`).

Measured-identity note: the wave-19.15 Linux Rust product/fixture
did not reproduce byte-exactly from the clean recipe in that wave
(document `.local/operations/report.md`); the hashes above are the
actual measured values at `e576d4e8` from the documented recipe.
The Windows go worker hash also differs from the wave-19.15 record
(the earlier Windows record predates the guard changes and was
built without the fully clean recipe; the recipe now pins a clean
tree and `-buildvcs=false`).  Records cite the measured hashes;
byte-exact reproducibility is re-verified by the closure role round
against this revision.

Previous wave blocks below remain part of the historical record;
the wave-19.15, wave-19.14, and earlier blocks are not superseded,
only superseded-in-position by this head.

Current evidence regenerated after the wave-19.15 same-source guard
completion (SOW-0028 "Wave 19 round 19.15", 2026-09-11): the
closure-role round at the wave-19.14 HEAD returned three blockers,
all repaired and re-qualified at the product source revision
`c2b2b0cf` (pushed, origin/master):

- NT object-manager spelling (`\??\C:\...\db.iprange.readers`)
  bypassed the guard in BOTH engines: the device-prefix strip tables
  covered only `\\?\` and `\\.\`, so metadata.get published over
  the absent live sidecar and made the source unreadable.  Both
  strip tables now include `\??\` (with `\??\UNC\...` mapping to
  `\\server\share`), and the Rust canonical walk presents the
  spelling as its verbatim twin (`\\?\`) before path parsing
  because Rust's Path parser does not treat `\??\` as a root
  prefix (`c2b2b0cf`).  Pins: native harness case
  `nt_namespace_sidecar`, strip unit rows in both engines.
- Forward-slash verbatim spelling (`\\?\C:/...`): Go preserved the
  slashed caller spelling through EvalSymlinks, produced a
  mixed-separator identity, bypassed the guard, and failed later at
  the kernel rename with `io`/`read_only_failure`; Rust refused
  canonically (PathBuf normalizes separators eagerly).  Go
  `canonicalAbsolute` now normalizes `/` to `\` on Windows, so both
  engines refuse the spelling with `invalid_argument`/`not_started`
  (`363a2116`).  Pin: native harness case `verbatim_fwd_sidecar` and
  the Go sidecar-spelling suite.
- Rust `windows_strip_extended` indexed `rest[..4]` on a byte
  boundary assumption: a non-ASCII head after the prefix
  (`\\?\abc\u00e9\...`) panicked the worker mid-session while Go
  could not panic, so the engines diverged on a crash.  The UNC
  head check is now char-boundary safe (`rest.get(..4)`), pinned by
  `strip_non_ascii_head_does_not_panic` (`363a2116`).

User decision 2026-09-11 (decision 2): the Go Windows create path
keeps rejecting non-Latin-1 main names (pre-existing parity gap,
tracked to SOW-0030) and the qualification continues to use
ASCII-name-then-rename; this is a recorded carve-out, not a code
claim of create-name parity.

Product source revision `c2b2b0cf` (pushed, origin/master).  Linux
toolchain go1.27.0 / rustc 1.91.1 stable (Go product and worker one
main package per invocation with
`CGO_ENABLED=0 go -C v4/go build -buildvcs=false ./cmd/iprange` and
`CGO_ENABLED=0 go -C v4/go build -buildvcs=false ./cmd/iprange-v4-worker`;
see the build note below; Rust product, worker,
and fixture with `cargo build --release --all-features`); Windows
toolchain go1.26.5 windows/amd64 / rustc 1.97.1 on the authorized
validation host.

Re-qualification at the final Linux identities (fresh battery):
matrices rust 38/38, go 38/38, rust_to_go 14 PASS + 24 legitimate
skips, go_to_rust 14 PASS + 24 skips; crash positive 16/16 both
directions and the /bin/false negative control fails as designed
(rc 1); resource proofs 8/8 with all self-test controls PASS;
kind-coverage gate PASS with fresh evidence and all self-test
controls; golden exchanges 55 / 38 case files; sensitivity gate
14/14; operations wave-19 probe 39/39 OK; the guard harness
selftest PASS and the POSIX negative control PASS for both
products (`guard-posix.json`).  The Go suite (24 packages) and the
Rust workspace (918 passed, 0 failed) are green on Linux.

Windows native at `c2b2b0cf` (go1.26.5 windows/amd64, rustc 1.97.1,
clean tree): the guard session harness (`windows-guard.json`) PASS
for both products — every absent-sidecar spelling refused
(drive_relative, drive_relative_upper, absolute_upper,
rooted_sidecar, absolute_trailing_dot, absolute_trailing_space,
non_ascii, ntfs_sigma, ntfs_final_sigma, verbatim_sidecar,
nt_namespace_sidecar, verbatim_fwd_sidecar) with
data.code=invalid_argument + outcome=not_started + the exact
message, distinct-destination controls (meta.txt, the drive-root
file) allowed with strict success-schema/digest validation, sources
byte-identical, sidecars absent, reopen fresh with the claimed
digest; housekeeping 2/2 PASS (`windows-housekeeping.json`,
skipped=False); the fixture database is created natively by the
fixture tool at the recorded revision.  The Go fold, identity,
strip, and guard-session tests PASS natively (including the new
`\??\` and forward-slash spellings in
`TestRefuseOutputOverSourceWindowsSidecarSpellings`); the Rust
strip pins PASS natively (`strip_extended_literals`,
`strip_non_ascii_head_does_not_panic`, `device_name_requires_equal_length`).
The Go Windows suite fails exactly the three documented
pre-existing host-environment tests (worker-spawn PATH and the two
immutable file-lock cleanup cases); the Rust iprange-cli suite
natively reports 309 passed / 2 failed, and the two failures are
the documented immutable file-lock cleanup class (the six
worker-spawn PATH cases pass in this environment).  The
iprange-capi `native_windows` integration test is not runnable on
the validation host: the installed toolchain does not emit the
GNU-style `libiprange_v4.dll.a` import library the test requires;
the C ABI crate is outside SOW-0028 milestone-4 scope (SOW-0017
surface).

Linux identities at `c2b2b0cf`: go product
`51b0b2b4f1b36860b15421393aefef3a7d94ea2f12ed7dd6ed75a69b713733f3` /
worker
`ee213ca1eb4e008f5e446b6ad0f56ddbb09bb63a75dfd1c3802241f735de6ea0`;
rust product
`20a43867baef341a874032bf424ee133f1a89bfe50832336e1c9eb3143fa457d` /
worker
`d7a599886eaecccbef00f0a683e2c481d52c2a24352c533b3f7ad5c2d257775e` /
fixture
`24401226902e2050d9377322649758c86826ab9290298185c3c4abba3b5e0637`.

Windows identities at `c2b2b0cf`: go product
`877be0892f80f308d714adeb2d8f3c8e03997764326af980e89adc5c97f2d68a` /
worker
`83fa0e17fd828a718b1f9b9825379d5b4c663bdf5306010f11168641d3b8525d`;
rust product
`19503794b899aae8be65457f187d97ff6cb352d3d892ee5e27f6d281aeb25780` /
worker
`1cf2f694e91bbbd3196fab4810d33e9e6e7c9e31091b8cda5989c20d6d695318` /
fixture tool
`fbaaa5f879787d8347fa499ab4ca11bd099e599f51db512808e6249e7ac6cc36`
(rebuild from `c2b2b0cf`; the wave-19.14 record value was not
reproducible from the recorded recipe);
the native fixture database created for this wave hashes to
`d7126fc04b502f3aee316ef98d192514246ec39adecf68c747b35ce43e1eabd4`.

All ten staged binaries verify with `sha256sum -c` against
`.local/shared/binaries/SHASUMS.txt` (10/10) and every evidence
JSON below embeds the identities of the executables that actually
served it.


The current evidence is regenerated after the wave-19.13 fold-parity
completion (SOW-0028 "Wave 19 round 19.13 fold-parity completion"):
the Go Windows fold now maps every rune whose go1.26.5
`unicode.ToLower` tables predate while rustc 1.97.1 applies them —
U+1C89, U+A7CB, U+A7CC, U+A7CE, U+A7D2, U+A7D4, U+A7DA, U+A7DC,
Garay U+10D50..U+10D65, and Kirat Rai U+16EA0..U+16EB8 — so both
hand-written products fold byte-identically at the shipped Windows
toolchain pair.  A full 1,112,064-rune differential enumeration on
the authorized Windows validation host (go1.26.5 x rustc 1.97.1)
reports **0 mismatches**; the enumeration probes and result procedure
are committed under `v4/cli/evidence/fold-enum/` and the Go and Rust
fold tests pin all 55 pairs.  (The earlier wave-19.13 releases
mapped only U+A7CE/U+A7D2/U+A7D4 and still diverged on the
remaining 52 runes; review FAILed on that and this round closes it.)

The rest of the wave-19.13 repairs stand: the Go same-source guard
stores numeric file identities instead of re-opening stat paths, the
two renamed-identity regressions pass natively on Windows, and the
windows-guard harness has product-interface IO deadlines.

The product source revision of the staged binaries is `9ea6becb`
(product; pushed, origin/master); the follow-up commit `233653fc`
changes the Rust fold test only.  Linux toolchain go1.27.0 / rustc
1.91.1 stable (Go product and worker with `-buildvcs=false`; Rust
product, worker, and fixture with `cargo build --release
--all-features`); Windows toolchain go1.26.5 windows/amd64 / rustc
1.97.1 on the authorized validation host.

Re-qualification at the final Linux identities: matrices rust 38/38,
go 38/38, rust_to_go 14 PASS + 24 legitimate skips, go_to_rust
14 PASS + 24 skips; crash positive 16/16 both directions and the
/bin/false negative control fails as designed (rc 1); resource
proofs 8/8 with all self-test controls PASS; kind-coverage gate PASS
with fresh evidence and all self-test controls; golden exchanges
55 / 38 case files; sensitivity gate 14/14; operations wave-19 probe
39/39 OK.  The Go suite (24 packages, fresh `-count=1`) and the Rust
workspace (918 passed, 0 failed) are green on Linux.

Windows native at `233653fc` (go1.26.5 windows/amd64, rustc 1.97.1,
clean tree): the guard session harness (`windows-guard.json`) PASS
for both products — all seven spellings refused
(drive_relative, drive_relative_upper, absolute_upper,
rooted_sidecar, absolute_trailing_dot, absolute_trailing_space,
non_ascii) with data.code=invalid_argument + outcome=not_started +
the exact message, control and reopen allowed, both sources
byte-identical, sidecars absent; housekeeping 2/2 PASS
(`windows-housekeeping.json`); the seven fold/identity Go tests PASS
natively — `TestSameCanonicalWindowsFold` plus
`TestSameCanonicalWindowsFoldUnicode16` (all 55 pairs) — and the
Rust fold tests pass natively (`same_canonical_folds_windows_case`,
`same_canonical_folds_windows_unicode16`); the Go Windows suite is
green except the three documented pre-existing host-environment
failures (worker-spawn PATH and immutable file-lock cleanup
classes).

Linux identities at `9ea6becb`: go product
`e5d26ad8e8f36f4cc10c9ee1890d27cd64639c910be5deb236ef80a30235ae2b` /
worker
`4f2eb0638f0cc9fac942f885aed4b20b3a23f388d1a1b1d0e0757594866399a7`;
rust product
`453b0ab91b9b8173bbe6d7612552fcb0569c8396d6cdbd42de0200e1fea92dba` /
worker
`4c17669de96631956d290a54a2553ddc8b9f7dcf517f81c538c844fa9dfe252e` /
fixture
`9b40420e7a72d8d0248ac07dffb842ed30ef1766df1e922bd9084e2c9c86ae91`.

Windows identities at `9ea6becb`: go product
`78419e47559d6caf55c8f4d9ec0220097396d12dfa0a68c3074a32e536989d96` /
worker
`65e75d99b2fad0b12f3af01ca486296eac1d710476eb36bf79623cd8c2fea01f`;
rust product
`68ca5446b1ae0ce22f416e0c3f549d9988069539d38fc5f4a34d603f4538c514` /
worker
`48d840ecece8dec3ab55aae619fb74840839eb142fb584dcb001aa2b04ee9c55` /
fixture
`f222a4303ce786f53c44b623c703fec69529a62b21136139371fd7c96bc0b3c6`.

All ten binaries are staged in `.local/shared/binaries/SHASUMS.txt`
(sha256sum -c OK, 10/10) and every evidence JSON below embeds the
identities of the executables that actually served it.

---
Historical wave record (superseded by the head block): the current evidence is regenerated after the wave-19.12 repair
(security-role P1/P2 findings, wave 19 round 19.12; SOW-0028 "Wave 19
round 19.12"): the same-source guard now refuses two more Windows
equivalence classes of the absent reader sidecar exactly like the
filesystem does — trailing dots and spaces in the final component,
which Win32 strips at create time so the sidecar materializes and
later opens fail, and non-ASCII case variants, which the wave-19.11
ASCII fold missed while NTFS equates them.  Both engines fold the
canonical pathname arms on Windows only: the final component's
trailing dots/spaces are trimmed (the special "." and ".." components
excluded), then both spellings are compared under Unicode full
lowercase (Rust `char::to_lowercase`; Go maps the single expanding
BMP character U+0130 to its two-rune form so both engines fold
byte-identically).  POSIX keeps exact comparison; the fold is a
documented practical approximation of the per-volume upcase table for
absent names while existing files stay protected by the OS
file-identity arm.  The product revision is `d10eb757` (pushed,
origin/master); this round's Linux evidence regeneration is staged at
that HEAD (no product change follows in this round).
Both products were rebuilt at `d10eb757`: Linux toolchain go1.27.0 /
rustc 1.91.1 stable (Go product and worker with `-buildvcs=false`;
Rust product, worker, and fixture with `cargo build --release
--all-features`).  The Go Linux product binary is byte-identical to
the wave-19.11 build (sha256 `949fa62c...`): the wave-19.12 change is
Windows-gated dead code on POSIX.
Re-qualification at the wave-19.12 Linux identities: matrices rust
38/38, go 38/38, rust_to_go 14 PASS + 24 legitimate skips,
go_to_rust 14 PASS + 24 skips; crash positive 16/16 both directions
and the /bin/false negative control fails as designed (rc 1);
resource proofs 8/8 — the evidence run and three consecutive
flake-check runs are all 8/8, and the wave-19.11 intermittent
proof-d.go timing race did not recur — with all self-test controls
PASS; kind-coverage gate PASS with fresh evidence and all self-test
controls; golden exchanges 55 / 38 case files; sensitivity gate
14/14.  The operations wave-19 probe is 39/39 OK against these
binaries (same per-case expected-outcome evaluator and
full-evaluator self-test as wave 19.11).  The Go suite (24 packages,
fresh `-count=1`) and the Rust workspace (918 passed, 0 failed) are
green.
The wave-19.12 change is Windows-gated; Windows-native
re-qualification at `d10eb757` is a separate follow-up (the staged
Windows binaries at the time of this round predate `d10eb757`), so
`windows-guard.json` and `windows-housekeeping.json` remain the
wave-19.11 (`7a193500`) artifacts.
Linux identities at `d10eb757`: go product
`949fa62c114d79171260d98300f29ada005b224d549315d72fdde5f893333d2f` /
worker
`4f2eb0638f0cc9fac942f885aed4b20b3a23f388d1a1b1d0e0757594866399a7`;
rust product
`45ba8b6fd5270a30d940464b15a2309541d1c0867c7c4895f4976d6e4f34ed3b` /
worker
`4c17669de96631956d290a54a2553ddc8b9f7dcf517f81c538c844fa9dfe252e` /
fixture
`9b40420e7a72d8d0248ac07dffb842ed30ef1766df1e922bd9084e2c9c86ae91`
(all staged in `.local/shared/binaries/SHASUMS.txt`, sha256sum -c
OK).
---

Historical wave record (superseded by the head block): the current evidence is regenerated at product revision `016010fc`
(the round-10 repair wave: destination name-rule gates and the
verbatim push fold).  The round-9 revision was re-anchored through
all seven role reviews; the portability role then FAILed with one
P1 and one P2, both independently verified and repaired:

- P1 — `rejectLiveSelf` dropped the `ValidDestinationName` gate in
  the round-9 wave (to probe the bound main-name spelling) and an
  overlong destination name surfaced the kernel error of the
  main-name Lstat as `io` (wire `code": "io"`) where Rust
  `Destination::bind` answers `name_invalid` via
  `require_name_lengths` before `open_regular`.  Repair: the
  preflight mirrors the Rust bind error order — the main-name
  component rule answers `name_invalid` before any parent access,
  the length rule answers `name_invalid` after the parent open and
  before the main-name open, and a missing parent still wins the
  class when both fail.  Pinned by `TestRejectLiveSelfNameRules`;
  live-probed: both products now answer `name_invalid`
  byte-identically for the 300-byte destination.
- P2 — `verbatimPushRebuild` appended the pushed name raw instead
  of folding its components like `PathBuf::_push`'s verbatim
  branch: a trailing separator stayed in the result, `"."` added a
  CurDir component, `".."` appended instead of popping the last
  Normal component, and `"a/b"` stayed one component.  Repair:
  CurDir vanishes, ParentDir pops the last Normal, RootDir
  truncates the buffer to its prefix, and the re-emission applies
  the disk-prefix need_sep rule; pinned by
  `TestVerbatimPushComponentRules` (37 native-rustc-derived rows,
  windows-gated) and the corrected wine oracle differential:
  WINDOWS 324/324 and POSIX 431/431, zero mismatches.

No current product call site passes a `.`/`..`/separator-carrying
name to the verbatim push, so the P2 class was latent; the P1 class
was reachable at the JSON-RPC snapshot surface and is now at parity.

Re-qualification at the new identities: Go suite 23/23 packages
PASS on Linux (go1.26.4 and host go1.27.0) and natively on the
Windows host (go1.26.5, 23/23 including the 201-row golden, the
push tests, and the new verbatim push fold test); Rust workspace
PASS on Linux (rustc 1.97.1, no Rust product source change).  The
full battery PASSes at the final staged identities: matrices 38/38
single and 14 PASS + 24 legitimate skips per mixed direction;
crash 16/16 both directions; the negative control 0/16; resource
proofs 8/8; kind-coverage gate PASS with all 46 self-test
controls; golden exchanges 55; sensitivity gate 14.  Windows
housekeeping re-qualified at `016010fc`: 2/2 PASS with native
Windows Python 3.14.6 (provenance: clean tree, go1.26.5
windows/amd64, rustc 1.97.1).

Linux reports record the product identities `07c4e314...` (rust,
unchanged since the round-4 qualified build) and `eab62a09...`
(go, rebuilt with `-buildvcs=false`), workers `77b6d086...`
(rust) / `2148bc0e...` (go), fixture `df3623a6...` (all staged in
`.local/shared/binaries/SHASUMS.txt` with sha256sum -c OK).  The
Windows housekeeping report records the Windows-host products
`c960a64f...` (rust) and `64854dfa...` (go); the Windows Go worker
is `06128e96...`.
---

Historical wave record (superseded by the head block): the current evidence is regenerated at product revision `01356600`
(the external-control repair wave; qualification HEAD `bfc60f96`
adds one test-only raw-parent helper repair).  The wave repairs
four Go/Rust divergence classes and two test-tripwire defects found
by the external whole-milestone control:

- Windows prefix parsing: `parsePrefix` normalizes the eight-byte
  prefix header (`/` -> `\`) before matching the verbatim UNC
  marker, so `\\?\\UNC/server/share` is VerbatimUNC with no name
  exactly like Rust `PrefixParser::get_prefix`; `parseUNC` no
  longer absorbs the share's trailing separator, so
  `\\server\share\\leaf` keeps one separator in derived parent
  and sidecar spellings.  `FileName` is rewritten as an
  allocation-free backward component walk with identical
  semantics (0 allocs/op on the probed corpus; the 8-byte
  verbatim-header normalization allocates once per call only
  for `\\?\\`-prefixed spellings).  The Windows golden corpus
  grows to 201 rows with five forward-slash UNC/verbatim-UNC
  shapes, every row confirmed against native windows-host rustc
  1.97.1.
- snapshot live-self rejection: `rejectLiveSelf` probes the bound
  destination spelling (main name + live parent), so valid
  `main/` and `main/.` destination spellings no longer fail
  ENOTDIR against the raw destination path; pinned by
  `reject_live_self_test.go`.
- Rust thread-creation tripwire: the scanner consumes its seeded
  brace once and now asserts the watchdog spawn marker
  (`iprange-signal-diag`) lies inside the checked region and
  reports checked/skipped line counts; the negative control
  (spawn injected at the watchdog) fails as designed.
- destination-preflight tests: accept shapes are delivered
  verbatim through raw (uncleaned) parents, so trailing-dot and
  mid-path `..` shapes reach the handlers; the raw-parent helper
  anchors at the volume/root and is Windows-correct.

Re-qualification at the new identities: Go suite 23/23 packages
PASS on Linux (go1.26.4 and host go1.27.0) and natively on the
Windows host (go1.26.5, 23/23 including the 201-row golden and the
push tests); Rust workspace PASS on Linux (rustc 1.97.1, no Rust
product source change).  The full battery PASSes at the final
staged identities: matrices 38/38 single and 14 PASS + 24
legitimate skips per mixed direction; crash 16/16 both directions;
the negative control 0/16; resource proofs 8/8; kind-coverage gate
PASS with all 46 self-test controls; golden exchanges 55;
sensitivity gate 14.  Windows housekeeping re-qualified at
`bfc60f96`: 2/2 PASS with native Windows Python 3.14.6
(provenance: clean tree, go1.26.5 windows/amd64, rustc 1.97.1).
The wine differential against the Rust-1.97.1-derived Python
oracle passes 296/296 Windows and 417/417 POSIX shapes with the
corrected prefix-header normalization.

Linux reports record the product identities `07c4e314...` (rust,
unchanged since the round-4 qualified build) and `23e4730a...`
(go, rebuilt with `-buildvcs=false`), workers `77b6d086...`
(rust) / `d83854dc...` (go), fixture `df3623a6...` (all staged in
`.local/shared/binaries/SHASUMS.txt` with sha256sum -c OK).  The
Windows housekeeping report records the Windows-host products
`c960a64f...` (rust) and `37a3e563...` (go); the Windows Go worker
is `c92b804b...`.

---

Historical wave record (superseded by the head block): the current evidence is regenerated at product revision `e54015d1`
(the round-6 Rust push join repair).  The re-anchored parity and
tester reviews closed the last two path-spelling divergences:
`expandPaths` and the export/metadata/removals temporary placement
built joins with an unconditional separator, while Rust
`PathBuf::push` inserts a separator only when the base does not
already end with one and never after a bare drive prefix, so
`"@<dir>/"` expanded to a doubled separator and a drive-relative
temporary (`C:` parent) was placed at the volume root instead of the
drive-relative name.  The repair at `e54015d1` adds one authoritative
`pathname.Push` mirror (bare-drive rule plus the verbatim `_push`
rebuild) and routes the four join sites through it; the separator
rule is pinned by `TestPushSeparatorRules` (cross-platform) and
`TestPushWindows` (native Windows), and the trailing-separator
`@`-spellings remain pinned by
`TestScratchAtExpansionTrailingSeparator`.  The round-5
physical-root repair (a69eb53d, forward slash after a verbatim
prefix) carries unchanged.

The Go suite is green on the qualified go1.26.4 and on the host
go1.27.0 (23/23 packages on each), and natively on the Windows host
(go1.26.5, 23/23 packages including the 196-row golden and the new
push tests); the Rust workspace suites are green on Linux (rustc
1.97.1) and natively on Windows (without source change).  The full
battery PASSes at the final staged identities: matrices 38/38 single
and 14 PASS + 24 legitimate skips per mixed direction; crash 16/16
both directions; the negative control 0/16 (8 real-producer
scenarios failing at the substituted-consumer stage and 8
substituted-producer scenarios failing during setup); resource
proofs 8/8; kind-coverage gate PASS with all 46 self-test controls;
golden exchanges 55; sensitivity gate 14.  Windows housekeeping
re-qualified at `e54015d1` on the authorized Windows validation
host: 2/2 PASS with native Windows Python 3.14.6 (provenance: clean
tree, go1.26.5 windows/amd64, rustc 1.97.1).  The `@`-expansion,
raw-path, and verbatim parity probes remain green on both products.

Linux reports at `e54015d1` record the product identities
`07c4e314…` (rust, unchanged since the round-4 qualified build) and
`b4aedb1c…` (go, rebuilt with `-buildvcs=false`), workers
`77b6d086…` (rust) / `11001b94…` (go), fixture `df3623a6…` (all
staged in `.local/shared/binaries/SHASUMS.txt` with sha256sum -c
OK).  The Windows housekeeping report records the Windows-host
products `c960a64f…` (rust) and `3ec21b97…` (go); the Windows Go
worker is `71229191…`.

---

Historical wave record (superseded by the head block): evidence was regenerated at product revision `8061d80d`
(the round-6 @-expansion separator repair).  The re-anchored parity
review found `expandPaths` concatenated the entry separator
unconditionally, so a trailing-separator referenced spelling
(`"@<dir>/"`) expanded to a doubled separator (`"<dir>//01.txt"`)
while Rust `read_dir entry.path()` (PathBuf push) inserts a separator
only when the base does not already end with one.  The repair at
`8061d80d` inserts the separator only when the last byte of the
referenced spelling is not a path separator and pins both spellings
with `TestScratchAtExpansionTrailingSeparator`; the round-5
physical-root repair (a69eb53d, forward slash after a verbatim
prefix) carries unchanged.

The Go suite is green on the qualified go1.26.4 and on the host
go1.27.0 (23/23 packages on each), and natively on the Windows host
(go1.26.5, 23/23 packages including the 196-row golden); the Rust
workspace suites are green on Linux (rustc 1.97.1) and natively on
Windows (without source change).  The full battery PASSes at the
final staged identities: matrices 38/38 single and 14 PASS + 24
legitimate skips per mixed direction; crash 16/16 both directions;
the negative control 0/16 (8 real-producer scenarios failing at the
substituted-consumer stage and 8 substituted-producer scenarios
failing during setup); resource proofs 8/8; kind-coverage gate PASS
with all 46 self-test controls; golden exchanges 55; sensitivity gate
14.  Windows housekeeping re-qualified at `8061d80d` on the
authorized Windows validation host: 2/2 PASS with native Windows
Python 3.14.6 (provenance: clean tree, go1.26.5 windows/amd64,
rustc 1.97.1).  The `@`-expansion, raw-path, and verbatim-UNC
parity probes remain green on both products.

Linux reports at `8061d80d` record the product identities
`07c4e314…` (rust, unchanged since the round-4 qualified build) and
`5383917e…` (go, rebuilt with `-buildvcs=false`), workers
`77b6d086…` (rust) / `3bb3180c…` (go), fixture `df3623a6…` (all
staged in `.local/shared/binaries/SHASUMS.txt` with sha256sum -c
OK).  The Windows housekeeping report records the Windows-host
products `c960a64f…` (rust) and `d013e0b5…` (go); the Windows Go
worker is `a91e548b…`.

---

Historical wave record (superseded by the head block): evidence was regenerated at product revision `a69eb53d`
(the round-6 forward-slash-after-verbatim-prefix repair).  The
re-anchored whole-milestone review found the Windows pathname port
checked the physical root with the verbatim-aware separator set: a
hand-built long-path spelling with a forward slash directly after a
verbatim prefix (`\\?\\C:/x`) was parsed with the slash inside the
first body component, so Go produced file names with a leading slash,
accepted `\\?\\C:/` with a name, and kept the slash in
`with_file_name` results, while Rust `has_physical_root` uses the
static separator set (body components still split on the verbatim
backslash only).  The repair at `a69eb53d` computes the physical root
with the static separator, consumes the root byte in `FileName` after
a verbatim prefix, keeps a trailing verbatim `"."` as a CurDir
component (no file name, like Rust), and mirrors the `PathBuf::_push`
verbatim rebuild in `WithFileName` (the root byte is re-emitted as the
main separator and the prefix raw bytes keep their parsed spelling),
replacing the share-less `\\?\\UNC\\` special case; the Windows golden
corpus grew to 196 rows with forward-slash-after-prefix and
trailing-dot verbatim shapes pinned against a native Windows rustc
1.97.1 probe (the previous 183-row corpus had no shape with a
separator after a verbatim prefix).

The Go suite is green on the qualified go1.26.4 and on the host
go1.27.0 (23/23 packages on each), and natively on the Windows host
(go1.26.5, 23/23 packages including the extended golden); the Rust
workspace suites are green on Linux (rustc 1.97.1) and natively on
Windows (without source change).  The full battery PASSes at the
final staged identities: matrices 38/38 single and 14 PASS + 24
legitimate skips per mixed direction; crash 16/16 both directions;
the negative control 0/16 (8 real-producer scenarios failing at the
substituted-consumer stage and 8 substituted-producer scenarios
failing during setup); resource proofs 8/8; kind-coverage gate PASS
with all 46 self-test controls; golden exchanges 55; sensitivity gate
14.  Windows housekeeping re-qualified at `a69eb53d` on the
authorized Windows validation host: 2/2 PASS with native Windows
Python 3.14.6 (provenance: clean tree, go1.26.5 windows/amd64,
rustc 1.97.1).  The `@`-expansion, raw-path, and verbatim-UNC
parity probes remain green on both products.

Linux reports at `a69eb53d` record the product identities
`07c4e314…` (rust, unchanged since the round-4 qualified build) and
`1ed287a8…` (go, rebuilt with `-buildvcs=false`), workers
`77b6d086…` (rust) / `3bb3180c…` (go), fixture `df3623a6…` (all
staged in `.local/shared/binaries/SHASUMS.txt` with sha256sum -c
OK).  The Windows housekeeping report records the Windows-host
products `c960a64f…` (rust) and `5018f974…` (go); the Windows Go
worker is `a91e548b…`.

---

Historical wave record (superseded by the head block): evidence was regenerated at product revision `2c5d668b`
(the round-6 @-directory expansion repair).  The re-anchored
whole-milestone review found the last remaining lexical
normalization of a caller-supplied path: `input.go` built each
`@`-directory entry path with `filepath.Join`, which lexically
cleaned a symlinked intermediate plus `".."` inside the caller's
referenced spelling, so Go refused the kernel-resolved directory (or
ingested a different one) while Rust's `read_dir entry.path()` keeps
the raw spelling.  The repair at `2c5d668b` builds the entry path by
raw concatenation and pins the class with a POSIX-gated `@`-expansion
regression test; a live dual-product probe proves both products
publish through `@<dir>/<symlink>/../<realdir>` with the same
`published` outcome.

The Go suite is green on the qualified go1.26.4 and on the host
go1.27.0 (23/23 packages on each), and natively on the Windows host
(go1.26.5, 23/23 packages); the Rust workspace suites are green on
Linux (rustc 1.97.1) and natively on Windows (without source
change).  The full battery PASSes at the final staged identities:
matrices 38/38 single and 14 PASS + 24 legitimate skips per mixed
direction; crash 16/16 both directions; the negative control 0/16
(8 real-producer scenarios failing at the substituted-consumer
stage and 8 substituted-producer scenarios failing during setup);
resource proofs 8/8; kind-coverage gate PASS with all 46 self-test
controls; golden corpus 55; sensitivity gate 14.  Windows
housekeeping re-qualified at `2c5d668b` on the authorized Windows
validation host: 2/2 PASS with native Windows Python 3.14.6
(provenance: clean tree, go1.26.5 windows/amd64, rustc 1.97.1).

Linux reports at `2c5d668b` record the product identities
`07c4e314…` (rust, unchanged since the round-4 qualified build) and
`78cbd4c3…` (go, rebuilt with `-buildvcs=false`), workers
`77b6d086…` (rust) / `114a7018…` (go), fixture `df3623a6…` (all
staged in `.local/shared/binaries/SHASUMS.txt` with sha256sum -c
OK).  The Windows housekeeping report records the Windows-host
products `c960a64f…` (rust) and `3b967437…` (go); the Windows Go
worker is `8338b58d…`.

---


Historical wave record (superseded by the head block): evidence was regenerated at product revision `03b7d4ab`
(the round-6 verbatim-UNC repair).  The re-anchored round-6
performance review found a P1 in the Windows pathname port: the
verbatim-UNC branch re-parsed the path as a plain UNC whenever the
share was the final component, so `FileName("\\?\\UNC\\srv\\sh")`
returned the share as a file name and `WithFileName` dropped the share
from derived sidecar paths, while Rust 1.97.1 keeps
`VerbatimUNC(server, share)` unconditionally and a share-terminal path
has no file name or parent.  The repair at `03b7d4ab` mirrors Rust
`Prefix::len` for VerbatimUNC (the share separator counts only with a
share) and the verbatim push rebuild (the share-less `\\?\\UNC\\`
prefix doubles the separator), and extends the Windows golden corpus
with the share-terminal, prefix-terminal, trailing-separator, and
plain-UNC share shapes (12 corpus rows) pinned against a native
Windows rustc 1.97.1 probe (the corpus previously contained no
share-terminal shape, so no committed gate could detect the class).

The Go suite is green on the qualified go1.26.4 and on the host
go1.27.0 (23/23 packages on each), and natively on the Windows host
(go1.26.5, 23/23 packages including the extended golden); the Rust
workspace suites are green on Linux (rustc 1.97.1) and natively on
Windows (without source change).  The full battery PASSes at the
final staged identities: matrices 38/38 single and 14 PASS + 24
legitimate skips per mixed direction; crash 16/16 both directions;
the negative control 0/16 (8 real-producer scenarios failing at the
substituted-consumer stage and 8 substituted-producer scenarios
failing during setup); resource proofs 8/8; kind-coverage gate PASS
with all 46 self-test controls; golden corpus 55; sensitivity gate
14.  Windows housekeeping re-qualified at `03b7d4ab` on the
authorized Windows validation host: 2/2 PASS with native Windows
Python 3.14.6 (provenance: clean tree, go1.26.5 windows/amd64,
rustc 1.97.1).  The symlink+`..` raw-path parity probe from the
prior repair remains green on both products.

Linux reports at `03b7d4ab` record the product identities
`07c4e314…` (rust, unchanged since the round-4 qualified build) and
`85b71310…` (go, rebuilt with `-buildvcs=false`), workers
`77b6d086…` (rust) / `114a7018…` (go), fixture `df3623a6…` (all
staged in `.local/shared/binaries/SHASUMS.txt` with sha256sum -c
OK).  The Windows housekeeping report records the Windows-host
products `c960a64f…` (rust) and `38417180…` (go); the Windows Go
worker is `8338b58d…`.

---


Historical wave record (superseded by the head block): evidence was regenerated at product revision `ae57845e`
(the round-6 raw-path repair).  The re-anchored round-6 tester review
at `5a008411` found a wire-reachable P1: the immutable reader open
still normalized the caller's raw path with `filepath.Clean`
(`v4/go/internal/mapping/mapping.go` `openMapping`), so a database
addressed as `<dir>/<symlink>/../<name>` was refused by Go while the
Rust twin serves it at the kernel-resolved location.  The repair at
`abfc2696` removes every lexical normalization of caller-supplied
paths on the open, verify, create, sidecar, snapshot preflight,
export, removal, and metadata publication paths: raw spellings flow
to the kernel exactly like the Rust `std::path` derivations, and only
os-resolved executable-discovery paths keep `filepath` helpers.  New
regression tests open a fixture through a symlinked intermediate plus
`".."` in the immutable reader and the validation source and assert
the cleaned spelling is refused (POSIX kernel-resolution class; the
tests skip on Windows, where reparse-point semantics apply to both
products equally because both pass the identical raw spelling).  A
live probe against the rebuilt products proves both products open,
describe, lookup, and close the same database through the raw path
and both refuse the cleaned spelling.

The Go suite is green on the qualified go1.26.4 and on the host
go1.27.0 (23/23 packages on each), and natively on the Windows host
(go1.26.5, 23/23 packages); the Rust workspace suites are green on
Linux (rustc 1.97.1) and natively on Windows (without source
change).  The full battery PASSes at the final staged identities:
matrices 38/38 single and 14 PASS + 24 legitimate skips per mixed
direction; crash 16/16 both directions; the negative control 0/16
(8 real-producer scenarios failing at the substituted-consumer
stage and 8 substituted-producer scenarios failing during setup);
resource proofs 8/8; kind-coverage gate PASS with all 46 self-test
controls; golden corpus 55; sensitivity gate 14.  Windows
housekeeping re-qualified at `ae57845e` on the authorized Windows
validation host: 2/2 PASS with native Windows Python 3.14.6
(provenance: clean tree, go1.26.5 windows/amd64, rustc 1.97.1).
The records commit `ae57845e` changes tests only; the product
binaries reproduce byte-identically from `abfc2696`.

Linux reports at `ae57845e` record the product identities
`07c4e314…` (rust, unchanged since the round-4 qualified build) and
`a00b1307…` (go, rebuilt with `-buildvcs=false`), workers
`77b6d086…` (rust) / `2d748be0…` (go), fixture `df3623a6…` (all
staged in `.local/shared/binaries/SHASUMS.txt` with sha256sum -c
OK).  The Windows housekeeping report records the Windows-host
products `c960a64f…` (rust) and `2f5b5fac…` (go); the Windows Go
worker is `b0bb2a2b…`.

---


Historical wave record (superseded by the head block): evidence was regenerated at product revision `06495eeb`
(the round-6 follow-up that charges the cross-toolchain deflate
workspace honestly).  The round-6 re-anchored portability review at
`5a008411` measured the Go `compress/flate` DefaultCompression
workspace at ~1.06 MiB on go1.27.0, above the declared 840 KiB
`deflateHeapOverhead` charged to metadata callers
(`v4/go/internal/writer/metadata.go`); the follow-up raises the
charge to 1150 KiB and pins the deflate branch of the small-payload
test with a budget that admits it under the larger charge.  The Go
suite is green on the qualified go1.26.4 and on the host go1.27.0
(23/23 packages on each), and natively on the Windows host
(go1.26.5, 23/23 packages); the Rust workspace suites are green on
Linux (rustc 1.97.1) and natively on Windows (without source
change).  The full battery PASSes at the final staged identities:
matrices 38/38 single and 14 PASS + 24 legitimate skips per mixed
direction; crash 16/16 both directions; the negative control 0/16
(8 real-producer scenarios failing at the substituted-consumer
stage and 8 substituted-producer scenarios failing during setup);
resource proofs 8/8; kind-coverage gate PASS with all 46 self-test
controls; golden corpus 55; sensitivity gate 14.  Windows
housekeeping re-qualified at `06495eeb` on the authorized Windows
validation host: 2/2 PASS with native Windows Python 3.14.6
(provenance: clean tree, go1.26.5 windows/amd64, rustc 1.97.1).

Linux reports at `06495eeb` record the product identities
`07c4e314…` (rust, unchanged since the round-4 qualified build) and
`7544ffc2…` (go, rebuilt with `-buildvcs=false`), workers
`77b6d086…` (rust) / `4c8f50fa…` (go), fixture `df3623a6…` (all
staged in `.local/shared/binaries/SHASUMS.txt` with sha256sum -c
OK).  The Windows housekeeping report records the Windows-host
products `c960a64f…` (rust) and `a20bcb2d…` (go); the Windows Go
worker is `d1273d04…`.

---


The round-6 evidence is regenerated at the round-6 final product
revision `ebbd0419`.  The external round-5 control review found the
Go publish destination preflight (`requirePublicationParent`) still
using raw `filepath.Base`: it rejected a trailing-dot destination
(`"<parent>/name/."`) that Rust `Path::file_name` accepts and
resolved a trailing `..` onto an ancestor that Rust refuses.  The
round-6 repair replaces every Go name/parent derivation that
mirrors a Rust twin with one shared implementation, the new
`internal/pathname` package that ports the Rust 1.97.1
`std::path` component state machine (`Path::file_name`,
`Path::parent`, `PathBuf::with_file_name`): raw paths are not
normalized, "." components are dropped, ".." is an ordinary
component that is never resolved, repeated and trailing separators
collapse, and the Windows volume prefix is not a name or a parent.
The port is pinned by golden differential tests against rustc
output (162 unix shapes generated natively on Linux, and 171
Windows shapes generated on the authorized Windows validation
host, which added the drive-relative, UNC, verbatim, and device
namespace classes).  Wired through every derivation site: the
publish/lifecycle destination preflights, the publication
destination binding, the live namespace bindPath/bindPair/
parent-identity/sync-parent and all of their callers, the
canonical sidecar and transition-temp derivation, the immutable
reader namespace checks and sidecar path, the recovery
basic/offline source openings, and the worker/system deps
discovery.  The native Windows suite then caught two remaining
parity gaps in the first port (Go filepath.VolumeName classified
`//a//b` as a UNC prefix although Rust requires a non-empty share,
and the CurDir yield after a drive prefix was permitted although
Rust's else-if chain never reaches it — `"C:."` has no file name
and no parent); classifyPrefix now ports
`sys/path/windows_prefix.rs parse_prefix` directly (Disk, UNC,
Verbatim, VerbatimDisk, VerbatimUNC, DeviceNS with their exact
byte lengths).  The live products now accept `"<parent>/name/."`
and refuse `".../.."` with identical error classes and messages
(probe-verified against the rebuilt Go and carried Rust binaries).

The round-6 repair wave was qualified at `ebbd0419` (product
source):

- the Go suite is green on Linux with the qualified go1.26.4
  (23/23 packages including the new pathname package) and natively
  on the Windows host (go1.26.5, 23/23 packages); the Rust
  workspace suites are green on Linux (rustc 1.97.1) and natively
  on Windows;
- the full battery PASSes at the final staged identities:
  matrices 38/38 single and 14 PASS + 24 legitimate skips per
  mixed direction; crash 16/16 both directions; the negative
  control 0/16 (8 real-producer scenarios failing at the
  substituted-consumer stage and 8 substituted-producer scenarios
  failing during setup); resource proofs 8/8; kind-coverage gate
  PASS with all 46 self-test controls; golden corpus 55;
  sensitivity gate 14;
- Windows housekeeping re-qualified at source revision
  `ebbd0419` on the authorized Windows validation host: 2/2 PASS
  with the native Windows Python 3.14.6 (Go `2156394a…`, Rust
  `c960a64f…` — the Rust product is unchanged since the round-4
  qualified build at `ed29e437`; provenance recorded: go1.26.5
  windows/amd64, rustc 1.97.1, clean tree), and the full Go suite
  passes natively there (23/23 packages) alongside the green
  native Rust suite.

Linux reports record the product identities `07c4e314…` (rust,
unchanged since the round-4 qualified build) and `34548919…` (go,
rebuilt with `-buildvcs=false` at the round-6 repair revision
`ebbd0419`), workers `77b6d086…` (rust) / `a7c9225a…` (go),
fixture `df3623a6…` (all staged in
`.local/shared/binaries/SHASUMS.txt` with sha256sum -c OK; the Go
worker was rebuilt with the qualified go1.26.4 because the worker
links the pathname helpers); the Windows housekeeping report
records the Windows-host products `c960a64f…` (rust) and
`2156394a…` (go) at source revision `ebbd0419`.

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

## Evidence identity binding

Every report in this directory carries `git_head`: the commit OID of the
reviewed product tree, from `git rev-parse HEAD` against the checkout that
owns the harness that produced the report, or `null` when that tree is not
a git checkout. It is what makes "this battery passed" a claim about a
revision rather than a claim about prose in this file.

What the field is worth is set by what `check_kind_coverage.py` enforces
over it, and that is now explicit rather than implied.  Each of the seven
consumed reports — `matrix-rust`, `matrix-go`, `matrix-rust_to_go`,
`matrix-go_to_rust`, `crash`, `fifo-surface`, `throughput` — must carry
`git_head`; it must be a 40-character hexadecimal object id; the
placeholder shapes `0`*40 and `1`*40 are rejected; and the seven values
must name one and the same commit.  `--fifo-surface` and `--throughput`
are required gate inputs, so a report set that quietly leaves them out is
a CLI error rather than a narrower claim.  A reviewer demonstrated at the
previous revision that all three of those attacks were accepted — an
all-zeros object id, the field deleted, and the field desynced between
reports — and each now has a committed `--self-test` control that FAILs
the gate.

The binding is against *mixing revisions*, not against a falsified field:
`git_head` is written by the harness that produced the report, so an
editor who rewrites it by hand can still write a well-shaped value.  What
the enforcement buys is that a report copied forward from an older
battery, or substituted from a different tree, cannot pass alongside
reports from the current one, and that an empty or omitted report set
cannot be presented as a passing battery.

`git_head` is deliberately a separate member from `checkout_root`.
`checkout_root` is the directory a reader resolves checkout-relative command
arguments against (null, absent, and empty all mean "the reviewing checkout",
which is what `check_kind_coverage._report_checkout_root()` falls back to).
Committed reports record it as `null` unconditionally, whatever machine
authored them: the operator-profile spelling is refused by the privacy scan,
and any other absolute spelling is host-specific prose that no reviewer can
check and no consumer resolves anything against. A producer that recorded the
directory of a non-personal checkout therefore authored an artifact its own
writer refused, since `committed_report_problems()` rejects a non-null value.
The value is still available to the harnesses that need a real path for a
filesystem decision (refusing output inside the checkout, refusing a
`--report-dir` inside it) through `command_sanitize.recorded_checkout_root()`,
which must never feed a committed report. A checkout root and a commit OID are
different kinds of thing, and putting the OID in the path field would break
the binding that field provides.

## Files

- `matrix-rust.json`, `matrix-go.json` — single-language matrices over
  the 63 committed case files. Rust: 63 passed, 0 failed. Go: 57 passed,
  6 failed — the six cases whose expectations come from the Rust authority
  while the corresponding Go fix is in flight, each declared in
  `known-defects.json` with its observed reply rather than removed or
  softened. See `## Declared engine defects`.  Every PASS case entry
  carries the per-actor SHA-256, the product-declared `implementation`
  (rust|go from `system.describe`) and the executed-step count, and the
  gate re-derives the executed case set from `v4/cli/cases/*.json` plus
  the matrix skip rules: rows cannot be deleted, invented, or moved
  between status classes without failing the gate.
- `matrix-rust_to_go.json`, `matrix-go_to_rust.json` — two-binary
  cross-language matrices over the same case set: both directions
  record 34 executed with 0 failures and 29 skipped (cases that do not
  exercise both a producer and a consumer role), and oracle checks 22,
  over the 63 cases that existed at the reports' recorded revision.
  The same per-actor identity is recorded for every PASS case; `check_kind_coverage.py` derives language attribution
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
- `crash-negative.json` — the negative control, 0/16 pass,
  `failed: 16`, zero leftover processes.  Each of the eight crash
  scenarios runs in both substitution directions: with the real
  producer and a substituted `/bin/false` consumer the scenario
  reaches the consumer stage and fails there (the fake consumer
  closes stdout: "service closed stdout"), so a substituted
  consumer can never be credited with executed work; with a
  substituted `/bin/false` producer the scenario fails during
  setup, because the fake producer never creates the artifact and
  no consumer operation runs.  Used as the harness sensitivity
  control, never as a kind-gate source.
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
  remove with the listed row passed unchanged and a
  proved-currently-absent result: `cleanup_state: clean`, no
  housekeeping artifacts, POSIX directory-synced unlink),
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
  passed unchanged, with a proved-currently-absent result
  (both products truthfully report
  crash_reappearance_possible; the spec's Clean contract makes
  no power-loss guarantee for the final unlink) and a zero-row
  after-listing — and (4) build provenance (source revision, clean
  tree, build commands, toolchain) with per-binary mtime/size.  The
  pair is built by `gc_envelope_windows.py` from the committed
  codec constants (`v4/go/internal/live/gc_codec.go`, `gc_name.go`,
  `identity_local_windows.go`, `v4/go/internal/security/
  security_windows.go`), not by any product test hook.  On
  non-Windows platforms the same script records the truthful
  `os_unsupported`/`read_only_failure` negative.

- `fifo-surface.json` — `iprange-cli-fifo-surface-report-v1`, produced by
  `check_fifo_surface.py`. The named-pipe refusal class for each of the 17
  user-path open arms on both engines, with the transport code, the
  `data.code` class, the elapsed time, the child exit status, and the exact
  request frame per arm, plus a regular-file control per engine. The
  expected class per arm is what Rust answers and is asserted identical for
  Go: `invalid_argument` for the read-only opens, `wrong_state` for the
  quiescent offline arms, `invalid_path` for the writer inputs (metadata
  `replace_file` source on both `direct.replace` and
  `database.metadata.replace`, the direct CSV input, and the `@file` list)
  and `conflict` for a snapshot destination. This is the pin that keeps the
  never-block open class from being only a prose claim, over the JSON-RPC
  session surface; the one-shot legacy argv surface deliberately keeps the
  blocking behavior of the C reference (`../README.md`, "The never-block
  contract is the session surface").  The report's executed actors are now
  identity-bound like the matrix and crash reports: each binary is
  identified by SHA-256 and by its `system.describe` implementation label
  taken from the executed-actor provenance, the pair must be consistent with
  the engine it claims to be, the digest must appear in the staged SHASUMS
  ledger when `--sha256-ledger` is supplied, and the on-disk file is
  re-hashed when binary verification is enabled.  A wrong digest, a missing
  digest, a foreign implementation label, and a digest no actor in the run
  actually executed are each rejected by a committed control.
- `throughput.json` — `iprange-cli-throughput-report-v1`, produced by
  `throughput_harness.py`. Busy-reply `replies_per_s` for `system.describe`
  in 30-frame bursts (10,000 requests x 3 rounds, fresh child per round)
  and the `clone`/`clone3` census at 3,000 and 6,000 requests under
  `strace`, per product. Rate is attested, not gated — an absolute floor is
  not host portable — so what the gate enforces is that every reply is
  served, the child exits cleanly, and the thread-creation count does not
  grow with the request count (measured at this revision: Go 17 `clone`
  calls and Rust 4, each identical at the 3,000 and 6,000 request probes;
  Go unique child tids 11 at 3,000 and 13 at 6,000, Rust 4 at both).
  Committed medians at this revision: Go 15,394.0 and Rust 45,751.7
  replies/s.  Those are host-load observations, and the rate window starts
  at child spawn so it includes process start.  The report carries one
  median per product, so no second quieter-host figure can be cited from
  it; the 17,689.4/55,471.5 and 35,464.7/50,535.9 pairs that appeared
  here earlier match no member of the committed artifact.  No Go/Rust ratio from these figures is evidence for the 1.3x
  relative-rate contract planned for milestone 5, which needs a load-isolated
  measurement protocol this harness does not implement.  The report is
  identity-bound (binary SHA-256 plus the `system.describe` implementation
  label of each executed actor, checked against the staged SHASUMS ledger when
  supplied), and its arithmetic is re-derived by the gate: replies must equal
  requests in every round, `replies_per_s` must equal `requests/seconds`, the
  median must be the median of the recorded rounds, and the census must be
  internally consistent.  A rate inflated to 9,999,999 over a fabricated
  census, a median edited alone, a missing or wrong digest, and a foreign
  implementation label are each rejected by a committed control.
- `refusal-class-parity.json` —
  `iprange-cli-refusal-class-parity-report-v1`, produced by
  `check_refusal_class_parity.py`.  The Go-versus-Rust refusal comparison
  over 23 arms x 21 path kinds = 483 cells, per cell on
  `(kind, transport code, data.code, outcome)` plus the
  publication-evidence shape, each attempt under its own
  bounded deadline with retries so a flake or a hang is reported apart from
  a divergence.  It records the grid it actually executed (the gate re-derives
  it and fails on a missing or invented cell), the `MANDATORY_PATH_KINDS`
  that a row deletion would otherwise remove silently, the
  `PINNED_REFUSALS` table whose expectations come from the Rust authority,
  the binary digests and `system.describe` implementation labels it drove,
  and `git_head`.  Committed at the artifact's own revision: 483 cells
  executed, 0 divergences, 0 hangs, 0 flaky, 36 of 36 pins satisfied,
  verdict PASS, plus the full 378-cell pressure sweep (0 flaky, 0
  hangs, 0 divergences) that replaces the routine subset at the
  milestone tier.  Earlier waves committed a 418-cell/29-pin grid and,
  at the full tier, a duplicate second sweep under a second name; both
  conditions are closed by this rotation (astra gate finding P2-6).
  See the head block and `../README.md`.
- `coverage-go.json` — `iprange-cli-coverage-go-report-v1`, produced by
  `coverage_harness.py`.  Measured Go coverage in three separable figures:
  `unit` from the module's own `go test -cover`, `integration` from the
  committed corpus executed against `go build -cover` binaries, and `merged`
  from the two counter sets combined by `go tool covdata`.  Statements,
  functions and blocks are recorded as raw counters plus percentages, per
  package, and each figure is cross-checked against `go tool covdata percent`
  — a package whose computed percentage disagrees with the tool is refused
  rather than published.  The instrumented binaries and their own staging
  directory are recorded so a reader can confirm they are not the
  qualification or throughput binaries; killed runs are never merged.
  Committed at this revision: unit 48.98% / 65.60% / 45.45%, integration
  41.22% / 60.14% / 37.31%, merged 60.46% / 80.89% / 55.71% (statements /
  functions / blocks) over 30 packages.
- `golden.json`, `sensitivity.json` —
  `iprange-cli-golden-report-v1` and
  `iprange-cli-sensitivity-report-v1`. The golden-exchange and
  broken-server counts with the covered-method list and per-mode
  outcomes. They exist so these two claims are machine-readable and carry
  the same revision binding as the rest of the battery, rather than living
  only in the suite's console output.

## Declared engine defects

- `known-defects.json` — `iprange-cli-known-defects-v1`. The cases a
  product engine is currently known to fail at the binaries above while
  its fix is in flight. Each entry names the matrix, the case, the owning
  component, the contract term it violates, and the response the engine
  actually gives. `check_kind_coverage.py` enforces it in both directions:
  a FAIL row that is not listed fails the gate, and a listed case that
  PASSes fails the gate, so the ledger can neither be used to paint a red
  battery green nor left behind as a hiding place once the engine is
  fixed. An absent or empty ledger means the battery must be entirely
  green, and the check now runs on every gate invocation rather than only
  when some matrix reports a failure — the earlier placement inside the
  failure branch let a fabricated entry sit beside an all-PASS report
  unnoticed.

  The ledger is committed EMPTY at this revision: the eight entries the
  go-engine carried through wave 19.23 — six in `matrix-go`
  (`writer.symlink_live_direct_replace`, `writer.symlink_live_feeds_create`,
  `recovery.inspect_live_junk`, `recover.live_junk`,
  `feeds.missing_feed_outcomes`, `snapshot.zero_byte_destination`) and the
  two of those that also ran in the `go_to_rust` direction — were retired
  when the wave-19.24 product repairs landed, and the battery has been
  green over the empty ledger at every rotation since.  An empty ledger
  means the battery must be entirely green, and it is: that is the state
  the two-directional enforcement above describes (a FAIL row the ledger
  does not list fails the gate; a listed case that PASSes fails the
  gate), so a future regression must be declared in the ledger before it
  can be tolerated, and it cannot hide there after it is fixed.

## Gate invocation

```bash
nice python3 v4/cli/check_kind_coverage.py \
  --matrix v4/cli/evidence/matrix-rust.json \
  --matrix v4/cli/evidence/matrix-go.json \
  --matrix v4/cli/evidence/matrix-rust_to_go.json \
  --matrix v4/cli/evidence/matrix-go_to_rust.json \
  --crash v4/cli/evidence/crash.json \
  --crash v4/cli/evidence/crash-go_to_rust.json \
  --crash-negative v4/cli/evidence/crash-negative-producer-false.json \
  --crash-negative v4/cli/evidence/crash-negative.json \
  --fifo-surface v4/cli/evidence/fifo-surface.json \
  --throughput v4/cli/evidence/throughput.json \
  --refusal-class-parity v4/cli/evidence/refusal-class-parity.json \
  --coverage-go v4/cli/evidence/coverage-go.json \
  --windows-housekeeping v4/cli/evidence/windows-housekeeping.json \
  --windows-guard v4/cli/evidence/windows-guard.json \
  --resource v4/cli/evidence/resource.json \
  --golden v4/cli/evidence/golden.json \
  --sensitivity v4/cli/evidence/sensitivity.json \
  --guard-posix v4/cli/evidence/guard-posix.json \
  --race-battery v4/cli/evidence/race-battery.json \
  --sha256-ledger /tmp/qualsvc/SHASUMS.txt

# Which arguments are required: at least one --matrix or --crash report,
# and both --fifo-surface and --throughput (an omitted flag is as cheap as a
# deleted git_head field, so the gate refuses to run without them).
# --refusal-class-parity, --coverage-go, --crash-negative,
# --windows-housekeeping, --windows-guard, --resource, --golden,
# --sensitivity, --guard-posix and --race-battery are repeatable and, when
# omitted, are discovered beside the reports already named; a report that
# cannot be found is a gate problem, not a skipped check.  Every consumed
# class must also be attested by the battery manifest, so an artifact the
# gate never consumes cannot be hollowed out unnoticed.  The parity class
# carries exactly one committed report: the chosen axis sweeps once into
# `refusal-class-parity.json` (astra gate finding P2-6 retired the
# second name and the duplicate full sweep that justified it).  --sha256-ledger takes a sha256sum-format
# ledger produced by the battery's own build step over the staged binaries,
# and when supplied every binary digest recorded in a consumed report must
# appear in it.  The committed `battery-manifest.json` is the durable form
# of the same binding: it records the ledger as digest-to-name entries, so
# provenance travels with the evidence and no committed artifact carries a
# workstation path.  --emit-manifest writes that manifest for the reports
# named on the command line, which is the battery step; --battery-manifest
# consumes it, which is why producer and consumer share one implementation.

# Refusal-class parity (23 arms x 21 path kinds = 483 cells x 2 engines;
# --pressure routine adds 12 descriptor-pressure profiles, --pressure full 42).
nice python3 v4/cli/check_refusal_class_parity.py \
  --go /tmp/qualsvc/bin/go/iprange --rust /tmp/qualsvc/bin/rust/iprange \
  --fixture /tmp/qualsvc/bin/rust/v4-fixture \
  --work "$(mktemp -d)" --json-report /tmp/refusal-class-parity.json
nice python3 v4/cli/check_refusal_class_parity.py --self-test

# Go unit + corpus-driven integration coverage.  --revision stages the
# measured tree from a commit, which is what makes the measurement
# reproducible while other workers edit the checkout; the instrumented
# binaries it builds are never the qualification or throughput binaries.
nice python3 v4/cli/coverage_harness.py --go-module v4/go \
  --revision 2c788b8e --rust /tmp/qualsvc/bin/rust/iprange \
  --fixture-tool /tmp/qualsvc/bin/rust/v4-fixture \
  --work "$(mktemp -d)" --json-report /tmp/coverage-go.json
nice python3 v4/cli/coverage_harness.py --self-test

# FIFO refusal surface (17 arms x 2 engines); --work must be empty.
nice python3 v4/cli/check_fifo_surface.py \
  --go /tmp/qualsvc/bin/go/iprange --rust /tmp/qualsvc/bin/rust/iprange \
  --fixture /tmp/qualsvc/bin/rust/v4-fixture \
  --work "$(mktemp -d)" --json-report v4/cli/evidence/fifo-surface.json
nice python3 v4/cli/check_fifo_surface.py --self-test

# Busy-reply rate and thread-structure attestation.
nice python3 v4/cli/throughput_harness.py \
  --go /tmp/qualsvc/bin/go/iprange --rust /tmp/qualsvc/bin/rust/iprange \
  --work "$(mktemp -d)" --json-report v4/cli/evidence/throughput.json
nice python3 v4/cli/throughput_harness.py --self-test

# Cross-compile and whole-module Windows vet for the Go product.
nice bash v4/cli/check_goos_matrix.sh
```

`--go`, `--rust`, `--fixture`, and `--work` are refused unless absolute,
and `--work` must be empty: a relative spelling resolves against the
invocation directory, which lets an arm read the wrong object and report
the opposite verdict without any error.

The gate requires all four matrix reports and a positive crash report,
rejects failed/leftover reports and unknown kinds, enforces the
both-language creation (and, where any service opens the kind,
both-language consumption) contract per kind from the executed-actor
identities, and counts only PASS crash scenarios.  Its doctored-report
self-test is opt-in (`--self-test`, run as its own step of the wave
battery) so an in-flight evidence rotation cannot replace a CLI verdict
with the self-test's own assertion.  That control battery covers the
clone-and-relabel attack (a `rust` report relabeled `go` fails),
missing per-case actors, and implementations outside rust/go.

Every kind in `REQUIRED_OPENED_KINDS` must be opened by both languages in
the **matrix** evidence specifically, which is what makes `adapter_output`
and `metadata_delivery` load-bearing: iterating only a hardcoded pair let
those kinds satisfy the gate from crash-only evidence, and stripping the
matrix opens for either kind now fails the battery even though the crash
report still records opens.  The same applies to the two surfaces the
corpus can only prove by assertion: every `writer_budget` method whose
grammar forbids zero must have a committed `-32602` assertion, each such
assertion must have actually executed in some PASS case, each product
language must be credited with at least one asserted rejection, and the
cross-language export digest groups must be attested by both roles on both
languages with one digest per group.  Self-test controls strip the digest
records, drop a role, diverge one digest, drop a params-rejection record,
invent one, flip its transport code, remove its request bytes, add an
undeclared failure, and mark a declared defect as passing; each is
rejected.

## Committed-report discipline

Every artifact in this directory is produced by a harness, and the
harnesses do not write here directly.  All twelve of them —
`run.py`, `crash_harness.py`, `resource_harness.py`,
`throughput_harness.py`, `coverage_harness.py`, `check_golden.py`,
`check_fifo_surface.py`, `check_refusal_class_parity.py`,
`sensitivity_gate.py`, `windows_guard_harness.py`,
`windows_housekeeping_harness.py` and `command_sanitize.py` — register in
`command_sanitize.COMMITTED_REPORT_WRITERS` and reach a committed path
only through `command_sanitize.write_committed_report`.  That one owner
adds the provenance members (`command`, `checkout_root`, `git_head`) and
the derived `privacy` block, screens the inputs the caller declares, and
refuses the write when any screened input or any finished string value
names an operator profile path.  Splitting the write from the provenance
would let a harness keep the artifact and drop the audit, so the registry
is audited as a set: `command_sanitize.py --self-test` executes 58
controls over the whole registry, and every harness self-test must report
`shared command_sanitize controls executed=58 expected=58` before its own
result counts.  `check_producer_privacy.py` attacks the three
lead-owned writers from the outside with 40 committed controls, so
neither a writer that screens nothing, nor one that serializes its own
JSON, nor an artifact with no `privacy` block, survives.

Verification modes, each with its own pinned control count so a deleted
control is a failure and not a smaller run:

```bash
# Gate and harness self-tests (offline, no products needed).
nice python3 v4/cli/check_refusal_class_parity.py --self-test   # 70 controls
nice python3 v4/cli/check_kind_coverage.py --self-test          # 133 controls + 5 acceptance
nice python3 v4/cli/forgery_battery.py                           # 21 classes
nice python3 v4/cli/check_fifo_surface.py --self-test   # 18 + 5 structural, 17 arms x 2 engines
nice python3 v4/cli/check_golden.py --self-test          # 7 walk + 17 reject + 1 structural
nice python3 v4/cli/coverage_harness.py --self-test      # 19 controls
nice python3 v4/cli/sensitivity_gate.py --self-test      # 14 modes + 2 inversions + 6 structural
nice python3 v4/cli/throughput_harness.py --self-test   # 12 cases + 4 structural
nice python3 v4/cli/resource_harness.py --self-test      # 25 control groups
nice python3 v4/cli/crash_harness.py --self-test         # 26 controls, eight groups
nice python3 v4/cli/windows_guard_harness.py --self-test # 39 total (38 on POSIX + 1 native-only) + 10 verify
nice python3 v4/cli/command_sanitize.py --self-test      # 58 registry controls
nice python3 v4/cli/check_producer_privacy.py --self-test # 40 producer controls
nice python3 v4/cli/windows_housekeeping_harness.py --self-test  # incl. 9 report-verification controls
nice python3 v4/cli/races/runner.py --self-test          # 22 mutation (15 arm, 7 detector) + 6 committed-report writer controls
                                                         # + clean-arm/clean-detector/report-location

# Re-check one committed Windows report without re-running the harness:
# the same verifier the battery uses, applied to the artifact on disk.
nice python3 v4/cli/windows_housekeeping_harness.py \
  --verify-report v4/cli/evidence/windows-housekeeping.json
```

`--verify-report` exists because a reader, not only a producer, must be
able to re-grade an accepted artifact: it recomputes the identity
bindings, the ledger digests and the privacy block from the stored bytes.
The parity gate additionally re-derives its grid, pin table, pressure
table and rollups from the captured per-cell records, so a report whose
summary disagrees with its own cells fails even when every number in it
is plausible.

### Identity records: what they are and how they get here

`build-ids.json` (`iprange-cli-build-ids-v1`) and
`battery-manifest.json` are the two committed files that record identity
instead of a measurement, and both are owned by the
`command_sanitize.py` entry of `COMMITTED_REPORT_WRITERS`
(`owner: lead`, tier `shared-writer`).  They are listed there — and
`--audit-committed-reports` enumerates this directory against that
table — because an identity record from an unregistered producer is
exactly as untrustworthy as a measurement that bypassed the writer.

- `build-ids.json` is produced by `command_sanitize.py --emit-build-ids
  <dest>`.  It holds the expected `IPRANGE_V4_BUILD_ID` of one source
  state for the `iprange-livedb` package: the SHA-256 that
  `v4/rust/iprange-livedb/build.rs` computes over the package's
  `Cargo.toml` and every `.rs` file under its `src/`, one record per
  input (`u64le(name-length) name u64le(content-length) content`) with
  the name framed as the `/`-joined logical path.  The four host entries
  carry one digest, which is the claim under test: the value the built
  binaries embed is the one the CLI and the co-located
  `iprange-v4-worker` compare during the worker handshake, and hashing
  the build host's own path spelling instead would split it between a
  POSIX and a Windows checkout.  The artifact therefore also quotes the
  two pre-normalizer digests (equal on a POSIX host, apart on a Windows
  host), names its input count, and records whether the expected digest
  was found verbatim in a `--built-cli` and a `--built-worker`
  executable — `"not measured"` for a role nobody supplied, so a digest
  that was never compared against a binary attests to a source tree and
  not to a delivered product.  Nothing here is hand-edited: the record
  regenerates from the package, and it is stale the moment that package
  changes.
- `battery-manifest.json` is authored by
  `check_kind_coverage.py --emit-manifest` and reaches this directory by
  `command_sanitize.py --commit-report <src> --commit-report-to <dest>`,
  because the gate that owns the manifest's shape cannot import the
  writer.  Promotion adds the provenance and privacy members and nothing
  else: the report bytes stay the gate's, and the recorded `command`
  names the promoting invocation, which is what distinguishes a promoted
  artifact from a writer that committed its own measurement.  That verb
  refuses a destination registered to another writer and refuses source
  and destination that name the same file, so a promotion cannot
  launder a file an existing producer owns.

Reading either record is a check, not a trust exercise: the build
identity is re-derivable from the package by re-running `--emit-build-ids`
to a scratch path and comparing, and both files carry the same
`git_head`/`privacy` binding as the reports they describe.
