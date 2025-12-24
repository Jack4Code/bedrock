package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestAppConfig is an example of how an application would embed BaseConfig
type TestAppConfig struct {
	Bedrock          BaseConfig `toml:"bedrock"`
	AppSpecificField string     `toml:"app_field" env:"APP_FIELD"`
	DatabaseURL      string     `toml:"database_url" env:"DATABASE_URL"`
	MaxConnections   int        `toml:"max_connections" env:"MAX_CONNECTIONS"`
}

func TestNewLoader(t *testing.T) {
	loader := NewLoader("test.toml")
	if loader == nil {
		t.Fatal("NewLoader returned nil")
	}
	if loader.configPath != "test.toml" {
		t.Errorf("expected configPath to be 'test.toml', got %s", loader.configPath)
	}
}

func TestLoadTOMLFile(t *testing.T) {
	// Create a temporary TOML file
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.toml")

	tomlContent := `
app_field = "test_value"
database_url = "postgres://localhost/test"
max_connections = 100

[bedrock]
log_level = "debug"
metrics_port = 9090
health_port = 8080
environment = "production"
`

	if err := os.WriteFile(configPath, []byte(tomlContent), 0644); err != nil {
		t.Fatalf("failed to write test config file: %v", err)
	}

	// Load the config
	loader := NewLoader(configPath)
	var config TestAppConfig
	if err := loader.Load(&config); err != nil {
		t.Fatalf("failed to load config: %v", err)
	}

	// Verify BaseConfig fields
	if config.Bedrock.LogLevel != "debug" {
		t.Errorf("expected LogLevel to be 'debug', got %s", config.Bedrock.LogLevel)
	}
	if config.Bedrock.MetricsPort != 9090 {
		t.Errorf("expected MetricsPort to be 9090, got %d", config.Bedrock.MetricsPort)
	}
	if config.Bedrock.HealthPort != 8080 {
		t.Errorf("expected HealthPort to be 8080, got %d", config.Bedrock.HealthPort)
	}
	if config.Bedrock.Environment != "production" {
		t.Errorf("expected Environment to be 'production', got %s", config.Bedrock.Environment)
	}

	// Verify app-specific fields
	if config.AppSpecificField != "test_value" {
		t.Errorf("expected AppSpecificField to be 'test_value', got %s", config.AppSpecificField)
	}
	if config.DatabaseURL != "postgres://localhost/test" {
		t.Errorf("expected DatabaseURL to be 'postgres://localhost/test', got %s", config.DatabaseURL)
	}
	if config.MaxConnections != 100 {
		t.Errorf("expected MaxConnections to be 100, got %d", config.MaxConnections)
	}
}

func TestEnvOverrides(t *testing.T) {
	// Create a temporary TOML file
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.toml")

	tomlContent := `
app_field = "original_value"
max_connections = 50

[bedrock]
log_level = "info"
metrics_port = 9090
health_port = 8080
environment = "development"
`

	if err := os.WriteFile(configPath, []byte(tomlContent), 0644); err != nil {
		t.Fatalf("failed to write test config file: %v", err)
	}

	// Set environment variables
	os.Setenv("LOG_LEVEL", "error")
	os.Setenv("METRICS_PORT", "9999")
	os.Setenv("APP_FIELD", "overridden_value")
	os.Setenv("MAX_CONNECTIONS", "200")
	defer func() {
		os.Unsetenv("LOG_LEVEL")
		os.Unsetenv("METRICS_PORT")
		os.Unsetenv("APP_FIELD")
		os.Unsetenv("MAX_CONNECTIONS")
	}()

	// Load the config
	loader := NewLoader(configPath)
	var config TestAppConfig
	if err := loader.Load(&config); err != nil {
		t.Fatalf("failed to load config: %v", err)
	}

	// Verify environment variables overrode TOML values
	if config.Bedrock.LogLevel != "error" {
		t.Errorf("expected LogLevel to be 'error' (from env), got %s", config.Bedrock.LogLevel)
	}
	if config.Bedrock.MetricsPort != 9999 {
		t.Errorf("expected MetricsPort to be 9999 (from env), got %d", config.Bedrock.MetricsPort)
	}
	if config.AppSpecificField != "overridden_value" {
		t.Errorf("expected AppSpecificField to be 'overridden_value' (from env), got %s", config.AppSpecificField)
	}
	if config.MaxConnections != 200 {
		t.Errorf("expected MaxConnections to be 200 (from env), got %d", config.MaxConnections)
	}

	// Verify fields without env vars keep TOML values
	if config.Bedrock.HealthPort != 8080 {
		t.Errorf("expected HealthPort to be 8080 (from TOML), got %d", config.Bedrock.HealthPort)
	}
	if config.Bedrock.Environment != "development" {
		t.Errorf("expected Environment to be 'development' (from TOML), got %s", config.Bedrock.Environment)
	}
}

func TestLoadNonExistentFile(t *testing.T) {
	// Try to load a non-existent file
	loader := NewLoader("/nonexistent/path/config.toml")
	var config TestAppConfig

	// Should not error, just use zero values
	if err := loader.Load(&config); err != nil {
		t.Fatalf("expected no error for non-existent file, got: %v", err)
	}

	// All values should be zero/empty
	if config.Bedrock.LogLevel != "" {
		t.Errorf("expected empty LogLevel for non-existent file, got %s", config.Bedrock.LogLevel)
	}
}

func TestLoadWithInvalidTOML(t *testing.T) {
	// Create a temporary TOML file with invalid content
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "invalid.toml")

	invalidContent := `
[bedrock
this is not valid TOML
`

	if err := os.WriteFile(configPath, []byte(invalidContent), 0644); err != nil {
		t.Fatalf("failed to write test config file: %v", err)
	}

	// Load the config - should error
	loader := NewLoader(configPath)
	var config TestAppConfig
	if err := loader.Load(&config); err == nil {
		t.Fatal("expected error for invalid TOML, got nil")
	}
}

func TestLoadNilConfig(t *testing.T) {
	loader := NewLoader("test.toml")
	if err := loader.Load(nil); err == nil {
		t.Fatal("expected error for nil config, got nil")
	}
}

func TestLoadNonPointer(t *testing.T) {
	loader := NewLoader("test.toml")
	var config TestAppConfig
	if err := loader.Load(config); err == nil {
		t.Fatal("expected error for non-pointer config, got nil")
	}
}

func TestLoadPointerToNonStruct(t *testing.T) {
	loader := NewLoader("test.toml")
	var config string
	if err := loader.Load(&config); err == nil {
		t.Fatal("expected error for pointer to non-struct, got nil")
	}
}

func TestEnvOverrideInvalidInt(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.toml")

	tomlContent := `
[bedrock]
metrics_port = 9090
`

	if err := os.WriteFile(configPath, []byte(tomlContent), 0644); err != nil {
		t.Fatalf("failed to write test config file: %v", err)
	}

	// Set invalid int value
	os.Setenv("METRICS_PORT", "not_a_number")
	defer os.Unsetenv("METRICS_PORT")

	loader := NewLoader(configPath)
	var config TestAppConfig
	if err := loader.Load(&config); err == nil {
		t.Fatal("expected error for invalid int in env var, got nil")
	}
}

func TestBaseConfigOnly(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.toml")

	tomlContent := `
log_level = "warn"
metrics_port = 7777
health_port = 6666
environment = "staging"
`

	if err := os.WriteFile(configPath, []byte(tomlContent), 0644); err != nil {
		t.Fatalf("failed to write test config file: %v", err)
	}

	// Load directly into BaseConfig (not embedded)
	loader := NewLoader(configPath)
	var config BaseConfig
	if err := loader.Load(&config); err != nil {
		t.Fatalf("failed to load config: %v", err)
	}

	if config.LogLevel != "warn" {
		t.Errorf("expected LogLevel to be 'warn', got %s", config.LogLevel)
	}
	if config.MetricsPort != 7777 {
		t.Errorf("expected MetricsPort to be 7777, got %d", config.MetricsPort)
	}
	if config.HealthPort != 6666 {
		t.Errorf("expected HealthPort to be 6666, got %d", config.HealthPort)
	}
	if config.Environment != "staging" {
		t.Errorf("expected Environment to be 'staging', got %s", config.Environment)
	}
}

func TestEnvOverrideWithoutTOMLFile(t *testing.T) {
	// Set environment variables without a TOML file
	os.Setenv("LOG_LEVEL", "debug")
	os.Setenv("METRICS_PORT", "5555")
	os.Setenv("HEALTH_PORT", "4444")
	os.Setenv("ENVIRONMENT", "test")
	defer func() {
		os.Unsetenv("LOG_LEVEL")
		os.Unsetenv("METRICS_PORT")
		os.Unsetenv("HEALTH_PORT")
		os.Unsetenv("ENVIRONMENT")
	}()

	loader := NewLoader("/nonexistent/config.toml")
	var config BaseConfig
	if err := loader.Load(&config); err != nil {
		t.Fatalf("failed to load config: %v", err)
	}

	// All values should come from environment
	if config.LogLevel != "debug" {
		t.Errorf("expected LogLevel to be 'debug' (from env), got %s", config.LogLevel)
	}
	if config.MetricsPort != 5555 {
		t.Errorf("expected MetricsPort to be 5555 (from env), got %d", config.MetricsPort)
	}
	if config.HealthPort != 4444 {
		t.Errorf("expected HealthPort to be 4444 (from env), got %d", config.HealthPort)
	}
	if config.Environment != "test" {
		t.Errorf("expected Environment to be 'test' (from env), got %s", config.Environment)
	}
}
