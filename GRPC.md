# gRPC

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
        Port:   9000,
        Health: health,
    }, svc.RegisterGRPC)

    bedrock.RunWithOptions(svc, cfg.BaseConfig, bedrock.Options{
        Health:  health,
        Servers: []bedrock.Server{server},
    })
}
```

A service with no HTTP routes is perfectly normal here — `Routes()` may return `nil`. Bedrock only reports "background mode" when there is nothing to serve at all.

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

1. The health service reports `NOT_SERVING`, so anything watching learns the instance is going away before connections close under it.
2. `GracefulStop()` runs in the background; if every RPC finishes first, `Shutdown` returns `nil`.
3. If the deadline arrives first, `Stop()` closes the connections outright and `Shutdown` returns `context.DeadlineExceeded`.

The budget comes from `bedrock.Options.ShutdownTimeout` (default 30s), shared with the HTTP server and your `OnStop`. Size it against your longest legitimate RPC:

```go
bedrock.Options{
    ShutdownTimeout: 45 * time.Second, // longer than the longest long-poll
    Servers:         []bedrock.Server{server},
}
```

## Ports under Nomad

`Config.Port` is a plain port number. For a Nomad dynamic port, read it from the environment yourself:

```go
port := 9000
if p := os.Getenv("NOMAD_PORT_grpc"); p != "" {
    port, _ = strconv.Atoi(p)
}
```

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

Two rules make the difference between a clean deploy and a stuck one:

- **`Start` returns once you are serving.** Bind the listener synchronously and spawn the serve loop, so a port conflict is a startup error rather than a log line after the process has announced itself healthy.
- **`Shutdown` honours its context.** A `Shutdown` that can block indefinitely will eventually hang a deploy.
