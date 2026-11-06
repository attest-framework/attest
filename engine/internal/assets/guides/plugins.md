# Plugin assertions

A plugin assertion (`expect(result).plugin("plugin-id")`) runs your own check in the SDK process and reports it through the same report, policy and baseline as the built-in layers. The engine never runs plugin code: the SDK runs the plugin and submits the result.

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
