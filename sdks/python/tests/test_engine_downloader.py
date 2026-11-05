"""Tests for engine_downloader: versioned cache and download verified against the shipped table."""

from __future__ import annotations

import base64
import hashlib
import json
import os
import platform
import shutil
import socket
import socketserver
import ssl
import subprocess
import threading
import time
import urllib.request
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
        self.declared_length: int | None = None
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
                declared = len(body) if outer.declared_length is None else outer.declared_length
                self.send_header("Content-Length", str(declared))
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
            "subjectAltName=IP:127.0.0.1,DNS:release.test",
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
    # The host running the tests may sit behind a proxy; the default here is a direct connection.
    monkeypatch.setattr(urllib.request, "getproxies", lambda: {})
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


def test_download_engine_truncated_body_reports_truncation_not_hash_mismatch(
    isolated_engine_env: Path,
    fake_release: FakeRelease,
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    binary = b"engine that is cut short"
    _ship_table(tmp_path / "table.json", monkeypatch, binary)
    fake_release.declared_length = len(binary)
    fake_release.routes[fake_release.asset_path] = lambda: (200, {}, binary[:6])

    with pytest.raises(RuntimeError, match=r"Failed to download engine.*truncated download"):
        download_engine()

    assert cached_engine_path() is None


@pytest.mark.parametrize(
    "location",
    ["https://127.0.0.1:99999999/engine", "https://[::1/engine"],
    ids=["invalid-port", "bad-ipv6-literal"],
)
def test_download_engine_malformed_redirect_location_is_a_download_failure(
    isolated_engine_env: Path,
    fake_release: FakeRelease,
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
    location: str,
) -> None:
    _ship_table(tmp_path / "table.json", monkeypatch, b"engine")
    fake_release.routes[fake_release.asset_path] = lambda: (302, {"Location": location}, b"")

    with pytest.raises(RuntimeError, match=r"Failed to download engine from .*invalid"):
        download_engine()

    assert cached_engine_path() is None


# ── proxies ────────────────────────────────────────────────────────────


class ConnectProxy:
    """A CONNECT-only proxy: it refuses every other method, so a request that does not tunnel
    never reaches the release host. Resolves ``release.test`` to the local https server."""

    def __init__(self, upstream_port: int) -> None:
        self.connects: list[str] = []
        self.auth_headers: list[str | None] = []
        self.reject_with: int | None = None
        outer = self

        class Handler(socketserver.BaseRequestHandler):
            def handle(self) -> None:
                sock: socket.socket = self.request
                buf = b""
                while b"\r\n\r\n" not in buf:
                    chunk = sock.recv(4096)
                    if not chunk:
                        return
                    buf += chunk
                head = buf.split(b"\r\n\r\n")[0].decode("latin-1").split("\r\n")
                method, target, _ = head[0].split(" ")
                headers = {k.lower(): v for k, _, v in (line.partition(": ") for line in head[1:])}
                outer.auth_headers.append(headers.get("proxy-authorization"))
                if method != "CONNECT":
                    sock.sendall(b"HTTP/1.1 405 Method Not Allowed\r\nContent-Length: 0\r\n\r\n")
                    return
                outer.connects.append(target)
                if outer.reject_with is not None:
                    sock.sendall(
                        f"HTTP/1.1 {outer.reject_with} Denied\r\nContent-Length: 0\r\n\r\n".encode()
                    )
                    return
                upstream = socket.create_connection(("127.0.0.1", upstream_port))
                sock.sendall(b"HTTP/1.1 200 Connection established\r\n\r\n")
                _relay(sock, upstream)

        class Server(socketserver.ThreadingTCPServer):
            allow_reuse_address = True
            daemon_threads = True

        self.server = Server(("127.0.0.1", 0), Handler)
        self.thread = threading.Thread(
            target=self.server.serve_forever, kwargs={"poll_interval": 0.01}, daemon=True
        )
        self.thread.start()

    @property
    def port(self) -> int:
        return int(self.server.server_address[1])

    def close(self) -> None:
        self.server.shutdown()
        self.server.server_close()


def _relay(a: socket.socket, b: socket.socket) -> None:
    def pump(src: socket.socket, dst: socket.socket) -> None:
        try:
            while data := src.recv(65536):
                dst.sendall(data)
        except OSError:
            pass
        finally:
            _shutdown_both(dst)

    forward = threading.Thread(target=pump, args=(a, b), daemon=True)
    forward.start()
    pump(b, a)
    forward.join(timeout=5)
    b.close()


def _shutdown_both(sock: socket.socket) -> None:
    try:
        sock.shutdown(socket.SHUT_WR)
    except OSError:
        pass


@pytest.fixture
def connect_proxy(fake_release: FakeRelease) -> Iterator[ConnectProxy]:
    proxy = ConnectProxy(fake_release.https.server_address[1])
    yield proxy
    proxy.close()


def _release_host_asset_url(fake_release: FakeRelease) -> str:
    return f"https://release.test:{fake_release.https.server_address[1]}/releases/download"


def test_download_engine_honours_https_proxy_and_verifies_the_target_end_to_end(
    isolated_engine_env: Path,
    fake_release: FakeRelease,
    connect_proxy: ConnectProxy,
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    binary = b"engine fetched through a proxy"
    _ship_table(tmp_path / "table.json", monkeypatch, binary)
    _serve(fake_release, binary)
    # release.test does not resolve: only a CONNECT to the proxy can reach the server.
    monkeypatch.setattr(downloader, "_GITHUB_RELEASE_BASE", _release_host_asset_url(fake_release))
    proxy_url = f"http://127.0.0.1:{connect_proxy.port}"
    monkeypatch.setattr(urllib.request, "getproxies", lambda: {"https": proxy_url})
    monkeypatch.setattr(urllib.request, "proxy_bypass", lambda host: False)

    target = download_engine()

    assert target.read_bytes() == binary
    assert connect_proxy.connects == [f"release.test:{fake_release.https.server_address[1]}"]


def test_download_engine_proxy_bypass_connects_directly(
    isolated_engine_env: Path,
    fake_release: FakeRelease,
    connect_proxy: ConnectProxy,
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    binary = b"engine fetched directly"
    _ship_table(tmp_path / "table.json", monkeypatch, binary)
    _serve(fake_release, binary)
    proxy_url = f"http://127.0.0.1:{connect_proxy.port}"
    monkeypatch.setattr(urllib.request, "getproxies", lambda: {"https": proxy_url})
    monkeypatch.setattr(urllib.request, "proxy_bypass", lambda host: True)

    target = download_engine()

    assert target.read_bytes() == binary
    assert connect_proxy.connects == []


def test_download_engine_proxy_credentials_go_in_connect_header_not_in_errors(
    isolated_engine_env: Path,
    fake_release: FakeRelease,
    connect_proxy: ConnectProxy,
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    binary = b"engine"
    _ship_table(tmp_path / "table.json", monkeypatch, binary)
    _serve(fake_release, binary)
    monkeypatch.setattr(downloader, "_GITHUB_RELEASE_BASE", _release_host_asset_url(fake_release))
    proxy_url = f"http://builder:s3cr3t%2Fpw@127.0.0.1:{connect_proxy.port}"
    monkeypatch.setattr(urllib.request, "getproxies", lambda: {"https": proxy_url})
    monkeypatch.setattr(urllib.request, "proxy_bypass", lambda host: False)
    connect_proxy.reject_with = 407

    with pytest.raises(RuntimeError, match="Failed to download engine") as denied:
        download_engine()

    expected = "Basic " + base64.b64encode(b"builder:s3cr3t/pw").decode()
    assert connect_proxy.auth_headers == [expected]
    assert "s3cr3t" not in str(denied.value)
    assert "builder" not in str(denied.value)


def test_download_engine_unreachable_proxy_error_omits_proxy_credentials(
    isolated_engine_env: Path,
    fake_release: FakeRelease,
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    _ship_table(tmp_path / "table.json", monkeypatch, b"engine")
    with socket.socket() as probe:
        probe.bind(("127.0.0.1", 0))
        closed_port = probe.getsockname()[1]
    monkeypatch.setattr(
        urllib.request,
        "getproxies",
        lambda: {"https": f"http://builder:s3cr3t@127.0.0.1:{closed_port}"},
    )
    monkeypatch.setattr(urllib.request, "proxy_bypass", lambda host: False)

    with pytest.raises(RuntimeError, match="Failed to download engine") as failed:
        download_engine()

    assert "s3cr3t" not in str(failed.value)
