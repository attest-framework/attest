"""The ``attest`` command line.

``attest init`` runs in the SDK. Every other command (``doctor``, ``baseline``,
``policy``, ``cache``, ``calibrate``, ``version`` and any command a later engine adds)
runs in ``attest-engine``: the CLI resolves a version-checked engine with
:func:`attest.engine_manager.resolve_engine`, checks that the engine's ``help`` lists the
command, and forwards the arguments, stdout, stderr and exit code unchanged. A missing,
mismatched or too-old engine exits 1 before anything runs, so a gate such as
``attest policy evaluate`` never exits 0 without evaluating.
"""

from __future__ import annotations

import subprocess
import sys
from collections.abc import Sequence
from pathlib import Path

from attest import ENGINE_VERSION, __version__
from attest.engine_manager import resolve_engine
from attest.exceptions import AttestError

_HELP_FLAGS = frozenset({"-h", "--help"})

_HELP_PROBE_TIMEOUT = 10.0

_SDK_USAGE = (
    "usage: attest <command> [args]\n"
    "\n"
    "SDK commands:\n"
    "  init  scaffold an attest test project in the current directory\n"
    "\n"
    "options:\n"
    "  --version  print the SDK version and the engine version it requires\n"
    "  --help     print this help\n"
)

_RESOLVE_ERRORS = (OSError, RuntimeError, subprocess.SubprocessError, AttestError)


def engine_commands(engine: str) -> dict[str, str] | None:
    """Return ``{command: summary}`` from ``<engine> help``, or ``None`` if it lists none.

    stdin is closed and the probe times out after 10 s, so an engine that predates the
    command registry (and starts the JSON-RPC server for ``help``) cannot hang the CLI.
    """
    try:
        proc = subprocess.run(
            [engine, "help"],
            stdin=subprocess.DEVNULL,
            capture_output=True,
            text=True,
            timeout=_HELP_PROBE_TIMEOUT,
            check=False,
        )
    except subprocess.TimeoutExpired:
        return None
    lines = proc.stdout.splitlines()
    if proc.returncode != 0 or "commands:" not in lines:
        return None
    commands: dict[str, str] = {}
    for line in lines[lines.index("commands:") + 1 :]:
        name, _, summary = line.strip().partition(" ")
        if name:
            commands[name] = summary.strip()
    return commands


def print_help() -> int:
    """Print SDK commands, plus engine commands when an engine is available without download.

    Never downloads and always returns 0.
    """
    print(_SDK_USAGE)
    try:
        engine = resolve_engine(allow_download=False)
    except _RESOLVE_ERRORS as exc:
        first_line = str(exc).splitlines()[0]
        print(
            f"engine commands: not available ({first_line}). Run `attest doctor` to download "
            f"attest-engine {ENGINE_VERSION}, or set ATTEST_ENGINE_PATH."
        )
        return 0
    commands = engine_commands(engine)
    if commands is None:
        print(
            f"engine commands: not available (attest-engine at {engine} lists no commands; "
            "it predates the command registry)."
        )
        return 0
    width = max(len(name) for name in commands) if commands else 0
    print(f"engine commands (attest-engine {ENGINE_VERSION}, {engine}):")
    for name, summary in commands.items():
        print(f"  {name.ljust(width)}  {summary}")
    return 0


def exit_status(returncode: int) -> int:
    """Map a subprocess return code to a shell exit status (signal N → 128 + N)."""
    return 128 - returncode if returncode < 0 else returncode


def forward(argv: Sequence[str]) -> int:
    """Run ``<engine> <argv>`` on a version-checked engine that provides the command."""
    try:
        engine = resolve_engine()
    except _RESOLVE_ERRORS as exc:
        print(f"attest: {exc}", file=sys.stderr)
        return 1
    commands = engine_commands(engine)
    command = argv[0]
    if commands is None or (command != "help" and command not in commands):
        print(
            f"attest: attest-engine at {engine} (version {ENGINE_VERSION}) predates "
            f"`attest {command}`: its help does not list that command. Set ATTEST_ENGINE_PATH "
            "to an engine built from this release (make engine), or install a release whose "
            "engine provides it.",
            file=sys.stderr,
        )
        return 1
    return exit_status(subprocess.run([engine, *argv], check=False).returncode)


def main(argv: Sequence[str] | None = None) -> int:
    """Dispatch ``attest`` arguments and return the process exit code."""
    args = list(sys.argv[1:] if argv is None else argv)

    if not args or args[0] in _HELP_FLAGS or args == ["help"]:
        return print_help()
    if args[0] == "--version":
        print(f"attest {__version__} (requires attest-engine {ENGINE_VERSION})")
        return 0
    if args[0] == "init":
        if len(args) > 1:
            print("usage: attest init  (scaffolds into the current directory)", file=sys.stderr)
            return 0 if args[1] in _HELP_FLAGS else 2
        from attest.scaffold import scaffold_project

        scaffold_project(Path.cwd())
        return 0
    if args[0].startswith("-"):
        print(f"attest: unknown option {args[0]}\n", file=sys.stderr)
        print(_SDK_USAGE, file=sys.stderr)
        return 2
    return forward(args)


def run() -> None:
    """Console-script entry point for ``attest``."""
    sys.exit(main())
