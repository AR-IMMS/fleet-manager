# Identity verification and Supervisor UID binding

**Implementation status:** Mongo-backed verification and guarded UID binding are implemented. OpAMP is still not activated by `Application.Run`. The exact credential wire contract remains unapproved. Official Supervisor v0.160.0 UID continuity and Mongo binding both passed locally on Windows; hosted Ubuntu and Windows CI results are pending.

## Ownership and identifiers

- **ACCEPTED by C01 Enrollment, Installation Identity & Credential Lifecycle:** `agent_id` reuses the Agent's persisted installation UUID. Fleet verifies credentials and owns revocation/session authorization. Host evidence does not authenticate an installation.
- **ACCEPTED by C04 OpAMP Integration Profile:** authenticate before binding; Supervisor `instance_uid` is a protocol identifier, not proof of identity. Do not silently remap a conflicting UID.
- `agent_id` and Supervisor `instance_uid` remain separate values. The current Mongo adapter stores the UID as the hex encoding of the exact 16 OpAMP bytes; it does not derive one identifier from the other.
- **Locally verified on Windows:** official Supervisor v0.160.0 retained the same UID after restart with the same state directory and produced a different UID with a fresh directory. Ubuntu CI and behavior across upgrade, reinstall, and other state-loss cases remain unverified. The [pinned Supervisor v0.160.0 README](https://github.com/open-telemetry/opentelemetry-collector-contrib/blob/cmd/opampsupervisor/v0.160.0/cmd/opampsupervisor/README.md) documents its persistent storage directory.

## Design and request flow

```mermaid
classDiagram
direction TB
    class Application {
        +Run(ctx) error
    }
    class IdentityVerifier {
        <<application port>>
        +Verify(ctx, credential) (Principal, error)
    }
    class Authenticator {
        <<application service>>
        +Authenticate(ctx, credential) (Principal, error)
    }
    class ConnectionAuthenticator {
        <<delivery port>>
        +Authenticate(ctx, credential) (Principal, error)
    }
    class SessionAuthorizer {
        <<application port>>
        +BindAndAuthorize(ctx, principal, uid) error
    }
    class MongoVerifier {
        -identity_installations collection
        +Verify(ctx, credential) (Principal, error)
        +BindAndAuthorize(ctx, principal, uid) error
        +StoreActiveCredential(ctx, agentID, credentialID, token) error
        +Revoke(ctx, credentialID) error
    }
    class Principal {
        -agentID string
        -credentialID string
    }
    class CallbackAdapter {
        -ConnectionAuthenticator authenticator
        -SessionAuthorizer authorizer
        +Callbacks() SDKCallbacks
    }
    class IdentityInstallationRecord {
        +agent_id string
        +credential_id string
        +credential_hash string
        +status ACTIVE|REVOKED
        +instance_uid optional hex
    }

    Application --> MongoVerifier : constructs, listener remains disabled
    MongoVerifier ..|> IdentityVerifier
    MongoVerifier ..|> SessionAuthorizer
    MongoVerifier --> IdentityInstallationRecord : atomic conditional update
    CallbackAdapter --> ConnectionAuthenticator : authenticate before accept
    ConnectionAuthenticator <|.. Authenticator
    Authenticator --> IdentityVerifier
    CallbackAdapter --> SessionAuthorizer : bind/check every message
    IdentityVerifier --> Principal : returns verified agent and credential IDs
```

The credential token is hashed with SHA-256 before storage. This implementation assumes a high-entropy token of at least 32 bytes; the enrollment flow must generate it randomly and return it only to its authorized recipient. Raw tokens are not written to MongoDB or logs. The adapter creates unique indexes for credential hash, credential ID, `agent_id`, and non-empty Supervisor UID. Expiration is not represented or checked yet: an active record remains valid until explicitly revoked. This is a deliberate limit while C01 expiry/rotation rules are OPEN, and another reason the listener remains inactive.

### Binding and reconnect behavior

1. **First binding:** `OnConnecting` verifies the bearer token and produces a `Principal` containing the authenticated `agent_id` plus a non-secret credential record ID. The first message must have a 16-byte UID. A conditional update stores that UID only while the credential is active.
2. **Reconnect:** the same active credential and same UID continue to authorize. A different UID for the installation conflicts. A UID already owned by another installation conflicts through the unique index.
3. **Conflict:** reject the message, close the connection, and do not rewrite the mapping. The client-reported UID never changes the authenticated `agent_id`.
4. **Recovery/rebinding:** no automatic remap or self-service reset is implemented. A future rebind requires a separately authorized recovery operation with audit and an expected-current-UID check. This avoids treating possession of a possibly copied credential as sufficient proof that Supervisor state was intentionally replaced.
5. **Revocation race:** binding/check and revocation update the same Mongo document. Mongo's single-document atomic update determines the order: a binding operation that wins first may complete; if revoke wins first, binding/check fails. Each subsequent OpAMP message rechecks the active credential and stored UID; on revoke or store failure, the adapter closes the connection and returns no protocol response. A message already authorized before the revoke update cannot be retroactively withdrawn. Active socket closure is therefore enforced at the next callback, not by a separate immediate session registry.

Mongo identity record writes and unique indexes do not depend on multi-document transactions. The credential and UID mapping share one document specifically to make status, credential, and mapping checks atomic at that boundary. If outbound configuration or control messages are added later, their delivery path must apply an equivalent authorization/revocation fence before sending; it does not exist in this bootstrap adapter.

## Code locations

- Application boundary: `internal/contexts/identity/application/authentication.go` and `session_authorization.go`.
- Principal: `internal/contexts/identity/domain/principal.go`.
- Mongo implementation: `internal/contexts/identity/infrastructure/mongodb/verifier.go`.
- OpAMP callback enforcement: `internal/contexts/identity/delivery/opamp/callbacks.go`.
- Composition constructs the verifier and authenticator, but does not start or register the OpAMP listener: `internal/app/wiring.go` and `internal/app/application.go`.

`StoreActiveCredential` and `Revoke` are persistence capabilities only; there is no enrollment issuance endpoint, Core authorization adapter, rotation flow, or production revocation API yet. Their callers must be added behind the approved C01 flow and must enforce Core/Fleet service authorization.

## Verification and evidence

- Unit tests cover authenticated principal shape, invalid UID rejection, per-message authorization, disconnect on revocation, and error classification.
- The Mongo integration test (`go test -tags=integration ./internal/contexts/identity/infrastructure/mongodb`) covers first bind, same-UID reconnect, changed UID conflict, cross-installation UID conflict, concurrent bind/revoke, revoked credential rejection, and token redaction in storage. It skips unless `FLEET_MONGODB_URI` or `MONGODB_URI` is set; a skipped run is not persistence evidence.
- **Pinned Supervisor UID continuity test: IMPLEMENTED.** `.github/workflows/supervisor-identity.yml` has a separate `uid-continuity` job on Ubuntu and Windows. It downloads the official `cmd/opampsupervisor/v0.160.0` binary, verifies its published SHA-256 sidecar and module build information, and runs `go test -tags=supervisor ./tests/supervisor`. The test starts the real binary against a local Fleet OpAMP server, records the 16-byte UID from authenticated messages, restarts with the same `storage.directory`, and asserts equality; it then starts with a fresh directory and asserts a different UID. A local Windows run passed. Hosted Ubuntu and Windows job results remain pending. Cleanup stops Supervisor and its test Collector child and removes temporary files; failures save redacted Supervisor logs as a short-retention CI artifact.
- **Fleet/Mongo binding test: IMPLEMENTED.** A separate Ubuntu job starts MongoDB and runs `go test -tags=supervisor,integration ./tests/supervisor`. It repeats first bind and retained-state reconnect against the Mongo-backed verifier, then confirms a fresh-state UID is rejected as a conflict and does not replace the original binding. The test passed locally on Windows against the running MongoDB container. Hosted Ubuntu results remain pending. This evidence is distinct from UID continuity and from the existing Mongo-only tests.
- These acceptance tests observe UID behavior; they do not approve a reset/rebind policy or prove production interoperability. Local execution requires the pinned Supervisor binary and the test Collector executable; Fleet/Mongo execution also requires `FLEET_MONGODB_URI`.
- The existing native `opamp-go` client integration tests are adapter tests, not a pinned official Supervisor test.

## Contract decisions still open

- **C01 Enrollment, Installation Identity & Credential Lifecycle:** exact credential format/entropy/expiry, credential provisioning and rotation, Agent HTTPS versus Supervisor WSS audience/scope, Core authorization transport, credential retention and operational rebind approval.
- **C04 OpAMP Integration Profile:** whether retained storage preserves UID across upgrade/reinstall/platform migration; exact UID provisioning/reset behavior in pinned Supervisor v0.160.0; duplicate-UID scope and operational remediation; whether revocation must actively terminate sockets rather than deny their next callback.
- These implementation choices are proposals for review. They do not approve C01/C04 endpoints, error codes, payload fields, or production interoperability.
