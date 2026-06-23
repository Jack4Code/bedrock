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
}

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
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	corsConfig := DefaultCORSConfig()
	if opts.CORS != nil {
		corsConfig = *opts.CORS
	}
	hosts := opts.Hosts

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

	ctx := context.Background()

	// Create health status tracker
	healthStatus := newHealthStatus()

	// Start cron jobs if the app provides them and this process is designated
	// to run them. In a split deployment only one task group should set
	// run_jobs=true so jobs fire once, not once per group.
	var jobs *jobRunner
	if jp, ok := app.(JobsProvider); ok {
		if runJobs {
			jobs, err = newJobRunner(ctx, jp.Jobs(), logger)
			if err != nil {
				return fmt.Errorf("failed to register jobs: %w", err)
			}
		} else {
			logger.Info("scheduled jobs disabled for this process (run_jobs=false)")
		}
	}

	// Determine if we should merge health endpoints into main server
	// This happens when HTTP and Health ports are the same
	mergeServers := cfg.HTTPPort == cfg.HealthPort

	// Only start separate health server if ports differ
	var healthServer *http.Server
	if !mergeServers {
		// Start health server BEFORE calling OnStart
		// This way Nomad/K8s can see the container is alive
		healthServer = startHealthServer(strconv.Itoa(cfg.HealthPort), healthStatus)
	} else {
		logger.Info("health endpoints will be merged into main server", "port", cfg.HTTPPort)
	}

	// Call app.OnStart()
	if err := app.OnStart(ctx); err != nil {
		return fmt.Errorf("failed to start app: %w", err)
	}

	// OnStart succeeded, mark as healthy
	healthStatus.SetHealthy(true)

	// Start cron runner after app is healthy
	if jobs != nil {
		jobs.Start()
		logger.Info("started scheduled jobs")
	}

	routes := app.Routes()

	// Validate routes don't conflict with reserved health endpoints when merging
	if mergeServers {
		reservedPaths := []string{"/health", "/ready", "/live"}
		for _, route := range routes {
			for _, reserved := range reservedPaths {
				if route.Path == reserved {
					return fmt.Errorf("route conflict: application route %s conflicts with reserved health endpoint %s", route.Path, reserved)
				}
			}
		}
	}

	if len(routes) == 0 {
		// No HTTP routes, running in background mode
		if mergeServers {
			// When merging servers but no app routes exist, we still need to start
			// a server for the health endpoints
			logger.Info("no HTTP routes, starting server for health endpoints only")
			router := mux.NewRouter()

			// Register health endpoints (no CORS needed for health checks)
			router.HandleFunc("/health", healthCheckHandler(healthStatus))
			router.HandleFunc("/ready", readyCheckHandler(healthStatus))
			router.HandleFunc("/live", liveCheckHandler(healthStatus))

			server := &http.Server{
				Addr:    ":" + strconv.Itoa(cfg.HTTPPort),
				Handler: router,
			}

			go func() {
				logger.Info("starting health-only server", "port", cfg.HTTPPort)
				if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
					logger.Error("server error", "err", err)
				}
			}()

			// Mark as ready
			healthStatus.SetReady(true)

			// Wait for shutdown signal
			quit := make(chan os.Signal, 1)
			signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
			<-quit
			logger.Info("shutting down")

			// Shutdown server
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			server.Shutdown(shutdownCtx)

			// Stop scheduled jobs
			if jobs != nil {
				jobs.Stop()
			}

			// Call app.OnStop()
			if err := app.OnStop(ctx); err != nil {
				logger.Error("error during OnStop", "err", err)
			}

			return nil
		}

		// Separate health server is already running
		logger.Info("no HTTP routes, running in background mode")

		// Mark as ready (no HTTP server to wait for)
		healthStatus.SetReady(true)

		// Wait for shutdown signal
		quit := make(chan os.Signal, 1)
		signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
		<-quit
		logger.Info("shutting down")

		// Shutdown health server
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		healthServer.Shutdown(shutdownCtx)

		// Stop scheduled jobs
		if jobs != nil {
			jobs.Stop()
		}

		// Call app.OnStop()
		if err := app.OnStop(ctx); err != nil {
			logger.Error("error during OnStop", "err", err)
		}

		return nil
	}

	// Create main HTTP server
	router := mux.NewRouter()

	// If merging servers, add health endpoints to main router BEFORE app routes
	// Health endpoints should NOT have CORS or app middleware applied,
	// and should match on any host so probes from K8s/Nomad work regardless
	// of the Host header.
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

	// Register app routes
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

		// Apply middleware if present
		handler := r.Handler
		if len(r.Middleware) > 0 {
			handler = Chain(handler, r.Middleware...)
		}

		// Register the route
		handlerFunc := func(w http.ResponseWriter, req *http.Request) {
			ctx := req.Context()
			response := handler(ctx, req)
			if err := response.Write(ctx, w); err != nil {
				http.Error(w, "Internal Server Error", 500)
			}
		}

		optionsFunc := func(w http.ResponseWriter, req *http.Request) {
			// Preflight requests just return 200 OK with CORS headers
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

	// Wrap router with CORS middleware
	// Note: Health endpoints are registered before CORS, so they won't have CORS applied
	// This is correct - health checks are infrastructure endpoints
	corsHandler := corsMiddleware(corsConfig)(router)

	server := &http.Server{
		Addr:    ":" + strconv.Itoa(cfg.HTTPPort),
		Handler: corsHandler,
	}

	// Start main server
	go func() {
		logger.Info("starting server", "port", cfg.HTTPPort)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("server error", "err", err)
		}
	}()

	// Server is up, mark as ready
	healthStatus.SetReady(true)

	// Wait for shutdown signal
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	logger.Info("shutting down servers")

	// Mark as not ready (stop accepting new traffic)
	healthStatus.SetReady(false)

	// Graceful shutdown
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Shutdown main server
	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Error("main server forced to shutdown", "err", err)
	}

	// Shutdown health server only if it's separate
	if !mergeServers {
		if err := healthServer.Shutdown(shutdownCtx); err != nil {
			logger.Error("health server forced to shutdown", "err", err)
		}
	}

	// Stop scheduled jobs
	if jobs != nil {
		jobs.Stop()
	}

	// Call app.OnStop()
	if err := app.OnStop(ctx); err != nil {
		logger.Error("error during OnStop", "err", err)
	}

	logger.Info("servers stopped")
	return nil
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
