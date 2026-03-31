# Copyright 1999-2015 Gentoo Foundation
# Distributed under the terms of the GNU General Public License v2
# $Id$

EAPI=5

inherit autotools git-2

DESCRIPTION="manage IP ranges"
HOMEPAGE="https://github.com/firehol/iprange"
EGIT_REPO_URI="https://github.com/firehol/iprange"

LICENSE="GPL-2+"
SLOT="0"
KEYWORDS=""
IUSE=""

RDEPEND=""
DEPEND="${RDEPEND}"

src_prepare() {
	eautoreconf
}
