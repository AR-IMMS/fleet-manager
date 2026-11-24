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

The code provides runtime configuration and lifecycle wiring, a health/readiness listener, MongoDB connectivity, identity verification, an OpAMP callback/server adapter, and opt-in invitation/enrollment HTTP routes. The API is still a bootstrap slice; configuration publication, observation ingestion, and full production interoperability remain incomplete. Enrollment routes are disabled by default and their cross-repository wire details remain provisional. See the [implementation roadmap](docs/shared/integration/fleet-management/implementation-roadmap.md) for target checkpoints and decision gates.

## API documentation for local UAT

Fleet serves an embedded OpenAPI document and Swagger UI only when
`FLEET_API_DOCS_ENABLED=true` is set in its environment before startup:

- OpenAPI document: `GET /openapi.yaml`
- Interactive Swagger UI: `GET /docs/`

For local HTTP development, set `FLEET_API_DOCS_ENABLED=true` in the Fleet
environment before restarting. Open `http://127.0.0.1:8081/docs/` with the
default listener, or `http://127.0.0.1:18081/docs/` with `compose.yaml` (set
the variable in the shell or the Compose `.env` file). Do not expose the docs
listener publicly; when Fleet is reachable beyond loopback, use configured TLS
and restrict access through the deployment network policy. Swagger UI does
not bypass the API's Core client-certificate checks or Agent invitation/recovery
proof.

Swagger lists the enrollment operations for reference, but Fleet only mounts
them when `FLEET_ENROLLMENT_ENABLED=true` has also been configured with all
required security settings; otherwise those paths are unavailable. The
displayed schemas describe this implementation and are marked provisional;
they are not final cross-repository contracts. Use Core's
existing Swagger UI at `http://localhost:<core-port>/api/docs` for the
human-authorized invitation and Core binding synchronization endpoints.

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

## Local Docker Compose

The development Compose stack builds a non-root Fleet image and starts a
separate MongoDB replica set. It is for local development only: MongoDB has no
authentication, and the current Fleet HTTP listener is health-only. OpAMP
remains disabled.

Fleet deliberately validates that its plaintext HTTP listener binds to an
explicit loopback address. The Compose service uses host networking to retain
that boundary and is reachable at `http://127.0.0.1:18081`. On Docker Desktop
4.34 or later, enable **Settings → Resources → Network → Enable host
networking**. Linux Docker Engine supports host networking without that setting.
This service uses host port `27020` for its local MongoDB replica set so it can
run beside the existing Core MongoDB stack. Change
`FLEET_MONGODB_HOST_PORT` if that port is occupied.

```bash
docker compose up --build --wait
docker compose ps
```

Check Fleet readiness at `http://127.0.0.1:18081/health/ready`. Follow logs
with `docker compose logs -f fleet-manager`. Stop the stack with
`docker compose down`; the named MongoDB volume remains. `docker compose down
-v` deletes the Fleet development database volume.
