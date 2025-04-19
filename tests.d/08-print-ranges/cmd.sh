#!/bin/bash
# Test printing IP ranges instead of CIDRs
echo "250.250.250.250" | ../../iprange -j - input1
