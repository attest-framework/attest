"""Record the judge fixtures in ``fixtures/judge/``.

    uv run python -m tests.integration.record_judge_fixtures --scripted
    ATTEST_JUDGE_API_KEY=... ATTEST_JUDGE_MODEL=... \\
        uv run python -m tests.integration.record_judge_fixtures --real

``--scripted`` records against the scripted judge in ``judge_support`` (no key, no network).
``--real`` records against a real OpenAI-compatible endpoint (``ATTEST_JUDGE_BASE_URL``,
default OpenAI) and refuses to run without a key and a model. Either way the judge's
requests go through the capture proxy, so the fixtures hold what the engine sent.
"""

from __future__ import annotations

import argparse
import os
import sys
import tempfile
import urllib.request
from pathlib import Path

from attest import record
from tests.integration.judge_support import (
    FIXTURE_DIR,
    SCENARIOS,
    SCRIPTED_MODEL,
    judge_otlp,
    run_judge,
    serve_scripted_judge,
)

_OPENAI_DEFAULT_BASE = "https://api.openai.com/v1"


def _post_trace() -> None:
    import json

    req = urllib.request.Request(
        os.environ["OTEL_EXPORTER_OTLP_TRACES_ENDPOINT"],
        data=json.dumps(judge_otlp()).encode(),
        headers={"Content-Type": "application/json"},
    )
    urllib.request.urlopen(req).read()  # noqa: S310 (loopback)


def record_all(*, upstream: str, api_key: str, model: str, out_dir: Path) -> None:
    """Record every scenario's judge exchange into ``out_dir``.

    ``upstream`` is the OpenAI-compatible base URL the capture proxy forwards to. The
    engine's judge is pointed at the proxy for the duration of each capture.
    """
    out_dir.mkdir(parents=True, exist_ok=True)
    saved = {k: os.environ.get(k) for k in _ENV}
    try:
        for scenario in SCENARIOS:
            os.environ["OPENAI_BASE_URL"] = upstream  # the proxy's upstream
            with (
                tempfile.TemporaryDirectory() as cache,
                record(scenario.name, out_dir=out_dir, emit_test=False),
            ):
                os.environ.update(
                    ATTEST_JUDGE_PROVIDER="openai",
                    ATTEST_JUDGE_BASE_URL=os.environ["OPENAI_BASE_URL"],  # the proxy itself
                    ATTEST_JUDGE_API_KEY=api_key,
                    ATTEST_JUDGE_MODEL=model,
                )
                result = run_judge(scenario, cache_dir=Path(cache))
                _post_trace()
            status = result.assertion_results[0].status
            print(f"recorded {scenario.name}: judge verdict {status}")
    finally:
        for key, value in saved.items():
            if value is None:
                os.environ.pop(key, None)
            else:
                os.environ[key] = value


_ENV = (
    "OPENAI_BASE_URL",
    "ATTEST_JUDGE_PROVIDER",
    "ATTEST_JUDGE_BASE_URL",
    "ATTEST_JUDGE_API_KEY",
    "ATTEST_JUDGE_MODEL",
    "ATTEST_CACHE_DIR",
)


def main(argv: list[str]) -> int:
    parser = argparse.ArgumentParser(
        description=__doc__, formatter_class=argparse.RawTextHelpFormatter
    )
    mode = parser.add_mutually_exclusive_group(required=True)
    mode.add_argument("--scripted", action="store_true", help="record against the scripted judge")
    mode.add_argument(
        "--real", action="store_true", help="record against a real endpoint (needs a key)"
    )
    parser.add_argument("--out-dir", type=Path, default=FIXTURE_DIR)
    args = parser.parse_args(argv)

    if args.scripted:
        server = serve_scripted_judge()
        record_all(
            upstream=f"http://127.0.0.1:{server.server_address[1]}",
            api_key="scripted-unused",
            model=SCRIPTED_MODEL,
            out_dir=args.out_dir,
        )
        server.shutdown()
        return 0

    key = os.environ.get("ATTEST_JUDGE_API_KEY") or os.environ.get("OPENAI_API_KEY")
    model = os.environ.get("ATTEST_JUDGE_MODEL")
    if not key or not model:
        print(
            "record-judge-fixtures --real needs a provider key (ATTEST_JUDGE_API_KEY or "
            "OPENAI_API_KEY) and ATTEST_JUDGE_MODEL; nothing was recorded and no call was made",
            file=sys.stderr,
        )
        return 2
    record_all(
        upstream=os.environ.get("ATTEST_JUDGE_BASE_URL") or _OPENAI_DEFAULT_BASE,
        api_key=key,
        model=model,
        out_dir=args.out_dir,
    )
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
