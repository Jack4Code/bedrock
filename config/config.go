package config

import (
	"fmt"
	"log"
	"os"
	"reflect"
	"strconv"

	"github.com/BurntSushi/toml"
)

// BaseConfig contains bedrock's core configuration needs.
// Applications can embed this in their own config structs to inherit bedrock's settings.
type BaseConfig struct {
	HTTPPort    int    `toml:"http_port" env:"HTTP_PORT"`
	HealthPort  int    `toml:"health_port" env:"HEALTH_PORT"`
	MetricsPort int    `toml:"metrics_port" env:"METRICS_PORT"`
	LogLevel    string `toml:"log_level" env:"LOG_LEVEL"`
	Environment string `toml:"environment" env:"ENVIRONMENT"`
}

// GetHTTPPort returns the HTTP port to use, checking Nomad dynamic port allocation first.
// If NOMAD_PORT_http is set and valid, it returns that value.
// Otherwise, it falls back to the configured HTTPPort value.
func (b *BaseConfig) GetHTTPPort() int {
	return resolvePort("http", b.HTTPPort)
}

// GetHealthPort returns the health port to use, checking Nomad dynamic port allocation first.
// If NOMAD_PORT_health is set and valid, it returns that value.
// Otherwise, it falls back to the configured HealthPort value.
func (b *BaseConfig) GetHealthPort() int {
	return resolvePort("health", b.HealthPort)
}

// GetMetricsPort returns the metrics port to use, checking Nomad dynamic port allocation first.
// If NOMAD_PORT_metrics is set and valid, it returns that value.
// Otherwise, it falls back to the configured MetricsPort value.
func (b *BaseConfig) GetMetricsPort() int {
	return resolvePort("metrics", b.MetricsPort)
}

// resolvePort checks for Nomad dynamic port allocation and falls back to configured value.
// label is the port label (e.g., "http", "health", "metrics")
// fallback is the value from the config to use if Nomad env var is not set
func resolvePort(label string, fallback int) int {
	envVar := "NOMAD_PORT_" + label
	nomadPort := os.Getenv(envVar)

	if nomadPort == "" {
		// No Nomad env var, use config value
		return fallback
	}

	// Parse Nomad port
	port, err := strconv.Atoi(nomadPort)
	if err != nil {
		log.Printf("Warning: %s is set but invalid (%q), falling back to configured port %d", envVar, nomadPort, fallback)
		return fallback
	}

	log.Printf("Using Nomad-assigned %s port: %d", label, port)
	return port
}

// Loader handles loading configuration from TOML files and environment variables.
type Loader struct {
	configPath string
}

// NewLoader creates a new config loader for the specified TOML file path.
func NewLoader(configPath string) *Loader {
	return &Loader{
		configPath: configPath,
	}
}

// Load reads the TOML configuration file and unmarshals it into the provided config struct.
// It then applies environment variable overrides for any fields with an `env` tag.
// The config parameter must be a pointer to a struct.
func (l *Loader) Load(config interface{}) error {
	if config == nil {
		return fmt.Errorf("config cannot be nil")
	}

	// Ensure config is a pointer to a struct
	rv := reflect.ValueOf(config)
	if rv.Kind() != reflect.Ptr {
		return fmt.Errorf("config must be a pointer to a struct, got %T", config)
	}
	if rv.Elem().Kind() != reflect.Struct {
		return fmt.Errorf("config must be a pointer to a struct, got pointer to %v", rv.Elem().Kind())
	}

	// Load TOML file
	if _, err := toml.DecodeFile(l.configPath, config); err != nil {
		// Check if file doesn't exist
		if os.IsNotExist(err) {
			// File doesn't exist, continue with zero values and env overrides
		} else {
			return fmt.Errorf("failed to decode TOML file %s: %w", l.configPath, err)
		}
	}

	// Apply environment variable overrides
	if err := l.applyEnvOverrides(config); err != nil {
		return fmt.Errorf("failed to apply environment overrides: %w", err)
	}

	return nil
}

// applyEnvOverrides walks through the config struct using reflection and applies
// environment variable overrides for any field with an `env` tag.
func (l *Loader) applyEnvOverrides(config interface{}) error {
	return applyEnvOverridesRecursive(reflect.ValueOf(config).Elem())
}

// applyEnvOverridesRecursive recursively walks through struct fields and applies env overrides.
func applyEnvOverridesRecursive(v reflect.Value) error {
	t := v.Type()

	for i := 0; i < v.NumField(); i++ {
		field := v.Field(i)
		fieldType := t.Field(i)

		// Skip unexported fields
		if !field.CanSet() {
			continue
		}

		// If the field is a struct, recurse into it
		if field.Kind() == reflect.Struct {
			if err := applyEnvOverridesRecursive(field); err != nil {
				return err
			}
			continue
		}

		// Check for env tag
		envTag := fieldType.Tag.Get("env")
		if envTag == "" {
			continue
		}

		// Get environment variable
		envValue := os.Getenv(envTag)
		if envValue == "" {
			continue
		}

		// Apply the environment variable based on field type
		if err := setFieldFromString(field, envValue, fieldType.Name); err != nil {
			return fmt.Errorf("failed to set field %s from env %s: %w", fieldType.Name, envTag, err)
		}
	}

	return nil
}

// setFieldFromString sets a struct field value from a string based on the field's type.
func setFieldFromString(field reflect.Value, value string, fieldName string) error {
	switch field.Kind() {
	case reflect.String:
		field.SetString(value)
		return nil

	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		intVal, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return fmt.Errorf("cannot parse %q as int for field %s: %w", value, fieldName, err)
		}
		field.SetInt(intVal)
		return nil

	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		uintVal, err := strconv.ParseUint(value, 10, 64)
		if err != nil {
			return fmt.Errorf("cannot parse %q as uint for field %s: %w", value, fieldName, err)
		}
		field.SetUint(uintVal)
		return nil

	case reflect.Bool:
		boolVal, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf("cannot parse %q as bool for field %s: %w", value, fieldName, err)
		}
		field.SetBool(boolVal)
		return nil

	case reflect.Float32, reflect.Float64:
		floatVal, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return fmt.Errorf("cannot parse %q as float for field %s: %w", value, fieldName, err)
		}
		field.SetFloat(floatVal)
		return nil

	default:
		return fmt.Errorf("unsupported field type %v for field %s", field.Kind(), fieldName)
	}
}
