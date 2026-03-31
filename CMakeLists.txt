cmake_minimum_required(VERSION 3.3)
project(iprange C)

find_package (Threads)

set(CMAKE_C_FLAGS "${CMAKE_C_FLAGS} -Wall -Wextra -Wformat-signedness -Werror=format-security")

set(SOURCE_FILES
        src/iprange.c src/ipset.c src/ipset.h src/iprange.h src/ipset_binary.c src/ipset_binary.h src/ipset_load.c src/ipset_load.h src/ipset_reduce.c src/ipset_print.c src/ipset_print.h src/ipset_optimize.c src/ipset_optimize.h src/ipset_reduce.h src/ipset_diff.c src/ipset_diff.h src/ipset_common.c src/ipset_common.h src/ipset_exclude.c src/ipset_exclude.h src/ipset_merge.c src/ipset_merge.h src/ipset_copy.c src/ipset_copy.h src/ipset_combine.c src/ipset_combine.h)

include_directories(AFTER ${CMAKE_CURRENT_BINARY_DIR} ${CMAKE_CURRENT_SOURCE_DIR}/src)
add_definitions("-DHAVE_CONFIG_H")

add_executable(iprange_git ${SOURCE_FILES})
