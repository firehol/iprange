#!/bin/bash

cat <<EOF | ../../iprange --print-prefix-ips "IP:" --print-suffix-ips ":I" --print-prefix-nets "NET:" --print-suffix-nets ":N" -
10.0.0.1
10.0.0.8/30
EOF
