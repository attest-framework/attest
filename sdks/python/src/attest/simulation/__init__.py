from __future__ import annotations

from attest.config import config as configure
from attest.config import is_simulation_mode
from attest.simulation._context import _active_builder, _active_mock_registry
from attest.simulation.fault_inject import fault_inject
from attest.simulation.mock_tools import MockToolRegistry, mock_tool
from attest.simulation.personas import (
    ADVERSARIAL_USER,
    CONFUSED_USER,
    FRIENDLY_USER,
    Persona,
)
from attest.simulation.repeat import RepeatResult, repeat
from attest.simulation.scenario import ScenarioConfig, ScenarioResult, scenario

__all__ = [
    "configure",
    "is_simulation_mode",
    "Persona",
    "FRIENDLY_USER",
    "ADVERSARIAL_USER",
    "CONFUSED_USER",
    "MockToolRegistry",
    "mock_tool",
    "fault_inject",
    "RepeatResult",
    "repeat",
    "ScenarioConfig",
    "ScenarioResult",
    "scenario",
    "_active_mock_registry",
    "_active_builder",
]
