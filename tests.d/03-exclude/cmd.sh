#!/bin/bash
# Test excluding IPs from a set
echo "250.250.250.250" | ../../iprange - input1 --except input2
