"""Provider-egress guard: fail in-process connections to known model-provider hosts.

Replay serves model responses from a local proxy, so a replayed test should never reach a
provider. With the guard on (``pytest --attest-egress=block`` or ``ATTEST_EGRESS=block``),
resolving a provider host name raises :class:`EgressBlockedError`; loopback and every other
host are untouched. The guard covers connections made by this process (the agent's own HTTP
clients); the engine subprocess is not affected.
"""

from __future__ import annotations

import socket
import threading
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
