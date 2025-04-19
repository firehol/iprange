#!/bin/bash

# Input file list exists, but a file is empty, -C should output 0,0 and exit code should be 0

echo >empty_file
echo "empty_file" > filelist
../../iprange @filelist -C
