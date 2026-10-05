# ADR 0001: Non-blocking container launch and runtime configuration

- **Status:** Accepted
- **Date:** 2026-10-05

## Context

The agent's `/launch` handler runs `docker run --rm` in the foreground and then calls `cmd.Wait()`, so the HTTP request blocks for the entire lifetime of the game server. On failure it writes two JSON bodies (error, then "launching"). Configuration is hardcoded: the earlier chi commit sets the agent's API key to `"agent"` and the controller's to `"default_api_key"` in a package-level variable, and the controller targets `http://localhost:8081`. API keys are compared with `!=`, which is not constant-time. User-supplied `name` and `image` are passed straight to `docker` as arguments, so a value like `--privileged` is interpreted as a flag.

Every later feature (async lifecycle, heartbeats, reconciliation) needs launches to return quickly and containers to be identifiable.

## Decision

1. Launch containers **detached** (`docker run -d`) and return as soon as Docker reports a container ID.
2. Tag each container with the label `snivur.server_id=<id>` so the agent can find containers it owns.
3. Replace the `GameServer` interface with a `Runtime` interface whose Docker implementation shells out through an injectable command runner.
4. Move all configuration to environment variables. The controller, not the client, holds the agent API key.
5. Validate `name` and `image` before they reach `docker`.
6. Require an API key on every route of both binaries. The controller has its own key, `SNIVUR_CONTROLLER_API_KEY`, which is separate from the agent key so that an agent's key cannot be used to control the fleet. This keeps the "API key on every route" behaviour from the earlier chi commit, minus its hardcoded keys.

## Specification

### `shared/`

```go
// shared/types.go (replace shared/main.go)
type LaunchRequest struct {
    ServerID string            `json:"server_id"`
    Name     string            `json:"name"`
    Game     string            `json:"game"`
    Config   map[string]string `json:"config"` // "image" is required
}

type LaunchResponse struct {
    ServerID    string `json:"server_id"`
    ContainerID string `json:"container_id"`
}

type ErrorResponse struct {
    Error string `json:"error"`
}

// NewID returns 16 random bytes from crypto/rand, hex-encoded (32 chars).
func NewID() string

// ValidateLaunch returns a descriptive error if:
//   - Name does not match ^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,62}$
//   - Config["image"] is empty, starts with "-", or contains whitespace
func ValidateLaunch(req LaunchRequest) error
```

`AgentApiKey` is removed from `LaunchRequest`.

### `agent/`

```go
// agent/runtime.go
type Runtime interface {
    Start(ctx context.Context, req shared.LaunchRequest) (containerID string, err error)
}

// CommandRunner executes a command and returns combined stdout/stderr.
type CommandRunner func(ctx context.Context, name string, args ...string) ([]byte, error)

func ExecRunner(ctx context.Context, name string, args ...string) ([]byte, error) // uses exec.CommandContext(...).CombinedOutput()

type DockerRuntime struct{ Run CommandRunner }
```

`DockerRuntime.Start` runs exactly:

```
docker run -d --rm --name <Name> --label snivur.server_id=<ServerID> <Config["image"]>
```

It returns the trimmed output as the container ID. On error, it wraps the error together with the trimmed output.

Handler `POST /launch` (wrapped in API-key middleware):

| Case | Status | Body |
| ---- | ------ | ---- |
| Missing/wrong `X-API-Key` | 401 | `ErrorResponse` |
| Malformed JSON | 400 | `ErrorResponse` |
| `ServerID` empty or `ValidateLaunch` fails | 400 | `ErrorResponse` |
| Runtime error | 500 | `ErrorResponse` |
| Success | 200 | `LaunchResponse` |

- API key comparison uses `crypto/subtle.ConstantTimeCompare`.
- Handlers are built from a struct holding config and the `Runtime`, so tests can inject fakes, e.g. `newAgentServer(cfg, rt) http.Handler`.

Environment:

| Var | Default | Notes |
| --- | ------- | ----- |
| `SNIVUR_AGENT_ADDR` | `:8081` | listen address |
| `SNIVUR_AGENT_API_KEY` | — | **required**; process exits non-zero with a clear log line if empty |

### `api/` (controller)

- `POST /servers` decodes a body of `{name, game, config}`, assigns `ServerID = shared.NewID()`, validates the request with `shared.ValidateLaunch` (returning 400 on failure), and forwards it to `${SNIVUR_AGENT_URL}/launch` with the `X-API-Key` header taken from the controller's own env. It passes through the agent's status code and body. If the agent is unreachable, it returns 502 with an `ErrorResponse`.
- The outbound HTTP client has a timeout of 5 minutes, because `docker run -d` still pulls images synchronously.
- The handler is built from a struct so tests can point it at an `httptest.Server`.

Environment:

| Var | Default |
| --- | ------- |
| `SNIVUR_CONTROLLER_ADDR` | `:8080` |
| `SNIVUR_AGENT_URL` | `http://localhost:8081` |
| `SNIVUR_AGENT_API_KEY` | — (required) |
| `SNIVUR_CONTROLLER_API_KEY` | — (required); checked on every controller route |

Both binaries also serve `GET /health`, which returns `{"status":"200","message":"Ok"}` and sits behind the same API key as their other routes.

Delete `agent/game.go` and `agent/docker.go`, or fold their contents into `runtime.go`.

## Acceptance criteria

1. `DockerRuntime.Start` calls the runner with exactly the argument list above (verified with a fake `CommandRunner`) and returns the trimmed container ID.
2. A runner error results in a 500 with a single JSON body that contains the runner's output.
3. A launch with `name` = `--privileged` or `image` = `-v/:/host` is rejected with 400, and the runner is never called.
4. A wrong or missing API key returns 401, and the runner is never called.
5. The agent exits non-zero when `SNIVUR_AGENT_API_KEY` is unset.
6. Controller → agent end to end with `httptest`: a successful launch passes through 200 with `container_id`, and an unreachable agent returns 502.
7. Smoke test, with a fake `docker` shell script on `PATH` that echoes a fake ID: `curl POST :8080/servers` returns in under 1 second.

## Consequences

- Launch requests no longer block, but the controller still waits synchronously for the agent, including the image pull. ADR 0002 makes this asynchronous.
- A single shared API key is used between the controller and all agents. Per-agent credentials or mTLS are future work.
- With `--rm`, a crashed container disappears instead of lingering as `exited`. ADR 0004 treats a missing container as a failure.
