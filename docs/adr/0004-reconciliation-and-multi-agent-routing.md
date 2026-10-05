# ADR 0004: Desired/actual state reconciliation and multi-agent routing

- **Status:** Accepted
- **Date:** 2026-10-05
- **Depends on:** ADR 0003

## Context

The controller now knows two things:

- **Desired state**: the server store (ADR 0002).
- **Actual state**: the containers each agent reports in its heartbeats (ADR 0003).

Nothing compares the two yet. A container that crashes, or that someone removes with `docker kill`, still shows as `running` forever. Commands also still go to the single `SNIVUR_AGENT_URL`, so only one host can be managed.

## Decision

1. Each server is bound to an agent chosen at creation time. Commands are routed to that agent's `Address` from the registry. `SNIVUR_AGENT_URL` is removed.
2. On every heartbeat, a **pure reconcile function** compares that agent's servers against the reported containers and returns state transitions. The controller then applies them through the existing state machine. This is the Kubernetes controller pattern, applied per agent.
3. When an agent goes `offline`, its running servers become `unknown` rather than `failed`. We don't know that they died, only that we can't see them.

## Specification

### `shared/`

- `Server` gains `AgentID string \`json:"agent_id"\``.
- `CreateServerRequest` gains `AgentID string \`json:"agent_id"\``.
- A new state: `StateUnknown ServerState = "unknown"`.

### State machine additions

```
running  -> unknown
unknown  -> running | failed
```

`unknown -> stopping` is **not** allowed. Stop on an `unknown` server returns 409.

### Reconciler (`api/reconcile.go`)

```go
type Transition struct {
    ServerID string
    To       shared.ServerState
    Message  string
}

// Reconcile is pure: no locks, no I/O. now is passed in (controller clock).
func Reconcile(servers []shared.Server, hb shared.Heartbeat, now time.Time) (transitions []Transition, orphans []shared.ContainerInfo)
```

If `hb.ContainersError != ""`, Reconcile returns no transitions and no orphans. A failed `docker ps` is not evidence that containers died.

Only servers with `AgentID == hb.AgentID` are considered.

**Grace period.** A `running` server whose `UpdatedAt` is within `reconcileGrace` (15s, longer than one heartbeat interval) of `now` is skipped. A heartbeat whose `docker ps` ran just before the launch finished could otherwise fail a server that just started. A genuinely missing container is caught by the next heartbeat after the grace period.

`Transition` also carries `From shared.ServerState`, which is the state Reconcile saw. A container counts as "running" if it is reported with `State == "running"`.

| Server state | Container reported running? | Result |
| ------------ | --------------------------- | ------ |
| `running` | yes | no-op |
| `running` | no | `failed`, "container not running on agent" |
| `unknown` | yes | `running`, "agent recovered" |
| `unknown` | no | `failed`, "container not found after agent recovered" |
| `pending` / `starting` / `stopping` | any | no-op (in flight; avoids racing the launch/stop goroutines) |
| `stopped` / `failed` | any | no-op |

- `orphans`: reported containers that are **running** and either have a `ServerID` with no server in the input list, or belong to a server in a terminal state (`stopped`/`failed`). The second case covers a stop or launch that timed out and marked the server `failed` while its container is still alive (found in ADR 0002 testing). Orphans are logged, never killed.
- Output is deterministic: transitions sorted by `ServerID`.

### Wiring

- `Store` gains `TransitionFrom(id string, from, to shared.ServerState, msg string, mutate func(*shared.Server)) (shared.Server, error)`. It works like `Transition`, but returns `ErrStateChanged` without changing anything if the current state is not `from`. This is a compare-and-set.
- `POST /agents/heartbeat`: `Upsert` → `Reconcile(store.List(), hb, now)` → apply each transition with `store.TransitionFrom(t.ServerID, t.From, t.To, ...)`. `ErrStateChanged` and `ErrInvalidTransition` are logged and ignored, because the snapshot went stale. Then log the orphans.
- The sweeper's offline → `unknown` move also uses `TransitionFrom(id, running, unknown, ...)`.
- Sweeper: for each `StatusChange` where `To == offline`, transition every `running` server on that agent to `unknown` with the message "agent offline".
- `POST /servers`:
  - `agent_id` is required (400 if empty).
  - An unknown agent returns 404.
  - An agent that is not `healthy` returns 409.
  - Store `AgentID` on the server.
- `AgentClient` methods take the agent's base URL. For example, `Launch(ctx, baseURL, req)` and `Stop(ctx, baseURL, serverID)`, or a factory keyed by agent. Stop resolves the URL from the server's `AgentID`. If the agent is not in the registry at stop time, the transition is `failed` with a message.
- `GET /servers?agent_id=<id>` filters the list.

## Acceptance criteria

1. Table test for `Reconcile` covering every row above, plus:
   - Servers on a different agent are ignored.
   - A container reported as `exited` counts as not running.
   - Orphans are detected: an unknown `ServerID`, and a running container for a `failed` or `stopped` server. A non-running container for a terminal server is not an orphan.
   - The output order is deterministic.
2. End to end with fakes: create a server on agent A, which reaches `running`. A heartbeat from A without that container moves the server to `failed`.
3. Offline path, with a fake clock: a `running` server on agent A becomes `unknown` after A's status changes to `offline` in a sweep. A later heartbeat with the container moves it back to `running`. A later heartbeat without the container moves it to `failed`.
4. Two `httptest` agents, A and B: servers created with `agent_id` A or B have their launch requests hit only the matching agent.
5. `POST /servers` with a missing agent returns 400, with an unknown agent returns 404, and with an unhealthy agent returns 409.
6. A heartbeat that arrives while a server is `starting` does not change its state.
   - A `running` server updated within `reconcileGrace` is not failed by a heartbeat that lacks its container. After the grace period it is.
   - `TransitionFrom` returns `ErrStateChanged` when the state differs from `from`. A server that moved to `stopping` between the snapshot and the apply is not touched by reconcile.
   - A heartbeat with `containers_error` set changes nothing, even for `running` servers absent from `containers`.
   - A sweep can mark an agent offline just before its next heartbeat arrives. Its servers may then sit in `unknown` while the agent is healthy. The next heartbeat must move them back to `running`.
7. `SNIVUR_AGENT_URL` no longer appears anywhere in the code.
8. `go test -race ./...` passes.

## Consequences

- Crashed containers are detected within one heartbeat interval (10s).
- Agent loss is distinguished from container loss, which avoids false `failed` states during network blips.
- The controller detects drift but does not yet **correct** it, for example by restarting failed servers or killing orphans. Self-healing policies are future work, and this design is the hook for them.
- The agent's default advertise URL is `http://localhost:<port>`, which is unreachable from a remote controller. The agent logs a warning at startup when `SNIVUR_AGENT_ADVERTISE_URL` is unset and `SNIVUR_CONTROLLER_URL` is not a loopback host.
- Placement is manual (`agent_id` is required). Automatic scheduling based on agent capacity is future work.
