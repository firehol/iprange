#ifndef IPRANGE_IPSET6_BINARY_H
#define IPRANGE_IPSET6_BINARY_H

#include "ipset6.h"

#define BINARY_HEADER_V20 "iprange binary format v2.0\n"

extern int ipset6_load_binary_v20(FILE *fp, ipset6 *ips, int first_line_missing);
extern void ipset6_save_binary_v20(ipset6 *ips);

#endif /* IPRANGE_IPSET6_BINARY_H */
