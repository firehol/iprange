#!/bin/bash

# Input file does not exist - -C should fail with exit code 1

../../iprange non_existent_file -C 2>/dev/null || echo "FAILED AS EXPECTED"
