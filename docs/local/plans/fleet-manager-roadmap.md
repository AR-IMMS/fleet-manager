# Fleet Manager — Implementation Roadmap

Updated: 2026-11-19
Status: PLANNED — checklist derived from the agreed B+ direction; not a source audit or implementation-completion claim.

## Purpose and scope

Build AR-IMMS Fleet Manager as the central Go service for Agent enrollment, installation credentials, native OpAMP sessions, Collector desired configuration, durable publications and operational observations.

Core remains the user-facing NestJS API, human IAM/RBAC, Asset/Node authority and business authorizer. Agent and Supervisor connect directly to Fleet for their respective control-plane interactions. Fleet validates authenticated caller scope and owns fleet execution audit.

The official OTel OpAMP Supervisor is the sole Collector lifecycle/config executor in native mode. The Telemetry Agent owns durable local installation identity, protected secrets, Supervisor provisioning, exporter/LHM dependency lifecycle and controlled local Operations. Collector owns telemetry execution and OTLP delivery. Existing Core-authorized NATS Operations remain outside Fleet execution authority.

## Planning rules

- Architecture ownership is agreed; REST paths, schemas, auth details, merge rules and retention policies must retain their actual ACCEPTED/PROPOSED/OPEN status.
- Chốt contract tối thiểu cho từng boundary trước khi triển khai production wire behavior. Không dùng Mock Fleet payload làm contract đã được duyệt.
- Security, audit, durable idempotency and failure handling accompany each vertical slice.
- Domain/application code is independent of HTTP, OpAMP protobuf and MongoDB adapters. Cross-capability calls use explicit application boundaries.
- Shared contracts remain canonical for cross-system behavior. This local roadmap owns implementation checkpoints and evidence tracking; it does not duplicate the decision ledger.
- No checkpoint is DONE solely because code compiles or an API returns acceptance. Record commit, command, environment, CI run and evidence relevant to its acceptance gates.

## Current evidence baseline

The Agent-side baseline was verified at telemetry-agent commit `1a7c15046bbf241fe18b631fda657c8979fe4834`, CI run `44380941059`. Windows and Ubuntu passed native service lifecycle, focused race checks, Supervisor fallback/OTLP and provisional Mock Fleet config/rollback/revocation E2Es. Docker-backed suites passed on Ubuntu; Windows Docker suites were skipped.

This proves tested Agent behavior with a fixture and real pinned Supervisor/Collector, not production Fleet/Core interoperability. Fleet implementation progress has not been audited for this roadmap. Preserve existing code and assess it before assigning DONE statuses.

## Checkpoint summary

| ID | Checkpoint | Dependency | Status |
|---|---|---|---|
| FM-01 | Service foundation | Existing repository assessment | PLANNED |
| FM-02 | Service authentication and authorization | FM-01; approved security profile | PLANNED |
| FM-03 | Enrollment and Agent registry | FM-02; enrollment contract | PLANNED |
| FM-04 | Native OpAMP session management | FM-03; UID/session profile | PLANNED |
| FM-05 | Configuration management | FM-04; intent/composition contract | PLANNED |
| FM-06 | Observations and fleet status | FM-03; observation contract; FM-04/05 for protocol projections | PLANNED |
| FM-07 | Reliability and audit acceptance | Implemented incrementally in FM-01–06 | PLANNED |
| FM-08 | Production integration and release | FM-01–07; Core/Agent readiness | PLANNED |

## FM-01 — Service foundation

- [ ] Assess existing code, tests, local instructions and documentation; identify obsolete adapter-only assumptions.
- [ ] Establish fleet-manager entrypoint, application composition and validated configuration.
- [ ] Compose HTTP and native OpAMP listeners with explicit TLS/termination policy, limits and graceful shutdown.
- [ ] Add Fleet-owned MongoDB connection, migrations/indexes and documented deployment-topology requirements.
- [ ] Add structured secret-safe logs, request correlation, liveness and readiness.
- [ ] Establish formatting, architecture checks, tests, vet/build and CI.

Acceptance: service starts with valid settings, rejects invalid settings, reports dependency readiness correctly and shuts down without leaking listeners/workers. Persistence is confined to the Fleet database. Transaction-dependent behavior is tested against a supported MongoDB topology.

## FM-02 — Service authentication and authorization

- [ ] Approve Core→Fleet service identity and delegated actor/target verification.
- [ ] Separate Core management, Agent reporting and Supervisor OpAMP permissions.
- [ ] Reject unauthorized scope/target and unverified caller-supplied actor identity.
- [ ] Standardize request/operation IDs, safe errors, redaction and credential-bearing transport behavior.

Acceptance: absent, invalid, expired/revoked or incorrectly scoped credentials cannot access protected actions; authorization tests cover cross-installation access. Logs and errors expose no secrets.

## FM-03 — Enrollment and Agent registry

- [ ] Create Core-preapproved invitations for authorized Nodes, with bounded expiry and use-once semantics.
- [ ] Exchange invitations using durable installation UUID and enrollment attempt identity.
- [ ] Persist recoverable outcomes and handle lost responses, replay and concurrent exchange safely.
- [ ] Implement registry list/detail and installation/credential lifecycle records.
- [ ] Implement revocation; implement credential replacement/rotation according to an approved delivery workflow.
- [ ] Preserve the distinction between durable agent_id and host identity evidence; handle collisions without automatic merging.
- [ ] Record Core-authorized binding without taking ownership of Asset/Node placement policy.

Acceptance: expiry, token replay, retry, conflicting attempts, concurrent enrollment, credential replacement and recovery tests pass with durable storage. Recovery requires approved proof, not a bare attempt UUID. Secrets remain protected and absent from ordinary persistence/logging except explicitly secured recovery material.

## FM-04 — Native OpAMP session management

- [ ] Authenticate the official Supervisor and persist controlled instance_uid↔agent_id binding.
- [ ] Track active sessions and actual reported capabilities separately from Agent capabilities.
- [ ] Handle reconnect, duplicate identity/session conflicts and server shutdown.
- [ ] Close/revalidate active sessions after persisted revocation and reject reconnect.
- [ ] Provide application ports for config wakeup and session invalidation.

Acceptance: real pinned Supervisor connects with valid credentials; wrong binding/revoked credentials fail. Revocation races with reconnect/publication are covered within a documented enforcement bound. Revoking connectivity does not implicitly stop the local Collector.

## FM-05 — Configuration management

- [ ] Approve typed monitoring intents, receiver capability catalog and endpoint/credential-reference policy.
- [ ] Persist desired configuration with CAS revisions and durable operation idempotency.
- [ ] Render deterministic remote configuration and verify local/remote/fallback composition using the pinned Supervisor.
- [ ] Persist immutable publication bytes/hash mapping before sending.
- [ ] Publish to already-connected sessions; reconcile latest desired after missed wakeups, reconnect and Fleet restart.
- [ ] Attribute reports to known publication hashes; retain UNKNOWN/unattributed outcomes when evidence is insufficient.
- [ ] Handle invalid-config rollback without declaring the newest desired revision applied.
- [ ] Define bounded retry/quarantine for known-bad desired config and compatible dependency teardown coordination.

Acceptance: stale CAS conflicts, duplicate requests are stable, no-op updates avoid unnecessary restarts, live updates reach connected Supervisor and restart/reconnect never regresses to obsolete desired policy. Real Collector delivers OTLP to a sink. Bad config recovers as evidenced, without duplicate scrapes or multiple Collector owners.

## FM-06 — Observations and fleet status

- [ ] Accept authenticated versioned Agent identity, inventory, dependency, capabilities and health evidence.
- [ ] Validate principal binding, payload bounds, timestamps and provenance.
- [ ] Reconcile provider/scope snapshots with COMPLETE/PARTIAL/FAILED semantics, ordering and deduplication.
- [ ] Consume supported Supervisor health/config/effective-config evidence with redaction and bounded retention.
- [ ] Expose Core query projections with freshness, source and explicit UNKNOWN states.

Acceptance: failed/partial snapshots cannot erase live inventory; stale complete snapshots cannot overwrite newer state. Agent, Supervisor and Collector evidence stay distinct. Readiness or OpAMP APPLIED never implies OTLP delivery.

## FM-07 — Reliability and execution audit acceptance

- [ ] Verify durable idempotency across timeout, retry and restart; changed requests under the same key conflict.
- [ ] Verify local atomicity of accepted state, execution audit and publication/outbox intent where required.
- [ ] Recover missed notifications through durable reconciliation; bound retries and worker shutdown.
- [ ] Correlate Core business intent and Fleet outcomes by operation ID without shared database access.
- [ ] Approve and implement retention/cleanup for attempts, credentials, publications, observations, audit and idempotency records.
- [ ] Exercise database failures, disconnected sessions, ambiguous apply outcomes and concurrent revoke/publication.

Acceptance: acknowledged durable work survives restart, ambiguous outcomes remain UNKNOWN until resolved, and audit distinguishes acceptance/publication/observation. Reliability controls are present in earlier checkpoints, then verified together here.

## FM-08 — Production integration and release

- [ ] Publish approved versioned cross-system contracts through the shared-docs workflow.
- [ ] Integrate production Core management API and Agent Fleet client/reporting paths.
- [ ] Define and test migration from Core-backed enrollment/config authority, preserving installation identity and protected credentials.
- [ ] Run production-boundary E2Es with pinned Supervisor/Collector and independent OTLP sink evidence.
- [ ] Verify upgrade, rollback limits, single Collector ownership and compatible Agent dependency lifecycle.
- [ ] Prepare deployment configuration, secret provisioning, operational monitoring, backup/restore and runbook.
- [ ] Complete code review, merge/release gates and record release artifacts/version.

Acceptance: real Core→Fleet→Agent/Supervisor flow passes. Existing installations migrate under explicit compatibility policy. Restore/restart recovery is demonstrated. Report supported platforms and remaining limitations without converting skips into passes.

## First vertical milestone

Core-authorized invitation → Agent enrollment → authenticated Supervisor session → Fleet publication → Collector OTLP received at an independent sink.

If Core is temporarily represented by a fixture, label the milestone Fleet/Agent integration with Mock Core; production Core interoperability remains open.

Follow with Fleet restart, disconnect during apply, bad-config rollback, active-session revoke/reconnect and idempotent retries. Inventory/health can proceed after enrollment/security foundations while configuration work continues.

## Contract gates and references

Resolve decisions in their owning shared contract rather than introducing another ledger:

- `docs/shared/contracts/fleet-management/README.md`
- `docs/shared/contracts/fleet-management/enrollment-identity-v1.md`
- `docs/shared/contracts/fleet-management/configuration-management-v1.md`
- `docs/shared/contracts/fleet-management/inventory-health-v1.md`
- `docs/shared/contracts/fleet-management/opamp-integration-v1.md`

Shared overview, roadmap, verification matrix and Agent handoff provide cross-repository context when published. This local roadmap adds repository execution tracking. Verify paths against the checked-out shared-docs revision before treating them as available.

## Progress and evidence convention

Use PLANNED, IN_PROGRESS, BLOCKED, DONE for implementation. Separately record PASS, FAIL, NOT_RUN, BLOCKED or UNKNOWN for verification. DONE requires the checkpoint acceptance gates and linked evidence; a mocked boundary must remain identified.

| Checkpoint | Source commit | Test / CI link | Environment | Result | Limits / follow-up |
|---|---|---|---|---|---|
| Baseline assessment | — | — | — | NOT_RUN | Inspect repository before assigning implementation progress |

## Deferred baseline scope

- General-purpose remote code execution or custom OpAMP command execution.
- Supervisor-driven package/binary updates.
- Automatic inference of physical topology from hostname/IP/fingerprints.
- Destructive legacy removal before verified migration and a support-window decision.
- A separate Fleet public UI or duplicate human IAM/Asset ownership.
