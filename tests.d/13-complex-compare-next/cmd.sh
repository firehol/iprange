#!/bin/bash
# Test all input methods with --compare-next
# First set: stdin(input1), input2, @filelist1(input3,input4), @dir1(input5,input6)
# Second set: input7, @filelist2(input8,input9), @dir2(input10,input11)

# Add header to the output
../../iprange --header - input2 @filelist1 @dir1 --compare-next input7 @filelist2 @dir2 < input1