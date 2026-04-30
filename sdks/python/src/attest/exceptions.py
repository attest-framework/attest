"""Attest SDK exception hierarchy."""

from __future__ import annotations

from collections.abc import Sequence

from attest._proto.diagnostics import ProtocolDiagnostic


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


class ProtocolDesyncError(AttestError):
    """Raised when the engine reader has lost framing.

    The reader buffers protocol diagnostics in a ring; once the rate
    exceeds the configured threshold every pending request is failed
    with this exception and new requests are refused.

    Attributes:
        diagnostics: Snapshot of the diagnostic ring at the moment of
            desync detection. Newest diagnostic is last.
    """

    def __init__(
        self,
        message: str,
        diagnostics: Sequence[ProtocolDiagnostic] = (),
    ) -> None:
        super().__init__(message)
        self.diagnostics: tuple[ProtocolDiagnostic, ...] = tuple(diagnostics)
