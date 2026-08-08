package bedrock

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/Jack4Code/bedrock/config"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// eventLog records lifecycle callbacks in the order they happen, which is the
// property most of these tests are actually asserting on.
type eventLog struct {
	mu     sync.Mutex
	events []string
}

func (l *eventLog) add(e string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, e)
}

func (l *eventLog) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.events)
}

// requireOrder asserts every named event happened, in the order given. Other
// events may be interleaved.
func (l *eventLog) requireOrder(t *testing.T, want ...string) {
	t.Helper()
	got := l.all()
	at := -1
	for _, w := range want {
		idx := slices.Index(got, w)
		if idx < 0 {
			t.Fatalf("event %q never happened; got %v", w, got)
		}
		if idx <= at {
			t.Fatalf("event %q happened out of order; got %v, want order %v", w, got, want)
		}
		at = idx
	}
}

func (l *eventLog) contains(e string) bool {
	return slices.Contains(l.all(), e)
}

type fakeApp struct {
	events   *eventLog
	routes   []Route
	startErr error

	mu             sync.Mutex
	onStopDeadline bool
	onStopBudget   time.Duration
}

func (a *fakeApp) OnStart(ctx context.Context) error {
	a.events.add("OnStart")
	return a.startErr
}

func (a *fakeApp) OnStop(ctx context.Context) error {
	deadline, ok := ctx.Deadline()
	a.mu.Lock()
	a.onStopDeadline = ok
	a.onStopBudget = time.Until(deadline)
	a.mu.Unlock()
	a.events.add("OnStop")
	return nil
}

func (a *fakeApp) Routes() []Route { return a.routes }

func (a *fakeApp) stopHadDeadline() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.onStopDeadline
}

// stopBudget is how much time OnStop had left when it was called. Negative
// means it was handed an already-expired context.
func (a *fakeApp) stopBudget() time.Duration {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.onStopBudget
}

type fakeServer struct {
	name     string
	events   *eventLog
	startErr error
	// blockShutdown holds Shutdown until the context expires, standing in for a
	// server with work that will not drain.
	blockShutdown bool

	mu             sync.Mutex
	shutdownBudget time.Duration
}

func (s *fakeServer) Name() string { return s.name }

func (s *fakeServer) Start(ctx context.Context) error {
	if s.startErr != nil {
		s.events.add("start-failed-" + s.name)
		return s.startErr
	}
	s.events.add("start-" + s.name)
	return nil
}

func (s *fakeServer) Shutdown(ctx context.Context) error {
	if deadline, ok := ctx.Deadline(); ok {
		s.mu.Lock()
		s.shutdownBudget = time.Until(deadline)
		s.mu.Unlock()
	}
	if s.blockShutdown {
		<-ctx.Done()
		s.events.add("shutdown-timeout-" + s.name)
		return ctx.Err()
	}
	s.events.add("shutdown-" + s.name)
	return nil
}

// drainBudget is how much time this server had left when Shutdown was called.
func (s *fakeServer) drainBudget() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.shutdownBudget
}

// quietLogger keeps lifecycle logging out of test output.
func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}

// freePort returns a port that was free a moment ago. Racy in principle, but
// the alternative is threading listeners through the config API.
func freePort(t *testing.T) int {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	defer lis.Close()
	return lis.Addr().(*net.TCPAddr).Port
}

// waitForHTTP polls until the URL answers or the deadline passes.
func waitForHTTP(t *testing.T, url string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
		if err == nil {
			resp.Body.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("server at %s never came up", url)
}

// ---------------------------------------------------------------------------
// tests
// ---------------------------------------------------------------------------

// TestLifecycleOrdering pins the contract Options.Servers exists to provide:
// servers come up after the app, in the order given, and every one of them is
// drained before the app's OnStop. Servers drain concurrently, so the order
// they finish in is deliberately not asserted.
func TestLifecycleOrdering(t *testing.T) {
	log := &eventLog{}
	app := &fakeApp{events: log}
	a := &fakeServer{name: "a", events: log}
	b := &fakeServer{name: "b", events: log}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, app, config.BaseConfig{HTTPPort: freePort(t), HealthPort: freePort(t)},
			Options{Logger: quietLogger(), Servers: []Server{a, b}})
	}()

	waitFor(t, func() bool { return log.contains("start-b") })
	cancel()

	if err := <-done; err != nil {
		t.Fatalf("run returned %v", err)
	}
	log.requireOrder(t, "OnStart", "start-a", "start-b", "shutdown-a", "OnStop")
	log.requireOrder(t, "start-b", "shutdown-b", "OnStop")
}

// TestOnStopReceivesDeadline covers the change from context.Background(): app
// cleanup now shares the drain budget instead of being able to run forever.
func TestOnStopReceivesDeadline(t *testing.T) {
	log := &eventLog{}
	app := &fakeApp{events: log}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, app, config.BaseConfig{HTTPPort: freePort(t), HealthPort: freePort(t)},
			Options{Logger: quietLogger()})
	}()

	waitFor(t, func() bool { return log.contains("OnStart") })
	cancel()
	<-done

	if !app.stopHadDeadline() {
		t.Error("OnStop received a context with no deadline")
	}
}

// TestSlowServerDoesNotStarveOthers is the regression test for the sequential
// drain. A gRPC server with an open stream, or an SSE connection, consumes its
// whole budget on every shutdown; when servers drained one after another that
// left nothing for the ones behind it, and requests that would have finished
// were cut off instead. Draining concurrently means a stuck server costs the
// others nothing.
func TestSlowServerDoesNotStarveOthers(t *testing.T) {
	log := &eventLog{}
	app := &fakeApp{events: log}
	quick := &fakeServer{name: "quick", events: log}
	stuck := &fakeServer{name: "stuck", events: log, blockShutdown: true}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		// stuck is listed last, so the old reverse-order drain reached it first
		// and quick was the one left with nothing.
		done <- run(ctx, app, config.BaseConfig{HTTPPort: freePort(t), HealthPort: freePort(t)},
			Options{
				Logger:          quietLogger(),
				Servers:         []Server{quick, stuck},
				ShutdownTimeout: time.Second,
			})
	}()

	waitFor(t, func() bool { return log.contains("start-stuck") })
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("run returned %v", err)
	}

	if budget := quick.drainBudget(); budget <= 0 {
		t.Fatalf("quick was handed an already-expired context (%s left); a stuck server starved it", budget)
	}
	if !log.contains("shutdown-quick") {
		t.Error("quick never drained")
	}
	if !log.contains("shutdown-timeout-stuck") {
		t.Error("stuck was expected to burn its whole budget")
	}
}

// TestOnStopKeepsBudgetAfterExhaustedDrain: a server that uses the entire drain
// must not leave app cleanup with a dead context. For a gRPC service with a
// streaming endpoint this is every deploy, not a rare case — an open stream
// never drains on its own — so OnStop silently getting zero time would mean
// metrics never flushed and connections never closed.
func TestOnStopKeepsBudgetAfterExhaustedDrain(t *testing.T) {
	log := &eventLog{}
	app := &fakeApp{events: log}
	stuck := &fakeServer{name: "stuck", events: log, blockShutdown: true}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, app, config.BaseConfig{HTTPPort: freePort(t), HealthPort: freePort(t)},
			Options{
				Logger:          quietLogger(),
				Servers:         []Server{stuck},
				ShutdownTimeout: time.Second,
				OnStopTimeout:   200 * time.Millisecond,
			})
	}()

	waitFor(t, func() bool { return log.contains("start-stuck") })
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("run returned %v", err)
	}

	budget := app.stopBudget()
	if budget <= 0 {
		t.Fatalf("OnStop got an expired context (%s left) after a server used the whole drain", budget)
	}
	if budget > 200*time.Millisecond {
		t.Errorf("OnStop budget %s exceeds the reserve it should have been floored at", budget)
	}
}

// TestOnStopTimeoutMustFitInsideShutdownTimeout: the reserve is carved out of
// the drain budget rather than added to it, so a reserve at least as large as
// the whole budget leaves servers no time at all. Reject it at startup rather
// than at the deploy that discovers it.
func TestOnStopTimeoutMustFitInsideShutdownTimeout(t *testing.T) {
	log := &eventLog{}
	app := &fakeApp{events: log}

	err := run(context.Background(), app,
		config.BaseConfig{HTTPPort: freePort(t), HealthPort: freePort(t)},
		Options{Logger: quietLogger(), ShutdownTimeout: time.Second, OnStopTimeout: time.Second})

	if err == nil {
		t.Fatal("an OnStopTimeout equal to ShutdownTimeout was accepted")
	}
	if log.contains("OnStart") {
		t.Error("the app was started before the bad configuration was rejected")
	}
}

// TestServerStartFailureUnwinds checks that a process failing halfway through
// startup still releases what it acquired, rather than exiting with the app's
// resources held.
func TestServerStartFailureUnwinds(t *testing.T) {
	log := &eventLog{}
	app := &fakeApp{events: log}
	good := &fakeServer{name: "good", events: log}
	bad := &fakeServer{name: "bad", events: log, startErr: errors.New("port in use")}

	err := run(context.Background(), app,
		config.BaseConfig{HTTPPort: freePort(t), HealthPort: freePort(t)},
		Options{Logger: quietLogger(), Servers: []Server{good, bad}})

	if err == nil {
		t.Fatal("run succeeded despite a server failing to start")
	}
	// The server that did start must be shut down, and the app must be stopped.
	log.requireOrder(t, "OnStart", "start-good", "start-failed-bad", "shutdown-good", "OnStop")
}

// TestOnStartFailureSkipsOnStop: if OnStart never succeeded there is nothing
// for OnStop to undo, and calling it would hand the app a teardown for a
// startup that did not happen.
func TestOnStartFailureSkipsOnStop(t *testing.T) {
	log := &eventLog{}
	app := &fakeApp{events: log, startErr: errors.New("database unreachable")}
	srv := &fakeServer{name: "a", events: log}

	err := run(context.Background(), app,
		config.BaseConfig{HTTPPort: freePort(t), HealthPort: freePort(t)},
		Options{Logger: quietLogger(), Servers: []Server{srv}})

	if err == nil {
		t.Fatal("run succeeded despite OnStart failing")
	}
	if log.contains("OnStop") {
		t.Error("OnStop was called even though OnStart failed")
	}
	if log.contains("start-a") {
		t.Error("a server was started even though OnStart failed")
	}
}

// TestShutdownDrainsInFlightRequests is the property conduit's long-polling
// depends on: a request already being served finishes before the app's OnStop
// tears down what the handler is using.
func TestShutdownDrainsInFlightRequests(t *testing.T) {
	httpPort := freePort(t)
	healthPort := freePort(t)
	log := &eventLog{}
	handlerEntered := make(chan struct{})

	app := &fakeApp{events: log}
	app.routes = []Route{{
		Method:     "GET",
		Path:       "/slow",
		Visibility: Public,
		Handler: func(ctx context.Context, r *http.Request) Response {
			close(handlerEntered)
			time.Sleep(300 * time.Millisecond)
			log.add("handler-done")
			return JSON(http.StatusOK, map[string]string{"status": "ok"})
		},
	}}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, app, config.BaseConfig{HTTPPort: httpPort, HealthPort: healthPort},
			Options{Logger: quietLogger()})
	}()

	base := fmt.Sprintf("http://127.0.0.1:%d", httpPort)
	waitForHTTP(t, fmt.Sprintf("http://127.0.0.1:%d/health", healthPort))

	codes := make(chan int, 1)
	go func() {
		resp, err := http.Get(base + "/slow")
		if err != nil {
			codes <- -1
			return
		}
		defer resp.Body.Close()
		io.Copy(io.Discard, resp.Body)
		codes <- resp.StatusCode
	}()

	<-handlerEntered
	cancel() // shutdown begins while the request is still being served

	if code := <-codes; code != http.StatusOK {
		t.Fatalf("in-flight request got status %d, want 200 — it was cut off by shutdown", code)
	}
	if err := <-done; err != nil {
		t.Fatalf("run returned %v", err)
	}
	log.requireOrder(t, "handler-done", "OnStop")
}

// TestBlockedShutdownRespectsTimeout: a Server that will not drain must not be
// able to hold the process open past the shutdown budget.
func TestBlockedShutdownRespectsTimeout(t *testing.T) {
	log := &eventLog{}
	app := &fakeApp{events: log}
	stuck := &fakeServer{name: "stuck", events: log, blockShutdown: true}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, app, config.BaseConfig{HTTPPort: freePort(t), HealthPort: freePort(t)},
			Options{Logger: quietLogger(), Servers: []Server{stuck}, ShutdownTimeout: 200 * time.Millisecond})
	}()

	waitFor(t, func() bool { return log.contains("start-stuck") })
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run never returned: a server that would not drain hung shutdown")
	}
	log.requireOrder(t, "shutdown-timeout-stuck", "OnStop")
}

// TestServersRunWithoutHTTPRoutes: a gRPC-only service has no routes but is
// still a server, and must not be treated as a background process.
func TestServersRunWithoutHTTPRoutes(t *testing.T) {
	log := &eventLog{}
	app := &fakeApp{events: log} // no routes
	srv := &fakeServer{name: "grpc", events: log}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, app, config.BaseConfig{HTTPPort: freePort(t), HealthPort: freePort(t)},
			Options{Logger: quietLogger(), Servers: []Server{srv}})
	}()

	waitFor(t, func() bool { return log.contains("start-grpc") })
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("run returned %v", err)
	}
	log.requireOrder(t, "OnStart", "start-grpc", "shutdown-grpc", "OnStop")
}

// TestReservedPathConflictUnwindsOnStart: the conflict is still rejected, but
// the app no longer keeps its resources while the process exits.
func TestReservedPathConflictUnwindsOnStart(t *testing.T) {
	port := freePort(t)
	log := &eventLog{}
	app := &fakeApp{events: log}
	app.routes = []Route{{
		Method: "GET", Path: "/health", Visibility: Public,
		Handler: func(ctx context.Context, r *http.Request) Response { return JSON(200, nil) },
	}}

	// Same port for both, so health endpoints are merged and can collide.
	err := run(context.Background(), app, config.BaseConfig{HTTPPort: port, HealthPort: port},
		Options{Logger: quietLogger()})

	if err == nil {
		t.Fatal("a route colliding with /health was accepted")
	}
	if !log.contains("OnStop") {
		t.Error("OnStart ran but OnStop did not, leaking whatever the app acquired")
	}
}

// TestMergedHealthServesWithoutRoutes: when the ports are merged and the app
// has no routes, something must still answer the health probes.
func TestMergedHealthServesWithoutRoutes(t *testing.T) {
	port := freePort(t)
	log := &eventLog{}
	app := &fakeApp{events: log}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, app, config.BaseConfig{HTTPPort: port, HealthPort: port},
			Options{Logger: quietLogger()})
	}()

	url := fmt.Sprintf("http://127.0.0.1:%d/health", port)
	waitForHTTP(t, url)

	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("health probe: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("health returned %d, want 200", resp.StatusCode)
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("run returned %v", err)
	}
}

func TestHTTPServerReportsBindFailure(t *testing.T) {
	// Hold a port, then ask a server to bind the same one.
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer lis.Close()
	addr := lis.Addr().String()

	srv := newHTTPServer("clash", addr, http.NewServeMux(), quietLogger())
	if err := srv.Start(context.Background()); err == nil {
		_ = srv.Shutdown(context.Background())
		t.Fatal("Start succeeded on an address already in use")
	}
}

func TestHTTPServerBoundAddr(t *testing.T) {
	srv := newHTTPServer("ephemeral", "127.0.0.1:0", http.NewServeMux(), quietLogger())
	if err := srv.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer srv.Shutdown(context.Background())

	if srv.boundAddr() == nil {
		t.Fatal("boundAddr is nil after a successful Start")
	}
	if port := srv.boundAddr().(*net.TCPAddr).Port; port == 0 {
		t.Error("boundAddr still reports port 0 after binding")
	}
}

// waitFor polls cond until it holds or the test times out.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition never became true")
}
