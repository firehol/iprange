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
      characters, and equals its own realpath (so neither a symlink nor a
      non-normalised path such as a trailing separator can slip through);
  G2  realpath is strictly inside `<repo>/.local/` (never `.local` itself);
  G3  depth >= 2 below `.local/` (a role root is never removed: its reports,
      HEARTBEAT and briefs survive every removal);
  G4  not `.local/shared` or `.local/_attic-md`, and not inside either -- the
      central kit and the archive are refused by identity, never by name match;
  G5  it is a real directory (not a symlink, not a file, not missing);
  G6  it contains no `.git` entry (a checkout or worktree is never scratch),
      no directory unreadable to us, and no mode-000 directory (the standing
      privacy fixtures ARE the gate signal; a mode-000 regular file is not a
      hazard because rmtree deletes it through the parent's write bit);
  G7  its exact path string appears in no gate artifact (every tracked file,
      `.local/shared/status.md`, `.local/shared/head`, every
      `.local/shared/evidence/*/manifest.json`, every
      `.local/shared/astra-turn*.md`): a path whose removal would invalidate
      a bound claim or the gate record is kept;
  G8  no live subagent child exists (a non-terminal run in the async-runs
      root means a role may be measuring inside its sandbox right now).

Dry run is the default: nothing is removed unless --execute is passed WITH
--reason. The audit record for each path (timestamp, size, path, reason) is
appended, flushed and fsynced to `.local/shared/removals.log` BEFORE the
destructive call, so a removal can never complete without a durable record;
a removal that fails after its record gets a compensating line carrying a
REMOVAL-FAILED field. Because the log is append-only, a compensating line
that itself cannot be written leaves a record that over-claims a destruction:
the tool then prints an ERROR line and exits rc 1 rather than reporting clean
success, so an uncorrectable trail is always surfaced. The log must be a
regular file (or absent): a symlink, directory or FIFO is refused, because
open("a") would follow or block on them and the audit trail would silently
not exist.

Usage:
  kit-rm.py --list FILE                      # verify and report only
  kit-rm.py --list FILE --execute --reason "gate-close, wave 18 complete"
"""
from __future__ import annotations

import argparse
import glob
import json
import os
import re
import shutil
import stat
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
# Bytes prose and JSON use to CLOSE a citation (astra turn-12/13/14/15 P1).
# A citation token is read at every trailing-fence length, one byte at a
# time, so a directory whose real name ends in one of these bytes
# (`.local/<role>/kit.`, ``.local/<role>/kit` ``) keeps its own reading while
# the fenced or sentence-final form is also seen. Adding a reading can only
# add a refusal, never remove one.
_CITATION_FENCE_BYTES = frozenset(".,;:)]}\"'`>} ")


AUDIT_LOG = os.path.join(SHARED, "removals.log")


def audit_log_problem() -> str | None:
    """Return why the audit log cannot hold a durable record, else None.

    The log must be a regular file (or absent). open("a") follows symlinks
    and blocks on FIFOs, so a link to /dev/null "succeeds" while nothing is
    durably recorded, and a FIFO hangs the tool (wave-29 fit-for-purpose
    P2-1). Appendability is probed, not assumed (wave-28 tester).
    """
    try:
        st = os.lstat(AUDIT_LOG)
    except FileNotFoundError:
        st = None
    if st is not None and not stat.S_ISREG(st.st_mode):
        return (f"the audit log {AUDIT_LOG} is not a regular file "
                f"({stat.filemode(st.st_mode)}); an append would not "
                f"durably record anything")
    if st is None:
        # Absent is a valid shape: the first append creates the log and
        # fsyncs the containing directory. Probing here with O_CREAT would
        # create the log and make append_audit believe it pre-existed,
        # skipping the directory fsync the creation requires (astra turn-13
        # P2). The directory must still be writable for that creation, so
        # check that without creating anything: an unwritable directory is
        # refused at startup, before any destructive work, exactly as the
        # existing-file probe refused it before.
        if not os.access(os.path.dirname(AUDIT_LOG), os.W_OK | os.X_OK):
            return (f"the audit log directory "
                    f"{os.path.dirname(AUDIT_LOG)} is not writable; the "
                    f"first record could not be created")
        return None
    try:
        # O_NOFOLLOW makes the open itself reject a symlink, so a swap
        # between the lstat above and this open cannot redirect the record
        # (wave-29 fit-for-purpose P2-2: the race is closed at the syscall,
        # not by check ordering). O_NONBLOCK makes the open itself reject a
        # FIFO: a write-only open of a FIFO with no reader fails with ENXIO
        # instead of blocking, and a FIFO with a reader is caught by the
        # fstat below (astra turn-12 P2: lstat + O_NOFOLLOW alone still let a
        # post-check FIFO swap hang the append). No O_CREAT: this path runs
        # only when the log already exists, and creating it here would
        # defeat append_audit's directory-fsync-on-creation (astra turn-13
        # P2).
        fd = os.open(AUDIT_LOG, os.O_WRONLY | os.O_APPEND
                     | os.O_NOFOLLOW | os.O_NONBLOCK)
    except OSError as err:
        return f"the audit log is not appendable ({err})"
    try:
        if not stat.S_ISREG(os.fstat(fd).st_mode):
            return ("the audit log is not a regular file after open "
                    "(swapped between check and open)")
    finally:
        os.close(fd)
    return None


def append_audit(line: str) -> str | None:
    """Append one audit record durably; return a refusal reason, else None.

    The shape check re-runs here, not only at startup: the log can be
    replaced between the two (wave-29 fit-for-purpose P2-2, a reproduced
    two-process chmod race). flush+fsync make the record durable BEFORE
    the destructive call it documents, so a write-time failure (ENOSPC,
    RLIMIT_FSIZE, a permission flip) refuses the removal instead of
    orphaning it (wave-29 parity P2-1).
    """
    # The directory is fsynced after every record, not only when this call
    # created the log. fsync(2) makes the FILE durable but not its DIRECTORY
    # ENTRY, and the write-ahead contract requires the record's name to
    # survive a crash that follows the removal. Deciding "did I create it"
    # from an lstat races with another process creating or replacing the log
    # between the check and the open, and the unsafe direction (skip the
    # directory sync on a file this call actually created) is exactly the
    # failure astra turn-13 P2 found. Removals are rare and human-gated, so
    # one extra directory fsync per record is the cheap, race-free shape.
    why = audit_log_problem()
    if why:
        return why
    try:
        fd = os.open(AUDIT_LOG, os.O_WRONLY | os.O_APPEND | os.O_CREAT
                     | os.O_NOFOLLOW | os.O_NONBLOCK)
    except OSError as err:
        return f"the audit record could not be written ({err})"
    # fdopen takes ownership of fd from here on: on any later error the
    # with-block closes it, so there is no second close (a double close could
    # release an unrelated recycled descriptor). The fstat check runs before
    # fdopen and must close the descriptor itself.
    if not stat.S_ISREG(os.fstat(fd).st_mode):
        os.close(fd)
        return ("the audit log is not a regular file after open "
                "(swapped between check and open)")
    try:
        with os.fdopen(fd, "a", encoding="utf-8",
                       errors="surrogateescape") as fh:
            fh.write(line)
            fh.flush()
            os.fsync(fh.fileno())
        dfd = os.open(os.path.dirname(AUDIT_LOG), os.O_RDONLY)
        try:
            os.fsync(dfd)
        finally:
            os.close(dfd)
    except OSError as err:
        return f"the audit record could not be written ({err})"
    return None


def human(n: float) -> str:
    return f"{n / (1024 * 1024):.1f} MB"


def gate_artifacts() -> list[str] | None:
    """Files whose content makes a kit path load-bearing for the record.

    Returns None when the tracked-file scan failed: every caller must treat
    that as a refusal, never as "no citations" (wave-26 tester finding).
    """
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
        # A failed scan of tracked files must REFUSE, not note-and-continue:
        # a citation in any tracked file would be invisible and a real
        # --execute would remove a path the record depends on
        # (wave-26 tester finding; REVIEWS.md § Kit hygiene: a failed
        # `git ls-files` leads to refusal, not to removal).
        return None
    files = [p.decode() for p in listing.split(b"\0") if p]
    # A successful-but-empty (or vacuous) listing is as blind as a failed
    # one: this tool is itself tracked, so any listing that does not name
    # its own path cannot be the tracked set, and G7's tracked-file half
    # would silently see no citations (wave-27 fit-for-purpose P3, closed
    # on intent: the requirement is that G7 never goes silently blind).
    if os.path.relpath(os.path.abspath(__file__), REPO) not in files:
        return None
    out += [os.path.join(REPO, p) for p in files]
    return [p for p in out if os.path.isfile(p)]


def referenced(path: str, texts: list[str]) -> str | None:
    """Return a human-readable reason this path is load-bearing, else None (G7).

    The return value is always a reason string, never a path: callers print it
    verbatim. Returning a path here once produced a relpath() of free text.

    The scan anchors on the CITATION, not on the removal path: every plausible
    `.local/...` path in the gate artifacts is extracted once (cached) and
    related to the removal path by COMPONENT relation. Anchoring on the
    removal path instead (searching the artifact for the path as a byte
    substring) produced both failure classes this guard has accumulated: a
    citation of `.local/<role>/kit11` matched the unrelated sibling
    `.local/<role>/kit1` as a substring (false refusal), and no substring
    reading of a removal path can recover a citation whose spelling differs
    from it (missed protection). Component relation refuses exactly when one
    path is the other or an ancestor of it at a '/' boundary, so a citation
    protects itself, its ancestors and its descendants, and nothing else.

    A citation is read at every space boundary and every trailing-fence
    length, because a real scratch directory may contain spaces (G1 accepts
    them) and may end in a punctuation byte that prose also uses to close a
    citation. Each reading is a candidate; a candidate equal to the citation
    as written reports "related to cited path", a derived reading reports
    "related to a path named by gate artifact". Adding a reading can only add
    a refusal, never remove one (astra turn-12/13/14/15 P1).

    `texts` is accepted for call-site stability; the scan re-reads the
    artifact set itself so a pre-deletion re-check sees files that appeared
    after planning.
    """
    cands = citation_candidates(texts)
    if cands is None:
        return ("gate artifacts could not be read or scanned; citations "
                "cannot be verified (refused, not skipped)")
    rel = os.path.relpath(path, REPO) if path.startswith(REPO + os.sep) else path
    relb = os.fsencode(rel)
    for cand, art, exact in cands:
        if cand.count(b"/") < 2:
            # names at most a role root, which G3 already protects from
            # removal; a bare-role mention in prose must not refuse the
            # role's whole subtree
            continue
        if relb == cand or relb.startswith(cand + b"/") or cand.startswith(relb + b"/"):
            if exact:
                return f"related to cited path {cand.decode(errors='replace')}"
            return f"related to a path named by gate artifact {art}"
    return None


def live_runs(runs_root: str = RUNS_ROOT) -> list[str]:
    """Non-terminal subagent runs whose cwd is THIS repository.

    Only this repo's runs can be measuring inside this repo's kit. A status
    file that cannot be read, or that lacks a cwd, counts as live: refusal is
    cheap and a wrong removal is not. `runs_root` is a parameter, not a
    constant read, so a test can point the guard at a fixture directory.

    An absent runs root counts as live too. The startup check refuses it with
    rc 2, but this function is also called by the pre-deletion recheck, where
    a root that vanished since planning cannot prove there is no live
    reviewer -- returning [] there would let the recheck pass while the
    evidence it depends on is gone (wave-24 finding: the documented
    fail-closed rule in REVIEWS.md § Kit hygiene held at startup but not at
    the recheck).

    An unreadable runs root counts as live by the same rule: isdir() passes
    while the glob silently sees no status files, so a mode-000 root looked
    like "no live runs" and a real --execute removed a tree with the
    evidence unreadable (wave-25 fit-for-purpose finding, same class one
    permission level away).

    Enumeration is scandir over the root, not a glob of */status.json: a
    glob silently omits a mode-000 RUN DIRECTORY, which hid a live
    repo-scoped run from both the startup scan and the pre-deletion recheck
    and a real --execute removed a tree beside it (wave-26 fit-for-purpose
    finding, the family one level deeper). With scandir, any run directory
    whose status cannot be inspected -- directory unreadable, file
    unreadable, malformed JSON -- counts as live: the whole fail-open family
    closes at one mechanism instead of one permission level at a time.
    """
    live = []
    try:
        run_dirs = [e.path for e in os.scandir(runs_root) if e.is_dir()]
    except OSError as err:
        return [f"runs-root {runs_root} cannot be listed ({err.__class__.__name__})"]
    for run_dir in run_dirs:
        status = os.path.join(run_dir, "status.json")
        try:
            with open(status, encoding="utf-8") as fh:
                rec = json.load(fh)
        except FileNotFoundError:
            continue
        except (OSError, ValueError):
            live.append(os.path.basename(run_dir) + ":unreadable")
            continue
        if not isinstance(rec, dict):
            # valid JSON that is not an object cannot prove the run terminal;
            # counting it live keeps the documented fail-closed rule instead
            # of crashing on .get (wave-27 parity P3-A)
            live.append(os.path.basename(run_dir) + ":not-an-object")
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
    # Removal is all-or-nothing: without write access part-way down, rmtree
    # deletes what it can and then fails, so a refusal would arrive after
    # partial destruction and nothing would be logged.
    if not os.access(path, os.W_OK):
        return f"the target is not writable by us; rmtree could half-delete ({path})"
    # The parent must be writable too: rmtree empties the target and then
    # unlinks the target directory itself, which needs write access on the
    # PARENT. An unwritable parent produced exactly the forbidden shape --
    # contents gone, KEEP printed after the destruction, nothing logged
    # (wave-27 tester; same construction as the batch-9 subtree finding).
    parent = os.path.dirname(path)
    if not os.access(parent, os.W_OK):
        return f"the parent is not writable by us; rmtree could half-delete ({parent})"
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
            if not os.access(full, os.R_OK | os.X_OK | os.W_OK):
                return (f"contains a directory we cannot fully traverse and "
                        f"delete ({full})")
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


_CITATION_CACHE: dict[tuple[str, ...], list | None] = {}


def citation_candidates(texts: list[str]) -> list | None:
    """Every plausible cited `.local/...` path in the gate artifacts.

    Returns (candidate-bytes, artifact-name, exact) tuples, or None when the
    tracked-file scan or an artifact read failed -- callers must treat None as
    a refusal, never as "no citations" (wave-26 tester). Cached per artifact
    set: the pre-deletion re-check passes a freshly read inventory, so a
    record file that appeared after planning is seen there (astra turn-13
    P2); the planning pass reuses one scan across all listed paths.

    For each `.local/` occurrence the token runs to end of line (a real
    scratch directory may contain spaces, which G1 accepts). Readings:
      * the first space boundary -- the citation as written (exact);
      * every later space boundary -- a citation whose name contains spaces
        followed by prose (derived);
      * every trailing-fence length of each -- prose and JSON close a
        citation with punctuation that may also be a real filename byte, so
        both readings are kept and a literal `kit.` or ``kit` `` directory
        name survives as its own candidate (derived).
    Trailing slashes are normalized away: a removal path is its own realpath
    and never carries one (G1). A reading with fewer than two slashes names
    at most a role root, which G3 already refuses, so it is dropped. Adding
    a reading can only add a refusal, never remove one (astra turn-12/13/
    14/15 P1).
    """
    key = tuple(texts)
    if key in _CITATION_CACHE:
        return _CITATION_CACHE[key]
    found: set[tuple[bytes, str, bool]] = set()
    for art in texts:
        try:
            # surrogateescape, not replace: a removal path reaches this
            # function through the list file read with surrogateescape, and
            # os.fsencode reverses that mapping exactly. Reading the artifact
            # with replace would mangle any invalid byte in a citation to
            # U+FFFD, so a citation naming a byte-invalid path could never
            # match the removal path -- the old byte-substring scan compared
            # raw bytes and did not have this hole (astra turn-15 review,
            # lead's own audit).
            text = open(art, encoding="utf-8", errors="surrogateescape").read()
        except OSError:
            _CITATION_CACHE[key] = None
            return None
        art_name = os.path.relpath(art, REPO)
        i = 0
        while True:
            i = text.find(".local/", i)
            if i < 0:
                break
            j = i
            while j < len(text) and text[j] not in "\n\r\v\f":
                j += 1
            parts = text[i:j].split(" ")
            for m in range(1, len(parts) + 1):
                seg = " ".join(parts[:m]).rstrip("/")
                # "exact" = the citation as written: the first space
                # boundary with its closing fences removed (a fence is never
                # part of a written path, so every fence-stripped length of
                # the first segment is still "as written"). Every reading
                # past the first space boundary is a guess that the name
                # contains spaces, hence derived.
                exact = (m == 1)
                as_written = {seg}
                k = len(seg)
                while k > 0 and seg[k - 1] in _CITATION_FENCE_BYTES:
                    k -= 1
                    as_written.add(seg[:k].rstrip("/"))
                for r in as_written:
                    found.add((os.fsencode(r), art_name, exact))
                # A removal path is its own realpath (G1), so a citation
                # spelled with a lexical ., .., doubled slash or trailing
                # slash names the same directory as its normalized form and
                # must protect it too. normpath is a derived reading (a
                # reading can only add a refusal, never remove one); a
                # component that is merely named "..x" is untouched.
                for r in as_written:
                    n = os.path.normpath(r)
                    if n != r:
                        found.add((os.fsencode(n), art_name, False))
            # advance by one, not to end of line: a second `.local/` citation
            # on the same line is a separate anchor and must be extracted too
            # (advancing to j would silently drop every citation after the
            # first on a line).
            i += 1
    result = sorted(found)
    _CITATION_CACHE[key] = result
    return result


def check(path: str, texts: list[str], live: list[str]) -> tuple[bool, str, int]:
    if not os.path.isabs(path) or (GLOB_CHARS & set(path)):
        return False, "G1 not a literal absolute path", 0
    if path != os.path.realpath(path):
        return False, "G1 path is not its own realpath (symlink or non-normalised path)", 0
    if not path.startswith(LOCAL + os.sep):
        return False, "G2 outside .local/", 0
    rel = os.path.relpath(path, LOCAL)
    # Component comparison, not a string prefix: a sandbox legitimately named
    # "..reviewer" has a rel path starting with ".." but is inside .local/
    # (astra turn-12 P3).
    if rel in (".", "..") or rel.split(os.sep)[0] == "..":
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
        return False, "G7 " + where, 0
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
    # A list line can carry bytes that decode to lone surrogates. Guards and
    # removal must see the exact path, so the bytes are preserved and only the
    # OUTPUT channels tolerate them -- the same split kit-gc.py uses: the report
    # is display, the log is data (wave-20 portability P3-1; a bare encode()
    # raised inside the removal loop, after other paths had already gone).
    for stream in (sys.stdout, sys.stderr):
        try:
            stream.reconfigure(errors="backslashreplace")
        except (AttributeError, ValueError):
            pass

    if not os.path.isfile(args.list):
        print(f"no such list file: {args.list}", file=sys.stderr)
        return 2

    # A list line is a path, so it is preserved verbatim: stripping whitespace
    # here made `--execute` on "<dir> " remove a DIFFERENT directory (<dir>)
    # and log the stripped name, hiding what was destroyed. Only the newline is
    # removed, and a line that is whitespace-only is refused as a bad path
    # rather than silently dropped.
    raw = open(args.list, encoding="utf-8", errors="surrogateescape").read().split("\n")
    lines = [l for l in (x.rstrip("\r") for x in raw) if l != ""]
    if not lines:
        print("list file is empty; nothing to do", file=sys.stderr)
        return 2
    # A line of only whitespace is a malformed list, not a blank separator: it
    # must be reported rather than dropped (so an operator cannot believe a
    # path was submitted when the editor stripped it) and never reached as a
    # stripped path that names a different directory.
    blank = [i for i, l in enumerate(lines, 1) if not l.strip()]
    if blank:
        print(f"list line(s) contain only whitespace: {blank}; refusing to run",
              file=sys.stderr)
        return 2

    texts = gate_artifacts()
    if texts is None:
        # G7 cannot be evaluated without the tracked-file scan; refusing is
        # the documented behavior (REVIEWS.md § Kit hygiene; wave-26 tester).
        print("G7: git ls-files failed or returned a vacuous listing; tracked "
              "citations cannot be verified; removal is refused", file=sys.stderr)
        return 2
    if args.execute:
        # The audit log is part of the removal contract: a removal that cannot
        # be logged must not happen. Without this probe an unwritable log let
        # rmtree complete, the append then crashed, and the run reported
        # nothing about what it destroyed (wave-28 tester). The probe runs
        # once here; the per-path append re-checks, because the record is
        # written BEFORE the destructive call (wave-29).
        why = audit_log_problem()
        if why:
            print(f"refusing to remove anything: {why}", file=sys.stderr)
            return 2
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
    # Set when the log holds a write-ahead record that could not be
    # corrected after the removal failed: the trail then over-claims a
    # destruction, and the run must not report clean success (wave-30
    # parity P2-1).
    log_inconsistent = False
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
        # Re-verify the exact path right before the destructive call, against
        # CHANGED state: the citation cache and the artifact inventory are
        # process snapshots from startup, and a record can gain a citation of
        # this path (or a new artifact can appear) while the run is in
        # progress. A re-check on stale inputs is not a re-check (astra
        # turn-12 P2). Removals are rare and human-gated, so the per-path
        # refresh cost is irrelevant next to the guard it restores.
        global _CITATION_CACHE
        _CITATION_CACHE = {}
        texts2 = gate_artifacts()
        if texts2 is None:
            kept += 1
            print(f"KEEP    re-check refused: tracked-file scan failed: {path}")
            continue
        ok2, why2, nbytes = check(path, texts2, live_runs(args.runs_root))
        if not ok2:
            kept += 1
            print(f"KEEP    re-check failed ({why2}): {path}")
            continue
        # Write-ahead audit: the record is durable before the path is
        # destroyed, so no removal can complete unlogged and no crash can
        # orphan a deletion (wave-29). A removal that fails after its record
        # gets a compensating line. Because the log is append-only, a record
        # or correction that cannot be written leaves a trail that
        # over-claims a destruction; every such failure is surfaced (ERROR +
        # rc 1), never silently believed (wave-31).
        stamp = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())
        # surrogateescape so the recorded path is the bytes that were removed,
        # not a replacement character: the log is the audit trail.
        why = append_audit(f"{stamp}\t{human(nbytes)}\t{path}\t{args.reason}\n")
        if why:
            kept += 1
            print(f"KEEP    audit record refused ({why}): {path}")
            # A failed record append can still leave a torn, newline-less
            # fragment that reads like a removal record and glues onto every
            # later line (wave-31 fit-for-purpose P2). The removal did not
            # happen, so the trail over-claims it: surface, do not believe.
            print(f"ERROR   audit record for {path} could not be written "
                  f"({why}); the log may hold a torn record", file=sys.stderr)
            log_inconsistent = True
            continue
        try:
            shutil.rmtree(path)
        except OSError as err:
            kept += 1
            why = append_audit(f"{stamp}\t-\t{path}\t{args.reason}\tREMOVAL-FAILED: {err}\n")
            print(f"KEEP    removal failed ({err}): {path}")
            if why:
                # The bare write-ahead record is still in the log and now
                # looks like a successful removal for a path that survived.
                # An append-only log cannot retract it, so the run must not
                # report clean success: surface the inconsistency and fail.
                print(f"ERROR   audit record for {path} claims a removal that "
                      f"did not happen and could not be corrected ({why})",
                      file=sys.stderr)
                log_inconsistent = True
            continue
        if os.path.exists(path):
            kept += 1
            why = append_audit(f"{stamp}\t-\t{path}\t{args.reason}\tREMOVAL-FAILED: still present\n")
            print(f"KEEP    still present after rmtree: {path}")
            if why:
                print(f"ERROR   audit record for {path} claims a removal that "
                      f"did not happen and could not be corrected ({why})",
                      file=sys.stderr)
                log_inconsistent = True
            continue
        removed += 1
        freed += nbytes
        print(f"REMOVED {human(nbytes)}: {path}")

    mode = "EXECUTED" if args.execute else "DRY RUN"
    print(f"{mode}: {removed} removed, {kept} refused, {human(freed)} freed")
    if log_inconsistent:
        # The audit trail holds a record that over-claims a destruction and
        # could not be corrected. The removals that succeeded are fine, but
        # the run must not report clean success: the log needs operator
        # attention (wave-30 parity P2-1).
        print("WARNING: the audit log holds an uncorrectable record; see the "
              "ERROR lines above", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
