#include "abi_test_support.h"

/* The creator-only opt-out contract (the ABI spec: zero is
   unprotected, any other value requests the proof). Two creates of
   the same direct live database -- creator_only 0 and 2 -- both must
   succeed; the artifact modes are asserted by the Rust wrapper that
   runs this fixture (0666&~umask vs the 0600 floor). This is the
   only C-boundary detector for the opt-out: a regression replacing
   `creator_only != 0` with `creator_only == 1` keeps zero
   unprotected either way but flips the value-2 case to unprotected —
   the mode assertion on this fixture's second artifact catches it. */

static int create_with(const char *path, uint8_t creator_only,
                       iprange_v4_abi1_report **created)
{
    static const uint8_t tag[] = "asn";
    iprange_v4_abi1_byte_slice value_tag = {tag, sizeof(tag) - 1};
    iprange_v4_abi1_error *error = NULL;
    iprange_v4_abi1_create_report facts = {0};
    CHECK(iprange_v4_abi1_create_live(
              path_from(path),
              IPRANGE_V4_ABI1_ADDRESS_FAMILY_IPV4,
              IPRANGE_V4_ABI1_VALUE_KIND_DIRECT,
              IPRANGE_V4_ABI1_STRUCTURE_KIND_NONE,
              value_tag,
              4,
              creator_only,
              no_cancellation(),
              created,
              &error) == IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(error == NULL);
    CHECK(iprange_v4_abi1_report_get_create(
              *created, &facts, &error) == IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(error == NULL);
    return 0;
}

int main(int argc, char **argv)
{
    iprange_v4_abi1_report *opt_out = NULL;
    iprange_v4_abi1_report *opt_in = NULL;

    CHECK(argc == 3);
    CHECK(create_with(argv[1], 0, &opt_out) == 0);
    CHECK(destroy_report(opt_out) == 0);
    CHECK(create_with(argv[2], 2, &opt_in) == 0);
    CHECK(destroy_report(opt_in) == 0);
    return 0;
}
