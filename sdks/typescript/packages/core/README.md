# @attest-ai/core

The TypeScript SDK for [Attest](https://github.com/attest-framework/attest#readme):
trace building, the assertion DSL, and communication with the Attest engine.
Deterministic checks run first; model-scored checks run last.

## Install

For Vitest, install the core SDK and its test integration at the same version.
`next` selects the prerelease channel:

<!-- snippet: skip reason="installs packages from npm (network)" -->
```bash
npm install -D @attest-ai/core@next @attest-ai/vitest@next "vitest@^4"
```

Attest runs a separate engine binary. The SDK discovers or downloads an engine
matching its version; set `ATTEST_ENGINE_PATH` to use a matching local binary.
See [configuration](https://attest-framework.github.io/guides/configuration/).

## Minimal Vitest test

Save as `agent.test.ts`. This example builds a trace without calling a model;
in your own tests, build or capture a trace from your agent's run.

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
npx vitest run
```

`attestExpect()` collects assertions; `await evaluate()` actually runs them and
fails the test on a hard failure. An assertion that is not evaluated skips the
test by default; `{ context, strict: true }` makes it fail instead.

## Learn more

- [Documentation](https://attest-framework.github.io/)
- [Project README](https://github.com/attest-framework/attest#readme)
- [TypeScript examples](https://github.com/attest-framework/attest/tree/main/sdks/typescript/examples)
