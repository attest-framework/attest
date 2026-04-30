"""CLI subcommand handlers for ``attest baseline`` and ``attest policy``.

Both commands are thin pass-throughs to the ``attest-engine`` binary —
the engine owns the SQLite cache (baselines table) and the policy
evaluator, so re-implementing in Python would create two sources of
truth. The Python SDK owns argument parsing, locating the engine
binary, and forwarding stdout/stderr/exit-code unchanged.
"""

from __future__ import annotations

import os
import shutil
import subprocess
import sys
from collections.abc import Sequence
from pathlib import Path


def _find_engine_binary() -> str:
    """Locate the attest-engine binary using the same precedence as
    ``EngineManager``: ATTEST_ENGINE_PATH → PATH → ~/.attest/bin.

    Raises:
        FileNotFoundError: If the binary cannot be located. Callers
            should print a single-line install hint and exit 1.
    """
    env = os.environ.get("ATTEST_ENGINE_PATH")
    if env:
        if not Path(env).is_file():
            raise FileNotFoundError(
                f"ATTEST_ENGINE_PATH={env} does not point to an executable file"
            )
        return env

    on_path = shutil.which("attest-engine")
    if on_path:
        return on_path

    home_bin = Path.home() / ".attest" / "bin" / "attest-engine"
    if home_bin.is_file():
        return str(home_bin)

    raise FileNotFoundError(
        "attest-engine binary not found; set ATTEST_ENGINE_PATH or run "
        "`attest-engine` install (see docs)"
    )


def _passthrough(subcommand: str, argv: Sequence[str]) -> int:
    """Run ``<engine-binary> <subcommand> <argv>`` and return its exit code.

    stdout/stderr stream through unchanged so callers get the engine's
    JSON output verbatim.
    """
    try:
        engine = _find_engine_binary()
    except FileNotFoundError as exc:
        print(f"attest {subcommand}: {exc}", file=sys.stderr)
        return 1
    cmd = [engine, subcommand, *argv]
    proc = subprocess.run(cmd, check=False)
    return proc.returncode


def cmd_baseline(argv: Sequence[str]) -> int:
    """Run ``attest baseline <subcommand>`` via the engine binary.

    Returns the engine's exit code. With no subcommand or with ``--help``
    a usage line is printed and 2 is returned to match the engine.
    """
    if not argv or argv[0] in {"-h", "--help"}:
        _print_baseline_usage()
        return 2
    return _passthrough("baseline", argv)


def cmd_policy(argv: Sequence[str]) -> int:
    """Run ``attest policy <subcommand>`` via the engine binary.

    The policy evaluate exit codes (0/1/2 = pass/block/warn) propagate
    untouched so a workflow can use this command as a merge gate.
    """
    if not argv or argv[0] in {"-h", "--help"}:
        _print_policy_usage()
        return 2
    return _passthrough("policy", argv)


def _print_baseline_usage() -> None:
    print(
        "usage: attest baseline <pin|list|show|delete> [args]\n"
        "  pin    --tag <name> --report <path>\n"
        "  list\n"
        "  show   --tag <name>\n"
        "  delete --tag <name>",
        file=sys.stderr,
    )


def _print_policy_usage() -> None:
    print(
        "usage: attest policy evaluate --policy <path> --report <path> [--baseline <tag>]",
        file=sys.stderr,
    )
