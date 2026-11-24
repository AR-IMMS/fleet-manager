# Fleet API OpenAPI and Swagger UI design

## Intent

Add an interactive, local test surface for Fleet's implemented HTTP routes. Core already serves Swagger at `/api/docs`; this change is Fleet-only. It does not add routes, change authorization, or declare provisional C01 enrollment details approved.

## Scope and constraints

- Describe only routes present in the current Fleet `dev` implementation: health, Core mTLS invitation/gate/administration routes, and Agent invitation exchange/recovery routes.
- Mark enrollment wire details as provisional in the document and UI. Examples use fake values; no secrets or production endpoints.
- Serve the OpenAPI 3.1 document and Swagger UI only when `FLEET_API_DOCS_ENABLED=true`; default remains disabled.
- Keep the spec as one embedded YAML source served at `/openapi.yaml`; serve the UI at `/docs` and point it to that same-origin spec so local `Try it out` requests use the running Fleet origin.
- Preserve TLS and mTLS requirements. Do not expose a docs-only bypass or mock service identity.
- Work from the current remote `dev` head (`c9326ee` at inspection), not the stale local `infra/docker-compose` checkout (`221de66`). Preserve its unrelated untracked files.

## Approaches considered

1. **OpenAPI plus opt-in embedded Swagger UI (selected):** provides a browsable spec and `Try it out` without changing business handlers; the explicit flag limits accidental exposure.
2. **OpenAPI file only:** smallest runtime change, but requires a separate viewer and is less convenient for this user's end-to-end UAT.
3. **Generate docs from Go annotations:** couples route source to documentation and adds generation workflow; not a fit for the current chi routes and provisional cross-service wire details.

## Runtime and file shape

- Add the OpenAPI YAML beside the Fleet HTTP documentation handler and embed it once.
- Add a platform HTTP docs handler for `GET /openapi.yaml` and the Swagger UI assets.
- Add a default-false docs setting and mount those handlers from the app router only when enabled.
- Keep Core Swagger unchanged at `/api/docs`; Core invitation creation and binding synchronization remain tested through Core.
- Document which operations require Core's client certificate and which Agent exchange operations use the invitation/recovery proof.

## Acceptance checks

- With docs disabled (default), the UI and spec endpoints are unavailable and existing health/enrollment routes behave as before.
- With docs enabled, `/docs` loads and points to `/openapi.yaml`; the OpenAPI document parses and contains the implemented route/method inventory, security schemes, request/response schemas, and non-secret examples.
- Docs do not weaken TLS/mTLS, alter route availability, or expose credential material in static content.

## Builder calls

- The docs flag and route names are local implementation choices; the user asked for Fleet interactive docs and Core already has Swagger.
- The OpenAPI document describes current implementation behavior, not an approved cross-repository contract. C01/C04 wire details stay marked provisional.
- After approval, implement on a new `feat/fleet-swagger-docs` branch from the latest remote `dev` head.
