#!/bin/bash
# Compare a baseline IPv6 set against the rest -> exercises ipset6_common
../../iprange -6 input1 as baseline input2 as subset --compare-first --header
