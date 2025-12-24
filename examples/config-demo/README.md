# Config Demo Example

This example demonstrates how to use bedrock's config package in your application.

## Running the Demo

### Basic Usage

```bash
cd examples/config-demo
go run main.go
```

This will load `config.toml` from the current directory and display all configuration values.

### With Custom Config File

```bash
go run main.go /path/to/your/config.toml
```

### With Environment Variable Overrides

Try overriding configuration values with environment variables:

```bash
# Override bedrock settings
export LOG_LEVEL="debug"
export METRICS_PORT="9999"

# Override app settings
export DATABASE_URL="postgres://prod-server:5432/proddb"
export MAX_CONNECTIONS="100"

# Run the demo
go run main.go
```

You'll see which values come from the TOML file and which are overridden by environment variables.

## Configuration Structure

The demo uses this config struct:

```go
type AppConfig struct {
    Bedrock config.BaseConfig `toml:"bedrock"`

    DatabaseURL    string `toml:"database_url" env:"DATABASE_URL"`
    MaxConnections int    `toml:"max_connections" env:"MAX_CONNECTIONS"`
    APIKey         string `toml:"api_key" env:"API_KEY"`
    CacheTTL       int    `toml:"cache_ttl" env:"CACHE_TTL"`
}
```

The corresponding TOML file structure is:

```toml
# Top-level fields for application config
database_url = "..."
max_connections = 50

# Bedrock config in its own section
[bedrock]
log_level = "info"
metrics_port = 9090
```

## Key Concepts Demonstrated

1. **Embedding BaseConfig**: Shows how to embed `config.BaseConfig` in your app config
2. **TOML Structure**: Demonstrates proper TOML file organization
3. **Env Overrides**: Shows how environment variables override TOML values
4. **Type Safety**: Demonstrates automatic type conversion (strings, ints)
5. **Error Handling**: Shows how to handle config loading errors

## Testing Environment Overrides

Try these examples:

```bash
# Test integer conversion
export METRICS_PORT="12345"
go run main.go

# Test string override
export LOG_LEVEL="error"
export ENVIRONMENT="production"
go run main.go

# Test multiple overrides
export DATABASE_URL="postgres://new-host/newdb"
export MAX_CONNECTIONS="200"
export CACHE_TTL="7200"
go run main.go
```

## Clean Up

To remove environment variable overrides:

```bash
unset LOG_LEVEL METRICS_PORT HEALTH_PORT ENVIRONMENT
unset DATABASE_URL MAX_CONNECTIONS API_KEY CACHE_TTL
```
