# @attest-ai/vitest

The [Attest](https://github.com/attest-framework/attest#readme) integration for Vitest: turn agent assertion chains into test outcomes and write report-v2 JSON with the optional reporter. Use it with `@attest-ai/core` to test agent traces.

Current release: **1.1.1** (documentation patch on 1.1.0 "Hardened"). Supports **Vitest 3, 4 and 5** (`>=3.0.0 <6`); use a Node version supported by your chosen Vitest release.

## Install

Install the core SDK and test integration at the same version:

<!-- snippet: skip reason="installs packages from npm (network)" -->
```bash
npm install -D "@attest-ai/core@>=1.1.0" "@attest-ai/vitest@>=1.1.0" "vitest@^4"
```

Attest discovers or downloads an engine matching the SDK version. To use a matching local engine instead, set `ATTEST_ENGINE_PATH`. See [configuration](https://attest-framework.github.io/sdk-guides/configuration/).

## Minimal test

Save as `agent.test.ts`. The trace below is constructed locally; no model or provider key is needed.

```typescript
import { it } from "vitest";
import { attestExpect, TraceBuilder } from "@attest-ai/core";
import { evaluate } from "@attest-ai/vitest";

it("checks the refund response and tool call", async (context) => {
  const trace = new TraceBuilder("refund-agent")
    .addToolCall("issue_refund", {
      args: { order_id: "ORD-42" },
      result: { refund_id: "RFD-42" },
    })
    .setOutput({ message: "Refund RFD-42 issued." })
    .build();

  await evaluate(
    attestExpect(trace)
      .requiredTools(["issue_refund"])
      .outputContains("Refund"),
    { context },
  );
});
```

<!-- snippet: skip reason="Vitest CLI invocation; the TypeScript block is run by the snippet runner" -->
```bash
npx attest doctor
npx vitest run
```

Run `npx attest doctor` once before the first test run: it downloads and checks the engine. On a cold home directory, the download inside the first test can exceed Vitest's 5-second timeout.

Always `await evaluate()` after building a chain. Since 1.1.0, unevaluated chains fail tests, including isolated `it.concurrent` bodies; evaluate again after adding assertions. A hard failure fails the test; a returned `not_evaluated` result skips it by default. Pass `{ context, strict: true }` to fail instead of skip. Pass the test context as shown, including when using `it.concurrent`.

On Vitest <4.1, put concurrent assertions in the test body, not unscoped lifecycle hooks. See the [verdicts guide](https://github.com/attest-framework/attest/blob/main/docs/guides/verdicts.md) for lifecycle guard limits.

## JSON report

The optional reporter records test outcomes and Attest assertion results:

<!-- snippet: skip reason="Vitest CLI invocation requires a project containing tests" -->
```bash
npx vitest run --reporter=default --reporter=@attest-ai/vitest/reporter --outputFile.attest=attest-report.json
```

## Learn more

- [Documentation](https://attest-framework.github.io/)
- [Project README](https://github.com/attest-framework/attest#readme)
- [Guides on GitHub](https://github.com/attest-framework/attest/tree/main/docs/guides)
- [TypeScript examples](https://github.com/attest-framework/attest/tree/main/sdks/typescript/examples)
