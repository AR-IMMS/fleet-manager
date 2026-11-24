# Fleet API OpenAPI and Swagger UI Implementation Plan

> **Approved for implementation on 2026-11-24.**

## Goal

Give local Fleet UAT a browsable OpenAPI document and interactive Swagger UI for routes that exist in the Fleet implementation, with the docs surface disabled by default.

## Scope

- Add one OpenAPI 3.1 YAML source describing health and the currently implemented Fleet enrollment routes.
- Mark enrollment details as provisional and describe only observed request, response, and authentication behavior.
- Serve `/openapi.yaml` and `/docs/` through embedded handlers only when `FLEET_API_DOCS_ENABLED=true`.
- Keep existing route registration, TLS/mTLS, business behavior, and Core Swagger unchanged.
- Update local configuration and README guidance, including the fact that Core-owned invitation/binding operations remain in Core at `/api/docs`.

## Work items

1. Add a platform `httpapi` handler that embeds the spec and embedded Swagger UI assets.
2. Add a default-false docs flag to platform config and mount docs routes from the app router behind that flag.
3. Describe the implemented route inventory, mTLS/proof requirements, schemas, and fake examples in OpenAPI.
4. Add opt-in setup/troubleshooting guidance to `.env.example` and README; do not modify shared-docs gitlink or business endpoints.
5. Format and compile/lint the changed Go code; preserve unrelated worktree files.

## Verification boundary

The OpenAPI document is a view of current implementation, not an approved cross-repository contract. Swagger UI cannot bypass TLS, mTLS, invitation proof, or route activation gates. Core public endpoints are documented by Core, not duplicated here.
