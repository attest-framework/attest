"""Tests for JSON-RPC codec."""

from __future__ import annotations

import json

import pytest

from attest._proto.codec import (
    ProtocolError,
    decode_response,
    encode_request,
    extract_id,
    extract_result,
)


def test_encode_request_format() -> None:
    """Verify encoded request is compact NDJSON."""
    result = encode_request(1, "initialize", {"sdk_name": "test"})
    assert result.endswith(b"\n")
    parsed = json.loads(result)
    assert parsed["jsonrpc"] == "2.0"
    assert parsed["id"] == 1
    assert parsed["method"] == "initialize"
    assert parsed["params"]["sdk_name"] == "test"


def test_encode_request_no_pretty_print() -> None:
    """Wire format must be compact (no whitespace outside values)."""
    result = encode_request(1, "test", {})
    line = result.rstrip(b"\n")
    assert b"\n" not in line


def test_decode_response_success() -> None:
    """Decode a valid success response."""
    line = b'{"jsonrpc":"2.0","id":1,"result":{"engine_version":"0.1.0"}}\n'
    resp = decode_response(line)
    assert resp["id"] == 1
    assert resp["result"]["engine_version"] == "0.1.0"


def test_decode_response_error() -> None:
    """Decode an error response raises ProtocolError."""
    line = (
        json.dumps(
            {
                "jsonrpc": "2.0",
                "id": 1,
                "error": {
                    "code": 3003,
                    "message": "session error",
                    "data": {
                        "error_type": "SESSION_ERROR",
                        "retryable": False,
                        "detail": "Call initialize first",
                    },
                },
            }
        ).encode()
        + b"\n"
    )
    with pytest.raises(ProtocolError) as exc_info:
        decode_response(line)
    assert exc_info.value.code == 3003
    assert exc_info.value.data is not None
    assert exc_info.value.data.error_type == "SESSION_ERROR"


def test_decode_response_malformed_json() -> None:
    """Malformed JSON raises ValueError."""
    with pytest.raises(ValueError, match="malformed JSON"):
        decode_response(b"not json\n")


def test_decode_response_empty() -> None:
    """Empty line raises ValueError."""
    with pytest.raises(ValueError, match="empty response"):
        decode_response(b"\n")


def test_decode_response_invalid_version() -> None:
    """Wrong jsonrpc version raises ValueError."""
    line = b'{"jsonrpc":"1.0","id":1,"result":{}}\n'
    with pytest.raises(ValueError, match="invalid jsonrpc version"):
        decode_response(line)


def test_extract_result() -> None:
    """Extract result from decoded response."""
    resp: dict[str, object] = {"jsonrpc": "2.0", "id": 1, "result": {"key": "value"}}
    assert extract_result(resp) == {"key": "value"}


def test_extract_result_missing() -> None:
    """Missing result raises ValueError."""
    with pytest.raises(ValueError, match="missing 'result'"):
        extract_result({"jsonrpc": "2.0", "id": 1})


def test_extract_id() -> None:
    """Extract ID from response."""
    assert extract_id({"jsonrpc": "2.0", "id": 42, "result": {}}) == 42
