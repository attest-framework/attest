"""Assertion ids, id scheme 2 (contract C3; ``proto/attest/v1/protocol-spec.md``,
"Assertion ids (id scheme 2)").

``assertion_id`` is ``"a2_"`` plus the first 32 hex characters of SHA-256 over the RFC 8785
(JCS) array ``[scope_id, assertion_type, canonical_spec_value, occurrence_index]``. The scope is
the test node id, or the ``ContinuousEvalRunner`` name outside tests. The occurrence index counts
earlier assertions in the scope with the same ``(assertion_type, canonical_spec)``.
"""

from __future__ import annotations

import hashlib
import math
import threading
from decimal import Decimal
from typing import Any

from attest._proto.types import Assertion, _serialize

ID_SCHEME = 2
ID_PREFIX = "a2_"

_ESCAPES = {
    '"': '\\"',
    "\\": "\\\\",
    "\b": "\\b",
    "\t": "\\t",
    "\n": "\\n",
    "\f": "\\f",
    "\r": "\\r",
}
# ES6 Number::toString switches to exponent form at 1e21 and below 1e-6.
_MAX_PLAIN_EXPONENT = 21
_MIN_PLAIN_EXPONENT = -6
_MAX_SAFE_INT = 2**53


class AssertionIdError(ValueError):
    """An assertion cannot be given an id: a string in its scope, type or spec is not valid
    Unicode (a lone surrogate), or a number is not finite."""


def _string(value: str) -> str:
    try:
        value.encode("utf-8")
    except UnicodeEncodeError as exc:
        raise AssertionIdError(
            f"string {value!r} is not valid Unicode (lone surrogate); "
            "ids are computed over UTF-8 and the engine would reject it"
        ) from exc
    out: list[str] = ['"']
    for ch in value:
        escaped = _ESCAPES.get(ch)
        if escaped is not None:
            out.append(escaped)
        elif ch < " ":
            out.append(f"\\u{ord(ch):04x}")
        else:
            out.append(ch)
    out.append('"')
    return "".join(out)


def _number(value: int | float) -> str:
    """ECMAScript ``Number::toString`` of the double nearest ``value``."""
    if isinstance(value, int) and -_MAX_SAFE_INT <= value <= _MAX_SAFE_INT:
        return str(value)
    number = float(value)
    if not math.isfinite(number):
        raise AssertionIdError(f"number {value!r} is not finite; JSON cannot carry it")
    if number == 0:
        return "0"
    sign = "-" if number < 0 else ""
    _, digit_tuple, exponent = Decimal(repr(abs(number))).as_tuple()
    assert isinstance(exponent, int)
    digits = "".join(map(str, digit_tuple)).rstrip("0")
    point = len(digit_tuple) + exponent  # value = 0.<digits> * 10**point
    count = len(digits)
    if count <= point <= _MAX_PLAIN_EXPONENT:
        body = digits + "0" * (point - count)
    elif 0 < point <= _MAX_PLAIN_EXPONENT:
        body = digits[:point] + "." + digits[point:]
    elif _MIN_PLAIN_EXPONENT < point <= 0:
        body = "0." + "0" * -point + digits
    else:
        exp = point - 1
        mantissa = digits if count == 1 else digits[0] + "." + digits[1:]
        body = f"{mantissa}e{'+' if exp >= 0 else '-'}{abs(exp)}"
    return sign + body


def canonicalize(value: Any) -> str:
    """RFC 8785 (JCS) serialization of a JSON value.

    Raises:
        AssertionIdError: A string holds a lone surrogate, or a number is not finite.
        TypeError: The value holds something JSON cannot carry (or a non-string object key).
    """
    if value is None:
        return "null"
    if value is True:
        return "true"
    if value is False:
        return "false"
    if isinstance(value, (int, float)):
        return _number(value)
    if isinstance(value, str):
        return _string(value)
    if isinstance(value, (list, tuple)):
        return "[" + ",".join(canonicalize(v) for v in value) + "]"
    if isinstance(value, dict):
        for key in value:
            if not isinstance(key, str):
                raise TypeError(f"object key {key!r} is not a string")
        keys = sorted(value, key=lambda k: _utf16_key(k))
        return "{" + ",".join(f"{_string(k)}:{canonicalize(value[k])}" for k in keys) + "}"
    raise TypeError(f"{type(value).__name__} is not a JSON value")


def _utf16_key(key: str) -> bytes:
    try:
        return key.encode("utf-16-be")
    except UnicodeEncodeError as exc:
        raise AssertionIdError(
            f"key {key!r} is not valid Unicode (lone surrogate); "
            "ids are computed over UTF-8 and the engine would reject it"
        ) from exc


def canonical_spec(assertion_type: str, spec: dict[str, Any] | None) -> str:
    """The JCS identity spec, excluding only effect's top-level runtime world.

    The original spec remains untouched for wire serialization and validation.
    """
    identity_spec = {} if spec is None else spec
    if assertion_type == "effect":
        identity_spec = {key: value for key, value in identity_spec.items() if key != "world"}
    return canonicalize(_serialize(identity_spec))


def canonical_input(
    scope_id: str, assertion_type: str, spec: dict[str, Any] | None, occurrence_index: int
) -> str:
    """The JCS array ``[scope_id, assertion_type, spec, occurrence_index]`` that is hashed."""
    return (
        "["
        + ",".join(
            (
                _string(scope_id),
                _string(assertion_type),
                canonical_spec(assertion_type, spec),
                str(occurrence_index),
            )
        )
        + "]"
    )


def compute_assertion_id(
    scope_id: str, assertion_type: str, spec: dict[str, Any] | None, occurrence_index: int
) -> str:
    """The scheme-2 id of one assertion.

    Raises:
        AssertionIdError: The scope id, type or spec is not valid Unicode; the message names the
            assertion type.
    """
    try:
        text = canonical_input(scope_id, assertion_type, spec, occurrence_index)
    except AssertionIdError as exc:
        raise AssertionIdError(
            f"assertion {assertion_type!r} in scope {scope_id!r}: {exc}"
        ) from exc
    return ID_PREFIX + hashlib.sha256(text.encode("utf-8")).hexdigest()[:32]


class OccurrenceCounter:
    """Hands out ids in one scope, counting identical ``(type, canonical spec)`` pairs.

    ``reset`` restarts the counts (each trial of a repeated test is its own pass over the scope,
    so trial rows share ids).
    """

    def __init__(self, scope_id: str) -> None:
        self.scope_id = scope_id
        self._seen: dict[tuple[str, str], int] = {}
        self._lock = threading.Lock()

    def reset(self) -> None:
        with self._lock:
            self._seen.clear()

    def assign(self, assertion: Assertion) -> None:
        """Set ``assertion.assertion_id`` and ``assertion.scope_id`` in evaluation order."""
        try:
            key = (assertion.type, canonical_spec(assertion.type, assertion.spec))
        except AssertionIdError as exc:
            raise AssertionIdError(
                f"assertion {assertion.type!r} in scope {self.scope_id!r}: {exc}"
            ) from exc
        with self._lock:
            index = self._seen.get(key, 0)
            assertion_id = compute_assertion_id(
                self.scope_id, assertion.type, assertion.spec, index
            )
            self._seen[key] = index + 1
        assertion.assertion_id = assertion_id
        assertion.scope_id = self.scope_id
