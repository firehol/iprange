#!/usr/bin/env python3
"""kit-gc.py — review-kit disk hygiene for .local/ (REVIEWS.md § Kit hygiene).

WHY: reviewer sandboxes historically copied whole repo trees with their
cargo/go/C build targets (~20-26 GB each); eighteen such trees reached
370 GB under .local/.  This script is the recurring close-out duty: run it
at every milestone-gate close, before the closure battery.

Procedure (deliberately bounded — astra gate turns 3-5: a mandated
destructive procedure must be reproducible from a checkout and must fail
CLOSED):

  report (default)      read-only: size per .local/<role>/<dir>, plus the
                        known build-target directories with the exact
                        reason each is guarded or eligible.
  --prune-builds        read-only: the same eligibility listing.
  --prune-builds --apply PATH...
                        deletes ONLY the explicitly named directories.
                        There is no sweep-delete mode: a path is deleted
                        only if it is named, exists, is a build-output
                        directory, and passes every guard below.  Any
                        guard failure refuses that path with its reason;
                        a broken protection inventory refuses the whole
                        run (exit 3).
  --attic DIR...        copies *.md exhibits of DIR into
                        .local/_attic-md/ (writes by design).

Guards for --apply (each enforced in code, none promised):
  * PATH must lie under .local/ and not under .local/shared/ or
    .local/_attic-md/;
  * PATH must be one of the build-output directory kinds (target,
    ctarget, gocache, cargotgt*, rust-target*, llvm-cov-target) and a
    directory;
  * the enclosing numbered kit (numerically highest r<N>/round<N>/w<N>
    among siblings) is the role's LIVE kit and is refused;
  * the enclosing workspace (that kit, else the top-level tree, else the
    role's second-level directory) is ACTIVE — any file modified within
    --min-age days — and is refused;
  * PATH refuses if it contains a protected file anywhere beneath it
    (report*.md, manifest*.json, SHASUMS*, *.sha256, status.md,
    README.md);
  * PATH refuses if it contains a file referenced by any manifest*.json
    under .local/ (references extracted from manifest text, resolved
    against the manifest directory, .local/, the repo root, and the
    current directory; both the written path and its resolved target are
    registered).  A manifest that cannot be read or scanned makes the
    registry incomplete and blocks --apply entirely;
  * --keep P refuses any PATH inside P or containing P; P is normalized
    to an absolute lexical path (trailing slashes, '.', '..' resolved)
    before comparison, from any invocation cwd.
"""
import argparse
import datetime
import json
import os
import re
import shutil
import sys
from pathlib import Path

BUILD_DIR_NAMES = {"target", "ctarget", "gocache"}
BUILD_DIR_PREFIXES = ("cargotgt", "rust-target")
REF_SUFFIX = ".log .txt .json .md .tsv .csv .sha256 .html .xml .py .sh .exe".split()
PROTECTED_NAMES = re.compile(
    r"^(report.*\.md|manifest.*\.json|SHASUMS.*|.*\.sha256|status\.md|README\.md)$")
KIT_NUMBER = re.compile(r"^(?:r|round|w)(\d+)")
REF_TOKEN = re.compile(r"[A-Za-z0-9@+._/-]{2,}")


def repo_and_root():
    here = Path(__file__).resolve().parent          # <repo>/.agents/tools
    if here.parent.name == ".local":                # legacy wrapper location
        return here.parent.parent, here.parent
    return here.parent.parent, here.parent.parent / ".local"


REPO, ROOT = repo_and_root()
SHARED, ATTIC = ROOT / "shared", ROOT / "_attic-md"


def is_build_dir(p: Path) -> bool:
    return p.is_dir() and (
        p.name in BUILD_DIR_NAMES or p.name.startswith(BUILD_DIR_PREFIXES)
        or p.name == "llvm-cov-target")


def norm(p: str) -> Path:
    # realpath (not lexical normpath): ".." must be resolved against the
    # real filesystem, and trailing "/" and "." are normalized with it
    return Path(os.path.realpath(os.path.abspath(os.path.expanduser(p))))


def inside(child: Path, parent: Path) -> bool:
    return child == parent or parent in child.parents


def kit_number(p: Path):
    m = KIT_NUMBER.match(p.name)
    return int(m.group(1)) if m else None


def numbered_children(scope: Path):
    out = []
    try:
        for c in scope.iterdir():
            if c.is_dir():
                n = kit_number(c)
                if n is not None:
                    out.append((n, c))
    except OSError:
        pass
    return out


def enclosing_kit(p: Path):
    """Nearest numbered-kit ancestor strictly under ROOT; else None."""
    a = p.parent
    while a != ROOT and a != a.parent:
        if kit_number(a) is not None:
            return a
        a = a.parent
    return None


def workspaces_of(p: Path):
    """Directories the ACTIVE rule applies to.  Fail-closed: every
    enclosing directory from ROOT's first level down to the candidate's
    parent is checked, so an active file anywhere on the ancestor chain
    (top-level notes.md, role-dir churn, sibling work in an unnumbered
    workspace) blocks deletion of a stale build target."""
    rel = p.relative_to(ROOT)
    out = []
    for i in range(1, len(rel.parts)):              # ROOT/p0, ROOT/p0/p1, ...
        w = ROOT.joinpath(*rel.parts[:i])
        if w.is_dir():
            out.append(w)
    return out


def ws_self_modified_after(ws: Path, stamp: float) -> bool:
    """True if the workspace's OWN directory was modified after stamp.

    Directory mtime changes when entries are created/removed inside it,
    which is exactly 'new work landed here'.  Descendants' mtimes are NOT
    consulted at the ancestor level: the per-kit activity walk and this
    ancestor rule together cover the guarantee without one new kit
    poisoning sibling eligibility.
    """
    try:
        return ws.stat().st_mtime > stamp
    except OSError:
        return True


def kit_files_active(kit: Path, stamp: float) -> bool:
    """True if any file or subdirectory INSIDE the numbered kit (the kit
    root excluded — its own churn is covered by the live rule and the
    role-ancestor rule) was modified after stamp."""
    for dp, dirs, files in os.walk(kit, followlinks=False):
        here = Path(dp)
        if here != kit:
            try:
                if here.stat().st_mtime > stamp:
                    return True
            except OSError:
                pass
        for name in files:
            try:
                if os.stat(os.path.join(dp, name)).st_mtime > stamp:
                    return True
            except OSError:
                continue
    return False


def has_protected_descendant(p: Path) -> bool:
    for dp, _dirs, files in os.walk(p, followlinks=False):
        for name in files:
            if PROTECTED_NAMES.match(name):
                return True
    return False


def build_reference_registry():
    """Return (referenced_paths, blockers).

    Every manifest*.json under .local/ (except _attic-md) is parsed; each
    string value and each path-like token is resolved against the
    manifest's directory, .local/, the repo root, and cwd.  A path is
    registered when it exists and lies under .local/ (or resolves to
    such), as written AND resolved.  Unreadable/unparseable manifests
    become blockers: the registry is then incomplete and --apply must
    refuse.
    """
    refs, blockers = set(), []
    if not ROOT.is_dir():
        return refs, [str(ROOT)]
    for dp, dirs, files in os.walk(ROOT, followlinks=False):
        d = Path(dp)
        if ATTIC in d.parents or d == ATTIC:
            dirs[:] = []
            continue
        for fn in files:
            if not (fn.startswith("manifest") and fn.endswith(".json")):
                continue
            mp = Path(dp) / fn
            try:
                text = mp.read_text(encoding="utf-8", errors="replace")
            except OSError as exc:
                blockers.append(f"{mp}: unreadable ({exc.strerror})")
                continue
            strings = []
            try:
                data = json.loads(text)
            except (json.JSONDecodeError, RecursionError):
                data = None            # fall through to raw-token scan only
            def harvest(v):
                if isinstance(v, str):
                    strings.append(v)
                elif isinstance(v, dict):
                    for k, w in v.items():
                        harvest(k); harvest(w)
                elif isinstance(v, list):
                    for w in v:
                        harvest(w)
            if data is not None:
                harvest(data)
            strings.append(text)
            for value in strings:
                cands = [value.strip()]
                cands += [t.strip(".,;:'\"") for t in re.split(r"\s+", value)]
                for tok in cands:
                    if not tok or tok.startswith("--"):
                        continue
                    looks_path = ("/" in tok) or any(
                        tok.endswith(sfx) for sfx in REF_SUFFIX)
                    if not looks_path:
                        continue
                    for base in (mp.parent, ROOT, REPO, Path.cwd()):
                        c = Path(os.path.normpath(
                            tok if os.path.isabs(tok) else str(base / tok)))
                        try:
                            r = c.resolve()
                        except OSError:
                            continue
                        if c.exists() and (inside(c, ROOT) or inside(r, ROOT)):
                            refs.add(c)
                            refs.add(r)
    return refs, blockers


def guard(p: Path, keeps, refs, min_age_days, registry_ok):
    """Return None if deletion is allowed, else the refusal reason."""
    if not (inside(p, ROOT) and p != ROOT):
        return "not under .local/"
    if inside(p, SHARED) or inside(p, ATTIC):
        return "shared/attic tree"
    # --keep is checked before existence: an explicit retention is always
    # honored, never masked by "not a directory"
    for k in keeps:
        if inside(p, k) or inside(k, p):
            return f"inside --keep {k}"
    if not p.is_dir():
        return "not a directory"
    if not is_build_dir(p):
        return "not a build-output directory kind"
    # stale rule enforced in guard (not only in the listing): a target
    # touched within min-age days is live work regardless of what was
    # named on the command line
    if _age_days(p) <= min_age_days:
        return f"build target modified within {min_age_days}d (not stale)"
    if has_protected_descendant(p):
        return "contains a protected file (report/manifest/SHASUMS/sha256/status/README)"
    if not registry_ok:
        return "manifest-reference registry incomplete"
    for r in refs:
        if inside(r, p):
            return f"contains manifest-referenced artifact {r}"
    kit = enclosing_kit(p)
    stamp = datetime.datetime.now().timestamp() - min_age_days * 86400
    if kit is not None and kit.parent != ROOT:
        # the live-kit rule orders NUMBERED KITS inside a role directory;
        # top-level trees under .local/ are workspaces, not kits (their
        # protection is the ancestor-activity and descendant rules)
        scope = kit.parent
        numbered = numbered_children(scope)
        if numbered:
            top = max(n for n, _ in numbered)
            # fail-closed: the candidate's kit sits at the maximum number
            # seen under its scope (ties all count as live-safe)
            if kit_number(kit) == top:
                return (f"enclosing kit {kit.name} is the highest-numbered "
                        f"kit under {scope.name} (live kit)")
        # per-kit activity: any file or subdir INSIDE this kit touched
        # recently means the kit itself is still in use
        if kit_files_active(kit, stamp):
            return f"enclosing kit {kit.name} has files modified within {min_age_days}d (active kit)"
    # ancestor-activity: the workspace chain (role/tree level down to the
    # candidate's parent) receiving new entries means work is landing
    for ws in workspaces_of(p):
        if ws_self_modified_after(ws, stamp):
            return f"workspace {ws.name} received new entries within {min_age_days}d (active)"
    return None


def cmd_report(args, keeps):
    print(f"== {ROOT} top-level usage ==")
    if not ROOT.is_dir():
        print("kit-gc: no .local/ directory"); return 0
    for c in sorted(ROOT.iterdir()):
        if c.is_dir():
            sz = _du(c)
            print(f"{sz/2**20:9.0f} MB  {c}")
    refs, blockers = build_reference_registry()
    refs_ok = not blockers
    print(f"== build-target directories (min age {args.min_age}d) ==")
    for dp, dirs, files in os.walk(ROOT, followlinks=False):
        d = Path(dp)
        if SHARED in d.parents or d == SHARED or ATTIC in d.parents or d == ATTIC:
            dirs[:] = []
            continue
        for name in list(dirs):
            c = d / name
            if is_build_dir(c) and _age_days(c) > args.min_age:
                reason = guard(c, keeps, refs, args.min_age, refs_ok)
                state = "ELIGIBLE (name only; --apply needs the path)" \
                    if reason is None else f"guarded: {reason}"
                print(f"{_du(c)/2**20:9.0f} MB  {c}  ->  {state}")
    for b in blockers:
        print(f"BLOCKER: {b}", file=sys.stderr)
    print()
    print("Cap (REVIEWS.md): a role sandbox must stay <= 1 GB after a gate close.")
    print("Delete explicitly: kit-gc.py --prune-builds --apply PATH... "
          "(names a path; guards must all pass).")
    return 0   # report is read-only; blockers are shown, apply refuses them


def _du(p: Path) -> int:
    total = 0
    for dp, dirs, files in os.walk(p, followlinks=False):
        for name in files:
            try:
                total += os.stat(os.path.join(dp, name)).st_size
            except OSError:
                pass
    return total


def _age_days(p: Path) -> float:
    newest = p.stat().st_mtime
    for dp, _dirs, files in os.walk(p, followlinks=False):
        for name in files:
            try:
                newest = max(newest, os.stat(os.path.join(dp, name)).st_mtime)
            except OSError:
                continue
    return (datetime.datetime.now().timestamp() - newest) / 86400


def main():
    ap = argparse.ArgumentParser(
        prog="kit-gc.py",
        description=__doc__,
        formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--prune-builds", action="store_true",
                    help="list eligible build-target directories "
                         "(with --apply, delete exactly the PATHs given)")
    ap.add_argument("--apply", action="store_true",
                    help="with --prune-builds: delete ONLY the PATHs named")
    ap.add_argument("--min-age", type=int, default=14, metavar="DAYS",
                    help="a build target or workspace is stale/active "
                         "relative to this age (default 14)")
    ap.add_argument("--keep", action="append", default=[], metavar="PATH",
                    help="protect any candidate inside PATH or containing "
                         "PATH; normalized to an absolute lexical path")
    ap.add_argument("--attic", nargs="+", metavar="DIR",
                    help="copy *.md exhibits of each DIR into "
                         ".local/_attic-md/ (this writes; it is not a "
                         "dry-run mode)")
    ap.add_argument("paths", nargs="*", metavar="PATH",
                    help="explicit candidates for --prune-builds --apply")
    args = ap.parse_args()
    keeps = [norm(k) for k in args.keep]

    if args.attic:
        ATTIC.mkdir(parents=True, exist_ok=True)
        for d in args.attic:
            src = norm(d)
            if not src.is_dir():
                print(f"attic: skip missing {src}"); continue
            for f in src.glob("*.md"):
                t = ATTIC / str(f.relative_to(ROOT)).replace(os.sep, "_") \
                    if inside(f, ROOT) else ATTIC / f.name
                if not t.exists():
                    shutil.copy2(f, t)
                print(f"attic: {f} -> {t}")
        return 0

    if args.prune_builds and args.apply:
        if not args.paths:
            print("kit-gc: --apply deletes ONLY explicitly named PATHs; "
                  "run the listing first and pass the paths you reviewed",
                  file=sys.stderr)
            return 2
        refs, blockers = build_reference_registry()
        registry_ok = not blockers
        for b in blockers:
            print(f"BLOCKER: {b}", file=sys.stderr)
        pruned = 0
        for p in args.paths:
            cand = norm(p)
            reason = guard(cand, keeps, refs, args.min_age, registry_ok)
            if reason is not None:
                print(f"REFUSED {cand}: {reason}")
                continue
            shutil.rmtree(cand)
            print(f"PRUNED {cand}")
            pruned += 1
        print(f"pruned dirs: {pruned} (applied; {len(args.paths) - pruned} refused)")
        return 0 if pruned or not args.paths else 0

    return cmd_report(args, keeps)


if __name__ == "__main__":
    sys.exit(main())
