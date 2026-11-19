# Local development

## Before coding

- Install supported Go toolchain (version set in `go.mod` and CI; choose/pin deliberately).
- Use pinned `github.com/open-telemetry/opamp-go` dependency; do not track `main` in production builds.
- Read the [accepted Fleet overview](../../shared/architecture/fleet-management/overview.md) and [C01–C04 index](../../shared/contracts/fleet-management/README.md). Proposed endpoints and schemas need owner approval before implementation.
- Follow [Clean Architecture](../architecture/clean-architecture.md) and [quality gates](QUALITY_GATES.md).

## Repository structure

```text
internal/
├── app/                         # Composition root and aggregate app config API
│   ├── config.go                # Config aliases and LoadConfig façade
│   ├── application.go           # Startup, cancellation and shutdown orchestration
│   ├── wiring.go                # Concrete platform adapter assembly
│   └── router.go                # Application-owned HTTP route wiring
├── contexts/
│   └── identity/
│       ├── domain/               # Authenticated principal and identity rules
│       ├── application/          # Authentication use case and owned ports
│       └── delivery/opamp/       # OpAMP adapter owned by identity/session context
├── platform/
│   ├── config/                   # Technical env-backed config parsing and defaults
│   ├── env/                      # Reusable environment reader
│   ├── http-server/              # Shared listener, health response and shutdown adapter
│   ├── logging/                  # Structured logger adapter
│   └── mongodb/                  # MongoDB driver adapter
└── shared/                       # Add only when a concrete shared mechanism is needed
```

Create context `infrastructure/` and additional `delivery/` packages only when an adapter exists. Current code has no `configuration` or `observations` implementation yet. Do not add empty packages.

## Dependency rules

- Dependencies point inward: domain has no application, adapter, app, platform or SDK imports; application may import only its own domain and minimal shared ports/mechanisms.
- Contexts do not import another context's internal packages. Put consumer-owned ports in the consuming context and wire adapters at `app`.
- Infrastructure and delivery implement inward ports. OpAMP delivery stays in the context that owns sessions/protocol; shared HTTP servers belong in platform only when genuinely shared.
- Only `app` may assemble concrete contexts and platform adapters. `platform` and `shared` do not import `app` or contexts; no package imports `app`.
- Aggregate runtime config is exposed at `app/config.go`; environment parsing and technical defaults live in `platform/config`, with reusable parsing in `platform/env`.
- `.golangci.yml` Depguard rules enforce these package boundaries with concrete import prefixes. Verify a boundary rule with a temporary forbidden import when changing the rules.

## Commands

```bash
go mod download
go fmt ./...
go vet ./...
go test -race ./...
go build ./...
```

## Local Git checks

Install the repository hooks after installing Lefthook 2.2.2 and golangci-lint 2.15.0:

```bash
lefthook install
lefthook validate
golangci-lint run ./...
```

`pre-commit` checks Go formatting and lint when Go files are staged. `commit-msg` requires a Conventional Commits subject such as `feat(opamp): handle reconnect` or `fix!: reject stale configuration`. Git-generated merge and revert subjects are accepted. `pre-push` runs vet, module tidy verification, and race tests. CI remains the required merge gate.

## Local server safety

- Bind to `127.0.0.1` for unauthenticated development experiments only.
- Never expose a demo `Accept: true` connection callback publicly.
- Record in docs whether an endpoint is HTTP/WebSocket, whether TLS is enabled, and how the dev identity verifier works.
- The current repository has no running Fleet server. Do not present bootstrap callback tests as enrollment, durable publication or production interoperability evidence.

## Debug checklist

| Symptom | Check |
|---|---|
| No connection | Listen endpoint, TLS scheme, DNS/proxy, client cert/token |
| Accepted but missing status | Agent capabilities, received AgentToServer fields, callback handling |
| Config not applied | Client capability, matching config hash, Supervisor/Collector logs |
| Reconnect creates duplicates | Principal↔UID mapping, session generation, stale close |
| Healthy agent with no metrics | OpAMP health is separate from OTLP exporter/data freshness |
| Desired revision disappears after restart | Fleet durable desired/publication records and reconciliation wakeup |
| Agent inventory seems empty | Snapshot provider/scope and COMPLETE/PARTIAL/FAILED semantics |

## When adding feature

Start with a focused use case and acceptance tests from the [shared roadmap](../../shared/integration/fleet-management/implementation-roadmap.md). Place protocol mapping in the owning context's `delivery/opamp`, HTTPS and persistence in context adapters, domain rules within the relevant Fleet feature, and external integrations behind consumer-owned ports. Add durable persistence for Fleet-owned state as each approved vertical slice requires it. Do not add empty packages for future roadmap items.
