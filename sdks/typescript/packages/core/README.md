# @attest-ai/core

The TypeScript SDK for [Attest](https://github.com/attest-framework/attest#readme): build traces, check agent output and tool calls, and communicate with the Attest engine. Deterministic checks run first; model-scored checks run last.

Current release: **1.1.1** (documentation patch on 1.1.0 "Hardened"). The test integration supports **Vitest 3, 4 and 5** (`>=3.0.0 <6`); use a Node version supported by your chosen Vitest release.

## Install

For Vitest, install the core SDK and its test integration at the same version:

<!-- snippet: skip reason="installs packages from npm (network)" -->
```bash
npm install -D "@attest-ai/core@>=1.1.0" "@attest-ai/vitest@>=1.1.0" "vitest@^4"
```

Attest runs a separate engine binary. The SDK discovers or downloads an engine matching its version; set `ATTEST_ENGINE_PATH` to use a matching local binary. See [configuration](https://attest-framework.github.io/sdk-guides/configuration/).

## Minimal Vitest test

Save as `agent.test.ts`. This example builds a trace without calling a model; in your own tests, build or capture a trace from your agent's run.

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

`attestExpect()` collects assertions; `await evaluate()` actually runs them and fails the test on a hard failure. Since 1.1.0, a chain that is never evaluated fails the test; evaluate again after adding assertions. A returned `not_evaluated` result skips the test by default; `{ context, strict: true }` makes it fail instead. Pass the test context as shown, including in `it.concurrent`.

## Learn more

- [Documentation](https://attest-framework.github.io/)
- [Project README](https://github.com/attest-framework/attest#readme)
- [Guides on GitHub](https://github.com/attest-framework/attest/tree/main/docs/guides)
- [TypeScript examples](https://github.com/attest-framework/attest/tree/main/sdks/typescript/examples)
