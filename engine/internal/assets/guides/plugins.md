# Plugin assertions

A plugin assertion (`expect(result).plugin("plugin-id")`) runs your own check in the SDK process and reports it through the same report, policy and baseline as the built-in layers. The engine never runs plugin code: the SDK runs the plugin and submits the result.

**Status in 1.1.0: beta.** Use a plugin for a check that the built-in DSL cannot express.

## Minimal Python plugin

Register an instance in the pytest fixture's registry, then evaluate its assertion like any other check:

```python
from attest import TraceBuilder, expect
from attest.plugins import PluginResult

class RefundReference:
    name = "refund-reference"
    plugin_type = "assertion"

    def execute(self, trace, spec):
        passed = trace.output.get("reference", "").startswith("R-")
        return PluginResult(
            status="pass" if passed else "hard_fail",
            score=1.0 if passed else 0.0,
            explanation="reference must start with R-",
        )

def test_refund_reference(attest):
    attest.plugin_registry.register("refund-reference", RefundReference())
    trace = TraceBuilder("refund-agent").set_output(reference="R-1042").build()
    attest.evaluate(expect(trace).plugin("refund-reference"))
```

For automatic discovery, Python loads the `attest.plugins` entry-point group. TypeScript uses package metadata and `loadPackagePlugins`; see [TypeScript plugin discovery](simulation.md#plugins-in-typescript). Register only trusted code: plugins run in your SDK process, not in an engine sandbox.

## How a result reaches the report

For each plugin assertion of a batch the SDK:

1. runs the plugin and gets a status (`pass`, `soft_fail`, `hard_fail`), a score in [0, 1] and an explanation;
2. sends `submit_plugin_result` with the batch's `trace_id`, the assertion's `assertion_id` and the plugin name;
3. sends `evaluate_batch` with an assertion of `type: "plugin"` and `spec.plugin_id` equal to that plugin name.

The report row carries the submitted status, score and explanation. The rows below are the only other outcomes:

| Situation | Row |
|---|---|
| No result was submitted for the assertion | `not_evaluated`, reason `missing_evidence` |
| The result came from a different plugin name | `hard_fail` naming both |

A plugin row never short-circuits the batch. A plugin `hard_fail` does not stop embedding and judge assertions in the same batch, and those assertions do not hide the plugin row. Plugin rows are evaluated in simulation mode too, because the result is already computed. The wire-level details are in the protocol specification (`submit_plugin_result`).

## Baselines and policy

A plugin assertion has an id like any other and takes part in [baselines](baselines.md): removing it from a test blocks the merge unless the policy lists its id under `allow_removed`. `not_evaluated` plugin rows follow the policy's `not_evaluated` severity.
