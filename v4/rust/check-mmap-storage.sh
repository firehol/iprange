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
    printf >&2 '%q ' "$@"
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
    printf >&2 ' %q' "$@"
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

mapfile -t sources < <(
    git -C "$repo_root" ls-files --cached --others --exclude-standard -- \
        'v4/rust/iprange-livedb/src/*.rs' \
        'v4/rust/iprange-capi/src/*.rs' |
        while IFS= read -r source; do
            case "$source" in
                *_test.rs|*_tests.rs|*/tests/*) continue ;;
            esac
            [[ -f "$repo_root/$source" ]] && printf '%s\n' "$repo_root/$source"
        done |
        sort -u
)

((${#sources[@]} > 0)) || fail 'no production Rust sources found'

scan() {
    local label=$1
    local pattern=$2
    local found=0
    local source relative cutoff matches

    for source in "${sources[@]}"; do
        relative=${source#"$repo_root/"}
        cutoff=$(rg -n --no-heading '^#\[cfg\(test\)\]$' "$source" |
            head -n 1 |
            cut -d: -f1 || true)
        [[ -n "$cutoff" ]] || cutoff=2147483647

        matches=$(rg -n --no-heading --color never -e "$pattern" "$source" |
            awk -F: -v cutoff="$cutoff" '$1 < cutoff' || true)
        if [[ -n "$matches" ]]; then
            if ((found == 0)); then
                printf >&2 '%b%s%b\n' "$RED" "$label" "$NC"
            fi
            while IFS= read -r match; do
                printf >&2 '  %s:%s\n' "$relative" "$match"
            done <<<"$matches"
            found=1
        fi
    done

    ((found == 0)) || return 1
}

content_io='\b(read_at|write_at|seek_read|seek_write|read_exact_at|write_exact_at|ReadFile|WriteFile|copy_file_range|sendfile|pread|pwrite|preadv|pwritev|readv|writev|BufReader|BufWriter)\b|std::os::(unix|windows)::fs::FileExt|std::io::(Read|Write|Seek)\b'
owned_page='\[u8;[[:space:]]*PAGE_SIZE\]|\[[^]]*;[[:space:]]*PAGE_SIZE\]|vec!\[[^]]*;[[:space:]]*PAGE_SIZE\]|Vec<[[:space:]]*\[[^]]*PAGE_SIZE|Box<[[:space:]]*\[[^]]*PAGE_SIZE'

status=0
run scan 'Forbidden persistent-content I/O symbols:' "$content_io" || status=1
run scan 'Forbidden complete page images or page-array APIs:' "$owned_page" || status=1

if [[ -e "$repo_root/v4/rust/iprange-livedb/src/file_io.rs" ]]; then
    printf >&2 '%b%s%b\n' "$RED" \
        'Forbidden positional-I/O module still exists: v4/rust/iprange-livedb/src/file_io.rs' "$NC"
    status=1
fi

if [[ -e "$repo_root/v4/rust/iprange-livedb/src/draft_store/page_cache.rs" ]]; then
    printf >&2 '%b%s%b\n' "$RED" \
        'Forbidden application page cache still exists: v4/rust/iprange-livedb/src/draft_store/page_cache.rs' "$NC"
    status=1
fi

((status == 0)) || fail 'Rust v4 is not mmap-only'

printf '%b%s%b %d production source files checked.\n' \
    "$GREEN" 'Rust v4 mmap-only source gate passes:' "$NC" "${#sources[@]}"
