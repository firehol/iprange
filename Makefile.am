#
# Copyright (C) 2015 Alon Bar-Lev <alon.barlev@gmail.com>
#
AUTOMAKE_OPTIONS=foreign dist-xz dist-bzip2 1.10
ACLOCAL_AMFLAGS = -I m4

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
dist_noinst_DATA = iprange.spec

if ENABLE_MAN
man_MANS = iprange.1
endif

iprange_SOURCES = \
	iprange.c \
	iprange.h \
	ipset.c \
	ipset.h \
	ipset_binary.c \
	ipset_binary.h \
	ipset_combine.c \
	ipset_combine.h \
	ipset_common.c \
	ipset_common.h \
	ipset_copy.c \
	ipset_copy.h \
	ipset_diff.c \
	ipset_diff.h \
	ipset_exclude.c \
	ipset_exclude.h \
	ipset_load.c \
	ipset_load.h \
	ipset_merge.c \
	ipset_merge.h \
	ipset_optimize.c \
	ipset_optimize.h \
	ipset_print.c \
	ipset_print.h \
	ipset_reduce.c \
	ipset_reduce.h \
	$(NULL)

VPATH_LOCAL_OBJECTS = \
	iprange.$(OBJEXT) \
	ipset.$(OBJEXT) \
	ipset_binary.$(OBJEXT) \
	ipset_combine.$(OBJEXT) \
	ipset_common.$(OBJEXT) \
	ipset_copy.$(OBJEXT) \
	ipset_diff.$(OBJEXT) \
	ipset_exclude.$(OBJEXT) \
	ipset_load.$(OBJEXT) \
	ipset_merge.$(OBJEXT) \
	ipset_optimize.$(OBJEXT) \
	ipset_print.$(OBJEXT) \
	ipset_reduce.$(OBJEXT)

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
	iprange-9999.ebuild \
	autogen.sh \
	run-tests.sh \
	run-build-tests.sh \
	run-unit-tests.sh \
	run-sanitizer-tests.sh \
	tests.d \
	tests.build.d \
	tests.sanitizers.d \
	tests.unit \
	$(man_MANS) \
	$(NULL)

check-local:
	IPRANGE_BIN='$(abs_builddir)/iprange' $(SHELL) $(top_srcdir)/run-tests.sh
	$(SHELL) $(top_srcdir)/run-build-tests.sh

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
