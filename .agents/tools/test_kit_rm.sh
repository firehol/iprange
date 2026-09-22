#!/usr/bin/env bash
# Adversarial test for .agents/tools/kit-rm.py: every attack must be REFUSED
# and one legitimate scratch tree must be ALLOWED. A destructive tool whose
# guards never fire is worse than no tool, so both directions are asserted.
# H4-LABEL-HASH: cca4a46d804712c4dd0ac3a1f4842a73b87cbb2c325973b80d09bd881c6d9d25
# sha256 over this suite's green ok-label multiset (sorted, newline-joined),
# declared by the suite itself and pinned against the bound log by the kit-gc
# suite's H4 reverse direction (batch 10; replaces batch 9's scalar count,
# which wave 23 forged by delete+duplicate at unchanged count). Any leg added,
# removed, renamed or reworded must update this line in the same edit.
set -uo pipefail
# E6 exec_modules the tool for the live_runs unit probe; keep bytecode caches
# out of policed dirs (H2).
export PYTHONDONTWRITEBYTECODE=1
# Derive the repository from this suite's own location (three hops up from
# .agents/tools/), so the suite tests the checkout it lives in and carries no
# operator-specific path (astra turn-12 P2).
KIT=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)
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
# the tool must be tracked: gate_artifacts() refuses a listing that does not
# name its own path (a vacuous scan is as blind as a failed one)
git -C "$R" -c user.name=suite -c user.email=suite@invalid add README.md .agents/tools/kit-rm.py
# gpgsign=false: a workstation-global commit.gpgsign=true makes this fixture
# commit fail (rc 128) while the legs stay green, because gate_artifacts()
# reads the INDEX, not the commits. The commit is decoration for realism; the
# flag keeps it from failing silently (wave-28 parity P3-B).
git -C "$R" -c commit.gpgsign=false -c user.name=suite -c user.email=suite@invalid commit -q -m fixture

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
# a '+' component is G1-legal and, since wave 32, captured by the citation
# regex (the pre-wave-32 class stopped at '+', leaving only the bare role,
# which the depth filter dropped). The citation set now refuses it.
printf 'the manifest cites %s verbatim\n' ".local/w1926+gate/inner" >> "$L/shared/status.md"
mkdir -p "$L/w1926+gate/inner"
LIST "$L/w1926+gate/inner";             run "KEEP:G7 related to cited path" "G7 citation set on a '+' component" --list "$T/list"
# a DESCENDANT of that '+' citation must also be refused (astra turn-12 P1:
# the truncated citation made the descendant removable).
mkdir -p "$L/w1926+gate/inner/child"
LIST "$L/w1926+gate/inner/child";       run "KEEP:G7 related to cited path" "G7 descendant of a '+' citation" --list "$T/list"
# a whitespace component is G1-legal but the token regex cannot spell it;
# only the ancestor byte-needle scan can see this citation (wave 32).
printf 'the manifest cites %s verbatim\n' ".local/w1926 space/inner" >> "$L/shared/status.md"
mkdir -p "$L/w1926 space/inner/child"
LIST "$L/w1926 space/inner/child";      run "KEEP:G7 related to a path named by gate artifact" "G7 byte scan on a whitespace component" --list "$T/list"
# the byte scan must NOT refuse an unrelated sibling whose name merely
# continues a cited component (`.local/perf/w1` inside `.local/perf/w11`).
printf 'the manifest cites %s verbatim\n' ".local/perf/w11/inner" >> "$L/shared/status.md"
mkdir -p "$L/perf/w1/scratch"
LIST "$L/perf/w1/scratch";              run DRY "G7 byte scan does not over-refuse a prefix sibling" --list "$T/list"
# the exact sibling-root counterexample (astra turn-15 P2): citing
# `.local/role/kit11` must NOT refuse the unrelated `.local/role/kit1` --
# the citation is not an ancestor of the removal path at a component
# boundary, and a substring match would refuse it.
printf 'the manifest cites %s verbatim\n' ".local/g7sib/kit11" >> "$L/shared/status.md"
mkdir -p "$L/g7sib/kit1"
LIST "$L/g7sib/kit1";                   run DRY "G7 sibling root not refused by a longer-name citation" --list "$T/list"
# a backtick-quoted citation (the common Markdown fence) must protect its
# descendant: the citation regex captures the trailing backtick, so the strip
# set must remove it (astra turn-13 P1).
printf 'the proof read `%s` during staging\n' ".local/w1926-tick/inner" >> "$L/shared/status.md"
mkdir -p "$L/w1926-tick/inner/child"
LIST "$L/w1926-tick/inner/child";       run "KEEP:G7 related to cited path" "G7 descendant of a backtick-quoted citation" --list "$T/list"
# a JSON string citation with a whitespace component: the token regex cannot
# spell it and the byte scan's extracted token carries a leading quote, so the
# open-delimiter strip must remove it (astra turn-13 P1).
printf '{"reason": "%s"}\n' ".local/w1926 jdir/inner" >> "$L/shared/status.md"
mkdir -p "$L/w1926 jdir/inner/child"
LIST "$L/w1926 jdir/inner/child";       run "KEEP:G7 related to a path named by gate artifact" "G7 JSON whitespace citation" --list "$T/list"
# compact JSON (no space after the colon): the byte scan must stop at the
# hard quote boundary, not scan back over the key and colon (astra turn-14 P1).
printf '{"input":"%s"}\n' ".local/w1926 cjson/inner" >> "$L/shared/status.md"
mkdir -p "$L/w1926 cjson/inner/child"
LIST "$L/w1926 cjson/inner/child";      run "KEEP:G7 related to a path named by gate artifact" "G7 compact-JSON whitespace citation" --list "$T/list"
# a directory whose REAL name ends in a path-legal punctuation byte must keep
# its descendant protection: the citation is read raw as well as stripped, so
# stripping cannot destroy the literal-name reading (astra turn-14 P1).
printf 'bound replay read %s\n' ".local/w1926-dot/kit." >> "$L/shared/status.md"
mkdir -p "$L/w1926-dot/kit./child"
LIST "$L/w1926-dot/kit./child";         run "KEEP:G7 related to cited path" "G7 descendant of a trailing-dot-named dir" --list "$T/list"
printf 'bound replay read %s\n' '.local/w1926-tick2/kit`' >> "$L/shared/status.md"
mkdir -p "$L/w1926-tick2/kit\`/child"
LIST "$L/w1926-tick2/kit\`/child";      run "KEEP:G7 related to cited path" "G7 descendant of a backtick-named dir" --list "$T/list"
# a whitespace component cited with trailing prose punctuation: the byte scan
# must read the token both raw and stripped, so the stripped reading names the
# real path (astra turn-14 P1; routes only through the byte scan, the regex
# truncates a whitespace component to the bare role).
printf 'bound replay read %s.\n' ".local/w1926-wdot/my dir" >> "$L/shared/status.md"
mkdir -p "$L/w1926-wdot/my dir/child"
LIST "$L/w1926-wdot/my dir/child";      run "KEEP:G7 related to a path named by gate artifact" "G7 byte scan strips a whitespace citation" --list "$T/list"
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
# a prose mention of a bare ROLE ROOT (one slash) must not refuse the whole
# subtree: the depth filter drops candidates shallower than two slashes,
# because G3 already protects the role root itself and a role name in prose
# is not a citation of everything under it.
printf 'the role %s is mentioned in prose\n' ".local/g7depth" >> "$L/shared/status.md"
mkdir -p "$L/g7depth/sub"
LIST "$L/g7depth/sub";                  run DRY  "G7 a bare role-root mention does not refuse its subtree" --list "$T/list"
# two citations on one line: the scan advances by one, not to end of line, so
# a second `.local/` occurrence on the same line is a separate anchor and
# still protects its subtree (the lead's own audit of the citation-anchored
# rebuild; advancing to EOL would silently drop every citation after the
# first on a line).
printf 'see %s and %s in one line\n' ".local/g7multi/a/b" ".local/g7multi/c/d" >> "$L/shared/status.md"
mkdir -p "$L/g7multi/c/d/child" "$L/g7multi/a/b/child"
LIST "$L/g7multi/c/d/child";            run "KEEP:G7 related to cited path" "G7 second citation on a line protects its subtree" --list "$T/list"
LIST "$L/g7multi/a/b/child";            run "KEEP:G7 related to cited path" "G7 first citation on a line protects its subtree" --list "$T/list"
# a byte-invalid citation must protect a byte-invalid removal path: the
# artifact is read with surrogateescape and os.fsencode reverses it exactly,
# so the citation round-trips to the same bytes the list file produced
# (the lead's own audit; reading the artifact with errors="replace" would
# mangle the invalid byte to U+FFFD and the citation could never match).
python3 -c "
with open('$L/shared/status.md','a') as f:
    f.buffer.write(b'bound replay read .local/g7byte/bad\xffname\n')
import os; os.makedirs(b'$L/g7byte/bad\xffname/child', exist_ok=True)
"
python3 -c "
with open('$T/list','wb') as f:
    f.write(b'$L/g7byte/bad\xffname/child\n')
"
out=$("$PYBIN" "$K" --list "$T/list" --runs-root "$T/async" 2>&1); rc=$?
grep -q "^KEEP    G7" <<<"$out" && [ -d "$L/g7byte/bad"$'\xff'"name/child" ] \
  && ok "G7 byte-invalid citation protects its byte-invalid subtree" \
  || bad "G7 byte-invalid citation did not refuse (rc=$rc): $(tail -1 <<<"$out")"

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
# unwritable PARENT: the target and its contents are fully writable, so the
# target-level and subtree arms pass; only the parent arm can refuse before
# rmtree empties the target and dies unlinking it (wave-27 tester: the old
# tool deleted all contents, printed KEEP after the fact, logged nothing)
mkdir -p "$L/roperm4/parent/cap/inner"
printf 'k1\n' > "$L/roperm4/parent/cap/f1"; printf 'k2\n' > "$L/roperm4/parent/cap/inner/f2"
chmod 555 "$L/roperm4/parent"
LIST "$L/roperm4/parent/cap";           run "KEEP:G6 the parent is not writable" "G6 unwritable parent: refuse before half-delete" --list "$T/list"
if [ -f "$L/roperm4/parent/cap/f1" ] && [ -f "$L/roperm4/parent/cap/inner/f2" ]; then
  ok "unwritable-parent tree left completely intact"
else
  bad "PARTIAL DELETION with an unwritable parent"
fi
chmod -R 755 "$L/roperm4" 2>/dev/null; rm -rf "$L/roperm4" 2>/dev/null
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
mkdir -p "$A/scalar" && printf '42\n' > "$A/scalar/status.json"
LIST "$L/perf/w1/rr-scratch"; run "KEEP:G8 live subagent" "G8 non-object status counts live" --list "$T/list"
rm -rf "$A/scalar"
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
B="$T/broken-git"; mkdir -p "$B/.agents/tools" "$B/.git" "$B/.local/shared" "$B/.local/perf/w1/cited"
: > "$B/.local/shared/removals.log"   # appendable: the refusal below must be G7's
cp "$TOOL" "$B/.agents/tools/kit-rm.py"
printf 'cite %s\n' ".local/perf/w1/cited" > "$B/README.md"
printf 'x\n' > "$B/.local/perf/w1/cited/f"
mkdir -p "$T/async2"
printf '%s\n' "$B/.local/perf/w1/cited" > "$T/list2"
out=$("$PYBIN" "$B/.agents/tools/kit-rm.py" --list "$T/list2" --runs-root "$T/async2" --execute --reason selftest 2>&1); rc=$?
[ "$rc" = 2 ] && [ -d "$B/.local/perf/w1/cited" ] \
  && grep -q 'G7: git ls-files failed or returned a vacuous listing' <<<"$out" \
  && ok "G7 failed tracked-file scan: refused, cited dir intact" \
  || bad "G7 git-scan failure not refused (rc=$rc): $(tail -1 <<<"$out")"
# a SUCCESSFUL-but-empty scan (index deleted: git rc 0, zero files) is as
# blind as a failed one; the tool is tracked, so a listing missing its own
# path is vacuous and must refuse (wave-27 fit-for-purpose, closed on intent)
C="$T/vacuous-git"; mkdir -p "$C/.agents/tools" "$C/.git" "$C/.local/shared" "$C/.local/perf/w1/cited"
: > "$C/.local/shared/removals.log"   # appendable: the refusal below must be G7's
cp "$TOOL" "$C/.agents/tools/kit-rm.py"
printf 'cite %s\n' ".local/perf/w1/cited" > "$C/README.md"
printf 'x\n' > "$C/.local/perf/w1/cited/f"
git -C "$C" -c init.defaultBranch=main init -q
git -C "$C" -c user.name=suite -c user.email=suite@invalid add README.md .agents/tools/kit-rm.py
git -C "$C" -c commit.gpgsign=false -c user.name=suite -c user.email=suite@invalid commit -q -m fixture
rm -f "$C/.git/index"          # ls-files now succeeds with an EMPTY list
mkdir -p "$T/async3"
printf '%s\n' "$C/.local/perf/w1/cited" > "$T/list3"
out=$("$PYBIN" "$C/.agents/tools/kit-rm.py" --list "$T/list3" --runs-root "$T/async3" --execute --reason selftest 2>&1); rc=$?
[ "$rc" = 2 ] && [ -d "$C/.local/perf/w1/cited" ] \
  && grep -q 'G7: git ls-files failed or returned a vacuous listing' <<<"$out" \
  && ok "G7 vacuous tracked-file scan: refused, cited dir intact" \
  || bad "G7 vacuous scan not refused (rc=$rc): $(tail -1 <<<"$out")"
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

echo "--- J: an unappendable audit log refuses BEFORE anything is deleted (wave-28 tester) ---"
# The log is part of the removal contract. Without the startup probe the
# guards passed, rmtree completed, the append raised PermissionError, and the
# run reported nothing about what it destroyed (rc 1, zero REMOVED lines).
mkdir -p "$L/perf/w4/cargo-target" && printf 'k\n' > "$L/perf/w4/cargo-target/f"
chmod 444 "$L/shared/removals.log"
LIST "$L/perf/w4/cargo-target"
out=$("$PYBIN" "$K" --list "$T/list" --runs-root "$T/async" --execute --reason "selftest" 2>&1); rc=$?
grep -q 'audit log is not appendable' <<<"$out" && [ "$rc" = 2 ] \
  && ok "unappendable log refused at startup (rc 2)" || bad "unappendable log rc=$rc: $(tail -1 <<<"$out")"
[ -f "$L/perf/w4/cargo-target/f" ] && ok "nothing deleted when the log cannot be written" \
  || bad "DELETED WITH NO AUDIT TRAIL"
grep -q '^REMOVED' <<<"$out" && bad "REMOVED line despite unappendable log" || ok "no REMOVED claim"
chmod 644 "$L/shared/removals.log"
# a missing log in an unwritable directory is the same contract violation
rm -f "$L/shared/removals.log"; chmod 555 "$L/shared"
out=$("$PYBIN" "$K" --list "$T/list" --runs-root "$T/async" --execute --reason "selftest" 2>&1); rc=$?
grep -q 'audit log directory.*is not writable' <<<"$out" && [ "$rc" = 2 ] \
  && ok "unwritable log directory refused at startup (rc 2)" || bad "unwritable log dir rc=$rc: $(tail -1 <<<"$out")"
[ -f "$L/perf/w4/cargo-target/f" ] && ok "second refusal also deleted nothing" \
  || bad "DELETED WITH NO AUDIT TRAIL (dir case)"
chmod 755 "$L/shared"; printf 'restored\n' >> "$L/shared/removals.log"
rm -rf "$L/perf/w4"

echo "--- J2: the log must be a REGULAR file (wave-29 fit-for-purpose P2-1) ---"
# open("a") follows symlinks and blocks on FIFOs, so a log that is a link to
# /dev/null "succeeds" while recording nothing durable: a worse outcome than
# the wave-28 crash, because the run reports EXECUTED rc 0.
mkdir -p "$L/perf/w5/cargo-target" && printf 'k\n' > "$L/perf/w5/cargo-target/f"
LIST "$L/perf/w5/cargo-target"
# literal labels per shape (no variable interpolation into ok labels): the H4
# forward pin requires every printed label to appear verbatim in this source
# (batch 9). A helper that interpolates the shape name would print a label the
# source never contains, and the pin would reject the bound log.
rm -f "$L/shared/removals.log"; ln -s /dev/null "$L/shared/removals.log"
out=$(timeout 20 "$PYBIN" "$K" --list "$T/list" --runs-root "$T/async" --execute --reason "selftest" 2>&1); rc=$?
[ "$rc" = 124 ] && bad "symlink log hung the tool (no timeout escape)" \
  || { grep -q 'not a regular file' <<<"$out" && [ "$rc" = 2 ] \
       && ok "symlink audit log refused at startup (rc 2)" || bad "symlink log rc=$rc: $(tail -1 <<<"$out")"; }
[ -f "$L/perf/w5/cargo-target/f" ] && ok "symlink log: nothing deleted" \
  || bad "DELETED WITH NO AUDIT TRAIL (symlink)"
rm -f "$L/shared/removals.log"; mkfifo "$L/shared/removals.log"
out=$(timeout 20 "$PYBIN" "$K" --list "$T/list" --runs-root "$T/async" --execute --reason "selftest" 2>&1); rc=$?
[ "$rc" = 124 ] && bad "fifo log hung the tool (no timeout escape)" \
  || { grep -q 'not a regular file' <<<"$out" && [ "$rc" = 2 ] \
       && ok "fifo audit log refused at startup (rc 2)" || bad "fifo log rc=$rc: $(tail -1 <<<"$out")"; }
[ -f "$L/perf/w5/cargo-target/f" ] && ok "fifo log: nothing deleted" \
  || bad "DELETED WITH NO AUDIT TRAIL (fifo)"
rm -rf "$L/shared/removals.log"; mkdir "$L/shared/removals.log"
out=$(timeout 20 "$PYBIN" "$K" --list "$T/list" --runs-root "$T/async" --execute --reason "selftest" 2>&1); rc=$?
[ "$rc" = 124 ] && bad "dir log hung the tool (no timeout escape)" \
  || { grep -q 'not a regular file' <<<"$out" && [ "$rc" = 2 ] \
       && ok "dir audit log refused at startup (rc 2)" || bad "dir log rc=$rc: $(tail -1 <<<"$out")"; }
[ -f "$L/perf/w5/cargo-target/f" ] && ok "dir log: nothing deleted" \
  || bad "DELETED WITH NO AUDIT TRAIL (dir)"
rm -rf "$L/shared/removals.log"
printf 'restored\n' > "$L/shared/removals.log"
rm -rf "$L/perf/w5"

echo "--- J3: the record is durable BEFORE the path is destroyed (wave-29 parity P2-1) ---"
# RLIMIT_FSIZE makes the log unwritable for NEW data while open("a") succeeds:
# the old code deleted the target and then died in the append. With
# write-ahead audit the append failure refuses the removal instead.
mkdir -p "$L/perf/w6/cargo-target" && printf 'k\n' > "$L/perf/w6/cargo-target/f"
head -c 1000 /dev/zero | tr '\0' 'x' > "$L/shared/removals.log"
LIST "$L/perf/w6/cargo-target"
out=$(bash -c "ulimit -f 1; exec timeout 60 '$PYBIN' '$K' --list '$T/list' --runs-root '$T/async' --execute --reason selftest" 2>&1); rc=$?
grep -q 'audit record could not be written' <<<"$out" \
  && ok "write-time log failure refuses the removal" || bad "write-time failure rc=$rc: $(tail -1 <<<"$out")"
[ -f "$L/perf/w6/cargo-target/f" ] && ok "target intact when the record cannot be written" \
  || bad "DELETED WITH NO AUDIT TRAIL (write-time)"
grep -q '^REMOVED' <<<"$out" && bad "REMOVED claim without a record" || ok "no REMOVED claim"
rm -rf "$L/perf/w6"

echo "--- J4: a removal that fails after its record gets a compensating line ---"
# Write-ahead means the log can name a path that survived. Without the
# compensating entry the audit trail would claim a destruction that did not
# happen. Deterministic at the unit level: rmtree is replaced by a raiser.
cat > "$T/compensate-probe.py" <<'PY'
import importlib.util, os, sys
spec = importlib.util.spec_from_file_location("kitrm", sys.argv[1])
m = importlib.util.module_from_spec(spec)
spec.loader.exec_module(m)
scratch = sys.argv[2]
os.makedirs(os.path.join(scratch, "inner"), exist_ok=True)
open(os.path.join(scratch, "inner", "f"), "w").write("k\n")
def boom(path, *a, **k):
    raise OSError(13, "simulated rmtree failure")
m.shutil.rmtree = boom
m.gate_artifacts = lambda: []
m.live_runs = lambda runs_root=None: []
lst = os.path.join(os.path.dirname(scratch), "comp-list")
with open(lst, "w") as fh:
    fh.write(scratch + "\n")
m.main(["kit-rm.py", "--list", lst, "--runs-root",
        os.path.dirname(scratch), "--execute", "--reason", "selftest"])
print("KEPT" if os.path.isdir(scratch) else "GONE")
PY
out=$("$PYBIN" "$T/compensate-probe.py" "$K" "$L/perf/w7/cargo-target" 2>&1); rc=$?
tail -1 <<<"$out" | grep -q '^KEPT$' && ok "simulated rmtree failure kept the target" || bad "target state after simulated failure: $(tail -1 <<<"$out")"
grep -q 'REMOVAL-FAILED: \[Errno 13\] simulated rmtree failure' "$L/shared/removals.log" \
  && ok "compensating REMOVAL-FAILED line recorded" || bad "no compensating line in the audit log"
rm -rf "$L/perf/w7" "$L/perf/w7/comp-list"

echo "--- J6: an uncorrectable compensating line is surfaced, not believed ---"
# Append-only means a bare write-ahead record cannot be retracted. If the
# compensating line ALSO fails to write (rmtree failed AND the log became
# unwritable in that window), the log keeps a record that over-claims a
# destruction for a surviving path. The run must not report clean success:
# it prints an ERROR line and exits rc 1 (wave-30 parity P2-1). Deterministic
# at the unit level: rmtree raises, the first append (the record) succeeds,
# the second append (the compensating line) is forced to fail.
cat > "$T/uncorrectable-probe.py" <<'PY'
import importlib.util, os, sys
spec = importlib.util.spec_from_file_location("kitrm", sys.argv[1])
m = importlib.util.module_from_spec(spec)
spec.loader.exec_module(m)
scratch = sys.argv[2]
os.makedirs(os.path.join(scratch, "inner"), exist_ok=True)
open(os.path.join(scratch, "inner", "f"), "w").write("k\n")
def boom(path, *a, **k):
    raise OSError(13, "simulated rmtree failure")
m.shutil.rmtree = boom
m.gate_artifacts = lambda: []
m.live_runs = lambda runs_root=None: []
real_append = m.append_audit
calls = {"n": 0}
def flaky_append(line):
    calls["n"] += 1
    if calls["n"] == 1:
        return real_append(line)          # the write-ahead record lands
    return "simulated compensating failure"  # the correction cannot
m.append_audit = flaky_append
lst = os.path.join(os.path.dirname(scratch), "unc-list")
with open(lst, "w") as fh:
    fh.write(scratch + "\n")
rc = m.main(["kit-rm.py", "--list", lst, "--runs-root",
             os.path.dirname(scratch), "--execute", "--reason", "unc-test"])
print("RC=%d KEPT=%s" % (rc, os.path.isdir(scratch)))
PY
out=$("$PYBIN" "$T/uncorrectable-probe.py" "$K" "$L/perf/w9/cargo-target" 2>&1); rc=$?
grep -q 'RC=1 KEPT=True' <<<"$out" && ok "uncorrectable trail exits rc 1 with the target kept" || bad "uncorrectable trail: $(tail -1 <<<"$out")"
grep -q 'claims a removal that did not happen' <<<"$out" && ok "uncorrectable trail prints an ERROR line" || bad "no ERROR line for the uncorrectable trail"
# the bare record is present (over-claim) but no REMOVAL-FAILED line exists
# for it: that is exactly the inconsistency the ERROR line surfaces. Scoped
# to this leg's reason so J4's earlier compensating line cannot collide.
# Field order is stamp, size, path, reason, REMOVAL-FAILED -- so the reason
# PRECEDES the marker (wave-31 tester P2-2: the reversed pattern could never
# match, making this pin dead).
grep -q 'unc-test.*REMOVAL-FAILED' "$L/shared/removals.log" && bad "a REMOVAL-FAILED line was written despite the forced failure" || ok "no false correction was recorded"
grep -q 'unc-test' "$L/shared/removals.log" && ok "the bare over-claiming record is in the log" || bad "the write-ahead record never landed (probe broken)"
rm -rf "$L/perf/w9" "$L/perf/w9/unc-list"

echo "--- J7: the still-present arm surfaces an uncorrectable trail too ---"
# The except-arm (J6) and the still-present arm are separate code paths; a
# fix to one does not fix the other (wave-31 tester P2-1: reverting only the
# still-present arm kept the whole suite green). Deterministic at the unit
# level: rmtree is a no-op so the path survives, and the compensating append
# is forced to fail.
cat > "$T/stillpresent-probe.py" <<'PY'
import importlib.util, os, sys
spec = importlib.util.spec_from_file_location("kitrm", sys.argv[1])
m = importlib.util.module_from_spec(spec)
spec.loader.exec_module(m)
scratch = sys.argv[2]
os.makedirs(os.path.join(scratch, "inner"), exist_ok=True)
open(os.path.join(scratch, "inner", "f"), "w").write("k\n")
m.shutil.rmtree = lambda path, *a, **k: None   # "succeeds", path survives
m.gate_artifacts = lambda: []
m.live_runs = lambda runs_root=None: []
real_append = m.append_audit
calls = {"n": 0}
def flaky_append(line):
    calls["n"] += 1
    if calls["n"] == 1:
        return real_append(line)          # the write-ahead record lands
    return "simulated compensating failure"  # the correction cannot
m.append_audit = flaky_append
lst = os.path.join(os.path.dirname(scratch), "sp-list")
with open(lst, "w") as fh:
    fh.write(scratch + "\n")
rc = m.main(["kit-rm.py", "--list", lst, "--runs-root",
             os.path.dirname(scratch), "--execute", "--reason", "sp-test"])
print("RC=%d KEPT=%s" % (rc, os.path.isdir(scratch)))
PY
out=$("$PYBIN" "$T/stillpresent-probe.py" "$K" "$L/perf/w10/cargo-target" 2>&1); rc=$?
grep -q 'RC=1 KEPT=True' <<<"$out" && ok "still-present uncorrectable trail exits rc 1" || bad "still-present uncorrectable trail: $(tail -1 <<<"$out")"
grep -q 'claims a removal that did not happen' <<<"$out" && ok "still-present trail prints an ERROR line" || bad "no ERROR line for the still-present trail"
grep -q 'sp-test.*REMOVAL-FAILED' "$L/shared/removals.log" && bad "a false correction was recorded for the still-present arm" || ok "no false correction for the still-present arm"
rm -rf "$L/perf/w10" "$L/perf/w10/sp-list"

echo "--- J8: a failed record append is surfaced too (torn-fragment class) ---"
# A record append that fails mid-write can leave a newline-less fragment
# that reads like a removal record for a path that survived, and glues onto
# every later line. The removal did not happen, so the trail over-claims it:
# the run must surface (ERROR + rc 1), not print KEEP at rc 0 (wave-31
# fit-for-purpose P2). Deterministic at the unit level: the record append is
# forced to fail.
cat > "$T/torn-record-probe.py" <<'PY'
import importlib.util, os, sys
spec = importlib.util.spec_from_file_location("kitrm", sys.argv[1])
m = importlib.util.module_from_spec(spec)
spec.loader.exec_module(m)
scratch = sys.argv[2]
os.makedirs(os.path.join(scratch, "inner"), exist_ok=True)
open(os.path.join(scratch, "inner", "f"), "w").write("k\n")
m.gate_artifacts = lambda: []
m.live_runs = lambda runs_root=None: []
m.append_audit = lambda line: "simulated torn record failure"
lst = os.path.join(os.path.dirname(scratch), "tr-list")
with open(lst, "w") as fh:
    fh.write(scratch + "\n")
rc = m.main(["kit-rm.py", "--list", lst, "--runs-root",
             os.path.dirname(scratch), "--execute", "--reason", "tr-test"])
print("RC=%d KEPT=%s" % (rc, os.path.isdir(scratch)))
PY
out=$("$PYBIN" "$T/torn-record-probe.py" "$K" "$L/perf/w11/cargo-target" 2>&1); rc=$?
grep -q 'RC=1 KEPT=True' <<<"$out" && ok "failed record append exits rc 1 with the target kept" || bad "failed record append: $(tail -1 <<<"$out")"
grep -q 'could not be written' <<<"$out" && ok "failed record append prints an ERROR line" || bad "no ERROR line for the failed record append"
rm -rf "$L/perf/w11" "$L/perf/w11/tr-list"

echo "--- J5: O_NOFOLLOW closes the swap-between-check-and-open window ---"
# The shape check lstats the log, then opens it: between the two an attacker
# can replace a regular file with a symlink to /dev/null. O_NOFOLLOW makes
# the open itself reject that. Deterministic at the unit level: lstat is
# faked to report a regular file while the real path is a symlink.
cat > "$T/nofollow-probe.py" <<'PROBE'
import importlib.util, os, sys, stat
spec = importlib.util.spec_from_file_location("kitrm", sys.argv[1])
m = importlib.util.module_from_spec(spec)
spec.loader.exec_module(m)
logdir = sys.argv[2]
os.makedirs(logdir, exist_ok=True)
m.AUDIT_LOG = os.path.join(logdir, "removals.log")
real = os.path.join(logdir, "real-target")
open(real, "w").write("nothing durable here\n")
os.symlink(real, m.AUDIT_LOG)
true_lstat = os.lstat
def fake_lstat(path, *a, **k):
    if path == m.AUDIT_LOG:
        return os.stat(real)   # the attacker swapped AFTER a regular-file check
    return true_lstat(path, *a, **k)
os.lstat = fake_lstat
why = m.append_audit("RECORD\n")
print("REFUSED" if why else "FOLLOWED")
PROBE
out=$("$PYBIN" "$T/nofollow-probe.py" "$K" "$L/perf/w8" 2>&1 | tail -1)
[ "$out" = "REFUSED" ] && ok "symlink swap after the shape check is refused" || bad "O_NOFOLLOW missing: the swap window was followed ($out)"
grep -q RECORD "$L/perf/w8/real-target" && bad "audit record leaked through the swap" || ok "no record leaked through the swap"
rm -rf "$L/perf/w8"

echo "--- J9: a FIFO swap after the shape check cannot hang the append (wave 32, astra turn-12 P2) ---"
# O_NOFOLLOW rejects symlinks, not FIFOs: a write-only open of a FIFO with no
# reader blocks forever. O_NONBLOCK makes the open fail with ENXIO, and the
# fstat on the opened descriptor rejects a FIFO that does have a reader.
cat > "$T/fifo-probe.py" <<'PROBE'
import importlib.util, os, sys
spec = importlib.util.spec_from_file_location("kitrm", sys.argv[1])
m = importlib.util.module_from_spec(spec)
spec.loader.exec_module(m)
logdir = sys.argv[2]
os.makedirs(logdir, exist_ok=True)
m.AUDIT_LOG = os.path.join(logdir, "removals.log")
real = os.path.join(logdir, "real-target")
open(real, "w").write("nothing durable here\n")
os.mkfifo(m.AUDIT_LOG)
true_lstat = os.lstat
def fake_lstat(path, *a, **k):
    if path == m.AUDIT_LOG:
        return os.stat(real)   # the attacker swapped AFTER a regular-file check
    return true_lstat(path, *a, **k)
os.lstat = fake_lstat
why = m.append_audit("RECORD\n")
print("REFUSED" if why else "FOLLOWED")
PROBE
out=$(timeout 30 "$PYBIN" "$T/fifo-probe.py" "$K" "$L/perf/w9" 2>&1 | tail -1); rc=$?
[ $rc = 124 ] && bad "FIFO swap HUNG the append (timeout)" || true
[ "$out" = "REFUSED" ] && ok "FIFO swap after the shape check is refused" || bad "FIFO swap not refused ($out)"
rm -rf "$L/perf/w9"

echo "--- J10: the full CLI sequence fsyncs the audit log's directory entry (wave 32, astra turn-12/13 P2) ---"
# fsync(2) makes the FILE durable, not its DIRECTORY ENTRY: a crash after the
# first record can lose the log's name while rmtree already ran. The startup
# probe must not create the log with O_CREAT (that made append_audit believe it
# pre-existed and skip the directory fsync -- astra turn-13 P2), so this probe
# drives the SHIPPED main() end-to-end and asserts the containing directory is
# fsynced before the destructive call.
cat > "$T/dirsync-probe.py" <<'PROBE'
import importlib.util, os, sys
spec = importlib.util.spec_from_file_location("kitrm", sys.argv[1])
m = importlib.util.module_from_spec(spec)
spec.loader.exec_module(m)
repo = sys.argv[2]
m.REPO = repo
m.LOCAL = os.path.join(repo, ".local")
m.SHARED = os.path.join(m.LOCAL, "shared")
m.AUDIT_LOG = os.path.join(m.SHARED, "removals.log")
target = os.path.join(m.LOCAL, "perf", "w10", "cargo-target")
os.makedirs(target, exist_ok=True)
open(os.path.join(target, "f"), "w").write("k\n")
open(os.path.join(m.SHARED, "status.md"), "w").write("nothing here yet\n")
fsynced = []
true_fsync = os.fsync
def spy(fno):
    fsynced.append(os.fstat(fno).st_ino)
    return true_fsync(fno)
os.fsync = spy
lst = os.path.join(repo, ".local", "dirsync-list.txt")
open(lst, "w").write(target + "\n")
m.main(["kit-rm.py", "--list", lst, "--runs-root", sys.argv[3],
        "--execute", "--reason", "selftest"])
dir_ino = os.stat(m.SHARED).st_ino
print("DIR-FSYNCED" if dir_ino in fsynced else "DIR-MISSED")
PROBE
mkdir -p "$T/async-dirsync"
out=$("$PYBIN" "$T/dirsync-probe.py" "$K" "$R" "$T/async-dirsync" 2>&1 | tail -1)
[ "$out" = "DIR-FSYNCED" ] && ok "the CLI sequence fsyncs the audit log directory before removal" || bad "directory entry not made durable across the CLI sequence ($out)"
rm -rf "$L/perf/w10"

echo "--- J11: the pre-delete re-check sees a citation in a record file that appeared after planning (wave 32, astra turn-12/13 P2) ---"
# The citation cache and the artifact inventory are process snapshots from
# startup; a re-check on stale inputs is not a re-check. The fixture must be a
# citation the stale inputs actually MISS: a citation in status.md is caught by
# the byte scan even on a stale cache (status.md is in the planning inventory
# and the target path is a substring of any descendant citation), so that shape
# is non-discriminating (astra turn-13 P2). A citation in a NEW gate-artifact
# file (matched by the astra-turn*.md glob) is invisible to the planning
# inventory, so only the re-check's fresh gate_artifacts() + cleared cache see
# it. The shipped code refuses; the stale re-check (driver mutant recheck-stale)
# removes the target.
cat > "$T/recheck-probe.py" <<'PROBE'
import importlib.util, os, sys
spec = importlib.util.spec_from_file_location("kitrm", sys.argv[1])
m = importlib.util.module_from_spec(spec)
spec.loader.exec_module(m)
repo = sys.argv[2]
m.REPO = repo
m.LOCAL = os.path.join(repo, ".local")
m.SHARED = os.path.join(m.LOCAL, "shared")
target = os.path.join(m.LOCAL, "perf", "w11", "cargo-target")
os.makedirs(target, exist_ok=True)
open(os.path.join(target, "f"), "w").write("k\n")
open(os.path.join(m.SHARED, "status.md"), "w").write("nothing here yet\n")
calls = {"n": 0}
true_check = m.check
def spy_check(path, texts, live):
    calls["n"] += 1
    r = true_check(path, texts, live)
    if calls["n"] == 1:   # after the planning check passed: a NEW gate-artifact
        # file appears citing the target exactly. It is not in the planning
        # inventory, so a stale re-check cannot see it by any mechanism.
        open(os.path.join(m.SHARED, "astra-turn99.md"), "w").write(
            "bound replay read .local/perf/w11/cargo-target\n")
    return r
m.check = spy_check
lst = os.path.join(repo, ".local", "recheck-list.txt")
open(lst, "w").write(target + "\n")
rc = m.main(["kit-rm.py", "--list", lst, "--runs-root", sys.argv[3],
             "--execute", "--reason", "selftest"])
print(f"RC={rc} SURVIVED={os.path.isdir(target)}")
PROBE
mkdir -p "$T/async-recheck"
out=$("$PYBIN" "$T/recheck-probe.py" "$K" "$R" "$T/async-recheck" 2>&1 | tail -1)
grep -q 'SURVIVED=True' <<<"$out" && ok "re-check refuses a citation in a record file added after planning" || bad "stale re-check removed the target: $out"
rm -rf "$L/perf/w11"

echo "--- K: a sandbox named '..x' is inside .local, not above it (wave 32, astra turn-12 P3) ---"
mkdir -p "$L/..dot/inner"
LIST "$L/..dot/inner";                  run DRY "leading-dot sandbox is inside .local" --list "$T/list"
rm -rf "$L/..dot"

echo
# The sentinel states what the run shows (every assertion passed), not what
# the suite proves about the tool: "all guards proven" implied exhaustive
# proof and was removed at batch 11 (wave-24 review, astra advisory).
[ $fail = 0 ] && echo "SUITE: all assertions passed" || echo "SUITE: FAILURES ABOVE"
exit $fail
