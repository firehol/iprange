#include "iprange_v4.h"

#include <stdint.h>
#include <stdio.h>
#include <string.h>

#include <shellapi.h>
#include <windows.h>

#define CHECK(expression)                                                       \
    do {                                                                        \
        if (!(expression)) {                                                    \
            fprintf(stderr, "check failed at %s:%d: %s\n", __FILE__, __LINE__, \
                    #expression);                                               \
            return 1;                                                           \
        }                                                                       \
    } while (0)

/* Native wide arguments (sol turn-3 wave MAH): the narrow argv the
 * CRT hands main() is code-page dependent and the old converter
 * refused every non-ASCII byte — a work directory containing U+00E9
 * failed this test before the product ever ran. The OS wide command
 * line is consumed directly; no encoding conversion happens at all. */
static int wide_path(const wchar_t *input, uint16_t *units, uint64_t capacity,
                     iprange_v4_abi1_path *output)
{
    uint64_t index = 0;
    if (input == NULL || input[0] == L'\0') {
        return 1;
    }
    while (input[index] != L'\0') {
        if (index >= capacity) {
            return 1;
        }
        units[index] = (uint16_t)input[index];
        ++index;
    }
    memset(output, 0, sizeof(*output));
    output->kind = IPRANGE_V4_ABI1_PATH_WINDOWS_UTF16;
    output->pointer = units;
    output->length = index;
    return 0;
}

int main(int argc, char **argv)
{
    int wargc = 0;
    wchar_t **wargv = CommandLineToArgvW(GetCommandLineW(), &wargc);
    (void)argc;
    (void)argv;
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

    CHECK(wargv != NULL && wargc == 2);
    {
        iprange_v4_abi1_path empty = {0};
        iprange_v4_abi1_reader *opened = NULL;
        uint16_t unit = 'd';
        uint32_t code = 0;
        uint8_t caller_present = 0;
        uint64_t caller_code = 0;
        empty.kind = IPRANGE_V4_ABI1_PATH_WINDOWS_UTF16;
        empty.pointer = NULL;
        empty.length = 1;
        CHECK(iprange_v4_abi1_open_live_reader(empty, cancellation, &opened, &error) ==
              IPRANGE_V4_ABI1_STATUS_ERROR);
        CHECK(opened == NULL);
        CHECK(iprange_v4_abi1_error_code(error, &code, &caller_present, &caller_code) ==
              IPRANGE_V4_ABI1_STATUS_OK);
        CHECK(code == IPRANGE_V4_ABI1_ERROR_CODE_NULL_POINTER);
        CHECK(iprange_v4_abi1_error_destroy(error) == IPRANGE_V4_ABI1_STATUS_OK);
        error = NULL;
        empty.pointer = &unit;
        empty.length = 0;
        CHECK(iprange_v4_abi1_open_live_reader(empty, cancellation, &opened, &error) ==
              IPRANGE_V4_ABI1_STATUS_ERROR);
        CHECK(iprange_v4_abi1_error_code(error, &code, &caller_present, &caller_code) ==
              IPRANGE_V4_ABI1_STATUS_OK);
        CHECK(code == IPRANGE_V4_ABI1_ERROR_CODE_INVALID_LENGTH);
        CHECK(iprange_v4_abi1_error_destroy(error) == IPRANGE_V4_ABI1_STATUS_OK);
        error = NULL;
    }
    CHECK(wide_path(wargv[1], path_units,
                    sizeof(path_units) / sizeof(path_units[0]), &path) == 0);
    LocalFree(wargv);
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

    {
        static const uint8_t metadata[] = "{\"k\":1}";
        uint8_t tiny[1] = {0};
        uint8_t changed = 0;
        uint64_t required = 0;
        uint32_t code = 0;
        uint8_t caller_present = 0;
        uint64_t caller_code = 0;
        iprange_v4_abi1_mutable_byte_slice output = {tiny, sizeof(tiny)};
        iprange_v4_abi1_transaction_budget budget = {0};
        iprange_v4_abi1_writer *meta_writer = NULL;
        budget.abi_version = IPRANGE_V4_ABI1_ABI_VERSION;
        budget.struct_size = sizeof(budget);
        budget.max_heap_bytes = 2 * 1024 * 1024;
        budget.max_private_pages = 20000;
        budget.max_file_growth_pages = 20000;
        budget.max_open_files = 2;
        CHECK(iprange_v4_abi1_open_live_writer(path, &budget, cancellation,
                                               &meta_writer, &error) ==
              IPRANGE_V4_ABI1_STATUS_OK);
        CHECK(error == NULL);
        CHECK(iprange_v4_abi1_writer_set_metadata_json(
                  meta_writer, metadata, sizeof(metadata) - 1, cancellation,
                  &changed, &error) == IPRANGE_V4_ABI1_STATUS_OK);
        CHECK(error == NULL);
        CHECK(iprange_v4_abi1_writer_metadata_read(meta_writer, output, &required,
                                                   &error) ==
              IPRANGE_V4_ABI1_STATUS_ERROR);
        CHECK(iprange_v4_abi1_error_code(error, &code, &caller_present,
                                         &caller_code) ==
              IPRANGE_V4_ABI1_STATUS_OK);
        CHECK(code == IPRANGE_V4_ABI1_ERROR_CODE_BUFFER_TOO_SMALL);
        CHECK(required > sizeof(tiny));
        CHECK(iprange_v4_abi1_error_destroy(error) ==
              IPRANGE_V4_ABI1_STATUS_OK);
        error = NULL;
        CHECK(iprange_v4_abi1_writer_close(meta_writer, &report, &error) ==
              IPRANGE_V4_ABI1_STATUS_OK);
        CHECK(iprange_v4_abi1_writer_destroy(meta_writer, &error) ==
              IPRANGE_V4_ABI1_STATUS_OK);
        CHECK(report != NULL);
        CHECK(iprange_v4_abi1_report_destroy(report, &error) ==
              IPRANGE_V4_ABI1_STATUS_OK);
        report = NULL;
        CHECK(error == NULL);
    }

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
