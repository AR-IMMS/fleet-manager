# AR-IMMS Fleet Management Server

Go service for AR-IMMS fleet management. The accepted target architecture gives Fleet authority over installation registration and credentials, desired Collector configuration, revisions and publications, authenticated OpAMP sessions, observed outcomes, and fleet execution audit. **This repository currently contains a bootstrap slice, not that completed service.**

## System boundary

| Component | Owns |
|---|---|
| Core Backend | IAM, Node/Asset binding, business authorization and audit; authorized monitoring intent |
| Fleet Management Server | Fleet credentials, desired policy and durable publications, sessions, observations and execution audit |
| Telemetry Agent | Stable installation UUID, protected credentials, host evidence, local inventory/health, OS exporters and Supervisor provisioning |
| Official OpAMP Supervisor | Collector process and configuration execution in Fleet-managed native mode |
| OTel Collector | Telemetry collection and OTLP export |

Fleet receives authorized management requests from Core, Agent enrollment and observations over authenticated HTTPS, and native OpAMP traffic from the official Supervisor over WSS. Exact REST paths, schemas, authentication scopes, UID mapping, config composition and migration remain under review in the shared contracts. Do not implement their illustrative examples as final APIs.

## Current implementation

The code currently provides runtime configuration, an application lifecycle/composition foundation, a loopback HTTP health/readiness listener, MongoDB connectivity, a principal/authentication use case, and an OpAMP connection callback adapter. There is not yet a service entrypoint or a complete Fleet API. Durable fleet records, enrollment exchange, Core management API, session registry, configuration publication, observation ingestion, and production interoperability remain unimplemented. See the [implementation roadmap](docs/shared/integration/fleet-management/implementation-roadmap.md) for target checkpoints and decision gates.

## Repository structure

The [OpAMP server design](docs/local/architecture/opamp-server.md) describes the tested WS/WSS listener component and callback adapter. Runtime activation awaits a production identity verifier; `Application.Run` continues to serve HTTP health routes only.

| Path | Responsibility |
|---|---|
| `internal/app` | Composition root, application lifecycle, HTTP route wiring, and aggregate config façade |
| `internal/contexts/identity` | Principal and authentication use case, plus its OpAMP delivery adapter |
| `internal/platform/config` and `internal/platform/env` | Runtime config parsing/defaults and reusable environment loading |
| `internal/platform/http-server`, `internal/platform/logging`, and `internal/platform/mongodb` | HTTP serving, structured logging, and database technical adapters |
| `internal/shared` | Reserved for concrete cross-context mechanisms; no package until needed |

Domain and application dependencies point inward. Contexts remain isolated and communicate through consumer-owned ports wired by `app`. `platform` and `shared` do not depend on application contexts or `app`. Depguard enforces these import boundaries in `.golangci.yml`; see the [development guide](docs/local/development/DEVELOPMENT.md) before adding packages.

## Read first

1. [Shared Fleet contract index](docs/shared/contracts/fleet-management/README.md) and [accepted architecture overview](docs/shared/architecture/fleet-management/overview.md) — cross-system source of truth.
2. [Local system overview](docs/local/architecture/system-overview.md) and [flows](docs/local/architecture/flows.md) — this repository's interpretation and implementation status.
3. [Clean architecture](docs/local/architecture/clean-architecture.md), [development guide](docs/local/development/DEVELOPMENT.md), [coding policy](docs/local/development/CODING_POLICY.md), and [quality gates](docs/local/development/QUALITY_GATES.md).

## Verify the bootstrap slice

```bash
lefthook install
golangci-lint run ./...
go vet ./...
go test -race ./...
go build ./...
go mod tidy -diff
```

The official [OpAMP specification](https://opentelemetry.io/docs/specs/opamp/) governs protocol semantics. The shared C01–C04 contracts govern accepted AR-IMMS ownership; their proposed wire examples are not approved endpoints.
