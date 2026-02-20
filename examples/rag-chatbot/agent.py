"""RAG chatbot agent — demonstrates retrieval-augmented generation with Attest."""

from __future__ import annotations

from typing import Any

from attest.agent import agent
from attest.trace import TraceBuilder


# Simulated knowledge base
KNOWLEDGE_BASE: dict[str, str] = {
    "install": (
        "Install Attest with: pip install attest-ai (Python) or "
        "npm install @attest-ai/core (Node.js)."
    ),
    "assertion": (
        "Attest assertions are organized in layers: Schema, Constraints, "
        "Trace, Content, Embedding, LLM Judge, Simulation, and Multi-Agent."
    ),
    "attest": (
        "Attest is an open-source testing framework for AI agents. "
        "It provides an 8-layer assertion pipeline from schema validation "
        "to multi-agent simulation."
    ),
}


@agent("rag-chatbot")
def rag_chat(builder: TraceBuilder, question: str) -> dict[str, Any]:
    """Answer a question using retrieval-augmented generation.

    Flow: embed query -> retrieve context -> generate answer
    """
    # Step 1: Embed the query
    builder.add_llm_call(
        name="embedding-model",
        args={"model": "text-embedding-3-small", "input": question},
        result={"embedding": [0.1, 0.2, 0.3], "dimensions": 3},
    )

    # Step 2: Retrieve relevant documents (longest match wins)
    q_lower = question.lower()
    relevant_key = "attest"
    best_len = 0
    for key in KNOWLEDGE_BASE:
        if key in q_lower and len(key) > best_len:
            relevant_key = key
            best_len = len(key)

    context = KNOWLEDGE_BASE[relevant_key]

    builder.add_retrieval(
        name="vector_search",
        args={"query": question, "top_k": 3},
        result={
            "documents": [
                {"content": context, "score": 0.95, "source": f"docs/{relevant_key}.md"},
            ],
        },
    )

    # Step 3: Generate answer with context
    builder.add_llm_call(
        name="gpt-4.1",
        args={
            "model": "gpt-4.1",
            "messages": [
                {"role": "system", "content": f"Answer using this context: {context}"},
                {"role": "user", "content": question},
            ],
        },
        result={
            "completion": f"Based on the documentation: {context}",
        },
    )

    builder.set_metadata(total_tokens=180, cost_usd=0.004, latency_ms=1200, model="gpt-4.1")

    answer = f"Based on the documentation: {context}"
    return {
        "message": answer,
        "structured": {
            "answer": answer,
            "sources": [f"docs/{relevant_key}.md"],
            "confidence": 0.95,
        },
    }
