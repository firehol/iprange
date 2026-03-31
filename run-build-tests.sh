#!/bin/bash

set -e

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)

TEST_DIRS="tests.build.d" "$ROOT_DIR/run-tests.sh"
