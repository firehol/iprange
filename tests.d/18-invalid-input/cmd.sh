#!/bin/bash
# Test handling of invalid input
# iprange should ignore invalid IPs and continue with valid ones

cat <<EOF | ../../iprange - input1 2>/dev/null
12345678901234567890
1.2.3.4.5.6.7.8.9.10.11.12.13.14.15.16.17.18.19.20
1.2.3.123456
1.23456.7.8
EOF
