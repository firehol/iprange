#!/usr/bin/env bash

set -euo pipefail

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
GRAY='\033[0;90m'
NC='\033[0m'

run() {
    printf >&2 "${GRAY}%s >${NC} " "$(pwd)"
    printf >&2 '%b' "$YELLOW"
    printf >&2 "%q " "$@"
    printf >&2 '%b\n' "$NC"

    local exit_code=0
    "$@" || exit_code=$?
    if ((exit_code == 0)); then
        return 0
    fi

    printf >&2 '%b%s%b\n' "$RED" \
        '━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━' "$NC"
    printf >&2 "${RED}[ERROR]${NC} Command failed with exit code %d: ${YELLOW}%s${NC}\n" \
        "$exit_code" "$1"
    printf >&2 '%b%s%b' "$RED" '        Full command:' "$NC"
    printf >&2 " %q" "$@"
    printf >&2 "\n${RED}        Working dir:${NC} %s\n" "$(pwd)"
    printf >&2 '%b%s%b\n' "$RED" \
        '━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━' "$NC"
    return "$exit_code"
}

fail() {
    printf >&2 "${RED}[ERROR]${NC} %s\n" "$1"
    exit 1
}

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
repo_root=$(run git -C "$script_dir" rev-parse --show-toplevel)
workspace="$repo_root/v4/rust"
targets=(
    x86_64-unknown-linux-gnu
    x86_64-pc-windows-gnu
    aarch64-apple-darwin
    x86_64-unknown-freebsd
)

# This source is compiled deliberately by the native-behavior test at runtime,
# not by Cargo. Keep the exception exact and prove its compiler call remains.
runtime_source="iprange-capi/tests/native/panic_shim.rs"
runtime_driver="$workspace/iprange-capi/tests/native_behavior.rs"
runtime_reference='tests/native/panic_shim.rs'

target_dir=$(run mktemp -d "${TMPDIR:-/tmp}/iprange-v4-source-graph.XXXXXX")
expected="$target_dir/expected-sources"
compiled="$target_dir/compiled-sources"
classified="$target_dir/classified-sources"
missing="$target_dir/missing-sources"
unexpected="$target_dir/unexpected-sources"

cleanup() {
    if [[ -d "$target_dir" ]]; then
        run rm -rf -- "$target_dir"
    fi
}
trap cleanup EXIT

collect_expected() {
    git -C "$repo_root" ls-files --cached --others --exclude-standard -- v4/rust |
        while IFS= read -r path; do
            case "$path" in
                *.rs)
                    if [[ ! -f "$repo_root/$path" ]]; then
                        continue
                    fi
                    relative=${path#v4/rust/}
                    if [[ "$relative" =~ [[:space:]] ]]; then
                        fail "Rust source paths containing whitespace are unsupported: $relative"
                    fi
                    printf '%s\n' "$relative"
                    ;;
            esac
        done |
        sort -u >"$expected"
}

canonical_source() {
    local path=$1
    local directory=${path%/*}
    local basename=${path##*/}
    if [[ "$directory" == "$path" ]]; then
        directory=.
    fi
    [[ -d "$directory" ]] || return 1
    (cd -- "$directory" && printf '%s/%s\n' "$(pwd -P)" "$basename")
}

collect_compiled() {
    find "$target_dir" -type f -name '*.d' -print0 |
        xargs -0 cat -- |
        tr ' ' '\n' |
        while IFS= read -r token; do
            case "$token" in
                *.rs)
                    case "$token" in
                        /*) source=$token ;;
                        v4/rust/*) source="$repo_root/$token" ;;
                        *) source="$workspace/$token" ;;
                    esac
                    absolute=$(canonical_source "$source") || continue
                    case "$absolute" in
                        "$workspace"/*)
                            printf '%s\n' "${absolute#"$workspace/"}"
                            ;;
                    esac
                    ;;
            esac
        done |
        sort -u >"$compiled"
}

require_no_dead_code_suppression() {
    local findings
    findings=$(rg -n '#\[(allow|expect)\(dead_code\)\]' "$workspace" --glob '*.rs' || true)
    if [[ -n "$findings" ]]; then
        printf >&2 '%s\n' "$findings"
        fail "dead-code suppression is forbidden; remove or explicitly wire the code"
    fi
}

run require_no_dead_code_suppression

rustflags="${RUSTFLAGS-} -D warnings"
for target in "${targets[@]}"; do
    run env \
        CARGO_INCREMENTAL=0 \
        RUSTFLAGS="$rustflags" \
        cargo check \
        --manifest-path "$workspace/Cargo.toml" \
        --workspace \
        --all-features \
        --all-targets \
        --locked \
        --quiet \
        --target "$target" \
        --target-dir "$target_dir"
done

run collect_expected
run collect_compiled

[[ -f "$workspace/$runtime_source" ]] || fail "missing runtime-compiled source: $runtime_source"
run rg --fixed-strings --quiet "$runtime_reference" "$runtime_driver"
run cp -- "$compiled" "$classified"
printf '%s\n' "$runtime_source" >>"$classified"
run sort -u -o "$classified" "$classified"

comm -23 "$expected" "$classified" >"$missing"
comm -13 "$expected" "$classified" >"$unexpected"

if [[ -s "$missing" ]]; then
    printf >&2 '%b%s%b\n' "$RED" \
        'Tracked Rust sources outside every supported compiler graph:' "$NC"
    while IFS= read -r source; do
        printf >&2 '  %s\n' "$source"
    done <"$missing"
    exit 1
fi

if [[ -s "$unexpected" ]]; then
    printf >&2 '%b%s%b\n' "$RED" \
        'Compiled Rust sources absent from the repository source inventory:' "$NC"
    while IFS= read -r source; do
        printf >&2 '  %s\n' "$source"
    done <"$unexpected"
    exit 1
fi

source_count=$(wc -l <"$expected")
printf '%b%s%b %d sources; 4 supported targets; 1 runtime-compiled native fixture.\n' \
    "$GREEN" 'Rust source graph is complete:' "$NC" "$source_count"
