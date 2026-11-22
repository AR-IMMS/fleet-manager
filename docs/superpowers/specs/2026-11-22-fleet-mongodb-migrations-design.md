# Fleet MongoDB migrations and indexes design

**Status:** Implemented after approval, with the requested separation between immutable migration history and per-startup index reconciliation.

## Intent

Complete the FM-01 persistence foundation by making Fleet-owned schema/index setup explicit, repeatable, and documented before the service accepts traffic.

## Verified current state

- `identity/infrastructure/mongodb.NewVerifier` previously created four unique indexes as a constructor side effect. It now only binds the collection.
- Application wiring opens MongoDB, runs versioned migrations, reconciles indexes on every startup, then constructs the verifier and starts enabled listeners.
- CI uses MongoDB 8.0 standalone. `.env.example` points to a local replica set (`rs0`). Identity binding and revocation update one installation document; no multi-document transaction or change stream is used.

## Approaches

1. **Context-owned versioned identity migration (recommended):** add an explicit migration function in the identity Mongo adapter, call it before verifier construction, record its version, and keep index creation idempotent. This gives startup a clear schema boundary and prepares a safe place for later identity schema changes without a migration dependency.
2. **Declarative index ensure only:** retain index setup as a repeatable startup operation and only improve names/tests/docs. This is smaller, but it has no applied-version record and would leave the roadmap's migration requirement unresolved.

Approved approach 1, with this adjustment: applied migration history and per-startup index reconciliation have separate lifecycles. Do not add a third-party migration framework or a separate CLI for this initial migration.

## Proposed migration behavior

- `identity/infrastructure/mongodb.Migrate(ctx, database)` applies the immutable baseline `identity-installations-v1`. It ensures the identity collection exists and records the migration's fixed SHA-256 descriptor checksum and applied time in `identity_schema_migrations`.
- A matching marker is a no-op; a checksum mismatch or unknown history entry fails startup. The initial migration's collection ensure and unique-keyed history insert can safely be retried and run concurrently. This behavior does not establish concurrency or interruption safety for future data migrations.
- `EnsureIndexes(ctx, database)` runs independently on every startup. It reconciles stable named unique indexes `credential_hash_1`, `credential_id_1`, `agent_id_1`, and `instance_uid_1`; the latter retains its string-valued partial filter. The index check never writes migration history.
- Wiring runs `Migrate`, `EnsureIndexes`, then `NewVerifier` after Mongo opens and before any listener starts. `NewVerifier` has no DDL side effects.
- Do not repair or delete existing records. Duplicate data or incompatible index definitions make index reconciliation fail closed; operator cleanup/backup and retry are required.
- No transactions are used or required. Every released migration definition must remain unchanged; future schema/data transformations require a new version and an explicitly reviewed execution/recovery strategy.

## Topology and version evidence

- **Verified:** MongoDB 8.0 standalone in CI supports the current identity indexes and single-document credential/UID operations.
- **Local example:** the supplied `.env.example` URI uses a replica set. Keep that sample compatible; do not treat it as proof that a replica set is required.
- **Proposed current requirement:** a reachable writable primary and database privileges to create the identity/migration collections and indexes, then read/write Fleet application data. Current code does not require multi-document transactions or change streams.
- **Open:** MongoDB 8.0 is the tested CI version, not an approved minimum server version. Production high-availability, backup, and failover topology still needs an operator/service-owner decision; this change must not claim it was exercised by standalone CI.

## Scope and files

- Create: `internal/contexts/identity/infrastructure/mongodb/migrations.go` and migration integration tests.
- Modify: `verifier.go` to remove constructor DDL; `app/wiring.go` to run migrations before verifier construction.
- Update local identity/runtime docs and FM-01 roadmap with migration order, privileges, standalone/replica-set evidence, and remaining OPEN topology choices. Do not change C01/C04 wire contracts or `docs/shared`.

## Verification

- Mongo integration tests passed locally for migration/reapplication, checksum validation, concurrent baseline starts, index creation and re-creation independent of migration history, index name/options, duplicate/incompatible index failure, and existing credential binding/revocation behavior.
- The application-level test passed locally with the race detector: an incompatible index causes startup failure before HTTP binds.
- Local checks passed: `go test ./...`, `go test -race ./...`, `go vet ./...`, `go build ./...`, `golangci-lint run ./...`, `go mod tidy -diff`, and `gofmt -l` on changed Go files (no output). Build was run on the host because sandbox VCS stamping could not read the docs submodule.
- The full Mongo identity suite passed with `-race`, and `go test -race -tags 'supervisor,integration' -timeout 10m -count 1 -v ./tests/supervisor` passed against the local configured Mongo endpoint and pinned Supervisor v0.160.0. Hosted CI for this change is pending. CI's configured MongoDB 8.0 standalone and the local endpoint are test evidence, not approval of the minimum server version or production topology.

## Remaining OPEN decisions

The minimum supported MongoDB version and production high-availability, failover, backup, and restore topology remain open. Future data migrations must not inherit the baseline's retry/concurrency assumptions; each needs a reviewed execution and recovery strategy. This implementation does not change accepted/proposed Fleet contracts or `docs/shared`.
