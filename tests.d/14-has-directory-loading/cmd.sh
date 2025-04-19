#!/bin/bash
# Test the --has-directory-loading flag
../../iprange --has-directory-loading 2>/dev/null
if [ $? -eq 0 ]; then
    echo "iprange --has-directory-loading OK"
else
    echo "iprange --has-directory-loading FAILED"
fi

# Also test the --has-filelist-loading flag
../../iprange --has-filelist-loading 2>/dev/null
if [ $? -eq 0 ]; then
    echo "iprange --has-filelist-loading OK"
else
    echo "iprange --has-filelist-loading FAILED"
fi
