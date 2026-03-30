#!/bin/bash

echo "10.0.0.0/30" | ../../iprange --print-single-ips --print-prefix-ips "P-" --print-suffix-ips "-S" -
