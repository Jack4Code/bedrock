package main

import (
	"fmt"
	"log"
	"os"

	"github.com/Jack4Code/bedrock/config"
)

// AppConfig demonstrates how to embed bedrock's BaseConfig
// in your application's configuration struct
type AppConfig struct {
	Bedrock config.BaseConfig `toml:"bedrock"`

	// Application-specific configuration fields
	DatabaseURL    string `toml:"database_url" env:"DATABASE_URL"`
	MaxConnections int    `toml:"max_connections" env:"MAX_CONNECTIONS"`
	APIKey         string `toml:"api_key" env:"API_KEY"`
	CacheTTL       int    `toml:"cache_ttl" env:"CACHE_TTL"`
}

func main() {
	fmt.Println("Bedrock Config Demo")
	fmt.Println("===================")
	fmt.Println()

	// Determine config file path (default to config.toml in current directory)
	configPath := "config.toml"
	if len(os.Args) > 1 {
		configPath = os.Args[1]
	}

	fmt.Printf("Loading configuration from: %s\n", configPath)
	fmt.Println()

	// Create a new config loader
	loader := config.NewLoader(configPath)

	// Load the configuration
	var cfg AppConfig
	if err := loader.Load(&cfg); err != nil {
		log.Fatalf("Failed to load configuration: %v", err)
	}

	// Display the loaded configuration
	fmt.Println("Bedrock Configuration:")
	fmt.Printf("  HTTP Port:    %d\n", cfg.Bedrock.HTTPPort)
	fmt.Printf("  Health Port:  %d\n", cfg.Bedrock.HealthPort)
	fmt.Printf("  Metrics Port: %d\n", cfg.Bedrock.MetricsPort)
	fmt.Printf("  Log Level:    %s\n", cfg.Bedrock.LogLevel)
	fmt.Printf("  Environment:  %s\n", cfg.Bedrock.Environment)
	fmt.Println()

	fmt.Println("Application Configuration:")
	fmt.Printf("  Database URL:    %s\n", cfg.DatabaseURL)
	fmt.Printf("  Max Connections: %d\n", cfg.MaxConnections)
	fmt.Printf("  API Key:         %s\n", maskAPIKey(cfg.APIKey))
	fmt.Printf("  Cache TTL:       %d\n", cfg.CacheTTL)
	fmt.Println()

	fmt.Println("Environment Variable Overrides:")
	checkEnvOverride("HTTP_PORT")
	checkEnvOverride("HEALTH_PORT")
	checkEnvOverride("METRICS_PORT")
	checkEnvOverride("LOG_LEVEL")
	checkEnvOverride("ENVIRONMENT")
	checkEnvOverride("DATABASE_URL")
	checkEnvOverride("MAX_CONNECTIONS")
	checkEnvOverride("API_KEY")
	checkEnvOverride("CACHE_TTL")
	fmt.Println()

	fmt.Println("✓ Configuration loaded successfully!")
}

// maskAPIKey masks most of an API key for security
func maskAPIKey(key string) string {
	if key == "" {
		return "(not set)"
	}
	if len(key) <= 8 {
		return "****"
	}
	return key[:4] + "****" + key[len(key)-4:]
}

// checkEnvOverride checks if an environment variable is set
func checkEnvOverride(envVar string) {
	value := os.Getenv(envVar)
	if value != "" {
		fmt.Printf("  ✓ %s is overridden\n", envVar)
	} else {
		fmt.Printf("    %s (using TOML value)\n", envVar)
	}
}
