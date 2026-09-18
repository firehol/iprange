#!/usr/bin/env bash
# kit-gc.sh — review-kit disk hygiene for .local/ (REVIEWS.md § Kit hygiene).
#
# WHY: reviewer sandboxes historically copied whole repo trees with their
# cargo/go/C build targets (~20-26 GB each); eighteen such trees reached
# 370 GB under .local/.  This script is the recurring close-out duty: run it
# at every milestone-gate close, before the closure battery.
#
# This committed copy (.agents/tools/kit-gc.sh) is the authoritative
# implementation and the mandated entry point (astra gate turns 3-4: a
# mandated destructive procedure must be reproducible from a checkout, not
# live only in the gitignored .local/).  A .local/kit-gc.sh forwarding
# wrapper may exist on a workstation for convenience; it is not required
# and nothing may depend on it.
#
# Usage:
#   kit-gc.sh                       # report: size per .local/<role>/<dir>
#   kit-gc.sh --all                 # report + every build-target candidate,
#                                   # ignoring the name-pattern skip (the
#                                   # live/active-kit and protected-file
#                                   # guarantees below still hold)
#   kit-gc.sh --prune-builds [--all] [--min-age DAYS]        # dry-run list
#   kit-gc.sh --prune-builds --apply [--all] [--min-age DAYS]
#   kit-gc.sh --attic DIR...        # copy *.md exhibits into .local/_attic-md/
#
# Safety model (each clause is enforced below, not promised):
#   * never touches .local/shared/ or .local/_attic-md/;
#   * never prunes anything under the role's LIVE kit — the numerically
#     highest-numbered numbered kit (r<N>…, round<N>…, w<N>…; identity is
#     the leading number after the prefix, so r20-kit outranks r9-kit)
#     containing the candidate, resolved by the kit that ENCLOSES it
#     (first path component under the role), so deep targets like
#     .local/tester/r20-kit/v4/target are covered;
#   * never prunes anything under a kit ACTIVE within --min-age days (any
#     file inside it modified recently).  This is also the separate
#     protection rule for UNNUMBERED workspaces (work/, dr/, build*/,
#     probe*/): they are prunable only when stale;
#   * never prunes a directory that CONTAINS a protected file anywhere
#     beneath it (report*.md, manifest*.json, SHASUMS*, *.sha256,
#     status.md, README.md);
#   * never prunes a directory containing a file REFERENCED by any
#     manifest*.json under .local/ (REVIEWS.md: "anything manifest-
#     referenced is never pruned").  References are extracted from
#     manifest text and resolved against the manifest's own directory,
#     .local/, and the repo root; only paths that exist under .local/ are
#     registered;
#   * --keep P protects any candidate that is inside P or contains P.  P
#     is normalized to an absolute path (relative --keep works from any
#     invocation cwd) and compared at directory boundaries;
#   * --prune-builds deletes ONLY recognizable build-output directories
#     (target/, ctarget/, gocache/, cargotgt*/, rust-target*/,
#     llvm-cov-target/), never reports or probes;
#   * without --apply everything is a dry run and prints what it would do.
set -euo pipefail

TOOL_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$TOOL_DIR/../.." && pwd)"
if [ "$(basename "$TOOL_DIR")" = ".local" ]; then
  ROOT="$TOOL_DIR"                       # legacy .local/kit-gc.sh invocation
else
  ROOT="$REPO_ROOT/.local"               # committed .agents/tools/ location
fi
[ -d "$ROOT" ] || { echo "kit-gc: no $ROOT directory" >&2; exit 2; }
SHARED="$ROOT/shared"
ATTIC="$ROOT/_attic-md"

# inside A B -> true if A equals B or lies strictly under B (directory
# boundary: no prefix collisions like /local/tester2 vs /local/tester).
inside() { [[ "$1" == "$2" || "$1" == "$2"/* ]]; }

# abs P -> absolute path (relative resolves against cwd); trailing slash cut.
abs() { case "$1" in /*) printf '%s\n' "${1%/}" ;; *) printf '%s\n' "${PWD%/}/${1#./}" ;; esac; }

all=0; apply=0; mode="report"; min_age=14; keeps=()
while [ $# -gt 0 ]; do
  case "$1" in
    --all) all=1 ;;
    --apply) apply=1 ;;
    --prune-builds) mode="prune" ;;
    --attic) mode="attic"; shift; for d in "$@"; do keeps+=("$(abs "$d")"); done; break ;;
    --min-age) shift; min_age="$1" ;;  # build-target age threshold in days
    --keep) shift; keeps+=("$(abs "$1")") ;;
    *) echo "unknown option: $1" >&2; exit 2 ;;
  esac
  shift || true
done

# role_of PATH -> first path component under ROOT ("shared", "tester", ...).
role_of() { local rel="${1#"$ROOT"/}"; echo "${rel%%/*}"; }

# unit_of PATH -> the immediate child of ROOT enclosing the candidate: the
# role's kit directory (.local/<role>/<kit>/...) or, for lead-managed
# top-level trees (.local/<tree>/...), the tree itself.
unit_of() {
  local rel="${1#"$ROOT"/}" rest
  case "$rel" in */*) rest="${rel#*/}"; echo "${rest%%/*}" ;; *) echo "" ;; esac
}

is_keep() { # candidate must not be inside --keep, nor contain it
  local p="$1" k
  for k in "${keeps[@]:-}"; do
    [ -n "$k" ] || continue
    { inside "$p" "$k" || inside "$k" "$p"; } && return 0
  done
  return 1
}

kit_number() { # leading digits of a kit dir name; empty if unnumbered
  local n; n="$(basename "$1")"
  case "$n" in
    r[0-9]*)      n="${n#r}" ;;
    round[0-9]*)  n="${n#round}" ;;
    w[0-9]*)      n="${n#w}" ;;
    *) echo ""; return ;;
  esac
  printf '%s\n' "${n%%[!0-9]*}"
}

live_kit_of_role() { # numerically highest NUMBERED kit under .local/<role>/
  local role="$1" d num best="" bestnum=-1
  for d in "$ROOT/$role"/r[0-9]* "$ROOT/$role"/round[0-9]* \
           "$ROOT/$role"/w[0-9]*; do
    [ -d "$d" ] || continue
    num="$(kit_number "$d")"
    [ -n "$num" ] || continue
    if [ "$num" -gt "$bestnum" ] 2>/dev/null; then bestnum=$num; best="$d"; fi
  done
  printf '%s\n' "$best"
}

kit_is_active() { # any file inside the kit modified in the last $min_age days
  [ -n "${1:-}" ] && [ -d "$1" ] || return 1
  find "$1" -mindepth 1 -mtime -"$min_age" -print -quit 2>/dev/null |
    grep -q .
}

# build_reference_registry: collect every existing .local path that some
# manifest*.json under .local/ mentions (REVIEWS.md manifest rule).
REF_REFERENCED=()
build_reference_registry() {
  command -v python3 >/dev/null 2>&1 || return 0
  local line
  while IFS= read -r line; do
    [ -n "$line" ] && REF_REFERENCED+=("$line")
  done < <(python3 - "$ROOT" "$REPO_ROOT" <<'PYEOF'
import os, re, sys
root = os.path.realpath(sys.argv[1])
repo = os.path.realpath(sys.argv[2])
pat = re.compile(r"[\w./+@-]+\.(?:log|txt|json|md|tsv|csv|sha256|html|xml|py|sh)\b")
skip_dirs = {"target", "ctarget", "gocache", "_attic-md"}
found = set()
for dp, dirs, files in os.walk(root):
    dirs[:] = [d for d in dirs if d not in skip_dirs and not d.startswith("cargotgt")
               and not d.startswith("rust-target") and d != "llvm-cov-target"]
    for fn in files:
        if not (fn.startswith("manifest") and fn.endswith(".json")):
            continue
        p = os.path.join(dp, fn)
        try:
            with open(p, encoding="utf-8", errors="replace") as fh:
                text = fh.read()
        except OSError:
            continue
        for tok in pat.findall(text):
            for base in (os.path.dirname(p), root, repo):
                cand = tok if os.path.isabs(tok) else os.path.join(base, tok)
                try:
                    cand = os.path.realpath(cand)
                except OSError:
                    continue
                if cand.startswith(root + os.sep) and os.path.exists(cand):
                    found.add(cand)
                    break
print("\n".join(sorted(found)))
PYEOF
  )
}

references_under() { # true if a registered reference lies at/under $1
  local p="$1" r
  for r in "${REF_REFERENCED[@]:-}"; do
    [ -n "$r" ] && inside "$r" "$p" && return 0
  done
  return 1
}

has_protected_descendant() { # true if any protected file exists under $1
  local f
  while IFS= read -r -d '' f; do
    case "$(basename "$f")" in
      report*.md|manifest*.json|SHASUMS*|*.sha256|status.md|README.md)
        return 0 ;;
    esac
  done < <(find "$1" -type f -print0 2>/dev/null)
  return 1
}

protected() { # true if candidate path must never be pruned
  local p="$1" role kit kit_dir
  case "$p" in
    "$SHARED"|"$SHARED"/*|"$ATTIC"|"$ATTIC"/*) return 0 ;;
  esac
  is_keep "$p" && return 0
  case "$(basename "$p")" in kit-gc.sh) return 0 ;; esac
  role="$(role_of "$p")"
  kit="$(unit_of "$p")"
  kit_dir="$ROOT/$role/$kit"
  # live-kit rule (numbered kits only)
  if [ -n "$kit" ] && [ "$(live_kit_of_role "$role")" = "$kit_dir" ]; then
    return 0
  fi
  # active-kit rule (also the unnumbered-workspace protection)
  if kit_is_active "$kit_dir"; then return 0; fi
  # protected-descendant and manifest-reference rules
  has_protected_descendant "$p" && return 0
  references_under "$p" && return 0
  return 1
}

if [ "$mode" = "attic" ]; then
  mkdir -p "$ATTIC"
  for d in "${keeps[@]}"; do
    [ -d "$d" ] || { echo "attic: skip missing $d" >&2; continue; }
    find "$d" -maxdepth 2 -name '*.md' -not -path '*/target/*' 2>/dev/null |
    while read -r f; do
      t="$ATTIC/$(echo "${f#"$ROOT"/}" | tr '/' '_')"
      [ -e "$t" ] || cp -p "$f" "$t"
      echo "attic: $f -> $t"
    done
  done
  exit 0
fi

echo "== .local top-level usage =="
du -h --max-depth=1 "$ROOT" 2>/dev/null | sort -rh | head -20

if [ "$mode" = "prune" ]; then
  build_reference_registry
  echo "== build-target candidates (min age ${min_age}d) =="
  pruned=0
  while read -r t; do
    if protected "$t"; then continue; fi
    if [ "$all" = 0 ]; then
      # name-pattern belt: numbered round/kit directories are lead-review
      # material; --all widens the sweep but never widens any safety rule
      case "$t" in */r[0-9]*/*|*/round*/*|*/w[0-9]*/*) continue ;; esac
    fi
    if [ "$apply" = 1 ]; then
      rm -rf "$t"
      echo "PRUNED $t"
      pruned=$((pruned+1))
    else
      echo "would prune $t"
    fi
  done < <(find "$ROOT" -mindepth 3 -maxdepth 5 -type d \
              \( -name target -o -name ctarget -o -name gocache \
                 -o -name 'cargotgt*' -o -name 'rust-target*' \
                 -o -name llvm-cov-target \) \
              -not -path "$SHARED/*" -not -path "$ATTIC/*" \
              -mtime +"$min_age" 2>/dev/null)
  echo "pruned dirs: $pruned ($([ "$apply" = 1 ] && echo applied || echo dry-run))"
else
  echo "== role sandboxes > 1 GB =="
  find "$ROOT" -mindepth 2 -maxdepth 2 -type d -size +0 2>/dev/null |
  while read -r d; do
    s=$(du -sm "$d" 2>/dev/null | cut -f1) || continue
    if [ "${s:-0}" -ge 1024 ]; then printf "%6d MB  %s\n" "$s" "$d"; fi
  done | sort -rn
  echo
  echo "Cap (REVIEWS.md): a role sandbox must stay <= 1 GB after a gate close."
  echo "Stale full-tree builds are the usual offender: run --prune-builds."
fi
