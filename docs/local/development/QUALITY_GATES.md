# Quality gates — build, review and release

## Pipeline at a glance

```mermaid
flowchart LR
  PR[PR created] --> F[Format / Vet]
  F --> T[Unit + race tests]
  T --> B[Build]
  B --> A[Architecture / security review]
  A --> M[Merge to dev/main]
  M --> R[Tag & release checks]
```

## Required for every PR

| Gate | Command or evidence | Fail if |
|---|---|---|
| Formatting | `gofmt -l` on tracked `.go` files yields nothing | Unformatted Go |
| Static checks | `go vet ./...` | Vet errors |
| Lint | `golangci-lint run ./...` | Lint or formatter findings |
| Unit/concurrency | `go test -race ./...` (CI Linux) | Test failure or data race |
| Build | `go build ./...` | Compilation failure |
| Dependencies | `go mod tidy -diff` yields no changes | Inconsistent modules |
| Architecture | Reviewer verifies inward dependency rule | Domain imports protocol/infra |
| Security | Verify auth, redaction, no secrets, bounded input | Unsafe configuration or secret exposure |
| Documentation | Relevant local docs updated; shared proposal reviewed when a cross-system contract changes | Behavior or ownership drift |

**CI job name recommendation:** `quality` (use exactly this name when configuring required GitHub status checks).

## Required for protocol / connection changes

- Integration test with a real `opamp-go` client and, for release, the pinned official Supervisor; fake Fleet verifier permitted only in tests.
- Multi-client concurrent connections; disconnect/reconnect; rejected auth; revoked credential; duplicate identity/conflicting session.
- Malformed/oversized payload and cancellation/timeout behavior.
- Confirm server capability flags reflect actual supported behavior.

## Required for remote configuration changes

- Durable desired revision, CAS/idempotency and publication/hash mapping before send; restart/reconnect reads persisted authority.
- Client supports/doesn't support remote configuration.
- Offline or interrupted delivery is pending/UNKNOWN with reason, never silently applied or assumed failed.
- Correlate revision and hash to applied/failed reports.
- Old/unknown hash cannot overwrite current revision status.
- Reconnect resynchronizes with latest desired state.
- Verify already-connected update, local/remote/fallback composition, rollback attribution and actual OTLP delivery as separate evidence.

## Required for enrollment and observation changes

- Invitation replay, same-attempt recovery, duplicate host evidence, revoked credential and concurrent publish/revoke paths.
- Agent/UID binding only after authentication; authorization scoped to actual Core or Agent service principal.
- COMPLETE/PARTIAL/FAILED inventory, older snapshot ordering, stale evidence, unknown fields, bounds and secret redaction.
- Draft C01–C04 paths/schemas require owner approval before production interoperability claims.

## Release gate

- All required PR checks pass on protected branch.
- Tag from reviewed commit, reproducible build and checksum generated.
- Dependency vulnerability scan (`govulncheck`) and software supply chain controls should be added **before production release**; not claim these are currently wired.
- TLS/auth hardening, configuration application and rollback verified against the pinned real Supervisor, Collector and OTLP sink, not just a mock client.
- Release notes: compatibility, migrations, operational risks, rollback plan.

## Continuous delivery policy

CI is required from first PR. Automated deployment is **not** part of bootstrap. Separate deploy workflows need a reviewed ADR, environment protection and credential management before activation.

## Branch protection suggestion

- `main`: pull requests, at least 1 independent review, resolved conversations, linear history, required `quality` check; no bypass by AI bots.
- `dev`: PR preferred; quality check required; no direct pushes except explicitly approved bootstrap/maintenance.
- Do not enable a required check before its workflow reports the exact check name, or merging may be blocked.

## Evidence template for PR

```text
Problem / intended behavior:
Architecture boundary touched:
Tests run (exact commands + result):
Failure paths covered:
Security / compatibility impact:
Docs / diagrams updated:
Open decisions or limitations:
```
