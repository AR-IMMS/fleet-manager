# Essential end-to-end flows

**Goal:** explain what happens, who is responsible and what counts as success. All diagrams are **target behavior**, not an assertion that code exists.

## 1. Connection, authentication and session ownership

```mermaid
sequenceDiagram
  participant Client as OpAMP Client / Supervisor
  participant Server as OpAMP Server
  participant Auth as Identity Verifier (Core adapter)
  participant Registry as Session Registry
  Client->>Server: OpAMP transport connect with credential
  Server->>Auth: Verify credential and allowed installation
  alt Invalid / revoked / verifier unavailable
    Auth-->>Server: Reject or unavailable
    Server-->>Client: Reject connection (fail closed)
  else Authenticated
    Auth-->>Server: Authorized principal
    Server->>Registry: Claim logical instance session
    Registry-->>Server: Session admitted or conflict
    Server-->>Client: Connected (if admitted)
    Client->>Server: AgentToServer capabilities/status
    Server->>Registry: Update activity and metadata
  end
  Client--xServer: Disconnect
  Server->>Registry: Release only this session's ownership
```

**Acceptance:** revoked credentials rejected; stale close cannot evict a newer reconnect; sessions never keyed solely by unverified UID. Local harness may substitute a deterministic fake identity verifier, but cannot present itself as production auth.

## 2. Reporting health, descriptions and capabilities

```mermaid
sequenceDiagram
  participant Client as OpAMP Client
  participant Server as OpAMP Server
  participant Core as Core integration port
  Client->>Server: AgentToServer: description + health + capabilities
  Server->>Server: Validate, redact, normalize
  Server->>Core: Agent status observed (idempotent correlation)
  Core-->>Server: Received / retryable failure
  Server-->>Client: ServerToAgent with supported server capabilities
```

**Acceptance:** capability flags are not misrepresented; known fields survive normalization; unknown extensions are safe; duplicate messages do not invent new installations. `online` means a valid live session; healthy means the reported health state, and is not equivalent to a good telemetry exporter.

## 3. Remote configuration and outcome tracking

```mermaid
sequenceDiagram
  actor Admin
  participant Core as Core: desired state owner
  participant Server as OpAMP Server
  participant Client as OpAMP Supervisor
  participant Collector as OTel Collector
  Admin->>Core: Change approved desired configuration
  Core->>Core: Authorize and create revision
  Core->>Server: Request delivery to authenticated target
  Server->>Server: Check session + AcceptsRemoteConfig
  alt Unsupported or offline
    Server-->>Core: Not delivered / pending, with reason
  else Supported
    Server->>Client: ServerToAgent remote_config + hash
    Server-->>Core: Delivery attempted (NOT applied)
    Client->>Collector: Apply via Supervisor-managed mechanism
    Client-->>Server: RemoteConfigStatus + last hash
    Server-->>Core: Report applied / failed + hash
  end
```

**Acceptance:** statuses are correlated to exact config hash/revision. Sending is not applying. Do not guess success if client is offline, omits status, or reports an unexpected hash.

## 4. Reconnection and desired-state reconciliation

```mermaid
sequenceDiagram
  participant Client as Supervisor
  participant Server as OpAMP Server
  participant Core as Core
  Client-xServer: Network interruption
  Note over Client: Collector runtime may continue independently
  Client->>Server: Reconnect + authenticate
  Server->>Core: Resolve latest desired revision / hash
  Core-->>Server: Desired revision
  alt Same effective configuration
    Server-->>Client: No redundant configuration
  else Drift or unknown state
    Server-->>Client: Re-deliver supported desired config
    Client-->>Server: Apply status (when supported)
  end
```

**Acceptance:** new session wins according to explicit policy, old disconnect cannot delete it; no regression to stale desired revision; retries are bounded and observable.

## 5. Negative paths to test

| Situation | Expected outcome |
|---|---|
| Revoked credential | Reject; no session |
| Duplicate UID from different authenticated principals | Reject or isolate per reviewed identity policy |
| Client lacks remote-config capability | Do not send; report unsupported |
| Unexpected / old config hash | Do not mark latest revision applied |
| Status event delivery to Core fails | Retry/buffer per bounded design; record failure, do not claim delivered |
| Server restarts | Agents reconnect and reconcile with Core source of truth |
| Malformed/oversized OpAMP message | Reject safely, no crash or sensitive log dump |
