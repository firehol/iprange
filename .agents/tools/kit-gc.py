#!/usr/bin/env python3
"""kit-gc.py — read-only review-kit usage reporter for .local/.

WHY: reviewer sandboxes historically copied whole repo trees with their
cargo/go/C build targets (~20-26 GB each); eighteen such trees reached
370 GB under .local/ (REVIEWS.md § Kit hygiene). This tool MEASURES and
REPORTS; it is the inspection step of the recurring close-out duty.

CONTRACT (user decision 2026-09-19, advisor ruling 2026-09-19): this is a
usage reporter only. It deletes nothing, archives nothing, and claims
nothing is safe for removal. Automatic deletion and its safety
classifier were removed because the operational need is a rare, one-off
removal of a few named directories — machinery beyond that need.
Deletion is the human procedure defined in REVIEWS.md § Kit hygiene:
establish ownership, inactivity and preservation needs for a specific
path, then remove it by name. Prevention at the source (symlink farms,
staged binaries, shared build targets, the ≤ 1 GB sandbox cap) remains
mandatory and is what this reporter verifies.

Modes (all read-only):
  kit-gc.py            report: allocated + apparent size per top-level
                       .local/ dir and per role sandbox vs the cap; an
                       inventory of build-target-named and large
                       directories FOR HUMAN INSPECTION (path, sizes,
                       newest modification time). The inventory is data
                       for a decision, never a removal verdict.
  kit-gc.py --json     the same data as machine-readable JSON;
                       "inspection_errors" is a list of {path, error}
                       objects, one per unmeasurable subtree.

Exit codes (distinct failure classes, all visible):
  0  scan complete; every role sandbox within the cap
  1  scan complete; at least one role sandbox exceeds the cap (finding)
  2  scan INCOMPLETE: inspection errors (unreadable dir, stat failure);
     the printed numbers are partial and say so.  An unexpected internal
     failure in this file's own execution -- module initialization or main
     -- is also 2, with "scan: INCOMPLETE" on stdout, so an unforeseen crash
     can never be read as a completed over-cap scan.  The traceback is
     best-effort on the current stderr and the INCOMPLETE line is best-effort
     on the current stdout: a hostile stream can suppress either, while the
     exit status still classifies (see announce_incomplete).  A failure that
     prevents this module from executing at all (SyntaxError or ImportError at
     load) exits with the interpreter's own status; no file can classify its
     own failure to load.

Sizes: "allocated" = st_blocks*512 summed WITHOUT following symlinks —
the disk-hygiene metric (a symlink farm costs link bytes, not its
targets' bytes; a sparse file costs only its mapped blocks, so its holes
are NOT charged). "apparent" = sum of regular-file st_size PLUS
symlink-value lengths (both kinds of link are measured as stored, never
followed); directory entries contribute their allocated blocks but no
apparent bytes. Apparent is therefore a content/link-value measure and
can be lower or higher than allocated; neither substitutes for the
other.

Known limitations (deliberate, not oversights):
  * modification times are reported, never interpreted: recency is an
    input to the human's inactivity judgment, not a stale/active
    classification;
  * the build-target-named pattern (target/, ctarget/, gocache/,
    cargotgt*, rust-target*, llvm-cov-target/) is a name heuristic for
    display grouping; large directories are additionally listed by size.
    Neither list implies a removal recommendation;
  * a subtree that cannot be measured is reported as an inspection
    error and contributes NO numbers; zero is never substituted for an
    unreadable measurement.
"""
import argparse
import datetime
import json
import os
import sys
import traceback
from pathlib import Path

BUILD_NAMES = {"target", "ctarget", "gocache", "llvm-cov-target"}
BUILD_PREFIXES = ("cargotgt", "rust-target")
DEFAULT_CAP_BYTES = 1024 * 1024 * 1024   # REVIEWS.md sandbox cap: 1 GB
LARGE_DIR_BYTES = 512 * 1024 * 1024      # listing threshold for inspection


def repo_and_root():
    here = Path(__file__).resolve().parent          # <repo>/.agents/tools
    if here.parent.name == ".local":                # legacy invocation spot
        return here.parent.parent, here.parent
    return here.parent.parent, here.parent.parent / ".local"


def announce_incomplete(where: str) -> int:
    """Report an internal failure as INCOMPLETE (class 2), never as 1.

    rc 1 asserts a completed scan that found a sandbox over the cap, so a
    failure anywhere in this program's own execution must not exit 1.  The
    traceback is best-effort on the current stderr: a hostile stderr (a
    strict error handler on a surrogate-bearing message) must not turn the
    classifier into the crash it classifies.  The INCOMPLETE line goes to
    the stdout the backslashreplace policy already protects.
    """
    try:
        traceback.print_exc()
    except Exception:
        pass
    try:
        print(f"scan: INCOMPLETE; exit 2 (internal failure in {where}; "
              "no measurement in this run is trustworthy)")
    except Exception:   # a closed or otherwise hostile stdout must not turn
        pass            # the classifier into the crash it classifies
    return 2


# Paths on disk can hold bytes that decode to lone surrogates; printing
# them must never crash the report or flip the exit class (wave-13:
# text mode died with UnicodeEncodeError on a surrogate-named chmod-000
# fixture and returned rc 1 "over-cap" where the truth was rc 2
# "incomplete"; --json was immune). Encoding errors are escaped for the
# whole run instead.
try:
    sys.stdout.reconfigure(errors="backslashreplace")
except (AttributeError, ValueError):    # non-replaceable stdout (pipes
    pass                                # already reconfigured, exotic)

# A failure here is an internal failure (class 2), not a completed scan.
try:
    REPO, ROOT = repo_and_root()
    SHARED_DIR = ROOT / "shared"        # the central kit: never a candidate
    ATTIC_DIR = ROOT / "_attic-md"      # exhibit archive: never a candidate
except Exception:
    raise SystemExit(announce_incomplete("module initialization"))


def ancestors_to_root(d: Path):
    """[d, parent, ..., ROOT] (inclusive), stopping at ROOT."""
    out = [d]
    cur = d
    while cur != cur.parent:
        cur = cur.parent
        out.append(cur)
        if cur == ROOT:
            break
    return out


def scan():
    """Single read-only pass over ROOT.

    Returns (agg, errors): agg maps Path -> [allocated, apparent,
    newest_mtime]; every ancestor directory under ROOT accumulates the
    sizes of all files and dirs beneath it (no symlink following, so a
    farm costs link bytes only). Unreadable entries append to errors and
    contribute nothing.
    """
    agg = {}
    # (path, message) pairs: PARTIAL attribution must use the real
    # erroring path, never a first-colon split of a formatted string
    # (Windows drive letters, colon-named roles -- wave-13 P3).
    errors: list = []

    def add(path: Path, alloc: int, app: int, mtime: float):
        for anc in ancestors_to_root(path):
            slot = agg.setdefault(anc, [0, 0, 0.0])
            slot[0] += alloc
            slot[1] += app
            if mtime > slot[2]:
                slot[2] = mtime

    def onerror(err):
        errors.append((str(getattr(err, "filename", "?")), str(err)))

    if not ROOT.is_dir():
        return agg, [(str(ROOT), "missing")]
    for dp, dirs, files in os.walk(ROOT, onerror=onerror, followlinks=False):
        d = Path(dp)
        agg.setdefault(d, [0, 0, 0.0])
        try:
            st = os.stat(d, follow_symlinks=False)
            add(d, st.st_blocks * 512, 0, st.st_mtime)  # dir's own blocks
        except OSError as err:
            errors.append((str(d), str(err)))
        for name in files:
            fp = os.path.join(dp, name)
            try:
                st = os.stat(fp, follow_symlinks=False)
            except OSError as err:
                errors.append((fp, str(err)))
                continue
            add(d, st.st_blocks * 512, st.st_size, st.st_mtime)
        # os.walk puts directory symlinks in `dirs` and, with
        # followlinks=False, never visits them -- so their own inode is
        # never statted by the loop above. Measure the link itself
        # (blocks, link value length, mtime) WITHOUT traversing its
        # target; otherwise usage and recency are silently understated
        # while the scan claims to be complete.
        for name in dirs:
            dp2 = os.path.join(dp, name)
            if os.path.islink(dp2):
                try:
                    st = os.stat(dp2, follow_symlinks=False)
                except OSError as err:
                    errors.append((dp2, str(err)))
                    continue
                add(d, st.st_blocks * 512, st.st_size, st.st_mtime)
    return agg, errors


def build_named(name: str) -> bool:
    return name in BUILD_NAMES or name.startswith(BUILD_PREFIXES)


def fmt_mb(n: float) -> str:
    return f"{n / (1024 * 1024):9.1f} MB"


def esc(text) -> str:
    """Escape line-breaking characters in a printed path or name.

    The text report is line-oriented: a directory whose NAME contains a
    newline can otherwise inject a whole line into the report, which a
    human reader (and any line-based gate predicate) cannot tell from a
    real one (wave-15 portability P3: a fixture named
    "evil\\nscan: COMPLETE; exit 0" echoed as a plausible report line).
    Only \\r and \\n are escaped: they are what forges lines for every
    consumer this report has (the gate predicates are `^-`anchored `grep`, and
    `grep`, `sed` and shell line reading treat only \\n as a line break), and
    leaving every other byte alone keeps Windows and colon-named paths
    readable.  Residual, by design: a reader using str.splitlines() also
    breaks on \\v, \\f, \\x1c, \\x1d, \\x1e, \\x85, U+2028 and U+2029, none
    of which is escaped here, so such a reader would see additional "lines"
    from a hostile name.  Nothing on this project's gate path reads the report
    that way.
    """
    return str(text).replace("\r", "\\r").replace("\n", "\\n")


def format_error_row(path: str, message: str) -> str:
    """One INSPECTION ERRORS line, with both fields line-safe.

    A message is a printed field exactly like the path: escaping only the path
    left the line forgeable by any exception whose str() embeds a raw newline.
    Today every shape scan() appends is an OSError, whose str() repr-quotes the
    filename, so that safety was incidental rather than designed; keeping the
    formatting in one function is what makes it assertable without a hostile
    filesystem. --json does not use this: JSON's own quoting protects it.
    """
    return f"ERROR  {esc(path)}: {esc(message)}"


def ts(mtime: float) -> str:
    if not mtime:
        return "unknown        "
    return datetime.datetime.fromtimestamp(mtime).strftime("%Y-%m-%d %H:%M")


def main() -> int:
    ap = argparse.ArgumentParser(
        prog="kit-gc.py",
        description="Read-only usage reporter for .local/ "
                    "(deletes nothing; recommends nothing).")
    ap.add_argument("--json", action="store_true",
                    help="machine-readable report on stdout")
    ap.add_argument("--cap", type=int, default=DEFAULT_CAP_BYTES,
                    metavar="BYTES",
                    help="sandbox cap in bytes (default 1 GiB, the "
                         "REVIEWS.md policy; override only to test or "
                         "when the policy itself changes)")
    args = ap.parse_args()

    agg, errors = scan()
    children = sorted(p for p in agg if p.parent == ROOT and p.is_dir())
    # One cap decision per ROLE SANDBOX (.local/<role>/ aggregate, per
    # REVIEWS.md and .agents/review-roles/README.md), computed
    # independently of child rows: a sandbox holding only files has no
    # child units and must still be measured against the cap.
    sandboxes = []
    role_units = []
    for role in children:
        if role in (SHARED_DIR, ATTIC_DIR):
            continue
        role_alloc = agg.get(role, [0, 0, 0.0])[0]
        sandboxes.append({"role": role.name, "path": str(role),
                          "allocated": role_alloc,
                          "apparent": agg.get(role, [0, 0, 0.0])[1],
                          "newest_mtime": agg.get(role, [0, 0, 0.0])[2],
                          "over_cap": role_alloc > args.cap})
        for unit in sorted(p for p in agg if p.parent == role and p.is_dir()):
            a = agg.get(unit, [0, 0, 0.0])
            role_units.append({"role": role.name, "unit": unit.name,
                               "path": str(unit), "allocated": a[0],
                               "apparent": a[1], "newest_mtime": a[2],
                               "sandbox_allocated": role_alloc})
    top_level = [{"path": str(c), "allocated": agg.get(c, [0, 0, 0])[0],
                  "apparent": agg.get(c, [0, 0, 0])[1],
                  "newest_mtime": agg.get(c, [0, 0, 0])[2]}
                 for c in children]
    candidates = []
    for d, a in agg.items():
        if d == ROOT or ROOT not in d.parents:
            continue
        # exclude the central kit and the archive by IDENTITY (they are
        # single directories under ROOT), not by matching a name anywhere
        # in the path -- a checkout under an ancestor called "shared" must
        # still surface its candidates
        if d == SHARED_DIR or d == ATTIC_DIR:
            continue
        if SHARED_DIR in d.parents or ATTIC_DIR in d.parents:
            continue
        why = None
        if build_named(d.name):
            why = "name matches a build-output pattern"
        elif a[0] > LARGE_DIR_BYTES:
            why = f"allocated larger than {LARGE_DIR_BYTES // (1024*1024)} MB"
        if why:
            candidates.append({"path": str(d), "allocated": a[0],
                               "apparent": a[1], "newest_mtime": a[2],
                               "why": why})
    candidates.sort(key=lambda c: -c["allocated"])

    # Map each inspection error to the sandbox whose measurement it
    # invalidates (a 0.0/low aggregate under an unreadable subtree is a
    # PARTIAL row, never a clean measurement -- operations wave-12 F-4).
    err_sandboxes: dict = {}
    for ep, _msg in errors:
        rel = os.path.relpath(ep, ROOT) if ep.startswith(str(ROOT) + os.sep) else None
        # Component comparison, not a string prefix: a sandbox legitimately
        # named "..reviewer" has a rel path starting with ".." but is inside
        # the root, and must keep its PARTIAL marker (astra turn-12 P3).
        if rel and rel != os.pardir and not rel.startswith(os.pardir + os.sep):
            top = rel.split(os.sep)[0]
            err_sandboxes[top] = err_sandboxes.get(top, 0) + 1
    for sbx in sandboxes:
        n = err_sandboxes.get(sbx["role"], 0)
        if n:
            sbx["partial_errors"] = n
    over = [s for s in sandboxes if s["over_cap"]]
    if errors:
        rc, state = 2, "INCOMPLETE"
    elif over:
        rc, state = 1, "COMPLETE"
    else:
        rc, state = 0, "COMPLETE"

    # A pipe consumer may exit before the buffered writes flush; that is
    # not a scan failure. The devnull recipe below keeps the scan's own
    # exit class from leaking as a 120 flush error.
    try:
        if args.json:
            print(json.dumps({
                "scan_state": state, "root": str(ROOT),
                "cap_bytes": args.cap,
                "top_level": top_level, "sandboxes": sandboxes,
                "role_units": role_units,
                "inspect_candidates": candidates,
                "inspection_errors": [{"path": ep, "error": em}
                                      for ep, em in errors],
                "note": "read-only reporter; inventory is data for human "
                        "inspection, never a removal verdict",
            }, indent=1))
        else:
            print(f"== {esc(ROOT)} usage (allocated = blocks*512, symlinks "
                  "not followed) ==")
            for t in top_level:
                print(f"{fmt_mb(t['allocated'])} alloc  "
                      f"{fmt_mb(t['apparent'])} app  {esc(t['path'])}")
            print(f"\n== role sandboxes vs {args.cap // (1024 * 1024)} MB "
                  "cap (one decision per .local/<role>/ aggregate) ==")
            if not sandboxes:
                print("(none found)")
            units_by_role = {}
            for r in role_units:
                units_by_role.setdefault(r["role"], []).append(r)
            for s in sorted(sandboxes, key=lambda s: -s["allocated"]):
                flag = "  OVER-CAP" if s["over_cap"] else ""
                if s.get("partial_errors"):
                    n_pe = s["partial_errors"]
                    flag += ("  PARTIAL (%d inspection error%s below)"
                             % (n_pe, "" if n_pe == 1 else "s"))
                print(f"{fmt_mb(s['allocated'])} alloc  "
                      f"SANDBOX {esc(s['role'])}{flag}")
                for r in sorted(units_by_role.get(s["role"], []),
                                key=lambda r: -r["allocated"]):
                    print(f"{fmt_mb(r['allocated'])} alloc    "
                          f"unit {esc(r['unit'])}")
            print("\n== directories for HUMAN INSPECTION -- name/size facts "
                  "only, NOT removal recommendations ==")
            if not candidates:
                print("(none)")
            for c in candidates:
                print(f"{fmt_mb(c['allocated'])} alloc  "
                      f"newest {ts(c['newest_mtime'])}  "
                      f"[{c['why']}]  {esc(c['path'])}")
            if errors:
                print("\n== INSPECTION ERRORS: scan INCOMPLETE, numbers "
                      "partial ==")
                for ep, em in errors:
                    print(format_error_row(ep, em))
            print(f"\nscan: {state}; exit {rc} "
                  "(0 within cap, 1 over-cap finding, 2 inspection error)")
            print("Removal is a human procedure (REVIEWS.md 'Kit hygiene'): "
                  "ownership, inactivity, preservation checks, then remove "
                  "by name.")
        sys.stdout.flush()
    except BrokenPipeError:
        devnull = os.open(os.devnull, os.O_WRONLY)
        os.dup2(devnull, sys.stdout.fileno())
    return rc


def main_guarded() -> int:
    """Run main(), classifying an unforeseen failure as INCOMPLETE.

    The exit classes above are a contract the caller trusts: rc 1 asserts
    a completed scan that found a sandbox over the cap.  An unexpected
    exception must therefore never leave the process as 1 (wave-14: a
    string/pair mismatch in the inspection-error list did exactly that,
    reporting "over-cap" for a scan that had produced no report at all).
    """
    try:
        return main()
    except Exception:  # noqa: BLE001 - deliberate last-resort classification
        return announce_incomplete("main")


if __name__ == "__main__":
    sys.exit(main_guarded())
