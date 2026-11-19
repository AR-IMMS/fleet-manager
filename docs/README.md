# Documentation map

| Area | Purpose | Authority |
|---|---|---|
| [`shared/architecture/fleet-management`](shared/architecture/fleet-management/overview.md) | Accepted cross-system Fleet ownership | `AR-IMMS/shared-docs` |
| [`shared/contracts/fleet-management`](shared/contracts/fleet-management/README.md) | C01–C04 decisions, draft wire proposals and open questions | `AR-IMMS/shared-docs`; follow each decision's status |
| [`local/architecture`](local/architecture/system-overview.md) | Fleet repository structure and target flows | This repo, subordinate to accepted shared decisions |
| [`local/development`](local/development/CODING_POLICY.md) | Implementation and quality rules | This repo |
| [`local/decisions`](local/decisions/README.md) | Service-level ADRs | This repo; cannot approve cross-system contracts alone |

Read the shared Fleet contract index before changing a cross-system boundary. Some older shared pages describe the former OpAMP-only adapter model; C01–C04 and the Fleet architecture overview describe the accepted B+ target. The implementation status must still be checked in code.
