iprange - manage IP ranges
==========================

Getting help
------------

~~~~
iprange --help 2>&1 | more
~~~~

Installation from tar-file
--------------------------

~~~~
./configure && make && make install
~~~~


Installation from git
---------------------

~~~~
./autogen.sh
./configure && make && make install
~~~~

When working with git, copy the hooks to the cloned folder:

~~~~
cp hooks/* .git/hooks
~~~~
