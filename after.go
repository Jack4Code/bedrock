package bedrock

import (
	"context"
	"errors"
	"log/slog"
	"runtime/debug"
	"sync"
)

// ErrAfterUnavailable is returned by After when there is no request to attach
// the task to: ctx did not come from a route bedrock is serving (a job, a
// test calling a handler directly), or the handler has already returned.
var ErrAfterUnavailable = errors.New("bedrock: After called outside a request bedrock is serving")

// DefaultAfterLimit is how many requests' After tasks may run at once when
// Options.AfterLimit is not set.
const DefaultAfterLimit = 100

// After schedules fn to run once the current handler has returned and its
// response has been written, so the caller gets its answer without waiting
// for fn. It is for work the response does not depend on.
//
// After is best-effort, and that is the first thing to know about it. fn runs
// in this process after the caller has been told the request succeeded; if
// the process crashes, is killed, or is still running fn when the shutdown
// deadline passes, fn's work is lost and nobody is told. Anything that must
// happen has to be made durable before the response — written to a database
// or a queue — with After as the fast path and something else (a job that
// sweeps for unfinished work) as the guarantee.
//
// What bedrock does provide:
//
//   - fn's context carries the request's values but not its cancellation, so
//     it is not cancelled when the response is sent or the client hangs up.
//     It is cancelled if fn is still running when bedrock's shutdown deadline
//     passes.
//   - Shutdown waits for running tasks, within the time left of the server
//     drain phase, before OnStop runs — so resources OnStop closes, such as a
//     database pool, are still open while tasks finish.
//   - A panic in fn is recovered and logged; it does not crash the process,
//     and later tasks from the same request still run.
//   - At most Options.AfterLimit requests' tasks run at once. Past that,
//     tasks are dropped and logged rather than queued, because a queue in
//     memory is exactly the kind of state a crash loses.
//
// Tasks registered by one request run in the order registered, one after
// another, on a single goroutine. They run whenever the handler returns
// normally — including when the client disconnected first, since the handler
// did its part — but not if the handler panics. A task may start just before
// the last bytes of the response leave the process; it never starts before
// the handler has returned.
//
// After returns ErrAfterUnavailable, and fn never runs, if ctx is not a
// bedrock request context or the handler has already returned. Middleware
// may call After too: it receives the same context.
func After(ctx context.Context, fn func(ctx context.Context)) error {
	if fn == nil {
		panic("bedrock: After called with a nil func")
	}
	scope, _ := ctx.Value(afterScopeKey{}).(*afterScope)
	if scope == nil {
		return ErrAfterUnavailable
	}
	return scope.add(fn)
}

type afterScopeKey struct{}

// afterScope collects one request's tasks while its handler runs.
type afterScope struct {
	mu     sync.Mutex
	tasks  []func(context.Context)
	sealed bool
}

func (s *afterScope) add(fn func(context.Context)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sealed {
		return ErrAfterUnavailable
	}
	s.tasks = append(s.tasks, fn)
	return nil
}

// seal stops further registrations and returns what was registered. Safe on a
// nil scope, and safe to call twice; the second call returns nothing.
func (s *afterScope) seal() []func(context.Context) {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sealed = true
	tasks := s.tasks
	s.tasks = nil
	return tasks
}

// afterRunner runs sealed task lists for the whole process and drains them at
// shutdown. One exists per run, shared by every route.
type afterRunner struct {
	logger *slog.Logger
	slots  chan struct{} // one per request whose tasks are running

	// stop is cancelled when the drain deadline passes, and every task's
	// context with it.
	stop       context.Context
	cancelStop context.CancelFunc

	mu     sync.Mutex
	closed bool
	wg     sync.WaitGroup
}

func newAfterRunner(limit int, logger *slog.Logger) *afterRunner {
	if limit <= 0 {
		limit = DefaultAfterLimit
	}
	stop, cancel := context.WithCancel(context.Background())
	return &afterRunner{
		logger:     logger,
		slots:      make(chan struct{}, limit),
		stop:       stop,
		cancelStop: cancel,
	}
}

// submit starts one request's tasks on their own goroutine, or drops them —
// logged — when the runner is at its limit or draining. It never blocks: it
// runs inside the request, and the caller is waiting.
func (a *afterRunner) submit(reqCtx context.Context, method, path string, tasks []func(context.Context)) {
	if len(tasks) == 0 {
		return
	}

	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		a.logger.Warn("after tasks dropped: shutting down",
			"method", method, "path", path, "tasks", len(tasks))
		return
	}
	select {
	case a.slots <- struct{}{}:
	default:
		a.mu.Unlock()
		a.logger.Warn("after tasks dropped: limit reached",
			"method", method, "path", path, "tasks", len(tasks), "limit", cap(a.slots))
		return
	}
	// Added under the lock that drain takes to close the runner, so drain's
	// Wait can never miss a task that was admitted.
	a.wg.Add(1)
	a.mu.Unlock()

	go func() {
		defer a.wg.Done()
		defer func() { <-a.slots }()

		ctx, cancel := context.WithCancel(context.WithoutCancel(reqCtx))
		defer cancel()
		stopWatching := context.AfterFunc(a.stop, cancel)
		defer stopWatching()

		for i, task := range tasks {
			a.runOne(ctx, method, path, i, task)
		}
	}()
}

func (a *afterRunner) runOne(ctx context.Context, method, path string, index int, task func(context.Context)) {
	defer func() {
		if p := recover(); p != nil {
			a.logger.Error("after task panicked",
				"method", method, "path", path, "task", index,
				"panic", p, "stack", string(debug.Stack()))
		}
	}()
	task(ctx)
}

// drain stops admitting tasks and waits for running ones until ctx is done.
// If they are still running then, their contexts are cancelled and drain
// returns without them: a task that ignores its context cannot be stopped,
// only abandoned, and shutdown must stay bounded.
func (a *afterRunner) drain(ctx context.Context) {
	a.mu.Lock()
	a.closed = true
	a.mu.Unlock()

	done := make(chan struct{})
	go func() {
		a.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-ctx.Done():
		a.logger.Warn("after tasks still running at shutdown deadline; cancelling and abandoning them",
			"requests", len(a.slots))
	}
	a.cancelStop()
}
