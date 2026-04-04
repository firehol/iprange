cmake_minimum_required(VERSION 3.10)
project(iprange C)

find_package(Threads REQUIRED)

include(CheckTypeSize)
check_type_size("__uint128_t" UINT128_SIZE LANGUAGE C)
if(NOT HAVE_UINT128_SIZE)
    message(FATAL_ERROR "Compiler does not support __uint128_t, required for IPv6")
endif()

# Generate config.h with the defines the source expects
include(CheckIncludeFile)
check_include_file(inttypes.h HAVE_INTTYPES_H)
check_include_file(stdint.h HAVE_STDINT_H)

configure_file(
    ${CMAKE_CURRENT_SOURCE_DIR}/cmake/config.h.in
    ${CMAKE_CURRENT_BINARY_DIR}/config.h
)

set(SOURCES
    src/iprange.c
    src/iprange6_main.c
    src/ipset.c
    src/ipset6.c
    src/ipset6_binary.c
    src/ipset6_combine.c
    src/ipset6_common.c
    src/ipset6_copy.c
    src/ipset6_diff.c
    src/ipset6_dns.c
    src/ipset6_exclude.c
    src/ipset6_load.c
    src/ipset6_merge.c
    src/ipset6_optimize.c
    src/ipset6_print.c
    src/ipset_binary.c
    src/ipset_combine.c
    src/ipset_common.c
    src/ipset_copy.c
    src/ipset_diff.c
    src/ipset_dns.c
    src/ipset_exclude.c
    src/ipset_load.c
    src/ipset_merge.c
    src/ipset_optimize.c
    src/ipset_print.c
    src/ipset_reduce.c
)

add_executable(iprange ${SOURCES})
target_include_directories(iprange PRIVATE
    ${CMAKE_CURRENT_BINARY_DIR}
    ${CMAKE_CURRENT_SOURCE_DIR}/src
)
target_compile_definitions(iprange PRIVATE
    HAVE_CONFIG_H
    COMPARE_WITH_COMMON=1
    VERSION="${PROJECT_VERSION}"
)
target_compile_options(iprange PRIVATE
    -Wall -Wextra -Wformat-signedness -Werror=format-security
)
target_link_libraries(iprange Threads::Threads)
