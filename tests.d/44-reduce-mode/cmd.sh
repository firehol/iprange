#!/bin/bash

cat <<EOF | ../../iprange --ipset-reduce 0 -
10.0.0.0/25
10.0.0.128/25
EOF
