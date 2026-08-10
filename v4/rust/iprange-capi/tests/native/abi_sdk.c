#include "abi_test_support.h"

typedef struct {
    uint64_t names;
    uint64_t cardinalities;
    uint64_t overlaps;
    uint64_t direct_cells;
    uint64_t cross_cells;
    uint64_t uncovered;
} sink_counts;

static iprange_v4_abi1_byte_slice bytes(const char *value)
{
    iprange_v4_abi1_byte_slice output = {0};
    output.pointer = (const uint8_t *)value;
    output.length = strlen(value);
    return output;
}

static uint32_t name_sink(
    void *context,
    const iprange_v4_abi1_feed_name_value *records,
    uint64_t count,
    iprange_v4_abi1_callback_failure *failure)
{
    sink_counts *counts = context;
    (void)records;
    (void)failure;
    counts->names += count;
    return IPRANGE_V4_ABI1_SINK_OUTCOME_CONTINUE;
}

static uint32_t feed_sink(
    void *context,
    const iprange_v4_abi1_feed_info *records,
    uint64_t count,
    iprange_v4_abi1_callback_failure *failure)
{
    sink_counts *counts = context;
    (void)records;
    (void)failure;
    counts->names += count;
    return IPRANGE_V4_ABI1_SINK_OUTCOME_CONTINUE;
}

static uint32_t cardinality_sink(
    void *context,
    const iprange_v4_abi1_feed_cardinality *records,
    uint64_t count,
    iprange_v4_abi1_callback_failure *failure)
{
    sink_counts *counts = context;
    (void)records;
    (void)failure;
    counts->cardinalities += count;
    return IPRANGE_V4_ABI1_SINK_OUTCOME_CONTINUE;
}

static uint32_t overlap_sink(
    void *context,
    const iprange_v4_abi1_feed_overlap *records,
    uint64_t count,
    iprange_v4_abi1_callback_failure *failure)
{
    sink_counts *counts = context;
    (void)records;
    (void)failure;
    counts->overlaps += count;
    return IPRANGE_V4_ABI1_SINK_OUTCOME_CONTINUE;
}

static uint32_t direct_join_sink(
    void *context,
    const iprange_v4_abi1_direct_join_cell *records,
    uint64_t count,
    iprange_v4_abi1_callback_failure *failure)
{
    sink_counts *counts = context;
    (void)records;
    (void)failure;
    counts->direct_cells += count;
    return IPRANGE_V4_ABI1_SINK_OUTCOME_CONTINUE;
}

static uint32_t cross_sink(
    void *context,
    const iprange_v4_abi1_membership_cross_cell *records,
    uint64_t count,
    iprange_v4_abi1_callback_failure *failure)
{
    sink_counts *counts = context;
    (void)records;
    (void)failure;
    counts->cross_cells += count;
    return IPRANGE_V4_ABI1_SINK_OUTCOME_CONTINUE;
}

static uint32_t uncovered_sink(
    void *context,
    const iprange_v4_abi1_uncovered_feed *records,
    uint64_t count,
    iprange_v4_abi1_callback_failure *failure)
{
    sink_counts *counts = context;
    (void)records;
    (void)failure;
    counts->uncovered += count;
    return IPRANGE_V4_ABI1_SINK_OUTCOME_CONTINUE;
}

static int create_immutable_feed(
    const char *path,
    const char *name,
    uint32_t from,
    uint32_t to)
{
    iprange_v4_abi1_range range = ipv4_range(from, to);
    coverage_source source = {&range, 1, 0};
    iprange_v4_abi1_immutable_feed_budget budget = {0};
    iprange_v4_abi1_optional_byte_slice metadata = {0};
    iprange_v4_abi1_immutable_feed_report facts = {0};
    iprange_v4_abi1_report *report = NULL;
    iprange_v4_abi1_error *error = NULL;
    budget.abi_version = IPRANGE_V4_ABI1_ABI_VERSION;
    budget.struct_size = sizeof(budget);
    budget.max_heap_bytes = 8 * 1024 * 1024;
    budget.max_output_pages = 65536;
    budget.max_workspace_pages = 65536;
    budget.max_open_files = 3;
    CHECK(iprange_v4_abi1_create_immutable_feed(
              path_from(path),
              IPRANGE_V4_ABI1_ADDRESS_FAMILY_IPV4,
              bytes("feeds"),
              bytes(name),
              metadata,
              IPRANGE_V4_ABI1_DESTINATION_POLICY_FAIL_IF_EXISTS,
              coverage_callback,
              &source,
              &budget,
              no_cancellation(),
              &facts,
              &report,
              &error) == IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(error == NULL);
    CHECK(facts.input_record_count == 1);
    CHECK(facts.normalized_interval_count == 1);
    CHECK(destroy_report(report) == 0);
    return 0;
}

static int finish_and_commit(iprange_v4_abi1_writer *writer)
{
    iprange_v4_abi1_report *report = NULL;
    iprange_v4_abi1_error *error = NULL;
    CHECK(iprange_v4_abi1_writer_finish_input(writer, &report, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(destroy_report(report) == 0);
    report = NULL;
    CHECK(iprange_v4_abi1_writer_commit(writer, &report, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(destroy_report(report) == 0);
    return 0;
}

static int close_writer(iprange_v4_abi1_writer *writer)
{
    iprange_v4_abi1_report *report = NULL;
    iprange_v4_abi1_error *error = NULL;
    CHECK(iprange_v4_abi1_writer_close(writer, &report, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(destroy_report(report) == 0);
    CHECK(iprange_v4_abi1_writer_destroy(writer, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    return 0;
}

static int create_direct(const char *path)
{
    iprange_v4_abi1_direct_range ranges[2] = {0};
    direct_source source;
    iprange_v4_abi1_transaction_budget budget = transaction_budget();
    iprange_v4_abi1_writer *writer = NULL;
    iprange_v4_abi1_report *report = NULL;
    iprange_v4_abi1_error *error = NULL;
    ranges[0].range = ipv4_range(0, 4);
    ranges[0].value = 10;
    ranges[1].range = ipv4_range(8, 12);
    ranges[1].value = 20;
    source.records = ranges;
    source.length = 2;
    source.offset = 0;
    CHECK(iprange_v4_abi1_create_live(
              path_from(path),
              IPRANGE_V4_ABI1_ADDRESS_FAMILY_IPV4,
              IPRANGE_V4_ABI1_VALUE_KIND_DIRECT,
              bytes("last_seen"),
              1,
              no_cancellation(),
              &report,
              &error) == IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(destroy_report(report) == 0);
    CHECK(iprange_v4_abi1_open_live_writer(
              path_from(path), &budget, no_cancellation(), &writer, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(iprange_v4_abi1_writer_begin_direct_replacement(
              writer, no_cancellation(), &error) == IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(iprange_v4_abi1_writer_add_direct_ranges(
              writer, direct_callback, &source, &error) == IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(finish_and_commit(writer) == 0);
    CHECK(close_writer(writer) == 0);
    return 0;
}

static int create_history_destination(const char *path)
{
    iprange_v4_abi1_report *report = NULL;
    iprange_v4_abi1_error *error = NULL;
    CHECK(iprange_v4_abi1_create_live(
              path_from(path),
              IPRANGE_V4_ABI1_ADDRESS_FAMILY_IPV4,
              IPRANGE_V4_ABI1_VALUE_KIND_MEMBERSHIP,
              bytes("history"),
              1,
              no_cancellation(),
              &report,
              &error) == IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(destroy_report(report) == 0);
    return 0;
}

static int close_reader(iprange_v4_abi1_reader *reader)
{
    iprange_v4_abi1_error *error = NULL;
    CHECK(iprange_v4_abi1_reader_close(reader, &error) == IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(iprange_v4_abi1_reader_destroy(reader, &error) == IPRANGE_V4_ABI1_STATUS_OK);
    return 0;
}

int main(int argc, char **argv)
{
    iprange_v4_abi1_reader *a_reader = NULL;
    iprange_v4_abi1_reader *b_reader = NULL;
    iprange_v4_abi1_reader *direct_reader = NULL;
    iprange_v4_abi1_membership_scope *a_scope = NULL;
    iprange_v4_abi1_membership_scope *b_scope = NULL;
    iprange_v4_abi1_membership_algebra *algebra = NULL;
    iprange_v4_abi1_error *error = NULL;
    iprange_v4_abi1_membership_query_budget query_budget = {0};
    iprange_v4_abi1_membership_algebra_budget algebra_budget = {0};
    iprange_v4_abi1_direct_join_budget direct_budget = {0};
    iprange_v4_abi1_membership_aggregation_report aggregate = {0};
    iprange_v4_abi1_direct_join_report direct_join = {0};
    iprange_v4_abi1_membership_join_report membership_join = {0};
    iprange_v4_abi1_matching_feeds_report matching = {0};
    iprange_v4_abi1_feed_selection_input all = {0};
    iprange_v4_abi1_feed_selection_input named_x = {0};
    iprange_v4_abi1_feed_selection_input named_y = {0};
    iprange_v4_abi1_byte_slice x_name = bytes("x");
    iprange_v4_abi1_byte_slice y_name = bytes("y");
    iprange_v4_abi1_algebra_count_report count = {0};
    iprange_v4_abi1_algebra_comparison_report comparison = {0};
    sink_counts sinks = {0};
    const iprange_v4_abi1_membership_scope *scopes[2];
    CHECK(argc == 6);
    CHECK(create_immutable_feed(argv[1], "x", 0, 9) == 0);
    CHECK(create_immutable_feed(argv[2], "y", 5, 14) == 0);
    CHECK(create_direct(argv[3]) == 0);
    CHECK(create_history_destination(argv[4]) == 0);

    CHECK(iprange_v4_abi1_open_immutable_reader(path_from(argv[1]), &a_reader, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(iprange_v4_abi1_open_immutable_reader(path_from(argv[2]), &b_reader, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(iprange_v4_abi1_open_live_reader(
              path_from(argv[3]), no_cancellation(), &direct_reader, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    query_budget.abi_version = IPRANGE_V4_ABI1_ABI_VERSION;
    query_budget.struct_size = sizeof(query_budget);
    query_budget.max_heap_bytes = 4 * 1024 * 1024;
    CHECK(iprange_v4_abi1_reader_all_feeds_scope(
              a_reader, &query_budget, no_cancellation(), &a_scope, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(iprange_v4_abi1_reader_named_feeds_scope(
              b_reader, &y_name, 1, &query_budget, no_cancellation(), &b_scope, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(iprange_v4_abi1_reader_matching_feeds(
              a_reader,
              ipv4(7),
              name_sink,
              &sinks,
              no_cancellation(),
              &matching,
              &error) == IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(matching.matching_feed_count == 1);
    CHECK(iprange_v4_abi1_membership_scope_feeds(
              a_scope, feed_sink, &sinks, no_cancellation(), &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(iprange_v4_abi1_membership_scope_aggregate(
              a_scope,
              IPRANGE_V4_ABI1_MEMBERSHIP_AGGREGATION_ALL_PAIRS,
              (iprange_v4_abi1_byte_slice){0},
              NULL,
              0,
              cardinality_sink,
              overlap_sink,
              &sinks,
              no_cancellation(),
              &aggregate,
              &error) == IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(aggregate.feed_result_count == 1);
    direct_budget.abi_version = IPRANGE_V4_ABI1_ABI_VERSION;
    direct_budget.struct_size = sizeof(direct_budget);
    direct_budget.max_result_cells = 16;
    CHECK(iprange_v4_abi1_membership_scope_join_direct(
              a_scope,
              direct_reader,
              &direct_budget,
              direct_join_sink,
              &sinks,
              no_cancellation(),
              &direct_join,
              &error) == IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(direct_join.mapped_addresses.lo == 7);
    CHECK(iprange_v4_abi1_membership_scope_join_membership(
              a_scope,
              b_scope,
              cross_sink,
              uncovered_sink,
              &sinks,
              no_cancellation(),
              &membership_join,
              &error) == IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(membership_join.overlap_addresses.lo == 5);

    scopes[0] = a_scope;
    scopes[1] = b_scope;
    algebra_budget.abi_version = IPRANGE_V4_ABI1_ABI_VERSION;
    algebra_budget.struct_size = sizeof(algebra_budget);
    algebra_budget.max_heap_bytes = 8 * 1024 * 1024;
    algebra_budget.max_sources = 2;
    CHECK(iprange_v4_abi1_membership_algebra_create(
              scopes, 2, &algebra_budget, no_cancellation(), &algebra, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(iprange_v4_abi1_membership_scope_close(a_scope, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(iprange_v4_abi1_membership_scope_destroy(a_scope, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(iprange_v4_abi1_membership_scope_close(b_scope, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(iprange_v4_abi1_membership_scope_destroy(b_scope, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(iprange_v4_abi1_membership_algebra_feeds(
              algebra, name_sink, &sinks, no_cancellation(), &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    named_x.kind = IPRANGE_V4_ABI1_FEED_SELECTION_NAMED;
    named_x.names = &x_name;
    named_x.name_count = 1;
    named_y.kind = IPRANGE_V4_ABI1_FEED_SELECTION_NAMED;
    named_y.names = &y_name;
    named_y.name_count = 1;
    CHECK(iprange_v4_abi1_membership_algebra_count(
              algebra, named_x, no_cancellation(), &count, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(count.addresses.lo == 10);
    CHECK(iprange_v4_abi1_membership_algebra_compare(
              algebra, named_x, named_y, no_cancellation(), &comparison, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(comparison.overlap_addresses.lo == 5);
    {
        iprange_v4_abi1_algebra_set_operation_input operation = {0};
        iprange_v4_abi1_algebra_output_mode_input mode = {0};
        iprange_v4_abi1_algebra_output_budget budget = {0};
        iprange_v4_abi1_optional_byte_slice metadata = {0};
        iprange_v4_abi1_algebra_set_report facts = {0};
        iprange_v4_abi1_report *report = NULL;
        all.kind = IPRANGE_V4_ABI1_FEED_SELECTION_ALL;
        operation.kind = IPRANGE_V4_ABI1_ALGEBRA_SET_UNION;
        operation.included = all;
        mode.kind = IPRANGE_V4_ABI1_ALGEBRA_OUTPUT_FLAT;
        mode.flat_name = bytes("combined");
        budget.abi_version = IPRANGE_V4_ABI1_ABI_VERSION;
        budget.struct_size = sizeof(budget);
        budget.max_output_pages = 65536;
        budget.max_open_files = 3;
        CHECK(iprange_v4_abi1_membership_algebra_publish_set(
                  algebra,
                  path_from(argv[5]),
                  bytes("combined"),
                  operation,
                  mode,
                  metadata,
                  IPRANGE_V4_ABI1_DESTINATION_POLICY_FAIL_IF_EXISTS,
                  &budget,
                  no_cancellation(),
                  &facts,
                  &report,
                  &error) == IPRANGE_V4_ABI1_STATUS_OK);
        CHECK(facts.output_feed_count == 1);
        CHECK(facts.output_addresses.lo == 15);
        CHECK(destroy_report(report) == 0);
    }
    CHECK(iprange_v4_abi1_membership_algebra_close(algebra, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(iprange_v4_abi1_membership_algebra_destroy(algebra, &error) ==
          IPRANGE_V4_ABI1_STATUS_OK);
    CHECK(close_reader(a_reader) == 0);
    CHECK(close_reader(b_reader) == 0);

    {
        iprange_v4_abi1_transaction_budget budget = transaction_budget();
        iprange_v4_abi1_writer *writer = NULL;
        iprange_v4_abi1_history_window_input window = {0};
        iprange_v4_abi1_history_projection_report facts = {0};
        iprange_v4_abi1_history_window_report window_facts = {0};
        iprange_v4_abi1_report *report = NULL;
        window.feed_name = bytes("recent");
        window.cutoff = 15;
        CHECK(iprange_v4_abi1_open_live_writer(
                  path_from(argv[4]), &budget, no_cancellation(), &writer, &error) ==
              IPRANGE_V4_ABI1_STATUS_OK);
        CHECK(iprange_v4_abi1_writer_project_history(
                  writer,
                  direct_reader,
                  &window,
                  1,
                  no_cancellation(),
                  &report,
                  &error) == IPRANGE_V4_ABI1_STATUS_OK);
        CHECK(iprange_v4_abi1_report_get_history_projection(report, &facts, &error) ==
              IPRANGE_V4_ABI1_STATUS_OK);
        CHECK(iprange_v4_abi1_report_get_history_window(
                  report, 0, &window_facts, &error) == IPRANGE_V4_ABI1_STATUS_OK);
        CHECK(facts.window_count == 1);
        CHECK(window_facts.after_addresses.lo == 5);
        CHECK(destroy_report(report) == 0);
        report = NULL;
        CHECK(iprange_v4_abi1_writer_commit(writer, &report, &error) ==
              IPRANGE_V4_ABI1_STATUS_OK);
        CHECK(destroy_report(report) == 0);
        CHECK(close_writer(writer) == 0);
    }
    CHECK(close_reader(direct_reader) == 0);
    CHECK(sinks.names >= 3);
    CHECK(sinks.cardinalities == 1);
    CHECK(sinks.direct_cells > 0);
    CHECK(sinks.cross_cells == 1);
    CHECK(sinks.uncovered == 2);
    return 0;
}
