#include "iprange_v4.h"

#include <stdint.h>
#include <stdio.h>
#include <string.h>

#define CHECK(expression)                                                       \
    do {                                                                        \
        if (!(expression)) {                                                    \
            fprintf(stderr, "check failed at %s:%d: %s\n", __FILE__, __LINE__, \
                    #expression);                                               \
            return 1;                                                           \
        }                                                                       \
    } while (0)

static int utf16_path(const char *input, uint16_t *units, uint64_t capacity,
                      iprange_v4_abi1_path *output)
{
    uint64_t length = strlen(input);
    uint64_t index;
    if (length == 0 || length > capacity) {
        return 1;
    }
    for (index = 0; index < length; ++index) {
        unsigned char byte = (unsigned char)input[index];
        if (byte > 0x7f) {
            return 1;
        }
        units[index] = byte;
    }
    memset(output, 0, sizeof(*output));
    output->kind = IPRANGE_V4_ABI1_PATH_WINDOWS_UTF16;
    output->pointer = units;
    output->length = length;
    return 0;
}

int main(int argc, char **argv)
{
    static const uint8_t tag_bytes[] = {'a', 's', 'n'};
    static const uint16_t database_name[] = {'\\', 'n', 'a', 't', 'i', 'v', 'e',
                                             '-', 0x03b4, '.', 'i', 'p', 'r'};
    uint16_t path_units[1024];
    iprange_v4_abi1_path path;
    iprange_v4_abi1_byte_slice tag = {tag_bytes, sizeof(tag_bytes)};
    iprange_v4_abi1_cancellation cancellation = {0};
    iprange_v4_abi1_report *report = NULL;
    iprange_v4_abi1_reader *reader = NULL;
    iprange_v4_abi1_error *error = NULL;
    iprange_v4_abi1_ip address = {0};
    uint8_t present = 0xff;
    uint32_t value = UINT32_MAX;

    CHECK(argc == 2);
    CHECK(utf16_path(argv[1], path_units,
                     sizeof(path_units) / sizeof(path_units[0]), &path) == 0);
    CHECK(path.length + sizeof(database_name) / sizeof(database_name[0]) <=
          sizeof(path_units) / sizeof(path_units[0]));
    memcpy(path_units + path.length, database_name, sizeof(database_name));
    path.length += sizeof(database_name) / sizeof(database_name[0]);
    CHECK(iprange_v4_abi1_version() == IPRANGE_V4_ABI1_ABI_VERSION);
    CHECK(iprange_v4_abi1_create_live(
              path, IPRANGE_V4_ABI1_ADDRESS_FAMILY_IPV4,
              IPRANGE_V4_ABI1_VALUE_KIND_DIRECT,
              IPRANGE_V4_ABI1_STRUCTURE_KIND_NONE, tag, 1, cancellation, &report,
              &error) == IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(report != NULL && error == NULL);
    CHECK(iprange_v4_abi1_report_destroy(report, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(error == NULL);

    CHECK(iprange_v4_abi1_open_live_reader(path, cancellation, &reader, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(reader != NULL && error == NULL);
    address.family = IPRANGE_V4_ABI1_ADDRESS_FAMILY_IPV4;
    CHECK(iprange_v4_abi1_reader_lookup_direct(reader, address, &present, &value,
                                               &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(error == NULL && present == 0 && value == 0);
    CHECK(iprange_v4_abi1_reader_close(reader, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(error == NULL);
    CHECK(iprange_v4_abi1_reader_destroy(reader, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(error == NULL);
    return 0;
}
