# ADR 0003: Agent heartbeats and health tracking

- **Status:** Accepted
- **Date:** 2026-10-05
- **Depends on:** ADR 0002

## Context

The controller cannot tell whether an agent is alive or what it is actually running. A dead host looks identical to a healthy one until a command fails. We also want agents to initiate contact, since that is the precondition for eventually supporting hosts behind NAT.

## Decision

Agents **push** a heartbeat to the controller on a fixed interval. Each heartbeat carries the agent's identity, its reachable address, and the containers it currently runs. The controller keeps an agent registry and a background sweeper that derives health from the time since the last heartbeat:

- `healthy`: the last heartbeat arrived ≤ 30s ago.
- `unhealthy`: ≤ 90s ago.
- `offline`: more than 90s ago.

## Specification

### `shared/`

```go
type ContainerInfo struct {
    ServerID    string `json:"server_id"`
    ContainerID string `json:"container_id"`
    State       string `json:"state"` // docker state, e.g. "running", "exited"
}

type Heartbeat struct {
    AgentID    string          `json:"agent_id"`
    Hostname   string          `json:"hostname"`
    Address    string          `json:"address"` // base URL the controller uses to reach this agent
    Version    string          `json:"version"`
    Containers []ContainerInfo `json:"containers"`
    // ContainersError is set when the agent could not list its containers.
    // Containers is then empty but MUST NOT be read as "no containers".
    ContainersError string     `json:"containers_error,omitempty"`
    SentAt     time.Time       `json:"sent_at"`
}

type AgentStatus string
const (
    AgentHealthy   AgentStatus = "healthy"
    AgentUnhealthy AgentStatus = "unhealthy"
    AgentOffline   AgentStatus = "offline"
)

type Agent struct {
    ID         string          `json:"id"`
    Hostname   string          `json:"hostname"`
    Address    string          `json:"address"`
    Version    string          `json:"version"`
    Status     AgentStatus     `json:"status"`
    LastSeen   time.Time       `json:"last_seen"` // controller clock, not SentAt
    Containers []ContainerInfo `json:"containers"`
}
```

### Agent

`Runtime` gains `List(ctx) ([]shared.ContainerInfo, error)`, implemented as:

```
docker ps -a --filter label=snivur.server_id --format {{.ID}}\t{{.Label "snivur.server_id"}}\t{{.State}}
```

The output is parsed line by line. Blank lines are skipped, and malformed lines are logged and skipped.

Heartbeat loop (`agent/heartbeat.go`):

- `type Heartbeater struct { ControllerURL, APIKey string; Interval time.Duration; Runtime Runtime; Client *http.Client; Identity ... }`
- `Run(ctx)` sends one heartbeat immediately, then one on every tick until `ctx` is cancelled.
- It calls `POST {ControllerURL}/agents/heartbeat` with `X-API-Key`.
- Each `List` call gets its own timeout of 5s (`listTimeout`) derived from `ctx`, so a hung `docker ps` cannot stop heartbeats.
- If `List` fails or times out, it still sends the heartbeat, with `containers: []` and `containers_error` set to the error text, and logs the error. A failed send is logged and retried on the next tick. It never panics or exits.
- The HTTP client timeout is 5s.

Environment (new):

| Var | Default | Notes |
| --- | ------- | ----- |
| `SNIVUR_AGENT_ID` | `os.Hostname()` | |
| `SNIVUR_CONTROLLER_URL` | — | if empty, heartbeats are disabled and a warning is logged |
| `SNIVUR_AGENT_ADVERTISE_URL` | `http://localhost` + port from `SNIVUR_AGENT_ADDR` | |
| `SNIVUR_HEARTBEAT_INTERVAL` | `10s` | parsed with `time.ParseDuration` |

The agent version is a package-level `var Version = "dev"`, which can be overridden with `-ldflags`.

### Controller registry (`api/registry.go`)

```go
type HealthThresholds struct{ Unhealthy, Offline time.Duration } // defaults 30s, 90s

type Registry struct { /* RWMutex, map[string]*shared.Agent, now func() time.Time, thresholds */ }

func NewRegistry(now func() time.Time, t HealthThresholds) *Registry
func (r *Registry) Upsert(hb shared.Heartbeat) shared.Agent   // sets LastSeen=now(), Status=healthy
func (r *Registry) Get(id string) (shared.Agent, bool)
func (r *Registry) List() []shared.Agent                      // sorted by ID
func (r *Registry) Sweep() []StatusChange                     // recompute every agent's status; return those that changed

type StatusChange struct{ AgentID string; From, To shared.AgentStatus }
```

- Copy semantics are the same as in the `Store`: the `Containers` slice is cloned.
- The sweeper goroutine calls `Sweep()` every 5s and logs each `StatusChange`. In tests, `Sweep()` is called directly with a fake clock, so no test sleeps for real thresholds.

### Controller endpoints

| Method & path | Auth | Behavior |
| ------------- | ---- | -------- |
| `POST /agents/heartbeat` | agent key (`SNIVUR_AGENT_API_KEY`), not the controller key | Validate that `agent_id` and `address` are non-empty (400 otherwise). Then `Upsert` and return 204 |
| `GET /agents` | controller key | 200 `[]Agent` (`[]` when empty) |
| `GET /agents/{id}` | controller key | 200 / 404 |

## Acceptance criteria

1. `List` parses a sample of `docker ps` output, including a blank line and a malformed line, into the correct `[]ContainerInfo`.
2. With a fake clock, an agent is `healthy` at +0s and +30s, `unhealthy` at +31s, and `offline` at +91s. A new heartbeat sets it back to `healthy`.
3. `Sweep()` returns only agents whose status actually changed. A second sweep at the same time returns nothing.
4. `Heartbeater.Run` against an `httptest.Server` sends one heartbeat immediately and more on each tick (use a 10ms interval), includes the API key, and stops when `ctx` is cancelled.
5. The heartbeater survives a controller returning 500 and a `Runtime.List` error, and keeps sending.
   - A `List` error sends `containers: []` with a non-empty `containers_error`.
   - A successful `List` omits `containers_error`.
   - A `List` that blocks until its context is cancelled is cut off by `listTimeout`, and the heartbeat is still sent. In tests, `listTimeout` may be a package var.
6. `POST /agents/heartbeat` with a bad key returns 401, with a missing `agent_id` returns 400, and with a valid heartbeat returns 204. The agent then appears in `GET /agents`.
7. `go test -race ./...` passes with concurrent `Upsert`, `Sweep`, and `List` calls.

## Consequences

- Detection latency for a failed agent is at most 30s plus one sweep interval (5s).
- Commands still flow from the controller to the agent, so agents must remain reachable at `Address`. A full NAT solution (commands delivered over a long-lived agent-initiated stream) is future work. This ADR establishes the agent-initiated half.
- `containers_error` lets ADR 0004 skip reconciliation for that heartbeat instead of failing every server on the agent (found in testing).
- Using a single shared key means any agent can impersonate another agent ID. Per-agent credentials are future work.
