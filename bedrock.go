package bedrock

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
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
	IsPrefix   bool        // If true, matches all paths with this prefix
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

func RunWithCORS(app App, cfg config.BaseConfig, corsConfig CORSConfig) error {
	return RunWithOptions(app, cfg, Options{CORS: &corsConfig})
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

	ctx := context.Background()

	// Create health status tracker
	healthStatus := newHealthStatus()

	// Start cron jobs if the app provides them
	var jobs *jobRunner
	if jp, ok := app.(JobsProvider); ok {
		var err error
		jobs, err = newJobRunner(ctx, jp.Jobs(), logger)
		if err != nil {
			return fmt.Errorf("failed to register jobs: %w", err)
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
	// Health endpoints should NOT have CORS or app middleware applied
	if mergeServers {
		router.HandleFunc("/health", healthCheckHandler(healthStatus))
		router.HandleFunc("/ready", readyCheckHandler(healthStatus))
		router.HandleFunc("/live", liveCheckHandler(healthStatus))
		logger.Info("health endpoints registered on main router")
	}

	// Register app routes
	for _, route := range routes {
		r := route

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

		if r.IsPrefix {
			router.PathPrefix(r.Path).HandlerFunc(handlerFunc).Methods(r.Method)
			router.PathPrefix(r.Path).HandlerFunc(optionsFunc).Methods("OPTIONS")
		} else {
			router.HandleFunc(r.Path, handlerFunc).Methods(r.Method)
			router.HandleFunc(r.Path, optionsFunc).Methods("OPTIONS")
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
}

func (r JSONResponse) Write(ctx context.Context, w http.ResponseWriter) error {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(r.StatusCode)
	return json.NewEncoder(w).Encode(r.Data)
}

func JSON(statusCode int, data any) Response {
	return JSONResponse{StatusCode: statusCode, Data: data}
}

func Error(data any) Response {
	return JSONResponse{StatusCode: 500, Data: data}
}
