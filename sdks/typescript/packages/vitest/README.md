# @attest-ai/vitest

The [Attest](https://github.com/attest-framework/attest#readme) integration for
Vitest: evaluate assertion chains as test outcomes and write report-v2 JSON
with the optional reporter. Use it with `@attest-ai/core` to test agent traces.

## Install

Install the core SDK and test integration at the same version. `next` selects
the prerelease channel:

<!-- snippet: skip reason="installs packages from npm (network)" -->
```bash
npm install -D @attest-ai/core@next @attest-ai/vitest@next "vitest@^4"
```

Attest discovers or downloads an engine matching the SDK version. To use a
matching local engine instead, set `ATTEST_ENGINE_PATH`. See
[configuration](https://attest-framework.github.io/guides/configuration/).

## Minimal test

Save as `agent.test.ts`. The trace below is constructed locally; no model or
provider key is needed.

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

Always `await evaluate()` after building a chain: collecting assertions does not
run them. A hard failure fails the test; a not-evaluated assertion skips it by
default. Pass `{ context, strict: true }` to fail instead of skip. Pass the test
context as shown, including when using `it.concurrent`.

## JSON report

The optional reporter records test outcomes and Attest assertion results:

<!-- snippet: skip reason="Vitest CLI invocation requires a project containing tests" -->
```bash
npx vitest run --reporter=default --reporter=@attest-ai/vitest/reporter --outputFile.attest=attest-report.json
```

## Learn more

- [Documentation](https://attest-framework.github.io/)
- [Project README](https://github.com/attest-framework/attest#readme)
- [TypeScript examples](https://github.com/attest-framework/attest/tree/main/sdks/typescript/examples)
