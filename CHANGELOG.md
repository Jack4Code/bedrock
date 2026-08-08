# Changelog

## v0.5.0

Adds `github.com/Jack4Code/bedrock/grpc`, a second module that runs gRPC servers under bedrock's lifecycle, and changes how the shutdown budget is spent to make that work.

**Nothing in this release breaks compilation.** Existing services build unchanged. The changes that matter are behavioural, which means `go build` passing is not evidence the upgrade is safe — read "Upgrading from v0.4.0" below.

### Added

- **`bedrock.Server`** — an interface for anything that runs for the process lifetime and needs bedrock to own its startup ordering and drain: a gRPC server, a metrics endpoint, a queue consumer. Supply implementations through `Options.Servers`. Nothing about it is gRPC-specific.
- **`github.com/Jack4Code/bedrock/grpc`** (package `bgrpc`) — a gRPC server implementing `bedrock.Server`, with the `grpc.health.v1` service wired to bedrock's readiness and an opt-in reflection service. A **separate Go module**, so services that speak only HTTP do not carry protobuf in their module graph. See [GRPC.md](GRPC.md).
- **`Options.Health`** — supply your own `*HealthStatus` instead of having bedrock create one, so the gRPC health service and the HTTP `/ready` endpoint report the same state.
- **`Options.ShutdownTimeout`** — bounds the drain, replacing timeouts that were hardcoded per path (30s for a service with routes, 5s for a background one). Defaults to 30s.
- **`Options.OnStopTimeout`** — the slice of `ShutdownTimeout` held back for `OnStop`. Defaults to a fifth of it.
- **`config.BaseConfig.GRPCPort`** and **`GetGRPCPort()`** — reads `NOMAD_PORT_grpc` then `GRPC_PORT` then the `grpc_port` TOML key, exactly as the HTTP, health and metrics ports do.
- A service with no HTTP routes but at least one `Server` is now a normal service. `Routes()` returning `nil` no longer means "background mode" unless there are no servers either.

### Changed

- **Servers drain concurrently rather than sequentially in reverse order.** They are independent listeners; draining them one after another made them share a budget they had no reason to share, so one server that would not drain cut off requests still in flight on every server behind it. **Drain order between `Server`s is now undefined.**
- **Servers get `ShutdownTimeout` minus the `OnStopTimeout` reserve** — 24s rather than 30s at the defaults. This is the one behavioural regression in the release: in-flight requests have 6 fewer seconds to finish before the server is forced closed.
- **`OnStop` now receives a context with a deadline. In v0.4.0 it received `context.Background()`** and could run for as long as it liked, holding a shutdown open indefinitely. It now gets whatever the drain left over, floored at `OnStopTimeout` — roughly the full `ShutdownTimeout` after a clean drain, and the reserve (6s at the defaults) after one that hit the deadline. **This is the change most likely to affect an existing service**; see the upgrade notes.

### Fixed

- **`bedrock/grpc` could not be installed.** Its `go.mod` required the parent at the placeholder pseudo-version `v0.0.0-00010101000000-000000000000` and satisfied it with a `replace ../`. A `replace` is only honoured in the main module, so consumers resolved the unsatisfiable `require` and got `invalid version: unknown revision 000000000000`. The require now names a real published version. `scripts/check_submodule.sh` guards it in CI by building a throwaway consumer outside the tree.
- GRPC.md claimed the health service reporting `NOT_SERVING` during a drain is observable by health probes. Only already-open `Watch` subscribers see it; a unary `Check` on a fresh connection — including the Nomad `type = "grpc"` check the same document recommends — gets connection-refused, because `GracefulStop` closes the listener first. Both are correctly read as "stop sending traffic"; the documentation was simply wrong about the mechanism.

### Module versions

The two modules are versioned and tagged separately, and `bedrock/grpc` depends on `bedrock`:

| module | tag | requires |
|---|---|---|
| `github.com/Jack4Code/bedrock` | `v0.5.0` | — |
| `github.com/Jack4Code/bedrock/grpc` | `grpc/v0.1.0` | `bedrock >= v0.5.0` |

**Adding `bedrock/grpc` to a service still on bedrock v0.4.0 will upgrade it to v0.5.0**, because Go's minimal version selection takes the highest version any module in the graph requires. You get the behavioural changes above whether or not you asked for them, so read this entry before adding the gRPC module rather than after.

See [RELEASING.md](RELEASING.md) for why the tag order matters.

---

## Upgrading from v0.4.0

Nothing here requires a code change. These are checks to run against your own service, in descending order of how likely they are to matter.

**1. How long does your `OnStop` take?**

This is the one that can bite. In v0.4.0 `OnStop` received `context.Background()` — no deadline, no cancellation — so cleanup that took a minute simply took a minute. It is now bounded:

| drain outcome | what `OnStop` gets (defaults) |
|---|---|
| servers drained cleanly | ~30s — the unused remainder of `ShutdownTimeout` |
| servers hit the deadline | 6s — the `OnStopTimeout` reserve |

If your cleanup can exceed those, either raise the budget, or stop passing the context into the slow part:

```go
bedrock.Options{
    ShutdownTimeout: 60 * time.Second,
    OnStopTimeout:   20 * time.Second, // servers get 40s, cleanup always gets >= 20s
}
```

Cleanup that ignores its context entirely still works — bedrock does not kill the goroutine, it only signals — but it delays process exit past `ShutdownTimeout`, which is what the deadline exists to prevent. If your orchestrator's kill timeout is 30s, a 60s `OnStop` gets SIGKILLed regardless of what bedrock does.

**2. Does 24 seconds still drain your longest request?**

The default drain window for in-flight work drops from 30s to 24s. If you have requests that legitimately run longer than that — long-polls, streamed exports, slow upstreams — raise the budget explicitly:

```go
bedrock.Options{
    ShutdownTimeout: 45 * time.Second, // servers get 36s, OnStop gets 9s
}
```

The reserve is carved out of `ShutdownTimeout`, not added to it, so total drain time is still bounded by the one number. `OnStopTimeout` must be shorter than `ShutdownTimeout`; bedrock rejects the configuration at startup rather than at the deploy that discovers it.

**3. Do you pass more than one `Server` in `Options.Servers`?**

If so, check that no `Shutdown` implementation assumes another server is already down or still up. They now run at the same time. A service passing zero or one `Server` — which is every service that has not adopted the gRPC module — is unaffected by this.

**4. Nothing else.**

`App`, `Route`, `Response`, `Handler`, `Visibility`, `HostConfig`, the CORS config and the health endpoints are untouched. `Run`, `RunWithCORS` and `RunWithOptions` keep their signatures.

### Verifying the upgrade in your own service

```bash
go get github.com/Jack4Code/bedrock@v0.5.0
go build ./... && go vet ./... && go test ./...
```

A green build tells you nothing about the drain changes, so exercise the one that matters — start the service, hold a request open past your longest expected duration, send `SIGTERM`, and confirm it exits within `ShutdownTimeout` with the request either completed or cleanly refused:

```
INFO  shutting down
ERROR server forced to shutdown  server=http  err="context deadline exceeded"
INFO  servers stopped
```

If your `OnStop` flushes metrics, deregisters from service discovery, or closes a database, confirm those log lines still appear after a shutdown that hit the deadline — that is the case where cleanup is working against the reserve rather than the full budget.

---

## v0.4.0 and earlier

Predate this changelog. See the git history.
