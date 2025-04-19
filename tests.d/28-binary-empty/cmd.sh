#!/bin/bash

# Create binary file from empty input
echo >empty
echo "250.250.250.250" | ../../iprange empty --print-binary | ../../iprange
