#!/bin/sh
# Runs inside the working directory before the snippet: a report and a pinned baseline for the guide commands.
set -e
pytest -q tests/test_refund.py --attest-report=attest-report.json > /dev/null
attest baseline pin --tag main --report attest-report.json > /dev/null
