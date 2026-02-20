"""CLI entry point for Attest: python -m attest."""

from __future__ import annotations

import json
import os
import sys
from pathlib import Path


def _cache_dir() -> Path:
    """Return the cache directory, respecting ATTEST_CACHE_DIR env override."""
    env_override = os.environ.get("ATTEST_CACHE_DIR")
    if env_override:
        return Path(env_override)
    return Path.home() / ".attest" / "cache"


def _cache_db_path() -> Path:
    """Return the path to the cache database file."""
    return _cache_dir() / "attest.db"


def _cmd_cache_stats() -> None:
    """Print JSON stats about the cache database."""
    db_path = _cache_db_path()
    exists = db_path.exists()
    file_size = db_path.stat().st_size if exists else 0
    stats = {
        "exists": exists,
        "file_size": file_size,
        "path": str(db_path),
    }
    print(json.dumps(stats))


def _cmd_cache_clear() -> None:
    """Delete the cache database file."""
    db_path = _cache_db_path()
    if db_path.exists():
        db_path.unlink()
        print(f"Cleared cache: {db_path}")
    else:
        print(f"No cache to clear: {db_path}")


def main() -> None:
    """Run attest CLI."""
    args = sys.argv[1:]

    if args and args[0] == "--version":
        from attest import __version__
        print(f"attest {__version__}")
        sys.exit(0)

    # `attest cache stats` and `attest cache clear`
    if len(args) >= 2 and args[0] == "cache":
        subcommand = args[1]
        if subcommand == "stats":
            _cmd_cache_stats()
            sys.exit(0)
        elif subcommand == "clear":
            _cmd_cache_clear()
            sys.exit(0)
        else:
            print(f"Unknown cache subcommand: {subcommand}", file=sys.stderr)
            print("Available: cache stats, cache clear", file=sys.stderr)
            sys.exit(1)

    if args and args[0] == "init":
        from attest.scaffold import scaffold_project
        scaffold_project(Path.cwd())
        sys.exit(0)

    if args and args[0] == "validate":
        from attest.scaffold import validate_suite
        validate_suite(Path.cwd())
        sys.exit(0)

    # `attest run [args]` — explicit alias for pytest passthrough
    if args and args[0] == "run":
        args = args[1:]

    # Default: run pytest with attest plugin loaded, pass through remaining args
    try:
        import pytest
    except ImportError:
        print("pytest is required. Install with: uv add pytest", file=sys.stderr)
        sys.exit(1)

    sys.exit(pytest.main(args))


if __name__ == "__main__":
    main()
