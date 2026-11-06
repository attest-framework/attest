"""Egress guard behind HTTP(S)_PROXY: the provider host appears only in the CONNECT line."""

from __future__ import annotations

import socket
import ssl
import subprocess
import threading
from collections.abc import Iterator
from pathlib import Path

import httpx
import openai
import pytest

from attest import egress


class RecordingProxy:
    """Loopback forward proxy that records request lines and refuses to forward anything."""

    def __init__(self) -> None:
        self.request_lines: list[str] = []
        self.raw_requests: list[bytes] = []
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
                    data = b""
                    while b"\r\n\r\n" not in data:
                        chunk = conn.recv(4096)
                        if not chunk:
                            break
                        data += chunk
                    if data:
                        self.raw_requests.append(data)
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


def test_plain_http_proxied_provider_request_is_blocked(
    guard: None, proxied_env: RecordingProxy
) -> None:
    proxy = proxied_env
    with httpx.Client(timeout=3) as client:
        with pytest.raises(httpx.HTTPError) as info:
            client.get("http://api.groq.com/openai/v1/models")

    assert _caused_by_egress_block(info.value)
    assert proxy.request_lines == []


def test_proxied_non_provider_request_reaches_the_proxy(
    guard: None, proxied_env: RecordingProxy
) -> None:
    proxy = proxied_env
    with httpx.Client(timeout=3) as client:
        response = client.get("http://example.com/")

    assert response.status_code == 502
    assert proxy.request_lines == ["GET http://example.com/ HTTP/1.1"]


def test_guard_release_restores_socket_methods() -> None:
    names = (
        "connect",
        "connect_ex",
        "dup",
        "send",
        "sendall",
        "sendmsg",
        "sendfile",
        "recv",
        "recv_into",
    )
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


def _proxy_port(proxy: RecordingProxy) -> int:
    return int(proxy.url.rsplit(":", 1)[1])


def _write_fragments(proxy: RecordingProxy, fragments: list[bytes]) -> None:
    """Write ``fragments`` to the proxy one ``send`` at a time, as a split client would."""
    with socket.create_connection(("127.0.0.1", _proxy_port(proxy)), timeout=3) as sock:
        for fragment in fragments:
            sock.sendall(fragment)
        sock.settimeout(1)
        try:
            sock.recv(4096)
        except (TimeoutError, OSError):
            pass


@pytest.fixture
def proxied_env(proxy: RecordingProxy, monkeypatch: pytest.MonkeyPatch) -> RecordingProxy:
    monkeypatch.setenv("HTTPS_PROXY", proxy.url)
    monkeypatch.setenv("HTTP_PROXY", proxy.url)
    monkeypatch.delenv("NO_PROXY", raising=False)
    monkeypatch.delenv("no_proxy", raising=False)
    return proxy


PROVIDER_CONNECT = b"CONNECT api.openai.com:443 HTTP/1.1\r\nHost: api.openai.com:443\r\n\r\n"


def test_split_connect_to_provider_is_blocked_before_the_proxy_sees_it(
    guard: None, proxied_env: RecordingProxy
) -> None:
    with pytest.raises(egress.EgressBlockedError):
        _write_fragments(proxied_env, [b"CON", PROVIDER_CONNECT[3:]])

    assert proxied_env.raw_requests == []


def test_byte_at_a_time_connect_to_provider_is_blocked(
    guard: None, proxied_env: RecordingProxy
) -> None:
    fragments = [PROVIDER_CONNECT[i : i + 1] for i in range(len(PROVIDER_CONNECT))]

    with pytest.raises(egress.EgressBlockedError):
        _write_fragments(proxied_env, fragments)

    assert proxied_env.raw_requests == []


def test_split_absolute_form_provider_get_is_blocked(
    guard: None, proxied_env: RecordingProxy
) -> None:
    request = b"GET http://api.openai.com/v1/models HTTP/1.1\r\nHost: api.openai.com\r\n\r\n"

    with pytest.raises(egress.EgressBlockedError):
        _write_fragments(proxied_env, [request[:5], request[5:30], request[30:]])

    assert proxied_env.raw_requests == []


def test_split_connect_via_sendmsg_to_provider_is_blocked(
    guard: None, proxied_env: RecordingProxy
) -> None:
    with socket.create_connection(("127.0.0.1", _proxy_port(proxied_env)), timeout=3) as sock:
        with pytest.raises(egress.EgressBlockedError):
            sock.sendmsg([b"CON", PROVIDER_CONNECT[3:]])

    assert proxied_env.raw_requests == []


def test_split_connect_to_non_provider_reaches_the_proxy_intact(
    guard: None, proxied_env: RecordingProxy
) -> None:
    request = b"CONNECT example.com:443 HTTP/1.1\r\nHost: example.com:443\r\n\r\n"

    _write_fragments(proxied_env, [b"CON", request[3:10], request[10:]])

    assert proxied_env.raw_requests == [request]


def test_oversized_proxy_request_line_is_refused(guard: None, proxied_env: RecordingProxy) -> None:
    with pytest.raises(egress.EgressBlockedError):
        _write_fragments(proxied_env, [b"GET http://example.com/" + b"a" * 70_000])

    assert proxied_env.raw_requests == []


def test_non_http_bytes_to_a_proxy_socket_are_refused(
    guard: None, proxied_env: RecordingProxy
) -> None:
    with socket.create_connection(("127.0.0.1", _proxy_port(proxied_env)), timeout=3) as sock:
        with pytest.raises(egress.EgressBlockedError):
            sock.send(b"\x05\x01\x00")


def test_no_proxy_provider_host_connects_directly_and_is_blocked(
    guard: None, proxied_env: RecordingProxy, monkeypatch: pytest.MonkeyPatch
) -> None:
    monkeypatch.setenv("NO_PROXY", "api.openai.com")
    client = openai.OpenAI(api_key="sk-test-not-real", max_retries=0, timeout=3)

    with pytest.raises(openai.APIConnectionError) as info:
        client.models.list()

    assert _caused_by_egress_block(info.value)
    assert proxied_env.raw_requests == []


def test_long_absolute_form_provider_request_line_is_blocked(
    guard: None, proxied_env: RecordingProxy
) -> None:
    request = b"GET http://api.openai.com/" + b"a" * 3000 + b" HTTP/1.1\r\n\r\n"

    with pytest.raises(egress.EgressBlockedError):
        _write_fragments(proxied_env, [request])

    assert proxied_env.raw_requests == []


class ByteSink:
    """Loopback server that records every byte each connection sends, until it closes."""

    def __init__(self) -> None:
        self.received = bytearray()
        self._closed = threading.Semaphore(0)
        self._server = socket.socket()
        self._server.bind(("127.0.0.1", 0))
        self._server.listen(8)
        threading.Thread(target=self._serve, daemon=True).start()

    @property
    def url(self) -> str:
        return f"http://127.0.0.1:{self.port}"

    @property
    def port(self) -> int:
        return int(self._server.getsockname()[1])

    def _serve(self) -> None:
        while True:
            try:
                conn, _ = self._server.accept()
            except OSError:
                return
            threading.Thread(target=self._drain, args=(conn,), daemon=True).start()

    def _drain(self, conn: socket.socket) -> None:
        with conn:
            conn.settimeout(3)
            try:
                while chunk := conn.recv(65536):
                    self.received += chunk
            except OSError:
                pass
        self._closed.release()

    def wait_closed(self) -> bytes:
        assert self._closed.acquire(timeout=5), "sink connection never closed"
        return bytes(self.received)

    def close(self) -> None:
        self._server.close()


@pytest.fixture
def sink(monkeypatch: pytest.MonkeyPatch) -> Iterator[ByteSink]:
    s = ByteSink()
    monkeypatch.setenv("HTTPS_PROXY", s.url)
    monkeypatch.setenv("HTTP_PROXY", s.url)
    monkeypatch.delenv("NO_PROXY", raising=False)
    monkeypatch.delenv("no_proxy", raising=False)
    yield s
    s.close()


ALLOWED_GET = b"GET http://example.com/ HTTP/1.1\r\nHost: example.com\r\n\r\n"
PROVIDER_GET = b"GET http://api.openai.com/v1/models HTTP/1.1\r\nHost: api.openai.com\r\n\r\n"


def _connect(sink: ByteSink) -> socket.socket:
    return socket.create_connection(("127.0.0.1", sink.port), timeout=3)


def test_pipelined_provider_request_after_allowed_one_is_blocked(
    guard: None, sink: ByteSink
) -> None:
    sock = _connect(sink)
    with pytest.raises(egress.EgressBlockedError):
        sock.sendall(ALLOWED_GET + PROVIDER_GET)
    sock.close()

    assert sink.wait_closed() == b""


def test_keep_alive_provider_request_in_a_second_write_is_blocked(
    guard: None, sink: ByteSink
) -> None:
    sock = _connect(sink)
    sock.sendall(ALLOWED_GET)
    with pytest.raises(egress.EgressBlockedError):
        sock.sendall(PROVIDER_GET)
    sock.close()

    assert sink.wait_closed() == ALLOWED_GET


@pytest.mark.parametrize("split", [False, True])
def test_provider_text_inside_a_content_length_body_is_not_blocked(
    guard: None, sink: ByteSink, split: bool
) -> None:
    body = b"note: GET http://api.openai.com/ HTTP/1.1\r\nHost: api.openai.com\r\n\r\n"
    head = (
        b"POST http://example.com/ HTTP/1.1\r\nHost: example.com\r\n"
        + f"Content-Length: {len(body)}\r\n\r\n".encode()
    )
    sock = _connect(sink)
    if split:
        sock.sendall(head)
        sock.sendall(body[:10])
        sock.sendall(body[10:])
    else:
        sock.sendall(head + body)
    sock.sendall(ALLOWED_GET)
    sock.close()

    assert sink.wait_closed() == head + body + ALLOWED_GET


_CHUNK_TEXT = b"GET http://api.openai.com/ HTTP/1.1\r\n\r\nHost: x\r\n\r\n"
CHUNKED_POST = (
    b"POST http://example.com/ HTTP/1.1\r\nHost: example.com\r\n"
    b"Transfer-Encoding: chunked\r\n\r\n"
    b"5\r\nhello\r\n" + f"{len(_CHUNK_TEXT):x}".encode() + b"\r\n" + _CHUNK_TEXT + b"\r\n"
    b"0\r\nX-Trailer: GET http://api.openai.com/\r\n\r\n"
)


def test_provider_request_after_a_chunked_body_is_blocked(guard: None, sink: ByteSink) -> None:
    sock = _connect(sink)
    sock.sendall(CHUNKED_POST)
    with pytest.raises(egress.EgressBlockedError):
        sock.sendall(PROVIDER_GET)
    sock.close()

    assert sink.wait_closed() == CHUNKED_POST


def test_provider_request_pipelined_after_a_chunked_body_is_blocked(
    guard: None, sink: ByteSink
) -> None:
    sock = _connect(sink)
    with pytest.raises(egress.EgressBlockedError):
        sock.sendall(CHUNKED_POST + PROVIDER_GET)
    sock.close()

    assert sink.wait_closed() == b""


@pytest.mark.parametrize(
    "head",
    [
        b"Content-Length: 3\r\nTransfer-Encoding: chunked\r\n",
        b"Transfer-Encoding: chunked\r\n",
    ],
)
def test_ambiguous_framing_is_refused(guard: None, sink: ByteSink, head: bytes) -> None:
    request = b"POST http://example.com/ HTTP/1.1\r\nHost: example.com\r\n" + head + b"\r\n"
    request += b"zz\r\n" if b"Content-Length" not in head else b"abc"
    sock = _connect(sink)
    with pytest.raises(egress.EgressBlockedError):
        sock.sendall(request)
    sock.close()

    assert sink.wait_closed() == b""


def test_oversized_header_block_is_refused(guard: None, sink: ByteSink) -> None:
    request = ALLOWED_GET[:-2] + b"X-Pad: " + b"a" * 70_000 + b"\r\n\r\n"
    sock = _connect(sink)
    with pytest.raises(egress.EgressBlockedError):
        sock.sendall(request)
    sock.close()

    assert sink.wait_closed() == b""


def test_sendfile_connect_to_provider_is_blocked(
    guard: None, sink: ByteSink, tmp_path: Path
) -> None:
    payload = tmp_path / "connect.txt"
    payload.write_bytes(PROVIDER_CONNECT)
    sock = _connect(sink)
    with payload.open("rb") as handle, pytest.raises(egress.EgressBlockedError):
        sock.sendfile(handle)
    sock.close()

    assert sink.wait_closed() == b""


def test_sendfile_allowed_request_arrives_intact(
    guard: None, sink: ByteSink, tmp_path: Path
) -> None:
    payload = tmp_path / "get.txt"
    payload.write_bytes(ALLOWED_GET)
    sock = _connect(sink)
    with payload.open("rb") as handle:
        sent = sock.sendfile(handle)
    sock.close()

    assert sent == len(ALLOWED_GET)
    assert sink.wait_closed() == ALLOWED_GET


def test_dup_of_a_proxy_socket_is_screened(guard: None, sink: ByteSink) -> None:
    sock = _connect(sink)
    copy = sock.dup()
    sock.sendall(ALLOWED_GET)
    with pytest.raises(egress.EgressBlockedError):
        copy.sendall(PROVIDER_GET)
    copy.close()
    sock.close()

    assert sink.wait_closed() == ALLOWED_GET


def test_makefile_writer_on_a_proxy_socket_is_screened(guard: None, sink: ByteSink) -> None:
    sock = _connect(sink)
    writer = sock.makefile("wb", buffering=0)
    with pytest.raises(egress.EgressBlockedError):
        writer.write(PROVIDER_CONNECT)
    writer.close()
    sock.close()

    assert sink.wait_closed() == b""


def test_non_http_exchange_with_a_non_proxy_server_is_untouched(
    guard: None, sink: ByteSink
) -> None:
    server = socket.socket()
    server.bind(("127.0.0.1", 0))
    server.listen(1)

    def serve() -> None:
        conn, _ = server.accept()
        with conn:
            conn.settimeout(3)
            if conn.recv(16) == b"HELLO":
                conn.sendall(b"WORLD")

    thread = threading.Thread(target=serve, daemon=True)
    thread.start()
    with socket.create_connection(("127.0.0.1", server.getsockname()[1]), timeout=3) as sock:
        assert sock.send(b"HELLO") == 5
        assert sock.recv(16) == b"WORLD"
    thread.join(3)
    server.close()


class TunnelProxy:
    """Loopback CONNECT proxy that forwards the tunnel to the requested local port."""

    def __init__(self) -> None:
        self.connect_lines: list[bytes] = []
        self._server = socket.socket()
        self._server.bind(("127.0.0.1", 0))
        self._server.listen(4)
        threading.Thread(target=self._serve, daemon=True).start()

    @property
    def port(self) -> int:
        return int(self._server.getsockname()[1])

    def _serve(self) -> None:
        while True:
            try:
                client, _ = self._server.accept()
            except OSError:
                return
            threading.Thread(target=self._tunnel, args=(client,), daemon=True).start()

    def _tunnel(self, client: socket.socket) -> None:
        with client:
            head = b""
            while b"\r\n\r\n" not in head:
                head += client.recv(4096)
            line = head.split(b"\r\n", 1)[0]
            self.connect_lines.append(line)
            port = int(line.split()[1].rsplit(b":", 1)[1])
            with socket.create_connection(("127.0.0.1", port), timeout=3) as upstream:
                client.sendall(b"HTTP/1.1 200 Connection established\r\n\r\n")
                relay = threading.Thread(target=_pipe, args=(upstream, client), daemon=True)
                relay.start()
                _pipe(client, upstream)
                relay.join(3)

    def close(self) -> None:
        self._server.close()


def _pipe(source: socket.socket, target: socket.socket) -> None:
    try:
        while chunk := source.recv(65536):
            target.sendall(chunk)
        target.shutdown(socket.SHUT_WR)
    except OSError:
        pass


def test_allowed_connect_then_tls_through_the_tunnel_works(
    guard: None, tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    cert, key = tmp_path / "cert.pem", tmp_path / "key.pem"
    subprocess.run(
        [
            "openssl",
            "req",
            "-x509",
            "-newkey",
            "ec",
            "-pkeyopt",
            "ec_paramgen_curve:prime256v1",
            "-nodes",
            "-keyout",
            str(key),
            "-out",
            str(cert),
            "-days",
            "1",
            "-subj",
            "/CN=localhost",
        ],
        check=True,
        capture_output=True,
    )
    server_ctx = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
    server_ctx.load_cert_chain(cert, key)
    origin = socket.socket()
    origin.bind(("127.0.0.1", 0))
    origin.listen(1)

    def serve() -> None:
        conn, _ = origin.accept()
        with server_ctx.wrap_socket(conn, server_side=True) as tls:
            tls.sendall(b"echo:" + tls.recv(16))

    thread = threading.Thread(target=serve, daemon=True)
    thread.start()
    tunnel = TunnelProxy()
    monkeypatch.setenv("HTTPS_PROXY", f"http://127.0.0.1:{tunnel.port}")
    target_port = origin.getsockname()[1]
    client_ctx = ssl.SSLContext(ssl.PROTOCOL_TLS_CLIENT)
    client_ctx.load_verify_locations(cert)
    client_ctx.check_hostname = False

    with socket.create_connection(("127.0.0.1", tunnel.port), timeout=3) as raw:
        authority = f"localhost:{target_port}"
        raw.sendall(f"CONNECT {authority} HTTP/1.1\r\nHost: {authority}\r\n\r\n".encode())
        assert raw.recv(4096).startswith(b"HTTP/1.1 200")
        with client_ctx.wrap_socket(raw, server_hostname="localhost") as tls:
            tls.sendall(b"ping")
            assert tls.recv(16) == b"echo:ping"
    thread.join(3)
    origin.close()
    tunnel.close()

    assert tunnel.connect_lines == [f"CONNECT localhost:{target_port} HTTP/1.1".encode()]


def test_bytes_after_an_allowed_connect_are_opaque(guard: None, sink: ByteSink) -> None:
    sock = _connect(sink)
    connect = b"CONNECT example.com:443 HTTP/1.1\r\nHost: example.com:443\r\n\r\n"
    tunnelled = b"\x16\x03\x01" + PROVIDER_GET
    sock.sendall(connect)
    sock.sendall(tunnelled)
    sock.close()

    assert sink.wait_closed() == connect + tunnelled
