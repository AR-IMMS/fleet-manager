# AI Agent Guide — AR-IMMS Fleet Management Server

This repository is the separate Go **Fleet Management Server**. Its target scope includes fleet registration and credentials, desired Collector configuration, durable revisions/publications, authenticated OpAMP sessions, observations, and fleet execution audit. The current code is only a bootstrap slice; read code before claiming any target capability exists.

## Source of truth

1. [Shared Fleet contract index](docs/shared/contracts/fleet-management/README.md) and [accepted architecture summary](docs/shared/architecture/fleet-management/overview.md).
2. C01 enrollment/identity, C02 configuration, C03 observations, C04 OpAMP integration in that index. Keep **ACCEPTED** ownership separate from **PROPOSED/OPEN** wire details.
3. [Local system overview](docs/local/architecture/system-overview.md), [clean architecture](docs/local/architecture/clean-architecture.md), [coding policy](docs/local/development/CODING_POLICY.md), and [quality gates](docs/local/development/QUALITY_GATES.md).
4. Upstream [OpAMP specification](https://opentelemetry.io/docs/specs/opamp/) and pinned Supervisor behavior for protocol work.

If older shared pages disagree with C01–C04 or the accepted Fleet overview, use the latter and flag the conflict for shared-docs owners. Do not silently change the submodule or treat draft examples as approved APIs.

## Ownership

- **Core (NestJS):** IAM, Node/Asset authority, business authorization and audit; sends authorized typed monitoring intent.
- **Fleet (this service):** invitation/registration and credential authority, desired Collector policy and revision, durable publication, authenticated sessions, observations/freshness, and execution audit.
- **Official OpAMP Supervisor:** sole Collector lifecycle and configuration executor in Fleet-managed native mode.
- **Telemetry Agent:** stable installation identity, protected local credentials, Supervisor provisioning, host evidence, local inventory/health, managed OS exporters, CLI, and existing Core-authorized Operations.

## Non-negotiables

- Keep domain/application independent of OpAMP protobuf, HTTP, framework, and storage packages. Put protocol and persistence behind adapters and ports.
- Authenticate before binding `instance_uid` to `agent_id`; `host_id` is evidence, not identity proof. Fail closed on uncertain authorization and revocation.
- Persist desired state and publication/hash correlation before delivery. Distinguish desired, published, reported applied, effective, Collector ready, and OTLP delivered.
- Preserve reconnect, cancellation, concurrency safety, bounded work, and secret redaction. Never infer application from a send or from a stale hash.
- Keep Core and Fleet persistence separate. Do not implement Supervisor behavior or a second native-mode Collector owner.
- Implement only reviewed contract details. Record unresolved wire/security decisions in the shared contract proposal and seek owner review before coupling code to them.
- Make small, tested changes with failure paths and update relevant documentation.
