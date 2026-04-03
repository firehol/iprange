#ifndef IPRANGE_IPSET6_PRINT_H
#define IPRANGE_IPSET6_PRINT_H

#include "ipset6.h"

extern uint8_t prefix6_enabled[];

extern void ipset6_print(ipset6 *ips, IPSET_PRINT_CMD print);

extern void prefix6_update_counters(ipv6_addr_t addr, int prefix);
extern void print_addr6(ipv6_addr_t addr, int prefix);
extern void print_addr6_range(ipv6_addr_t lo, ipv6_addr_t hi);
extern void print_addr6_single(ipv6_addr_t x);

extern int split_range6(ipv6_addr_t addr, int prefix, ipv6_addr_t lo, ipv6_addr_t hi, void (*print)(ipv6_addr_t, int));

#endif /* IPRANGE_IPSET6_PRINT_H */
