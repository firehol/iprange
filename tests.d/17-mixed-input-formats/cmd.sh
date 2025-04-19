#!/bin/bash
# Test handling of mixed input formats in the same file

echo "250.250.250.250 - 250.250.250.251" | ../../iprange - input1
