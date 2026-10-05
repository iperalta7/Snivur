# ADR 0002: Async server lifecycle with a state machine

- **Status:** Accepted
- **Date:** 2026-10-05
- **Depends on:** ADR 0001

## Context

After ADR 0001, the controller still holds the client's request open while the agent pulls the image and starts the container, which can take minutes. The controller keeps no record of servers, so there is no way to ask "what is running?" or to stop anything.

## Decision

The controller becomes the source of truth for **desired state**. It keeps an in-memory server store. Lifecycle commands return `202 Accepted` immediately and are executed against the agent in background goroutines. Every state change goes through one function that rejects illegal transitions.

## Specification

### `shared/`

```go
type ServerState string

const (
    StatePending  ServerState = "pending"
    StateStarting ServerState = "starting"
    StateRunning  ServerState = "running"
    StateStopping ServerState = "stopping"
    StateStopped  ServerState = "stopped"
    StateFailed   ServerState = "failed"
)

type Server struct {
    ID          string            `json:"id"`
    Name        string            `json:"name"`
    Game        string            `json:"game"`
    Config      map[string]string `json:"config"`
    State       ServerState       `json:"state"`
    Message     string            `json:"message,omitempty"` // last error / reason
    ContainerID string            `json:"container_id,omitempty"`
    CreatedAt   time.Time         `json:"created_at"`
    UpdatedAt   time.Time         `json:"updated_at"`
}

type CreateServerRequest struct {
    Name   string            `json:"name"`
    Game   string            `json:"game"`
    Config map[string]string `json:"config"`
}
```

### Controller state machine (`api/store.go`)

Allowed transitions. Anything else returns `ErrInvalidTransition`:

```
pending  -> starting | failed
starting -> running  | failed
running  -> stopping | failed
stopping -> stopped  | failed
```

`stopped` and `failed` are terminal.

```go
type Store struct { /* sync.RWMutex + map[string]*shared.Server + now func() time.Time */ }

func NewStore(now func() time.Time) *Store
func (s *Store) Create(req shared.CreateServerRequest) (shared.Server, error) // ErrNameConflict if a non-terminal server has the same name
func (s *Store) Get(id string) (shared.Server, bool)
func (s *Store) List() []shared.Server                                       // sorted by CreatedAt asc
func (s *Store) Transition(id string, to shared.ServerState, msg string, mutate func(*shared.Server)) (shared.Server, error)
```

- Every method returns **copies**, including a cloned `Config` map, never pointers into the map.
- `Transition` updates `UpdatedAt`, sets `Message` to `msg`, and applies `mutate`, for example to set `ContainerID`, all while holding the lock.

### Agent client (`api/agentclient.go`)

```go
type AgentClient interface {
    Launch(ctx context.Context, req shared.LaunchRequest) (shared.LaunchResponse, error)
    Stop(ctx context.Context, serverID string) error // returns nil if agent says 404 (already gone)
}
```

The HTTP implementation uses the base URL and API key from env, as in ADR 0001.

### Controller endpoints

| Method & path | Behavior | Status |
| ------------- | -------- | ------ |
| `POST /servers` | Validate (`shared.ValidateLaunch`) → `Create` as `pending` → respond with the server → start a goroutine: `starting` → `Launch` → `running` with `ContainerID`, or `failed` with the error message | 202 with `Location: /servers/{id}`; 400 invalid; 409 name conflict |
| `GET /servers` | List | 200 `[]Server` (empty list is `[]`, not `null`) |
| `GET /servers/{id}` | Get | 200 / 404 |
| `POST /servers/{id}/stop` | Only when state is `running`: transition to `stopping`, respond, then a goroutine runs `Stop` → `stopped`, or `failed` | 202 / 404 / 409 if not `running` |

- Background launches use `context.WithTimeout(context.Background(), 5*time.Minute)`. Stops use 1 minute.
- The `POST /servers` request from ADR 0001 is replaced by this flow. The pass-through proxy behavior is removed.

### Agent

The `Runtime` interface gains `Stop(ctx context.Context, serverID string) error`:

1. `docker ps -q --filter label=snivur.server_id=<id>`. If the output is empty, return `ErrNotFound`.
2. `docker stop <container id>`.

New handler, behind the API-key middleware: `POST /servers/{id}/stop` returns 204 on success, 404 for `ErrNotFound`, and 500 on any other error.

### Hardening (both binaries)

These items were found while testing ADR 0001.

- Wrap every JSON request body in `http.MaxBytesReader` with a 1 MiB limit. An oversized body returns 413 with an `ErrorResponse`.
- Serve with an `http.Server` that sets `ReadHeaderTimeout: 10s` instead of calling bare `http.ListenAndServe`.
- Because launches now run on a background context, a client that disconnects no longer cancels an in-flight `docker run`.

## Acceptance criteria

1. State machine table test: every (from, to) pair is either allowed or returns `ErrInvalidTransition`, matching the table above exactly.
2. `POST /servers` responds 202 within 100ms even when the fake `AgentClient` blocks. The server is observed moving through `pending → starting → running`, and then has its `container_id` set.
3. A launch error from the agent results in `failed`, with the error in `message`.
4. Stop on a `running` server ends in `stopped`. Stop on any other state returns 409. Stop on an unknown ID returns 404. An agent 404 during stop still ends in `stopped`.
5. Creating a second server with the same name as a non-terminal server returns 409. Reusing a name after the first server is `stopped` succeeds.
6. `go test -race ./...` passes with concurrent create, stop, and list calls, with at least 50 goroutines.
7. Agent `Stop` issues the exact `docker ps` and `docker stop` arguments, verified with a fake runner.
8. A request body larger than 1 MiB returns 413 on both the controller and the agent.

## Consequences

- State lives in memory only, so restarting the controller loses it. Persistence is future work.
- Restart is intentionally out of scope. It is stop followed by start, and can be added later without changing the state machine.
- If the controller crashes mid-transition, servers can stay stuck in `starting` or `stopping`. ADR 0004's reconciliation is the first step toward self-healing.
