#!/bin/bash
# Test handling of large input files

for x in {0..127}; do
  for y in {128..255}; do
    for z in 10 30 50; do
      echo "10.$z.$x.$y"
    done
  done
done | ../../iprange -
