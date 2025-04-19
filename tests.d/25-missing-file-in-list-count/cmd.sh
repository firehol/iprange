#!/bin/bash
# Input file list exists, but a file in the list does not - -C should fail with exit code 1

echo "non_existent_file" > filelist
../../iprange @filelist -C 2>/dev/null || echo "FAILED AS EXPECTED"
