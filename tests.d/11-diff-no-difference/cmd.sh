#!/bin/bash
# Test diff mode with identical files (should return exit code 0)
# This tests the case where there are no differences

# Run the diff command
echo "250.250.250.250" | ../../iprange - input1 --diff input2
DIFF_EXIT=$?

# Check that the exit code is 0 (no differences found)
if [ $DIFF_EXIT -ne 0 ]; then
    echo "# ERROR: Expected exit code 0 (no differences), got $DIFF_EXIT"
    exit 1
fi

# If we get here, test passed
echo "# OK: No differences found (exit code 0, no output)"
exit 0