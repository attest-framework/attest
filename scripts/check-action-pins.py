#!/usr/bin/env python3
"""Fail when a GitHub Actions `uses:` is not pinned to a full commit SHA.

Every remote action or reusable workflow must read `owner/repo[/path]@<40 hex> # <version>`: a
tag or branch can be moved after review, a commit SHA cannot, and the trailing comment is what
Dependabot rewrites when it bumps the pin. Local references (`./...`) are exempt; `docker://`
references must end in `@sha256:<64 hex>`. `uses:` is found in block style (`uses: x`, `- uses: x`)
and in flow style (`- { uses: x, with: {...} }`), quoted or not.

Usage: check-action-pins.py [--verify-remote] [PATH ...]

A PATH is a workflow or action file, or a directory searched for `*.yml`/`*.yaml` files. With no
PATH the repository's `.github/workflows` and every `action.yml`/`action.yaml` under the
repository root (skipping dependency and virtualenv directories) are checked.

--verify-remote also runs `git ls-remote` for each pin's `# <tag>` comment and requires the
pinned SHA to equal the commit the tag peels to, which catches a SHA that is a real commit of
some fork or an unrelated branch (an impostor commit) but carries a legitimate-looking comment.
"""

from __future__ import annotations

import re
import subprocess
import sys
from collections.abc import Iterator
from dataclasses import dataclass
from pathlib import Path

# `uses:` as a mapping key: first on its line (optionally after `- `) or after `{` / `,` in a
# flow mapping. The value is quoted or runs to whitespace, `,`, `}` or `#`.
USES = re.compile(
    r"""(?:^\s*(?:-\s*)?|[{,]\s*)uses:\s*(?P<ref>"[^"]*"|'[^']*'|[^\s,}#]+)"""
)
COMMENT = re.compile(r"\s#\s*(?P<tag>\S+)")
ACTION_REF = re.compile(r"^(?P<repo>[\w.-]+/[\w.-]+)(?P<path>/[^@\s]+)?@(?P<sha>[0-9a-f]{40})$")
DOCKER_REF = re.compile(r"^docker://\S+@sha256:[0-9a-f]{64}$")
SKIPPED_DIRS = {".git", "node_modules", ".venv", ".worktrees", "dist", "build", "__pycache__"}


@dataclass(frozen=True)
class Pin:
    """A remote `uses:` that is pinned to a SHA and carries a version comment."""

    where: str
    repo: str
    sha: str
    tag: str


def _references(path: Path) -> Iterator[tuple[str, str, str]]:
    """Yield `(where, ref, tag)` for each `uses:` in ``path``; ``tag`` is the first word of the
    trailing comment, or "" when there is none."""
    for number, line in enumerate(path.read_text().splitlines(), start=1):
        if line.lstrip().startswith("#"):
            continue
        for match in USES.finditer(line):
            ref = match["ref"].strip("'\"")
            comment = COMMENT.search(line, match.end())
            yield f"{path}:{number}", ref, comment["tag"] if comment else ""


def violations(path: Path) -> list[str]:
    """Return one message per unpinned or uncommented `uses:` in ``path``."""
    found: list[str] = []
    for where, ref, tag in _references(path):
        if ref.startswith("./"):
            continue
        if ref.startswith("docker://"):
            if not DOCKER_REF.match(ref):
                found.append(f"{where}: docker image is not pinned to a sha256:<64 hex> digest: {ref}")
        elif not ACTION_REF.match(ref):
            found.append(f"{where}: not pinned to a 40-hex commit SHA: {ref}")
        elif not tag:
            found.append(f"{where}: pinned action has no version comment: {ref}")
    return found


def pins(path: Path) -> list[Pin]:
    """The well-formed, commented remote pins in ``path``."""
    found: list[Pin] = []
    for where, ref, tag in _references(path):
        match = ACTION_REF.match(ref)
        if match and tag:
            found.append(Pin(where, match["repo"], match["sha"], tag))
    return found


def _peeled_commit(repo: str, tag: str) -> str | None:
    """The commit ``tag`` points at on GitHub (annotated tags dereferenced), or None."""
    result = subprocess.run(
        [
            "git",
            "ls-remote",
            f"https://github.com/{repo}",
            f"refs/tags/{tag}",
            f"refs/tags/{tag}^{{}}",
        ],
        capture_output=True,
        text=True,
        check=False,
    )
    if result.returncode != 0:
        raise RuntimeError(f"git ls-remote {repo} failed: {result.stderr.strip()}")
    refs = {ref: sha for sha, ref in (line.split() for line in result.stdout.splitlines())}
    return refs.get(f"refs/tags/{tag}^{{}}") or refs.get(f"refs/tags/{tag}")


def remote_violations(all_pins: list[Pin]) -> list[str]:
    """One message per pin whose SHA is not the commit its version comment names."""
    found: list[str] = []
    cache: dict[tuple[str, str], str | None] = {}
    for pin in all_pins:
        key = (pin.repo, pin.tag)
        if key not in cache:
            cache[key] = _peeled_commit(*key)
        commit = cache[key]
        if commit is None:
            found.append(f"{pin.where}: {pin.repo} has no tag {pin.tag}")
        elif commit != pin.sha:
            found.append(
                f"{pin.where}: {pin.repo}@{pin.sha[:12]} is not {pin.tag} "
                f"(the tag points at {commit[:12]})"
            )
    return found


def collect(paths: list[Path]) -> list[Path]:
    files: list[Path] = []
    for path in paths:
        if path.is_dir():
            files.extend(
                p
                for p in sorted(path.rglob("*"))
                if p.suffix in {".yml", ".yaml"} and not SKIPPED_DIRS & set(p.relative_to(path).parts)
            )
        else:
            files.append(path)
    return files


def default_paths() -> list[Path]:
    root = Path(__file__).resolve().parent.parent
    actions = [
        p
        for p in sorted(root.rglob("action.y*ml"))
        if not SKIPPED_DIRS & set(p.relative_to(root).parts)
    ]
    return [root / ".github" / "workflows", *actions]


def main(argv: list[str]) -> int:
    verify_remote = "--verify-remote" in argv
    args = [a for a in argv if a != "--verify-remote"]
    paths = [Path(a) for a in args] if args else default_paths()
    missing = [p for p in paths if not p.exists()]
    if missing:
        print(f"check-action-pins: no such path: {missing[0]}", file=sys.stderr)
        return 2
    files = collect(paths)
    if not files:
        print("check-action-pins: no workflow or action files found", file=sys.stderr)
        return 2
    problems = [message for path in files for message in violations(path)]
    for message in problems:
        print(message, file=sys.stderr)
    if problems:
        print(f"check-action-pins: {len(problems)} unpinned action reference(s)", file=sys.stderr)
        return 1
    print(f"check-action-pins: {len(files)} file(s), every `uses:` is SHA-pinned")
    if verify_remote:
        all_pins = [pin for path in files for pin in pins(path)]
        impostors = remote_violations(all_pins)
        for message in impostors:
            print(message, file=sys.stderr)
        if impostors:
            print(f"check-action-pins: {len(impostors)} pin(s) do not match their tag", file=sys.stderr)
            return 1
        print(f"check-action-pins: {len(all_pins)} pin(s) match their tag on GitHub")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
