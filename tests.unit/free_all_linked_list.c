#include "iprange.h"

char *PROG = "unit-free-all";
int debug;
int cidr_use_network = 1;
int default_prefix = 32;
int active_family = 0;
unsigned long ipv6_dropped_in_ipv4_mode = 0;

int main(void) {
    ipset *a = ipset_create("a", 0);
    ipset *b = ipset_create("b", 0);
    ipset *c = ipset_create("c", 0);

    if(!a || !b || !c) {
        fprintf(stderr, "cannot allocate linked ipsets\n");
        return 1;
    }

    a->next = b;
    b->prev = a;
    b->next = c;
    c->prev = b;

    ipset_free_all(b);
    return 0;
}
