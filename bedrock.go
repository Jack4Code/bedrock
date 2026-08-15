package bedrock

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Jack4Code/bedrock/config"
	"github.com/gorilla/mux"
)

// Handler takes context and request, returns a Response
type Handler func(ctx context.Context, r *http.Request) Response

// Response knows how to write itself to http.ResponseWriter
type Response interface {
	Write(ctx context.Context, w http.ResponseWriter) error
}

// App interface
type App interface {
	OnStart(ctx context.Context) error
	OnStop(ctx context.Context) error
	Routes() []Route
}

// Route represents an HTTP route
type Route struct {
	Method     string
	Path       string
	Handler    Handler
	Middleware []Middleware // Optional per-route middleware
	IsPrefix   bool         // If true, matches all paths with this prefix
	Visibility Visibility   // Defaults to Private (zero value) — fail closed
}

// CORSConfig holds CORS configuration
type CORSConfig struct {
	AllowedOrigins   []string
	AllowedMethods   []string
	AllowedHeaders   []string
	ExposedHeaders   []string
	AllowCredentials bool
	MaxAge           int
}

// Options configures optional bedrock behaviour.
type Options struct {
	CORS   *CORSConfig
	Logger *slog.Logger // optional; defaults to slog.Default()
	Hosts  HostConfig   // optional; if zero, host matching is skipped (dev mode)

	// Middleware wraps every application route this process registers, without
	// each Route having to list it. It runs outside any per-route Middleware —
	// the chain is Chain(handler, append(globals, route...)...) — so globals
	// execute first, in the order given, and a route with no Middleware of its
	// own still gets them.
	//
	// It exists for cross-cutting checks that must not be forgettable. The
	// motivating case is an edge token: a secret header injected by a reverse
	// proxy and validated here, so a request that reached the process without
	// transiting the proxy is dropped. Applied per route, a check like that is
	// missing from the next route somebody adds — which is precisely the
	// failure it defends against. Being a property of the server rather than of
	// each route is the whole of its value.
	//
	// Four boundaries, each deliberate:
	//
	//   - Health endpoints are excluded. When HTTPPort == HealthPort, /health,
	//     /ready and /live are registered as raw handlers and never enter this
	//     chain. A global that rejects unauthenticated requests would otherwise
	//     fail every liveness probe, and an orchestrator whose probes fail
	//     restarts the task indefinitely.
	//   - The loopback subrouter is included. HostConfig.TrustLoopback matches
	//     on the Host header, which the client supplies; a global that did not
	//     apply there could be skipped by any remote caller sending
	//     "Host: localhost", turning this field into a way around itself.
	//   - OPTIONS preflight bypasses it. Preflight is answered by a separate
	//     raw handler, because routing it through an auth-style global breaks
	//     CORS for legitimate browsers, which cannot attach credentials to a
	//     preflight. The consequence is that OPTIONS returns 200 for any
	//     registered path regardless of what the global middleware would
	//     decide, so it can be used to enumerate which paths exist. That is the
	//     accepted trade.
	//   - CORS remains outermost. Globals run inside it, so a rejected request
	//     still carries CORS headers and a browser sees the status rather than
	//     an opaque network error.
	//
	// Unlike Serve and RunJobs there is no env var override: middleware is
	// code, not configuration.
	Middleware []Middleware

	// Serve restricts which visibilities this process registers routes for.
	// An empty slice (the default) serves every visibility, preserving the
	// single-process behaviour. A non-empty slice serves only the listed
	// visibilities; routes of any other visibility are skipped (404), exactly
	// as if no host were configured for them.
	//
	// This lets the same image run as separate task groups that each own a
	// subset of the surface — e.g. a public-facing group serving {Public,
	// Gated} and an internal group serving {Private} — so private routes are
	// not merely host-gated but absent from the public process entirely.
	//
	// The BEDROCK_SERVE env var (comma-separated visibility names, e.g.
	// "public,gated") overrides this field when set, so deployments can
	// parameterise the surface per task group without code changes.
	Serve []Visibility

	// RunJobs controls whether this process starts the app's scheduled jobs
	// (its JobsProvider). It defaults to true. Set it to false on all but one
	// task group in a split deployment so cron jobs run once, not once per
	// group. The BEDROCK_RUN_JOBS env var (a bool) overrides this when set.
	RunJobs *bool

	// Servers are additional long-running components bedrock supervises
	// alongside the HTTP router — a gRPC server, a metrics endpoint, a consumer
	// loop. They are started after the app's OnStart, in the order given, and
	// drained concurrently before its OnStop — so a server that will not drain
	// costs the others nothing but the wait.
	//
	// A service with no HTTP routes but one or more Servers is a normal server,
	// not a background process.
	Servers []Server

	// Health lets the caller supply the health tracker rather than have bedrock
	// create one. Pass a shared instance when something outside the HTTP
	// endpoints needs to report the same state — the gRPC health service, for
	// example. Bedrock creates one when this is nil.
	Health *HealthStatus

	// ShutdownTimeout bounds the whole drain sequence. Servers drain
	// concurrently within it, then the health server and the app's OnStop run
	// with whatever is left — never less than OnStopTimeout. Defaults to
	// DefaultShutdownTimeout.
	ShutdownTimeout time.Duration

	// OnStopTimeout is the slice of ShutdownTimeout held back for app cleanup,
	// so OnStop is never handed an already-expired context.
	//
	// Without a reserve, a Server that uses its entire drain budget leaves
	// nothing for OnStop, and for a gRPC server with a streaming or
	// long-polling endpoint that is the normal case rather than the edge case:
	// an open stream never drains on its own, so every deploy would reach
	// OnStop with a dead context and silently skip flushing metrics,
	// deregistering from service discovery, or closing the database.
	//
	// Servers get ShutdownTimeout minus this reserve; OnStop gets whatever
	// remains, floored at this value. Defaults to a fifth of ShutdownTimeout.
	// It must be shorter than ShutdownTimeout.
	OnStopTimeout time.Duration
}

// DefaultShutdownTimeout is how long bedrock spends draining before giving up,
// when Options.ShutdownTimeout is not set.
const DefaultShutdownTimeout = 30 * time.Second

// defaultOnStopReserveFraction is the share of ShutdownTimeout reserved for
// OnStop when Options.OnStopTimeout is not set: enough for cleanup to do
// something useful, small enough to leave servers most of the budget.
const defaultOnStopReserveFraction = 5

// DefaultCORSConfig returns a permissive CORS config for development
func DefaultCORSConfig() CORSConfig {
	return CORSConfig{
		AllowedOrigins:   []string{"*"},
		AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "PATCH", "OPTIONS"},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type", "X-CSRF-Token"},
		ExposedHeaders:   []string{"Link"},
		AllowCredentials: false,
		MaxAge:           300,
	}
}

func Run(app App, cfg config.BaseConfig) error {
	return RunWithCORS(app, cfg, DefaultCORSConfig())
}

// isLoopbackHost reports whether a request's Host header names the loopback
// interface (localhost, 127.0.0.1 or ::1), ignoring any port. Used as the
// matcher for the loopback subrouter when HostConfig.TrustLoopback is set.
func isLoopbackHost(r *http.Request, _ *mux.RouteMatch) bool {
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

func RunWithCORS(app App, cfg config.BaseConfig, corsConfig CORSConfig) error {
	return RunWithOptions(app, cfg, Options{CORS: &corsConfig})
}

// resolveServe determines which visibilities this process should serve. The
// BEDROCK_SERVE env var (comma-separated visibility names) takes precedence
// over optsServe when set; if it is set but names no valid visibility, that's
// a configuration error (fail fast rather than silently serving everything).
// A nil result means "serve all".
func resolveServe(optsServe []Visibility) (serveSet, error) {
	if raw, ok := os.LookupEnv("BEDROCK_SERVE"); ok {
		set := serveSet{}
		for tok := range strings.SplitSeq(raw, ",") {
			if strings.TrimSpace(tok) == "" {
				continue
			}
			v, err := ParseVisibility(tok)
			if err != nil {
				return nil, fmt.Errorf("BEDROCK_SERVE: %w", err)
			}
			set[v] = true
		}
		if len(set) == 0 {
			return nil, fmt.Errorf("BEDROCK_SERVE is set to %q but names no valid visibility", raw)
		}
		return set, nil
	}
	if len(optsServe) > 0 {
		set := serveSet{}
		for _, v := range optsServe {
			set[v] = true
		}
		return set, nil
	}
	return nil, nil // serve all
}

// resolveRunJobs determines whether this process runs scheduled jobs. The
// BEDROCK_RUN_JOBS env var (a bool) takes precedence over optsRunJobs; absent
// both, jobs run (the default).
func resolveRunJobs(optsRunJobs *bool) (bool, error) {
	if raw, ok := os.LookupEnv("BEDROCK_RUN_JOBS"); ok {
		b, err := strconv.ParseBool(strings.TrimSpace(raw))
		if err != nil {
			return false, fmt.Errorf("BEDROCK_RUN_JOBS: invalid bool %q: %w", raw, err)
		}
		return b, nil
	}
	if optsRunJobs != nil {
		return *optsRunJobs, nil
	}
	return true, nil
}

func RunWithOptions(app App, cfg config.BaseConfig, opts Options) error {
	// signal.NotifyContext replaces the hand-rolled quit channel this used to
	// keep in three separate places, and makes the lifecycle testable: run takes
	// a context, so a test cancels it directly instead of signalling the process.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	return run(ctx, app, cfg, opts)
}

// run is the whole server lifecycle. Cancelling ctx begins shutdown.
//
// The ordering matters and is the same on every path: health server, OnStart,
// jobs, servers on the way up; servers, health server, jobs, OnStop on the way
// down. Servers start in the order given and drain together — they are
// independent, and see teardown for why sharing one sequential budget was
// worse. Every startup failure unwinds through the same teardown as a normal
// shutdown, so a process that dies half-started still releases what it
// acquired.
func run(ctx context.Context, app App, cfg config.BaseConfig, opts Options) error {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	corsConfig := DefaultCORSConfig()
	if opts.CORS != nil {
		corsConfig = *opts.CORS
	}
	shutdownTimeout := opts.ShutdownTimeout
	if shutdownTimeout <= 0 {
		shutdownTimeout = DefaultShutdownTimeout
	}
	onStopReserve := opts.OnStopTimeout
	if onStopReserve <= 0 {
		onStopReserve = shutdownTimeout / defaultOnStopReserveFraction
	}
	if onStopReserve >= shutdownTimeout {
		return fmt.Errorf("OnStopTimeout (%s) must be shorter than ShutdownTimeout (%s): it is carved out of it, not added to it",
			onStopReserve, shutdownTimeout)
	}
	healthStatus := opts.Health
	if healthStatus == nil {
		healthStatus = NewHealthStatus()
	}

	// Resolve which visibilities this process serves and whether it runs jobs.
	// Both can be overridden by env so the same image is parameterised per task
	// group. Resolve before anything starts so a bad config fails fast.
	served, err := resolveServe(opts.Serve)
	if err != nil {
		return err
	}
	runJobs, err := resolveRunJobs(opts.RunJobs)
	if err != nil {
		return err
	}
	logger.Info("resolved serve configuration", "serve", served.String(), "run_jobs", runJobs)

	// Build the job runner up front so an invalid cron expression fails before
	// anything is listening, but do not start it until the app is healthy.
	//
	// Jobs deliberately get a background context rather than the lifecycle one:
	// jobs.Stop() waits for a running job to finish, and handing them a context
	// that is already cancelled by the shutdown signal would cut them off
	// mid-work instead.
	var jobs *jobRunner
	if jp, ok := app.(JobsProvider); ok {
		if runJobs {
			jobs, err = newJobRunner(context.Background(), jp.Jobs(), logger)
			if err != nil {
				return fmt.Errorf("failed to register jobs: %w", err)
			}
		} else {
			logger.Info("scheduled jobs disabled for this process (run_jobs=false)")
		}
	}

	// Health endpoints share the application port when both are configured the
	// same, rather than binding twice.
	mergeServers := cfg.HTTPPort == cfg.HealthPort

	// The health server starts before OnStart so an orchestrator can see the
	// container is alive while the application is still initialising.
	var healthServer *httpServer
	if !mergeServers {
		healthServer = newHTTPServer("health", ":"+strconv.Itoa(cfg.HealthPort), healthMux(healthStatus), logger)
		if err := healthServer.Start(ctx); err != nil {
			return fmt.Errorf("failed to start health server: %w", err)
		}
		logger.Info("started health server", "port", cfg.HealthPort)
	} else {
		logger.Info("health endpoints will be merged into main server", "port", cfg.HTTPPort)
	}

	var (
		started     []Server
		jobsStarted bool
	)

	// teardown drains whatever is currently running. Both the normal shutdown
	// and every startup failure go through it, which is why there is only one
	// copy of this sequence.
	teardown := func(runOnStop bool) {
		deadline := time.Now().Add(shutdownTimeout)

		// budget is what is left of the drain, floored at the OnStop reserve.
		// Each phase asks for it fresh, so a phase that overran cannot hand the
		// next one a context that is already dead.
		budget := func() time.Duration {
			return max(time.Until(deadline), onStopReserve)
		}

		// Servers drain concurrently, each with the whole server phase. They are
		// independent listeners with no ordering relationship, and draining them
		// in sequence made them share a budget they had no reason to share: one
		// server that would not drain — a gRPC stream, an open SSE connection —
		// consumed the deadline and every server behind it was cut off mid
		// request. Running them together bounds the phase by the slowest server
		// rather than by their sum.
		serverCtx, cancelServers := context.WithDeadline(context.Background(), deadline.Add(-onStopReserve))
		var wg sync.WaitGroup
		for _, s := range started {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := s.Shutdown(serverCtx); err != nil {
					logger.Error("server forced to shutdown", "server", s.Name(), "err", err)
				}
			}()
		}
		wg.Wait()
		cancelServers()

		// The health server goes down after them, so probes keep answering for
		// as long as anything is still draining.
		if healthServer != nil {
			healthCtx, cancel := context.WithTimeout(context.Background(), budget())
			err := healthServer.Shutdown(healthCtx)
			cancel()
			if err != nil {
				logger.Error("server forced to shutdown", "server", healthServer.Name(), "err", err)
			}
		}
		if jobsStarted {
			jobs.Stop()
		}
		if runOnStop {
			// OnStop is bounded so app cleanup cannot hold a shutdown open
			// forever, but never below the reserve — see Options.OnStopTimeout.
			stopCtx, cancel := context.WithTimeout(context.Background(), budget())
			err := app.OnStop(stopCtx)
			cancel()
			if err != nil {
				logger.Error("error during OnStop", "err", err)
			}
		}
	}

	if err := app.OnStart(ctx); err != nil {
		// OnStart failed, so there is nothing for OnStop to undo.
		teardown(false)
		return fmt.Errorf("failed to start app: %w", err)
	}
	healthStatus.SetHealthy(true)

	// Routes are read after OnStart because handlers commonly close over state
	// the app initialises there.
	routes := app.Routes()
	if mergeServers {
		if err := checkReservedPaths(routes); err != nil {
			// OnStart already ran, so unwind it rather than leaving its
			// resources held by a process that is about to exit.
			teardown(true)
			return err
		}
	}

	if jobs != nil {
		jobs.Start()
		jobsStarted = true
		logger.Info("started scheduled jobs")
	}

	servers := make([]Server, 0, len(opts.Servers)+1)
	switch {
	case len(routes) > 0:
		handler := buildRouter(routes, served, opts.Hosts, opts.Middleware, healthStatus, mergeServers, corsConfig, logger)
		servers = append(servers, newHTTPServer("http", ":"+strconv.Itoa(cfg.HTTPPort), handler, logger))
	case mergeServers:
		// No application routes, but the health endpoints live on this port and
		// still need something listening for them.
		logger.Info("no HTTP routes, serving health endpoints only", "port", cfg.HTTPPort)
		servers = append(servers, newHTTPServer("http", ":"+strconv.Itoa(cfg.HTTPPort), healthMux(healthStatus), logger))
	}
	servers = append(servers, opts.Servers...)

	for _, s := range servers {
		if err := s.Start(ctx); err != nil {
			teardown(true)
			return fmt.Errorf("failed to start %s server: %w", s.Name(), err)
		}
		started = append(started, s)
		logger.Info("server started", "server", s.Name())
	}

	healthStatus.SetReady(true)

	if len(routes) == 0 && len(opts.Servers) == 0 {
		logger.Info("no HTTP routes and no additional servers, running in background mode")
	}

	<-ctx.Done()
	logger.Info("shutting down")

	// Fail readiness before draining so load balancers stop sending new traffic
	// while in-flight requests finish.
	healthStatus.SetReady(false)
	teardown(true)

	logger.Info("servers stopped")
	return nil
}

// checkReservedPaths rejects application routes that would shadow the health
// endpoints. It only applies when the health endpoints share the application
// port; on separate ports there is nothing to collide with.
func checkReservedPaths(routes []Route) error {
	reserved := []string{"/health", "/ready", "/live"}
	for _, route := range routes {
		for _, p := range reserved {
			if route.Path == p {
				return fmt.Errorf("route conflict: application route %s conflicts with reserved health endpoint %s", route.Path, p)
			}
		}
	}
	return nil
}

// buildRouter registers the application routes and returns the fully wrapped
// handler for the main HTTP server.
func buildRouter(
	routes []Route,
	served serveSet,
	hosts HostConfig,
	globalMiddleware []Middleware,
	healthStatus *HealthStatus,
	mergeServers bool,
	corsConfig CORSConfig,
	logger *slog.Logger,
) http.Handler {
	router := mux.NewRouter()

	// Health endpoints are registered before the app routes and outside CORS:
	// they are infrastructure probes, and they match on any host so probes from
	// K8s/Nomad work regardless of the Host header.
	if mergeServers {
		router.HandleFunc("/health", healthCheckHandler(healthStatus))
		router.HandleFunc("/ready", readyCheckHandler(healthStatus))
		router.HandleFunc("/live", liveCheckHandler(healthStatus))
		logger.Info("health endpoints registered on main router")
	}

	// When loopback trust is enabled, build one shared subrouter that matches a
	// loopback Host header. Every route is also registered here (below),
	// regardless of visibility, so co-located callers hitting the server
	// directly on localhost reach all routes. See HostConfig.TrustLoopback.
	var loopbackRouter *mux.Router
	if hosts.configured() && hosts.TrustLoopback {
		loopbackRouter = router.NewRoute().MatcherFunc(isLoopbackHost).Subrouter()
		logger.Warn("loopback trust enabled: all routes are served to localhost callers regardless of visibility")
	}

	for _, route := range routes {
		r := route

		// Skip routes whose visibility this process isn't serving. The route is
		// absent entirely (404), not merely host-gated — see Options.Serve.
		if !served.serves(r.Visibility) {
			logger.Info("route not served by this process, skipping",
				"visibility", r.Visibility.String(),
				"method", r.Method,
				"path", r.Path,
			)
			continue
		}

		// Global middleware wraps the route's own, so it runs first and applies
		// even to a route that declares none — see Options.Middleware. The
		// chain is copied rather than appended onto globalMiddleware in place,
		// which would let one route's middleware leak into the next route's
		// chain through a shared backing array.
		handler := r.Handler
		if len(globalMiddleware)+len(r.Middleware) > 0 {
			chain := make([]Middleware, 0, len(globalMiddleware)+len(r.Middleware))
			chain = append(chain, globalMiddleware...)
			chain = append(chain, r.Middleware...)
			handler = Chain(handler, chain...)
		}

		handlerFunc := func(w http.ResponseWriter, req *http.Request) {
			ctx := req.Context()
			response := handler(ctx, req)
			if err := response.Write(ctx, w); err != nil {
				http.Error(w, "Internal Server Error", 500)
			}
		}

		optionsFunc := func(w http.ResponseWriter, req *http.Request) {
			// Preflight requests just return 200 OK with CORS headers. This is
			// a raw handler and never enters the middleware chain, so neither
			// per-route nor global middleware sees an OPTIONS request — a
			// browser cannot attach credentials to a preflight, so gating it on
			// an auth-style check would break CORS for legitimate clients. See
			// Options.Middleware for what that concedes.
			w.WriteHeader(http.StatusOK)
		}

		// Choose where to register: a host-scoped subrouter when Hosts is
		// configured, or the main router (any host) in dev mode.
		var registerOn interface {
			HandleFunc(string, func(http.ResponseWriter, *http.Request)) *mux.Route
			PathPrefix(string) *mux.Route
		} = router

		if hosts.configured() {
			host := hosts.hostFor(r.Visibility)
			if host == "" {
				logger.Warn("no host configured for visibility, skipping route",
					"visibility", r.Visibility.String(),
					"method", r.Method,
					"path", r.Path,
				)
				continue
			}
			registerOn = router.Host(host).Subrouter()
			logger.Info("registered route",
				"method", r.Method,
				"path", r.Path,
				"visibility", r.Visibility.String(),
				"host", host,
			)
		} else {
			logger.Info("registered route (no host matching)",
				"method", r.Method,
				"path", r.Path,
				"visibility", r.Visibility.String(),
			)
		}

		// registerRoute wires the handler + OPTIONS preflight onto a target
		// router. Called for the visibility-scoped target and, when enabled,
		// the shared loopback subrouter.
		registerRoute := func(target interface {
			HandleFunc(string, func(http.ResponseWriter, *http.Request)) *mux.Route
			PathPrefix(string) *mux.Route
		}) {
			if r.IsPrefix {
				target.PathPrefix(r.Path).HandlerFunc(handlerFunc).Methods(r.Method)
				target.PathPrefix(r.Path).HandlerFunc(optionsFunc).Methods("OPTIONS")
			} else {
				target.HandleFunc(r.Path, handlerFunc).Methods(r.Method)
				target.HandleFunc(r.Path, optionsFunc).Methods("OPTIONS")
			}
		}

		registerRoute(registerOn)
		if loopbackRouter != nil {
			registerRoute(loopbackRouter)
		}
	}

	return corsMiddleware(corsConfig)(router)
}

// corsMiddleware wraps an http.Handler with CORS headers
func corsMiddleware(cfg CORSConfig) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")

			// Check if origin is allowed
			allowed := false
			for _, allowedOrigin := range cfg.AllowedOrigins {
				if allowedOrigin == "*" || allowedOrigin == origin {
					allowed = true
					if allowedOrigin == "*" {
						origin = "*"
					}
					break
				}
			}

			if allowed {
				w.Header().Set("Access-Control-Allow-Origin", origin)
			}

			// Set other CORS headers
			if len(cfg.AllowedMethods) > 0 {
				methods := ""
				for i, method := range cfg.AllowedMethods {
					if i > 0 {
						methods += ", "
					}
					methods += method
				}
				w.Header().Set("Access-Control-Allow-Methods", methods)
			}

			if len(cfg.AllowedHeaders) > 0 {
				headers := ""
				for i, header := range cfg.AllowedHeaders {
					if i > 0 {
						headers += ", "
					}
					headers += header
				}
				w.Header().Set("Access-Control-Allow-Headers", headers)
			}

			if len(cfg.ExposedHeaders) > 0 {
				headers := ""
				for i, header := range cfg.ExposedHeaders {
					if i > 0 {
						headers += ", "
					}
					headers += header
				}
				w.Header().Set("Access-Control-Expose-Headers", headers)
			}

			if cfg.AllowCredentials {
				w.Header().Set("Access-Control-Allow-Credentials", "true")
			}

			if cfg.MaxAge > 0 {
				w.Header().Set("Access-Control-Max-Age", fmt.Sprintf("%d", cfg.MaxAge))
			}

			next.ServeHTTP(w, r)
		})
	}
}

// --- Request Helpers

func DecodeJSON(r *http.Request, v any) error {
	return json.NewDecoder(r.Body).Decode(v)
}

// --- Response implementations ---

type JSONResponse struct {
	StatusCode int
	Data       any
	Headers    http.Header
}

func (r JSONResponse) Write(ctx context.Context, w http.ResponseWriter) error {
	for key, values := range r.Headers {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(r.StatusCode)
	return json.NewEncoder(w).Encode(r.Data)
}

func JSON(statusCode int, data any) Response {
	return JSONResponse{StatusCode: statusCode, Data: data}
}

func JSONWithHeaders(statusCode int, data any, headers http.Header) Response {
	return JSONResponse{StatusCode: statusCode, Data: data, Headers: headers}
}

func Error(data any) Response {
	return JSONResponse{StatusCode: 500, Data: data}
}
