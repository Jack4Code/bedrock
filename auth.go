package bedrock

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// contextKey is a custom type for context keys to avoid collisions
type contextKey string

const userIDKey contextKey = "userID"

// RequireAuth creates middleware that validates JWT tokens from the Authorization header.
// It expects the header format: "Authorization: Bearer <token>"
//
// If the token is valid, the user ID is added to the request context.
// If the token is invalid or missing, it returns a 401 Unauthorized response.
//
// Usage:
//
//	auth := bedrock.RequireAuth("your-secret-key")
//	routes := []bedrock.Route{
//	    {
//	        Method: "GET",
//	        Path: "/api/protected",
//	        Handler: bedrock.Chain(myHandler, auth),
//	    },
//	}
func RequireAuth(secret string) Middleware {
	return func(next Handler) Handler {
		return func(ctx context.Context, r *http.Request) Response {
			// Extract token from Authorization header
			authHeader := r.Header.Get("Authorization")
			if authHeader == "" {
				return JSON(http.StatusUnauthorized, map[string]string{
					"error": "missing authorization header",
				})
			}

			// Expected format: "Bearer <token>"
			parts := strings.Split(authHeader, " ")
			if len(parts) != 2 || parts[0] != "Bearer" {
				return JSON(http.StatusUnauthorized, map[string]string{
					"error": "invalid authorization format",
				})
			}

			token := parts[1]

			// Validate token and extract user ID
			userID, err := ValidateJWT(token, secret)
			if err != nil {
				return JSON(http.StatusUnauthorized, map[string]string{
					"error": "invalid token",
				})
			}

			// Add user ID to context for downstream handlers
			ctx = WithUserID(ctx, userID)

			// Call next handler
			return next(ctx, r)
		}
	}
}

// GenerateJWT creates a signed JWT token for the given user ID.
// The token includes standard claims (subject, issued at, expiration).
//
// Parameters:
//   - userID: The user identifier to embed in the token (stored as "sub" claim)
//   - secret: The secret key used to sign the token
//   - expiration: How long the token should be valid (e.g., 24 * time.Hour)
//
// Returns the signed token string or an error.
//
// Example:
//
//	token, err := bedrock.GenerateJWT("user123", "secret", 24*time.Hour)
func GenerateJWT(userID string, secret string, expiration time.Duration) (string, error) {
	now := time.Now()

	claims := jwt.RegisteredClaims{
		Subject:   userID,
		IssuedAt:  jwt.NewNumericDate(now),
		ExpiresAt: jwt.NewNumericDate(now.Add(expiration)),
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(secret))
}

// ValidateJWT parses and validates a JWT token string.
// It verifies the signature, expiration, and extracts the user ID.
//
// Parameters:
//   - tokenString: The JWT token to validate
//   - secret: The secret key used to verify the signature
//
// Returns the user ID (from "sub" claim) or an error if invalid.
//
// Example:
//
//	userID, err := bedrock.ValidateJWT(token, "secret")
func ValidateJWT(tokenString string, secret string) (string, error) {
	// Parse and validate token
	token, err := jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
		// Verify signing method is HMAC
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, errors.New("invalid signing method")
		}
		return []byte(secret), nil
	})

	if err != nil {
		return "", err
	}

	if !token.Valid {
		return "", errors.New("invalid token")
	}

	// Extract claims
	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return "", errors.New("invalid claims")
	}

	// Extract user ID from "sub" claim
	userID, ok := claims["sub"].(string)
	if !ok {
		return "", errors.New("missing user ID in token")
	}

	return userID, nil
}

// WithUserID adds a user ID to the request context.
// This is typically called by authentication middleware.
//
// Example:
//
//	ctx = bedrock.WithUserID(ctx, "user123")
func WithUserID(ctx context.Context, userID string) context.Context {
	return context.WithValue(ctx, userIDKey, userID)
}

// GetUserID extracts the user ID from the request context.
// Returns the user ID and a boolean indicating if it was found.
//
// This should be called in handlers that are protected by RequireAuth middleware.
//
// Example:
//
//	func MyHandler(ctx context.Context, r *http.Request) bedrock.Response {
//	    userID, ok := bedrock.GetUserID(ctx)
//	    if !ok {
//	        return bedrock.JSON(500, map[string]string{"error": "user not found"})
//	    }
//	    // Use userID...
//	}
func GetUserID(ctx context.Context) (string, bool) {
	userID, ok := ctx.Value(userIDKey).(string)
	return userID, ok
}
