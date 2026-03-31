#include "iprange.h"

char *PROG = "unit-optimize-empty";
int debug;
int cidr_use_network = 1;
int default_prefix = 32;

int main(void) {
    ipset *ips = ipset_create("empty", 0);

    if(!ips) {
        fprintf(stderr, "cannot allocate empty ipset\n");
        return 1;
    }

    ipset_optimize(ips);
    ipset_free(ips);
    return 0;
}
