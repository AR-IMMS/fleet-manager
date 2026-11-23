# Agent Registration in Fleet

**Implementation state:** implemented locally on `feat/agent-registration`.
**Contract state:** C01/C04 ownership is accepted; the HTTP paths, payloads,
authentication details, TTLs, and recovery rules below are implementation
proposals and are not approved cross-service contracts.
**Activation:** `FLEET_ENROLLMENT_ENABLED=false` by default. Keep production
OpAMP disabled until the prerequisites in [Open decisions](#open-decisions)
are resolved.

## Flow

1. Core asks Fleet for an invitation for a Node. Fleet requires a verified
   Core client certificate and derives the caller identity from its configured
   certificate common name; actor identity is never accepted from request JSON.
2. Fleet persists an invitation and returns its secret to Core. An identical
   idempotent retry returns the same pending secret. A changed request conflicts.
3. The Agent exchanges the invitation, its stable enrollment attempt ID, its
   `agent_id`, and a high-entropy recovery proof. Mongo atomically consumes the
   invitation, inserts the active credential digest, stores the encrypted
   response for recovery, and records the Agent binding against the Node gate.
4. A retry with the same attempt and proof receives the same credential. A
   separate recovery request can recover the result during the configured
   recovery window. Core can query a secret-free status using its invitation
   operation ID.
5. Core closes or reopens a Node enrollment gate with an idempotent operation
   ID. Exchange and gate updates write the same Mongo gate record. A close
   therefore either sees an exchange that committed first, or causes a later
   exchange to fail closed.
6. The existing OpAMP verifier authenticates the Fleet credential and binds
   the Supervisor `instance_uid` to the separately owned `agent_id`.

## Class diagram

Solid components are implemented in Fleet. Route shapes and lifecycle wire
semantics remain proposed until Core, Fleet, and Agent owners align them.

```mermaid
classDiagram
direction LR
class Core {
  +createInvitation(nodeID, operationID)
  +changeGate(nodeID, operationID, status)
  +getEnrollmentStatus(operationID)
}
class EnrollmentHTTP {
  <<provisional HTTP adapter>>
  +createInvitation(request)
  +cancelInvitation(invitationID, operationID)
  +closeGate(nodeID, operationID)
  +completeGate(nodeID, operationID, version, state)
  +revokeAgent(agentID)
  +getStatus(operationID)
  +exchange(request)
  +recover(request)
}
class EnrollmentService {
  +CreateInvitation(command)
  +Exchange(command)
  +Recover(attemptID, proof)
  +CancelInvitation(invitationID, operationID)
  +CloseGate(nodeID, operationID)
  +CompleteGate(nodeID, operationID, version, state)
  +RevokeAgent(agentID)
  +GetStatus(operationID)
}
class MongoEnrollmentStore {
  +CreateInvitation(invitation)
  +Enroll(attempt)
  +Recover(attemptID, proofHash)
  +CancelInvitation(invitationID, operationID)
  +CloseGate(nodeID, operationID)
  +CompleteGate(nodeID, operationID, version, state)
  +GetStatus(operationID)
}
class IdentityVerifier {
  <<existing>>
  +Verify(credential)
  +BindAndAuthorize(agentID, instanceUID)
  +Revoke(credentialID)
  +RevokeAgent(agentID)
}
class Agent {
  +exchangeInvitation()
  +persistCredential()
}
class Supervisor {
  +connectToFleetOpAMP()
}
Core --> EnrollmentHTTP : mTLS; proposed
Agent --> EnrollmentHTTP : TLS; proposed
EnrollmentHTTP --> EnrollmentService
EnrollmentService --> MongoEnrollmentStore
MongoEnrollmentStore --> IdentityVerifier : insert credential digest
MongoEnrollmentStore --> MongoEnrollmentStore : atomic gate and exchange
Supervisor --> IdentityVerifier : verified Fleet credential
```

## Boundaries and persistence

- Use cases and domain types live in `internal/contexts/identity`; HTTP DTOs
  and certificate checks live in `delivery/http`; Mongo behavior stays in
  `infrastructure/mongodb`; `app` only composes them.
- `agent_id`, Core `node_id`, host evidence, and Supervisor `instance_uid`
  remain different values. Fleet stores `agent_id` in the existing verifier;
  the existing OpAMP adapter binds the Supervisor UID only after credential
  authentication.
- Migration `identity-enrollment-v1` creates enrollment collections. Startup
  index reconciliation installs unique keys and TTL indexes separately from
  migration history. Enrollment activation checks Mongo transaction support.
- Invitation token hashes, enrollment attempt/recovery proof hashes, and
  credential hashes are stored instead of their raw values. Pending invitation
  secrets and exchange responses use AES-256-GCM escrow. Mongo TTL indexes
  expire invitation escrow at invitation expiry and recovery escrow at
  `FLEET_ENROLLMENT_RECOVERY_TTL` (default 24 hours, maximum 7 days). Durable
  status metadata does not contain credential secrets.
- Exchange and gate changes require a Mongo replica set or sharded deployment
  that supports transactions. CI starts a single-node replica set for these
  tests; a standalone MongoDB deployment fails enrollment startup checks.
- The current unique `agent_id` and partial unique `node_id` indexes allow one
  active Fleet credential per Agent and one Agent per Core Node. This bounds
  gate binding state. Rotation, reinstall, and any future multi-Agent-per-Node
  model need an explicitly reviewed migration/policy.

## Provisional routes

These routes are local implementation details for review, not approved Fleet
contracts. All request bodies reject unknown fields, are size-limited, and
responses/errors do not echo submitted secrets.

| Route | Caller | Purpose |
|---|---|---|
| `POST /internal/v1/enrollment/invitations` | Core mTLS certificate with configured common name | Create or replay a pending invitation; body includes operation ID, idempotency key, Node ID, optional TTL seconds. |
| `POST /internal/v1/enrollment/invitations/{invitation_id}/cancel` | Core mTLS | Cancel only a pending invitation when the operation ID matches the original create. |
| `POST /internal/v1/nodes/{node_id}/enrollment-gate/close` | Core mTLS | Close the gate and return version, prior state, and active binding snapshot. |
| `PUT /internal/v1/nodes/{node_id}/enrollment-gate` | Core mTLS | Commit CLOSED or restore prior state with the exact operation ID/version. |
| `POST /internal/v1/agents/{agent_id}/revoke` | Core mTLS | Idempotently revoke active Fleet credentials; does not stop Collector. |
| `POST /internal/v1/enrollment/nodes/{node_id}/gate` | Core mTLS | Legacy one-step gate route; reject while a two-phase transition is pending. |
| `GET /internal/v1/enrollment/operations/{operation_id}` | Core mTLS | Return secret-free invitation/enrollment status for Core journal recovery. |
| `POST /v1/enrollment/exchanges` | Agent over HTTPS | Exchange attempt ID, Agent ID, invitation token, and recovery proof for a Fleet credential and WSS endpoint. |
| `POST /v1/enrollment/recoveries` | Agent over HTTPS | Recover the same credential using attempt ID, Agent ID, and recovery proof before escrow expiry. |

Enabling enrollment requires TLS certificate/key, a dedicated Core client CA,
the exact Core certificate common name, a 32-byte base64 encryption key, and a
WSS OpAMP endpoint. Configure the values in the deployment secret manager; do
not commit them to `.env.local` or a deployment manifest.

## Open decisions

| Decision | Current local implementation | Needed for interoperability/production |
|---|---|---|
| Core authentication and actor attribution | mTLS with one configured certificate common name as actor | Agree on Core service/delegated actor claims, certificate rotation, and authorization freshness. |
| Fleet routes and schemas | Provisional paths and JSON fields above | Approve exact C01 paths, headers, payloads, error codes, and status semantics across Core/Fleet/Agent. |
| Idempotency | `(actor_id, idempotency-key hash)` plus operation fingerprint; pending secret replay ends on redemption/expiry | Confirm scope, retention, and response-loss behavior with Core. |
| Agent recovery | Stable attempt ID plus separate 32-byte-minimum proof; encrypted response escrow expires after configured TTL | Confirm proof generation/storage and recovery window with Agent; Fleet enrollment is an explicit Agent CLI mode while Core enrollment remains the default. |
| Identity lifecycle | One active credential per `agent_id`; no implicit rebind | Define credential rotation and recovery delivery, Agent reinstall/migration, revoke authorization/idempotency, and duplicate Node/Agent policy. Cancellation and two-phase gate behavior are implemented but remain provisional cross-service contract details. |
| Core lifecycle gate | Close returns a versioned prior-state and active-binding snapshot; completion commits or compensates only the matching operation/version. | Agree on exact wire/status semantics and Core recovery behavior for unknown outcomes and races. |
| Supervisor continuity | Existing verifier requires the authenticated Agent's pinned UID | Production use still depends on cross-system security and contract review. |
| Secret key management | Static 32-byte AES key supplied by environment, not logged or serialized | Select production secret-manager source, rotation/versioning, backup and key-loss recovery. |

## Evidence and limits

Verified locally on 2026-11-23:

- `go test -race -count=1 ./...` passed on the host.
- `go test -race -tags=integration -timeout=12m -count=1 ./...` passed with
  the host Mongo URI loaded from `.env.local`; required integrations did not
  skip.
- `go vet ./...`, `golangci-lint run` (0 issues), and
  `go build -buildvcs=false ./...` passed. The VCS-aware build flag is disabled
  because this checkout cannot read the nested `docs/shared` git metadata.
- `go test -race -tags=supervisor,integration -timeout=12m -count=1 -v
  ./tests/supervisor` passed against checksum-verified official Supervisor
  v0.160.0 (`df2dfae96d6b711db31675d2deb8493d3dbada8561b25fe575fd90bd7cac8449`)
  and host Mongo. This covered Fleet-issued credential enrollment through
  `Application.Run`, authenticated OpAMP connection, persisted UID binding,
  shutdown, startup failure cleanup, index-conflict fail-closed behavior,
  retained-state reconnect, and fresh-state UID conflict.
- `git diff --check` passed. The implementation branch is
  `feat/agent-registration`.

Still open before calling cross-system Agent Registration complete:

- A coordinated Agent adapter and Core/Fleet contract review. The current
  Agent keeps Core enrollment as its default and has an explicit Fleet enrollment
  CLI path. Local UAT and tests do not approve the shared wire proposal or enable
  production OpAMP.
- Hosted CI evidence. Local checks passed; no hosted run has been executed for
  this branch yet.
