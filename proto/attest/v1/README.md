# Attest Protocol v1

This is the wire contract between an Attest SDK and its engine subprocess. Attest 1.1.0 ships protocol version `1` for both the Python and TypeScript SDKs.

**Transport:** JSON-RPC 2.0 over stdio (SDK spawns engine as subprocess)
**Encoding:** JSON (newline-delimited)
**Direction:** SDK → Engine (requests), Engine → SDK (responses only)

## Shipped transport

JSON-RPC over stdio is the only SDK-to-engine transport in 1.1.0. There is no gRPC transport or shared-engine server mode. The request, response and trace schemas are JSON schemas in this directory; this release does not use `.proto` files or a buf schema registry.

## Message Types

See [protocol-spec.md](protocol-spec.md) for the full specification.

## Design Principles

1. **Protocol-first:** The protocol is the contract between all components
2. **Capability negotiation:** SDKs declare required capabilities; engine declares supported ones
3. **Forward compatibility:** New fields in existing messages are non-breaking; ignore unknown fields
4. **Batch-first:** `evaluate_batch` is the primary evaluation path, not single assertions
5. **Structured errors:** All errors use typed error codes with human-readable messages
