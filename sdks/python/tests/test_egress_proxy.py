"""Egress guard behind HTTP(S)_PROXY: the provider host appears only in the CONNECT line."""

from __future__ import annotations

import socket
import threading
from collections.abc import Iterator

import httpx
import openai
import pytest

from attest import egress


class RecordingProxy:
    """Loopback forward proxy that records request lines and refuses to forward anything."""

    def __init__(self) -> None:
        self.request_lines: list[str] = []
        self._server = socket.socket()
        self._server.bind(("127.0.0.1", 0))
        self._server.listen(8)
        self._thread = threading.Thread(target=self._serve, daemon=True)
        self._thread.start()

    @property
    def url(self) -> str:
        return f"http://127.0.0.1:{self._server.getsockname()[1]}"

    def _serve(self) -> None:
        while True:
            try:
                conn, _ = self._server.accept()
            except OSError:
                return
            with conn:
                conn.settimeout(2)
                try:
                    data = conn.recv(4096)
                    if data:
                        self.request_lines.append(
                            data.split(b"\r\n", 1)[0].decode("ascii", "replace")
                        )
                    conn.sendall(b"HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\n\r\n")
                except OSError:
                    pass

    def close(self) -> None:
        self._server.close()


def _caused_by_egress_block(exc: BaseException | None) -> bool:
    while exc is not None:
        if isinstance(exc, egress.EgressBlockedError):
            return True
        exc = exc.__cause__ or exc.__context__
    return False


@pytest.fixture
def proxy() -> Iterator[RecordingProxy]:
    p = RecordingProxy()
    yield p
    p.close()


@pytest.fixture
def guard() -> Iterator[None]:
    egress.install()
    yield
    egress.uninstall()


def test_openai_call_through_https_proxy_is_blocked_before_connect(
    guard: None, proxy: RecordingProxy, monkeypatch: pytest.MonkeyPatch
) -> None:
    monkeypatch.setenv("HTTPS_PROXY", proxy.url)
    monkeypatch.setenv("HTTP_PROXY", proxy.url)
    monkeypatch.delenv("NO_PROXY", raising=False)
    monkeypatch.delenv("no_proxy", raising=False)
    client = openai.OpenAI(api_key="sk-test-not-real", max_retries=0, timeout=3)

    with pytest.raises(openai.APIConnectionError) as info:
        client.models.list()

    assert _caused_by_egress_block(info.value)
    assert not any("api.openai.com" in line for line in proxy.request_lines)


def test_plain_http_proxied_provider_request_is_blocked(guard: None, proxy: RecordingProxy) -> None:
    with httpx.Client(proxy=proxy.url, timeout=3) as client:
        with pytest.raises(httpx.HTTPError) as info:
            client.get("http://api.groq.com/openai/v1/models")

    assert _caused_by_egress_block(info.value)
    assert proxy.request_lines == []


def test_proxied_non_provider_request_reaches_the_proxy(guard: None, proxy: RecordingProxy) -> None:
    with httpx.Client(proxy=proxy.url, timeout=3) as client:
        response = client.get("http://example.com/")

    assert response.status_code == 502
    assert proxy.request_lines == ["GET http://example.com/ HTTP/1.1"]


def test_guard_release_restores_socket_methods() -> None:
    names = ("send", "sendall", "recv", "recv_into")
    before = {n: getattr(socket.socket, n) for n in names}
    egress.install()
    egress.uninstall()

    assert {n: getattr(socket.socket, n) for n in names} == before


@pytest.mark.parametrize(
    "host",
    [
        "bedrock-runtime.us-east-1.amazonaws.com",
        "us-central1-aiplatform.googleapis.com",
        "aiplatform.googleapis.com",
        "acme.openai.azure.com",
        "api.mistral.ai",
        "api.groq.com",
        "openrouter.ai",
        "api.together.xyz",
    ],
)
def test_is_provider_host_covers_additional_providers(host: str) -> None:
    assert egress.is_provider_host(host)


@pytest.mark.parametrize(
    "host",
    [
        "s3.us-east-1.amazonaws.com",
        "bedrock.us-east-1.amazonaws.com",
        "googleapis.com",
        "evil.groq.com.example.org",
    ],
)
def test_is_provider_host_rejects_lookalikes(host: str) -> None:
    assert not egress.is_provider_host(host)


@pytest.mark.parametrize(
    ("data", "host"),
    [
        (b"CONNECT api.openai.com:443 HTTP/1.1\r\nHost: x\r\n\r\n", "api.openai.com"),
        (b"CONNECT [::1]:443 HTTP/1.1\r\n\r\n", "::1"),
        (b"POST http://openrouter.ai/api/v1/chat HTTP/1.1\r\n", "openrouter.ai"),
        (b"GET /v1/models HTTP/1.1\r\n", None),
        (b"\x16\x03\x01tls", None),
    ],
)
def test_proxied_target_host_parses_request_lines(data: bytes, host: str | None) -> None:
    assert egress.proxied_target_host(data) == host
