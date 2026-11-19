# Local architecture decisions

Keep service-level ADRs short. An accepted ADR records context, decision, consequences, alternatives and date. Use `0001-short-title.md`; mark drafts `Proposed` until reviewed.

Local ADRs may choose Fleet-internal persistence, outbox, session, deployment, and runtime designs **within** the accepted ownership in [shared Fleet architecture](../../shared/architecture/fleet-management/overview.md). They cannot approve C01–C04 wire schemas, auth scopes, UID mapping, composition or migration on behalf of Core and Agent owners. Record those decisions in the [canonical contract index](../../shared/contracts/fleet-management/README.md).

Useful first ADR topics: durable state/outbox and atomicity assumptions; session ownership and reconnect races; storage/recovery for enrollment results; publication hash canonicalization after cross-system agreement.
