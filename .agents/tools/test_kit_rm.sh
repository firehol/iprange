#!/usr/bin/env bash
# Adversarial test for .agents/tools/kit-rm.py: every attack must be REFUSED
# and one legitimate scratch tree must be ALLOWED. A destructive tool whose
# guards never fire is worse than no tool, so both directions are asserted.
# H4-LABEL-HASH: ce0ab7b804424a26b06dcef7493db24d30ffb7d5c0a89210764005324d678701
# sha256 over this suite's green ok-label multiset (sorted, newline-joined),
# declared by the suite itself and pinned against the bound log by the kit-gc
# suite's H4 reverse direction (batch 10; replaces batch 9's scalar count,
# which wave 23 forged by delete+duplicate at unchanged count). Any leg added,
# removed, renamed or reworded must update this line in the same edit.
set -uo pipefail
# E6 exec_modules the tool for the live_runs unit probe; keep bytecode caches
# out of policed dirs (H2).
export PYTHONDONTWRITEBYTECODE=1
KIT=/home/costa/src/firehol/iprange
# Overridable so the mutation driver can run this suite against a reverted
# copy of the tool: a guard that cannot be shown to fail is not a guard.
TOOL=${KITRM_TOOL:-$KIT/.agents/tools/kit-rm.py}
T=$(mktemp -d /tmp/kitrm-test.XXXXXX); trap 'rm -rf "$T"' EXIT
fail=0
ok(){ echo "ok   $1"; }
bad(){ echo "FAIL $1"; fail=1; }

# Sandbox repo that mirrors the real layout so attacks are real, not mocked.
# A REAL git repo: gate_artifacts() now refuses when `git ls-files` fails
# (wave-26 tester), so a fake .git would refuse every leg for the wrong
# reason. The tracked file carries no `.local/` token, so it adds no G7
# citation and cannot make any refusal correct for the wrong reason.
command -v git >/dev/null || { echo "FAIL git is required by this suite"; exit 1; }
R="$T/repo"; mkdir -p "$R/.agents/tools" "$R/.local/shared/evidence/round16"
cp "$TOOL" "$R/.agents/tools/kit-rm.py"
K="$R/.agents/tools/kit-rm.py"
L="$R/.local"
printf 'sandbox\n' > "$R/README.md"
git -C "$R" -c init.defaultBranch=main init -q
git -C "$R" -c user.name=suite -c user.email=suite@invalid add README.md
git -C "$R" -c user.name=suite -c user.email=suite@invalid commit -q -m fixture

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
LIST "$L";                              run "KEEP:G2 outside" "G2 .local itself"    --list "$T/list"
LIST "$R";                              run "KEEP:G2 outside" "G2 repo root"        --list "$T/list"
LIST "$L/..";                           run "KEEP:G1 path is not" "G2 above .local (non-normalised)" --list "$T/list"
LIST "/";                               run "KEEP:G2 outside" "G2 /"               --list "$T/list"
LIST "/etc";                            run "KEEP:G2 outside" "G2 /etc"             --list "$T/list"
LIST ".local/perf/w1";                  run "KEEP:G1 not a literal" "G1 relative" --list "$T/list"
LIST "$L/perf/w1*";                     run "KEEP:G1 not a literal" "G1 glob"     --list "$T/list"
LIST "$L/perf/w1/";                     run "KEEP:G1 path is not" "G1 trailing space/sep" --list "$T/list"
# a list whose only content is whitespace is an empty list: the tool must
# refuse to act on it (rc 2), not silently proceed
LIST "  ";
"$PYBIN" "$K" --list "$T/list" --runs-root "$T/async" >/dev/null 2>&1
[ $? = 2 ] && ok "blank-only list refused (rc 2)" || bad "blank-only list not refused"

echo "--- B: role roots and the protected kit ---"
LIST "$L/perf";                         run "KEEP:G3 role root" "G3 role root"      --list "$T/list"
LIST "$L/shared";                       run "KEEP:G3 role root" "G3 shared is a role root" --list "$T/list"
LIST "$L/shared/evidence/round16";      run "KEEP:G4 central kit" "G4 evidence dir" --list "$T/list"
LIST "$L/_attic-md";                    run "KEEP:G3 role root" "G3 attic is a role root" --list "$T/list"
LIST "$L/_attic-md/ex";                 run "KEEP:G4 central kit" "G4 inside attic" --list "$T/list"

echo "--- C: symlink and type attacks ---"
ln -s "$L/perf/w1" "$L/eviltarget" 2>/dev/null
LIST "$L/eviltarget";                   run "KEEP:G1 path is not" "G1 symlink as path" --list "$T/list"
ln -s "$L/shared" "$T/link" 2>/dev/null
LIST "$L/perf/w1/../../../.local/shared"; run "KEEP:G1 path is not" "G1 .. traversal to shared" --list "$T/list"
printf 'f\n' > "$L/perf/w1/plainfile"
LIST "$L/perf/w1/plainfile";            run "KEEP:G5 not a directory" "G5 regular file" --list "$T/list"
LIST "$L/perf/nope";                    run "KEEP:G5 not a directory" "G5 missing" --list "$T/list"

echo "--- D: content attacks (G6) ---"
LIST "$L/parity/clone";                 run "KEEP:G6 contains a git entry" "G6 git checkout" --list "$T/list"
# the fixture directly, and its parent: both must be refused
LIST "$L/parity/r2/p2/d1/unreadable";   run "KEEP:G6 the target is itself mode-000" "G6 the mode-000 fixture itself" --list "$T/list"
mkdir -p "$L/perf/w2/with000/child" && chmod 000 "$L/perf/w2/with000/child"
LIST "$L/perf/w2/with000";              run "KEEP:G6 contains a mode-000 fixture" "G6 dir holding a mode-000 child" --list "$T/list"
chmod 755 "$L/perf/w2/with000/child" 2>/dev/null; rmdir "$L/perf/w2/with000/child" 2>/dev/null; rmdir "$L/perf/w2/with000" 2>/dev/null
mkdir -p "$L/parity/r2/p2/d1"           # parent of the standing fixture
LIST "$L/parity/r2/p2";                 run "KEEP:G6 contains a mode-000 fixture" "G6 above the privacy fixture" --list "$T/list"

echo "--- E: gate-artifact reference (G7) ---"
LIST "$L/w1926-gate/tdir";              run "KEEP:G7 related to cited path" "G7 named by status.md" --list "$T/list"
# the same path cited REPO-RELATIVELY (how manifest reasons and SOW prose do
# it), at depth >= 2 so G3 cannot be what refuses it
printf 'the replay used %s under the kit\n' ".local/w1926-rel/inner" >> "$L/shared/status.md"
mkdir -p "$L/w1926-rel/inner"
LIST "$L/w1926-rel/inner";              run "KEEP:G7 related to cited path" "G7 named relatively" --list "$T/list"
# the needle scan (not the citation set) must also refuse: the token
# extractor captures only `.local/<role>/<sub>...` tokens, so a citation under
# a role name containing '+' yields just the bare role (skipped as a
# citation). Only the byte-needle scan can see this path.
printf 'the manifest cites %s verbatim\n' ".local/w1926+gate/inner" >> "$L/shared/status.md"
mkdir -p "$L/w1926+gate/inner"
LIST "$L/w1926+gate/inner";             run "KEEP:G7 named by gate artifact" "G7 needle scan on a token-extractor miss" --list "$T/list"
# an ANCESTOR of a cited path must also be refused: removing the parent would
# destroy the path the record depends on. The ancestors used here are at
# depth >= 2, so a refusal is G7 and not G3's role-root rule.
mkdir -p "$L/g7deep/a/b/c"
printf 'bound replay read %s\n' ".local/g7deep/a/b/c" >> "$L/shared/status.md"
LIST "$L/g7deep/a/b";                   run "KEEP:G7 related to cited path" "G7 parent of a cited dir" --list "$T/list"
LIST "$L/g7deep/a";                     run "KEEP:G7 related to cited path" "G7 grandparent of a cited dir" --list "$T/list"
# an unrelated sibling of a cited dir must still be allowed (no blanket block)
mkdir -p "$L/g7deep/other/cache"
LIST "$L/g7deep/other";                 run DRY  "uncited sibling still allowed"   --list "$T/list"

echo "--- E2: G7 protects cited CONTENT, not just the cited directory (wave-19 security P1) ---"
# A record naming `.local/<role>/kit` depends on everything under it, so a
# descendant must be refused exactly as the directory itself is. The needle scan
# cannot see this: the record never spells out the deeper path.
mkdir -p "$L/g7sub/kit/deepcache"
printf 'the proof read .local/g7sub/kit during staging\n' >> "$L/shared/status.md"
LIST "$L/g7sub/kit/deepcache";          run "KEEP:G7 related to cited path" "G7 descendant of a cited dir" --list "$T/list"
LIST "$L/g7sub/kit";                    run "KEEP:G7 related to cited path" "G7 the cited dir itself" --list "$T/list"
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
# Two arms refuse an unwritable tree and BOTH must be pinned: the target-level
# arm (the listed dir itself unwritable) and the subtree arm (a descendant
# unwritable while the target is writable). The subtree arm is the one that
# prevents an UNLOGGED partial deletion: with the target writable, rmtree
# deletes the target's own files, then dies inside the unwritable descendant,
# so nothing is logged and the tree is half-gone (found by mutation at batch 9:
# reverting the subtree arm kept the suite green and a real --execute deleted
# f1 and left the parent). The two fixtures are shaped so each arm is the FIRST
# to fire on its own path.
mkdir -p "$L/roperm/cap/inner"
printf 'k1\n' > "$L/roperm/cap/f1"; printf 'k2\n' > "$L/roperm/cap/inner/f2"
chmod 555 "$L/roperm/cap/inner" "$L/roperm/cap"
LIST "$L/roperm/cap";                   run "KEEP:G6 the target is not writable" "G6 unwritable target: refuse first" --list "$T/list"
if [ -f "$L/roperm/cap/f1" ] && [ -f "$L/roperm/cap/inner/f2" ]; then
  ok "unwritable tree left completely intact"
else
  bad "PARTIAL DELETION on an unwritable tree"
fi
chmod -R 755 "$L/roperm/cap" 2>/dev/null
# subtree-only: the target stays writable, a descendant does not, so the
# target-level arm passes and the subtree arm must be what refuses it
mkdir -p "$L/roperm2/cap/inner"
printf 'k1\n' > "$L/roperm2/cap/f1"; printf 'k2\n' > "$L/roperm2/cap/inner/f2"
chmod 555 "$L/roperm2/cap/inner"
LIST "$L/roperm2/cap";                  run "KEEP:G6 contains a directory we cannot fully traverse" "G6 unwritable subtree: refuse before half-delete" --list "$T/list"
if [ -f "$L/roperm2/cap/f1" ] && [ -f "$L/roperm2/cap/inner/f2" ]; then
  ok "unwritable-subtree tree left completely intact"
else
  bad "PARTIAL DELETION on an unwritable subtree"
fi
chmod -R 755 "$L/roperm2/cap" 2>/dev/null
# target-unreadable: the target hides its own contents from the walk (mode
# 0300 = writable+executable, NOT readable), so the git and mode-000 scans
# inside it see nothing and the target-unreadable arm is the only thing that
# refuses it (wave-23 tester P2: reverting that arm kept the suite green and
# the tool reported the tree removable)
mkdir -p "$L/roperm3/cap/.git" "$L/roperm3/cap/locked"
printf 'k\n' > "$L/roperm3/cap/f1"
chmod 000 "$L/roperm3/cap/locked"
chmod 300 "$L/roperm3/cap"
LIST "$L/roperm3/cap";                  run "KEEP:G6 the target is not readable" "G6 unreadable target: refuse before blind removal" --list "$T/list"
chmod -R 755 "$L/roperm3/cap" 2>/dev/null; rm -rf "$L/roperm3" 2>/dev/null

echo "--- E5: a byte-invalid list line fails closed and does not stop the run (wave-20 portability P3-1) ---"
# The path bytes must reach the guards intact; only the output channels may
# substitute. A bare encode() raised inside the removal loop, i.e. after other
# paths had already been deleted, so this asserts: no traceback, the good line
# still evaluated, and a non-destructive outcome.
mkdir -p "$L/gxb/ok"
# the undisplayable path is CREATED so check() walks past G5 into the G7
# needle scan, where os.fsencode is load-bearing (wave-23 fit-for-purpose P2:
# with the dir absent the run stopped at "G5 not a directory" and an
# os.fsencode->encode() regression kept the whole suite green)
python3 -c "import os; os.makedirs(b'$L/gxb/bad\xffname', exist_ok=True)"
python3 -c "
with open('$T/list','wb') as f:
    f.write(b'$L/gxb/ok\n')
    f.write(b'$L/gxb/bad\xffname\n')
"
out=$("$PYBIN" "$K" --list "$T/list" --runs-root "$T/async" 2>&1); rc=$?
printf '%s\n' "$out" | grep -q Traceback && bad "E5 traceback on a byte-invalid line" || { [ "$rc" = 0 ] && ok "E5 no traceback, clean exit" || bad "E5 rc=$rc on a byte-invalid line"; }
printf '%s\n' "$out" | grep -q "gxb/ok" && ok "E5 the clean line was still evaluated" || bad "E5 clean line skipped"
[ -d "$L/gxb/ok" ] && ok "E5 nothing removed while a line was undisplayable" || bad "E5 removed data on a malformed line"

echo "--- E6: the runs-root guard fails closed at startup AND at the recheck (wave-24) ---"
# Startup: an absent runs root cannot prove there is no live reviewer -> rc 2,
# nothing removed. The pre-deletion recheck calls live_runs() directly, so a
# root that vanished after planning must count as live there too: that path
# cannot be raced from a shell, so it is pinned at the unit level.
mkdir -p "$L/perf/w1/rr-scratch"
LIST "$L/perf/w1/rr-scratch"
"$PYBIN" "$K" --list "$T/list" --runs-root "$T/no-such-runs" >/dev/null 2>&1; rc=$?
[ "$rc" = 2 ] && ok "missing runs root refused at startup (rc 2)" || bad "missing runs root rc=$rc"
[ -d "$L/perf/w1/rr-scratch" ] && ok "missing runs root removed nothing" || bad "DATA REMOVED on a missing runs root"
cat > "$T/lr-probe.py" <<'PY'
import importlib.util, sys
spec = importlib.util.spec_from_file_location("kitrm", sys.argv[1])
m = importlib.util.module_from_spec(spec)
spec.loader.exec_module(m)
r = m.live_runs("/nonexistent-runs-root-for-selftest")
print("FAILCLOSED" if r else "OPEN")
PY
lr=$("$PYBIN" "$T/lr-probe.py" "$K")
[ "$lr" = "FAILCLOSED" ] && ok "live_runs fails closed on a vanished root" || bad "live_runs returned no-runs for a vanished root"
# a readable+executable FILE as the runs root: the isdir check is the only arm
# that refuses it (os.access passes, the glob sees nothing), so this probe
# keeps that arm load-bearing (batch 12: without it the isdir revert was an
# equivalent mutant -- the unreadable-root check masks it for absent paths)
printf 'x\n' > "$T/file-root"; chmod 755 "$T/file-root"
cat > "$T/lr-probe2.py" <<'PY'
import importlib.util, sys
spec = importlib.util.spec_from_file_location("kitrm", sys.argv[1])
m = importlib.util.module_from_spec(spec)
spec.loader.exec_module(m)
r = m.live_runs(sys.argv[2])
print("FAILCLOSED" if r else "OPEN")
PY
lr2=$("$PYBIN" "$T/lr-probe2.py" "$K" "$T/file-root")
[ "$lr2" = "FAILCLOSED" ] && ok "live_runs fails closed on a file runs root" || bad "live_runs returned no-runs for a file runs root"
# The remaining fail-closed arms named in REVIEWS.md § Kit hygiene each get
# their own leg (wave-25: reverts of these arms kept the suite green while
# removals proceeded with the live-run evidence unreadable or malformed):
# unreadable runs root, unreadable status, status without cwd, unrecognized
# state, and an unreadable gate artifact.
mkdir -p "$T/ro-runs"; chmod 000 "$T/ro-runs"
LIST "$L/perf/w1/rr-scratch"
out=$("$PYBIN" "$K" --list "$T/list" --runs-root "$T/ro-runs" 2>&1); rc=$?
grep -q "^KEEP    G8" <<<"$out" && [ -d "$L/perf/w1/rr-scratch" ] \
  && ok "unreadable runs root: refused, nothing removed" || bad "unreadable runs root not refused (rc=$rc)"
chmod 755 "$T/ro-runs"
mkdir -p "$A/badrun" && printf 'not json at all\n' > "$A/badrun/status.json"
LIST "$L/perf/w1/rr-scratch"; run "KEEP:G8 live subagent" "G8 unreadable status counts live" --list "$T/list"
rm -rf "$A/badrun"
mkdir -p "$A/nocwd" && printf '{"state":"complete"}\n' > "$A/nocwd/status.json"
LIST "$L/perf/w1/rr-scratch"; run "KEEP:G8 live subagent" "G8 status without cwd counts live" --list "$T/list"
rm -rf "$A/nocwd"
mkdir -p "$A/weird" && printf '{"state":"banana","cwd":"%s"}\n' "$R" > "$A/weird/status.json"
LIST "$L/perf/w1/rr-scratch"; run "KEEP:G8 live subagent" "G8 unrecognized state counts live" --list "$T/list"
rm -rf "$A/weird"
mkdir -p "$L/shared/evidence/round17"
printf 'x\n' > "$L/shared/evidence/round17/manifest.json"
chmod 000 "$L/shared/evidence/round17/manifest.json"
LIST "$L/perf/w1/rr-scratch"; run "KEEP:G7 gate artifact" "G7 unreadable gate artifact refuses" --list "$T/list"
chmod 644 "$L/shared/evidence/round17/manifest.json"; rm -rf "$L/shared/evidence/round17"
# a live run hidden in an UNREADABLE RUN DIRECTORY: a glob of */status.json
# silently omits it, so enumeration is scandir over the root and any run
# directory whose status cannot be inspected counts as live (wave-26
# fit-for-purpose: the fail-open family one level deeper than the root)
mkdir -p "$A/hiddenrun"
printf '{"state":"running","cwd":"%s"}\n' "$R" > "$A/hiddenrun/status.json"
chmod 000 "$A/hiddenrun"
LIST "$L/perf/w1/rr-scratch"; run "KEEP:G8 live subagent" "G8 hidden live run in an unreadable run dir" --list "$T/list"
chmod 755 "$A/hiddenrun"; rm -rf "$A/hiddenrun"
# a run directory WITHOUT status.json is not a run: scandir sees it, so the
# FileNotFoundError arm must skip it -- otherwise every stray directory
# refuses the tool forever (batch 13: the arm the scandir rewrite introduced)
mkdir -p "$A/stray"
LIST "$L/perf/w1/rr-scratch"; run DRY "stray run dir without status is not live" --list "$T/list"
rmdir "$A/stray"
# a failed `git ls-files` must REFUSE, not note-and-continue (wave-26
# tester): the citation lives only in a tracked file, so a skipped scan
# would remove a path the record depends on. Mini-sandbox with a broken .git.
B="$T/broken-git"; mkdir -p "$B/.agents/tools" "$B/.git" "$B/.local/perf/w1/cited"
cp "$TOOL" "$B/.agents/tools/kit-rm.py"
printf 'cite %s\n' ".local/perf/w1/cited" > "$B/README.md"
printf 'x\n' > "$B/.local/perf/w1/cited/f"
mkdir -p "$T/async2"
printf '%s\n' "$B/.local/perf/w1/cited" > "$T/list2"
out=$("$PYBIN" "$B/.agents/tools/kit-rm.py" --list "$T/list2" --runs-root "$T/async2" --execute --reason selftest 2>&1); rc=$?
[ "$rc" = 2 ] && [ -d "$B/.local/perf/w1/cited" ] \
  && ok "G7 failed tracked-file scan: refused, cited dir intact" \
  || bad "G7 git-scan failure not refused (rc=$rc): $(tail -1 <<<"$out")"
# the pre-deletion recheck must call live_runs() FRESH (wave-26 tester: the
# stale-reuse mutant kept the suite green and removed a late target in a
# race). Deterministic at the unit level: first call no runs, later calls a
# live run; correct code refuses at the re-check and removes nothing.
cat > "$T/recheck-probe.py" <<'PY'
import importlib.util, os, sys
spec = importlib.util.spec_from_file_location("kitrm", sys.argv[1])
m = importlib.util.module_from_spec(spec)
spec.loader.exec_module(m)
scratch = sys.argv[2]
os.makedirs(scratch, exist_ok=True)
calls = {"n": 0}
def fake_live_runs(runs_root=None):
    calls["n"] += 1
    return [] if calls["n"] == 1 else ["fakerun(running)"]
m.live_runs = fake_live_runs
m.gate_artifacts = lambda: []
lst = os.path.join(os.path.dirname(scratch), "recheck-list")
with open(lst, "w") as fh:
    fh.write(scratch + "\n")
m.main(["kit-rm.py", "--list", lst, "--runs-root",
        os.path.dirname(scratch), "--execute", "--reason", "selftest"])
print("REFUSED" if os.path.isdir(scratch) else "REMOVED")
PY
rc_out=$("$PYBIN" "$T/recheck-probe.py" "$K" "$L/perf/w1/recheck-target" 2>/dev/null | tail -1)
[ "$rc_out" = "REFUSED" ] && ok "pre-deletion recheck calls live_runs fresh" || bad "recheck reused the planning live list ($rc_out)"
rm -rf "$L/perf/w1/recheck-target" "$L/perf/w1/recheck-list"

echo "--- F: live-run guard (G8) ---"
# live_runs() scans <runs-root>/*/status.json, so the status lives in a run dir
mkdir -p "$A/one"
printf '{"state":"running","cwd":"%s"}\n' "$R" > "$A/one/status.json"
LIST "$L/perf/w1/cargo-target";         run "KEEP:G8 live subagent" "G8 live run in this repo" --list "$T/list"
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
# The sentinel states what the run shows (every assertion passed), not what
# the suite proves about the tool: "all guards proven" implied exhaustive
# proof and was removed at batch 11 (wave-24 review, astra advisory).
[ $fail = 0 ] && echo "SUITE: all assertions passed" || echo "SUITE: FAILURES ABOVE"
exit $fail
