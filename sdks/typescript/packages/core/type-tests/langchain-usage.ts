import { RunnableLambda } from "@langchain/core/runnables";
import { FakeListChatModel } from "@langchain/core/utils/testing";
import { LangChainAdapter } from "../dist/esm/index.js";

// Compiled against the built .d.ts under `tsc --strict`: the documented usage must type-check.
const adapter = new LangChainAdapter("agent");
const model = new FakeListChatModel({ responses: ["ok"] });
const chain = RunnableLambda.from(async (question: string, config) => model.invoke(question, config));

export async function run(): Promise<void> {
  await chain.invoke("hi", { callbacks: [adapter] });
  await model.invoke("hi", { callbacks: [adapter] });
}
