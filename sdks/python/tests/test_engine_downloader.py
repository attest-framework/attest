"""Tests for engine_downloader: versioned cache and download verified against the shipped table."""

from __future__ import annotations

import hashlib
import json
import os
import platform
import shutil
import ssl
import subprocess
import threading
import time
from collections.abc import Callable, Iterator
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from unittest.mock import patch

import pytest

from attest import ENGINE_VERSION, __version__
from attest import engine_downloader as downloader
from attest.engine_downloader import (
    _binary_filename,
    _platform_key,
    cached_engine_path,
    download_engine,
    versioned_bin_dir,
)

# ── _platform_key ──────────────────────────────────────────────────────


@patch("attest.engine_downloader.platform")
def test_platform_key_darwin_arm64(mock_platform: object) -> None:
    """Maps Darwin arm64 to darwin-arm64."""
    mock_platform.system = lambda: "Darwin"  # type: ignore[attr-defined]
    mock_platform.machine = lambda: "arm64"  # type: ignore[attr-defined]
    assert _platform_key() == "darwin-arm64"


@patch("attest.engine_downloader.platform")
def test_platform_key_linux_x86_64(mock_platform: object) -> None:
    """Maps Linux x86_64 to linux-amd64."""
    mock_platform.system = lambda: "Linux"  # type: ignore[attr-defined]
    mock_platform.machine = lambda: "x86_64"  # type: ignore[attr-defined]
    assert _platform_key() == "linux-amd64"


@patch("attest.engine_downloader.platform")
def test_platform_key_unsupported(mock_platform: object) -> None:
    """Raises RuntimeError for unsupported platform."""
    mock_platform.system = lambda: "FreeBSD"  # type: ignore[attr-defined]
    mock_platform.machine = lambda: "mips"  # type: ignore[attr-defined]
    with pytest.raises(RuntimeError, match="Unsupported platform"):
        _platform_key()


# ── _binary_filename ───────────────────────────────────────────────────


@patch("attest.engine_downloader.platform")
def test_binary_filename_unix(mock_platform: object) -> None:
    """Returns 'attest-engine' on non-Windows."""
    mock_platform.system = lambda: "Linux"  # type: ignore[attr-defined]
    assert _binary_filename() == "attest-engine"


@patch("attest.engine_downloader.platform")
def test_binary_filename_windows(mock_platform: object) -> None:
    """Returns 'attest-engine.exe' on Windows."""
    mock_platform.system = lambda: "Windows"  # type: ignore[attr-defined]
    assert _binary_filename() == "attest-engine.exe"


# ── versioned cache ────────────────────────────────────────────────────


def test_versioned_bin_dir_uses_canonical_engine_version(isolated_engine_env: Path) -> None:
    assert versioned_bin_dir() == isolated_engine_env / ".attest" / "bin" / ENGINE_VERSION


def test_cached_engine_path_missing_returns_none(isolated_engine_env: Path) -> None:
    assert cached_engine_path() is None


def test_cached_engine_path_ignores_legacy_unversioned_binary(isolated_engine_env: Path) -> None:
    legacy = isolated_engine_env / ".attest" / "bin" / _binary_filename()
    legacy.parent.mkdir(parents=True)
    legacy.write_bytes(b"old")

    assert cached_engine_path() is None


def test_cached_engine_path_present_returns_versioned_binary(isolated_engine_env: Path) -> None:
    binary = versioned_bin_dir() / _binary_filename()
    binary.parent.mkdir(parents=True)
    binary.write_bytes(b"engine")

    assert cached_engine_path() == binary


# ── download_engine ────────────────────────────────────────────────────

Response = tuple[int, dict[str, str], bytes]


class FakeRelease:
    """A local https release host (self-signed cert on 127.0.0.1) plus a plain-http host."""

    def __init__(self, cert: Path, key: Path) -> None:
        self.routes: dict[str, Callable[[], Response]] = {}
        self.hits: list[str] = []
        self.http_hits: list[str] = []
        self.release = threading.Event()
        self.trickle_delay = 0.0
        outer = self

        class Handler(BaseHTTPRequestHandler):
            hits_log = outer.hits

            def do_GET(self) -> None:
                self.hits_log.append(self.path)
                route = outer.routes.get(self.path)
                if route is None:
                    self.send_error(404)
                    return
                status, headers, body = route()
                self.send_response(status)
                for name, value in headers.items():
                    self.send_header(name, value)
                self.send_header("Content-Length", str(len(body)))
                self.end_headers()
                if outer.trickle_delay:
                    for i in range(len(body)):
                        self.wfile.write(body[i : i + 1])
                        self.wfile.flush()
                        time.sleep(outer.trickle_delay)
                    return
                self.wfile.write(body)

            def log_message(self, format: str, *args: object) -> None:
                return

        class PlainHandler(Handler):
            hits_log = outer.http_hits

        self.https = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        tls = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
        tls.load_cert_chain(cert, key)
        self.https.socket = tls.wrap_socket(self.https.socket, server_side=True)
        self.plain = ThreadingHTTPServer(("127.0.0.1", 0), PlainHandler)
        self.client_context = ssl.create_default_context(cafile=str(cert))
        self.threads = [
            threading.Thread(target=srv.serve_forever, kwargs={"poll_interval": 0.01}, daemon=True)
            for srv in (self.https, self.plain)
        ]
        for thread in self.threads:
            thread.start()

    @property
    def base(self) -> str:
        return f"https://127.0.0.1:{self.https.server_address[1]}/releases/download"

    @property
    def plain_base(self) -> str:
        return f"http://127.0.0.1:{self.plain.server_address[1]}"

    @property
    def asset_path(self) -> str:
        return f"/releases/download/v{ENGINE_VERSION}/{ASSET}"

    def close(self) -> None:
        self.release.set()
        for srv in (self.https, self.plain):
            srv.shutdown()
            srv.server_close()


# Resolved at import: isolated_engine_env empties PATH before fixtures run.
OPENSSL = shutil.which("openssl")

ASSET = f"attest-engine-{_platform_key()}" + (".exe" if platform.system() == "Windows" else "")


@pytest.fixture
def fake_release(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> Iterator[FakeRelease]:
    assert OPENSSL is not None, "the openssl CLI is required to mint the test certificate"
    cert, key = tmp_path / "cert.pem", tmp_path / "key.pem"
    subprocess.run(
        [
            OPENSSL,
            "req",
            "-x509",
            "-newkey",
            "rsa:2048",
            "-nodes",
            "-days",
            "1",
            "-subj",
            "/CN=127.0.0.1",
            "-addext",
            "subjectAltName=IP:127.0.0.1",
            "-keyout",
            str(key),
            "-out",
            str(cert),
        ],
        check=True,
        capture_output=True,
    )
    server = FakeRelease(cert, key)
    monkeypatch.setattr(downloader, "_GITHUB_RELEASE_BASE", server.base)
    monkeypatch.setattr(downloader, "_ssl_context", lambda: server.client_context)
    yield server
    server.close()


def _ship_table(path: Path, monkeypatch: pytest.MonkeyPatch, binary: bytes) -> None:
    digest = hashlib.sha256(binary).hexdigest()
    path.write_text(json.dumps({ENGINE_VERSION: {_platform_key(): digest}}))
    monkeypatch.setattr(downloader, "_TABLE_PATH", path)


def _serve(server: FakeRelease, binary: bytes) -> None:
    server.routes[server.asset_path] = lambda: (200, {}, binary)


def test_download_engine_writes_versioned_cache_and_keeps_legacy(
    isolated_engine_env: Path,
    fake_release: FakeRelease,
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    legacy = isolated_engine_env / ".attest" / "bin" / _binary_filename()
    legacy.parent.mkdir(parents=True)
    legacy.write_bytes(b"old engine")
    binary = b"new engine"
    _ship_table(tmp_path / "table.json", monkeypatch, binary)
    _serve(fake_release, binary)

    target = download_engine()

    assert target == versioned_bin_dir() / _binary_filename()
    assert target.read_bytes() == binary
    assert os.access(target, os.X_OK)
    assert legacy.read_bytes() == b"old engine"
    assert sorted(p.name for p in versioned_bin_dir().iterdir()) == [_binary_filename()]


def test_download_engine_modified_binary_refused_even_if_release_checksums_agree(
    isolated_engine_env: Path,
    fake_release: FakeRelease,
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    _ship_table(tmp_path / "table.json", monkeypatch, b"published")
    tampered = b"tampered"
    _serve(fake_release, tampered)
    fake_release.routes[f"/releases/download/v{ENGINE_VERSION}/checksums-sha256.txt"] = lambda: (
        200,
        {},
        f"{hashlib.sha256(tampered).hexdigest()}  {ASSET}\n".encode(),
    )

    with pytest.raises(
        RuntimeError, match=f"does not match the hash shipped in attest-ai {__version__}"
    ):
        download_engine()

    assert cached_engine_path() is None
    assert not any("checksums-sha256" in hit for hit in fake_release.hits)


def test_download_engine_https_to_http_redirect_refused(
    isolated_engine_env: Path,
    fake_release: FakeRelease,
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    binary = b"engine"
    _ship_table(tmp_path / "table.json", monkeypatch, binary)
    plain_url = f"{fake_release.plain_base}{fake_release.asset_path}"
    fake_release.routes[fake_release.asset_path] = lambda: (302, {"Location": plain_url}, b"")

    with pytest.raises(RuntimeError, match="non-https"):
        download_engine()

    assert fake_release.http_hits == []
    assert cached_engine_path() is None


def test_download_engine_relative_location_resolves_against_current_url(
    isolated_engine_env: Path,
    fake_release: FakeRelease,
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    binary = b"engine behind a relative redirect"
    _ship_table(tmp_path / "table.json", monkeypatch, binary)
    fake_release.routes[fake_release.asset_path] = lambda: (
        302,
        {"Location": "../blobs/engine-blob"},
        b"",
    )
    fake_release.routes["/releases/download/blobs/engine-blob"] = lambda: (200, {}, binary)

    target = download_engine()

    assert target.read_bytes() == binary
    assert fake_release.hits == [fake_release.asset_path, "/releases/download/blobs/engine-blob"]


def test_download_engine_stalled_server_fails_within_timeout(
    isolated_engine_env: Path,
    fake_release: FakeRelease,
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    _ship_table(tmp_path / "table.json", monkeypatch, b"engine")
    monkeypatch.setattr(downloader, "_CONNECT_TIMEOUT_S", 0.5)
    monkeypatch.setattr(downloader, "_TOTAL_TIMEOUT_S", 1.0)

    def stall() -> Response:
        fake_release.release.wait(30)
        return 200, {}, b"engine"

    fake_release.routes[fake_release.asset_path] = stall
    started = time.monotonic()

    with pytest.raises(RuntimeError, match="Failed to download engine"):
        download_engine()

    assert time.monotonic() - started < 5
    assert cached_engine_path() is None


def test_download_engine_source_install_without_table_names_engine_path_and_never_fetches(
    isolated_engine_env: Path,
    fake_release: FakeRelease,
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    monkeypatch.setattr(downloader, "_TABLE_PATH", tmp_path / "absent.json")
    _serve(fake_release, b"engine")

    with pytest.raises(RuntimeError, match="ATTEST_ENGINE_PATH") as excinfo:
        download_engine()

    assert "make engine" in str(excinfo.value)
    assert fake_release.hits == []


@pytest.mark.parametrize(
    "table",
    [
        {},
        {ENGINE_VERSION: {}},
        {ENGINE_VERSION: {_platform_key(): "not-a-sha256"}},
        ["not", "an", "object"],
    ],
)
def test_download_engine_unusable_table_raises_without_fetching(
    isolated_engine_env: Path,
    fake_release: FakeRelease,
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
    table: object,
) -> None:
    path = tmp_path / "table.json"
    path.write_text(json.dumps(table))
    monkeypatch.setattr(downloader, "_TABLE_PATH", path)

    with pytest.raises(RuntimeError, match="attest-ai"):
        download_engine()

    assert fake_release.hits == []


def test_download_engine_trickling_server_fails_within_total_timeout(
    isolated_engine_env: Path,
    fake_release: FakeRelease,
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    _ship_table(tmp_path / "table.json", monkeypatch, b"engine")
    monkeypatch.setattr(downloader, "_CONNECT_TIMEOUT_S", 0.5)
    monkeypatch.setattr(downloader, "_TOTAL_TIMEOUT_S", 1.0)

    def trickle() -> Response:
        # Headers promise 6 bytes; the body is sent one byte per 0.4s by the handler below.
        return 200, {"X-Trickle": "1"}, b"engine"

    fake_release.routes[fake_release.asset_path] = trickle
    fake_release.trickle_delay = 0.4
    started = time.monotonic()

    with pytest.raises(RuntimeError, match="Failed to download engine"):
        download_engine()

    assert time.monotonic() - started < 3
    assert cached_engine_path() is None
