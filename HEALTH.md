# Health Endpoint Handling

Bedrock provides built-in health check endpoints for monitoring application health and readiness. The framework supports two deployment modes: merged server mode (health endpoints on the same port as your application) and separate server mode (health endpoints on a dedicated port).

## Overview

Health checks are critical for orchestration platforms like Kubernetes, Nomad, Fly.io, Railway, and Render. Bedrock provides three standard health endpoints:

- `/health` - Liveness check: is the application alive and running?
- `/ready` - Readiness check: is the application ready to serve traffic?
- `/live` - Alias for `/health` (provided for compatibility)

## Deployment Modes

### Merged Server Mode (Same Port)

When `HTTPPort == HealthPort` in your configuration, Bedrock merges health endpoints into the main HTTP server. This is ideal for:

- **Platform-as-a-Service deployments** (Fly.io, Railway, Render)
- **Single-port constraints** (some cloud platforms only expose one port)
- **Simplified deployments** (fewer ports to manage)
- **Development environments** (easier local testing)

```go
config := config.BaseConfig{
    HTTPPort:   8080,
    HealthPort: 8080,  // Same port = merged mode
}

bedrock.Run(app, config)
```

**Output:**
```
Health endpoints will be merged into main server on port 8080
Health endpoints (/health, /ready, /live) registered on main router
Starting server on :8080
```

In merged mode:
- Health endpoints are registered BEFORE application routes
- Health endpoints do NOT have application middleware applied
- Health endpoints are infrastructure-level (no CORS restrictions needed)
- The main server serves both health checks and application routes

### Separate Server Mode (Different Ports)

When `HTTPPort != HealthPort`, Bedrock runs a dedicated health server on a separate port. This is ideal for:

- **Kubernetes deployments** (separate liveness/readiness probes)
- **Nomad deployments** (dedicated health check port)
- **Security-sensitive environments** (isolate health checks from application)
- **High-traffic applications** (health checks don't compete with app traffic)

```go
config := config.BaseConfig{
    HTTPPort:   8080,
    HealthPort: 8081,  // Different ports = separate mode
}

bedrock.Run(app, config)
```

**Output:**
```
Starting health server on :8081
Starting server on :8080
```

In separate mode:
- Health server starts BEFORE `app.OnStart()` is called
- Main application server starts AFTER `app.OnStart()` succeeds
- Each server can be monitored independently

## Health Status Lifecycle

Bedrock manages health status automatically based on application lifecycle:

```
Application Start
    ├─ Health server/endpoints start
    ├─ healthy = false, ready = false
    │
    ├─ app.OnStart() called
    │   └─ If successful: healthy = true
    │   └─ If failed: shutdown and return error
    │
    ├─ Main HTTP server starts (if routes exist)
    │   └─ Server running: ready = true
    │
    └─ Application running (healthy=true, ready=true)

Graceful Shutdown
    ├─ SIGTERM/SIGINT received
    ├─ ready = false (stop accepting new traffic)
    ├─ Servers shutdown (30s timeout)
    └─ app.OnStop() called
```

## API Reference

### Health Endpoints

#### `GET /health`

**Liveness Check** - Indicates whether the application is alive.

**Success Response (200 OK):**
```json
{
  "status": "healthy"
}
```

**Failure Response (503 Service Unavailable):**
```json
{
  "status": "unhealthy"
}
```

Returns healthy if:
- `app.OnStart()` completed successfully
- Application is still running

#### `GET /ready`

**Readiness Check** - Indicates whether the application is ready to serve traffic.

**Success Response (200 OK):**
```json
{
  "status": "ready"
}
```

**Failure Response (503 Service Unavailable):**
```json
{
  "status": "not ready"
}
```

Returns ready if:
- Application is healthy (OnStart succeeded)
- HTTP server is running and accepting connections
- Graceful shutdown has not started

#### `GET /live`

**Alias for /health** - Provided for Kubernetes compatibility.

Same behavior as `/health`.

## Reserved Endpoint Paths

When using merged server mode, the following paths are reserved and cannot be used by your application:

- `/health`
- `/ready`
- `/live`

If your application tries to register a route on these paths in merged mode, Bedrock will return an error during startup:

```
route conflict: application route /health conflicts with reserved health endpoint /health
```

In separate server mode, these paths are available for your application routes since health endpoints run on a different port.

## Platform-Specific Examples

### Fly.io

Fly.io expects health checks on the same port as your application:

```toml
# config.toml
[bedrock]
http_port = 8080
health_port = 8080  # Same port for Fly.io

# fly.toml
[http_service]
  internal_port = 8080

  [[http_service.checks]]
    grace_period = "5s"
    interval = "10s"
    method = "GET"
    timeout = "2s"
    path = "/health"
```

### Railway

Railway also expects health checks on the main port:

```toml
# config.toml
[bedrock]
http_port = 8080
health_port = 8080  # Same port for Railway
```

Railway will automatically detect the `/health` endpoint.

### Render

Render supports health checks on the application port:

```toml
# config.toml
[bedrock]
http_port = 8080
health_port = 8080  # Same port for Render
```

Configure in your `render.yaml`:
```yaml
services:
  - type: web
    name: bedrock-app
    env: docker
    healthCheckPath: /health
```

### Kubernetes

Kubernetes works well with separate ports for isolation:

```toml
# config.toml
[bedrock]
http_port = 8080
health_port = 8081  # Separate port for K8s
```

```yaml
# deployment.yaml
apiVersion: v1
kind: Pod
spec:
  containers:
  - name: bedrock-app
    ports:
    - containerPort: 8080
      name: http
    - containerPort: 8081
      name: health

    livenessProbe:
      httpGet:
        path: /health
        port: 8081
      initialDelaySeconds: 5
      periodSeconds: 10

    readinessProbe:
      httpGet:
        path: /ready
        port: 8081
      initialDelaySeconds: 5
      periodSeconds: 5
```

Alternatively, use the same port:

```toml
# config.toml
[bedrock]
http_port = 8080
health_port = 8080  # Same port also works
```

```yaml
# deployment.yaml
livenessProbe:
  httpGet:
    path: /health
    port: 8080  # Use main application port
```

### Nomad

Nomad supports both modes. With dynamic ports:

```toml
# config.toml
[bedrock]
http_port = 8080
health_port = 8081  # Different static ports
```

```hcl
# job.nomad
job "bedrock-app" {
  group "app" {
    network {
      port "http" {}      # Dynamic port for app
      port "health" {}    # Dynamic port for health
    }

    task "server" {
      service {
        name = "bedrock-app"
        port = "http"

        check {
          name     = "health"
          type     = "http"
          port     = "health"
          path     = "/health"
          interval = "10s"
        }

        check {
          name     = "ready"
          type     = "http"
          port     = "health"
          path     = "/ready"
          interval = "5s"
        }
      }
    }
  }
}
```

See [NOMAD.md](NOMAD.md) for details on Nomad dynamic port support.

## Background Mode (No HTTP Routes)

If your application has no HTTP routes (background worker, cron job, etc.), Bedrock still provides health endpoints:

**Separate Server Mode:**
```go
// No routes, different ports
config := config.BaseConfig{
    HTTPPort:   8080,
    HealthPort: 8081,
}

// Health server runs on 8081
// No main HTTP server starts
```

**Merged Server Mode:**
```go
// No routes, same port
config := config.BaseConfig{
    HTTPPort:   8080,
    HealthPort: 8080,
}

// A minimal server starts on 8080 serving ONLY health endpoints
```

This ensures orchestration platforms can still monitor background applications.

## Best Practices

### 1. Choose the Right Mode

**Use Merged Mode (same port) when:**
- Deploying to PaaS platforms (Fly.io, Railway, Render)
- You only have one port available
- Simplicity is preferred
- You're doing local development

**Use Separate Mode (different ports) when:**
- Deploying to Kubernetes or Nomad
- You want health checks isolated from application traffic
- Security policies require separation
- High traffic requires dedicated health check capacity

### 2. Configure Based on Environment

Use environment variables to change modes per environment:

```toml
# config.toml (development - merged mode)
[bedrock]
http_port = 8080
health_port = 8080
```

```bash
# Production (separate mode)
export HTTP_PORT=8080
export HEALTH_PORT=8081
./app
```

### 3. Don't Use Reserved Paths

Avoid using `/health`, `/ready`, or `/live` in your application routes if you plan to use merged mode.

### 4. Monitor Both Endpoints

Even though `/health` and `/ready` are similar, use them correctly:
- `/health` for liveness (should the pod be restarted?)
- `/ready` for readiness (should traffic be routed here?)

### 5. Test Locally

Test health checks locally:

```bash
# Start your app
./app

# Test health endpoint
curl http://localhost:8080/health

# Test ready endpoint
curl http://localhost:8080/ready
```

## Troubleshooting

### Health checks failing during startup

**Symptom:** `/health` returns 503 during app initialization

**Cause:** Health check is called before `app.OnStart()` completes

**Solution:** This is expected. The application will return healthy once OnStart succeeds. Configure your orchestrator with appropriate `initialDelaySeconds` or grace periods.

### Route conflict error

**Symptom:**
```
route conflict: application route /health conflicts with reserved health endpoint /health
```

**Cause:** Your application is trying to register `/health`, `/ready`, or `/live` in merged mode

**Solution:**
- Rename your application route
- Or switch to separate server mode (use different ports)

### Health endpoints getting CORS errors

**Symptom:** Browser-based health checks fail with CORS errors

**Cause:** Health endpoints are infrastructure endpoints, not meant for browser access

**Solution:** Health checks should be called by orchestration platforms (Kubernetes, Nomad, load balancers), not browsers. If you need browser access, create separate application-level status endpoints.

### Wrong port mode being used

**Symptom:** Seeing separate servers when you expected merged mode (or vice versa)

**Cause:** Port configuration mismatch

**Solution:** Verify your configuration:
```go
// Check what ports are configured
fmt.Printf("HTTP Port: %d\n", config.HTTPPort)
fmt.Printf("Health Port: %d\n", config.HealthPort)
```

Remember that environment variables override TOML config (see [config/README.md](config/README.md)).

## Migration from Separate to Merged Mode

If you're migrating an existing Bedrock app from separate mode to merged mode:

**Before (separate mode):**
```toml
[bedrock]
http_port = 8080
health_port = 8081
```

**After (merged mode):**
```toml
[bedrock]
http_port = 8080
health_port = 8080  # Change this
```

**Code changes:** None required! Bedrock handles the transition automatically.

**Infrastructure changes:**
- Update health check configuration to point to port 8080 instead of 8081
- Remove health port from firewall rules / security groups
- Update any monitoring that references the old health port

## Performance Considerations

### Merged Mode Performance

- Health endpoints share resources with application traffic
- Under high load, health checks compete with application requests
- Generally not an issue for most applications
- Monitor if you have extremely high traffic (>10k req/s)

### Separate Mode Performance

- Health checks isolated from application traffic
- No performance impact on application
- Requires managing an additional port
- Slightly higher memory usage (separate HTTP server)

For most applications, the performance difference is negligible. Choose based on deployment requirements rather than performance.
