#
# Copyright (C) 2015 Alon Bar-Lev <alon.barlev@gmail.com>
#
AUTOMAKE_OPTIONS=foreign dist-xz dist-bzip2 1.10 subdir-objects
ACLOCAL_AMFLAGS = -I m4
AM_CPPFLAGS = -I$(srcdir)/src

MAINTAINERCLEANFILES= \
	config.log config.status \
	$(srcdir)/Makefile.in \
	$(srcdir)/config.h.in $(srcdir)/config.h.in~ $(srcdir)/configure \
	$(srcdir)/install-sh $(srcdir)/ltmain.sh $(srcdir)/missing \
	$(srcdir)/compile $(srcdir)/depcomp $(srcdir)/aclocal.m4 \
	$(srcdir)/config.guess $(srcdir)/config.sub \
	$(srcdir)/m4/ltsugar.m4 $(srcdir)/m4/libtool.m4 \
	$(srcdir)/m4/ltversion.m4 $(srcdir)/m4/lt~obsolete.m4 \
	$(srcdir)/m4/ltoptions.m4 \
	$(srcdir)/pkcs11-helper.spec $(srcdir)/config-w32-vc.h \
	iprange.1

AM_H2MFLAGS = \
	--no-info

iprange_DESCRIPTION = "manage IP ranges"

bin_PROGRAMS = iprange
dist_noinst_DATA = packaging/iprange.spec

if ENABLE_MAN
man_MANS = iprange.1
endif

iprange_SOURCES = \
	src/iprange.c \
	src/iprange.h \
	src/iprange6.h \
	src/iprange6_main.c \
	src/ipset.c \
	src/ipset.h \
	src/ipset6.c \
	src/ipset6.h \
	src/ipset6_binary.c \
	src/ipset6_binary.h \
	src/ipset6_combine.c \
	src/ipset6_common.c \
	src/ipset6_copy.c \
	src/ipset6_diff.c \
	src/ipset6_dns.c \
	src/ipset6_dns.h \
	src/ipset6_exclude.c \
	src/ipset6_load.c \
	src/ipset6_load.h \
	src/ipset6_merge.c \
	src/ipset6_optimize.c \
	src/ipset6_print.c \
	src/ipset6_print.h \
	src/ipset_binary.c \
	src/ipset_binary.h \
	src/ipset_combine.c \
	src/ipset_combine.h \
	src/ipset_common.c \
	src/ipset_common.h \
	src/ipset_copy.c \
	src/ipset_copy.h \
	src/ipset_diff.c \
	src/ipset_diff.h \
	src/ipset_dns.c \
	src/ipset_dns.h \
	src/ipset_exclude.c \
	src/ipset_exclude.h \
	src/ipset_load.c \
	src/ipset_load.h \
	src/ipset_merge.c \
	src/ipset_merge.h \
	src/ipset_optimize.c \
	src/ipset_optimize.h \
	src/ipset_print.c \
	src/ipset_print.h \
	src/ipset_reduce.c \
	src/ipset_reduce.h \
	$(NULL)

VPATH_LOCAL_OBJECTS = \
	src/iprange.$(OBJEXT) \
	src/iprange6_main.$(OBJEXT) \
	src/ipset.$(OBJEXT) \
	src/ipset6.$(OBJEXT) \
	src/ipset6_binary.$(OBJEXT) \
	src/ipset6_combine.$(OBJEXT) \
	src/ipset6_common.$(OBJEXT) \
	src/ipset6_copy.$(OBJEXT) \
	src/ipset6_diff.$(OBJEXT) \
	src/ipset6_dns.$(OBJEXT) \
	src/ipset6_exclude.$(OBJEXT) \
	src/ipset6_load.$(OBJEXT) \
	src/ipset6_merge.$(OBJEXT) \
	src/ipset6_optimize.$(OBJEXT) \
	src/ipset6_print.$(OBJEXT) \
	src/ipset_binary.$(OBJEXT) \
	src/ipset_combine.$(OBJEXT) \
	src/ipset_common.$(OBJEXT) \
	src/ipset_copy.$(OBJEXT) \
	src/ipset_diff.$(OBJEXT) \
	src/ipset_dns.$(OBJEXT) \
	src/ipset_exclude.$(OBJEXT) \
	src/ipset_load.$(OBJEXT) \
	src/ipset_merge.$(OBJEXT) \
	src/ipset_optimize.$(OBJEXT) \
	src/ipset_print.$(OBJEXT) \
	src/ipset_reduce.$(OBJEXT)

LOCAL_OBJECT_STAMP = local-build-objects.stamp
BUILT_SOURCES = $(LOCAL_OBJECT_STAMP)
CLEANFILES = $(LOCAL_OBJECT_STAMP)

# GNU make uses VPATH for both prerequisites and targets. If the source tree
# contains stale in-tree object files, an out-of-tree build can skip local
# compilation and fail at link time. A build-local stamp created after configure
# keeps local object targets newer than any pre-existing source-tree objects.
$(LOCAL_OBJECT_STAMP):
	@rm -f $@
	@touch $@

$(VPATH_LOCAL_OBJECTS): $(LOCAL_OBJECT_STAMP)

EXTRA_DIST = \
	.gitignore \
	README.md \
	packaging/iprange.spec.in \
	packaging/iprange-9999.ebuild \
	autogen.sh \
	run-tests.sh \
	run-build-tests.sh \
	run-unit-tests.sh \
	run-sanitizer-tests.sh \
	tests.d \
	tests.build.d \
	tests.sanitizers.d \
	tests.tsan.d \
	tests.unit \
	wiki \
	cmake \
	$(man_MANS) \
	$(NULL)

check-local:
	IPRANGE_BIN='$(abs_builddir)/iprange' $(SHELL) $(top_srcdir)/run-tests.sh
	IPRANGE_BIN='$(abs_builddir)/iprange' $(SHELL) $(top_srcdir)/run-build-tests.sh

check-sanitizers:
	$(SHELL) $(top_srcdir)/run-sanitizer-tests.sh

.PHONY: check-sanitizers

if MAINTAINER_MODE
%.1: %
	$(HELP2MAN) \
		-s 1 \
		$(AM_H2MFLAGS) \
		-I $(top_srcdir)/$*.h2m \
		-n $(if $($(subst -,_,$*)_DESCRIPTION), $($(subst -,_,$*)_DESCRIPTION), "manual page for $*") \
		-o $@ \
		$(top_builddir)/$<
endif
