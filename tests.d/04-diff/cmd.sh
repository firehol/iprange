#!/bin/bash
# Test symmetric difference between sets
# The diff command should return exit code 1 when differences are found

# Run the diff command
echo "250.250.250.250" | ../../iprange - input1 --diff input2
DIFF_EXIT=$?

# Check that the exit code is 1 (differences found)
if [ $DIFF_EXIT -ne 1 ]; then
    echo "# ERROR: Expected exit code 1 (differences found), got $DIFF_EXIT"
    exit 1
fi

# If we get here, test passed
echo "# OK: Differences found (exit code 1)"
exit 0
