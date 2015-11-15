#
# Copyright (C) 2015 Alon Bar-Lev <alon.barlev@gmail.com>
#
AC_PREREQ(2.60)

define([VERSION_MAJOR], [1])
define([VERSION_MINOR], [0])
define([VERSION_FIX], [0])
define([VERSION_NUMBER], VERSION_MAJOR[.]VERSION_MINOR[.]VERSION_FIX)
define([VERSION_SUFFIX], [_master])

AC_INIT([iprange], VERSION_NUMBER[]VERSION_SUFFIX)
PACKAGE_RPM_VERSION="VERSION_NUMBER"
PACKAGE_RPM_RELEASE="0.0.$(echo VERSION_SUFFIX | sed 's/^_//')"
AC_SUBST([PACKAGE_RPM_VERSION])
AC_SUBST([PACKAGE_RPM_RELEASE])

AC_CONFIG_AUX_DIR([.])
AC_CONFIG_HEADERS([config.h])
AC_CONFIG_MACRO_DIR([m4])
AC_CONFIG_SRCDIR([iprange.c])
AM_INIT_AUTOMAKE
AC_CANONICAL_HOST
AC_PROG_CC
AC_PROG_INSTALL
AC_ARG_ENABLE(
	[pedantic],
	[AS_HELP_STRING([--enable-pedantic], [enable pedantic compiler warnings])],
	,
	[enable_pedantic="no"]
)
AC_ARG_WITH(
	[compare-with-common],
	[AS_HELP_STRING([--without-compare-with-common], [if set, use MODE_COMMON to compare files this is 20 times faster than MODE COMBINE])],
	,
	[with_compare_with_common="yes"]
)
AC_ARG_WITH(
	[system-ip2str],
	[AS_HELP_STRING([--with-system-ip2str], [if set, use slower inet_ntoa])],
	,
	[with_system_ip2str="no"]
)

ACX_PTHREAD(, [AC_MSG_ERROR([Cannot initialize pthread environment])])
LIBS="${PTHREAD_LIBS} ${LIBS}"
CFLAGS="${CFLAGS} ${PTHREAD_CFLAGS}"
CC="${PTHREAD_CC}"

AC_TYPE_UINT32_T
AC_C_INLINE

test "${with_compare_with_common}" = "yes" && AC_DEFINE([COMPARE_WITH_COMMON], [1], [compare settings])
test "${with_system_ip2str}" = "yes" && AC_DEFINE([SYSTEM_IP2STR], [1], [ip2str settings])

if test "${GCC}" = "yes"; then
	AC_DEFINE_UNQUOTED([likely(x)], [__builtin_expect(!!(x), 1)], [gcc branch optimization])
	AC_DEFINE_UNQUOTED([unlikely(x)], [__builtin_expect(!!(x), 0)], [gcc branch optimization])
else
	AC_DEFINE_UNQUOTED([likely(x)], [(x)], [gcc branch optimization])
	AC_DEFINE_UNQUOTED([unlikely(x)], [(x)], [gcc branch optimization])
fi

if test "${enable_pedantic}" = "yes"; then
	enable_strict="yes"
	CFLAGS="${CFLAGS} -pedantic -Wall -Wextra"
fi
AC_CONFIG_FILES([
	Makefile
	iprange.spec
])
AC_OUTPUT
