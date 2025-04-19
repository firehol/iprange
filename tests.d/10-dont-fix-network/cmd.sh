#!/bin/bash
# Test the --dont-fix-network option
echo "250.250.250.250" | ../../iprange --dont-fix-network - input1
