#ifndef IPRANGE_IPSET_PRINT_H
#define IPRANGE_IPSET_PRINT_H

typedef enum ipset_print_cmd {
    PRINT_RANGE = 1,
    PRINT_CIDR = 2,
    PRINT_SINGLE_IPS = 3,
    PRINT_BINARY = 4
} IPSET_PRINT_CMD;

extern uint8_t prefix_enabled[];

extern char *print_prefix_ips;
extern char *print_prefix_nets;
extern char *print_suffix_ips;
extern char *print_suffix_nets;

extern void ipset_print(ipset *ips, IPSET_PRINT_CMD print);

extern void prefix_update_counters(in_addr_t addr, int prefix);
extern void print_addr(in_addr_t addr, int prefix);
extern void print_addr_range(in_addr_t lo, in_addr_t hi);
extern void print_addr_single(in_addr_t x);

extern int split_range(in_addr_t addr, int prefix, in_addr_t lo, in_addr_t hi, void (*print)(in_addr_t, int));

#endif //IPRANGE_IPSET_PRINT_H
