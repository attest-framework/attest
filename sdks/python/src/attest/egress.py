"""Provider-egress guard: fail in-process connections to known model-provider hosts.

Replay serves model responses from a local proxy, so a replayed test should never reach a
provider. With the guard on (``pytest --attest-egress=block`` or ``ATTEST_EGRESS=block``),
resolving a provider host name raises :class:`EgressBlockedError`; loopback and every other
host are untouched. The guard covers connections made by this process (the agent's own HTTP
clients), including calls through the proxy named by ``HTTP(S)_PROXY`` / ``ALL_PROXY``: only
sockets whose connect target is that proxy are screened, request by request (every
``CONNECT`` target and absolute-URI host, plus ``Host``). After an allowed ``CONNECT`` the
guard goes opaque on a three-byte TLS record prefix (0x16 0x03 0x00-0x04); the tunnel itself
is not verified. Anything else (the proxy refused the CONNECT, for example with 407, and the
client sent another request, or the tunnel carries plain HTTP) returns to request framing
with full screening. A client that forges that prefix and then writes plaintext is not
inspected. The proxy's response is not read, so a non-HTTP, non-TLS protocol inside a tunnel
is refused. This is a best-effort guard for trusted test code, not an isolation boundary.
Every other socket is left alone.
Child processes are not covered: the engine subprocess and anything the agent spawns keep
their own network access.
"""

from __future__ import annotations

import os
import re
import socket
import threading
import weakref
from collections.abc import Callable, Mapping
from typing import Any
from urllib.parse import urlsplit

PROVIDER_HOSTS = (
    "api.openai.com",
    "api.anthropic.com",
    "generativelanguage.googleapis.com",
    "aiplatform.googleapis.com",
    "api.mistral.ai",
    "api.groq.com",
    "openrouter.ai",
    "api.together.xyz",
    "api.together.ai",
)
PROVIDER_HOST_SUFFIXES = (".openai.azure.com", "-aiplatform.googleapis.com")
# AWS Bedrock runtime: bedrock-runtime.<region>.amazonaws.com
_BEDROCK_HOST = re.compile(r"^bedrock-runtime\.[a-z0-9-]+\.amazonaws\.com$")
# First line of a request an HTTP client sends to a forward proxy: ``CONNECT host:port`` for
# TLS tunnels, or an absolute-URI request line (``GET http://host/path``) for plain HTTP.
_PROXY_REQUEST_LINE = re.compile(rb"^([A-Z]{3,7}) (\S{1,8192}) HTTP/1\.[01]\r\n")


class EgressBlockedError(ConnectionError):
    """A connection to a model-provider host was refused by the egress guard."""


def is_provider_host(host: object) -> bool:
    """Whether ``host`` names a known model provider."""
    if isinstance(host, bytes):
        host = host.decode("ascii", "replace")
    if not isinstance(host, str):
        return False
    h = host.strip().rstrip(".").lower()
    return (
        h in PROVIDER_HOSTS
        or h.endswith(PROVIDER_HOST_SUFFIXES)
        or _BEDROCK_HOST.match(h) is not None
    )


def proxied_target_host(data: object) -> str | None:
    """The origin host a forward-proxy request line names, or ``None`` for any other bytes.

    Reads only the request line of ``data``: the ``CONNECT`` authority or the absolute-URI
    host. A proxied provider call connects to the proxy, so the socket destination alone
    never names the provider.
    """
    if not isinstance(data, (bytes, bytearray, memoryview)):
        return None
    match = _PROXY_REQUEST_LINE.match(bytes(data[:8200]))
    if match is None:
        return None
    method, target = match.group(1), match.group(2).decode("ascii", "replace")
    if method == b"CONNECT":
        return urlsplit(f"//{target}").hostname
    if "://" in target:
        return urlsplit(target).hostname
    return None


def egress_block_requested(env: Mapping[str, str] | None = None) -> bool:
    """Whether ``ATTEST_EGRESS=block`` is set (``allow`` and unset are not blocking)."""
    value = (os.environ if env is None else env).get("ATTEST_EGRESS", "").strip().lower()
    if value not in ("", "block", "allow"):
        raise ValueError(f"ATTEST_EGRESS must be block or allow, got {value!r}")
    return value == "block"


_lock = threading.Lock()
_original: Any = None
_installs = 0
# Socket methods wrapped to screen proxy connections, name -> original function.
_socket_originals: dict[str, Any] = {}
# Sockets that carried a blocked proxy request; their reads fail with the same error, because
# HTTP clients (httpcore) swallow a write error and would otherwise report a dropped connection.
_blocked_sockets: weakref.WeakKeyDictionary[socket.socket, str] = weakref.WeakKeyDictionary()
_PROXY_ENV = ("HTTPS_PROXY", "HTTP_PROXY", "ALL_PROXY", "https_proxy", "http_proxy", "all_proxy")


def _blocked(host: object) -> EgressBlockedError:
    return EgressBlockedError(
        f"attest egress guard: connection to {host} blocked "
        "(--attest-egress=block / ATTEST_EGRESS=block); replay the fixture "
        "or turn the guard off"
    )


def _refusal(reason: str) -> EgressBlockedError:
    return EgressBlockedError(
        f"attest egress guard: proxy request refused ({reason}), so its target host cannot be "
        "checked (--attest-egress=block / ATTEST_EGRESS=block)"
    )


# Largest request head (request line plus headers) or chunk framing line a proxy socket may hold.
_MAX_HEAD = 64 * 1024
_METHOD_PREFIX = re.compile(rb"[A-Z]*")
_CHUNK_SIZE = re.compile(rb"^([0-9a-fA-F]{1,16})(?:[ \t]*;[^\r\n]*)?$")
_HEADER_NAME = re.compile(rb"^[!#$%&'*+.^_`|~0-9A-Za-z-]+$")
_REQUEST_LINE = re.compile(rb"^([A-Z]{3,7}) (\S+) HTTP/1\.[01]$")


def _could_be_request_line(buffer: bytes) -> bool | None:
    """``True`` when ``buffer`` can still become an HTTP request line, ``False`` when it
    cannot, ``None`` when more bytes are needed."""
    method = _METHOD_PREFIX.match(buffer)
    end = method.end() if method is not None else 0
    if end == 0 or end > 7:
        return False
    if end == len(buffer):
        return None
    return buffer[end : end + 1] == b" "


def _tls_record_start(buffer: bytes) -> bool | None:
    """``True`` for a TLS record header (0x16 0x03 0x00-0x04), ``False`` when ``buffer`` cannot be
    one, ``None`` when more bytes are needed."""
    for index, byte in enumerate(buffer[:3]):
        if byte != (0x16, 0x03)[index] if index < 2 else byte > 0x04:
            return False
    return True if len(buffer) >= 3 else None


class _Framer:
    """Frames the HTTP/1.1 requests an application writes to a proxy socket.

    ``feed`` consumes the application's bytes and returns the bytes that may go on the wire.
    Only an incomplete request head (or chunk-size / trailer line) is held back; bodies are
    forwarded as they arrive. A request naming a provider, or framing the guard cannot
    follow, raises :class:`EgressBlockedError` and forwards nothing from that write.
    """

    HEAD, BODY, CHUNK_SIZE, CHUNK_DATA, CHUNK_END, TRAILER, TUNNEL, AWAIT_TUNNEL = range(8)

    def __init__(self) -> None:
        self.state = self.HEAD
        self.buffer = b""
        self.remaining = 0
        # Leading bytes of ``buffer`` already on the wire (after a partial ``send``).
        self.sent = 0

    def clone(self) -> _Framer:
        other = _Framer()
        other.assign(self)
        return other

    def assign(self, other: _Framer) -> None:
        self.state, self.buffer = other.state, other.buffer
        self.remaining, self.sent = other.remaining, other.sent

    def mark_sent(self) -> None:
        """The held bytes were already written by a partial ``send``."""
        self.sent = len(self.buffer)

    def feed(self, data: bytes) -> bytes:
        out = bytearray()
        self.buffer += data
        while self.buffer:
            released = self._step()
            if released is None:
                break
            out += released
        return bytes(out)

    def _take(self, count: int) -> bytes:
        taken, self.buffer = self.buffer[:count], self.buffer[count:]
        return taken

    def _release(self, unit: bytes) -> bytes:
        skipped, self.sent = min(self.sent, len(unit)), 0
        return unit[skipped:]

    def _line(self) -> bytes | None:
        end = self.buffer.find(b"\r\n")
        if end == -1:
            if len(self.buffer) > _MAX_HEAD:
                raise _refusal("a framing line over 64 KiB")
            return None
        return self._take(end + 2)

    def _step(self) -> bytes | None:
        if self.state == self.TUNNEL:
            return self._release(self._take(len(self.buffer)))
        if self.state == self.AWAIT_TUNNEL:
            starts_tls = _tls_record_start(self.buffer)
            if starts_tls is None:
                return None
            self.state = self.TUNNEL if starts_tls else self.HEAD
            return b""
        if self.state in (self.BODY, self.CHUNK_DATA):
            unit = self._take(self.remaining)
            self.remaining -= len(unit)
            if self.remaining == 0:
                self.state = self.HEAD if self.state == self.BODY else self.CHUNK_END
            return self._release(unit) if unit else b""
        if self.state == self.HEAD:
            return self._head()
        line = self._line()
        if line is None:
            return None
        if self.state == self.CHUNK_END:
            if line != b"\r\n":
                raise _refusal("a chunk not followed by CRLF")
            self.state = self.CHUNK_SIZE
        elif self.state == self.CHUNK_SIZE:
            size = _CHUNK_SIZE.match(line[:-2])
            if size is None:
                raise _refusal("a bad chunk size")
            self.remaining = int(size.group(1), 16)
            self.state = self.CHUNK_DATA if self.remaining else self.TRAILER
        elif line == b"\r\n":
            self.state = self.HEAD
        return self._release(line)

    def _head(self) -> bytes | None:
        end = self.buffer.find(b"\r\n\r\n")
        if end == -1:
            if len(self.buffer) > _MAX_HEAD:
                raise _refusal("a request head over 64 KiB")
            if _could_be_request_line(self.buffer) is False:
                raise _refusal("bytes that are not an HTTP request")
            return None
        if end + 4 > _MAX_HEAD:
            raise _refusal("a request head over 64 KiB")
        head = self._take(end + 4)
        self._screen(head[: end + 2])
        return self._release(head)

    def _screen(self, head: bytes) -> None:
        lines = head.split(b"\r\n")[:-1]
        request = _REQUEST_LINE.match(lines[0])
        if request is None:
            raise _refusal("a malformed request line")
        method, target = request.group(1), request.group(2).decode("ascii", "replace")
        headers: dict[bytes, list[bytes]] = {}
        for line in lines[1:]:
            name, colon, value = line.partition(b":")
            if not colon or _HEADER_NAME.match(name) is None:
                raise _refusal("a malformed header line")
            headers.setdefault(name.lower(), []).append(value.strip())
        authority = (
            urlsplit(f"//{target}").hostname if method == b"CONNECT" else urlsplit(target).hostname
        )
        hosts = [
            authority,
            *(
                urlsplit("//" + v.decode("ascii", "replace")).hostname
                for v in headers.get(b"host", [])
            ),
        ]
        for host in hosts:
            if host is not None and is_provider_host(host):
                raise _blocked(host)
        lengths, encodings = (
            headers.get(b"content-length", []),
            headers.get(b"transfer-encoding", []),
        )
        if lengths and encodings:
            raise _refusal("both Content-Length and Transfer-Encoding")
        if method == b"CONNECT":
            self.state = self.AWAIT_TUNNEL
        elif encodings:
            codings = [c.strip().lower() for v in encodings for c in v.split(b",")]
            if codings[-1] != b"chunked":
                raise _refusal("a Transfer-Encoding that does not end in chunked")
            self.state = self.CHUNK_SIZE
        elif lengths:
            if len(lengths) > 1 or not lengths[0].isdigit():
                raise _refusal("an invalid Content-Length")
            self.remaining = int(lengths[0])
            self.state = self.BODY if self.remaining else self.HEAD
        else:
            self.state = self.HEAD


# Sockets connected to a configured proxy, with the framer that screens their writes. A
# ``dup`` shares its original's framer: both write to the same byte stream.
_proxy_streams: weakref.WeakKeyDictionary[socket.socket, _Framer] = weakref.WeakKeyDictionary()


def _configured_proxies(env: Mapping[str, str]) -> list[tuple[str, int]]:
    """Host and port of every HTTP(S) proxy named in the environment (SOCKS is not screened)."""
    proxies = []
    for name in _PROXY_ENV:
        value = env.get(name, "").strip()
        if not value:
            continue
        parts = urlsplit(value if "://" in value else f"http://{value}")
        if parts.scheme not in ("http", "https") or parts.hostname is None:
            continue
        proxies.append((parts.hostname, parts.port or (443 if parts.scheme == "https" else 80)))
    return proxies


def _addresses(host: str, port: int) -> set[str]:
    try:
        infos = _original(host, port, type=socket.SOCK_STREAM)
    except OSError:
        # A name that does not resolve cannot be the peer of this connect.
        return set()
    return {str(info[4][0]).split("%")[0] for info in infos}


def _is_proxy_peer(address: Any) -> bool:
    """Whether a ``connect`` address is a configured proxy, by port and resolved address."""
    if not isinstance(address, tuple) or len(address) < 2 or not isinstance(address[0], str):
        return False
    host, port = address[0], address[1]
    candidates = [p for p in _configured_proxies(os.environ) if p[1] == port]
    if not candidates:
        return False
    peer = _addresses(host, port)
    return any(
        proxy_host == host or peer & _addresses(proxy_host, port) for proxy_host, _ in candidates
    )


def _refuse(sock: socket.socket, error: EgressBlockedError) -> None:
    _blocked_sockets[sock] = str(error)
    raise error


def _screened_write(
    sock: socket.socket, framer: _Framer, data: Any, flat: bytes, real: Any, args: Any
) -> Any:
    trial = framer.clone()
    try:
        out = trial.feed(flat)
    except EgressBlockedError as error:
        _refuse(sock, error)
    if out == flat:
        sent = real(sock, data, *args)
        if sent == len(flat):
            framer.assign(trial)
        else:
            framer.feed(flat[:sent])
            framer.mark_sent()
        return sent
    # Held-back bytes (an earlier partial head) go out with this write.
    _socket_originals["sendall"](sock, out)
    framer.assign(trial)
    return len(flat)


def _wrap_write(name: str) -> None:
    real = getattr(socket.socket, name)
    _socket_originals[name] = real

    def guarded_write(self: socket.socket, data: Any, *args: Any) -> Any:
        framer = _proxy_streams.get(self)
        if framer is None:
            return real(self, data, *args)
        blocked = _blocked_sockets.get(self)
        if blocked is not None:
            raise EgressBlockedError(blocked)
        if name == "sendmsg":
            flat = b"".join(bytes(part) for part in data)
        elif isinstance(data, (bytes, bytearray, memoryview)):
            flat = bytes(data)
        else:
            return real(self, data, *args)
        return _screened_write(self, framer, data, flat, real, args)

    setattr(socket.socket, name, guarded_write)


def _wrap_read(name: str) -> None:
    real = getattr(socket.socket, name)
    _socket_originals[name] = real

    def guarded_read(self: socket.socket, *args: Any) -> Any:
        message = _blocked_sockets.get(self)
        if message is not None:
            raise EgressBlockedError(message)
        return real(self, *args)

    setattr(socket.socket, name, guarded_read)


def _wrap_connect(name: str) -> None:
    real = getattr(socket.socket, name)
    _socket_originals[name] = real

    def guarded_connect(self: socket.socket, address: Any) -> Any:
        if _original is not None and self.type == socket.SOCK_STREAM and _is_proxy_peer(address):
            _proxy_streams[self] = _Framer()
        return real(self, address)

    setattr(socket.socket, name, guarded_connect)


def _wrap_sendfile() -> None:
    real = socket.socket.sendfile
    _socket_originals["sendfile"] = real

    def guarded_sendfile(
        self: socket.socket, file: Any, offset: int = 0, count: int | None = None
    ) -> int:
        if self not in _proxy_streams:
            return real(self, file, offset, count)
        # The OS sendfile path never calls ``send``; the stdlib read-and-send fallback does.
        return self._sendfile_use_send(file, offset, count)  # type: ignore[attr-defined, no-any-return]

    setattr(socket.socket, "sendfile", guarded_sendfile)


def _wrap_dup() -> None:
    real = socket.socket.dup
    _socket_originals["dup"] = real

    def guarded_dup(self: socket.socket) -> socket.socket:
        copy = real(self)
        framer = _proxy_streams.get(self)
        if framer is not None:
            _proxy_streams[copy] = framer
            message = _blocked_sockets.get(self)
            if message is not None:
                _blocked_sockets[copy] = message
        return copy

    setattr(socket.socket, "dup", guarded_dup)


def install() -> None:
    """Block provider host names for this process. Installs are counted: the guard comes
    off when every :func:`install` has been matched by an :func:`uninstall`."""
    global _original, _installs
    with _lock:
        _installs += 1
        if _original is not None:
            return
        _original = socket.getaddrinfo
        real = _original

        def guarded(host: Any, *args: Any, **kwargs: Any) -> Any:
            if is_provider_host(host):
                raise _blocked(host)
            return real(host, *args, **kwargs)

        socket.getaddrinfo = guarded
        # Behind HTTP(S)_PROXY the client resolves and connects to the proxy only; the
        # provider appears solely in the CONNECT / absolute-URI request lines, so connections
        # whose peer is a configured proxy are framed and every request on them screened.
        # Not covered: child processes (subprocess, the engine), SOCKS proxies, a TLS-wrapped
        # proxy connection (its bytes never reach ``send``), a client that names its proxy
        # only in code (``proxy=`` options the environment does not repeat), and writers that
        # bypass the socket object: ``os.write`` / ``os.sendfile`` on its fd,
        # ``socket.fromfd`` / ``socket.socket(fileno=)`` re-wraps, ``send_fds``.
        for name in ("connect", "connect_ex"):
            _wrap_connect(name)
        for name in ("send", "sendall", "sendmsg"):
            _wrap_write(name)
        for name in ("recv", "recv_into"):
            _wrap_read(name)
        _wrap_sendfile()
        _wrap_dup()


def uninstall() -> None:
    """Release one :func:`install`; restore name resolution when none remain."""
    global _original, _installs
    with _lock:
        _installs = max(0, _installs - 1)
        if _original is not None and _installs == 0:
            socket.getaddrinfo = _original
            for name, real in _socket_originals.items():
                setattr(socket.socket, name, real)
            _socket_originals.clear()
            _blocked_sockets.clear()
            _proxy_streams.clear()
            _original = None


def active() -> bool:
    """Whether the guard is currently installed."""
    return _original is not None


def install_egress_guard() -> Callable[[], None]:
    """:func:`install`, returning a function that releases only this install (once)."""
    install()
    released = False

    def release() -> None:
        nonlocal released
        if not released:
            released = True
            uninstall()

    return release


def uninstall_egress_guard() -> None:
    """Remove the guard now, whatever the install count."""
    global _installs
    with _lock:
        _installs = 1
    uninstall()
