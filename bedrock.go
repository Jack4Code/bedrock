package bedrock

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

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
	Method  string
	Path    string
	Handler Handler
}

func Run(app App, cfg Config) error {
	ctx := context.Background()

	// Create health status tracker
	healthStatus := newHealthStatus()

	// Start health server BEFORE calling OnStart
	// This way Nomad/K8s can see the container is alive
	healthServer := startHealthServer(cfg.HealthPort, healthStatus)

	// Call app.OnStart()
	if err := app.OnStart(ctx); err != nil {
		return fmt.Errorf("failed to start app: %w", err)
	}

	// OnStart succeeded, mark as healthy
	healthStatus.SetHealthy(true)

	routes := app.Routes()

	if len(routes) == 0 {
		// No HTTP routes, but health server is running
		log.Println("No HTTP routes, running in background mode")

		// Mark as ready (no HTTP server to wait for)
		healthStatus.SetReady(true)

		// Wait for shutdown signal
		quit := make(chan os.Signal, 1)
		signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
		<-quit

		log.Println("Shutting down...")

		// Shutdown health server
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		healthServer.Shutdown(shutdownCtx)

		// Call app.OnStop()
		if err := app.OnStop(ctx); err != nil {
			log.Printf("Error during OnStop: %v", err)
		}

		return nil
	}

	// Create main HTTP server
	router := mux.NewRouter()

	// Register routes
	for _, route := range routes {
		r := route
		router.HandleFunc(r.Path, func(w http.ResponseWriter, req *http.Request) {
			ctx := req.Context()
			response := r.Handler(ctx, req)
			if err := response.Write(ctx, w); err != nil {
				http.Error(w, "Internal Server Error", 500)
			}
		}).Methods(r.Method)
	}

	server := &http.Server{
		Addr:    ":" + cfg.HTTPPort,
		Handler: router,
	}

	// Start main server
	go func() {
		log.Printf("Starting server on :%s", cfg.HTTPPort)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("Server error: %v", err)
		}
	}()

	// Server is up, mark as ready
	healthStatus.SetReady(true)

	// Wait for shutdown signal
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("Shutting down servers...")

	// Mark as not ready (stop accepting new traffic)
	healthStatus.SetReady(false)

	// Graceful shutdown
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Shutdown main server
	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Printf("Main server forced to shutdown: %v", err)
	}

	// Shutdown health server
	if err := healthServer.Shutdown(shutdownCtx); err != nil {
		log.Printf("Health server forced to shutdown: %v", err)
	}

	// Call app.OnStop()
	if err := app.OnStop(ctx); err != nil {
		log.Printf("Error during OnStop: %v", err)
	}

	log.Println("Servers stopped")
	return nil
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
