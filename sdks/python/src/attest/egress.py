"""Provider-egress guard: fail in-process connections to known model-provider hosts.

Replay serves model responses from a local proxy, so a replayed test should never reach a
provider. With the guard on (``pytest --attest-egress=block`` or ``ATTEST_EGRESS=block``),
resolving a provider host name raises :class:`EgressBlockedError`; loopback and every other
host are untouched. The guard covers connections made by this process (the agent's own HTTP
clients), including calls tunnelled through ``HTTP(S)_PROXY`` (the CONNECT target is
checked, not just the proxy socket). Child processes are not covered: the engine subprocess
and anything the agent spawns keep their own network access.
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
# Socket methods wrapped to catch proxied requests, name -> original function.
_socket_originals: dict[str, Any] = {}
# Sockets that carried a blocked proxy request; their reads fail with the same error, because
# HTTP clients (httpcore) swallow a write error and would otherwise report a dropped connection.
_blocked_sockets: weakref.WeakKeyDictionary[socket.socket, str] = weakref.WeakKeyDictionary()


def _blocked(host: object) -> EgressBlockedError:
    return EgressBlockedError(
        f"attest egress guard: connection to {host} blocked "
        "(--attest-egress=block / ATTEST_EGRESS=block); replay the fixture "
        "or turn the guard off"
    )


# Longest first request line a socket may buffer before the guard refuses it.
_MAX_REQUEST_LINE = 8192
_OVERSIZED = "an oversized request line"
_METHOD_PREFIX = re.compile(rb"[A-Z]*")


class _Pending:
    """Outgoing bytes held back on a socket until its first request line is complete."""

    __slots__ = ("buffer",)

    def __init__(self) -> None:
        self.buffer = bytearray()


# Sockets whose first request line is not yet decided. The guard cannot tell which sockets
# lead to a proxy (HTTP(S)_PROXY, an explicit client ``proxy=`` option), so every socket's
# first write is screened; bytes that cannot be an HTTP request line pass straight through.
_pending: weakref.WeakKeyDictionary[socket.socket, _Pending] = weakref.WeakKeyDictionary()
# Sockets whose first request line was inspected and allowed; their writes pass untouched.
_cleared: weakref.WeakSet[socket.socket] = weakref.WeakSet()


def _could_be_request_line(buffer: bytes) -> bool | None:
    """``True`` when ``buffer`` can still become an HTTP request line, ``False`` when it
    cannot (so the connection speaks something else), ``None`` when more bytes are needed."""
    method = _METHOD_PREFIX.match(buffer)
    end = method.end() if method is not None else 0
    if end == 0 or end > 7:
        return False
    if end == len(buffer):
        return None
    return buffer[end : end + 1] == b" "


def _screen_write(sock: socket.socket, data: bytes) -> bytes | None:
    """Hold ``data`` until ``sock``'s first request line is complete, then decide.

    Returns ``None`` while the line is incomplete (the bytes are buffered and the caller
    reports them as sent), or every byte to forward once the line was allowed. Raises
    :class:`EgressBlockedError` when the line names a provider or outgrows the buffer bound.
    Splitting the line across writes (``CON`` + ``NECT api.openai.com:443``) therefore
    cannot hide the target.
    """
    pending = _pending.get(sock)
    if pending is None:
        if sock in _cleared:
            return data
        pending = _pending[sock] = _Pending()
    pending.buffer += data
    buffer = bytes(pending.buffer)
    line_end = buffer.find(b"\r\n")
    if line_end == -1 and len(buffer) > _MAX_REQUEST_LINE:
        _refuse_oversized(sock)
    if line_end == -1:
        if _could_be_request_line(buffer) is False:
            _release(sock, cleared=True)
            return buffer
        return None
    if line_end + 2 > _MAX_REQUEST_LINE:
        _refuse_oversized(sock)
    host = proxied_target_host(buffer)
    if host is not None and is_provider_host(host):
        _refuse(sock, host)
    _release(sock, cleared=True)
    return buffer


def _release(sock: socket.socket, *, cleared: bool) -> None:
    _pending.pop(sock, None)
    if cleared:
        _cleared.add(sock)


def _refuse(sock: socket.socket, target: object) -> None:
    _pending.pop(sock, None)
    _blocked_sockets[sock] = str(target)
    raise _blocked(target)


def _refuse_oversized(sock: socket.socket) -> None:
    _pending.pop(sock, None)
    _blocked_sockets[sock] = _OVERSIZED
    raise EgressBlockedError(
        f"attest egress guard: first request line exceeds {_MAX_REQUEST_LINE} bytes, "
        "so its target host cannot be checked (--attest-egress=block / ATTEST_EGRESS=block)"
    )


def _wrap_socket_method(name: str, is_write: bool) -> None:
    real = getattr(socket.socket, name)
    _socket_originals[name] = real

    if is_write:

        def guarded_write(self: socket.socket, data: Any, *args: Any) -> Any:
            blocked = _blocked_sockets.get(self)
            if blocked is not None:
                raise _blocked(blocked)
            if name == "sendmsg":
                flat = b"".join(bytes(part) for part in data)
            elif isinstance(data, (bytes, bytearray, memoryview)):
                flat = bytes(data)
            else:
                return real(self, data, *args)
            ready = _screen_write(self, flat)
            if ready is None:
                return len(flat)
            if ready is flat:
                return real(self, data, *args)
            # Earlier writes were held back and reported as sent: flush them with this one.
            real_sendall = _socket_originals["sendall"]
            real_sendall(self, ready)
            return len(flat)

        setattr(socket.socket, name, guarded_write)
        return

    def guarded_read(self: socket.socket, *args: Any) -> Any:
        host = _blocked_sockets.get(self)
        if host is not None:
            raise _blocked(host)
        return real(self, *args)

    setattr(socket.socket, name, guarded_read)


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
        # provider appears solely in the CONNECT / absolute-URI request line, so refuse the
        # write that would carry it. Not covered: child processes (subprocess, the engine),
        # SOCKS proxies, and a TLS-wrapped proxy connection, which hides the request line.
        for name in ("send", "sendall", "sendmsg"):
            _wrap_socket_method(name, is_write=True)
        for name in ("recv", "recv_into"):
            _wrap_socket_method(name, is_write=False)


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
            _pending.clear()
            _cleared.clear()
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
