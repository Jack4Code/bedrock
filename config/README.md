# Bedrock Config Package

A flexible TOML-based configuration system with environment variable override support.

## Features

- Load configuration from TOML files using `github.com/BurntSushi/toml`
- Override any config value with environment variables
- Automatic type conversion for common types (string, int, uint, bool, float)
- Reflection-based env var override system
- Embeddable `BaseConfig` for standardized bedrock settings
- Idiomatic Go with proper error handling

## BaseConfig

The `BaseConfig` struct provides bedrock's core configuration needs:

```go
type BaseConfig struct {
    HTTPPort    int    `toml:"http_port" env:"HTTP_PORT"`
    HealthPort  int    `toml:"health_port" env:"HEALTH_PORT"`
    MetricsPort int    `toml:"metrics_port" env:"METRICS_PORT"`
    LogLevel    string `toml:"log_level" env:"LOG_LEVEL"`
    Environment string `toml:"environment" env:"ENVIRONMENT"`
}
```

## Usage

### Basic Usage

Applications that use bedrock should embed `BaseConfig` in their own config struct:

```go
package main

import (
    "fmt"
    "log"

    "github.com/Jack4Code/bedrock/config"
)

type AppConfig struct {
    Bedrock config.BaseConfig `toml:"bedrock"`

    // Your app-specific configuration
    DatabaseURL    string `toml:"database_url" env:"DATABASE_URL"`
    MaxConnections int    `toml:"max_connections" env:"MAX_CONNECTIONS"`
    APIKey         string `toml:"api_key" env:"API_KEY"`
}

func main() {
    // Create a loader for your config file
    loader := config.NewLoader("config.toml")

    // Load configuration
    var cfg AppConfig
    if err := loader.Load(&cfg); err != nil {
        log.Fatalf("Failed to load config: %v", err)
    }

    // Use the configuration
    fmt.Printf("Log Level: %s\n", cfg.Bedrock.LogLevel)
    fmt.Printf("Database: %s\n", cfg.DatabaseURL)
}
```

### TOML File Structure

Your `config.toml` file should structure bedrock config under a `[bedrock]` section:

```toml
# Application-specific configuration (top-level)
database_url = "postgres://localhost:5432/myapp"
max_connections = 50
api_key = "your-api-key"

# Bedrock configuration
[bedrock]
http_port = 8080
health_port = 9090
metrics_port = 9091
log_level = "info"
environment = "production"
```

### Environment Variable Overrides

Environment variables will override TOML values for any field with an `env` tag:

```bash
# Override database URL
export DATABASE_URL="postgres://prod-server:5432/myapp"

# Override bedrock settings
export LOG_LEVEL="debug"
export METRICS_PORT="9999"

# Run your application
./myapp
```

The loader automatically:
1. Loads values from the TOML file
2. Applies environment variable overrides
3. Performs type conversion based on the field type

### Supported Types

The env override system supports these types:
- `string`
- `int`, `int8`, `int16`, `int32`, `int64`
- `uint`, `uint8`, `uint16`, `uint32`, `uint64`
- `bool`
- `float32`, `float64`

### Using BaseConfig Only

You can also use `BaseConfig` directly without embedding:

```go
package main

import (
    "log"

    "github.com/Jack4Code/bedrock/config"
)

func main() {
    loader := config.NewLoader("config.toml")

    var cfg config.BaseConfig
    if err := loader.Load(&cfg); err != nil {
        log.Fatalf("Failed to load config: %v", err)
    }

    // Use bedrock config
    // ...
}
```

With this TOML file:

```toml
http_port = 8080
health_port = 9090
metrics_port = 9091
log_level = "info"
environment = "production"
```

## Error Handling

The loader provides clear error messages for common issues:

```go
loader := config.NewLoader("config.toml")
var cfg AppConfig

if err := loader.Load(&cfg); err != nil {
    // Handle specific errors
    log.Fatalf("Configuration error: %v", err)
}
```

Error cases include:
- Invalid TOML syntax
- Type conversion errors for env vars
- Invalid config parameter (nil, non-pointer, non-struct)

## Design Pattern

The recommended pattern is:

1. Define your app config struct with embedded `BaseConfig`
2. Add `toml` tags for TOML field mapping
3. Add `env` tags for environment variable override capability
4. Load once at application startup
5. Pass config to components that need it

```go
type AppConfig struct {
    Bedrock config.BaseConfig `toml:"bedrock"`

    // Add your fields with both tags
    MyField string `toml:"my_field" env:"MY_FIELD"`
}
```

This design ensures:
- Consistent bedrock configuration across all apps
- Flexibility for app-specific needs
- Easy testing with env var overrides
- Clear separation between file-based and runtime config
