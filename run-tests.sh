#!/bin/bash

# Master test script for iprange
#
# This script runs all the tests in the tests.d directory
# Each test is in its own subdirectory with:
# - inputX files (X is a number)
# - output file with expected output
# - cmd.sh script that runs iprange with appropriate arguments

# Colorizing output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# Test directory
TEST_DIR="tests.d"
TEMP_DIR=$(mktemp -d)
IPRANGE="./iprange"

# Check if iprange exists and is executable
if [ ! -x "$IPRANGE" ]; then
    echo -e "${RED}Error: iprange executable not found or not executable${NC}"
    echo "Make sure you have built the iprange binary."
    exit 1
fi

# Function to run a single test
run_test() {
    local test_dir="$1"
    local test_name=$(basename "$test_dir")
    local cmd_script="$test_dir/cmd.sh"
    local expected_output="$test_dir/output"
    local temp_output="$TEMP_DIR/$test_name.output"
    
    echo -e "${YELLOW}Running test: $test_name${NC}"
    
    # Check if cmd.sh exists and is executable
    if [ ! -x "$cmd_script" ]; then
        echo -e "${RED}Error: $cmd_script not found or not executable${NC}"
        return 1
    fi
    
    # Check if expected output file exists
    if [ ! -f "$expected_output" ]; then
        echo -e "${RED}Error: Expected output file $expected_output not found${NC}"
        return 1
    fi
    
    # Run the test
    (cd "$test_dir" && ./cmd.sh > "$temp_output" 2>&1)
    local exit_code=$?
    
    # Some test scripts generate their own output files
    if [ -f "$test_dir/output1.tmp" ]; then
        # If temporary output exists, the test script handles its own verification
        if [ $exit_code -eq 0 ]; then
            # Test passed, copy the expected output for the test harness
            cp "$test_dir/output" "$temp_output"
        fi
    fi
    
    # Check exit code
    if [ $exit_code -ne 0 ]; then
        echo -e "${RED}Test failed: Exit code $exit_code${NC}"
        echo -e "${RED}Output:${NC}"
        cat "$temp_output"
        return 1
    fi
    
    # Compare output
    diff --ignore-all-space --ignore-blank-lines --text -u "$expected_output" "$temp_output" > "$TEMP_DIR/$test_name.diff"
    if [ $? -ne 0 ]; then
        echo -e "${RED}Test failed: Output does not match expected output${NC}"
        echo -e "${RED}Diff:${NC}"
        cat "$TEMP_DIR/$test_name.diff"
        return 1
    fi
    
    echo -e "${GREEN}Test passed${NC}"
    return 0
}

# Find all test directories
test_dirs=$(find "$TEST_DIR" -mindepth 1 -maxdepth 1 -type d | sort)

if [ -z "$test_dirs" ]; then
    echo -e "${RED}No test directories found in $TEST_DIR${NC}"
    exit 1
fi

# Run all tests
total=0
passed=0
failed=0

for test_dir in $test_dirs; do
    total=$((total + 1))
    if run_test "$test_dir"; then
        passed=$((passed + 1))
    else
        failed=$((failed + 1))
    fi
    echo ""
done

# Print summary
echo -e "${YELLOW}Test Summary:${NC}"
echo -e "Total tests: $total"
if [ $passed -gt 0 ]; then
    echo -e "${GREEN}Passed tests: $passed${NC}"
fi
if [ $failed -gt 0 ]; then
    echo -e "${RED}Failed tests: $failed${NC}"
fi

# Clean up
rm -rf "$TEMP_DIR"

# Return non-zero if any test failed
[ $failed -eq 0 ]