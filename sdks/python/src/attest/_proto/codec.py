"""JSON-RPC 2.0 codec for Attest protocol communication."""

from __future__ import annotations

import json
from typing import Any

from attest._proto.types import ErrorData


class ProtocolError(Exception):
    """Raised when the engine returns a JSON-RPC error."""

    def __init__(self, code: int, message: str, data: ErrorData | None = None) -> None:
        super().__init__(message)
        self.code = code
        self.error_message = message
        self.data = data


def encode_request(request_id: int, method: str, params: dict[str, Any]) -> bytes:
    """Encode a JSON-RPC 2.0 request as NDJSON bytes.

    Returns compact JSON followed by newline, as bytes.
    """
    msg: dict[str, Any] = {
        "jsonrpc": "2.0",
        "id": request_id,
        "method": method,
        "params": params,
    }
    return json.dumps(msg, ensure_ascii=False, separators=(",", ":")).encode("utf-8") + b"\n"


def decode_response(line: bytes) -> dict[str, Any]:
    """Decode a JSON-RPC 2.0 response from NDJSON bytes.

    Returns the parsed response dict.
    Raises ValueError for malformed JSON.
    Raises ProtocolError if the response contains an error.
    """
    text = line.strip()
    if not text:
        raise ValueError("empty response line")

    try:
        data: Any = json.loads(text)
    except json.JSONDecodeError as e:
        raise ValueError(f"malformed JSON response: {e}") from e

    if not isinstance(data, dict):
        raise ValueError(f"expected JSON object, got {type(data).__name__}")

    if data.get("jsonrpc") != "2.0":
        raise ValueError(f"invalid jsonrpc version: {data.get('jsonrpc')}")

    if "error" in data and data["error"] is not None:
        err: dict[str, Any] = data["error"]
        error_data: ErrorData | None = None
        if "data" in err and err["data"] is not None:
            raw: dict[str, Any] = err["data"]
            error_data = ErrorData(
                error_type=raw.get("error_type", ""),
                retryable=raw.get("retryable", False),
                detail=raw.get("detail", ""),
            )
        raise ProtocolError(
            code=err.get("code", -1),
            message=err.get("message", "unknown error"),
            data=error_data,
        )

    return data


def extract_result(response: dict[str, Any]) -> Any:
    """Extract the result field from a decoded response."""
    if "result" not in response:
        raise ValueError("response missing 'result' field")
    return response["result"]


def extract_id(response: dict[str, Any]) -> int:
    """Extract the request ID from a decoded response."""
    if "id" not in response:
        raise ValueError("response missing 'id' field")
    return int(response["id"])
