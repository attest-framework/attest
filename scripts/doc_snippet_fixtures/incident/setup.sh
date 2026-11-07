#!/bin/sh
# Layered on top of `project`: a captured INC-1042 fixture and the contracts the project's tests record, as in the mutation guide.
set -e
ATTEST_CONTRACTS_DIR=.attest/contracts pytest -q tests/test_refund.py > /dev/null
