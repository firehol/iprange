#!/bin/bash
# Test printing with prefix and suffix
echo "250.250.250.250" | ../../iprange --print-prefix "add " --print-suffix " nomatch" input1 -