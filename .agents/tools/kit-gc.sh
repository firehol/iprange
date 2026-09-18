#!/usr/bin/env bash
# kit-gc.sh — review-kit disk hygiene for .local/ (REVIEWS.md § Kit hygiene).
#
# WHY: reviewer sandboxes historically copied whole repo trees with their
# cargo/go/C build targets (~20-26 GB each); eighteen such trees reached
# 370 GB under .local/.  This script is the recurring close-out duty: run it
# at every milestone-gate close, before the closure battery.
#
# This committed copy (.agents/tools/kit-gc.sh) is the authoritative
# implementation (astra gate turn 3, mandatory_cleanup_does_not_enforce_
# safeguards: a mandated destructive procedure must be reproducible from a
# checkout, not live only in the gitignored .local/).  .local/kit-gc.sh is
# a forwarding wrapper so either path works.
#
# Usage:
#   kit-gc.sh                       # report: size per .local/<role>/<dir>
#   kit-gc.sh --all                 # report + every build-target candidate,
#                                   # ignoring the name-pattern skip (the
#                                   # live/active-kit and protected-file
#                                   # guarantees still hold)
#   kit-gc.sh --prune-builds [--all] [--min-age DAYS]        # dry-run list
#   kit-gc.sh --prune-builds --apply [--all] [--min-age DAYS]
#   kit-gc.sh --attic DIR...        # copy *.md exhibits into .local/_attic-md/
#
# Safety model (each clause is enforced below, not promised):
#   * never touches .local/shared/ or .local/_attic-md/;
#   * never prunes anything under the role's LIVE kit — the
#     highest-numbered r*/round*/w*/build* directory under .local/<role>/,
#     identified by the kit that ENCLOSES the candidate (the first path
#     component under the role), so deep targets like
#     .local/tester/r20-kit/v4/target are covered;
#   * never prunes anything under a kit ACTIVE within --min-age days (any
#     file inside the kit modified recently);
#   * never prunes a directory that CONTAINS a protected file anywhere
#     beneath it (report*.md, manifest*.json, SHASUMS*, *.sha256,
#     status.md, README.md);
#   * --keep P protects any candidate that is inside P or contains P;
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

all=0; apply=0; mode="report"; min_age=14; keeps=()
while [ $# -gt 0 ]; do
  case "$1" in
    --all) all=1 ;;
    --apply) apply=1 ;;
    --prune-builds) mode="prune" ;;
    --attic) mode="attic"; shift; for d in "$@"; do keeps+=("$d"); done; break ;;
    --min-age) shift; min_age="$1" ;;  # build-target age threshold in days
    --keep) shift; keeps+=("$1") ;;
    *) echo "unknown option: $1" >&2; exit 2 ;;
  esac
  shift || true
done

# role_of PATH -> first path component under ROOT ("shared", "tester", ...).
role_of() { local rel="${1#"$ROOT"/}"; echo "${rel%%/*}"; }

# unit_of PATH -> the immediate child of ROOT that encloses the candidate:
# the role's kit directory (.local/<role>/<kit>/...) or, for lead-managed
# top-level trees (.local/<tree>/...), the tree itself.
unit_of() {
  local rel="${1#"$ROOT"/}" rest
  case "$rel" in */*) rest="${rel#*/}"; echo "${rest%%/*}" ;; *) echo "" ;; esac
}

is_keep() { # candidate must not be inside --keep, nor contain it
  local p="$1" k
  for k in "${keeps[@]:-}"; do
    [ -n "$k" ] || continue
    [[ "$p" == "$k"* || "$k" == "$p"* ]] && return 0
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

live_kit_of_role() { # highest-numbered kit directory under .local/<role>/
  local role="$1"
  ls -d "$ROOT/$role"/r[0-9]* "$ROOT/$role"/round* "$ROOT/$role"/w[0-9]* \
        "$ROOT/$role"/build* "$ROOT/$role"/kit-gc* "$ROOT/$role"/probe* \
        "$ROOT/$role"/dr "$ROOT/$role"/work 2>/dev/null |
    sort -V | tail -1 || true
}

kit_is_active() { # any file inside the kit modified in the last $min_age days
  [ -n "$1" ] && [ -d "$1" ] || return 1
  find "$1" -mindepth 1 -mtime -"$min_age" -print -quit 2>/dev/null |
    grep -q .
}

protected() { # true if candidate path must never be pruned
  local p="$1" role kit_dir
  case "$p" in
    "$SHARED"|"$SHARED"/*|"$ATTIC"|"$ATTIC"/*) return 0 ;;
  esac
  is_keep "$p" && return 0
  case "$(basename "$p")" in kit-gc.sh) return 0 ;; esac
  role="$(role_of "$p")"
  kit="$(unit_of "$p")"
  kit_dir="$ROOT/$role/$kit"
  # live-kit rule: the enclosing kit/unit is the role's highest-numbered kit
  if [ "$kit" != "" ] && [ "$(live_kit_of_role "$role")" = "$kit_dir" ]; then
    return 0
  fi
  # active-kit rule: anything touched within min-age days is still live work
  if kit_is_active "$kit_dir"; then return 0; fi
  # protected-descendant rule: refuse to take reports/manifests with it
  has_protected_descendant "$p" && return 0
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
  echo "== build-target candidates (min age ${min_age}d) =="
  pruned=0
  while read -r t; do
    if protected "$t"; then continue; fi
    if [ "$all" = 0 ] && [[ "$t" =~ /(r|round|w)[0-9] ]]; then
      # name-pattern belt: round/kit directories are lead-review material;
      # --all widens the sweep but never widens any safety rule above
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
    [ "${s:-0}" -ge 1024 ] && printf "%6d MB  %s\n" "$s" "$d"
  done | sort -rn
  echo
  echo "Cap (REVIEWS.md): a role sandbox must stay <= 1 GB after a gate close."
  echo "Stale full-tree builds are the usual offender: run --prune-builds."
fi
