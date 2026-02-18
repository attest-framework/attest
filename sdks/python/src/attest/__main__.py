"""CLI entry point for Attest: python -m attest."""

from __future__ import annotations

import sys


def main() -> None:
    """Run attest CLI."""
    args = sys.argv[1:]

    if args and args[0] == "--version":
        from attest import __version__
        print(f"attest {__version__}")
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
