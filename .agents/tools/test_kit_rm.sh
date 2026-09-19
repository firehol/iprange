#!/usr/bin/env bash
# Adversarial test for .agents/tools/kit-rm.py: every attack must be REFUSED
# and one legitimate scratch tree must be ALLOWED. A destructive tool whose
# guards never fire is worse than no tool, so both directions are asserted.
set -uo pipefail
KIT=/home/costa/src/firehol/iprange
# Overridable so the mutation driver can run this suite against a reverted
# copy of the tool: a guard that cannot be shown to fail is not a guard.
TOOL=${KITRM_TOOL:-$KIT/.agents/tools/kit-rm.py}
T=$(mktemp -d /tmp/kitrm-test.XXXXXX); trap 'rm -rf "$T"' EXIT
fail=0
ok(){ echo "ok   $1"; }
bad(){ echo "FAIL $1"; fail=1; }

# Sandbox repo that mirrors the real layout so attacks are real, not mocked.
R="$T/repo"; mkdir -p "$R/.agents/tools" "$R/.git" "$R/.local/shared/evidence/round16"
cp "$TOOL" "$R/.agents/tools/kit-rm.py"
K="$R/.agents/tools/kit-rm.py"
L="$R/.local"

# legitimate scratch: a build cache inside a role sandbox
mkdir -p "$L/perf/w1/cargo-target/debug/deps" && head -c 200000 /dev/zero > "$L/perf/w1/cargo-target/debug/deps/x.o"
# the privacy fixture the gate signal depends on
mkdir -p "$L/parity/r2/p2/d1/unreadable" && chmod 000 "$L/parity/r2/p2/d1/unreadable"
printf 'x\n' > "$L/shared/evidence/round16/manifest.json"
printf 'state\n' > "$L/shared/status.md"
printf 'sha\n' > "$L/shared/head"
mkdir -p "$L/_attic-md/ex"
# a real git checkout inside a sandbox
mkdir -p "$L/parity/clone/.git" && printf 'x' > "$L/parity/clone/.git/HEAD"
# a path referenced by a gate artifact. The referenced path must not be a
# prefix of any other path in the fixture, or a substring match would make
# refusal correct for the wrong reason.
printf 'cite %s for the tdir scratch\n' "$L/w1926-gate/tdir" >> "$L/shared/status.md"
mkdir -p "$L/w1926-gate/tdir/inner"

# Live-run directory for the G8 guard. Deliberately EMPTY for sections A-E2:
# check() evaluates G8 before G6/G7, so an armed run here would refuse every
# path behind G8's back and make the G6/G7 legs pass without their guards ever
# running (found by mutation at batch 8: a tool with G6 and G7 deleted passed
# 52/52). Section F arms and clears the run explicitly for the G8 legs.
A="$T/async"; mkdir -p "$A"

LIST(){ printf '%s\n' "$@" > "$T/list"; }
run(){ # run <expect DRY|KEEP:<guard-token>|rc-N> <label> <args...>
  # KEEP pins the guard token that must cause the refusal: a KEEP leg that
  # passes on ANY refusal cannot tell which guard fired, so deleting a guard
  # would keep the suite green (the wave-20 "pin that cannot fail" class).
  local expect="$1" label="$2"; shift 2
  local out rc
  out=$("$PYBIN" "$K" "$@" --runs-root "$T/async" 2>&1); rc=$?
  echo "$out" > "$T/last.out"
  case "$expect" in
    KEEP:*) grep -q "^KEEP    ${expect#KEEP:}" "$T/last.out" && ok "$label refused" || bad "$label NOT refused by ${expect#KEEP:} (rc=$rc): $(tail -1 "$T/last.out")";;
    DRY)  grep -q '^DRY     would remove' "$T/last.out" && ok "$label allowed" || bad "$label NOT allowed (rc=$rc): $(tail -1 "$T/last.out")";;
    RC2)  [ "$rc" = 2 ] && ok "$label refused at parse (rc 2)" || bad "$label rc=$rc";;
  esac
}
PYBIN=python3

echo "--- A: path-shape attacks ---"
LIST "$L";                              run KEEP:G2\ outside "G2 .local itself"    --list "$T/list"
LIST "$R";                              run KEEP:G2\ outside "G2 repo root"        --list "$T/list"
LIST "$L/..";                           run KEEP:G1\ path\ is\ not "G2 above .local (non-normalised)" --list "$T/list"
LIST "/";                               run KEEP:G2\ outside "G2 /"               --list "$T/list"
LIST "/etc";                            run KEEP:G2\ outside "G2 /etc"             --list "$T/list"
LIST ".local/perf/w1";                  run KEEP:G1\ not\ a\ literal "G1 relative" --list "$T/list"
LIST "$L/perf/w1*";                     run KEEP:G1\ not\ a\ literal "G1 glob"     --list "$T/list"
LIST "$L/perf/w1/";                     run KEEP:G1\ path\ is\ not "G1 trailing space/sep" --list "$T/list"
# a list whose only content is whitespace is an empty list: the tool must
# refuse to act on it (rc 2), not silently proceed
LIST "  ";
"$PYBIN" "$K" --list "$T/list" --runs-root "$T/async" >/dev/null 2>&1
[ $? = 2 ] && ok "blank-only list refused (rc 2)" || bad "blank-only list not refused"

echo "--- B: role roots and the protected kit ---"
LIST "$L/perf";                         run KEEP:G3\ role\ root "G3 role root"      --list "$T/list"
LIST "$L/shared";                       run KEEP:G3\ role\ root "G3 shared is a role root" --list "$T/list"
LIST "$L/shared/evidence/round16";      run KEEP:G4\ central\ kit "G4 evidence dir" --list "$T/list"
LIST "$L/_attic-md";                    run KEEP:G3\ role\ root "G3 attic is a role root" --list "$T/list"
LIST "$L/_attic-md/ex";                 run KEEP:G4\ central\ kit "G4 inside attic" --list "$T/list"

echo "--- C: symlink and type attacks ---"
ln -s "$L/perf/w1" "$L/eviltarget" 2>/dev/null
LIST "$L/eviltarget";                   run KEEP:G1\ path\ is\ not "G1 symlink as path" --list "$T/list"
ln -s "$L/shared" "$T/link" 2>/dev/null
LIST "$L/perf/w1/../../../.local/shared"; run KEEP:G1\ path\ is\ not "G1 .. traversal to shared" --list "$T/list"
printf 'f\n' > "$L/perf/w1/plainfile"
LIST "$L/perf/w1/plainfile";            run KEEP:G5\ not\ a\ directory "G5 regular file" --list "$T/list"
LIST "$L/perf/nope";                    run KEEP:G5\ not\ a\ directory "G5 missing" --list "$T/list"

echo "--- D: content attacks (G6) ---"
LIST "$L/parity/clone";                 run KEEP:G6\ contains\ a\ git\ entry "G6 git checkout" --list "$T/list"
# the fixture directly, and its parent: both must be refused
LIST "$L/parity/r2/p2/d1/unreadable";   run KEEP:G6\ the\ target\ is\ itself\ mode-000 "G6 the mode-000 fixture itself" --list "$T/list"
mkdir -p "$L/perf/w2/with000/child" && chmod 000 "$L/perf/w2/with000/child"
LIST "$L/perf/w2/with000";              run KEEP:G6\ contains\ a\ mode-000\ fixture "G6 dir holding a mode-000 child" --list "$T/list"
chmod 755 "$L/perf/w2/with000/child" 2>/dev/null; rmdir "$L/perf/w2/with000/child" 2>/dev/null; rmdir "$L/perf/w2/with000" 2>/dev/null
mkdir -p "$L/parity/r2/p2/d1"           # parent of the standing fixture
LIST "$L/parity/r2/p2";                 run KEEP:G6\ contains\ a\ mode-000\ fixture "G6 above the privacy fixture" --list "$T/list"

echo "--- E: gate-artifact reference (G7) ---"
LIST "$L/w1926-gate/tdir";              run KEEP:G7\ related\ to\ cited\ path "G7 named by status.md" --list "$T/list"
# the same path cited REPO-RELATIVELY (how manifest reasons and SOW prose do
# it), at depth >= 2 so G3 cannot be what refuses it
printf 'the replay used %s under the kit\n' ".local/w1926-rel/inner" >> "$L/shared/status.md"
mkdir -p "$L/w1926-rel/inner"
LIST "$L/w1926-rel/inner";              run KEEP:G7\ related\ to\ cited\ path "G7 named relatively" --list "$T/list"
# the needle scan (not the citation set) must also refuse: the token
# extractor captures only `.local/<role>/<sub>...` tokens, so a citation under
# a role name containing '+' yields just the bare role (skipped as a
# citation). Only the byte-needle scan can see this path.
printf 'the manifest cites %s verbatim\n' ".local/w1926+gate/inner" >> "$L/shared/status.md"
mkdir -p "$L/w1926+gate/inner"
LIST "$L/w1926+gate/inner";             run KEEP:G7\ named\ by\ gate\ artifact "G7 needle scan on a token-extractor miss" --list "$T/list"
# an ANCESTOR of a cited path must also be refused: removing the parent would
# destroy the path the record depends on. The ancestors used here are at
# depth >= 2, so a refusal is G7 and not G3's role-root rule.
mkdir -p "$L/g7deep/a/b/c"
printf 'bound replay read %s\n' ".local/g7deep/a/b/c" >> "$L/shared/status.md"
LIST "$L/g7deep/a/b";                   run KEEP:G7\ related\ to\ cited\ path "G7 parent of a cited dir" --list "$T/list"
LIST "$L/g7deep/a";                     run KEEP:G7\ related\ to\ cited\ path "G7 grandparent of a cited dir" --list "$T/list"
# an unrelated sibling of a cited dir must still be allowed (no blanket block)
mkdir -p "$L/g7deep/other/cache"
LIST "$L/g7deep/other";                 run DRY  "uncited sibling still allowed"   --list "$T/list"

echo "--- E2: G7 protects cited CONTENT, not just the cited directory (wave-19 security P1) ---"
# A record naming `.local/<role>/kit` depends on everything under it, so a
# descendant must be refused exactly as the directory itself is. The needle scan
# cannot see this: the record never spells out the deeper path.
mkdir -p "$L/g7sub/kit/deepcache"
printf 'the proof read .local/g7sub/kit during staging\n' >> "$L/shared/status.md"
LIST "$L/g7sub/kit/deepcache";          run KEEP:G7\ related\ to\ cited\ path "G7 descendant of a cited dir" --list "$T/list"
LIST "$L/g7sub/kit";                    run KEEP:G7\ related\ to\ cited\ path "G7 the cited dir itself" --list "$T/list"
mkdir -p "$L/g7sub/sibling/cache"
LIST "$L/g7sub/sibling";                run DRY  "uncited sibling still removable"  --list "$T/list"

echo "--- E3: exact-path guarantee, no whitespace stripping (wave-19 portability P2-1) ---"
# `strip()` on a list line made --execute on "<dir> " remove the DIFFERENT
# directory "<dir>" and log the stripped name, so a listed path and a destroyed
# path could differ while the run reported success.
mkdir -p "$L/spam/w1 " "$L/spam/w1"
printf 'listed\n'  > "$L/spam/w1 /IMPORTANT.txt"
printf 'unlisted\n' > "$L/spam/w1/IMPORTANT.txt"
printf '%s\n' "$L/spam/w1 " > "$T/list"
# This case deletes for real. The runs directory is empty here (section F arms
# it explicitly), so G8 finds no live run and the removal proceeds.
"$PYBIN" "$K" --list "$T/list" --runs-root "$T/async" --execute --reason "selftest" > "$T/ws.out" 2>&1
if [ ! -d "$L/spam/w1 " ] && [ -d "$L/spam/w1" ]; then
  ok "removed the LISTED trailing-space dir and left its stripped twin"
else
  bad "exact-path guarantee broken: listed_present=$([ -d "$L/spam/w1 " ] && echo Y || echo N) twin_present=$([ -d "$L/spam/w1" ] && echo Y || echo N)"
fi
if grep -qF "spam/w1 " "$L/shared/removals.log"; then
  ok "removals.log recorded the true trailing-space path"
else
  bad "removals.log lost the trailing space"
fi
# a whitespace-only line is a malformed list, refused rather than dropped
printf '   \n' > "$T/wsblank"
"$PYBIN" "$K" --list "$T/wsblank" --runs-root "$T/async" >/dev/null 2>&1
[ $? = 2 ] && ok "whitespace-only list line refused (rc 2)" || bad "whitespace-only line not refused"

echo "--- E4: an unwritable tree is refused BEFORE anything is deleted (wave-19 portability P3-2) ---"
mkdir -p "$L/roperm/cap/inner"
printf 'k1\n' > "$L/roperm/cap/f1"; printf 'k2\n' > "$L/roperm/cap/inner/f2"
chmod 555 "$L/roperm/cap/inner" "$L/roperm/cap"
LIST "$L/roperm/cap";                   run KEEP:G6\ the\ target\ is\ not\ writable "G6 unwritable: refuse first" --list "$T/list"
if [ -f "$L/roperm/cap/f1" ] && [ -f "$L/roperm/cap/inner/f2" ]; then
  ok "unwritable tree left completely intact"
else
  bad "PARTIAL DELETION on an unwritable tree"
fi
chmod -R 755 "$L/roperm/cap" 2>/dev/null

echo "--- E5: a byte-invalid list line fails closed and does not stop the run (wave-20 portability P3-1) ---"
# The path bytes must reach the guards intact; only the output channels may
# substitute. A bare encode() raised inside the removal loop, i.e. after other
# paths had already been deleted, so this asserts: no traceback, the good line
# still evaluated, and a non-destructive outcome.
mkdir -p "$L/gxb/ok"
python3 -c "
with open('$T/list','wb') as f:
    f.write(b'$L/gxb/ok\n')
    f.write(b'$L/gxb/bad\xffname\n')
"
out=$("$PYBIN" "$K" --list "$T/list" --runs-root "$T/async" 2>&1); rc=$?
printf '%s\n' "$out" | grep -q Traceback && bad "E5 traceback on a byte-invalid line" || ok "E5 no traceback (rc $rc)"
printf '%s\n' "$out" | grep -q "gxb/ok" && ok "E5 the clean line was still evaluated" || bad "E5 clean line skipped"
[ -d "$L/gxb/ok" ] && ok "E5 nothing removed while a line was undisplayable" || bad "E5 removed data on a malformed line"

echo "--- F: live-run guard (G8) ---"
# live_runs() scans <runs-root>/*/status.json, so the status lives in a run dir
mkdir -p "$A/one"
printf '{"state":"running","cwd":"%s"}\n' "$R" > "$A/one/status.json"
LIST "$L/perf/w1/cargo-target";         run KEEP:G8\ live\ subagent "G8 live run in this repo" --list "$T/list"
# same list, with the live run marked terminal: the positive control
printf '{"state":"complete","cwd":"%s"}\n' "$R" > "$A/one/status.json"
LIST "$L/perf/w1/cargo-target";         run DRY  "positive control: real scratch"  --list "$T/list"
# a live run belonging to ANOTHER repo must not block us forever
printf '{"state":"running","cwd":"/somewhere/else"}\n' "$R" > "$A/one/status.json"
LIST "$L/perf/w1/cargo-target";         run DRY  "foreign live run does not block" --list "$T/list"

echo "--- G: destructive-mode gates ---"
LIST "$L/perf/w1/cargo-target"
"$PYBIN" "$K" --list "$T/list" --execute >/dev/null 2>&1; rc=$?
[ "$rc" = 2 ] && ok "--execute without --reason refused" || bad "--execute without --reason rc=$rc"
"$PYBIN" "$K" --list "$T/list" --execute --reason "   " >/dev/null 2>&1; rc=$?
[ "$rc" = 2 ] && ok "--execute with blank --reason refused" || bad "blank reason rc=$rc"
"$PYBIN" "$K" --list "$T/nonexistent" >/dev/null 2>&1; rc=$?
[ "$rc" = 2 ] && ok "missing list file refused" || bad "missing list rc=$rc"
: > "$T/empty"; "$PYBIN" "$K" --list "$T/empty" >/dev/null 2>&1; rc=$?
[ "$rc" = 2 ] && ok "empty list refused" || bad "empty list rc=$rc"

echo "--- H: nothing was actually removed by any of the above ---"
[ -d "$L/perf/w1/cargo-target" ] && ok "legit scratch still on disk after dry runs" || bad "DRY RUN REMOVED DATA"
[ -d "$L/shared/evidence/round16" ] && ok "evidence intact" || bad "EVIDENCE REMOVED"
[ -d "$L/parity/r2/p2/d1/unreadable" ] && ok "privacy fixture intact" || bad "FIXTURE REMOVED"

echo "--- I: real removal removes ONLY the listed path, and logs it ---"
printf '{"state":"complete","cwd":"%s"}\n' "$R" > "$A/one/status.json"
mkdir -p "$L/perf/w3/cargo-target" "$L/perf/w3/keepme" && printf 'k\n' > "$L/perf/w3/keepme/f"
LIST "$L/perf/w3/cargo-target"
"$PYBIN" "$K" --list "$T/list" --execute --reason "selftest" > "$T/exec.out" 2>&1; rc=$?
grep -q '^REMOVED' "$T/exec.out" && ok "executed removal reported" || bad "no REMOVED line: $(tail -2 "$T/exec.out")"
[ ! -e "$L/perf/w3/cargo-target" ] && ok "listed scratch gone" || bad "STILL PRESENT after removal"
[ -d "$L/perf/w3/keepme" ] && ok "sibling kept" || bad "SIBLING COLLATERAL DAMAGE"
[ -f "$L/shared/removals.log" ] && grep -q 'selftest' "$L/shared/removals.log" && ok "removal logged with reason" || bad "not logged"

echo
[ $fail = 0 ] && echo "SUITE: all guards proven" || echo "SUITE: FAILURES ABOVE"
exit $fail
