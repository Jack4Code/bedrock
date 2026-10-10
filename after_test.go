package bedrock

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Jack4Code/bedrock/config"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// newTestRunner returns a runner logging into a buffer the test can inspect,
// drained when the test ends so no task outlives it.
func newTestRunner(t *testing.T, limit int) (*afterRunner, *syncBuffer) {
	t.Helper()
	logs := &syncBuffer{}
	a := newAfterRunner(limit, slog.New(slog.NewTextHandler(logs, nil)))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		a.drain(ctx)
	})
	return a, logs
}

// serveWithAfter serves one handler through serveRoute on a real listener,
// with the given runner. net/http's own error log is silenced so a test that
// panics a handler on purpose does not spray a stack trace.
func serveWithAfter(t *testing.T, a *afterRunner, h Handler) string {
	t.Helper()
	srv := httptest.NewUnstartedServer(serveRoute(h, quietLogger(), a))
	srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	srv.Start()
	t.Cleanup(srv.Close)
	return srv.URL
}

func get(t *testing.T, url string) int {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

// waitClosed fails the test if ch is not closed within a few seconds.
func waitClosed(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

func waitForLog(t *testing.T, logs *syncBuffer, msg string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(logs.String(), msg) {
		if time.Now().After(deadline) {
			t.Fatalf("expected log %q; logs:\n%s", msg, logs.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// ---------------------------------------------------------------------------
// tests: scheduling
// ---------------------------------------------------------------------------

// TestAfterRunsAfterResponse is the point of the feature: the caller has its
// response while the task is still running.
func TestAfterRunsAfterResponse(t *testing.T) {
	a, _ := newTestRunner(t, 0)
	started, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})

	url := serveWithAfter(t, a, func(ctx context.Context, r *http.Request) Response {
		if err := After(ctx, func(context.Context) {
			close(started)
			<-release
			close(finished)
		}); err != nil {
			t.Errorf("After: %v", err)
		}
		return JSON(http.StatusAccepted, nil)
	})

	if code := get(t, url); code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", code)
	}
	// The response is in hand; the task must be running and not yet done.
	waitClosed(t, started, "task to start")
	select {
	case <-finished:
		t.Fatal("task finished before it was released; the response waited on it")
	default:
	}
	close(release)
	waitClosed(t, finished, "task to finish")
}

// TestAfterContextOutlivesRequest: the task's context keeps the request's
// values but is not cancelled when the request ends.
func TestAfterContextOutlivesRequest(t *testing.T) {
	a, _ := newTestRunner(t, 0)
	type key struct{}
	responded, checked := make(chan struct{}), make(chan struct{})
	var taskErr error
	var taskVal any

	inner := serveRoute(func(ctx context.Context, r *http.Request) Response {
		_ = After(ctx, func(ctx context.Context) {
			<-responded
			time.Sleep(50 * time.Millisecond) // let net/http finish the request
			taskErr, taskVal = ctx.Err(), ctx.Value(key{})
			close(checked)
		})
		return JSON(http.StatusOK, nil)
	}, quietLogger(), a)
	// Stand-in for anything upstream that puts values on the request context,
	// such as a request ID.
	outer := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		inner.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), key{}, "req-123")))
	})
	srv := httptest.NewServer(outer)
	defer srv.Close()

	get(t, srv.URL)
	close(responded)
	waitClosed(t, checked, "task to inspect its context")

	if taskErr != nil {
		t.Errorf("task context was cancelled after the request ended: %v", taskErr)
	}
	if taskVal != "req-123" {
		t.Errorf("task context lost the request's values: got %v", taskVal)
	}
}

// TestAfterUnavailableOutsideRequest: After cannot silently accept a task it
// will never run.
func TestAfterUnavailableOutsideRequest(t *testing.T) {
	if err := After(context.Background(), func(context.Context) {}); !errors.Is(err, ErrAfterUnavailable) {
		t.Errorf("After on a non-request context = %v, want ErrAfterUnavailable", err)
	}

	a, _ := newTestRunner(t, 0)
	ctxs := make(chan context.Context, 1)
	url := serveWithAfter(t, a, func(ctx context.Context, r *http.Request) Response {
		ctxs <- ctx
		return JSON(http.StatusOK, nil)
	})
	get(t, url)
	handlerCtx := <-ctxs

	// A goroutine the handler left behind, registering too late.
	if err := After(handlerCtx, func(context.Context) {}); !errors.Is(err, ErrAfterUnavailable) {
		t.Errorf("After after the handler returned = %v, want ErrAfterUnavailable", err)
	}
}

// TestAfterTasksRunInOrderAndSurvivePanic: a panicking task is logged and the
// rest of the request's tasks still run, in order.
func TestAfterTasksRunInOrderAndSurvivePanic(t *testing.T) {
	a, logs := newTestRunner(t, 0)
	var mu sync.Mutex
	var order []int
	done := make(chan struct{})
	record := func(n int) {
		mu.Lock()
		order = append(order, n)
		mu.Unlock()
	}

	url := serveWithAfter(t, a, func(ctx context.Context, r *http.Request) Response {
		_ = After(ctx, func(context.Context) { record(1) })
		_ = After(ctx, func(context.Context) { panic("boom") })
		_ = After(ctx, func(context.Context) { record(3); close(done) })
		return JSON(http.StatusOK, nil)
	})
	get(t, url)
	waitClosed(t, done, "last task")

	mu.Lock()
	defer mu.Unlock()
	if fmt.Sprint(order) != "[1 3]" {
		t.Errorf("tasks ran as %v, want [1 3]", order)
	}
	if !strings.Contains(logs.String(), "after task panicked") || !strings.Contains(logs.String(), "boom") {
		t.Errorf("panic was not logged; logs:\n%s", logs.String())
	}
}

// TestAfterRunsWhenClientDisconnected: the handler did its part, so a caller
// that hung up does not cancel the follow-up work.
func TestAfterRunsWhenClientDisconnected(t *testing.T) {
	a, _ := newTestRunner(t, 0)
	ran := make(chan struct{})

	url := serveWithAfter(t, a, func(ctx context.Context, r *http.Request) Response {
		_ = After(ctx, func(context.Context) { close(ran) })
		<-ctx.Done() // returns only once the client has gone
		return JSON(http.StatusOK, nil)
	})

	client := &http.Client{Timeout: 100 * time.Millisecond}
	if resp, err := client.Get(url); err == nil {
		resp.Body.Close()
		t.Fatal("expected the client to time out")
	}
	waitClosed(t, ran, "task after client disconnect")
}

// TestAfterSkippedWhenHandlerPanics: a handler that panics never finished its
// part, so its tasks are discarded rather than run.
func TestAfterSkippedWhenHandlerPanics(t *testing.T) {
	a, _ := newTestRunner(t, 0)
	ran := make(chan struct{}, 1)

	url := serveWithAfter(t, a, func(ctx context.Context, r *http.Request) Response {
		_ = After(ctx, func(context.Context) { ran <- struct{}{} })
		panic("handler failed")
	})

	if resp, err := http.Get(url); err == nil {
		resp.Body.Close()
	}
	select {
	case <-ran:
		t.Fatal("task registered by a panicking handler ran")
	case <-time.After(200 * time.Millisecond):
	}
}

// TestAfterLimitDropsInsteadOfQueuing: at the limit, new tasks are dropped
// and logged, and the request they came from is not slowed down.
func TestAfterLimitDropsInsteadOfQueuing(t *testing.T) {
	a, logs := newTestRunner(t, 1)
	release := make(chan struct{})
	defer close(release)
	first := make(chan struct{})
	secondRan := make(chan struct{}, 1)

	var n int
	var mu sync.Mutex
	url := serveWithAfter(t, a, func(ctx context.Context, r *http.Request) Response {
		mu.Lock()
		n++
		call := n
		mu.Unlock()
		if call == 1 {
			_ = After(ctx, func(context.Context) { close(first); <-release })
		} else {
			_ = After(ctx, func(context.Context) { secondRan <- struct{}{} })
		}
		return JSON(http.StatusOK, nil)
	})

	get(t, url)
	waitClosed(t, first, "first task to occupy the only slot")

	// A runner that queued instead of dropping would hold this request until
	// the first task finished, which it never does while the test runs.
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		t.Fatalf("second request waited for a slot instead of dropping its task: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("second request status = %d, want 200", resp.StatusCode)
	}
	waitForLog(t, logs, "after tasks dropped: limit reached")
	select {
	case <-secondRan:
		t.Fatal("task past the limit ran")
	case <-time.After(100 * time.Millisecond):
	}
}

// ---------------------------------------------------------------------------
// tests: drain
// ---------------------------------------------------------------------------

func TestAfterDrainWaitsForRunningTasks(t *testing.T) {
	a, _ := newTestRunner(t, 0)
	finished := make(chan struct{})
	a.submit(context.Background(), "GET", "/x", []func(context.Context){
		func(context.Context) { time.Sleep(200 * time.Millisecond); close(finished) },
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	a.drain(ctx)

	select {
	case <-finished:
	default:
		t.Fatal("drain returned before a running task finished")
	}
}

// TestAfterDrainCancelsAtDeadline: shutdown stays bounded. A task still
// running at the deadline has its context cancelled and is abandoned.
func TestAfterDrainCancelsAtDeadline(t *testing.T) {
	a, logs := newTestRunner(t, 0)
	sawCancel := make(chan struct{})
	a.submit(context.Background(), "GET", "/x", []func(context.Context){
		func(ctx context.Context) { <-ctx.Done(); close(sawCancel) },
	})

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	a.drain(ctx)
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("drain took %s past a 100ms deadline", elapsed)
	}
	waitClosed(t, sawCancel, "task context to be cancelled at the deadline")
	if !strings.Contains(logs.String(), "after tasks still running at shutdown deadline") {
		t.Errorf("abandonment was not logged; logs:\n%s", logs.String())
	}
}

func TestAfterDrainRejectsNewTasks(t *testing.T) {
	a, logs := newTestRunner(t, 0)
	a.drain(context.Background())

	ran := make(chan struct{}, 1)
	a.submit(context.Background(), "GET", "/x", []func(context.Context){
		func(context.Context) { ran <- struct{}{} },
	})
	waitForLog(t, logs, "after tasks dropped: shutting down")
	select {
	case <-ran:
		t.Fatal("task submitted after drain ran")
	case <-time.After(100 * time.Millisecond):
	}
}

// TestShutdownWaitsForAfterTasksBeforeOnStop is the ordering that makes After
// usable with a database: a task still running when shutdown begins finishes
// before OnStop gets the chance to close the pool under it.
func TestShutdownWaitsForAfterTasksBeforeOnStop(t *testing.T) {
	httpPort, healthPort := freePort(t), freePort(t)
	events := &eventLog{}
	taskStarted := make(chan struct{})

	app := &fakeApp{events: events}
	app.routes = []Route{{
		Method:     "POST",
		Path:       "/webhook",
		Visibility: Public,
		Handler: func(ctx context.Context, r *http.Request) Response {
			_ = After(ctx, func(context.Context) {
				close(taskStarted)
				time.Sleep(300 * time.Millisecond)
				events.add("after-done")
			})
			return JSON(http.StatusAccepted, nil)
		},
	}}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, app, config.BaseConfig{HTTPPort: httpPort, HealthPort: healthPort},
			Options{Logger: quietLogger()})
	}()
	waitForHTTP(t, fmt.Sprintf("http://127.0.0.1:%d/health", healthPort))

	resp, err := http.Post(fmt.Sprintf("http://127.0.0.1:%d/webhook", httpPort), "application/json", nil)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", resp.StatusCode)
	}

	waitClosed(t, taskStarted, "task to start")
	cancel() // shutdown begins with the request answered and the task mid-flight

	if err := <-done; err != nil {
		t.Fatalf("run returned %v", err)
	}
	events.requireOrder(t, "after-done", "OnStop")
}
