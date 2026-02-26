"""Attest SDK exception hierarchy."""

from __future__ import annotations


class AttestError(Exception):
    """Base class for all Attest SDK exceptions."""


class EngineTimeoutError(AttestError):
    """Raised when reading a response from the engine subprocess times out.

    Attributes:
        method: The JSON-RPC method name that triggered the timeout.
        timeout: The timeout duration in seconds.
    """

    def __init__(self, method: str, timeout: float) -> None:
        self.method = method
        self.timeout = timeout
        super().__init__(
            f"Engine did not respond to '{method}' within {timeout}s. "
            "Check that the engine process is healthy or increase "
            "ATTEST_ENGINE_TIMEOUT."
        )
