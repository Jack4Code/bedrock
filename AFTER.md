# After: work that runs once the response is sent

`bedrock.After` schedules a function to run once the handler has returned and its response has been written. The caller gets its answer without waiting for work the answer does not depend on.

```go
Handler: func(ctx context.Context, r *http.Request) bedrock.Response {
    // ... handle the request ...
    _ = bedrock.After(ctx, func(ctx context.Context) {
        recordMetrics(ctx, ...)
    })
    return bedrock.JSON(http.StatusOK, result)
}
```

A service that never calls `After` is unaffected: no goroutines start, and shutdown spends no time on it.

## It is best-effort

Read this before anything else on the page.

The work runs in this process, **after the caller has been told the request succeeded**. If the process crashes, is OOM-killed, or is still running the task when the shutdown deadline passes, the work is lost and nobody is told. The caller will not retry, because as far as it knows nothing went wrong.

That makes `After` the right tool for work whose loss is tolerable — metrics, audit logs, cache warming, notifications, cleanup — and the wrong tool, on its own, for anything that must happen. For that, make the work durable *before* responding and use `After` only as the fast path. The next section shows how.

## Pattern: accept a webhook, process it after

A webhook sender (Stripe, Clerk, anyone) retries until it gets a 2xx. That retry is a guarantee you can keep, as long as you only return 2xx once the event is somewhere a crash cannot take it:

1. Verify the signature.
2. **Persist the event.** If this fails, return a 5xx so the sender retries.
3. Return `202 Accepted` — accepted for processing, not yet processed.
4. Process it in `After`, as the fast path.
5. A scheduled job sweeps for events that were persisted but never finished, and processes them. **This is the guarantee.** `After` only makes the common case fast.

```go
Handler: func(ctx context.Context, r *http.Request) bedrock.Response {
    body, err := io.ReadAll(r.Body)
    if err != nil {
        return bedrock.JSON(http.StatusBadRequest, nil)
    }
    event, err := verify(body, r.Header) // provider signature check
    if err != nil {
        return bedrock.JSON(http.StatusUnauthorized, nil)
    }

    // INSERT ... ON CONFLICT (event_id) DO NOTHING, status 'received'.
    // A duplicate delivery is not an error: the event is already safe.
    if err := events.Record(ctx, event.ID, event.Type, body); err != nil {
        return bedrock.JSON(http.StatusServiceUnavailable, nil) // sender retries
    }

    // Best-effort. If this is never run — dropped at the limit, lost in a
    // crash, or returned ErrAfterUnavailable in a test — the sweep picks the
    // event up, so the error needs no handling here.
    _ = bedrock.After(ctx, func(ctx context.Context) {
        process(ctx, event.ID)
    })
    return bedrock.JSON(http.StatusAccepted, nil)
}
```

`process` claims the event before doing anything (an atomic update from `received`/`failed`, or from a `processing` claim whose lease has expired, to `processing`), runs the handler, and marks it `processed` or `failed`. The claim is what makes it safe for the `After` task and the sweep to race for the same event: one wins, the other walks away.

The sweep is an ordinary bedrock job:

```go
func (s *Service) Jobs() []bedrock.Job {
    return []bedrock.Job{{
        Schedule: "@every 1m",
        Handler: func(ctx context.Context) error {
            // Unfinished events older than a grace period, so the sweep is
            // not competing with After for brand-new ones.
            ids, err := events.Unfinished(ctx, time.Minute)
            if err != nil {
                return err
            }
            for _, id := range ids {
                process(ctx, id)
            }
            return nil
        },
    }}
}
```

Three things this design has to own that the sender's retries used to own:

- **Retries and their end.** Count attempts, back off, and give up into a terminal state that someone is alerted about. Otherwise a poison event is retried every minute forever.
- **Ordering.** Senders rarely guarantee order, and your own retries reorder events further. Handlers should check the current state of the object rather than trust that events arrive in sequence.
- **Idempotency.** Processing is at-least-once: a crash after the work but before `processed` is written means it runs again.

If processing is fast and reliable, returning 2xx only after processing — and letting the sender's retries be your retry loop — is less machinery. This pattern earns its keep when processing is slow, or depends on something flaky enough that you would rather own the retries.

## What bedrock guarantees

| | |
|---|---|
| **When tasks run** | After the handler returns normally and its response has been written. A task may start just before the last bytes leave the process, never before the handler returns. |
| **Client disconnected** | Tasks still run. The handler did its part. |
| **Handler panicked** | Tasks are discarded. |
| **Context** | Carries the request's values (request ID, auth claims), but not its cancellation: it is not cancelled when the response is sent or the client hangs up. It is cancelled if the task is still running at the shutdown deadline. |
| **Order** | Tasks from one request run in the order registered, one after another, on one goroutine. Tasks from different requests run concurrently. |
| **Panics in a task** | Recovered and logged at `Error` with a stack trace. The process keeps running, and later tasks from the same request still run. |
| **Limit** | At most `Options.AfterLimit` requests' tasks run at once (default `DefaultAfterLimit`, 100). Past that, new tasks are **dropped and logged** at `Warn`, not queued — a queue in memory is exactly what a crash loses, and the request that registered them is not slowed down. |
| **Outside a request** | `After` returns `ErrAfterUnavailable` and the task never runs: when `ctx` is not from a bedrock route (a job, a handler called directly in a test), or the handler has already returned (a goroutine it left behind). |
| **Middleware** | Can call `After` too; it receives the same context. |

## Shutdown

Tasks are drained between the servers and `OnStop`:

1. Servers stop accepting and finish in-flight requests. Requests finishing now can still hand off tasks.
2. **After tasks are drained.** New tasks are refused (dropped and logged), and running ones are waited for, with whatever is left of the server phase of `ShutdownTimeout`. They never eat into the `OnStopTimeout` reserve.
3. If tasks are still running when that runs out, their contexts are cancelled and bedrock moves on without them, logging how many. A task that ignores its context cannot be stopped, only abandoned; shutdown stays bounded.
4. `OnStop` runs. Because it comes after the drain, a database pool it closes is still open while tasks finish.

The default `ShutdownTimeout` is 30 seconds. A task that legitimately runs longer than that will be cut off on every deploy, which is one more reason to keep the guarantee in the sweep and not in the task.
