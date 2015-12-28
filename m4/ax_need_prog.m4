#
# SYNOPSIS
#
#   AX_NEED_PROG([VARIABLE],[program],[OPTIONS-IF-FOUND],[PATH])
#
# DESCRIPTION
#
#   Checks for an installed program binary, placing the PATH and
#   OPTIONS-IF-FOUND in the precious variable VARIABLE if so.
#   Uses AC_PATH_PROG, adding a test for success and bailing out if not.
#
# LICENSE
#
#   Copyright (c) 2015 Phil Whineray <phil@sanewall.org>
#
#   Copying and distribution of this file, with or without modification, are
#   permitted in any medium without royalty provided the copyright notice
#   and this notice are preserved. This file is offered as-is, without any
#   warranty.

AC_DEFUN([AX_NEED_PROG],[
    pushdef([VARIABLE],$1)
    pushdef([EXECUTABLE],$2)
    pushdef([OPTIONS_IF_FOUND],$3)
    pushdef([PATH_PROG],$4)

    AS_IF([test "x$VARIABLE" = "x"],[
        AC_PATH_PROG([]VARIABLE[], []EXECUTABLE[], [], []PATH_PROG[])

        AS_IF([test "x$VARIABLE" = "x"],[
          AC_MSG_ERROR([cannot find required executable, bailing out])
        ],[
          AS_IF([test x"OPTIONS_IF_FOUND" = "x"],[],
                [VARIABLE="$VARIABLE OPTIONS_IF_FOUND"])
          ])
    ])

    popdef([PATH_PROG])
    popdef([OPTIONS_IF_FOUND])
    popdef([EXECUTABLE])
    popdef([VARIABLE])
])
