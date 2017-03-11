#include "iprange.h"

size_t prefix_counters[33];

/* ----------------------------------------------------------------------------
 * ipset_reduce()
 *
 * takes an ipset, an acceptable increase % and a minimum accepted entries
 * and disables entries in the global prefix_enabled[] array, so that once
 * the ipset is printed, only the enabled prefixes will be used
 *
 * prefix_enable[] is not reset before use, so that it can be initialized with
 * some of the prefixes enabled and others disabled already (user driven)
 *
 * this function does not alter the given ipset and it does not print it
 */

void ipset_reduce(ipset *ips, size_t acceptable_increase, size_t min_accepted) {
    size_t i, n = ips->entries, total = 0, acceptable, iterations = 0, initial = 0, eliminated = 0;

    if(unlikely(!(ips->flags & IPSET_FLAG_OPTIMIZED)))
        ipset_optimize(ips);

    /* reset the prefix counters */
    for(i = 0; i <= 32; i++)
        prefix_counters[i] = 0;

    /* find how many prefixes are there */
    if(unlikely(debug)) fprintf(stderr, "\nCounting prefixes in %s\n", ips->filename);
    for(i = 0; i < n ;i++)
        split_range(0, 0, ips->netaddrs[i].addr, ips->netaddrs[i].broadcast, prefix_update_counters);

    /* count them */
    if(unlikely(debug)) fprintf(stderr, "Break down by prefix:\n");
    total = 0;
    for(i = 0; i <= 32 ;i++) {
        if(prefix_counters[i]) {
            if(unlikely(debug)) fprintf(stderr, "	- prefix /%zu counts %zu entries\n", i, prefix_counters[i]);
            total += prefix_counters[i];
            initial++;
        }
        else prefix_enabled[i] = 0;
    }
    if(unlikely(debug)) fprintf(stderr, "Total %zu entries generated\n", total);

    /* find the upper limit */
    acceptable = total * acceptable_increase / 100;
    if(acceptable < min_accepted) acceptable = min_accepted;
    if(unlikely(debug)) fprintf(stderr, "Acceptable is to reach %zu entries by reducing prefixes\n", acceptable);

    /* reduce the possible prefixes */
    while(total < acceptable) {
        ssize_t min = -1, to = -1, j;
        size_t min_increase = acceptable * 10, multiplier, increase, old_to_counters;

        iterations++;

        /* find the prefix with the least increase */
        for(i = 0; i <= 31 ;i++) {
            if(!prefix_counters[i] || !prefix_enabled[i]) continue;

            for(j = i + 1, multiplier = 2; j <= 32 ; j++, multiplier *= 2) {
                if(!prefix_counters[j]) continue;

                increase = prefix_counters[i] * (multiplier - 1);
                if(unlikely(debug)) fprintf(stderr, "		> Examining merging prefix %zu to %zu (increase by %zu)\n", i, j, increase);

                if(increase < min_increase) {
                    min_increase = increase;
                    min = i;
                    to = j;
                }
                break;
            }
        }

        if(min == -1 || to == -1 || min == to) {
            if(unlikely(debug)) fprintf(stderr, "	Nothing more to reduce\n");
            break;
        }

        multiplier = 1;
        ssize_t x;
        for(x = min; x < to; x++) multiplier *= 2;

        increase = prefix_counters[min] * multiplier - prefix_counters[min];
        if(unlikely(debug)) fprintf(stderr, "		> Selected prefix %zd (%zu entries) to be merged in %zd (total increase by %zu)\n", min, prefix_counters[min], to, increase);

        if(total + increase > acceptable) {
            if(unlikely(debug)) fprintf(stderr, "	Cannot proceed to increase total %zu by %zu, above acceptable %zu.\n", total, increase, acceptable);
            break;
        }

        old_to_counters = prefix_counters[to];

        total += increase;
        prefix_counters[to] += increase + prefix_counters[min];
        prefix_counters[min] = 0;
        prefix_enabled[min] = 0;
        eliminated++;
        if(unlikely(debug)) fprintf(stderr, "		Eliminating prefix %zd in %zd (had %zu, now has %zu entries), total is now %zu (increased by %zu)\n", min, to, old_to_counters, prefix_counters[to], total, increase);
    }

    if(unlikely(debug)) fprintf(stderr, "\nEliminated %zu out of %zu prefixes (%zu remain in the final set).\n\n", eliminated, initial, initial - eliminated);

    /* reset the prefix counters */
    for(i = 0; i <= 32; i++)
        prefix_counters[i] = 0;
}
