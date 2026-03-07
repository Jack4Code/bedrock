# Logging in Bedrock

## Current State

Bedrock uses Go's standard `log` package internally (`log.Printf`, `log.Println`). All log output goes to **stderr**. There is no structured formatting, no log levels, and no way to redirect bedrock's internal logs to an external service.

This is fine for local development. In production it creates a gap: if you add an observability service like Axiom, Kibana, or a similar log aggregator, bedrock's own logs (startup messages, job errors, shutdown events) will not flow there automatically.

## Where Logs Go in Practice

**Local / development:** stderr, printed to your terminal.

**Nomad:** stdout/stderr are captured by the Nomad agent and written to alloc log files on disk (`/alloc/logs/<task>.std{out,err}.N`). They do not leave the host unless you run a separate log shipper (Filebeat, Vector, Fluent Bit, etc.) or configure Nomad's log collection.

**Other platforms (Fly.io, Railway, Render, K8s):** similar story — the platform captures stdout/stderr and may surface it in a dashboard, but structured log forwarding to a third-party service requires either a log shipper or structured JSON output that the platform's agent can parse.

## The Sentry Case (Different)

Sentry does not consume log lines. It works via explicit SDK calls (`sentry.CaptureException(err)`). If you want job errors to reach Sentry, use the `OnError` hook on each `Job`:

```go
func (a *App) Jobs() []bedrock.Job {
    return []bedrock.Job{
        {
            Schedule: "@weekly",
            Handler:  a.cleanupWebhookEvents,
            OnError: func(err error) {
                sentry.CaptureException(err)
                slog.Error("cleanup job failed", "err", err)
            },
        },
    }
}
```

No bedrock changes needed for Sentry — the hook is the right place.

## The Structured Logging Problem

For Axiom, Kibana, Datadog Logs, and similar services, the typical integration path is:

1. Write structured JSON logs (key-value pairs, not free-form strings)
2. Either ship them via a log collector, or write them directly to the service's ingest API

Go's stdlib `slog` package (available since Go 1.21) handles this well. You configure a handler — a JSON handler, an Axiom handler, etc. — and pass a `*slog.Logger` into your code. The logger is just an interface; swapping the underlying handler changes where logs go.

The problem today is that bedrock calls `log.Printf` directly, bypassing any `slog.Logger` the app might have configured. Even if your app writes structured logs everywhere, bedrock's own output remains unstructured and separate.

## Proposed Fix: Logger Injection

The fix is to let the app pass a `*slog.Logger` into bedrock at startup. Bedrock would use it for all internal logging instead of calling `log.Printf` directly.

**Proposed API:**

```go
type Options struct {
    CORS   *CORSConfig
    Logger *slog.Logger  // optional; defaults to slog.Default()
}

func RunWithOptions(app App, cfg config.BaseConfig, opts Options) error
```

Existing `Run` and `RunWithCORS` would continue to work unchanged (they'd call `RunWithOptions` internally with `Logger: slog.Default()`).

**App usage with Axiom:**

```go
import (
    "github.com/axiomhq/axiom-go/adapters/slog"
    axslog "log/slog"
)

func main() {
    handler, _ := axiom.New()
    logger := axslog.New(handler)

    bedrock.RunWithOptions(app, cfg, bedrock.Options{
        Logger: logger,
    })
}
```

**App usage with JSON to stdout (for a log shipper to pick up):**

```go
logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

bedrock.RunWithOptions(app, cfg, bedrock.Options{
    Logger: logger,
})
```

Once bedrock uses the injected logger, all of bedrock's startup messages, job error logs, and shutdown events flow through the same handler as the rest of the application.

## What This Does Not Cover

**Trace context / request IDs:** Passing a logger through `RunWithOptions` handles process-level logs. For per-request structured logging (attaching a request ID or trace ID to every log line in a handler), the app needs to store the logger in the request context and retrieve it in each handler. Bedrock could help by injecting a logger into the context before calling each route handler, but that is a separate concern.

**Log levels:** `slog` supports levels (Debug, Info, Warn, Error). Bedrock's current internal logs are all informational or error. Once migrated to `slog`, a `MinLevel` option on the handler controls verbosity without code changes.

## Status

Not yet implemented. The current `log.Printf` calls in `bedrock.go` and `cron.go` would need to be replaced with `slog` calls, and `RunWithOptions` (or a similar entry point) would need to be added.

The `OnError` hook on `Job` is available today and is the right place for Sentry integration without waiting for this work.
