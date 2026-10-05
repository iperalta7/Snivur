# Architecture Decision Records

Each feature ships as a single commit containing its ADR, implementation, and tests.

| ADR | Title | Status |
| --- | ----- | ------ |
| [0001](0001-non-blocking-launch-and-config.md) | Non-blocking container launch and runtime configuration | Accepted |
| [0002](0002-async-server-lifecycle.md) | Async server lifecycle with a state machine | Accepted |
| [0003](0003-agent-heartbeats-and-health.md) | Agent heartbeats and health tracking | Accepted |
| [0004](0004-reconciliation-and-multi-agent-routing.md) | Desired/actual state reconciliation and multi-agent routing | Accepted |

## Format

Each ADR has: **Context** (why), **Decision** (what), **Specification** (exact contracts an implementer follows), **Acceptance criteria** (what a tester verifies), and **Consequences** (trade-offs, follow-ups).

## Conventions (apply to every ADR)

- Go 1.24, standard library only. Module path `snivur/v0`.
- Routing uses Go 1.22+ `http.ServeMux` patterns, e.g. `mux.HandleFunc("POST /servers/{id}/stop", h)`.
- Every handler writes **exactly one** response. JSON responses set `Content-Type: application/json`.
- Error body shape everywhere: `{"error": "<message>"}`.
- Shared wire types live in `shared/`; binaries are `api/` (controller) and `agent/`.
- Everything that shells out goes through an injectable command runner so it is testable without Docker.
- `go build ./...`, `go vet ./...`, and `go test -race ./...` must pass on every commit.
