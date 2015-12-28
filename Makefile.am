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

iprange_SOURCES = iprange.c

EXTRA_DIST = \
	.gitignore \
	README.md \
	iprange-9999.ebuild \
	autogen.sh \
	$(man_MANS) \
	$(NULL)

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
