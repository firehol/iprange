#!/bin/bash

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
TEMP_DIR=$(mktemp -d)
TEST_DIRS=${TEST_DIRS:-tests.d}
IPRANGE_LINK="$ROOT_DIR/iprange"
IPRANGE_BIN=${IPRANGE_BIN:-$IPRANGE_LINK}

ORIGINAL_IPRANGE_STATE="missing"
ORIGINAL_IPRANGE_TARGET=""
IPRANGE_LINK_CHANGED=0

cleanup() {
    rm -rf "$TEMP_DIR"

    if [ "$IPRANGE_BIN" != "$IPRANGE_LINK" ] && [ "$IPRANGE_LINK_CHANGED" -eq 1 ]; then
        case "$ORIGINAL_IPRANGE_STATE" in
            symlink)
                ln -sfn "$ORIGINAL_IPRANGE_TARGET" "$IPRANGE_LINK"
                ;;
            missing)
                rm -f "$IPRANGE_LINK"
                ;;
        esac
    fi
}

trap cleanup EXIT

prepare_iprange_link() {
    if [ "$IPRANGE_BIN" = "$IPRANGE_LINK" ]; then
        if [ ! -x "$IPRANGE_LINK" ]; then
            echo -e "${RED}Error: iprange executable not found or not executable at $IPRANGE_LINK${NC}"
            exit 1
        fi
        return
    fi

    if [ ! -x "$IPRANGE_BIN" ]; then
        echo -e "${RED}Error: requested iprange binary not found or not executable at $IPRANGE_BIN${NC}"
        exit 1
    fi

    if [ -L "$IPRANGE_LINK" ]; then
        ORIGINAL_IPRANGE_STATE="symlink"
        ORIGINAL_IPRANGE_TARGET=$(readlink "$IPRANGE_LINK")
    elif [ -e "$IPRANGE_LINK" ]; then
        echo -e "${RED}Error: cannot replace existing non-symlink $IPRANGE_LINK${NC}"
        exit 1
    fi

    ln -sfn "$IPRANGE_BIN" "$IPRANGE_LINK"
    IPRANGE_LINK_CHANGED=1
}

run_test() {
    local test_dir="$1"
    local test_root="$2"
    local test_name
    local cmd_script
    local expected_output
    local temp_output
    local temp_diff

    test_name="${test_root}-$(basename "$test_dir")"
    cmd_script="$test_dir/cmd.sh"
    expected_output="$test_dir/output"
    temp_output="$TEMP_DIR/$test_name.output"
    temp_diff="$TEMP_DIR/$test_name.diff"

    echo -e "${YELLOW}Running test: ${test_name}${NC}"

    if [ ! -x "$cmd_script" ]; then
        echo -e "${RED}Error: $cmd_script not found or not executable${NC}"
        return 1
    fi

    if [ ! -f "$expected_output" ]; then
        echo -e "${RED}Error: Expected output file $expected_output not found${NC}"
        return 1
    fi

    (cd "$test_dir" && ./cmd.sh > "$temp_output" 2>&1)
    local exit_code=$?

    if [ $exit_code -ne 0 ]; then
        echo -e "${RED}Test failed: Exit code $exit_code${NC}"
        echo -e "${RED}Output:${NC}"
        cat "$temp_output"
        return 1
    fi

    diff --ignore-all-space --ignore-blank-lines --text -u "$expected_output" "$temp_output" > "$temp_diff"
    if [ $? -ne 0 ]; then
        echo -e "${RED}Test failed: Output does not match expected output${NC}"
        echo -e "${RED}Diff:${NC}"
        cat "$temp_diff"
        return 1
    fi

    echo -e "${GREEN}Test passed${NC}"
    return 0
}

prepare_iprange_link

total=0
passed=0
failed=0

for test_root in $TEST_DIRS; do
    test_root="$ROOT_DIR/$test_root"

    if [ ! -d "$test_root" ]; then
        echo -e "${RED}No test directory found at $test_root${NC}"
        exit 1
    fi

    test_dirs=$(find "$test_root" -mindepth 1 -maxdepth 1 -type d | sort)

    if [ -z "$test_dirs" ]; then
        echo -e "${RED}No test cases found in $test_root${NC}"
        exit 1
    fi

    for test_dir in $test_dirs; do
        total=$((total + 1))
        if run_test "$test_dir" "$(basename "$test_root")"; then
            passed=$((passed + 1))
        else
            failed=$((failed + 1))
        fi
        echo ""
    done
done

echo -e "${YELLOW}Test Summary:${NC}"
echo -e "Total tests: $total"
if [ $passed -gt 0 ]; then
    echo -e "${GREEN}Passed tests: $passed${NC}"
fi
if [ $failed -gt 0 ]; then
    echo -e "${RED}Failed tests: $failed${NC}"
fi

[ $failed -eq 0 ]
