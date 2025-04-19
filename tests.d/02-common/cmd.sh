#!/bin/bash
# Test finding common IPs between sets
cat <<EOF | ../../iprange --common input1 input2 -
250.250.250.250
10.0.0.2/31
192.168.1.0/28
EOF
