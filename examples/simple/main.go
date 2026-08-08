// Command simple is a minimal bedrock service: a few JSON routes, a file
// upload, and the lifecycle hooks. Run it with `go run ./examples/simple` from
// the repository root.
package main

import (
	"context"
	"io"
	"log"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"

	"github.com/Jack4Code/bedrock"
	"github.com/Jack4Code/bedrock/config"
)

const uploadDir = "uploads"

type SimpleApp struct {
	logger *slog.Logger
}

// OnStart runs before anything is served. Routes are read after it returns, so
// a handler can close over whatever is set up here.
func (a *SimpleApp) OnStart(ctx context.Context) error {
	a.logger.Info("app starting")
	// Created here rather than committed, since git does not track empty
	// directories and the upload handler needs somewhere to write.
	return os.MkdirAll(uploadDir, 0o755)
}

// OnStop receives a context with a deadline — see Options.OnStopTimeout. Real
// cleanup (closing a database, flushing metrics) should honour it.
func (a *SimpleApp) OnStop(ctx context.Context) error {
	a.logger.Info("app shutting down")
	return nil
}

// Routes declares Visibility explicitly. The zero value is Private, which is
// the safe default, but saying what you mean is better than relying on it.
func (a *SimpleApp) Routes() []bedrock.Route {
	return []bedrock.Route{
		{
			Method:     "GET",
			Path:       "/hello",
			Visibility: bedrock.Public,
			Handler:    a.helloHandler,
		},
		{
			Method:     "GET",
			Path:       "/error",
			Visibility: bedrock.Public,
			Handler:    a.errorHandler,
		},
		{
			Method:     "POST",
			Path:       "/user",
			Visibility: bedrock.Public,
			Handler:    a.createUser,
		},
		{
			Method:     "POST",
			Path:       "/uploadFile",
			Visibility: bedrock.Private,
			Handler:    a.uploadDocumentHandler,
		},
	}
}

func (a *SimpleApp) helloHandler(ctx context.Context, r *http.Request) bedrock.Response {
	return bedrock.JSON(http.StatusOK, map[string]string{"message": "Hello!"})
}

func (a *SimpleApp) errorHandler(ctx context.Context, r *http.Request) bedrock.Response {
	return bedrock.Error("Something went wrong")
}

type User struct {
	Firstname string
	Lastname  string
	Email     string
}

func (a *SimpleApp) createUser(ctx context.Context, r *http.Request) bedrock.Response {
	var user User
	if err := bedrock.DecodeJSON(r, &user); err != nil {
		return bedrock.JSON(http.StatusBadRequest, "Invalid JSON")
	}
	return bedrock.JSON(http.StatusCreated, user)
}

func (a *SimpleApp) uploadDocumentHandler(ctx context.Context, r *http.Request) bedrock.Response {
	if err := bedrock.ParseMultipartForm(r, 0); err != nil {
		return bedrock.JSON(http.StatusBadRequest, "Failed to parse form")
	}

	uploaded, err := bedrock.GetUploadedFile(r, "document")
	if err != nil {
		return bedrock.JSON(http.StatusBadRequest, "No file uploaded")
	}
	defer uploaded.Close()

	// filepath.Base strips any directory component the client sent. Without it
	// a filename of "../../etc/passwd" would be written outside uploadDir.
	name := filepath.Base(uploaded.Filename)
	if name == "." || name == string(filepath.Separator) {
		return bedrock.JSON(http.StatusBadRequest, "Invalid filename")
	}
	dstPath := filepath.Join(uploadDir, name)

	dst, err := os.Create(dstPath)
	if err != nil {
		a.logger.Error("failed to create upload destination", "path", dstPath, "err", err)
		return bedrock.JSON(http.StatusInternalServerError, "Failed to create file")
	}
	defer dst.Close()

	if _, err := io.Copy(dst, uploaded.File); err != nil {
		a.logger.Error("failed to write upload", "path", dstPath, "err", err)
		return bedrock.JSON(http.StatusInternalServerError, "Failed to save file")
	}

	return bedrock.JSON(http.StatusOK, map[string]any{
		"filename": name,
		"path":     dstPath,
	})
}

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	var cfg config.BaseConfig
	if err := config.NewLoader("examples/simple/config.toml").Load(&cfg); err != nil {
		log.Fatalf("failed to load config: %v", err)
	}

	app := &SimpleApp{logger: logger}

	// RunWithOptions rather than Run so the example shows where the logger and
	// the shutdown budget are supplied. bedrock.Run(app, cfg) is the short form.
	if err := bedrock.RunWithOptions(app, cfg, bedrock.Options{
		Logger: logger,
	}); err != nil {
		log.Fatalf("server exited: %v", err)
	}
}
