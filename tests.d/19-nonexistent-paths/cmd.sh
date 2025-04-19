#!/bin/bash
# Test handling of non-existent files and directories
# The program should gracefully handle these cases

../../iprange nonexistent_file input1 2>/dev/null || echo "FAILED AS EXPECTED 1"
../../iprange input1 @nonexistent_file 2>/dev/null || echo "FAILED AS EXPECTED 2"
