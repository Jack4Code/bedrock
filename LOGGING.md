# Logging in Bedrock

Bedrock logs through `log/slog`. Pass it a `*slog.Logger` and everything the framework says — startup, route registration, job errors, shutdown — goes through your handler, alongside the rest of your application's logs.

```go
logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

bedrock.RunWithOptions(app, cfg, bedrock.Options{
    Logger: logger,
})
```

`Options.Logger` is optional and defaults to `slog.Default()`. `bedrock.Run` and `bedrock.RunWithCORS` use the default too, so if you call `slog.SetDefault` in `main` before starting, you get the same result without touching `Options`.

The gRPC module takes its own logger the same way, for the same reason:

```go
bgrpc.New(bgrpc.Config{Port: cfg.GetGRPCPort(), Logger: logger}, svc.Register)
```

## What bedrock logs

At `Info`: resolved serve configuration, health server startup, each registered route with its method, path and visibility, routes skipped because this process does not serve their visibility, each server starting, and the shutdown sequence.

At `Warn`: a route skipped because no host is configured for its visibility, loopback trust being enabled, and a server force-closing work still in flight at the deadline.

At `Error`: a server that failed to drain in time, a listener that died unexpectedly, a job that returned an error, and an `OnStop` that returned one.

Route registration is the chattiest part at startup — one line per route. If that is noise in your environment, raise the handler's level to `Warn`; nothing bedrock logs at `Info` is load-bearing once a service is running.

## Job errors, and Sentry

Sentry does not consume log lines — it works through explicit SDK calls. Each `Job` has an `OnError` hook, which is the right place:

```go
func (a *App) Jobs() []bedrock.Job {
    return []bedrock.Job{{
        Schedule: "@weekly",
        Handler:  a.cleanupWebhookEvents,
        OnError: func(err error) {
            sentry.CaptureException(err)
            slog.Error("cleanup job failed", "err", err)
        },
    }}
}
```

Without an `OnError`, a failing job is logged at `Error` through bedrock's logger and nothing else happens. The job keeps its schedule either way — one failure does not unregister it.

## Shipping logs somewhere

Bedrock has no integration of its own and needs none: anything that satisfies `slog.Handler` works, which is how Axiom, Datadog, Kibana and the rest expose themselves to Go services. Construct the handler however that vendor's SDK says to, then hand bedrock a logger wrapping it:

```go
handler := someVendor.NewSlogHandler(...) // whatever the SDK provides

bedrock.RunWithOptions(app, cfg, bedrock.Options{
    Logger: slog.New(handler),
})
```

Where output actually lands, absent a handler that ships it directly:

- **Local:** stderr, in your terminal.
- **Nomad:** the agent captures stdout/stderr into alloc log files (`/alloc/logs/<task>.std{out,err}.N`). They stay on the host unless you run a shipper (Vector, Fluent Bit, Filebeat) or configure Nomad's log collection.
- **Fly.io, Railway, Render, Kubernetes:** the platform captures stdout/stderr and surfaces it in a dashboard. Structured forwarding to a third party still needs a shipper, or JSON output the platform's agent can parse.

Writing JSON to **stdout** rather than stderr is usually what a collector expects, which is why the first example above uses `os.Stdout`.

## What this does not cover

**`BaseConfig.LogLevel` is not read by bedrock.** It is a configuration field for *your* use — bedrock never consults it, and setting `log_level = "debug"` in your TOML changes nothing on its own. Verbosity comes from the level on the handler you construct. If you want the config value to drive it, wire it yourself:

```go
var level slog.Level
if err := level.UnmarshalText([]byte(cfg.LogLevel)); err != nil {
    level = slog.LevelInfo
}
logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level}))
```

**The `config` package still uses the standard `log` package.** `resolvePort` writes two messages through `log.Printf` — one when a `NOMAD_PORT_*` variable is set, one when it is set but unparseable. They go to stderr unstructured, outside your handler. This is because port resolution runs while loading configuration, before a logger exists to inject. In practice it is two lines at startup, but it does mean a malformed `NOMAD_PORT_http` produces a warning your log aggregator will not see.

**There is no per-request logger.** Bedrock passes the request's own context to handlers and does not attach a logger to it, so a request ID or trace ID on every line inside a handler is something you arrange yourself — typically middleware that puts a `*slog.Logger` with the request's attributes into the context.
