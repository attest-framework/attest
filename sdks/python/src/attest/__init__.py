"""Attest — Test framework for AI agents."""

from __future__ import annotations

from attest._proto.types import (
    REASON_REPLAY_MISS,
    Assertion,
    AssertionResult,
    BiasProbe,
    ConversationMessage,
    DriftReport,
    JudgeAgreement,
    JudgeMetadata,
    SimulateFaultConfig,
    SimulatePersona,
    Step,
    Trace,
    TraceMetadata,
    Usage,
)
from attest.adapters._base import BaseAdapter, BaseProviderAdapter
from attest.adapters.anthropic import AnthropicAdapter
from attest.adapters.crewai import CrewAIAdapter
from attest.adapters.gemini import GeminiAdapter
from attest.adapters.google_adk import GoogleADKAdapter
from attest.adapters.langchain import LangChainAdapter, LangChainCallbackHandler
from attest.adapters.llamaindex import LlamaIndexInstrumentationHandler
from attest.adapters.manual import ManualAdapter
from attest.adapters.ollama import OllamaAdapter
from attest.adapters.openai import OpenAIAdapter
from attest.adapters.otel import OTelAdapter
from attest.agent import Agent, agent
from attest.capture import CaptureError, Fixture, load_fixture, record
from attest.config import config
from attest.continuous import AlertDispatcher, ContinuousEvalRunner, Sampler
from attest.delegate import delegate
from attest.egress import (
    EgressBlockedError,
    egress_block_requested,
    install_egress_guard,
    is_provider_host,
    uninstall_egress_guard,
)
from attest.exceptions import EngineTimeoutError
from attest.expect import ExpectChain, Related, expect, related
from attest.replay import (
    ReplayError,
    ReplayMissError,
    ReplayNotEvaluatedError,
    ReplaySession,
    ReplaySummary,
    first_miss,
    live_line,
    resolve_mode,
    start_replay,
)
from attest.result import AgentResult
from attest.tier import TIER_1, TIER_2, TIER_3, tier
from attest.trace import TraceBuilder
from attest.trace_tree import TraceTree
from attest.world import WorldError, WorldSession, start_world

__version__: str = "1.1.0"
ENGINE_VERSION: str = "1.1.0"

__all__ = [
    # Core types
    "Assertion",
    "AssertionResult",
    "BiasProbe",
    "JudgeAgreement",
    "JudgeMetadata",
    "Step",
    "Trace",
    "TraceMetadata",
    "Usage",
    # Configuration
    "config",
    # Agent
    "Agent",
    "agent",
    # Results
    "AgentResult",
    # Expect DSL
    "ExpectChain",
    "Related",
    "expect",
    "related",
    # Capture
    "CaptureError",
    "ReplayError",
    "ReplayMissError",
    "ReplayNotEvaluatedError",
    "ReplaySession",
    "ReplaySummary",
    "start_replay",
    "WorldError",
    "WorldSession",
    "start_world",
    "first_miss",
    "live_line",
    "resolve_mode",
    "REASON_REPLAY_MISS",
    "EgressBlockedError",
    "egress_block_requested",
    "install_egress_guard",
    "is_provider_host",
    "uninstall_egress_guard",
    "Fixture",
    "load_fixture",
    "record",
    # Trace building
    "TraceBuilder",
    # Multi-agent
    "TraceTree",
    "delegate",
    # Tier
    "tier",
    "TIER_1",
    "TIER_2",
    "TIER_3",
    # Adapter base classes
    "BaseAdapter",
    "BaseProviderAdapter",
    # Adapters
    "ManualAdapter",
    "OpenAIAdapter",
    "AnthropicAdapter",
    "GeminiAdapter",
    "GoogleADKAdapter",
    "OllamaAdapter",
    "OTelAdapter",
    "LangChainAdapter",
    "LangChainCallbackHandler",
    "LlamaIndexInstrumentationHandler",
    "CrewAIAdapter",
    # Continuous eval
    "ContinuousEvalRunner",
    "Sampler",
    "AlertDispatcher",
    # Drift detection
    "DriftReport",
    # Simulation types
    "ConversationMessage",
    "SimulatePersona",
    "SimulateFaultConfig",
    # Exceptions
    "EngineTimeoutError",
    # Version
    "__version__",
    "ENGINE_VERSION",
]
