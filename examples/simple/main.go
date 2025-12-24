package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"

	"bedrock"
	"github.com/Jack4Code/bedrock/config"
)

type SimpleApp struct{}

func (a *SimpleApp) OnStart(ctx context.Context) error {
	fmt.Println("App starting...")
	return nil
}

func (a *SimpleApp) OnStop(ctx context.Context) error {
	fmt.Println("App gracefully shutting down...")
	return nil
}

func (a *SimpleApp) Routes() []bedrock.Route {
	return []bedrock.Route{
		{
			Method:  "GET",
			Path:    "/hello",
			Handler: a.helloHandler,
		},
		{
			Method:  "GET",
			Path:    "/error",
			Handler: a.errorHandler,
		},
		{
			Method:  "POST",
			Path:    "/user",
			Handler: a.createUser,
		},
		{
			Method:  "POST",
			Path:    "/uploadFile",
			Handler: a.uploadDocumentHandler,
		},
	}
}

func (a *SimpleApp) helloHandler(ctx context.Context, r *http.Request) bedrock.Response {
	return bedrock.JSON(200, map[string]string{"message": "Hello!"})
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
		return bedrock.JSON(400, "Invalid JSON")
	}

	return bedrock.JSON(201, user)
}

func (a *SimpleApp) uploadDocumentHandler(ctx context.Context, r *http.Request) bedrock.Response {
	if err := bedrock.ParseMultipartForm(r, 0); err != nil {
		return bedrock.JSON(400, "Failed to parse form")
	}

	uploadedFile, err := bedrock.GetUploadedFile(r, "document")
	if err != nil {
		return bedrock.JSON(400, "No file uploaded")
	}
	defer uploadedFile.Close()

	// Create destination file
	dst, err := os.Create(filepath.Join("uploads", uploadedFile.Filename))
	if err != nil {
		return bedrock.JSON(500, "Failed to create file")
	}
	defer dst.Close()

	// Copy uploaded file to destination
	if _, err := io.Copy(dst, uploadedFile.File); err != nil {
		return bedrock.JSON(500, "Failed to save file")
	}

	return bedrock.JSON(200, map[string]interface{}{
		"filename": uploadedFile.Filename,
		"path":     filepath.Join("uploads", uploadedFile.Filename),
	})
}

func main() {
	app := &SimpleApp{}

	// Load configuration
	loader := config.NewLoader("config.toml")
	var cfg config.BaseConfig
	if err := loader.Load(&cfg); err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	if err := bedrock.Run(app, cfg); err != nil {
		fmt.Printf("Error: %v\n", err)
	}
}
