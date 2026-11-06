"""Assertion ids, id scheme 2 (contract C3): the shared golden vectors, lone-surrogate
rejection and occurrence counting."""

from __future__ import annotations

import hashlib
import json
from pathlib import Path
from typing import Any

import pytest

from attest._proto.types import Assertion
from attest.assertion_id import (
    AssertionIdError,
    OccurrenceCounter,
    canonical_input,
    canonical_spec,
    compute_assertion_id,
)

_VECTORS_FILE = (
    Path(__file__).resolve().parents[3] / "protocol-tests" / "ids" / "assertion-ids.json"
)
_DOC: dict[str, Any] = json.loads(_VECTORS_FILE.read_text(encoding="utf-8"))
_VECTORS: list[dict[str, Any]] = _DOC["vectors"]
_INVALID: list[dict[str, Any]] = _DOC.get("invalid", [])


def test_vectors_file_has_all_vectors() -> None:
    assert len(_VECTORS) == 35
    assert len(_INVALID) == 7


@pytest.mark.parametrize("vector", _VECTORS, ids=lambda v: v["name"])
def test_golden_vector_canonical_bytes_and_id_match(vector: dict[str, Any]) -> None:
    spec = json.loads(vector["spec_json"])

    text = canonical_input(
        vector["scope_id"], vector["assertion_type"], spec, vector["occurrence_index"]
    )

    identity_spec = canonical_spec(vector["assertion_type"], spec)
    assert identity_spec == vector["canonical_spec"]
    if "spec_hash" in vector:
        assert hashlib.sha256(identity_spec.encode("utf-8")).hexdigest() == vector["spec_hash"]
    assert text == vector["canonical_input"]
    assert text.encode("utf-8") == vector["canonical_input"].encode("utf-8")
    assert (
        compute_assertion_id(
            vector["scope_id"], vector["assertion_type"], spec, vector["occurrence_index"]
        )
        == vector["id"]
    )


def _invalid_input(vector: dict[str, Any]) -> tuple[str, str, dict[str, Any]]:
    """The scope, type and spec of an ``invalid`` vector; bytes given as hex are decoded as
    strict UTF-8, which is where invalid UTF-8 is refused (a Python str cannot hold it)."""
    scope = vector.get("scope_id")
    if scope is None:
        scope = bytes.fromhex(vector["scope_id_bytes_hex"]).decode("utf-8")
    spec_text = vector.get("spec_json")
    if spec_text is None:
        spec_text = bytes.fromhex(vector["spec_json_bytes_hex"]).decode("utf-8")
    return scope, vector["assertion_type"], json.loads(spec_text)


@pytest.mark.parametrize("vector", _INVALID, ids=lambda v: str(v["name"]))
def test_invalid_vector_is_rejected(vector: dict[str, Any]) -> None:
    with pytest.raises((AssertionIdError, UnicodeDecodeError)):
        scope, assertion_type, spec = _invalid_input(vector)
        compute_assertion_id(scope, assertion_type, spec, vector["occurrence_index"])


@pytest.mark.parametrize(
    ("scope", "assertion_type", "spec"),
    [
        ("tests/test_a.py::test_x", "output_contains", {"text": "a\ud800b"}),
        ("tests/test_a.py::test_x", "output_contains", {"a\udc00": 1}),
        ("tests/test_a.py::test_x", "output_contains", {"text": ["ok", {"deep": "\udfff"}]}),
        ("tests/test_a.py::test_\ud83d", "output_contains", {"text": "ok"}),
        ("tests/test_a.py::test_x", "output_\udc80contains", {"text": "ok"}),
    ],
    ids=["spec-value", "spec-key", "nested-value", "scope-id", "assertion-type"],
)
def test_lone_surrogate_is_rejected_naming_the_assertion(
    scope: str, assertion_type: str, spec: dict[str, Any]
) -> None:
    with pytest.raises(AssertionIdError, match="not valid Unicode"):
        compute_assertion_id(scope, assertion_type, spec, 0)


def test_lone_surrogate_error_names_the_assertion_type() -> None:
    counter = OccurrenceCounter("tests/test_a.py::test_x")
    assertion = Assertion(assertion_id="", type="output_contains", spec={"text": "\ud800"})

    with pytest.raises(AssertionIdError, match="output_contains"):
        counter.assign(assertion)


def test_valid_surrogate_pair_is_accepted() -> None:
    assert compute_assertion_id("s", "t", {"text": "\U0001f600"}, 0).startswith("a2_")


def test_non_finite_number_is_rejected() -> None:
    with pytest.raises(AssertionIdError, match="not finite"):
        compute_assertion_id("s", "t", {"max": float("nan")}, 0)


def test_assign_counts_identical_pairs_in_evaluation_order() -> None:
    counter = OccurrenceCounter("tests/test_a.py::test_x")
    first = Assertion(assertion_id="", type="output_contains", spec={"text": "ok"})
    other = Assertion(assertion_id="", type="output_contains", spec={"text": "no"})
    second = Assertion(assertion_id="", type="output_contains", spec={"text": "ok"})

    for assertion in (first, other, second):
        counter.assign(assertion)

    assert first.assertion_id == compute_assertion_id(
        "tests/test_a.py::test_x", "output_contains", {"text": "ok"}, 0
    )
    assert second.assertion_id == compute_assertion_id(
        "tests/test_a.py::test_x", "output_contains", {"text": "ok"}, 1
    )
    assert other.assertion_id == compute_assertion_id(
        "tests/test_a.py::test_x", "output_contains", {"text": "no"}, 0
    )
    assert first.scope_id == "tests/test_a.py::test_x"


def test_assign_reordering_distinct_assertions_keeps_every_id() -> None:
    def ids(order: list[str]) -> dict[str, str]:
        counter = OccurrenceCounter("s")
        out: dict[str, str] = {}
        for text in order:
            a = Assertion(assertion_id="", type="output_contains", spec={"text": text})
            counter.assign(a)
            out[text] = a.assertion_id
        return out

    assert ids(["a", "b", "c"]) == ids(["c", "a", "b"])


def test_assign_reset_restarts_the_counts() -> None:
    counter = OccurrenceCounter("s")
    a = Assertion(assertion_id="", type="t", spec={})
    b = Assertion(assertion_id="", type="t", spec={})

    counter.assign(a)
    counter.reset()
    counter.assign(b)

    assert a.assertion_id == b.assertion_id


def test_effect_world_is_evidence_only_without_mutating_wire_spec() -> None:
    baseline = {"predicate": {"world": {"expected": 1}}, "world": {"actual": 1}}
    changed = {**baseline, "world": {"actual": 2}}
    assert canonical_spec("effect", baseline) == canonical_spec("effect", changed)
    assert compute_assertion_id("s", "effect", baseline, 0) == compute_assertion_id(
        "s", "effect", changed, 0
    )
    assert baseline["world"] == {"actual": 1}
    assert changed["world"] == {"actual": 2}
    assert canonical_spec("effect", baseline) == '{"predicate":{"world":{"expected":1}}}'
    assert canonical_spec("effect", {"world": None}) == canonical_spec("effect", None)
    assert canonical_spec("other", baseline) != canonical_spec("other", changed)
    assert canonical_spec("effect", baseline) != canonical_spec(
        "effect", {**baseline, "predicate": {"world": {"expected": 2}}}
    )


def test_effect_occurrences_count_contract_not_world() -> None:
    counter = OccurrenceCounter("s")
    first = Assertion(assertion_id="", type="effect", spec={"world": {"actual": 1}})
    second = Assertion(assertion_id="", type="effect", spec={"world": {"actual": 2}})
    counter.assign(first)
    counter.assign(second)
    assert first.assertion_id == compute_assertion_id("s", "effect", {}, 0)
    assert second.assertion_id == compute_assertion_id("s", "effect", {}, 1)
    assert first.spec == {"world": {"actual": 1}}
    assert second.spec == {"world": {"actual": 2}}
    counter.reset()
    counter.assign(second)
    assert second.assertion_id == first.assertion_id


@pytest.mark.parametrize(
    "assertion_type",
    ["schema", "constraint", "trace", "content", "embedding", "llm_judge", "trace_tree", "plugin"],
)
def test_other_types_keep_top_level_world_in_identity(assertion_type: str) -> None:
    assert compute_assertion_id("s", assertion_type, {"world": {"value": 1}}, 0) != (
        compute_assertion_id("s", assertion_type, {"world": {"value": 2}}, 0)
    )


@pytest.mark.parametrize(
    ("field", "before", "after"),
    [
        ("check", "count", "sum"),
        ("resource", "refunds", "orders"),
        ("where", {"world": 1}, {"world": 2}),
        ("equals", 1, 2),
        ("soft", False, True),
        ("extension", {"world": 1}, {"world": 2}),
    ],
)
def test_effect_contract_fields_remain_identity(field: str, before: Any, after: Any) -> None:
    assert compute_assertion_id("s", "effect", {field: before, "world": {}}, 0) != (
        compute_assertion_id("s", "effect", {field: after, "world": {}}, 0)
    )


def test_excluded_effect_world_is_not_canonicalized() -> None:
    assert canonical_spec("effect", {"world": {"runtime": float("nan")}}) == "{}"
    with pytest.raises(AssertionIdError, match="not finite"):
        canonical_spec("effect", {"contract": {"world": float("nan")}})
