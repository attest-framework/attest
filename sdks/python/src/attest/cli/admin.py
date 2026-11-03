"""The ``attest`` command line.

``attest init`` runs in the SDK. Every other command (``doctor``, ``baseline``,
``policy``, ``cache``, ``calibrate``, ``version`` and any command a later engine adds)
runs in ``attest-engine``: the CLI resolves a version-checked engine with
:func:`attest.engine_manager.resolve_engine`, checks that the engine's ``help`` lists the
command, and forwards the arguments, stdout, stderr and exit code unchanged. A missing,
mismatched or too-old engine exits 1 before anything runs, so a gate such as
``attest policy evaluate`` never exits 0 without evaluating.

``attest capture`` is forwarded the same way; when the engine writes a fixture, the CLI
then drafts its pytest file (:func:`attest.capture.emit_pytest`).
"""

from __future__ import annotations

import contextlib
import json
import subprocess
import sys
import tempfile
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
    "  init [--ci github] [--skills] [--json]\n"
    "                      scaffold a sample test and merge policy in the current directory\n"
    "                      (--ci github also writes .github/workflows/attest.yml;\n"
    "                      --skills installs the agent skills with attest-engine;\n"
    "                      --json prints {created, updated, skipped} paths as JSON)\n"
    "\n"
    "`attest capture` (engine) also drafts tests/attest/test_<name>.py (name lowercased,\n"
    "other characters as _) from a verified fixture, unless that file exists.\n"
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
        first_line = str(exc).splitlines()[0].rstrip(".")
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


def _engine_providing(command: str) -> str | None:
    """Resolve a version-checked engine whose ``help`` lists ``command``.

    Prints the reason to stderr and returns ``None`` when there is none.
    """
    try:
        engine = resolve_engine()
    except _RESOLVE_ERRORS as exc:
        print(f"attest: {exc}", file=sys.stderr)
        return None
    commands = engine_commands(engine)
    if commands is None or (command != "help" and command not in commands):
        print(
            f"attest: attest-engine at {engine} (version {ENGINE_VERSION}) predates "
            f"`attest {command}`: its help does not list that command. Set ATTEST_ENGINE_PATH "
            "to an engine built from this release (make engine), or install a release whose "
            "engine provides it.",
            file=sys.stderr,
        )
        return None
    return engine


_INIT_HELP_ENTRY: dict[str, object] = {
    "name": "init",
    "summary": "scaffold a sample test and policy; --ci github, --skills, --json",
    "schema": "cli/init.schema.json",
    "exit_codes": [
        {"code": 0, "meaning": "success"},
        {
            "code": 1,
            "meaning": "--skills could not install: the engine is missing, too old or failed",
        },
        {"code": 2, "meaning": "usage error"},
    ],
}


def _help_json(engine: str) -> int:
    """Print the engine's ``help --json`` document with the SDK's ``init`` command added."""
    proc = subprocess.run(
        [engine, "help", "--json"],
        stdin=subprocess.DEVNULL,
        capture_output=True,
        text=True,
        check=False,
    )
    if proc.returncode != 0:
        sys.stderr.write(proc.stderr)
        return exit_status(proc.returncode)
    try:
        doc = json.loads(proc.stdout)
        commands = doc["commands"]
    except (json.JSONDecodeError, KeyError, TypeError) as exc:
        print(
            f"attest: `{engine} help --json` printed an unexpected document: {exc}",
            file=sys.stderr,
        )
        return 1
    doc["commands"] = sorted([*commands, _INIT_HELP_ENTRY], key=lambda c: str(c["name"]))
    print(json.dumps(doc, indent=2))
    return 0


def forward(argv: Sequence[str]) -> int:
    """Run ``<engine> <argv>`` on a version-checked engine that provides the command."""
    engine = _engine_providing(argv[0])
    if engine is None:
        return 1
    if list(argv) == ["help", "--json"]:
        return _help_json(engine)
    if argv[0] == "capture" and _capture_writes_fixture(argv[1:]):
        return _capture_and_emit(engine, argv[1:])
    return exit_status(subprocess.run([engine, *argv], check=False).returncode)


# Engine capture modes the CLI drafts no test for: help, --check-redaction (writes no
# fixture) and --serve (used by record(), which drafts its own). Go flags accept one or
# two dashes.
_CAPTURE_NO_TEST = frozenset({"check-redaction", "serve", "h", "help"})


def _flag_name(arg: str) -> str | None:
    if not arg.startswith("-") or arg in ("-", "--"):
        return None
    return arg.lstrip("-").split("=", 1)[0]


def _capture_writes_fixture(args: Sequence[str]) -> bool:
    """True when ``attest capture`` args select a mode whose fixture the CLI drafts a
    test for."""
    for a in args:
        if a == "--":
            break
        if _flag_name(a) in _CAPTURE_NO_TEST:
            return False
    return True


def _user_result_file(args: Sequence[str]) -> bool:
    for a in args:
        if a == "--":
            break
        if _flag_name(a) == "result-file":
            return True
    return False


def _capture_and_emit(engine: str, args: Sequence[str]) -> int:
    """Run ``attest-engine capture`` and draft the pytest file for the fixture it wrote."""
    from attest.capture import emit_pytest, test_path_for

    if _user_result_file(args):
        print(
            "attest: capture --result-file is set by the attest CLI; "
            "run attest-engine capture directly to choose it",
            file=sys.stderr,
        )
        return 2
    with tempfile.TemporaryDirectory(prefix="attest-capture-") as tmp:
        result_file = Path(tmp) / "result.json"
        code = exit_status(
            subprocess.run(
                [engine, "capture", "--result-file", str(result_file), *args], check=False
            ).returncode
        )
        if not result_file.exists():
            return code
        result = json.loads(result_file.read_text(encoding="utf-8"))
    fixture = Path(result["fixture"])
    if not result["verified"]:
        print(f"  test: none drafted for unverified {fixture}", file=sys.stderr)
        return code
    written = emit_pytest(fixture)
    if written is None:
        print(f"  test: {test_path_for(fixture)} exists; left unchanged", file=sys.stderr)
    else:
        print(f"  test: drafted {written}", file=sys.stderr)
    return code


_INIT_USAGE = (
    "usage: attest init [--ci github] [--skills] [--json]  (scaffolds into the current directory)"
)

_INIT_KINDS = ("created", "updated", "skipped")

# `attest-engine skills install --json` key -> `attest init --json` key.
_SKILLS_KINDS = {"created": "created", "updated": "updated", "unchanged": "skipped"}


def _parse_init_args(args: Sequence[str]) -> tuple[str | None, bool, bool] | None:
    """Return ``(ci, skills, json)`` for ``attest init`` args, or ``None`` on a usage error."""
    ci: str | None = None
    skills = as_json = False
    rest = list(args)
    while rest:
        arg = rest.pop(0)
        if arg == "--ci" and rest and rest[0] == "github" and ci is None:
            rest.pop(0)
            ci = "github"
        elif arg == "--ci=github" and ci is None:
            ci = "github"
        elif arg == "--skills" and not skills:
            skills = True
        elif arg == "--json" and not as_json:
            as_json = True
        else:
            return None
    return ci, skills, as_json


def _install_skills(engine: str, root: Path) -> dict[str, list[str]]:
    """Run ``<engine> skills install --dir <root> --json`` and map its paths to init kinds.

    Raises :class:`AttestError` when the engine fails or prints an unexpected document.
    """
    argv = [engine, "skills", "install", "--dir", str(root), "--json"]
    proc = subprocess.run(
        argv, stdin=subprocess.DEVNULL, capture_output=True, text=True, check=False
    )
    if proc.returncode != 0:
        detail = proc.stderr.strip()
        raise AttestError(
            f"skills install failed (`{' '.join(argv)}` exited {exit_status(proc.returncode)})"
            + (f": {detail}" if detail else "")
        )
    sys.stderr.write(proc.stderr)
    try:
        doc = json.loads(proc.stdout)
    except json.JSONDecodeError as exc:
        raise AttestError(f"`{' '.join(argv)}` printed invalid JSON: {exc}") from exc
    if not isinstance(doc, dict):
        raise AttestError(f"`{' '.join(argv)}` printed {type(doc).__name__}, not an object")
    result: dict[str, list[str]] = {kind: [] for kind in _INIT_KINDS}
    for engine_kind, init_kind in _SKILLS_KINDS.items():
        paths = doc.get(engine_kind)
        if not isinstance(paths, list) or not all(isinstance(p, str) for p in paths):
            raise AttestError(
                f"`{' '.join(argv)}` printed no `{engine_kind}` list of paths: {proc.stdout!r}"
            )
        result[init_kind].extend(paths)
    return result


def _print_skills(root: Path, skills: dict[str, list[str]]) -> None:
    for rel in skills["created"]:
        print(f"Created: {root / rel}")
    for rel in skills["updated"]:
        print(f"Updated: {root / rel}")
    for rel in skills["skipped"]:
        print(f"Skipped (unchanged): {root / rel}", file=sys.stderr)


def init(args: Sequence[str]) -> int:
    """Run ``attest init [--ci github] [--skills] [--json]`` in the current directory.

    ``--skills`` installs the agent skills with ``attest-engine skills install`` before
    scaffolding; a missing or failing engine exits 1 with nothing scaffolded. ``--json``
    prints one ``{"created", "updated", "skipped"}`` document (paths relative to the
    current directory, sorted) as the only stdout; everything else goes to stderr.
    """
    if any(a in _HELP_FLAGS for a in args):
        print(_INIT_USAGE)
        return 0
    parsed = _parse_init_args(args)
    if parsed is None:
        print(_INIT_USAGE, file=sys.stderr)
        return 2
    ci, skills, as_json = parsed
    from attest.scaffold import scaffold_project

    root = Path.cwd()
    result: dict[str, list[str]] = {kind: [] for kind in _INIT_KINDS}
    # Under --json, stdout carries the document only: anything else printed goes to stderr.
    with contextlib.redirect_stdout(sys.stderr) if as_json else contextlib.nullcontext():
        if skills:
            engine = _engine_providing("skills")
            if engine is None:
                return 1
            try:
                installed = _install_skills(engine, root)
            except AttestError as exc:
                print(f"attest: {exc}", file=sys.stderr)
                return 1
            if not as_json:
                _print_skills(root, installed)
            for kind in _INIT_KINDS:
                result[kind].extend(installed[kind])
        scaffolded = scaffold_project(root, ci=ci, quiet=as_json)
    result["created"].extend(scaffolded.created)
    result["skipped"].extend(scaffolded.skipped)
    if as_json:
        print(json.dumps({kind: sorted(result[kind]) for kind in _INIT_KINDS}))
    return 0


def main(argv: Sequence[str] | None = None) -> int:
    """Dispatch ``attest`` arguments and return the process exit code."""
    args = list(sys.argv[1:] if argv is None else argv)

    if not args or args[0] in _HELP_FLAGS or args == ["help"]:
        return print_help()
    if args[0] == "--version":
        print(f"attest {__version__} (requires attest-engine {ENGINE_VERSION})")
        return 0
    if args[0] == "init":
        return init(args[1:])
    if args[0].startswith("-"):
        print(f"attest: unknown option {args[0]}\n", file=sys.stderr)
        print(_SDK_USAGE, file=sys.stderr)
        return 2
    return forward(args)


def run() -> None:
    """Console-script entry point for ``attest``."""
    sys.exit(main())
