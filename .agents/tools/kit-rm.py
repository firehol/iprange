#!/usr/bin/env python3
"""kit-rm.py — guarded removal of disposable review-kit scratch under .local/.

WHY: kit-gc.py is a read-only reporter by contract (it deletes nothing and
never calls anything safe to remove). Removal of reviewer scratch is a real
operational need -- the reviewer sandboxes are what trip the <= 1 GB cap the
gate checks -- and it is the one destructive action this kit performs. This
tool exists so the action is never an ad-hoc `rm -rf` of hand-written paths:
it removes ONLY the exact absolute paths in a list file, and only after every
guard below passes for that path, re-checked immediately before removal.

The user pre-authorized lead-initiated removal of disposable reviewer scratch
(REVIEWS.md § Kit hygiene); the authorization covers WHAT may be removed, not
a relaxation of the guards.

Guards (a path is refused, not removed, if ANY fails):
  G1  the list line is an absolute path with no glob or whitespace-control
      characters, and equals its own realpath (so no symlink component);
  G2  realpath is strictly inside `<repo>/.local/` (never `.local` itself);
  G3  depth >= 2 below `.local/` (a role root is never removed: its reports,
      HEARTBEAT and briefs survive every removal);
  G4  not `.local/shared` or `.local/_attic-md`, and not inside either -- the
      central kit and the archive are refused by identity, never by name match;
  G5  it is a real directory (not a symlink, not a file, not missing);
  G6  it contains no `.git` entry (a checkout or worktree is never scratch),
      no directory unreadable to us, and no mode-000 entry (the standing
      privacy fixtures ARE the gate signal);
  G7  its exact path string appears in no gate artifact (every tracked file,
      `.local/shared/status.md`, `.local/shared/head`, every
      `.local/shared/evidence/*/manifest.json`, every
      `.local/shared/astra-turn*.md`): a path whose removal would invalidate
      a bound claim or the gate record is kept;
  G8  no live subagent child exists (a non-terminal run in the async-runs
      root means a role may be measuring inside its sandbox right now).

Dry run is the default: nothing is removed unless --execute is passed WITH
--reason, and the removal of each path is logged (path, size, reason,
timestamp) to `.local/shared/removals.log`.

Usage:
  kit-rm.py --list FILE                      # verify and report only
  kit-rm.py --list FILE --execute --reason "gate-close, wave 18 complete"
"""
from __future__ import annotations

import argparse
import glob
import json
import os
import shutil
import subprocess
import sys
import time

# <repo>/.agents/tools/kit-rm.py -> three hops up is the repository root.
REPO = os.path.realpath(
    os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__)))))
LOCAL = os.path.realpath(os.path.join(REPO, ".local"))
SHARED = os.path.join(LOCAL, "shared")
ATTIC = os.path.join(LOCAL, "_attic-md")
RUNS_ROOT = "/tmp/pi-subagents-uid-1001/async-subagent-runs"
# States a run can finish in. Anything else (including an unrecognised value
# or an unreadable status) is treated as LIVE, so the guard fails closed.
TERMINAL = {"complete", "completed", "failed", "stopped", "cancelled",
            "canceled", "interrupted"}
GLOB_CHARS = set("*?[]$\t\n\r'\"\\")


def human(n: float) -> str:
    return f"{n / (1024 * 1024):.1f} MB"


def gate_artifacts() -> list[str]:
    """Files whose content makes a kit path load-bearing for the record."""
    # The kit records are collected FIRST: if `git ls-files` fails we must
    # still check the files that make a kit path load-bearing, never return an
    # empty list (an empty list would disable G7 and fail open).
    out = [os.path.join(SHARED, "status.md"), os.path.join(SHARED, "head")]
    out += glob.glob(os.path.join(SHARED, "evidence", "*", "manifest.json"))
    out += glob.glob(os.path.join(SHARED, "astra-turn*.md"))
    try:
        listing = subprocess.run(["git", "-C", REPO, "ls-files", "-z"],
                                 capture_output=True, check=True).stdout
    except (OSError, subprocess.SubprocessError):
        listing = b""
        print("note: git ls-files failed; tracked files are not scanned by G7",
              file=sys.stderr)
    out += [os.path.join(REPO, p.decode()) for p in listing.split(b"\0") if p]
    return [p for p in out if os.path.isfile(p)]


def referenced(path: str, texts: list[str]) -> str | None:
    """Return the first artifact naming this path, else None (G7).

    Records cite kit scratch two ways: the absolute path (commands, checker
    output) and a repo-relative path such as
    `.local/tester/r17-kit/g12/*-mutant.py` (manifest reasons, SOW prose).
    Both forms are searched, plus the path plus a separator, or a directory
    named only relatively could be deleted out from under a bound claim.
    """
    needles = [path, path + os.sep]
    if path.startswith(REPO + os.sep):
        rel = os.path.relpath(path, REPO)
        needles += [rel, rel + os.sep]
    for p in texts:
        if os.path.getsize(p) > 8 * 1024 * 1024:
            continue
        try:
            with open(p, "rb") as fh:
                blob = fh.read()
        except OSError:
            return p
        for needle in needles:
            if needle.encode() in blob:
                return p
    return None


def live_runs(runs_root: str = RUNS_ROOT) -> list[str]:
    """Non-terminal subagent runs whose cwd is THIS repository.

    Only this repo's runs can be measuring inside this repo's kit. A status
    file that cannot be read, or that lacks a cwd, counts as live: refusal is
    cheap and a wrong removal is not. `runs_root` is a parameter, not a
    constant read, so a test can point the guard at a fixture directory.
    """
    live = []
    for status in glob.glob(os.path.join(runs_root, "*", "status.json")):
        try:
            with open(status, encoding="utf-8") as fh:
                rec = json.load(fh)
        except (OSError, ValueError):
            live.append(os.path.basename(os.path.dirname(status)) + ":unreadable")
            continue
        state = rec.get("state")
        cwd = rec.get("cwd")
        if cwd is None:
            live.append(os.path.basename(os.path.dirname(status)) + ":no-cwd")
            continue
        if state in TERMINAL:
            continue
        if os.path.realpath(cwd) == REPO:
            live.append(f"{os.path.basename(os.path.dirname(status))[:8]}({state})")
    return live


def inner_hazards(path: str) -> str | None:
    """Return the first reason this tree is not disposable scratch (G6).

    The target itself is checked first: an unreadable or mode-000 directory
    cannot be walked, so a list line naming a privacy fixture directly would
    otherwise find no hazard inside it and be removed. The standing fixtures
    are part of the gate signal (the reporter is expected to fail on them).
    """
    try:
        own = os.stat(path, follow_symlinks=False)
    except OSError as err:
        return f"cannot stat the target itself: {err}"
    if own.st_mode & 0o777 == 0:
        return f"the target is itself mode-000 ({path})"
    if not os.access(path, os.R_OK | os.X_OK):
        return f"the target is not readable by us ({path})"
    for root, dirs, files in os.walk(path, followlinks=False):
        if ".git" in dirs or ".git" in files:
            return f"contains a git entry at {root}"
        for name in dirs:
            full = os.path.join(root, name)
            try:
                mode = os.stat(full, follow_symlinks=False).st_mode
            except OSError as err:
                return f"cannot stat directory {full}: {err}"
            if mode & 0o777 == 0:
                return f"contains a mode-000 fixture {full}"
            if not os.access(full, os.R_OK | os.X_OK):
                return f"contains an unreadable directory {full}"
    return None


def size_of(path: str) -> int:
    total = 0
    for root, dirs, files in os.walk(path, followlinks=False):
        for name in files:
            try:
                total += os.stat(os.path.join(root, name),
                                 follow_symlinks=False).st_blocks * 512
            except OSError:
                continue
    return total


def check(path: str, texts: list[str], live: list[str]) -> tuple[bool, str, int]:
    if not os.path.isabs(path) or (GLOB_CHARS & set(path)):
        return False, "G1 not a literal absolute path", 0
    if path != os.path.realpath(path):
        return False, "G1 path is not its own realpath (symlink component)", 0
    if not path.startswith(LOCAL + os.sep):
        return False, "G2 outside .local/", 0
    rel = os.path.relpath(path, LOCAL)
    if rel in (".", "..") or rel.startswith(".."):
        return False, "G2 resolves to .local itself or above", 0
    if len(rel.split(os.sep)) < 2:
        return False, "G3 role root: reports and briefs are never removed", 0
    if path == SHARED or path == ATTIC or path.startswith(SHARED + os.sep) \
            or path.startswith(ATTIC + os.sep):
        return False, "G4 central kit / archive is never scratch", 0
    if os.path.islink(path):
        return False, "G5 is a symlink, not a directory", 0
    if not os.path.isdir(path):
        return False, "G5 not a directory", 0
    if live:
        return False, f"G8 live subagent run(s) present: {', '.join(live[:3])}", 0
    hazard = inner_hazards(path)
    if hazard:
        return False, "G6 " + hazard, 0
    where = referenced(path, texts)
    if where:
        return False, f"G7 named by gate artifact {os.path.relpath(where, REPO)}", 0
    return True, "removable", size_of(path)


def main(argv: list[str]) -> int:
    ap = argparse.ArgumentParser(prog="kit-rm.py", description=__doc__.split("\n")[0])
    ap.add_argument("--list", required=True, metavar="FILE",
                    help="newline-separated absolute paths to consider")
    ap.add_argument("--execute", action="store_true",
                    help="actually remove; requires --reason")
    ap.add_argument("--reason", default="", help="recorded with each removal")
    ap.add_argument("--runs-root", default=RUNS_ROOT,
                    help="async-runs directory used for the live-run guard")
    args = ap.parse_args(argv[1:])

    if args.execute and not args.reason.strip():
        print("refusing to remove anything: --execute requires a non-empty --reason",
              file=sys.stderr)
        return 2
    if not os.path.isfile(args.list):
        print(f"no such list file: {args.list}", file=sys.stderr)
        return 2

    lines = [l.strip() for l in open(args.list, encoding="utf-8") if l.strip()]
    if not lines:
        print("list file is empty; nothing to do", file=sys.stderr)
        return 2

    texts = gate_artifacts()
    if not os.path.isdir(args.runs_root):
        # An absent runs root cannot prove there is no live reviewer.
        print(f"G8: runs root {args.runs_root} is not a directory; "
              "removal is refused", file=sys.stderr)
        return 2
    live = live_runs(args.runs_root)
    if live:
        print(f"G8: {len(live)} non-terminal subagent run(s); removal is refused "
              "while a reviewer may be measuring", file=sys.stderr)

    removed = kept = 0
    freed = 0
    for path in lines:
        ok, why, nbytes = check(path, texts, live)
        if not ok:
            kept += 1
            print(f"KEEP    {why}: {path}")
            continue
        if not args.execute:
            kept += 1          # dry run removes nothing: every path is retained
            print(f"DRY     would remove {human(nbytes)}: {path}")
            continue
        # Re-verify the exact path right before the destructive call.
        ok2, why2, nbytes = check(path, texts, live_runs(args.runs_root))
        if not ok2:
            kept += 1
            print(f"KEEP    re-check failed ({why2}): {path}")
            continue
        try:
            shutil.rmtree(path)
        except OSError as err:
            kept += 1
            print(f"KEEP    removal failed ({err}): {path}")
            continue
        if os.path.exists(path):
            kept += 1
            print(f"KEEP    still present after rmtree: {path}")
            continue
        removed += 1
        freed += nbytes
        stamp = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())
        with open(os.path.join(SHARED, "removals.log"), "a", encoding="utf-8") as fh:
            fh.write(f"{stamp}\t{human(nbytes)}\t{path}\t{args.reason}\n")
        print(f"REMOVED {human(nbytes)}: {path}")

    mode = "EXECUTED" if args.execute else "DRY RUN"
    print(f"{mode}: {removed} removed, {kept} refused, {human(freed)} freed")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
