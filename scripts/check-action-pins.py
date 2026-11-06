#!/usr/bin/env python3
"""Fail when a GitHub Actions `uses:` is not pinned to a full commit SHA.

Every remote action must read `owner/repo[/path]@<40 hex> # <version>`: a tag or branch can be
moved after review, a commit SHA cannot, and the trailing comment is what Dependabot rewrites
when it bumps the pin. Local actions (`./...`) are exempt; `docker://` references must carry a
`sha256:` digest.

Usage: check-action-pins.py [PATH ...]

A PATH is a workflow or action file, or a directory searched for `*.yml`/`*.yaml` files. With no
PATH the repository's `.github/workflows` and every `action.yml`/`action.yaml` under the
repository root (skipping dependency and virtualenv directories) are checked.
"""

from __future__ import annotations

import re
import sys
from pathlib import Path

USES = re.compile(r"^\s*(?:-\s*)?uses:\s*(?P<ref>[^\s#]+)\s*(?P<comment>#.*)?$")
SHA = re.compile(r"^[0-9a-f]{40}$")
SKIPPED_DIRS = {".git", "node_modules", ".venv", ".worktrees", "dist", "build", "__pycache__"}


def violations(path: Path) -> list[str]:
    """Return one message per unpinned or uncommented `uses:` in ``path``."""
    found: list[str] = []
    for number, line in enumerate(path.read_text().splitlines(), start=1):
        match = USES.match(line)
        if match is None:
            continue
        ref = match["ref"].strip("'\"")
        if ref.startswith("./"):
            continue
        where = f"{path}:{number}"
        if ref.startswith("docker://"):
            if "@sha256:" not in ref:
                found.append(f"{where}: docker image is not pinned to a sha256 digest: {ref}")
            continue
        _, _, pin = ref.partition("@")
        if not SHA.match(pin):
            found.append(f"{where}: not pinned to a 40-hex commit SHA: {ref}")
        elif not (match["comment"] or "").lstrip("# ").strip():
            found.append(f"{where}: pinned action has no version comment: {ref}")
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


def default_paths(root: Path) -> list[Path]:
    actions = [
        p
        for name in ("action.yml", "action.yaml")
        for p in sorted(root.rglob(name))
        if not SKIPPED_DIRS & set(p.relative_to(root).parts)
    ]
    return [root / ".github" / "workflows", *actions]


def main(argv: list[str]) -> int:
    root = Path(__file__).resolve().parent.parent
    paths = [Path(a) for a in argv] or default_paths(root)
    missing = [p for p in paths if not p.exists()]
    if missing:
        print(f"check-action-pins: no such path: {missing[0]}", file=sys.stderr)
        return 2
    files = collect(paths)
    if not files:
        print("check-action-pins: no workflow or action files found", file=sys.stderr)
        return 2
    problems = [m for f in files for m in violations(f)]
    for message in problems:
        print(message, file=sys.stderr)
    if problems:
        print(f"check-action-pins: {len(problems)} unpinned action reference(s)", file=sys.stderr)
        return 1
    print(f"check-action-pins: {len(files)} file(s), every `uses:` is SHA-pinned")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
