# AI Agent Guide — AR-IMMS OpAMP Server

This Go service implements the **OpAMP protocol boundary** for AR-IMMS Fleet/Agent Management. It authenticates OpAMP connections, handles sessions and protocol messages, delivers remote configuration from Core, and forwards status/results. It is **not** the Fleet business backend.

## Ownership

- **Core (NestJS):** IAM, enrollment/installation identity, asset binding, desired state, rollout policy, audit.
- **This service:** OpAMP transport, sessions, capability-aware delivery, normalized protocol events.
- **Official OpAMP Supervisor:** manages OTel Collector process and applies Collector configuration.
- **Telemetry Agent:** local discovery, integrations, inventory and local policies; do not silently refactor its responsibilities.

## Read before coding

1. [`docs/local/architecture/system-overview.md`](docs/local/architecture/system-overview.md)
2. [`docs/local/architecture/clean-architecture.md`](docs/local/architecture/clean-architecture.md)
3. [`docs/local/development/CODING_POLICY.md`](docs/local/development/CODING_POLICY.md)
4. [`docs/local/development/QUALITY_GATES.md`](docs/local/development/QUALITY_GATES.md)
5. Upstream [OpAMP spec](https://opentelemetry.io/docs/specs/opamp/) for protocol semantics.

## Non-negotiables

- Domain/application MUST NOT import OpAMP protobufs, HTTP, framework or storage packages.
- Never trust `instance_uid` alone as authentication; fail closed on uncertain identity.
- Never claim a configuration is **applied** merely because a message was sent.
- Preserve reconnect, cancellation, concurrency safety, and sensitive data redaction.
- Keep changes small, add tests for behavior and failure paths, update relevant docs.
- No unsolicited scope growth, new persistent business database, supervisor lifecycle ownership, or speculative capabilities.

If a contract or security decision is unclear, document the assumption and request review instead of inventing behavior.
