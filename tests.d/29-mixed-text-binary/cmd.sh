#!/bin/bash

# Mixing text and binary files: have input1 as text and pipe the output of another iprange that creates a binary version of input2

cat >input1 <<INPUT1
192.168.1.1
192.168.1.2
INPUT1

cat >input2 <<INPUT2
10.0.0.1
10.0.0.2
INPUT2

# Create a binary version of input2 and pipe it to iprange along with input1
echo "250.250.250.250" | ../../iprange input1 --print-binary | ../../iprange input2 -
