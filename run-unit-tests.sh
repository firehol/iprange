#!/bin/bash

set -u

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
BUILD_DIR=${BUILD_DIR:-$(pwd)}
UNIT_TESTS_DIR=${UNIT_TESTS_DIR:-tests.unit}
WORK_DIR=$(mktemp -d)
CC_BIN=${CC:-clang}
TEST_CFLAGS=${TEST_CFLAGS:-"-g -O1 -fno-omit-frame-pointer -fsanitize=address,undefined"}
TEST_LDFLAGS=${TEST_LDFLAGS:-"-fsanitize=address,undefined"}

read -r -a EXTRA_CFLAGS <<< "$TEST_CFLAGS"
read -r -a EXTRA_LDFLAGS <<< "$TEST_LDFLAGS"

PROJECT_SOURCES=(
    "$ROOT_DIR/src/ipset.c"
    "$ROOT_DIR/src/ipset_binary.c"
    "$ROOT_DIR/src/ipset_combine.c"
    "$ROOT_DIR/src/ipset_common.c"
    "$ROOT_DIR/src/ipset_copy.c"
    "$ROOT_DIR/src/ipset_diff.c"
    "$ROOT_DIR/src/ipset_dns.c"
    "$ROOT_DIR/src/ipset_exclude.c"
    "$ROOT_DIR/src/ipset_load.c"
    "$ROOT_DIR/src/ipset_merge.c"
    "$ROOT_DIR/src/ipset_optimize.c"
    "$ROOT_DIR/src/ipset_print.c"
    "$ROOT_DIR/src/ipset_reduce.c"
)

cleanup() {
    rm -rf "$WORK_DIR"
}

trap cleanup EXIT

if [ ! -f "$BUILD_DIR/config.h" ] && [ -f "$ROOT_DIR/config.h" ]; then
    BUILD_DIR="$ROOT_DIR"
fi

if [ ! -f "$BUILD_DIR/config.h" ]; then
    echo -e "${RED}Error: config.h not found in $BUILD_DIR or $ROOT_DIR${NC}"
    exit 1
fi

if [ ! -d "$ROOT_DIR/$UNIT_TESTS_DIR" ]; then
    echo -e "${RED}Error: unit test directory $ROOT_DIR/$UNIT_TESTS_DIR not found${NC}"
    exit 1
fi

run_unit_test() {
    local src="$1"
    local name
    local bin
    local rc

    name=$(basename "$src" .c)
    bin="$WORK_DIR/$name"

    echo -e "${YELLOW}Running unit test: $name${NC}"

    if ! "$CC_BIN" \
        -DHAVE_CONFIG_H \
        -I"$BUILD_DIR" \
        -I"$ROOT_DIR" \
        -I"$ROOT_DIR/src" \
        -pthread \
        "${EXTRA_CFLAGS[@]}" \
        "$src" \
        "${PROJECT_SOURCES[@]}" \
        "${EXTRA_LDFLAGS[@]}" \
        -o "$bin"; then
        echo -e "${RED}Unit test build failed${NC}"
        return 1
    fi

    ASAN_OPTIONS=${ASAN_OPTIONS:-detect_leaks=1:abort_on_error=1} \
    UBSAN_OPTIONS=${UBSAN_OPTIONS:-print_stacktrace=1:halt_on_error=1} \
    "$bin"
    rc=$?

    if [ $rc -ne 0 ]; then
        echo -e "${RED}Unit test failed: Exit code $rc${NC}"
        return 1
    fi

    echo -e "${GREEN}Unit test passed${NC}"
}

total=0
passed=0
failed=0

for src in "$ROOT_DIR"/"$UNIT_TESTS_DIR"/*.c; do
    [ -f "$src" ] || continue
    total=$((total + 1))
    if run_unit_test "$src"; then
        passed=$((passed + 1))
    else
        failed=$((failed + 1))
    fi
    echo ""
done

echo -e "${YELLOW}Unit Test Summary:${NC}"
echo -e "Total tests: $total"
if [ $passed -gt 0 ]; then
    echo -e "${GREEN}Passed tests: $passed${NC}"
fi
if [ $failed -gt 0 ]; then
    echo -e "${RED}Failed tests: $failed${NC}"
fi

[ $failed -eq 0 ]
