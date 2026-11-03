# TypeScript parity

Every capability the Python SDK exposes has a TypeScript counterpart, or a documented exception. This page is generated from the public surface of both SDKs by the parity test (`sdks/python/tests/test_parity.py`); do not edit it by hand. Regenerate it with `ATTEST_UPDATE_PARITY=1` after changing either SDK's surface.

Python names map to TypeScript names by snake_case to camelCase, plus an explicit alias table. Capabilities that decide a test outcome (simulation scenarios, personas and fault injection, the L8 plugin runner and its discovery, `init`, capture, replay and world helpers, trials) are never exceptions. Commands other than `init` are forwarded to the engine by both SDKs, so they are identical.

Exceptions use one of three reasons: `upstream_python_only` (the framework has no TypeScript release), `otel_capture` (TypeScript users capture the framework through OpenTelemetry) or `language_idiom` (the TypeScript way is named in the note).

## Documented exceptions

| Python | Reason | TypeScript way |
| --- | --- | --- |
| `CrewAIAdapter` | `upstream_python_only` | CrewAI has no TypeScript release. |
| `GoogleADKAdapter` | `otel_capture` | Capture Google ADK runs through its OpenTelemetry spans with `attest capture`. |
| `LlamaIndexInstrumentationHandler` | `otel_capture` | Capture LlamaIndex runs through its OpenTelemetry spans with `attest capture`. |
| `BaseProviderAdapter` | `language_idiom` | TypeScript adapters implement the `TraceAdapter` interface directly; there is no template-method base class. |
| `attest` | `language_idiom` | Vitest selects Attest tests by file glob and `describe`; no marker is needed. |
| `integration` | `language_idiom` | Vitest splits integration tests with a separate include glob or `test:integration` script. |

## Adapters

| Python | TypeScript | Status |
| --- | --- | --- |
| `AnthropicAdapter` | `AnthropicAdapter` | match |
| `CrewAIAdapter` | - | exception (`upstream_python_only`) |
| `GeminiAdapter` | `GeminiAdapter` | match |
| `GoogleADKAdapter` | - | exception (`otel_capture`) |
| `LangChainAdapter` | `LangChainAdapter` | match |
| `LangChainCallbackHandler` | `LangChainAdapter` | alias |
| `LlamaIndexInstrumentationHandler` | - | exception (`otel_capture`) |
| `ManualAdapter` | `ManualAdapter` | match |
| `OTelAdapter` | `OTelAdapter` | match |
| `OllamaAdapter` | `OllamaAdapter` | match |
| `OpenAIAdapter` | `OpenAIAdapter` | match |

## Top-level exports

| Python | TypeScript | Status |
| --- | --- | --- |
| `Agent` | `Agent` | match |
| `AgentResult` | `AgentResult` | match |
| `AlertDispatcher` | `AlertDispatcher` | match |
| `Assertion` | `Assertion` | match |
| `AssertionResult` | `AssertionResult` | match |
| `BaseAdapter` | `TraceAdapter` | alias |
| `BaseProviderAdapter` | - | exception (`language_idiom`) |
| `BiasProbe` | `BiasProbe` | match |
| `CaptureError` | `CaptureError` | match |
| `ContinuousEvalRunner` | `ContinuousEvalRunner` | match |
| `ConversationMessage` | `ConversationMessage` | match |
| `DriftReport` | `DriftReport` | match |
| `ENGINE_VERSION` | `ENGINE_VERSION` | match |
| `EgressBlockedError` | `EgressBlockedError` | match |
| `EngineTimeoutError` | `EngineTimeoutError` | match |
| `ExpectChain` | `ExpectChain` | match |
| `Fixture` | `Fixture` | match |
| `JudgeAgreement` | `JudgeAgreement` | match |
| `JudgeMetadata` | `JudgeMetadata` | match |
| `REASON_REPLAY_MISS` | `REASON_REPLAY_MISS` | match |
| `ReplayError` | `ReplayError` | match |
| `ReplayMissError` | `ReplayMissError` | match |
| `ReplayNotEvaluatedError` | `ReplayNotEvaluatedError` | match |
| `ReplaySession` | `ReplaySession` | match |
| `ReplaySummary` | `ReplaySummary` | match |
| `Sampler` | `Sampler` | match |
| `SimulateFaultConfig` | `SimulateFaultConfig` | match |
| `SimulatePersona` | `SimulatePersona` | match |
| `Step` | `Step` | match |
| `TIER_1` | `TIER_1` | match |
| `TIER_2` | `TIER_2` | match |
| `TIER_3` | `TIER_3` | match |
| `Trace` | `Trace` | match |
| `TraceBuilder` | `TraceBuilder` | match |
| `TraceMetadata` | `TraceMetadata` | match |
| `TraceTree` | `TraceTree` | match |
| `Usage` | `Usage` | match |
| `WorldError` | `WorldError` | match |
| `WorldSession` | `WorldSession` | match |
| `agent` | `agent` | match |
| `config` | `config` | match |
| `delegate` | `delegate` | match |
| `egress_block_requested` | `egressBlockRequested` | match |
| `expect` | `attestExpect` | alias |
| `first_miss` | `firstMiss` | match |
| `install_egress_guard` | `installEgressGuard` | match |
| `is_provider_host` | `isProviderHost` | match |
| `live_line` | `liveLine` | match |
| `load_fixture` | `loadFixture` | match |
| `record` | `record` | match |
| `resolve_mode` | `resolveMode` | match |
| `start_replay` | `startReplay` | match |
| `start_world` | `startWorld` | match |
| `tier` | `tier` | match |
| `uninstall_egress_guard` | `uninstallEgressGuard` | match |

## ExpectChain methods

| Python | TypeScript | Status |
| --- | --- | --- |
| `agent_called` | `agentCalled` | match |
| `agent_ordered_before` | `agentOrderedBefore` | match |
| `agent_output_contains` | `agentOutputContains` | match |
| `agent_wall_time_under` | `agentWallTimeUnder` | match |
| `agents_overlap` | `agentsOverlap` | match |
| `aggregate_cost_under` | `aggregateCostUnder` | match |
| `aggregate_latency_under` | `aggregateLatencyUnder` | match |
| `aggregate_tokens_under` | `aggregateTokensUnder` | match |
| `all_tools_called` | `allToolsCalled` | match |
| `assertions` | `assertions` | match |
| `constraint` | `constraint` | match |
| `content_contains` | `contentContains` | match |
| `cost_under` | `costUnder` | match |
| `cross_agent_data_flow` | `crossAgentDataFlow` | match |
| `delegation_depth` | `delegationDepth` | match |
| `follows_transitions` | `followsTransitions` | match |
| `forbidden_tools` | `forbiddenTools` | match |
| `latency_under` | `latencyUnder` | match |
| `max_tool_failure_streak` | `maxToolFailureStreak` | match |
| `no_duplicate_tools` | `noDuplicateTools` | match |
| `no_tool_cycles` | `noToolCycles` | match |
| `no_tool_loops` | `noToolLoops` | match |
| `ordered_agents` | `orderedAgents` | match |
| `output_contains` | `outputContains` | match |
| `output_field_matches_schema` | `outputFieldMatchesSchema` | match |
| `output_forbids` | `outputForbids` | match |
| `output_has_all_keywords` | `outputHasAllKeywords` | match |
| `output_has_any_keyword` | `outputHasAnyKeyword` | match |
| `output_matches_regex` | `outputMatchesRegex` | match |
| `output_matches_schema` | `outputMatchesSchema` | match |
| `output_not_contains` | `outputNotContains` | match |
| `output_similar_to` | `outputSimilarTo` | match |
| `passes_judge` | `passesJudge` | match |
| `plugin` | `plugin` | match |
| `required_tools` | `requiredTools` | match |
| `step_count` | `stepCount` | match |
| `tokens_between` | `tokensBetween` | match |
| `tokens_under` | `tokensUnder` | match |
| `tool_arg_satisfies` | `toolArgSatisfies` | match |
| `tool_args_match_schema` | `toolArgsMatchSchema` | match |
| `tool_call_count` | `toolCallCount` | match |
| `tool_called_before` | `toolCalledBefore` | match |
| `tool_cardinality` | `toolCardinality` | match |
| `tool_result_matches_schema` | `toolResultMatchesSchema` | match |
| `tools_called_exactly` | `toolsCalledExactly` | match |
| `tools_called_in_order` | `toolsCalledInOrder` | match |
| `trace` | `trace` | match |
| `trajectory` | `trajectory` | match |
| `trajectory_partial_order` | `trajectoryPartialOrder` | match |
| `world` | `world` | match |

## `init` flags

| Python | TypeScript | Status |
| --- | --- | --- |
| `--ci` | `--ci` | match |
| `--json` | `--json` | match |
| `--mcp` | `--mcp` | match |
| `--skills` | `--skills` | match |

## Plugin runner

| Python | TypeScript | Status |
| --- | --- | --- |
| `AttestPlugin` | `AttestPlugin` | match |
| `PluginRegistry` | `PluginRegistry` | match |
| `PluginResult` | `PluginResult` | match |
| `execute_plugin_assertion` | `executePluginAssertion` | match |
| `load_entrypoint_plugins` | `loadPackagePlugins` | alias |
| `register_plugin` | `registerPlugin` | match |

## Simulation

| Python | TypeScript | Status |
| --- | --- | --- |
| `ADVERSARIAL_USER` | `ADVERSARIAL_USER` | match |
| `CONFUSED_USER` | `CONFUSED_USER` | match |
| `COOPERATIVE_USER` | `COOPERATIVE_USER` | match |
| `FRIENDLY_USER` | `FRIENDLY_USER` | match |
| `MockToolRegistry` | `MockToolRegistry` | match |
| `Persona` | `SimulatePersona` | alias |
| `RepeatResult` | `RepeatResult` | match |
| `ScenarioConfig` | `ScenarioConfig` | match |
| `ScenarioResult` | `ScenarioResult` | match |
| `configure` | `config` | alias |
| `fault_inject` | `faultInject` | match |
| `is_simulation_mode` | `isSimulationMode` | match |
| `mock_tool` | `mockTool` | match |
| `repeat` | `repeat` | match |
| `scenario` | `scenario` | match |

## Test-runner helpers

| Python | TypeScript | Status |
| --- | --- | --- |
| `attest` | - | exception (`language_idiom`) |
| `attest_budget` | `evaluate` | alias |
| `attest_engine` | `useAttest` | alias |
| `attest_min_pass_rate` | `trials` | alias |
| `attest_replay` | `useReplay` | alias |
| `attest_tier` | `filterByTier` | alias |
| `attest_world` | `useWorld` | alias |
| `integration` | - | exception (`language_idiom`) |
