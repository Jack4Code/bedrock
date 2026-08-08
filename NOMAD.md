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
    grpcPort := cfg.Bedrock.GetGRPCPort()       // Checks NOMAD_PORT_grpc first

    // Start your app with resolved ports
    bedrock.Run(yourApp, cfg.Bedrock)
}
```

### Nomad Job Specification

Configure your Nomad job with dynamic port allocation using the labels `http`, `health`, `metrics`, and — for a service using [bedrock/grpc](GRPC.md) — `grpc`:

```hcl
job "bedrock-app" {
  datacenters = ["dc1"]

  group "app" {
    network {
      # Dynamic port allocation
      port "http" {}      # Exposed as NOMAD_PORT_http
      port "health" {}    # Exposed as NOMAD_PORT_health
      port "metrics" {}   # Exposed as NOMAD_PORT_metrics
      port "grpc" {}      # Exposed as NOMAD_PORT_grpc (only if you serve gRPC)
    }

    task "server" {
      driver = "docker"

      config {
        image = "your-bedrock-app:latest"
        ports = ["http", "health", "metrics", "grpc"]
      }

      # Optional: Set static config values as fallback
      env {
        HTTP_PORT = "8080"
        HEALTH_PORT = "8081"
        METRICS_PORT = "8082"
        GRPC_PORT = "9000"
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

#### `GetGRPCPort() int`

Returns the gRPC port to use, checking `NOMAD_PORT_grpc` first. Falls back to `GRPCPort` config value if Nomad variable is not set or invalid. Pass it to `bgrpc.Config.Port` — see [GRPC.md](GRPC.md).

## Best Practices

1. **Always use getter methods**: Use `GetHTTPPort()`, `GetHealthPort()`, `GetMetricsPort()` and `GetGRPCPort()` instead of accessing fields directly
2. **Use standard port labels**: Use `http`, `health`, `metrics` and `grpc` as your Nomad port labels
3. **Provide fallback config**: Include default ports in your TOML or environment for local development
4. **Test locally**: Use `NOMAD_PORT_*` environment variables to test Nomad behavior without deploying
5. **Monitor logs**: Watch for port resolution warnings in your application logs

## Split Deployments (Visibility-Scoped Runtimes)

By default a single bedrock process registers **every** route, partitioned onto
hostnames by `Visibility` (see `HostConfig`). The reverse proxy + `Host` header
decide what is reachable from where.

You can instead deploy the **same image** as several Nomad task groups, where
each group serves only a subset of visibilities. A public-facing group serving
`{Public, Gated}` simply doesn't register your `Private` routes — they're absent
from that process, not merely host-gated. This buys you:

- **Stronger isolation** — there's no Host-spoof or proxy-misconfig path to a
  route the process never wired up.
- **Independent scaling** — scale the public fleet without scaling admin/private.
- **Independent blast radius** — deploying public routes doesn't bounce private.
- **Credential separation** — the private group can hold creds the public can't.

### Selecting the surface: `BEDROCK_SERVE`

Set `BEDROCK_SERVE` to a comma-separated list of visibility names
(`public`, `private`, `gated`; case-insensitive). When unset, the process serves
everything (the original behavior). An invalid name fails startup rather than
silently serving everything.

In code this maps to `Options.Serve []Visibility`; the env var wins when both are
set, so you parameterise per task group without code changes.

### Running jobs once: `BEDROCK_RUN_JOBS`

`OnStart` and your `JobsProvider` run in **every** process. If you split into
three groups, scheduled jobs would fire 3×. Set `BEDROCK_RUN_JOBS=false` on all
but one group so cron runs once. Defaults to `true`. In code: `Options.RunJobs
*bool`; the env var wins when set.

### Example: public group + internal group

```hcl
job "bedrock-app" {
  datacenters = ["dc1"]

  # Public-facing surface: Public + Gated routes. Does not run jobs.
  group "public" {
    count = 3
    network { port "http" {}  port "health" {} }
    task "server" {
      driver = "docker"
      config { image = "your-bedrock-app:latest"  ports = ["http", "health"] }
      env {
        BEDROCK_SERVE    = "public,gated"
        BEDROCK_RUN_JOBS = "false"
      }
      service { name = "bedrock-app-public"  port = "http" }
    }
  }

  # Internal surface: Private routes only. Runs the scheduled jobs.
  group "internal" {
    count = 1
    network { port "http" {}  port "health" {} }
    task "server" {
      driver = "docker"
      config { image = "your-bedrock-app:latest"  ports = ["http", "health"] }
      env {
        BEDROCK_SERVE    = "private"
        BEDROCK_RUN_JOBS = "true"
      }
      service { name = "bedrock-app-internal"  port = "http" }
    }
  }
}
```

### Caveat: Public and Gated sharing a hostname

`Public` and `Gated` typically share the public hostname; the difference is the
edge allowlist, not the `Host`. If you split them into **different** task groups,
your reverse proxy can no longer tell them apart by `Host` alone — it must route
by path or allowlist to reach the right group. Splitting along the host boundary
(`{Public, Gated}` vs `{Private}`) avoids this entirely and is the common case.
Arbitrary subsets are supported; this is a proxy-topology constraint, not a
bedrock one.

## Troubleshooting

### Ports not being detected

Check that:
- Environment variables are named exactly `NOMAD_PORT_http`, `NOMAD_PORT_health`, `NOMAD_PORT_metrics`, `NOMAD_PORT_grpc` (case-sensitive)
- Port labels in your Nomad job match: `http`, `health`, `metrics`, `grpc`
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
