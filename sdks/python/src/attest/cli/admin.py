"""The ``attest`` command line.

``attest init`` runs in the SDK. Every other command (``doctor``, ``baseline``,
``policy``, ``cache``, ``calibrate``, ``version`` and any command a later engine adds)
runs in ``attest-engine``: the CLI resolves a version-checked engine with
:func:`attest.engine_manager.resolve_engine` and forwards the arguments, stdout, stderr
and exit code unchanged. A missing or mismatched engine exits 1 before anything runs, so
a gate such as ``attest policy evaluate`` never exits 0 without evaluating.
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


def _engine_or_error() -> str | None:
    """Resolve the engine, printing the reason to stderr when that fails."""
    try:
        return resolve_engine()
    except (OSError, RuntimeError, subprocess.SubprocessError, AttestError) as exc:
        print(f"attest: {exc}", file=sys.stderr)
        return None


def _engine_commands(engine: str) -> str:
    """Return the ``commands:`` section of ``attest-engine help``."""
    proc = subprocess.run(
        [engine, "help"], stdin=subprocess.DEVNULL, capture_output=True, text=True, check=True
    )
    lines = proc.stdout.splitlines()
    if "commands:" not in lines:
        raise RuntimeError(f"{engine} help printed no commands section:\n{proc.stdout}")
    return "\n".join(lines[lines.index("commands:") + 1 :])


def print_help() -> int:
    """Print SDK and engine commands. Returns 1 when the engine commands cannot be listed."""
    print(_SDK_USAGE)
    engine = _engine_or_error()
    if engine is None:
        print("engine commands: unavailable (see error above)")
        return 1
    try:
        commands = _engine_commands(engine)
    except (subprocess.CalledProcessError, RuntimeError) as exc:
        print(f"attest: {exc}", file=sys.stderr)
        return 1
    print(f"engine commands (attest-engine {ENGINE_VERSION}, {engine}):")
    print(commands)
    return 0


def forward(argv: Sequence[str]) -> int:
    """Run ``<engine> <argv>`` on a version-checked engine and return its exit code."""
    engine = _engine_or_error()
    if engine is None:
        return 1
    return subprocess.run([engine, *argv], check=False).returncode


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
