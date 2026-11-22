# Fleet Manager service entrypoint Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Add a runnable `fleet-manager` command that starts the existing application with validated configuration and signal-driven shutdown.

**Architecture:** Keep process concerns in `cmd/fleet-manager`; delegate composition and resource lifecycle to `internal/app.Application`. Use existing config and logging APIs and standard-library signal handling.

**Tech Stack:** Go, `os/signal`, `context`, `log/slog`, existing app/config/logging packages.

**Spec:** [2026-11-22-fleet-manager-entrypoint-design.md](../specs/2026-11-22-fleet-manager-entrypoint-design.md)

## Global Constraints

- `OPAMP_ENABLED` remains false by default; the entrypoint cannot bypass application validation, authentication, or TLS.
- Never log config values, environment contents, Mongo driver details, or secrets.
- Add no dependencies or business policy to the command package.
- Preserve Windows and Linux builds.

## Review Focus

- Invalid settings fail before opening external resources.
- Interrupt/termination cancellation reaches `Application.Run`, which owns bounded cleanup.
- Bootstrap and runtime errors produce a nonzero process exit without exposing secret values.

---

### Task 1: Add and verify the command entrypoint

**Files:**
- Create: `cmd/fleet-manager/main.go`
- Create: `cmd/fleet-manager/main_test.go`
- Modify: `docs/local/architecture/runtime-configuration-and-application.md`
- Modify: `docs/local/plans/fleet-manager-roadmap.md`
- Modify: `docs/superpowers/specs/2026-11-22-fleet-manager-entrypoint-design.md`

**Interfaces:**
- Consume `app.LoadConfig(app.LookupEnv)`, `logging.New(io.Writer, logging.Options)`, and `app.NewApplication(app.Config, *slog.Logger)`.
- Provide a testable `run(context.Context, app.LookupEnv, io.Writer) (*slog.Logger, error)` helper; `main` owns signal registration and nonzero process exit behavior.

- [x] **Step 1: Create `cmd/fleet-manager/main_test.go` with `TestRunRejectsInvalidConfigBeforeRuntimeStartup`**. Pass an invalid `FLEET_LOG_LEVEL` and a sentinel secret through the injected lookup; require a configuration error and verify it does not reveal the sentinel in the returned error or output.
- [x] **Step 2: Run the focused test**: `go test ./cmd/fleet-manager -run '^TestRunRejectsInvalidConfigBeforeRuntimeStartup$' -count=1`. Expected: compile failure because `run` is undefined; observed `undefined: run`.
- [x] **Step 3: Implement `run` and `main`**. Load config, create a configured logger on stderr, construct `Application`, and call `Run`. `main` uses `signal.NotifyContext` for `os.Interrupt` and `syscall.SIGTERM`, stops signal delivery on return, logs failures through stderr, and exits nonzero only after cleanup has returned.
- [x] **Step 4: Run focused native Windows tests and a Linux cross-compile**: `go test ./cmd/fleet-manager -count=1` and `GOOS=linux GOARCH=amd64 go test -c ./cmd/fleet-manager`. Expected: both pass; native tests and Linux cross-compile passed.
- [x] **Step 5: Update runtime docs and roadmap** to show the command path and mark only the command-entrypoint FM-01 item complete; keep FM-01 IN PROGRESS while Mongo migration/deployment topology is incomplete.
- [x] **Step 6: Run full checks**: `go test -count=1 ./...`, `go test -race -count=1 ./...`, `go vet ./...`, `go build ./...`, `golangci-lint run ./...`, and `go mod tidy -diff`. Expected: all pass with no dependency changes.
- [x] **Step 7: Review the diff, commit the entrypoint change, and push the PR branch.**
