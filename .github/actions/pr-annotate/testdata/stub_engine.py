#!/usr/bin/env python3
"""Stub attest-engine for the pr-annotate tests (the real `report` commands live in the engine).

Behaviour is driven by files next to each report path P:
  P.policy-exit  exit code of `policy evaluate` (default 0)
  P.rows.json    stdout of `report rows` (default [])
  P.rows-exit    exit code of `report rows` (default 0)
Every call appends its argv as one JSON line to $STUB_LOG.
"""

import json
import os
import signal
import sys
from pathlib import Path

argv = sys.argv[1:]
with open(os.environ["STUB_LOG"], "a", encoding="utf-8") as log:
    log.write(json.dumps(argv) + "\n")
if os.environ.get("GITHUB_TOKEN") or os.environ.get("INPUT_GITHUB-TOKEN"):
    Path(os.environ["STUB_LOG"] + ".token-seen").write_text("1")


def reports() -> list[str]:
    return [argv[i + 1] for i, a in enumerate(argv) if a == "--report"]


def side(report: str, suffix: str, default: str) -> str:
    path = Path(report + suffix)
    return path.read_text() if path.exists() else default


if argv[:2] == ["policy", "evaluate"]:
    report = reports()[0]
    print(json.dumps({"report": report}))
    code = side(report, ".policy-exit", "0")
    if code == "kill":
        os.kill(os.getpid(), signal.SIGKILL)
    sys.exit(int(code))
if argv[:2] == ["report", "rows"]:
    report = reports()[0]
    sys.stdout.write(side(report, ".rows.json", "[]"))
    sys.exit(int(side(report, ".rows-exit", "0")))
if argv[:2] == ["report", "summary"]:
    if os.environ.get("STUB_SUMMARY_EXIT"):
        print("bad input", file=sys.stderr)
        sys.exit(int(os.environ["STUB_SUMMARY_EXIT"]))
    print(f"### attest: 1 passed · {len(reports())} failed · 0 not evaluated")
    if "--policy-result" in argv:
        print("policy: " + Path(argv[argv.index("--policy-result") + 1]).read_text().strip())
    sys.exit(0)
sys.exit(64)
