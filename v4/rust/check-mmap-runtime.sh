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

[[ $(uname -s) == Linux ]] || fail 'the runtime mmap syscall gate requires Linux strace'
command -v strace >/dev/null || fail 'strace is required for the runtime mmap syscall gate'

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
workspace="$script_dir"
probe_root=$(run mktemp -d "${TMPDIR:-/tmp}/iprange-v4-mmap-runtime.XXXXXX")
storage="$probe_root/storage"
trace="$probe_root/trace.log"

cleanup() {
    if [[ -d "$probe_root" ]]; then
        run rm -rf -- "$probe_root"
    fi
}
trap cleanup EXIT

run cargo build \
    --manifest-path "$workspace/Cargo.toml" \
    -p iprange-livedb \
    --bin iprange-v4-worker \
    --quiet

syscalls='mmap,msync,read,readv,pread64,preadv,preadv2,write,writev,pwrite64,pwritev,pwritev2,copy_file_range,sendfile'
run env IPRANGE_V4_MMAP_PROBE_DIR="$storage" \
    strace -f -qq -yy -o "$trace" -e "trace=$syscalls" \
    cargo test \
    --manifest-path "$workspace/Cargo.toml" \
    -p iprange-livedb \
    --lib \
    mmap_runtime_tests::persistent_storage_uses_mappings_only \
    --quiet -- --ignored --exact

forbidden=$(rg --fixed-strings "$storage/" "$trace" |
    rg '^[0-9]+ (read|readv|pread64|preadv|preadv2|write|writev|pwrite64|pwritev|pwritev2|copy_file_range|sendfile)\(' || true)
if [[ -n "$forbidden" ]]; then
    printf >&2 '%s\n' "$forbidden"
    fail 'persistent SDK artifacts used content-transfer syscalls'
fi

require_mapping() {
    local fragment=$1
    if ! rg '^[0-9]+ mmap\(' "$trace" | rg --fixed-strings --quiet "$fragment"; then
        fail "runtime probe did not map expected artifact: $fragment"
    fi
}

run require_mapping "$storage/live.v4"
run require_mapping "$storage/live.v4.readers"
run require_mapping "$storage/snapshot.v4"
run require_mapping "$storage/recovered.v4"
run require_mapping "$storage/.iprange-publish-"
run require_mapping "$storage/.iprange-reservation-"
run require_mapping "$storage/scratch/.iprange-scratch-"

printf '%b%s%b\n' "$GREEN" \
    'Rust v4 runtime mmap gate passes: no persistent-content transfer syscalls observed.' "$NC"
