# CORS Support in Bedrock

Bedrock now has built-in CORS support with automatic OPTIONS preflight handling.

## Default Behavior

By default, `bedrock.Run()` uses permissive CORS settings suitable for development:

```go
func main() {
    app := NewMyApp()
    cfg := bedrock.LoadConfig()
    
    // Uses default CORS (allows all origins)
    bedrock.Run(app, cfg)
}
```

Default CORS config:
- **AllowedOrigins**: `["*"]` (all origins)
- **AllowedMethods**: `["GET", "POST", "PUT", "DELETE", "PATCH", "OPTIONS"]`
- **AllowedHeaders**: `["Accept", "Authorization", "Content-Type", "X-CSRF-Token"]`
- **AllowCredentials**: `false`
- **MaxAge**: `300` seconds

## Custom CORS Configuration

For production, you should restrict origins:

```go
func main() {
    app := NewMyApp()
    cfg := bedrock.LoadConfig()
    
    // Custom CORS config
    corsConfig := bedrock.CORSConfig{
        AllowedOrigins:   []string{
            "https://myapp.com",
            "https://app.myapp.com",
        },
        AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE"},
        AllowedHeaders:   []string{"Authorization", "Content-Type"},
        ExposedHeaders:   []string{"X-Total-Count"},
        AllowCredentials: true,  // Allow cookies
        MaxAge:           3600,  // Cache preflight for 1 hour
    }
    
    bedrock.RunWithCORS(app, cfg, corsConfig)
}
```

## How Preflight Works

When a browser makes a cross-origin DELETE, PUT, or PATCH request, it first sends an OPTIONS preflight request.

**Browser sends:**
```
OPTIONS /api/bookings/123
Origin: https://myapp.com
Access-Control-Request-Method: DELETE
Access-Control-Request-Headers: Authorization
```

**Bedrock responds:**
```
HTTP/1.1 200 OK
Access-Control-Allow-Origin: https://myapp.com
Access-Control-Allow-Methods: GET, POST, PUT, DELETE, PATCH, OPTIONS
Access-Control-Allow-Headers: Accept, Authorization, Content-Type, X-CSRF-Token
Access-Control-Max-Age: 300
```

**Then browser sends actual request:**
```
DELETE /api/bookings/123
Origin: https://myapp.com
Authorization: Bearer eyJhbGc...
```

Bedrock automatically handles both the OPTIONS preflight and the actual request.

## CORS Headers Explained

### Access-Control-Allow-Origin
Specifies which origins can access the resource. Use specific domains in production:
```go
AllowedOrigins: []string{"https://myapp.com"}
```

### Access-Control-Allow-Methods
Which HTTP methods are allowed:
```go
AllowedMethods: []string{"GET", "POST", "PUT", "DELETE"}
```

### Access-Control-Allow-Headers
Which request headers the client can send:
```go
AllowedHeaders: []string{"Authorization", "Content-Type"}
```

### Access-Control-Expose-Headers
Which response headers the client can read:
```go
ExposedHeaders: []string{"X-Total-Count", "X-Page-Number"}
```

### Access-Control-Allow-Credentials
Whether to allow cookies/credentials:
```go
AllowCredentials: true  // Allows cookies, requires specific origin (not "*")
```

### Access-Control-Max-Age
How long the browser can cache the preflight response:
```go
MaxAge: 3600  // 1 hour
```

## Environment-Specific Configuration

Use environment variables to configure CORS per environment:

```go
func getCORSConfig() bedrock.CORSConfig {
    env := os.Getenv("ENV")
    
    if env == "production" {
        return bedrock.CORSConfig{
            AllowedOrigins:   []string{os.Getenv("FRONTEND_URL")},
            AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE"},
            AllowedHeaders:   []string{"Authorization", "Content-Type"},
            AllowCredentials: true,
            MaxAge:           3600,
        }
    }
    
    // Development - permissive
    return bedrock.DefaultCORSConfig()
}

func main() {
    app := NewMyApp()
    cfg := bedrock.LoadConfig()
    corsConfig := getCORSConfig()
    
    bedrock.RunWithCORS(app, cfg, corsConfig)
}
```

## Troubleshooting

### "No 'Access-Control-Allow-Origin' header"
- Check that your origin is in `AllowedOrigins`
- For development, use `AllowedOrigins: []string{"*"}`
- For production with credentials, you MUST specify exact origins (not "*")

### "Method DELETE is not allowed by Access-Control-Allow-Methods"
- Add the method to `AllowedMethods`: `[]string{"GET", "POST", "PUT", "DELETE"}`

### "Request header Authorization is not allowed"
- Add the header to `AllowedHeaders`: `[]string{"Authorization", "Content-Type"}`

### Preflight fails with 404
- This is handled automatically! Bedrock registers OPTIONS handlers for all routes.
- If you still see this, make sure you're using the updated bedrock.go

## Testing CORS

### Test with curl
```bash
# Simulate preflight request
curl -X OPTIONS http://localhost:8080/api/bookings/123 \
  -H "Origin: https://myapp.com" \
  -H "Access-Control-Request-Method: DELETE" \
  -v

# Should see CORS headers in response
```

### Test with JavaScript
```javascript
// This will trigger a preflight request
fetch('http://localhost:8080/api/bookings/123', {
  method: 'DELETE',
  headers: {
    'Authorization': 'Bearer eyJhbGc...',
    'Content-Type': 'application/json'
  }
})
.then(response => response.json())
.then(data => console.log(data))
.catch(error => console.error('CORS error:', error));
```

## Security Best Practices

1. **Never use `AllowedOrigins: ["*"]` with `AllowCredentials: true`**
   - Browsers will reject this
   - Always specify exact origins when using credentials

2. **Use specific origins in production**
   ```go
   // ❌ Bad
   AllowedOrigins: []string{"*"}
   
   // ✅ Good
   AllowedOrigins: []string{"https://app.myapp.com"}
   ```

3. **Only allow necessary headers**
   ```go
   // ❌ Too permissive
   AllowedHeaders: []string{"*"}
   
   // ✅ Specific
   AllowedHeaders: []string{"Authorization", "Content-Type"}
   ```

4. **Set appropriate MaxAge**
   - Too short: More preflight requests (slower)
   - Too long: Changes take time to propagate
   - Recommended: 300-3600 seconds

## Migration from Old Bedrock

If you're upgrading from bedrock without CORS:

1. **No changes needed for basic usage** - `bedrock.Run()` still works
2. **Routes now support Middleware field** - Add auth, logging, etc.
3. **OPTIONS is handled automatically** - No need to register OPTIONS routes
4. **Use `RunWithCORS()` for custom config** - When you need production settings

Your existing code will continue to work with sensible defaults!
