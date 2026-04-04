#include "iprange.h"

char *PROG = "unit-empty-ipset";
int debug;
int cidr_use_network = 1;
int default_prefix = 32;
int active_family = 0;
unsigned long ipv6_dropped_in_ipv4_mode = 0;

static ipset *make_single_ipset(const char *name, in_addr_t ip) {
    ipset *ips = ipset_create(name, 1);

    if(!ips) {
        fprintf(stderr, "cannot allocate ipset %s\n", name);
        return NULL;
    }

    ipset_add_ip_range(ips, ip, ip);
    ips->flags |= IPSET_FLAG_OPTIMIZED;
    return ips;
}

static int expect_range(const char *label, ipset *ips, size_t entries, size_t unique_ips, in_addr_t from, in_addr_t to) {
    if(!ips) {
        fprintf(stderr, "%s: result is NULL\n", label);
        return 1;
    }

    if(ips->entries != entries) {
        fprintf(stderr, "%s: expected %zu entries, got %zu\n", label, entries, ips->entries);
        return 1;
    }

    if(ips->unique_ips != unique_ips) {
        fprintf(stderr, "%s: expected %zu unique IPs, got %zu\n", label, unique_ips, ips->unique_ips);
        return 1;
    }

    if(entries == 0) return 0;

    if(ips->netaddrs[0].addr != from || ips->netaddrs[0].broadcast != to) {
        fprintf(stderr, "%s: unexpected range %u-%u\n", label, ips->netaddrs[0].addr, ips->netaddrs[0].broadcast);
        return 1;
    }

    return 0;
}

int main(void) {
    ipset empty = { 0 };
    ipset *one;
    ipset *result;

    strncpy(empty.filename, "empty", FILENAME_MAX);
    empty.filename[FILENAME_MAX] = '\0';
    empty.flags = IPSET_FLAG_OPTIMIZED;
    empty.netaddrs = NULL;

    one = make_single_ipset("one", 0x0A000001U);
    if(!one) return 1;

    result = ipset_common(&empty, one);
    if(expect_range("common(empty, one)", result, 0, 0, 0, 0)) return 1;
    ipset_free(result);

    result = ipset_common(one, &empty);
    if(expect_range("common(one, empty)", result, 0, 0, 0, 0)) return 1;
    ipset_free(result);

    result = ipset_diff(&empty, one);
    if(expect_range("diff(empty, one)", result, 1, 1, 0x0A000001U, 0x0A000001U)) return 1;
    ipset_free(result);

    result = ipset_diff(one, &empty);
    if(expect_range("diff(one, empty)", result, 1, 1, 0x0A000001U, 0x0A000001U)) return 1;
    ipset_free(result);

    result = ipset_exclude(&empty, one);
    if(expect_range("exclude(empty, one)", result, 0, 0, 0, 0)) return 1;
    ipset_free(result);

    result = ipset_exclude(one, &empty);
    if(expect_range("exclude(one, empty)", result, 1, 1, 0x0A000001U, 0x0A000001U)) return 1;
    ipset_free(result);

    ipset_free(one);
    return 0;
}
