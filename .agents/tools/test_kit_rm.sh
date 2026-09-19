#!/usr/bin/env bash
# Adversarial test for .agents/tools/kit-rm.py: every attack must be REFUSED
# and one legitimate scratch tree must be ALLOWED. A destructive tool whose
# guards never fire is worse than no tool, so both directions are asserted.
set -uo pipefail
KIT=/home/costa/src/firehol/iprange
TOOL="$KIT/.agents/tools/kit-rm.py"
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

# live-run directory (non-terminal state) pointing at this sandbox repo
A="$T/async/runningone"; mkdir -p "$A"
printf '{"state":"running","cwd":"%s"}\n' "$R" > "$A/status.json"

LIST(){ printf '%s\n' "$@" > "$T/list"; }
run(){ # run <expect DRY|KEEP|rc-N> <label> <args...>
  local expect="$1" label="$2"; shift 2
  local out rc
  out=$("$PYBIN" "$K" "$@" --runs-root "$T/async" 2>&1); rc=$?
  echo "$out" > "$T/last.out"
  case "$expect" in
    KEEP) grep -q '^KEEP' "$T/last.out" && ok "$label refused" || bad "$label NOT refused (rc=$rc): $(tail -1 "$T/last.out")";;
    DRY)  grep -q '^DRY' "$T/last.out" && ok "$label allowed" || bad "$label NOT allowed (rc=$rc): $(tail -1 "$T/last.out")";;
    RC2)  [ "$rc" = 2 ] && ok "$label refused at parse (rc 2)" || bad "$label rc=$rc";;
  esac
}
PYBIN=python3

echo "--- A: path-shape attacks ---"
LIST "$L";                              run KEEP "G2 .local itself"                --list "$T/list"
LIST "$R";                              run KEEP "G2 repo root"                    --list "$T/list"
LIST "$L/..";                           run KEEP "G2 above .local"                 --list "$T/list"
LIST "/";                               run KEEP "G1 / "                           --list "$T/list"
LIST "/etc";                            run KEEP "G2 /etc"                         --list "$T/list"
LIST ".local/perf/w1";                  run KEEP "G1 relative"                     --list "$T/list"
LIST "$L/perf/w1*";                     run KEEP "G1 glob"                         --list "$T/list"
LIST "$L/perf/w1/";                     run KEEP "G1 trailing space/sep"           --list "$T/list"
# a list whose only content is whitespace is an empty list: the tool must
# refuse to act on it (rc 2), not silently proceed
LIST "  ";
"$PYBIN" "$K" --list "$T/list" --runs-root "$T/async" >/dev/null 2>&1
[ $? = 2 ] && ok "blank-only list refused (rc 2)" || bad "blank-only list not refused"

echo "--- B: role roots and the protected kit ---"
LIST "$L/perf";                         run KEEP "G3 role root"                    --list "$T/list"
LIST "$L/shared";                       run KEEP "G4 central kit"                  --list "$T/list"
LIST "$L/shared/evidence/round16";      run KEEP "G4 evidence dir"                 --list "$T/list"
LIST "$L/_attic-md";                    run KEEP "G4 attic"                        --list "$T/list"
LIST "$L/_attic-md/ex";                 run KEEP "G4 inside attic"                 --list "$T/list"

echo "--- C: symlink and type attacks ---"
ln -s "$L/perf/w1" "$L/eviltarget" 2>/dev/null
LIST "$L/eviltarget";                   run KEEP "G1/G5 symlink as path"           --list "$T/list"
ln -s "$L/shared" "$T/link" 2>/dev/null
LIST "$L/perf/w1/../../../.local/shared"; run KEEP "G1 .. traversal to shared"     --list "$T/list"
printf 'f\n' > "$L/perf/w1/plainfile"
LIST "$L/perf/w1/plainfile";            run KEEP "G5 regular file"                 --list "$T/list"
LIST "$L/perf/nope";                    run KEEP "G5 missing"                      --list "$T/list"

echo "--- D: content attacks (G6) ---"
LIST "$L/parity/clone";                 run KEEP "G6 git checkout"                 --list "$T/list"
# the fixture directly, and its parent: both must be refused
LIST "$L/parity/r2/p2/d1/unreadable";   run KEEP "G6 the mode-000 fixture itself"   --list "$T/list"
mkdir -p "$L/perf/w2/with000" && chmod 000 "$L/perf/w2/with000"
LIST "$L/perf/w2/with000";              run KEEP "G6 dir holding a mode-000 child"  --list "$T/list"
chmod 755 "$L/perf/w2/with000" 2>/dev/null; rmdir "$L/perf/w2/with000" 2>/dev/null
mkdir -p "$L/parity/r2/p2/d1"           # parent of the standing fixture
LIST "$L/parity/r2/p2";                 run KEEP "G6/G7 above the privacy fixture" --list "$T/list"

echo "--- E: gate-artifact reference (G7) ---"
LIST "$L/w1926-gate/tdir";              run KEEP "G7 named by status.md"           --list "$T/list"
# the same path cited REPO-RELATIVELY (how manifest reasons and SOW prose do it)
printf 'the replay used %s under the kit\n' ".local/w1926-rel/inner" >> "$L/shared/status.md"
mkdir -p "$L/w1926-rel/inner"
LIST "$L/w1926-rel";                    run KEEP "G7 named relatively"            --list "$T/list"
# an ANCESTOR of a cited path must also be refused: removing the parent would
# destroy the path the record depends on. The ancestors used here are at
# depth >= 2, so a refusal is G7 and not G3's role-root rule.
mkdir -p "$L/g7deep/a/b/c"
printf 'bound replay read %s\n' ".local/g7deep/a/b/c" >> "$L/shared/status.md"
LIST "$L/g7deep/a/b";                   run KEEP "G7 parent of a cited dir"        --list "$T/list"
LIST "$L/g7deep/a";                     run KEEP "G7 grandparent of a cited dir"   --list "$T/list"
# an unrelated sibling of a cited dir must still be allowed (no blanket block)
mkdir -p "$L/g7deep/other/cache"
LIST "$L/g7deep/other";                 run DRY  "uncited sibling still allowed"   --list "$T/list"

echo "--- F: live-run guard (G8) ---"
LIST "$L/perf/w1/cargo-target";         run KEEP "G8 live run in this repo"        --list "$T/list"
# same list, with the live run marked terminal: the positive control
printf '{"state":"complete","cwd":"%s"}\n' "$R" > "$A/status.json"
LIST "$L/perf/w1/cargo-target";         run DRY  "positive control: real scratch"  --list "$T/list"
# a live run belonging to ANOTHER repo must not block us forever
printf '{"state":"running","cwd":"/somewhere/else"}\n' "$R" > "$A/status.json"
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
printf '{"state":"complete","cwd":"%s"}\n' "$R" > "$A/status.json"
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
