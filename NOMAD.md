# Nomad Dynamic Port Support

Bedrock apps can automatically detect and use Nomad-assigned ports when running in a Nomad allocation, while maintaining backward compatibility for non-Nomad deployments.

## Overview

When Nomad schedules a task with dynamic port allocation, it exposes the assigned ports via environment variables like `NOMAD_PORT_<label>`. Bedrock's config system now automatically detects these environment variables and uses them for port binding, falling back to configured values when not running in Nomad.

## Usage

### In Your Application Code

Instead of accessing port fields directly, use the Nomad-aware getter methods:

```go
package main

import (
    "github.com/Jack4Code/bedrock"
    "github.com/Jack4Code/bedrock/config"
)

type AppConfig struct {
    Bedrock config.BaseConfig `toml:"bedrock"`
    // ... your app-specific config
}

func main() {
    // Load configuration
    loader := config.NewLoader("config.toml")
    var cfg AppConfig
    loader.Load(&cfg)

    // Use Nomad-aware port resolution
    httpPort := cfg.Bedrock.GetHTTPPort()     // Checks NOMAD_PORT_http first
    healthPort := cfg.Bedrock.GetHealthPort() // Checks NOMAD_PORT_health first
    metricsPort := cfg.Bedrock.GetMetricsPort() // Checks NOMAD_PORT_metrics first

    // Start your app with resolved ports
    bedrock.Run(yourApp, cfg.Bedrock)
}
```

### Nomad Job Specification

Configure your Nomad job with dynamic port allocation using the labels `http`, `health`, and `metrics`:

```hcl
job "bedrock-app" {
  datacenters = ["dc1"]

  group "app" {
    network {
      # Dynamic port allocation
      port "http" {}      # Exposed as NOMAD_PORT_http
      port "health" {}    # Exposed as NOMAD_PORT_health
      port "metrics" {}   # Exposed as NOMAD_PORT_metrics
    }

    task "server" {
      driver = "docker"

      config {
        image = "your-bedrock-app:latest"
        ports = ["http", "health", "metrics"]
      }

      # Optional: Set static config values as fallback
      env {
        HTTP_PORT = "8080"
        HEALTH_PORT = "8081"
        METRICS_PORT = "8082"
      }

      # Service registration
      service {
        name = "bedrock-app"
        port = "http"

        check {
          name     = "health"
          type     = "http"
          port     = "health"
          path     = "/health"
          interval = "10s"
          timeout  = "2s"
        }
      }

      service {
        name = "bedrock-app-metrics"
        port = "metrics"
      }
    }
  }
}
```

## Port Resolution Order

Bedrock resolves ports in the following priority order:

1. **Nomad dynamic ports** - `NOMAD_PORT_<label>` environment variables
2. **Environment overrides** - `HTTP_PORT`, `HEALTH_PORT`, `METRICS_PORT` environment variables
3. **TOML configuration** - Values from your `config.toml` file
4. **Zero values** - Default to 0 if nothing is configured

## Backward Compatibility

The Nomad port detection is completely transparent and maintains full backward compatibility:

- **Non-Nomad deployments**: Apps continue to work exactly as before, using TOML or environment variable configuration
- **No code changes required**: Existing apps work without modification
- **Graceful fallback**: If `NOMAD_PORT_*` variables are invalid, Bedrock logs a warning and falls back to configured values
- **Mixed environments**: You can use Nomad ports for some services and static config for others

## Error Handling

If a `NOMAD_PORT_*` environment variable is set but contains an invalid value:

1. Bedrock logs a warning: `Warning: NOMAD_PORT_http is set but invalid ("abc"), falling back to configured port 8080`
2. The application falls back to the configured port value
3. The application continues to run normally

## Examples

### Example 1: Running Locally (No Nomad)

```bash
# Your config.toml
http_port = 8080
health_port = 8081
metrics_port = 8082

# Run the app
./your-bedrock-app

# Output:
# Using config port 8080
```

### Example 2: Running in Nomad

```bash
# Nomad sets these environment variables:
export NOMAD_PORT_http=25432
export NOMAD_PORT_health=27891
export NOMAD_PORT_metrics=29103

# Run the app
./your-bedrock-app

# Output:
# Using Nomad-assigned http port: 25432
# Using Nomad-assigned health port: 27891
# Using Nomad-assigned metrics port: 29103
```

### Example 3: Testing Nomad Behavior Locally

```bash
# Simulate Nomad environment
export NOMAD_PORT_http=12345
export NOMAD_PORT_health=12346
export NOMAD_PORT_metrics=12347

# Run the app
./your-bedrock-app

# The app will use the Nomad ports
```

## Testing

The config package includes comprehensive tests for Nomad port resolution:

```bash
go test ./config/... -v -run TestNomad
```

Test coverage includes:
- Nomad ports taking precedence over config
- Fallback to config when Nomad vars aren't set
- Graceful handling of invalid Nomad port values
- Partial Nomad port configuration
- Interaction between Nomad ports and regular env overrides

## API Reference

### BaseConfig Methods

#### `GetHTTPPort() int`

Returns the HTTP port to use, checking `NOMAD_PORT_http` first. Falls back to `HTTPPort` config value if Nomad variable is not set or invalid.

#### `GetHealthPort() int`

Returns the health port to use, checking `NOMAD_PORT_health` first. Falls back to `HealthPort` config value if Nomad variable is not set or invalid.

#### `GetMetricsPort() int`

Returns the metrics port to use, checking `NOMAD_PORT_metrics` first. Falls back to `MetricsPort` config value if Nomad variable is not set or invalid.

## Best Practices

1. **Always use getter methods**: Use `GetHTTPPort()`, `GetHealthPort()`, and `GetMetricsPort()` instead of accessing fields directly
2. **Use standard port labels**: Use `http`, `health`, and `metrics` as your Nomad port labels
3. **Provide fallback config**: Include default ports in your TOML or environment for local development
4. **Test locally**: Use `NOMAD_PORT_*` environment variables to test Nomad behavior without deploying
5. **Monitor logs**: Watch for port resolution warnings in your application logs

## Troubleshooting

### Ports not being detected

Check that:
- Environment variables are named exactly `NOMAD_PORT_http`, `NOMAD_PORT_health`, `NOMAD_PORT_metrics` (case-sensitive)
- Port labels in your Nomad job match: `http`, `health`, `metrics`
- The environment variables contain valid integer values

### App binding to wrong port

Check the application logs for messages like:
- `Using Nomad-assigned http port: 12345` - Nomad port is being used
- `Warning: NOMAD_PORT_http is set but invalid...` - Falling back to config

Run the config-demo example to see which ports are being resolved:

```bash
cd examples/config-demo
go run main.go
```
