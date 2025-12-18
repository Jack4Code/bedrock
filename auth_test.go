package bedrock

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// Test JWT Generation and Validation
func TestJWTGeneration(t *testing.T) {
	secret := "test-secret-key"
	userID := "user123"
	expiration := 1 * time.Hour

	// Generate token
	token, err := GenerateJWT(userID, secret, expiration)
	if err != nil {
		t.Fatalf("Failed to generate JWT: %v", err)
	}

	if token == "" {
		t.Fatal("Generated token is empty")
	}

	// Validate token
	extractedUserID, err := ValidateJWT(token, secret)
	if err != nil {
		t.Fatalf("Failed to validate JWT: %v", err)
	}

	if extractedUserID != userID {
		t.Errorf("Expected user ID %s, got %s", userID, extractedUserID)
	}
}

func TestJWTValidation_InvalidSecret(t *testing.T) {
	secret := "test-secret-key"
	userID := "user123"
	expiration := 1 * time.Hour

	token, _ := GenerateJWT(userID, secret, expiration)

	// Try to validate with wrong secret
	_, err := ValidateJWT(token, "wrong-secret")
	if err == nil {
		t.Error("Should fail with wrong secret")
	}
}

func TestJWTValidation_ExpiredToken(t *testing.T) {
	secret := "test-secret-key"
	userID := "user123"
	expiration := -1 * time.Hour // Expired 1 hour ago

	token, _ := GenerateJWT(userID, secret, expiration)

	// Should fail because token is expired
	_, err := ValidateJWT(token, secret)
	if err == nil {
		t.Error("Should fail with expired token")
	}
}

func TestJWTValidation_MalformedToken(t *testing.T) {
	secret := "test-secret-key"

	// Try to validate garbage token
	_, err := ValidateJWT("this.is.not.a.jwt", secret)
	if err == nil {
		t.Error("Should fail with malformed token")
	}
}

// Test Password Hashing
func TestPasswordHashing(t *testing.T) {
	password := "mySecurePassword123!"

	// Hash password
	hash, err := HashPassword(password)
	if err != nil {
		t.Fatalf("Failed to hash password: %v", err)
	}

	if hash == "" {
		t.Fatal("Generated hash is empty")
	}

	if hash == password {
		t.Error("Hash should not be the same as password")
	}
}

func TestPasswordCheck_ValidPassword(t *testing.T) {
	password := "mySecurePassword123!"
	hash, _ := HashPassword(password)

	// Should succeed with correct password
	err := CheckPassword(password, hash)
	if err != nil {
		t.Error("Valid password should pass check")
	}
}

func TestPasswordCheck_InvalidPassword(t *testing.T) {
	password := "mySecurePassword123!"
	hash, _ := HashPassword(password)

	// Should fail with wrong password
	err := CheckPassword("wrongPassword", hash)
	if err == nil {
		t.Error("Invalid password should fail check")
	}
}

func TestPasswordCheck_DifferentHashEachTime(t *testing.T) {
	password := "mySecurePassword123!"

	// Generate two hashes of the same password
	hash1, _ := HashPassword(password)
	hash2, _ := HashPassword(password)

	// Hashes should be different (bcrypt includes random salt)
	if hash1 == hash2 {
		t.Error("Each hash should be unique due to random salt")
	}

	// But both should validate correctly
	if err := CheckPassword(password, hash1); err != nil {
		t.Error("First hash should validate")
	}
	if err := CheckPassword(password, hash2); err != nil {
		t.Error("Second hash should validate")
	}
}

// Test Context Helpers
func TestContextHelpers(t *testing.T) {
	ctx := context.Background()
	userID := "user123"

	// Add user ID to context
	ctx = WithUserID(ctx, userID)

	// Extract user ID from context
	extractedUserID, ok := GetUserID(ctx)
	if !ok {
		t.Fatal("Failed to extract user ID from context")
	}

	if extractedUserID != userID {
		t.Errorf("Expected user ID %s, got %s", userID, extractedUserID)
	}
}

func TestContextHelpers_NotFound(t *testing.T) {
	ctx := context.Background()

	// Try to get user ID from empty context
	_, ok := GetUserID(ctx)
	if ok {
		t.Error("Should not find user ID in empty context")
	}
}

// Test RequireAuth Middleware
func TestRequireAuth_ValidToken(t *testing.T) {
	secret := "test-secret"
	userID := "user123"

	// Generate valid token
	token, _ := GenerateJWT(userID, secret, 1*time.Hour)

	// Create middleware
	authMiddleware := RequireAuth(secret)

	// Create a test handler that checks for user ID in context
	testHandler := func(ctx context.Context, r *http.Request) Response {
		extractedUserID, ok := GetUserID(ctx)
		if !ok {
			return JSON(500, map[string]string{"error": "user ID not found"})
		}
		return JSON(200, map[string]string{"userID": extractedUserID})
	}

	// Wrap handler with middleware
	wrappedHandler := authMiddleware(testHandler)

	// Create request with valid token
	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("Authorization", "Bearer "+token)

	// Call handler
	response := wrappedHandler(context.Background(), req)

	// Verify it's a JSONResponse with status 200
	jsonResp, ok := response.(JSONResponse)
	if !ok {
		t.Fatal("Expected JSONResponse")
	}

	if jsonResp.StatusCode != 200 {
		t.Errorf("Expected status 200, got %d", jsonResp.StatusCode)
	}
}

func TestRequireAuth_MissingToken(t *testing.T) {
	secret := "test-secret"
	authMiddleware := RequireAuth(secret)

	testHandler := func(ctx context.Context, r *http.Request) Response {
		return JSON(200, map[string]string{"status": "ok"})
	}

	wrappedHandler := authMiddleware(testHandler)

	// Create request without Authorization header
	req := httptest.NewRequest("GET", "/test", nil)

	response := wrappedHandler(context.Background(), req)

	// Should fail with 401
	jsonResp, ok := response.(JSONResponse)
	if !ok {
		t.Fatal("Expected JSONResponse")
	}

	if jsonResp.StatusCode != 401 {
		t.Errorf("Expected status 401, got %d", jsonResp.StatusCode)
	}
}

func TestRequireAuth_InvalidFormat(t *testing.T) {
	secret := "test-secret"
	authMiddleware := RequireAuth(secret)

	testHandler := func(ctx context.Context, r *http.Request) Response {
		return JSON(200, map[string]string{"status": "ok"})
	}

	wrappedHandler := authMiddleware(testHandler)

	// Create request with invalid format (missing "Bearer")
	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("Authorization", "some-token")

	response := wrappedHandler(context.Background(), req)

	// Should fail with 401
	jsonResp, ok := response.(JSONResponse)
	if !ok {
		t.Fatal("Expected JSONResponse")
	}

	if jsonResp.StatusCode != 401 {
		t.Errorf("Expected status 401, got %d", jsonResp.StatusCode)
	}
}

func TestRequireAuth_InvalidToken(t *testing.T) {
	secret := "test-secret"
	authMiddleware := RequireAuth(secret)

	testHandler := func(ctx context.Context, r *http.Request) Response {
		return JSON(200, map[string]string{"status": "ok"})
	}

	wrappedHandler := authMiddleware(testHandler)

	// Create request with invalid token
	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("Authorization", "Bearer invalid.token.here")

	response := wrappedHandler(context.Background(), req)

	// Should fail with 401
	jsonResp, ok := response.(JSONResponse)
	if !ok {
		t.Fatal("Expected JSONResponse")
	}

	if jsonResp.StatusCode != 401 {
		t.Errorf("Expected status 401, got %d", jsonResp.StatusCode)
	}
}

func TestRequireAuth_WrongSecret(t *testing.T) {
	correctSecret := "correct-secret"
	wrongSecret := "wrong-secret"
	userID := "user123"

	// Generate token with correct secret
	token, _ := GenerateJWT(userID, correctSecret, 1*time.Hour)

	// Create middleware with wrong secret
	authMiddleware := RequireAuth(wrongSecret)

	testHandler := func(ctx context.Context, r *http.Request) Response {
		return JSON(200, map[string]string{"status": "ok"})
	}

	wrappedHandler := authMiddleware(testHandler)

	// Create request with token signed by different secret
	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("Authorization", "Bearer "+token)

	response := wrappedHandler(context.Background(), req)

	// Should fail with 401
	jsonResp, ok := response.(JSONResponse)
	if !ok {
		t.Fatal("Expected JSONResponse")
	}

	if jsonResp.StatusCode != 401 {
		t.Errorf("Expected status 401, got %d", jsonResp.StatusCode)
	}
}

// Test Middleware Chaining
func TestMiddlewareChain(t *testing.T) {
	executionOrder := []string{}

	// Create three middleware that track execution order
	middleware1 := func(next Handler) Handler {
		return func(ctx context.Context, r *http.Request) Response {
			executionOrder = append(executionOrder, "middleware1")
			return next(ctx, r)
		}
	}

	middleware2 := func(next Handler) Handler {
		return func(ctx context.Context, r *http.Request) Response {
			executionOrder = append(executionOrder, "middleware2")
			return next(ctx, r)
		}
	}

	middleware3 := func(next Handler) Handler {
		return func(ctx context.Context, r *http.Request) Response {
			executionOrder = append(executionOrder, "middleware3")
			return next(ctx, r)
		}
	}

	// Final handler
	handler := func(ctx context.Context, r *http.Request) Response {
		executionOrder = append(executionOrder, "handler")
		return JSON(200, map[string]string{"status": "ok"})
	}

	// Chain them
	chained := Chain(handler, middleware1, middleware2, middleware3)

	// Execute
	req := httptest.NewRequest("GET", "/test", nil)
	chained(context.Background(), req)

	// Check execution order
	expected := []string{"middleware1", "middleware2", "middleware3", "handler"}
	if len(executionOrder) != len(expected) {
		t.Fatalf("Expected %d executions, got %d", len(expected), len(executionOrder))
	}

	for i, step := range expected {
		if executionOrder[i] != step {
			t.Errorf("Step %d: expected %s, got %s", i, step, executionOrder[i])
		}
	}
}
