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

- Go 1.24. Module path `snivur/v0`. The only third-party dependency is `github.com/go-chi/chi/v5` for routing; everything else is the standard library.
- Routing uses chi, e.g. `r.Post("/servers/{id}/stop", h)`. chi v5 populates `r.PathValue`, so handlers read path parameters with `r.PathValue("id")`. `main` wraps each binary in chi's `middleware.Logger` and `middleware.Recoverer`.
- Every route on both binaries requires an `X-API-Key` header, including `/health`.
- Every handler writes **exactly one** response. JSON responses set `Content-Type: application/json`.
- Error body shape everywhere: `{"error": "<message>"}`.
- Shared wire types live in `shared/`; binaries are `api/` (controller) and `agent/`.
- Everything that shells out goes through an injectable command runner so it is testable without Docker.
- `go build ./...`, `go vet ./...`, and `go test -race ./...` must pass on every commit.
