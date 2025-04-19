#!/bin/bash
# Test handling of non-existent files and directories
# The program should gracefully handle these cases

echo "=== Test 1: Non-existent file ==="
if ../../iprange nonexistent_file input1 2>error.log; then
    echo "FAILED: Should have shown error for non-existent file"
else
    echo "PASSED: Correctly handled non-existent file"
fi

echo "=== Test 2: Non-existent directory ==="
if ../../iprange @nonexistent_dir input1 2>error2.log; then
    echo "FAILED: Should have shown error for non-existent directory"
else
    echo "PASSED: Correctly handled non-existent directory"
fi

echo "=== Test 3: Valid file works ==="
../../iprange input1

# Cleanup
rm -f error*.log