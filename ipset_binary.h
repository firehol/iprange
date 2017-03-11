#ifndef IPRANGE_IPSET_LOAD_SAVE_H
#define IPRANGE_IPSET_LOAD_SAVE_H

#define BINARY_HEADER_V10 "iprange binary format v1.0\n"

extern int ipset_load_binary_v10(FILE *fp, ipset *ips, int first_line_missing);
extern void ipset_save_binary_v10(ipset *ips);

#endif //IPRANGE_IPSET_LOAD_SAVE_H
