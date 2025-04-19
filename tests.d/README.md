# iprange Test Suite

This directory contains tests for the iprange utility.

## Test Structure

Each test is in its own subdirectory and contains:

- `inputX` files (where X is a number) used as test inputs
- An `output` file with the expected output
- A `cmd.sh` script that runs the specific test case

## Running Tests

To run all tests, use the master test script from the main directory:

```
./run-tests.sh
```

The script will:
1. Run each test's `cmd.sh` script
2. Capture the output
3. Compare it with the expected output in the `output` file
4. Check exit codes
5. Report differences and failures

## Adding New Tests

To add a new test:

1. Create a new directory in `tests.d` with a descriptive name
2. Create the necessary input files
3. Create a `cmd.sh` script that runs iprange with the desired options
4. Generate the expected output file by running `cmd.sh` and redirecting to `output`
5. Make `cmd.sh` executable with `chmod +x cmd.sh`

## Test Cases

The test suite includes coverage for:

01. Basic merging of IP sets
02. Finding common IPs between sets
03. Excluding IPs from a set
04. Symmetric difference between sets (with differences - exit code 1)
05. Using @filename feature for file lists
06. Using @filename with compare mode
07. Using @filename with compare-next mode
08. Printing IP ranges instead of CIDRs
09. Using prefix and suffix for output
10. Using --dont-fix-network option
11. Symmetric difference with no differences (exit code 0)

Each test verifies a specific feature or mode of the iprange utility.