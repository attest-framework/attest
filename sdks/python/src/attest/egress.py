"""Provider-egress guard: fail in-process connections to known model-provider hosts.

Replay serves model responses from a local proxy, so a replayed test should never reach a
provider. With the guard on (``pytest --attest-egress=block`` or ``ATTEST_EGRESS=block``),
resolving a provider host name raises :class:`EgressBlockedError`; loopback and every other
host are untouched. The guard covers connections made by this process (the agent's own HTTP
clients); the engine subprocess is not affected.
"""

from __future__ import annotations

import os
import socket
import threading
from collections.abc import Callable, Mapping
from typing import Any

PROVIDER_HOSTS = (
    "api.openai.com",
    "api.anthropic.com",
    "generativelanguage.googleapis.com",
)
PROVIDER_HOST_SUFFIXES = (".openai.azure.com",)


class EgressBlockedError(ConnectionError):
    """A connection to a model-provider host was refused by the egress guard."""


def is_provider_host(host: object) -> bool:
    """Whether ``host`` names a known model provider."""
    if isinstance(host, bytes):
        host = host.decode("ascii", "replace")
    if not isinstance(host, str):
        return False
    h = host.strip().rstrip(".").lower()
    return h in PROVIDER_HOSTS or h.endswith(PROVIDER_HOST_SUFFIXES)


def egress_block_requested(env: Mapping[str, str] | None = None) -> bool:
    """Whether ``ATTEST_EGRESS=block`` is set (``allow`` and unset are not blocking)."""
    value = (os.environ if env is None else env).get("ATTEST_EGRESS", "").strip().lower()
    if value not in ("", "block", "allow"):
        raise ValueError(f"ATTEST_EGRESS must be block or allow, got {value!r}")
    return value == "block"


_lock = threading.Lock()
_original: Any = None
_installs = 0


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
                raise EgressBlockedError(
                    f"attest egress guard: connection to {host} blocked "
                    "(--attest-egress=block / ATTEST_EGRESS=block); replay the fixture "
                    "or turn the guard off"
                )
            return real(host, *args, **kwargs)

        socket.getaddrinfo = guarded


def uninstall() -> None:
    """Release one :func:`install`; restore name resolution when none remain."""
    global _original, _installs
    with _lock:
        _installs = max(0, _installs - 1)
        if _original is not None and _installs == 0:
            socket.getaddrinfo = _original
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
