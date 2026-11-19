# Local development

## Before coding

- Install supported Go toolchain (version set in `go.mod` and CI; choose/pin deliberately).
- Use pinned `github.com/open-telemetry/opamp-go` dependency; do not track `main` in production builds.
- Follow [Clean Architecture](../architecture/clean-architecture.md) and [quality gates](QUALITY_GATES.md).

## Commands (once Go module exists)

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

## Debug checklist

| Symptom | Check |
|---|---|
| No connection | Listen endpoint, TLS scheme, DNS/proxy, client cert/token |
| Accepted but missing status | Agent capabilities, received AgentToServer fields, callback handling |
| Config not applied | Client capability, matching config hash, Supervisor/Collector logs |
| Reconnect creates duplicates | Principal↔UID mapping, session generation, stale close |
| Healthy agent with no metrics | OpAMP health is separate from OTLP exporter/data freshness |

## When adding feature

Start with a focused use case and acceptance tests. Place protocol mapping in `transport/opamp`, domain rules within the relevant feature, and external integrations behind application ports. Do not add empty packages for future roadmap items.
