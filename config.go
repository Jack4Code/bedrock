package bedrock

import (
	"os"

	"github.com/BurntSushi/toml"
)

type Config struct {
	HTTPPort   string `toml:"http_port"`
	HealthPort string `toml:"health_port"`
	LogLevel   string `toml:"log_level"`
}

func LoadConfig() Config {
	cfg := Config{
		// Defaults
		HTTPPort:   "8080",
		HealthPort: "9090",
		LogLevel:   "info",
	}

	// Try to read config.toml
	if _, err := toml.DecodeFile("config.toml", &cfg); err != nil {
		// If file doesn't exist, that's okay - use defaults
		// If file exists but has errors, you might want to log/panic
		panic("panic for now on failed decoding of config file")
	}

	// Environment variables override config file
	if port := os.Getenv("HTTP_PORT"); port != "" {
		cfg.HTTPPort = port
	}
	if logLevel := os.Getenv("LOG_LEVEL"); logLevel != "" {
		cfg.LogLevel = logLevel
	}
	if port := os.Getenv("HEALTH_PORT"); port != "" {
		cfg.HealthPort = port
	}

	return cfg
}
