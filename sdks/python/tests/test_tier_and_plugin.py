"""Tests for tier decorator and pytest plugin extensions."""

from __future__ import annotations

import pytest

from attest.tier import TIER_1, TIER_2, TIER_3, tier


class TestTierConstants:
    """Tests for tier level constants."""

    def test_tier_1_value(self) -> None:
        assert TIER_1 == 1

    def test_tier_2_value(self) -> None:
        assert TIER_2 == 2

    def test_tier_3_value(self) -> None:
        assert TIER_3 == 3

    def test_constants_are_ints(self) -> None:
        assert isinstance(TIER_1, int)
        assert isinstance(TIER_2, int)
        assert isinstance(TIER_3, int)

    def test_tier_ordering(self) -> None:
        assert TIER_1 < TIER_2 < TIER_3


class TestTierDecorator:
    """Tests for the tier() decorator."""

    def test_sets_attest_tier_attribute(self) -> None:
        @tier(TIER_1)
        def test_fn() -> None:
            pass

        assert test_fn._attest_tier == TIER_1

    def test_tier_2_sets_attribute(self) -> None:
        @tier(TIER_2)
        def test_fn() -> None:
            pass

        assert test_fn._attest_tier == TIER_2

    def test_tier_3_sets_attribute(self) -> None:
        @tier(TIER_3)
        def test_fn() -> None:
            pass

        assert test_fn._attest_tier == TIER_3

    def test_decorated_function_still_callable(self) -> None:
        results: list[int] = []

        @tier(TIER_1)
        def test_fn() -> int:
            return 42

        results.append(test_fn())
        assert results[0] == 42

    def test_decorator_preserves_function_behavior(self) -> None:
        @tier(TIER_2)
        def add(x: int, y: int) -> int:
            return x + y

        assert add(3, 4) == 7

    def test_arbitrary_tier_level(self) -> None:
        @tier(99)
        def test_fn() -> None:
            pass

        assert test_fn._attest_tier == 99

    def test_functions_without_tier_have_no_attribute(self) -> None:
        def test_fn() -> None:
            pass

        assert not hasattr(test_fn, "_attest_tier")

    def test_tier_importable_from_attest_module(self) -> None:
        from attest import TIER_1 as T1
        from attest import TIER_2 as T2
        from attest import TIER_3 as T3
        from attest import tier as attest_tier

        assert attest_tier is tier
        assert T1 == 1
        assert T2 == 2
        assert T3 == 3


class TestPluginOptions:
    """Tests for plugin CLI option registration."""

    def test_attest_tier_option_registered(self, pytestconfig: pytest.Config) -> None:
        """--attest-tier option is registered by the plugin."""
        # The option is registered; verifying it can be retrieved with default
        val = pytestconfig.getoption("--attest-tier", default=None)
        # In normal test run without --attest-tier, it should be None
        assert val is None

    def test_attest_budget_option_registered(self, pytestconfig: pytest.Config) -> None:
        """--attest-budget option is registered by the plugin."""
        val = pytestconfig.getoption("--attest-budget", default=None)
        assert val is None

    def test_attest_cost_report_option_registered(self, pytestconfig: pytest.Config) -> None:
        """--attest-cost-report option is registered by the plugin."""
        val = pytestconfig.getoption("--attest-cost-report", default=False)
        assert val is False

    def test_attest_tier_marker_registered(self, pytestconfig: pytest.Config) -> None:
        """attest_tier marker is registered (with its parameters)."""
        raw_markers = [m for m in pytestconfig.getini("markers") if isinstance(m, str)]
        # The marker is registered as "attest_tier(level): ..." so check prefix
        assert any(m.startswith("attest_tier") for m in raw_markers)

    def test_attest_marker_still_registered(self, pytestconfig: pytest.Config) -> None:
        """Original attest marker is still registered."""
        marker_names = []
        for m in pytestconfig.getini("markers"):
            if isinstance(m, str):
                marker_names.append(m.split(":")[0].strip())
        assert "attest" in marker_names


class TestCollectionTierFilter:
    """Tests for pytest_collection_modifyitems tier filtering logic."""

    def test_tier_filter_keeps_untiered_tests(self) -> None:
        """Functions without _attest_tier are always included."""
        from unittest.mock import MagicMock

        item = MagicMock()
        item.function = MagicMock(spec=[])  # no _attest_tier attribute

        config = MagicMock()
        config.getoption.return_value = 1  # --attest-tier=1

        items = [item]
        deselected: list[MagicMock] = []

        def capture_deselected(items: list[MagicMock]) -> None:  # type: ignore[misc]
            deselected.extend(items)

        config.hook.pytest_deselected.side_effect = lambda items: deselected.extend(items)

        from attest.plugin import pytest_collection_modifyitems

        pytest_collection_modifyitems(config, items)

        assert item in items
        assert item not in deselected

    def test_tier_filter_keeps_matching_tier(self) -> None:
        """Functions with _attest_tier <= filter are kept."""
        from unittest.mock import MagicMock

        fn = MagicMock()
        fn._attest_tier = 1

        item = MagicMock()
        item.function = fn

        config = MagicMock()
        config.getoption.return_value = 2  # --attest-tier=2

        items = [item]
        config.hook.pytest_deselected.side_effect = lambda items: None

        from attest.plugin import pytest_collection_modifyitems

        pytest_collection_modifyitems(config, items)

        assert item in items

    def test_tier_filter_removes_higher_tier(self) -> None:
        """Functions with _attest_tier > filter are deselected."""
        from unittest.mock import MagicMock

        fn = MagicMock()
        fn._attest_tier = 3

        item = MagicMock()
        item.function = fn

        config = MagicMock()
        config.getoption.return_value = 1  # --attest-tier=1

        items = [item]
        deselected: list[MagicMock] = []
        config.hook.pytest_deselected.side_effect = lambda items: deselected.extend(items)

        from attest.plugin import pytest_collection_modifyitems

        pytest_collection_modifyitems(config, items)

        assert item not in items
        assert item in deselected

    def test_no_tier_filter_skips_modification(self) -> None:
        """When --attest-tier is None, no items are deselected."""
        from unittest.mock import MagicMock

        fn = MagicMock()
        fn._attest_tier = 1
        item = MagicMock()
        item.function = fn

        config = MagicMock()
        config.getoption.return_value = None  # no --attest-tier

        items = [item]
        from attest.plugin import pytest_collection_modifyitems

        pytest_collection_modifyitems(config, items)

        assert item in items
        config.hook.pytest_deselected.assert_not_called()
