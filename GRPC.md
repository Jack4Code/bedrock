# gRPC

> New in bedrock v0.5.0 / bedrock/grpc v0.1.0. If you are upgrading an existing service, read the [changelog](CHANGELOG.md) first — the shutdown budget changed to make this work.

Bedrock supervises gRPC servers through the same lifecycle it gives its HTTP router: started after your app's `OnStart`, drained before its `OnStop`, sharing one shutdown deadline.

The gRPC support lives in a **separate Go module**, `github.com/Jack4Code/bedrock/grpc`. `google.golang.org/grpc` and protobuf pull in a large dependency tree, and a service that speaks only HTTP should not carry them in its module graph. Only services that import the module pay for it.

## Quick start

```bash
go get github.com/Jack4Code/bedrock/grpc
```

```go
import (
    "github.com/Jack4Code/bedrock"
    bgrpc "github.com/Jack4Code/bedrock/grpc"
    "google.golang.org/grpc"
)

// Your app registers its services.
func (svc *Service) RegisterGRPC(g *grpc.Server) {
    pb.RegisterMyServiceServer(g, svc)
}

func main() {
    // One health tracker shared by the HTTP /ready endpoint and the gRPC
    // health service, so the two cannot disagree.
    health := bedrock.NewHealthStatus()

    server := bgrpc.New(bgrpc.Config{
        Port:   cfg.GetGRPCPort(),
        Health: health,
    }, svc.RegisterGRPC)

    bedrock.RunWithOptions(svc, cfg.BaseConfig, bedrock.Options{
        Health:  health,
        Servers: []bedrock.Server{server},
    })
}
```

A service with no HTTP routes is perfectly normal here — `Routes()` may return `nil`. Bedrock only reports "background mode" when there is nothing to serve at all.

### Register before `OnStart`, not after

`register` runs inside `bgrpc.New` — in `main`, before `bedrock.Run` is called. This is the opposite of the HTTP side, where bedrock reads `Routes()` *after* `OnStart` so handlers can close over state initialised there. A `*grpc.Server` cannot accept a new service once it is serving, so registration cannot be deferred the same way.

The consequence: register a pointer whose fields `OnStart` fills in, and don't resolve dependencies inside the callback.

```go
// Good — the pointer is registered; OnStart populates svc.db before any RPC lands.
svc := myservice.New(logger)
server := bgrpc.New(cfg, svc.Register)

// Broken — app.db is still nil here, and the service captures the nil.
server := bgrpc.New(cfg, func(g *grpc.Server) {
    pb.RegisterMyServiceServer(g, newService(app.db))
})
```

The ordering still works out: bedrock runs `OnStart` before it calls `Start` on any `Server`, so the listener does not accept its first RPC until the app is initialised.

## Configuration

```go
type Config struct {
    Port          int                  // 0 binds a free port; Addr() reports it
    Host          string               // empty listens on all interfaces
    Health        *bedrock.HealthStatus
    Logger        *slog.Logger         // defaults to slog.Default()
    ServerOptions []grpc.ServerOption  // interceptors, TLS, keepalive, limits
}
```

Bedrock has no opinion about interceptors, credentials, or message limits — pass them through `ServerOptions`:

```go
bgrpc.New(bgrpc.Config{
    Port: 9000,
    ServerOptions: []grpc.ServerOption{
        grpc.Creds(credentials.NewTLS(tlsConfig)),
        grpc.ChainUnaryInterceptor(loggingInterceptor, authInterceptor),
        grpc.MaxRecvMsgSize(16 << 20),
    },
}, register)
```

## Health checks

The `grpc.health.v1.Health` service is always registered. When you pass a `Health` tracker it mirrors bedrock's readiness — the same state `/ready` reports — so an orchestrator configured for gRPC probes sees the same picture as one using HTTP.

```
NOT_SERVING   before OnStart completes, and once shutdown begins
SERVING       once bedrock marks the process ready
```

Without a tracker the server simply reports `SERVING` while it is running.

Readiness is polled every 250ms rather than pushed: `bedrock.HealthStatus` is a guarded bool with no change notification, and a quarter second of staleness is well inside any probe interval.

Nomad:

```hcl
check {
  type     = "grpc"
  port     = "grpc"
  interval = "10s"
  timeout  = "2s"
}
```

### What a probe actually sees during a drain

`NOT_SERVING` is not what a health probe observes on shutdown, and it is worth being precise about why.

| client | what it sees |
|---|---|
| `Health/Watch`, connection already open | `SERVING` → `NOT_SERVING`, then GOAWAY |
| `Health/Check`, dialling in fresh | connection refused |

`Shutdown` sets `NOT_SERVING` and then calls `GracefulStop()`, whose first act is to close the listener. Subscribers already connected get the transition pushed to them. Anything dialling in afterwards — including the Nomad check above, which is a unary `Check` on a new connection — finds nothing accepting.

Both are correctly read as "stop sending traffic", so probes behave properly either way. The distinction matters only if you are reasoning about a rolling deploy and expecting a `NOT_SERVING` response rather than a refused connection.

## Reflection

Opt-in, because reflection publishes your entire service surface:

```go
server := bgrpc.New(cfg, register, bgrpc.WithReflection())
```

Then `grpcurl -plaintext localhost:9000 list` works without a copy of your `.proto` files.

## Shutdown

This is the part worth understanding, because getting it wrong is invisible until a deploy hangs.

`grpc.Server.GracefulStop()` waits for every open RPC to return **and has no timeout of its own**. A server with long-polling or streaming endpoints always has open RPCs, so a naive integration blocks forever on SIGTERM until the orchestrator loses patience and kills the container.

`Shutdown` therefore races the graceful drain against the context bedrock supplies:

1. The health service reports `NOT_SERVING`, so open `Watch` subscribers learn the instance is going away before connections close under them (see the table above for what a fresh probe sees instead).
2. `GracefulStop()` runs in the background; if every RPC finishes first, `Shutdown` returns `nil`.
3. If the deadline arrives first, `Stop()` closes the connections outright and `Shutdown` returns `context.DeadlineExceeded`.

### The drain budget

`bedrock.Options.ShutdownTimeout` (default 30s) bounds the whole drain. Inside it:

- **Servers drain concurrently**, each getting the full server phase. A gRPC server holding a stream open costs the other servers nothing but the wait.
- **`OnStop` gets a reserve** carved out of the budget — `Options.OnStopTimeout`, defaulting to a fifth of `ShutdownTimeout`. If the servers finish early `OnStop` gets whatever is left over; if they use everything, it still gets the reserve.

The reserve exists because of exactly this package. A streaming or long-polling endpoint always has an open RPC, so a gRPC server uses its entire drain budget on **every** shutdown, not occasionally. Without a reserve, `OnStop` would be handed an already-expired context on every deploy and would silently skip flushing metrics, deregistering from service discovery, and closing the database.

Size `ShutdownTimeout` against your longest legitimate RPC, and remember the reserve comes out of it:

```go
bedrock.Options{
    ShutdownTimeout: 45 * time.Second, // longer than the longest long-poll
    OnStopTimeout:   5 * time.Second,  // servers get 40s, cleanup always gets 5s
    Servers:         []bedrock.Server{server},
}
```

`OnStopTimeout` must be shorter than `ShutdownTimeout`; bedrock rejects the configuration at startup rather than at the deploy that discovers it.

## Ports

`config.BaseConfig` carries `GRPCPort` alongside the HTTP, health and metrics ports, with the same Nomad handling:

```toml
# config.toml
http_port   = 8080
health_port = 8081
grpc_port   = 9000
```

```go
server := bgrpc.New(bgrpc.Config{
    Port:   cfg.GetGRPCPort(),
    Health: health,
}, svc.Register)
```

`GetGRPCPort()` prefers `NOMAD_PORT_grpc` when it is set and falls back to the configured value, exactly as `GetHTTPPort()` does — so nothing needs hand-rolling for a dynamic port. `GRPC_PORT` overrides the TOML value directly.

See [NOMAD.md](NOMAD.md) for the surrounding job specification.

## Writing your own Server

`bgrpc.Server` is just an implementation of `bedrock.Server`, and nothing about that interface is gRPC-specific. Anything that runs for the process lifetime can use the same hook — a metrics endpoint, a queue consumer, a background poller:

```go
type Server interface {
    Name() string
    Start(ctx context.Context) error    // must not block; bind errors returned here
    Shutdown(ctx context.Context) error // must return when ctx expires
}
```

Three rules make the difference between a clean deploy and a stuck one:

- **`Start` returns once you are serving.** Bind the listener synchronously and spawn the serve loop, so a port conflict is a startup error rather than a log line after the process has announced itself healthy.
- **`Shutdown` honours its context.** A `Shutdown` that can block indefinitely will eventually hang a deploy.
- **`Shutdown` assumes nothing about the others.** Servers are drained concurrently, so another server may be up, down, or midway through its own drain while yours runs.

`http.Server` needs the same deadline race `bgrpc.Server` does if it serves anything long-lived — `http.Server.Shutdown` waits on open connections, and a Server-Sent Events connection is open by definition:

```go
func (s *StreamServer) Shutdown(ctx context.Context) error {
    err := s.srv.Shutdown(ctx)
    if errors.Is(err, context.DeadlineExceeded) {
        return errors.Join(err, s.srv.Close())
    }
    return err
}
```
