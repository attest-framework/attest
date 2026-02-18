# Attest Protocol v1

**Transport:** JSON-RPC 2.0 over stdio (SDK spawns engine as subprocess)
**Encoding:** JSON (newline-delimited)
**Direction:** SDK → Engine (requests), Engine → SDK (responses + notifications)

## Upgrade Path

- v0.1–v0.2: JSON-RPC over stdio (current)
- v0.3+: Evaluate gRPC if TypeScript SDK needs shared engine process
- v1.0+: Formal .proto files with buf schema registry

## Message Types

See [protocol-spec.md](protocol-spec.md) for the full specification.

## Design Principles

1. **Protocol-first:** The protocol is the contract between all components
2. **Capability negotiation:** SDKs declare required capabilities; engine declares supported ones
3. **Forward compatibility:** New fields in existing messages are non-breaking; ignore unknown fields
4. **Batch-first:** `evaluate_batch` is the primary evaluation path, not single assertions
5. **Structured errors:** All errors use typed error codes with human-readable messages
