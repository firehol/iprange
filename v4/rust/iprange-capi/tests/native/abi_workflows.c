#include "abi_test_support.h"

static int commit(iprange_v4_abi1_writer *writer)
{
    iprange_v4_abi1_error *error = NULL;
    iprange_v4_abi1_report *report = NULL;
    iprange_v4_abi1_commit_report facts = {0};
    uint32_t kind = 0;
    CHECK(iprange_v4_abi1_writer_commit(writer, &report, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(iprange_v4_abi1_report_kind(report, &kind) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(kind == IPRANGE_V4_ABI1_REPORT_KIND_COMMIT);
    CHECK(iprange_v4_abi1_report_get_commit(report, &facts, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(facts.durability == IPRANGE_V4_ABI1_COMMIT_DURABILITY_COMMITTED);
    CHECK(destroy_report(report) == 0);
    return 0;
}

static int finish(iprange_v4_abi1_writer *writer, uint32_t workflow)
{
    iprange_v4_abi1_error *error = NULL;
    iprange_v4_abi1_report *report = NULL;
    iprange_v4_abi1_finish_input_report facts = {0};
    CHECK(iprange_v4_abi1_writer_finish_input(writer, &report, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(iprange_v4_abi1_report_get_finish_input(report, &facts, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(facts.workflow == workflow);
    CHECK(destroy_report(report) == 0);
    return 0;
}

static int close_writer(iprange_v4_abi1_writer *writer)
{
    iprange_v4_abi1_error *error = NULL;
    iprange_v4_abi1_report *report = NULL;
    CHECK(iprange_v4_abi1_writer_close(writer, &report, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(destroy_report(report) == 0);
    CHECK(iprange_v4_abi1_writer_destroy(writer, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    return 0;
}

static int create_membership(const char *path)
{
    static const uint8_t tag[] = "feeds";
    iprange_v4_abi1_byte_slice value_tag = {tag, sizeof(tag) - 1};
    iprange_v4_abi1_error *error = NULL;
    iprange_v4_abi1_report *report = NULL;
    CHECK(iprange_v4_abi1_create_live(
              path_from(path),
              IPRANGE_V4_ABI1_ADDRESS_FAMILY_IPV4,
              IPRANGE_V4_ABI1_VALUE_KIND_MEMBERSHIP,
              value_tag,
              4,
              no_cancellation(),
              &report,
              &error) == IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(destroy_report(report) == 0);
    return 0;
}

static int exercise_membership_workflows(const char *source_path,
                                         const char *destination_path)
{
    iprange_v4_abi1_error *error = NULL;
    iprange_v4_abi1_writer *writer = NULL;
    iprange_v4_abi1_reader *source_reader = NULL;
    iprange_v4_abi1_reader *destination_reader = NULL;
    iprange_v4_abi1_transaction_budget budget = transaction_budget();
    iprange_v4_abi1_range first[] = {ipv4_range(1, 10)};
    iprange_v4_abi1_range replacement[] = {ipv4_range(5, 15)};
    iprange_v4_abi1_range temporary[] = {ipv4_range(100, 101)};
    coverage_source input = {0};
    uint8_t present = 0;
    iprange_v4_abi1_feed_info feed = {0};
    iprange_v4_abi1_membership_view *membership = NULL;
    uint8_t contains = 0;

    CHECK(create_membership(source_path) == 0);
    CHECK(iprange_v4_abi1_open_live_writer(
              path_from(source_path), &budget, no_cancellation(), &writer, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);

    CHECK(iprange_v4_abi1_writer_begin_create_feed(
              writer,
              (const uint8_t *)"alpha",
              5,
              no_cancellation(),
              &error) == IPRANGE_V4_ABI1_STATUS_OK);
    input = (coverage_source){first, 1, 0};
    CHECK(iprange_v4_abi1_writer_add_coverage_ranges(
              writer, coverage_callback, &input, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(finish(writer, IPRANGE_V4_ABI1_WORKFLOW_CREATE_FEED) == 0);
    CHECK(commit(writer) == 0);

    CHECK(iprange_v4_abi1_writer_begin_replace_feed(
              writer,
              (const uint8_t *)"alpha",
              5,
              no_cancellation(),
              &error) == IPRANGE_V4_ABI1_STATUS_OK);
    input = (coverage_source){replacement, 1, 0};
    CHECK(iprange_v4_abi1_writer_add_coverage_ranges(
              writer, coverage_callback, &input, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(finish(writer, IPRANGE_V4_ABI1_WORKFLOW_REPLACE_FEED) == 0);
    CHECK(commit(writer) == 0);

    CHECK(iprange_v4_abi1_writer_rename_feed(
              writer,
              (const uint8_t *)"alpha",
              5,
              (const uint8_t *)"renamed",
              7,
              no_cancellation(),
              &error) == IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(commit(writer) == 0);

    CHECK(iprange_v4_abi1_writer_begin_create_feed(
              writer,
              (const uint8_t *)"temporary",
              9,
              no_cancellation(),
              &error) == IPRANGE_V4_ABI1_STATUS_OK);
    input = (coverage_source){temporary, 1, 0};
    CHECK(iprange_v4_abi1_writer_add_coverage_ranges(
              writer, coverage_callback, &input, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(finish(writer, IPRANGE_V4_ABI1_WORKFLOW_CREATE_FEED) == 0);
    CHECK(commit(writer) == 0);
    CHECK(iprange_v4_abi1_writer_delete_feed(
              writer,
              (const uint8_t *)"temporary",
              9,
              no_cancellation(),
              &error) == IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(commit(writer) == 0);
    CHECK(close_writer(writer) == 0);
    writer = NULL;

    CHECK(create_membership(destination_path) == 0);
    CHECK(iprange_v4_abi1_open_live_reader(
              path_from(source_path),
              no_cancellation(),
              &source_reader,
              &error) == IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(iprange_v4_abi1_open_live_writer(
              path_from(destination_path),
              &budget,
              no_cancellation(),
              &writer,
              &error) == IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(iprange_v4_abi1_writer_begin_membership_import(
              writer, source_reader, no_cancellation(), &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(finish(writer, IPRANGE_V4_ABI1_WORKFLOW_MEMBERSHIP_IMPORT) == 0);
    CHECK(commit(writer) == 0);
    CHECK(close_writer(writer) == 0);
    writer = NULL;
    CHECK(iprange_v4_abi1_reader_close(source_reader, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(iprange_v4_abi1_reader_destroy(source_reader, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);

    CHECK(iprange_v4_abi1_open_live_reader(
              path_from(destination_path),
              no_cancellation(),
              &destination_reader,
              &error) == IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(iprange_v4_abi1_reader_lookup_feed(
              destination_reader,
              (const uint8_t *)"renamed",
              7,
              &present,
              &feed,
              &error) == IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(present == 1);
    CHECK(iprange_v4_abi1_reader_lookup_membership(
              destination_reader, ipv4(8), &membership, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(membership != NULL);
    CHECK(iprange_v4_abi1_membership_view_contains_index(
              membership, feed.index, &contains, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(contains == 1);
    CHECK(iprange_v4_abi1_membership_view_close(membership, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(iprange_v4_abi1_membership_view_destroy(membership, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(iprange_v4_abi1_reader_close(destination_reader, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(iprange_v4_abi1_reader_destroy(destination_reader, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    return 0;
}

static int exercise_direct_advanced(const char *path)
{
    static const uint8_t tag[] = "asn";
    static const uint8_t metadata[] = "{\"advanced\":true}";
    iprange_v4_abi1_byte_slice value_tag = {tag, sizeof(tag) - 1};
    iprange_v4_abi1_error *error = NULL;
    iprange_v4_abi1_report *report = NULL;
    iprange_v4_abi1_writer *writer = NULL;
    iprange_v4_abi1_reader *reader = NULL;
    iprange_v4_abi1_cursor *cursor = NULL;
    iprange_v4_abi1_transaction_budget budget = transaction_budget();
    iprange_v4_abi1_direct_range assigned[2] = {
        {ipv4_range(100, 200), 1, 0},
        {ipv4_range(150, 160), 2, 0},
    };
    iprange_v4_abi1_range cleared[] = {ipv4_range(155, 157)};
    direct_source direct = {assigned, 2, 0};
    coverage_source coverage = {cleared, 1, 0};
    iprange_v4_abi1_direct_range item = {0};
    iprange_v4_abi1_reclaim_report reclaim = {0};
    uint8_t item_present = 0;
    uint8_t changed = 0;
    uint8_t present = 0;
    uint64_t required = 0;

    CHECK(iprange_v4_abi1_create_live(
              path_from(path),
              IPRANGE_V4_ABI1_ADDRESS_FAMILY_IPV4,
              IPRANGE_V4_ABI1_VALUE_KIND_DIRECT,
              value_tag,
              4,
              no_cancellation(),
              &report,
              &error) == IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(destroy_report(report) == 0);
    report = NULL;
    CHECK(iprange_v4_abi1_open_live_writer(
              path_from(path), &budget, no_cancellation(), &writer, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(iprange_v4_abi1_writer_begin_direct(
              writer, no_cancellation(), &error) == IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(iprange_v4_abi1_writer_direct_assign_ranges(
              writer, direct_callback, &direct, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(iprange_v4_abi1_writer_direct_clear_ranges(
              writer, coverage_callback, &coverage, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(iprange_v4_abi1_writer_set_metadata_json(
              writer,
              metadata,
              sizeof(metadata) - 1,
              no_cancellation(),
              &changed,
              &error) == IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(changed == 1);
    CHECK(commit(writer) == 0);
    CHECK(close_writer(writer) == 0);
    writer = NULL;

    CHECK(iprange_v4_abi1_open_live_reader(
              path_from(path), no_cancellation(), &reader, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(iprange_v4_abi1_reader_open_direct_cursor(
              reader,
              IPRANGE_V4_ABI1_CURSOR_DIRECTION_FORWARD,
              NULL,
              &cursor,
              &error) == IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(iprange_v4_abi1_cursor_next_direct(
              cursor, &item_present, &item, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(item_present == 1 && item.value == 1);
    CHECK(iprange_v4_abi1_cursor_close(cursor, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(iprange_v4_abi1_cursor_destroy(cursor, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(iprange_v4_abi1_reader_close(reader, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(iprange_v4_abi1_reader_destroy(reader, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    reader = NULL;

    CHECK(iprange_v4_abi1_open_live_writer(
              path_from(path), &budget, no_cancellation(), &writer, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(iprange_v4_abi1_writer_clear_metadata_json(
              writer, no_cancellation(), &changed, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(changed == 1);
    CHECK(iprange_v4_abi1_writer_metadata_query(
              writer, &present, &required, &error) == IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(present == 0 && required == 0);
    CHECK(commit(writer) == 0);
    CHECK(iprange_v4_abi1_writer_reclaim(
              writer,
              UINT64_MAX,
              UINT64_MAX,
              no_cancellation(),
              &report,
              &error) == IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(iprange_v4_abi1_report_get_reclaim(report, &reclaim, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(destroy_report(report) == 0);
    CHECK(close_writer(writer) == 0);
    return 0;
}

int main(int argc, char **argv)
{
    CHECK(argc == 4);
    CHECK(exercise_membership_workflows(argv[1], argv[2]) == 0);
    CHECK(exercise_direct_advanced(argv[3]) == 0);
    return 0;
}
