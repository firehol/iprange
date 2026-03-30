# TL;DR

Purpose: make `iprange` fit for safe maintenance by first building a comprehensive test suite that exercises its real features and failure modes, then adding sanitizer-backed test execution to prove concrete bugs, and only then fixing bugs that have been reproduced by tests.

Current sub-purpose: verify `iprange` against the exact way `../firehol/sbin/update-ipsets` uses it today and add coverage for every distinct invocation pattern, stdout contract, exit-code contract, and stderr-sensitive operational path that matters to `update-ipsets`.

User requirements:
- Build a full test suite that exercises `iprange` features.
- Add ASAN or equivalent tooling to detect illegal memory accesses and related undefined behavior.
- Prove each bug with a test before fixing it.
- Do not fix anything that has not been proven with a test first.
- Perform a complete code review as part of the work.

# Analysis

Initial state:
- TODO created before implementation, per project process.
- Repository already contains a shell-based CLI regression suite in `tests.d/` with 29 tests and a simple top-level runner in `run-tests.sh`.
- Existing tests currently pass on a default autotools build.
- The project CI path in `.github/workflows/publish.yml` uses autotools (`./configure && make check`), not CMake.
- `make check` is not currently wired to the shell suite in `run-tests.sh`, so the existing tests are not part of the normal automake test flow.
- The CMake path is currently broken on a fresh checkout: `CMakeLists.txt` lists `config.h` as a source file even though it is generated, so configure/generate fails before build.

Current feature coverage from existing tests:
- Covered: merge/common/exclude/diff, file lists, directory loading, compare, compare-next, range output, binary round-trip, empty inputs, invalid inputs, nonexistent paths, special filenames, and some count cases.
- Missing or weakly covered: compare-first, count-unique merged/header cases, count-unique-all, has-compare/has-reduce, valid/invalid default prefix, min-prefix, explicit prefixes selection, print-single-ips, split IP/net prefix-suffix controls, quiet diff, hostname/DNS path, reduce mode, help/version exit semantics, non-default build variants, and sanitizer-backed regressions.

Build and sanitizer facts gathered:
- Default autotools build succeeds with `../configure --disable-man && make`.
- Non-default autotools build fails with `../configure --disable-man --without-compare-with-common && make` because of `compips` typo at `iprange.c:874`.
- ASAN+UBSAN build succeeds with clang using `-fsanitize=address,undefined`.
- UBSAN reproduces signed-shift UB in `set_bit()` with a minimal CLI case (`echo 1.2.3.4 | iprange`).
- UBSAN reproduces invalid `--default-prefix 64` handling via negative shift in `netmask()`.
- `/0` counting is functionally wrong today: `printf '0.0.0.0/0\n' | iprange -C` prints `1,0`.
- `--help` and `--version` currently exit with status `1`.
- `--count-unique-all` currently emits unexpected stderr (`Is already optimized ...`) even without `-v`, due to unconditional logging in `ipset_optimize()`.
- Diff debug output confirms wrong filename assignment in the multi-file second set path: the merged left-hand side is mislabeled as `ipset B`.

Bug validation status:
- Proven already:
  - Signed left-shift UB in `iprange.h` (`set_bit()` / `netmask()` paths).
  - Invalid `--default-prefix` UB.
  - Non-default `--without-compare-with-common` build failure.
  - `/0` unique IP count overflow.
  - `--help` / `--version` wrong exit status.
  - Wrong diff debug labeling when multiple files exist on the second side.
  - `--count-unique-all` emits unexpected stderr on already-optimized inputs.
  - Binary output on broken pipe exits successfully instead of surfacing a write failure.
  - Extra UB in `ip2str_r()` from signed left shifts on `0xff << 24`.
- Needs direct unit-level proof rather than CLI-only proof:
  - Empty logical ipset handling in `ipset_common()`, `ipset_diff()`, `ipset_exclude()`.
  - Empty-ipset optimize leak path.
  - `ipset_optimize()` malloc-failure / freed-object error path.
- Needs decision based on proof quality:
  - Broken-pipe output handling is externally observable (current binary pipeline returns success), but the desired exit semantics must be encoded carefully in tests.

Additional review findings from the current post-fix audit:
- Proven with direct reproduction:
  - `ipset_binary.c:103-111` can be driven to heap-buffer-overflow with a crafted binary header: `entries_max` wraps in `ipset_grow_internal()` (`ipset.c:83-85`) and `fread()` then writes more records than the wrapped allocation can hold.
  - `ipset.c:65-76` / `ipset.c:49-54` contains a real use-after-free in `ipset_free_all()`: recursive sibling frees are followed by `ipset_free()` relinking through already-freed neighbors.
  - `ipset_binary.c:141` writes `lines` as `ips->entries` instead of `ips->lines`, so binary round-trips corrupt the original input line count and break `--count-unique-all`.
  - `ipset_load.c:164-175` plus `ipset_load.c:546-571` leaves DNS counters/state global across successive `ipset_load()` calls, so later non-DNS files can emit stale DNS summary/progress output.
- Intentionally not promoted to findings in this audit:
  - Clang `-Weverything` also reports several portability/style warnings (missing prototypes, sign conversions, overlong usage string, dead stores), but none of these were promoted without a concrete correctness or safety failure.

Fresh independent review findings to address in the next evidence-driven round:
- Proven with manual reproduction or fault injection:
  - `ipset_load.c:230-260` plus `ipset_load.c:549-585`: if `pthread_create()` fails for the first DNS worker, the queued request remains pending and `dns_done()` waits forever.
  - `iprange.c:653-664` and `iprange.c:723-733`: `@directory` and `@filelist` prepend each loaded ipset to the chain, so file-list order is reversed and directory order depends on raw `readdir()` enumeration.
  - `ipset_load.c:66-146`: malformed digit-prefixed input falls back to `parse_hostname()` on the original line, so invalid IP-like text is silently treated as a hostname and can trigger DNS.
  - `iprange.c:669-676` and `iprange.c:739-745`: empty `@directory` / `@filelist` prints an error but still exits successfully when other inputs are present, yielding partial output with status 0.
- Analyzer notes reviewed and intentionally not promoted:
  - `clang --analyze` reports only dead stores around `iprange.c:989` and the DNS progress snapshots in `ipset_load.c:579-581`.
  - `gcc -fanalyzer` reported no additional correctness bugs in the current tree.

Latest post-fix review findings:
- Proven with direct reproduction:
  - `ipset_load.c:85-106` now rejects some numeric-leading dotted hostnames such as `1.example.com` as invalid input, even though the existing hostname parser in `ipset_load.c:31-68` still accepts that hostname shape. This is a regression introduced by the malformed-IP fallback hardening.
  - Plain out-of-tree builds remain fragile when the source tree already contains top-level object files: after an in-tree build creates `iprange.o`, `ipset.o`, etc., a fresh `mkdir build && cd build && ../configure && make` jumps straight to link and fails because the object files are searched via VPATH but linked from the build directory. The harness now avoids this by copying to a clean source snapshot, but the underlying build defect remains.

# Decisions

User decisions already made:
- Tests first, fixes second.
- Sanitizer-backed execution is required.
- Scope includes comprehensive feature coverage, targeted regression tests for proven bugs, and source fixes only for bugs reproduced by tests.
- Fix the 4 additional proven findings from the post-fix audit as part of this task, but still only after adding tests that prove them.
- Proceed with the next evidence-driven round on the remaining findings: prove/fix the `ipset_optimize()` OOM path if reproducible, investigate the DNS threading claim with tooling/stress before changing it, and do not patch the weaker `strcpy` / `lineid` claims without proof.

Pending decisions:
- None identified yet.

Decisions made for the next fixes:
- `@filelist` must preserve the exact user-listed order, because `--compare-first` and CSV/reporting modes treat positional order as semantic input.
- `@directory` must be deterministic. Since raw `readdir()` order is unspecified, directory entries will be normalized to lexical filename order before loading.
- Invalid IP-like syntax that begins with digits but does not match a valid IP/CIDR/range must be treated as invalid input, not silently reclassified as a hostname.
- Empty `@filelist` / `@directory` is a hard input failure and must produce a non-zero exit instead of partial success.
- DNS worker creation failure must fail the current load cleanly without leaving pending requests behind or hanging in `dns_done()`.
- Numeric-leading dotted hostnames that fit the existing hostname grammar must remain valid hostname input; the parser hardening should only block truly malformed IP/CIDR/range syntax.
- Plain out-of-tree builds must work even if the source tree previously had in-tree build artifacts, or at minimum the build system must avoid treating source-tree object files as valid prerequisites for VPATH builds.

# Plan

1. Inspect repository structure, build system, and existing tests.
2. Review feature surface and map required functional coverage.
3. Reproduce reported issues and classify them into normal CLI regressions, build-variant regressions, sanitizer regressions, and internal unit regressions.
4. Extend the existing shell harness instead of replacing it, so current tests remain valid while new coverage is added.
5. Add missing functional coverage for the existing CLI surface.
6. Add regression tests that fail on current code and prove each target bug.
7. Add ASAN/UBSAN/LSAN-backed execution scripts and unit tests for internal failure modes that the CLI does not expose cleanly.
8. Fix only the bugs covered by failing tests.
9. Re-run the full suite under default and sanitized builds.
10. Summarize residual risks, uncovered areas, and any issues that could not be proven automatically.
11. Add regression coverage for the 4 newly proven issues, confirm failure on current code, then fix them and rerun the full suite.
12. Build proof for the remaining high-confidence error-path finding (`ipset_optimize()` malloc failure), and investigate whether the remaining review claims are real defects or should be downgraded/closed.
13. Add regression coverage for the 4 fresh independent findings from the latest manual review:
    - DNS thread-create failure hang
    - reversed / unstable `@filelist` and `@directory` ordering
    - malformed IP-like input misclassified as hostname
    - empty `@filelist` / `@directory` returning success with partial output
14. Implement only the fixes proven by those new regressions.
15. Re-run the default, build-variant, sanitizer, and unit suites after the new fixes.
16. Add proof coverage for the two latest findings:
    - numeric-leading dotted hostnames remain hostname input, not parse errors
    - VPATH/out-of-tree builds still succeed after the source tree has been dirtied by a prior in-tree build
17. Implement only the fixes proven by those two new regressions.
18. Re-run the full verification stack again after those fixes.

Implemented test work:
- Extended `run-tests.sh` to support multiple test roots and explicit binary selection.
- Added `run-build-tests.sh`, `run-sanitizer-tests.sh`, and `run-unit-tests.sh`.
- Wired `make check` to the shell suite and added `make check-sanitizers`.
- Added 16 new CLI regression/feature tests under `tests.d/`.
- Added 1 build regression test under `tests.build.d/`.
- Added 2 sanitizer CLI regressions under `tests.sanitizers.d/`.
- Added 2 sanitizer unit tests under `tests.unit/`.
- Added 2 more CLI regressions under `tests.d/` for binary count round-trip preservation and DNS state reset between sequential loads.
- Added 1 sanitizer CLI regression under `tests.sanitizers.d/` for crafted binary header overflow.
- Added 1 more sanitizer unit regression under `tests.unit/` for `ipset_free_all()` use-after-free.
- Extended sanitizer coverage to include TSAN in `run-sanitizer-tests.sh`.
- Added 1 more ASAN/UBSAN regression under `tests.sanitizers.d/` for the `ipset_optimize()` malloc-failure path.
- Added 1 TSAN regression under `tests.tsan.d/` for DNS queue/counter races during repeated hostname resolution.
- Added 4 more CLI regressions under `tests.d/` for malformed IP-like parser fallback, preserved file-list order, deterministic directory order, and empty grouped-input failure semantics.
- Added 1 more sanitizer fault-injection regression under `tests.sanitizers.d/` for DNS thread creation failure without hangs.
- Added 1 more CLI regression under `tests.d/` for numeric-leading dotted hostname parsing.
- Added 2 more CLI regressions under `tests.d/` for numeric-leading hyphen hostname parsing and malformed CIDR-like input rejection.
- Added 3 more CLI regressions under `tests.d/` for malformed hostname-like input rejection, strict numeric CLI validation, and malformed binary metadata validation.
- Added 1 more build regression under `tests.build.d/` for out-of-tree builds after the source tree already contains top-level object files.

Implemented fixes after test proof:
- Replaced signed shift operations with unsigned arithmetic in `iprange.h`.
- Added `--default-prefix` validation with clean error handling.
- Changed `--help` and `--version` to exit with status 0.
- Fixed the `compips` typo in the non-default compare build path.
- Promoted `unique_ips` accounting to 64-bit and fixed `/0` counting.
- Fixed empty logical ipset handling in `ipset_common()`, `ipset_diff()`, and `ipset_exclude()`.
- Fixed `ipset_optimize()` behavior for already-optimized and empty ipsets, eliminating noisy stderr and the empty-optimize leak.
- Fixed wrong diff debug labeling for multi-file right-hand-side merges.
- Added checked binary output writes so broken pipes return failure.
- Fixed binary save/load metadata so `lines` survives round-trips.
- Hardened binary loading against oversized crafted record counts that previously wrapped allocation growth and caused heap overwrite under ASAN.
- Fixed `ipset_free_all()` to free linked ipsets without relinking through already-freed siblings.
- Reset DNS batch counters/state after each completed load so later non-DNS files do not print stale DNS summaries/progress.
- Fixed the `ipset_optimize()` malloc-failure path so it reports the allocation failure without freeing the ipset first and then dereferencing freed memory.
- Fixed DNS synchronization by:
  - making request-queue wait/signal use the same mutex as the request queue itself,
  - removing unlocked reads of the request and reply queues,
  - snapshotting DNS progress/stat counters under lock before using them in `dns_done()`,
  - resetting shared DNS state under lock after each batch.
- Fixed malformed digit-prefixed input handling so invalid IP-like text is reported as invalid input instead of falling back to hostname parsing and triggering DNS.
- Refined that parser hardening so numeric-leading dotted hostnames (for example `1.example.invalid`) still take the hostname/DNS path, while malformed complete IPv4/CIDR tokens with junk suffixes remain invalid.
- Preserved `@filelist` load order and made `@directory` expansion deterministic by sorting directory file paths lexically before loading.
- Changed empty `@filelist` / `@directory` from partial-success warnings to hard input failures with non-zero exit status.
- Fixed the DNS thread-create error path so a failed first worker creation does not leave pending requests behind or hang in `dns_done()`; the load now fails cleanly.
- Updated the verification harness to build from a temporary source snapshot, isolating out-of-tree build and sanitizer runs from source-tree object-file artifacts.
- Hardened the autotools build so a VPATH/out-of-tree build still compiles local object files even when the source tree already contains top-level object artifacts from an earlier in-tree build.
- Tightened numeric-leading token classification in `ipset_load.c` so:
  - full-line hostname candidates such as `1-foo.example.invalid` and `1-2.example.invalid` still take the hostname/DNS path,
  - malformed slash-tailed CIDR-like input such as `1.2.3.4/24.example.invalid` is rejected as invalid input instead of being truncated to hostname `1.2.3.4`.
- Tightened hostname parsing in `ipset_load.c` so malformed hostname-like lines no longer reach DNS at all:
  - empty hostnames are rejected,
  - malformed hostnames with trailing junk such as `foo!bar`, `foo/bar`, and `foo bar` are rejected instead of being truncated to `foo`.
- Added strict numeric parsing in `iprange.c` for `--min-prefix`, `--default-prefix`, `--dns-threads`, `--ipset-reduce`, and `--reduce-entries`, so invalid or partial numeric input now fails fast.
- Added strict binary metadata parsing in `ipset_binary.c`, rejecting malformed numeric fields instead of accepting partial parses such as `record size 8garbage` or `records 0x10`.

Residual issues intentionally not changed yet:
- The `strcpy`-vs-`strncpy` concern still appears to be a fragility/style report rather than a demonstrated overflow path in this checkout, so it remains intentionally unchanged.
- The extremely large file-list / input `lineid` overflow concern remains theoretically real but still lacks a practical proof test in this task, so it remains unchanged.

Current next-step analysis:
- `ipset_optimize.c:58-62` is now proven/fixed with an ASAN fault-injection regression.
- The generic DNS thread-race claim was too vague, but TSAN proved two real races in the DNS implementation:
  - unlocked reply-queue reads in `dns_process_replies()`
  - unlocked request-queue / counter reads around `dns_request_get()` and `dns_done()`
  These are now fixed and covered by the TSAN regression.
- The `strcpy` claim still appears to be about fragility rather than an actual overflow path in this checkout.
- `lineid` overflow remains theoretically real but low-severity and should be treated as hardening unless a practical proof path is added.
- The latest independent review adds 4 consumer-visible defects that are not covered yet:
  - a DNS error-path hang,
  - positional-order corruption for grouped file inputs,
  - surprising DNS resolution of malformed IP-like tokens,
  - success exit codes on empty grouped inputs.
  These require new regressions before any implementation changes.
- The latest review after those fixes found:
  - one parser regression introduced by the malformed-IP hardening,
  - one remaining build-system defect that the test harness currently masks by using a clean source snapshot.
  These also require tests before changes.
- The latest review found 3 new evidence-backed issues:
  - `ipset_binary.c` accepts malformed numeric metadata in binary headers because `atol()` / `strtoull(..., NULL, 10)` are used without checking whether parsing consumed the whole field.
  - `ipset_load.c` still routes malformed non-hostname lines to DNS: `parse_hostname()` returns hostname success even when it parsed zero hostname characters, and it also truncates malformed hostnames such as `foo!bar` to `foo`.
  - `iprange.c` still accepts invalid numeric CLI option values silently for `--dns-threads`, `--ipset-reduce`, and `--reduce-entries`.
- The latest review found 3 more evidence-backed issues:
  - `ipset_combine.c` still trusts `ips1->entries + ips2->entries` and the resulting `memcpy()` sizes without overflow or bounds validation; a sanitizer harness proves heap corruption on wrapped counts.
  - `ipset_merge.c` has the same unchecked-count bug and additionally triggers pointer arithmetic UB when `to->entries + add->entries` overflows.
  - `ipset_binary.c` accepts trailing garbage after the declared binary payload instead of rejecting the file as malformed.
- The latest review found 3 additional evidence-backed issues:
  - `ipset_binary.c` still trusts the binary file's `optimized` flag and precomputed `lines` / `unique ips` metadata, so a crafted file can report arbitrary counts and skip normalization of overlapping or duplicate records.
  - `iprange.c` directory loading skips only subdirectories and will try to open FIFOs and other non-regular entries discovered via `@directory`, which can hang the process.
  - `ipset_load.c` logs invalid input lines but does not propagate failure to the process exit status; the CLI still exits `0` and may emit partial output after parse errors.
- The latest review found 3 more evidence-backed issues:
  - `ipset_copy.c` still trusts `ips1->entries` blindly and can trigger a heap-buffer-overflow on invalid internal state, just like the earlier `ipset_combine()` / `ipset_merge()` bugs.
  - User-facing `entries` columns still report `ips->lines`, so crafted binary metadata can still forge visible counts even after payload validation.
  - DNS hostname resolution failures still do not affect process exit status; unresolved hostnames are logged, but the command still exits `0` and may print `0,0` counts.
- Latest CI follow-up on PR `#37`:
  - First CI follow-up failure was in `tests.d/45-broken-pipe-output`: GitHub Actions emitted an extra stderr line (`iprange: cannot write binary output: Broken pipe`) while the harness compares combined stdout+stderr. That was fixed by capturing pipeline stderr inside the test and asserting only the real contract: non-zero exit on broken pipe.
  - After that fix, GitHub Actions still failed in `run-build-tests.sh`:
    - `tests.build.d/01-without-compare-with-common` copied configured-tree outputs (`Makefile`, `config.h`, `config.status`) into the temporary source snapshot, so out-of-tree `configure` aborted with `source directory already configured; run "make distclean" there first`.
    - `tests.build.d/02-vpath-build-after-in-tree-build` was reproduced locally by first dirtying the source tree with `./configure --disable-man && make -j1 iprange`. In that state, the temporary source snapshot also carried `local-build-objects.stamp` from the earlier in-tree build. During the temp VPATH build, GNU make reused that stale source-side stamp, so the fake source-tree `*.o` files stayed newer and the build skipped local compilation, failing directly at link time with missing `iprange.o`, `ipset.o`, and the rest.
  - Local verification also exposed a harness-cleanup bug in `run-tests.sh`: when `IPRANGE_BIN` points to another binary and a real non-symlink `./iprange` already exists, `prepare_iprange_link()` aborts as intended, but the `cleanup()` trap still treats the original state as `missing` and removes the real top-level `./iprange` file on exit. This is proven by rebuilding `./iprange`, running `IPRANGE_BIN="$PWD/build-default/iprange" ./run-tests.sh`, observing the expected refusal (`cannot replace existing non-symlink ...`), and then seeing that `./iprange` has been deleted.

# Implied Decisions

- Prefer repository-native tooling and coding style over introducing heavyweight new infrastructure unless the current project lacks a reasonable test path.
- Treat sanitizer findings as test evidence when the failing behavior is memory safety or undefined behavior related.
- Use direct unit tests for library-level empty-set and fault-injection cases when CLI coverage would hide the bug instead of proving it.
- Keep autotools as the authoritative test/build path for this task because that is the path the repository CI already uses today.
- Keep bug fixes minimal in surface area but complete in behavior once proven.
- For DNS-related parsing, malformed input must fail as input validation and must not enqueue DNS work. Valid hostname lines must keep working, including the previously proven numeric-leading hostname cases.
- Numeric CLI parameters must use strict parsing: non-numeric text, trailing junk, negatives where unsupported, and out-of-range values must fail fast with a non-zero exit.
- Binary metadata fields must be parsed strictly and reject trailing junk, empty numeric fields, and overflow.
- Internal set-combination helpers must reject impossible entry counts cleanly instead of corrupting memory. For this task, `ipset_combine()` should fail with `NULL`, and `ipset_merge()` should surface an error to its callers so the CLI can exit cleanly.
- Binary files must be consumed exactly: extra bytes after the declared payload are malformed input and must be rejected.
- Binary files must not be trusted blindly: if an input file claims it is optimized, its records and counters still need to be validated against the actual payload before the in-memory ipset is treated as optimized or its counters are trusted.
- `@directory` auto-discovery must only load regular files. FIFOs, sockets, devices, and other special entries should be skipped so the command cannot hang on directory contents.
- Input parse errors are fatal for the overall command. Once a file contains invalid lines, the process must exit non-zero instead of silently returning success with partial or empty results.
- Internal copy helpers must reject impossible entry counts cleanly instead of reading past allocated buffers.
- User-facing `entries` columns must report actual optimized entry counts, not stored `lines` metadata. This closes the remaining forged-binary reporting path without changing the binary file format.
- DNS hostname resolution failures are fatal for the current input. If any requested hostname cannot be resolved, the command must fail non-zero instead of silently succeeding with an empty or partial result.
- Compatibility with `update-ipsets` is defined by the current observed behavior of `../firehol/sbin/update-ipsets`, not by historical `iprange` behavior in isolation.
- If a generic existing test is close but does not prove the exact stdout field order, empty-stdout behavior, or exit-code meaning that `update-ipsets` depends on, add a dedicated compatibility regression instead of assuming coverage.
- The build-test source snapshot must be clean from configured-tree outputs and build-local stamp artifacts. Otherwise the harness stops testing the intended out-of-tree behavior and starts reproducing CI pollution from earlier in-tree configure/build steps.
- The test harness must never remove a real top-level `./iprange` binary when it aborts while trying to switch binaries for a test run. Cleanup must only restore state that was actually changed by the harness.

# Testing Requirements

- Functional coverage for parsing, normalization, set operations, formatting, and command-line modes.
- Regression coverage for each proven bug before fixing it.
- Sanitized execution for memory safety and undefined behavior detection.
- Normal execution to ensure no regressions in standard builds.
- Dedicated regression coverage for:
  - `@filelist` preserving listed order,
  - `@directory` producing deterministic lexical order,
  - malformed digit-prefixed input being rejected without DNS fallback,
  - empty grouped inputs failing with non-zero status,
  - DNS thread creation failure failing cleanly instead of hanging.
- Additional regression coverage for:
  - numeric-leading dotted hostnames reaching the hostname/DNS path,
  - dirty-source-tree out-of-tree builds succeeding without depending on source-tree object files.
- CI follow-up verification requirements:
  - Reproduce the GitHub Actions build-test failure locally by first dirtying the source tree with an in-tree `./configure --disable-man && make -j1 iprange`.
  - Re-run `./run-build-tests.sh` after the harness fix and confirm both build tests pass from that configured-tree state.
  - Re-run `make -C build-default check` so the automake `check-local` path exercises the updated build-test harness.
  - Re-run `IPRANGE_BIN="$PWD/build-default/iprange" ./run-tests.sh` from a state where a real top-level `./iprange` exists and confirm the harness refuses safely without deleting it.
- Additional regression coverage for:
  - malformed binary files lying about `lines` / `unique ips`,
  - malformed binary files claiming `optimized` while containing duplicate or overlapping records,
  - `@directory` skipping FIFOs and other special files instead of hanging,
  - invalid input lines causing a non-zero process exit, including mixed valid+invalid input.
- Additional regression coverage for:
  - internal `ipset_copy()` invalid-state overflow rejection,
  - count/compare outputs using real entry counts even when binary metadata lies about `lines`,
  - unresolved hostnames causing non-zero exit, including mixed resolvable and unresolvable hostname input.
- Compatibility coverage required for `update-ipsets` specifically:
  - `--has-reduce` exit-status probe
  - `--has-directory-loading` exit-status probe
  - `--print-binary` success path as used for history/latest/retention slots
  - `--union-all` over multiple binary history slots
  - `--exclude-next` plain-text to `-C` pipeline
  - `--exclude-next ... --print-binary` success path
  - `--common ... --print-binary` success path
  - `-C` exact `entries,ips` stdout contract
  - `--count-unique-all` exact `name,entries,ips` stdout contract
  - `--compare` exact 8-column CSV stdout contract
  - `--compare-next` exact 8-column CSV stdout contract for both explicit file arrays and `@directory`
  - `--diff --quiet` empty-stdout plus exit-code contract
  - default normalization mode in stdout pipelines
  - `-1` single-IP mode in stdout pipelines
  - `--print-prefix` output shape compatible with `ipset restore`
  - `-1 --dns-threads ... --dns-silent [--dns-progress]` hostname-resolver contract
- Verified end-to-end with:
  - `IPRANGE_BIN="$PWD/build-default/iprange" ./run-tests.sh`
  - `./run-sanitizer-tests.sh`
  - `./run-build-tests.sh`
  - `make -C build-default check`
  - `make -C build-default check-sanitizers`

Latest verification notes:
- Final `clang --analyze` on `iprange.c` and `ipset_load.c` still reports only:
  - dead-store noise in `dns_done()` progress/stat snapshots,
  - one low-confidence `unix.Stream` warning on the hostname-request error path that is not reproduced by runtime testing.
- `gcc -fanalyzer` reports no additional findings on those files.
- Manual parser repro on the latest tree still shows one remaining classification bug:
  - fixed: `1-foo.example.invalid` and `1-2.example.invalid` now follow the hostname/DNS path, matching the existing hostname grammar.
  - fixed: `1.2.3.4/24.example.invalid` is now rejected as invalid input instead of being partially accepted as hostname `1.2.3.4`.
- The `clang --analyze` dead-store reports in `dns_done()` are real dead assignments: the first loop snapshots of `retries`, `replies_found`, and `replies_failed` are overwritten before any read. They are cleanup noise, not a reproduced runtime defect.
- Re-verified after the parser fix:
  - `clang --analyze -DHAVE_CONFIG_H -Ibuild-default -I. ipset_load.c` still reports only the same 3 dead stores in `dns_done()` and the same low-confidence `unix.Stream` warning on the early hostname-error path.
  - `gcc -fanalyzer` remains clean on the reviewed files.
- Latest repros to cover before implementation:
  - fixed: malformed hostname-like input no longer reaches DNS; those lines are now rejected as parse errors.
  - fixed: invalid numeric CLI input for `--dns-threads`, `--ipset-reduce`, `--reduce-entries`, and `--min-prefix` is now rejected instead of being silently coerced.
  - fixed: malformed binary metadata values with trailing junk, missing digits, or hex-like strings are now rejected instead of being partially parsed.
  - `make -C build-default check`
  - `make -C build-default check-sanitizers`
- Final verification after these fixes:
  - `IPRANGE_BIN="$PWD/build-default/iprange" ./run-tests.sh`
  - `./run-build-tests.sh`
  - `./run-sanitizer-tests.sh`
  - `make -C build-default check`
  - `make -C build-default check-sanitizers`
- Final analyzer status remains:
  - `clang --analyze -DHAVE_CONFIG_H -Ibuild-default -I. iprange.c ipset_load.c ipset_binary.c` reports only the same 3 dead stores in `dns_done()` and the same low-confidence `unix.Stream` warning on the early hostname-error path.
  - `gcc -fanalyzer` remains clean on the reviewed files.
- New repros to prove before implementation:
  - a temporary ASAN harness that sets wrapped `entries` counts crashes in `ipset_combine()` at `memcpy()`.
  - a temporary ASAN/UBSAN harness that sets wrapped `entries` counts crashes in `ipset_merge()` with pointer overflow and heap-buffer-overflow.
  - a valid generated binary file with extra bytes appended after the payload is still accepted and printed successfully.
- New repros to prove before implementation:
  - a crafted one-record binary file with `lines 999` and `unique ips 999` is accepted and `--count-unique-all` prints those fake counts.
  - a crafted binary file marked `optimized` but containing duplicate or overlapping ranges is accepted and printed without normalization.
  - `timeout 2 ./build-default/iprange "@dir"` hangs with exit `124` when `dir` contains a FIFO alongside a normal file.
  - invalid input such as `!` prints a parse error but the process still exits `0`, and mixed valid+invalid input still emits partial output with exit `0`.
- Proven with regression tests before implementation:
  - `tests.d/59-binary-semantic-validation` failed because forged binary `unique ips` metadata and forged `optimized` claims were accepted.
  - `tests.d/60-directory-skips-special-files` failed because `@directory` hung on a FIFO and timed out with exit `124`.
  - `tests.d/61-invalid-input-exit-status` failed because invalid input still returned exit `0`.
- Implemented in this round:
  - `ipset_binary.c` now validates the binary payload semantically:
    - rejects records with `addr > broadcast`,
    - recomputes the actual unique-IP union from the payload and rejects mismatched `unique ips` metadata,
    - rejects files that claim `optimized` while the payload is unsorted, overlapping, or adjacent.
    - note: `lines` is still preserved metadata because the original source-line count is not derivable from the binary payload; changing that would be a separate binary-format / reporting semantics decision.
  - `iprange.c` directory auto-discovery now loads only regular files and skips FIFOs and other special entries.
  - `ipset_load.c` now marks parse errors as fatal for the current input and returns failure after cleanup, so the CLI exits non-zero instead of silently succeeding.
  - Older parser regressions were updated to the new fatal-parse-error contract:
    - `tests.d/18-invalid-input`
    - `tests.d/48-invalid-ip-does-not-fallback-dns`
    - `tests.d/54-invalid-cidr-like-input`
    - `tests.d/55-invalid-hostname-input`
- Final verification after this round:
  - `IPRANGE_BIN="$PWD/build-default/iprange" ./run-tests.sh` passed with 61/61 tests.
  - `./run-build-tests.sh` passed with 2/2 tests.
  - `./run-sanitizer-tests.sh` passed with 5/5 sanitizer CLI tests, 5/5 unit tests, and 1/1 TSAN tests.
  - `make -C build-default check` passed.
  - `make -C build-default check-sanitizers` passed.
- Final analyzer status after this round:
  - `clang --analyze -DHAVE_CONFIG_H -Ibuild-default -I. ipset_binary.c ipset_load.c iprange.c` still reports only the same 3 dead stores in `dns_done()` and the same low-confidence `unix.Stream` warning on the hostname-request path.
  - `gcc -fanalyzer` remains clean on the reviewed files.
- Proven with regression tests before implementation:
  - `tests.unit/copy_overflow.c` failed under ASAN in `ipset_copy.c:19` with a heap-buffer-overflow when `entries > entries_max`.
  - `tests.d/62-binary-count-uses-actual-entries` showed `--count-unique` still printed forged binary `lines` metadata as `entries`.
  - `tests.d/63-compare-uses-actual-entries` showed compare CSV output still printed forged binary `lines` metadata under `entries1`.
  - `tests.d/64-dns-failure-exit-status` showed unresolved hostnames still exited `0` and could print `0,0` count output.
- Implemented in this round:
  - `ipset_copy.c` now rejects invalid internal entry counts instead of reading past the source allocation.
  - User-facing CSV/count `entries` fields in `iprange.c` now report actual optimized entry counts, not stored `lines` metadata.
  - Compare/count paths now prepare counts through `ipset_unique_ips()` so `entries` and `unique_ips` come from the actual optimized in-memory set.
  - `ipset_load.c` / `dns_done()` now treat hostname resolution failures as fatal for the current input and return non-zero to the CLI instead of silently succeeding.
  - Updated existing expectations that intentionally changed:
    - `tests.d/30-compare-first/output`
    - `tests.d/31-count-unique-merged/output`
    - `tests.d/32-count-unique-all/output`
    - `tests.d/49-filelist-order-preserved/output`
    - `tests.d/50-directory-order-deterministic/output`
    - `tests.d/52-numeric-leading-hostname/cmd.sh`
    - `tests.d/53-numeric-leading-hyphen-hostname/cmd.sh`
- Additional tests added in this round:
  - `tests.unit/copy_overflow.c`
  - `tests.d/62-binary-count-uses-actual-entries`
  - `tests.d/63-compare-uses-actual-entries`
  - `tests.d/64-dns-failure-exit-status`
- Final verification after this round:
  - `IPRANGE_BIN="$PWD/build-default/iprange" ./run-tests.sh` passed with 64/64 tests.
  - `./run-build-tests.sh` passed with 2/2 tests.
  - `./run-sanitizer-tests.sh` passed with 5/5 sanitizer CLI tests, 6/6 unit tests, and 1/1 TSAN tests.
  - `make -C build-default check` passed.
  - `make -C build-default check-sanitizers` passed.
- Final analyzer status after this round:
  - `clang --analyze -DHAVE_CONFIG_H -Ibuild-default -I. ipset_copy.c ipset_load.c iprange.c` still reports only the same 3 dead stores in `dns_done()` and the same low-confidence `unix.Stream` warning on the hostname-request path.
  - `gcc -fanalyzer` remains clean on the reviewed files.
- New compatibility audit against `../firehol/sbin/update-ipsets`:
  - `update-ipsets` uses `iprange` as a shell-tool contract, not as a library.
  - Concrete call patterns traced from `../firehol/sbin/update-ipsets`:
    - capability probes: `--has-reduce`, `--has-directory-loading`
    - binary/history paths: `--print-binary`, `--union-all`, `--exclude-next ... --print-binary`, `--common ... --print-binary`
    - count/reporting paths: `-C`, `--count-unique-all`, `--compare`, `--compare-next`
    - equality check path: `--diff --quiet`
    - kernel-restore generation paths: default normalization mode, `-1`, `--print-prefix`, `--ipset-reduce`, `--ipset-reduce-entries`
    - DNS helper path: `-1 --dns-threads ... --dns-silent [--dns-progress]`
  - `update-ipsets` does not appear to parse specific `iprange` stderr strings. A search in `../firehol/sbin/update-ipsets` found no consumers of messages such as `DNS:` or `Cannot understand line`.
  - The compatibility contract that matters is:
    - stdout shape must stay stable where `update-ipsets` parses it,
    - exit codes must match the shell branching logic,
    - stdout must stay clean in pipelines,
    - stderr noise is acceptable only where `update-ipsets` intentionally wants DNS progress or operator-visible errors.
  - The most sensitive stdout contracts are:
    - `-C` must be `entries,ips`
    - `--count-unique-all` must be `name,entries,ips`
    - `--compare` and `--compare-next` must be `name1,name2,entries1,entries2,ips1,ips2,combined,common`
    - `--diff --quiet` must emit no stdout and signal equality via exit code `0`
  - The most sensitive exit-code contracts are:
    - capability probes return `0` only when the feature is supported
    - transformation commands used with `|| ipset_error ...` return non-zero on failure
    - `--diff --quiet` returns `0` only for identical sets
  - Compatibility gaps still need to be mapped test-by-test against the current suite before any code changes are made for this round.
  - Compatibility coverage mapping after the audit:
    - already covered before this round:
      - `--has-reduce`
      - `--has-directory-loading`
      - `--count-unique-all`
      - `--compare-next`
      - `--diff --quiet` difference path
    - added in this round because the exact `update-ipsets` invocation pattern was not previously proven:
      - `--union-all` on binary history slots
      - `--exclude-next | -C`
      - `--exclude-next ... --print-binary | -C`
      - `--common ... --print-binary | -C`
      - exact `--compare` 8-column CSV
      - exact `@directory` retention CSV without headers
      - `--ipset-reduce ... --ipset-reduce-entries ... --print-prefix`
      - `-1 file --print-prefix`
      - exact `-C` contract on the apply path
      - `-1 --dns-threads ... --dns-silent`
      - `-1 --dns-threads ... --dns-silent --dns-progress`
      - `--diff --quiet` equality path with empty stdout/stderr
  - Implemented in the `update-ipsets` compatibility round:
    - added 7 exact compatibility regressions:
      - `tests.d/65-update-ipsets-union-all`
      - `tests.d/66-update-ipsets-retention-binary-ops`
      - `tests.d/67-update-ipsets-compare-contract`
      - `tests.d/68-update-ipsets-directory-retention-contract`
      - `tests.d/69-update-ipsets-apply-contract`
      - `tests.d/70-update-ipsets-dns-helper-contract`
      - `tests.d/71-update-ipsets-diff-quiet-equal`
    - adjusted one new test after proving that `update-ipsets` suppresses `--has-reduce` stderr and therefore depends only on its exit code, not on quiet stderr.
    - no `iprange` source changes were required in this round: the current implementation is compatible with the traced `update-ipsets` invocations once these exact contracts are tested.
  - Final verification after the `update-ipsets` compatibility round:
    - isolated new compatibility tests passed 7/7
    - `IPRANGE_BIN="$PWD/build-default/iprange" ./run-tests.sh` passed with 71/71 tests
    - `./run-build-tests.sh` passed with 2/2 tests
    - `./run-sanitizer-tests.sh` passed with 5/5 sanitizer CLI tests, 6/6 unit tests, and 1/1 TSAN tests
    - `make -C build-default check` passed
    - `make -C build-default check-sanitizers` passed
  - CI follow-up after publishing PR #37:
    - GitHub Actions failed in `Build package` on `tests.d/45-broken-pipe-output`.
    - Root cause: the test expected a single stdout line only, but on the GitHub runner `iprange` also emitted `iprange: cannot write binary output: Broken pipe` on stderr, and the shell harness compares combined stdout/stderr.
    - Fix: `tests.d/45-broken-pipe-output/cmd.sh` now captures the pipeline stderr internally and asserts the real contract that matters:
      - the pipeline must return non-zero on broken pipe
      - the test output itself must stay stable across runner-specific stderr behavior
    - Re-verified with:
      - `cd tests.d/45-broken-pipe-output && ./cmd.sh`
      - `make -C build-default check`
  - CI follow-up after publishing commit `26aea98`:
    - PR `#37` still has a failing `Build package` job after the broken-pipe test stabilization.
    - Next step is to inspect the latest Actions logs again, reproduce the current failure locally, and patch only the CI-specific breakage if it is test-only.
  - Latest CI root cause after inspecting the new failing runs:
    - the main CLI suite now passes 71/71 in GitHub Actions.
    - the failure has moved to `run-build-tests.sh`, specifically both `tests.build.d` cases.
    - `tests.build.d/01-without-compare-with-common` fails in CI because it configures directly against a source directory that has already been configured earlier in the workflow, triggering:
      - `configure: error: source directory already configured; run "make distclean" there first`
    - `tests.build.d/02-vpath-build-after-in-tree-build` still fails in CI with the original symptom:
      - link step runs without local object compilation
      - `/usr/bin/ld: cannot find iprange.o` and the rest of the objects
    - next step is to inspect the build-test harness/scripts and make them match the CI environment exactly before changing core build logic again.
  - Implemented for the CI follow-up:
    - `tests.build.d/01-without-compare-with-common` now excludes configured-tree outputs (`Makefile`, `config.h`, `config.log`, `config.status`, `config.cache`, `iprange.spec`, `stamp-h1`) and the build-local `local-build-objects.stamp` from the temporary source snapshot.
    - `tests.build.d/02-vpath-build-after-in-tree-build` now excludes the same configured/build-local artifacts, so the temp VPATH build no longer inherits stale source-side state from an earlier in-tree build.
    - added `tests.build.d/03-run-tests-safe-refusal` to prove that `run-tests.sh` does not delete an existing non-symlink `iprange` file when it aborts while trying to switch binaries.
    - `run-tests.sh` now restores/removes the top-level `iprange` link only if it actually changed it during setup.
  - Reproduced and verified locally after the fix:
    - first reproduced the CI state with:
      - `./configure --disable-man`
      - `make -j1 iprange`
    - then verified:
      - `./run-build-tests.sh`
      - `tests.build.d/03-run-tests-safe-refusal/cmd.sh`
      - `make -j1 iprange && make check`
    - result: all passed, including the exact source-root `make check` path used in GitHub Actions.

# Documentation Updates Required

- Determine after inspecting current docs/help/manpages/build instructions whether test or sanitizer workflow documentation needs updating.
- No user-facing documentation change is required from the `update-ipsets` compatibility round because the work only added regression coverage and did not change `iprange` behavior.
