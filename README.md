# bedrock

A small Go framework for services that have to run somewhere. It owns the parts every service rewrites — startup and shutdown ordering, health endpoints, config loading, port resolution under an orchestrator, scheduled jobs — and stays out of the way of the parts that are actually yours.

You implement one interface:

```go
type App interface {
    OnStart(ctx context.Context) error
    OnStop(ctx context.Context) error
    Routes() []Route
}
```

Bedrock runs it:

```go
package main

import (
    "context"
    "database/sql"
    "log"
    "net/http"

    "github.com/Jack4Code/bedrock"
    "github.com/Jack4Code/bedrock/config"
    "github.com/gorilla/mux"
)

type App struct{ db *sql.DB }

// OnStart initialises what the handlers need. Routes are read after it returns,
// so a handler can safely close over anything set up here.
func (a *App) OnStart(ctx context.Context) error { a.db = openDB(); return nil }
func (a *App) OnStop(ctx context.Context) error  { return a.db.Close() }

func (a *App) Routes() []bedrock.Route {
    return []bedrock.Route{{
        Method:     "GET",
        Path:       "/widgets/{id}",
        Visibility: bedrock.Public,
        Handler: func(ctx context.Context, r *http.Request) bedrock.Response {
            return bedrock.JSON(200, map[string]string{"id": mux.Vars(r)["id"]})
        },
    }}
}

func main() {
    var cfg config.BaseConfig
    if err := config.NewLoader("config.toml").Load(&cfg); err != nil {
        log.Fatal(err)
    }
    if err := bedrock.Run(&App{}, cfg); err != nil {
        log.Fatal(err)
    }
}
```

That gets you a router, `/health` `/ready` `/live`, TOML + env config, SIGTERM handling and an ordered drain. Routing is `gorilla/mux`, so path variables come from `mux.Vars(r)`.

```bash
go get github.com/Jack4Code/bedrock
```

## The two modules

Bedrock is two Go modules in one repository, versioned and tagged separately.

| module | import path | what it is |
|---|---|---|
| bedrock | `github.com/Jack4Code/bedrock` | the framework: lifecycle, routing, health, config |
| bedrock/grpc | `github.com/Jack4Code/bedrock/grpc` | a gRPC server that runs under bedrock's lifecycle |

**The dependency runs one way: `bedrock/grpc` imports `bedrock`, never the reverse.** The parent has no idea gRPC exists — it exposes a transport-agnostic `bedrock.Server` interface, and the grpc module is one implementation of it. You could write another for a queue consumer or a metrics endpoint without touching the framework.

The split exists so you only pay for what you use. `google.golang.org/grpc` and protobuf are a large dependency tree, and a service that speaks only HTTP has no business carrying them in its module graph. Import the parent alone and you never see them.

That direction makes the release order **load-bearing**. `grpc/go.mod` names a concrete parent version, and a `replace` directive is only honoured in the main module — so the `require` line is what every consumer actually resolves. Tag the parent first, point the submodule at that tag, then tag the submodule. Getting it backwards publishes a module nobody can install, and the failure is invisible from inside the repository. [RELEASING.md](RELEASING.md) has the procedure; `scripts/check_submodule.sh` enforces it in CI by building a throwaway consumer from outside the tree.

One consequence worth knowing before you reach for gRPC: **adding `bedrock/grpc` will pull the parent up to whatever version it requires**, because Go's minimal version selection takes the highest version in the graph. You get that release's behavioural changes whether or not you asked for them. Read the [changelog](CHANGELOG.md) first.

## Startup and shutdown

The ordering is the reason to use a framework at all, and it is the same on every path — including a startup that fails halfway, which unwinds through the same teardown as a normal shutdown so a half-started process still releases what it acquired.

**Up:** health server → `OnStart` → jobs → servers.
**Down:** readiness fails → servers → health server → jobs → `OnStop`.

Readiness fails *first*, before anything drains, so a load balancer stops sending new traffic while in-flight requests finish.

### The drain budget

`Options.ShutdownTimeout` (default 30s) bounds the whole drain. Inside it, `Options.OnStopTimeout` (default a fifth of it) is held back so app cleanup is never handed an already-expired context:

| phase | budget at the defaults |
|---|---|
| servers, drained concurrently | 24s — `ShutdownTimeout` minus the reserve |
| `OnStop` | whatever the drain left over, floored at 6s |

So a clean shutdown gives `OnStop` close to the full 30s; one where a server hit its deadline still gives it 6s. The reserve is carved out of `ShutdownTimeout`, not added to it, so total drain time stays bounded by the single number. A reserve that does not fit inside the timeout is rejected at startup rather than at the deploy that discovers it.

Servers drain **concurrently** and the order between them is undefined. They are independent listeners; draining them one after another made them share a budget they had no reason to share, so one server that would not drain cut off requests still in flight on every server behind it.

If your cleanup or your longest request does not fit in the defaults, say so:

```go
bedrock.RunWithOptions(app, cfg, bedrock.Options{
    ShutdownTimeout: 60 * time.Second, // servers get 45s
    OnStopTimeout:   15 * time.Second, // cleanup always gets at least this
})
```

Whatever you pick, both phases have to fit inside your orchestrator's kill timeout — bedrock signals cleanup, it does not outrank SIGKILL.

## What else is in the box

- **Health endpoints** — `/health`, `/ready`, `/live`. On their own port, or merged into the main server when `HTTPPort == HealthPort`, which is what single-port platforms need. See [HEALTH.md](HEALTH.md).
- **Config** — TOML with environment overrides via struct tags, and `BaseConfig` carrying the ports every service needs. Embed it in your own config struct. See [config/README.md](config/README.md).
- **Orchestrator ports** — `GetHTTPPort()`, `GetHealthPort()`, `GetMetricsPort()`, `GetGRPCPort()` each prefer the matching `NOMAD_PORT_*` and fall back to configuration. See [NOMAD.md](NOMAD.md).
- **Route visibility** — every `Route` declares `Public`, `Gated` or `Private`, which selects the hostname it registers under. The zero value is `Private`, so forgetting to set one produces a 404 on the public surface rather than a leak. `Options.Serve` (or `BEDROCK_SERVE`) narrows a process to a subset, so the same image runs as separate task groups that each own part of the surface.
- **Scheduled jobs** — implement `JobsProvider` and bedrock runs a cron loop within the lifecycle. `Options.RunJobs` / `BEDROCK_RUN_JOBS` keeps them to one process in a split deployment.
- **CORS** — permissive by default for development, configurable per service, with OPTIONS preflight handled. See [CORS.md](CORS.md).
- **Helpers** — JSON decode/encode, multipart uploads, JWT issue/validate, bcrypt password hashing, per-route middleware chaining. Thin wrappers, not a framework of their own.
- **gRPC** — a supervised gRPC server with the health service wired to bedrock's readiness. See [GRPC.md](GRPC.md).

## Documentation

| | |
|---|---|
| [CHANGELOG.md](CHANGELOG.md) | what changed, and what to check before upgrading |
| [GRPC.md](GRPC.md) | the `bedrock/grpc` module |
| [HEALTH.md](HEALTH.md) | health endpoints and deployment modes |
| [NOMAD.md](NOMAD.md) | dynamic ports, job specs, split deployments |
| [CORS.md](CORS.md) | CORS configuration |
| [LOGGING.md](LOGGING.md) | logging behaviour |
| [RELEASING.md](RELEASING.md) | tagging the two modules, in the right order |
| [config/README.md](config/README.md) | the config package |

## Development

Both modules are tested separately — the root build does not compile `grpc/`, so a change spanning both needs both:

```bash
go vet ./... && go test -race ./...
(cd grpc && go vet ./... && go test -race ./...)
scripts/check_submodule.sh   # can anyone actually install bedrock/grpc?
```

`grpc/go.mod` carries a `replace` pointing at the parent in the working tree, so cross-module changes are testable before either module is tagged. That replace is also exactly why `check_submodule.sh` exists: it hides the one failure mode CI would otherwise never see.

Requires Go 1.25+.
