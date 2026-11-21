# Runtime configuration and application

**Status:** describes the bootstrap code currently implemented. It is a small composition foundation, not a complete Fleet runtime.

## Package responsibilities

- `internal/platform/env` reads injected environment values and parses durations. It distinguishes absent settings (use the caller's default) from explicitly empty required settings (error). Optional settings become empty strings when absent or empty.
- `internal/platform/config` owns the full runtime `Config`, environment names/defaults, and validation for HTTP, OpAMP, MongoDB, and logging settings.
- `Config` embeds `OpAMPConfig`; existing flat JSON/YAML names and promoted Go field selectors are preserved.
- `internal/app/config.go` provides aliases and `LoadConfig` as the application-facing facade. The configuration type itself currently lives in `platform/config`.
- `internal/app` validates inputs, assembles runtime dependencies, coordinates startup and cleanup, and owns readiness state.
- `internal/platform/mongodb` and `internal/platform/http-server` own the database and HTTP listener mechanics.

Configuration loading validates the settings before application construction. `NewApplication` validates again and requires a logger. It does not open external resources. Configuration can contain secrets, especially the MongoDB URI, so do not log or serialize it.

## Startup and shutdown today

`Application.Run` is single-use. It creates a local lifecycle registry and calls `wireRuntime`. Wiring opens MongoDB, registers its close operation, creates a readiness checker backed by Mongo ping, builds the health router, then starts the HTTP server. On every exit path, the application marks readiness as draining and calls registered cleanup steps in reverse acquisition order under one bounded shutdown context.

The running HTTP routes are `/health/live` and `/health/ready`. Readiness returns a generic unavailable response if Mongo is unhealthy or shutdown has begun. The application currently waits for context cancellation or an unexpected HTTP serving error. It does not yet start an OpAMP listener or background worker.

## Class diagram

```mermaid
classDiagram
direction TB
    class Config {
        +OpAMPConfig embedded
        +HTTPConfig HTTP
        +MongoConfig Mongo
        +LoggingConfig Logging
        +Duration ShutdownTimeout
        +LoadConfig(lookup) (Config, error)
        +Validate() error
    }
    class OpAMPConfig {
        +string ListenAddress
        +string OpAMPPath
        +string TLSCertFile
        +string TLSKeyFile
        +int64 MaxMessageBytes
        +Duration AuthTimeout
        +Validate() error
    }
    class envReader {
        <<Reader>>
        -LookupEnv lookup
        +Setting(key, fallback) (string, error)
        +Optional(key) string
        +Duration(key, fallback) (Duration, error)
    }
    class Application {
        -Config config
        -Logger logger
        -atomic.Bool started
        +NewApplication(cfg, logger) (*Application, error)
        +Run(ctx) error
    }
    class lifecycle {
        -[]shutdownStep steps
        +add(name, stop)
        +stopAll(ctx) error
    }
    class runtimeState {
        -FleetReadiness readiness
        -HTTPServer http
    }
    class HTTPServer {
        +Start(ctx) error
        +Shutdown(ctx) error
    }
    class Router {
        +GET /health/live
        +GET /health/ready
    }
    class HealthHandler {
        -ReadinessChecker checker
        +Live(w, r)
        +Ready(w, r)
    }
    class FleetReadiness {
        -[]ReadinessCheck checks
        -atomic.Bool draining
        +Check(ctx) error
        +BeginDrain()
    }
    class MongoDatabase {
        +Ping(ctx) error
        +Close(ctx) error
    }
    class ReadinessChecker {
        <<interface>>
        +Check(ctx) error
    }
    envReader ..> Config : supplies environment values
    Config *-- OpAMPConfig : embeds
    Application --> Config : stores validated config
    Application ..> lifecycle : creates during Run
    Application ..> runtimeState : builds through wireRuntime
    runtimeState *-- FleetReadiness
    runtimeState *-- HTTPServer
    HTTPServer --> Router : serves
    Router *-- HealthHandler
    HealthHandler --> ReadinessChecker
    FleetReadiness ..|> ReadinessChecker
    FleetReadiness --> MongoDatabase : checks ping
    lifecycle o-- MongoDatabase : closes after HTTP server
    lifecycle o-- HTTPServer : shuts down first
```

## Reading the diagram

The lifecycle registry is local to each `Run`; it is not a field on `Application`. MongoDB and the HTTP server are acquired in that order, so reverse cleanup shuts HTTP down before MongoDB. The app layer owns this sequencing; platform packages do not import `app`.

## Not implemented yet

The [OpAMP server component](opamp-server.md) now implements `Start()` / `Stop(ctx)` and authenticated callbacks, but is not wired into `Application.Run`: a production identity verifier is still required. There is no `BackgroundWorker` interface or full Fleet API wiring. Keep config defaults in their owning configuration sections and reusable environment parsing in `platform/env`.
