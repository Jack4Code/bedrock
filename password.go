package bedrock

import "golang.org/x/crypto/bcrypt"

// bcryptCost defines the computational cost of the bcrypt algorithm.
// Higher values are more secure but slower. 12 is a good balance for 2024.
const bcryptCost = 12

// HashPassword generates a bcrypt hash of the given password.
// The resulting hash is safe to store in a database.
//
// Parameters:
//   - password: The plaintext password to hash
//
// Returns the hashed password string or an error.
//
// Example:
//
//	hash, err := bedrock.HashPassword("user_password123")
//	if err != nil {
//	    return err
//	}
//	// Store hash in database
func HashPassword(password string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcryptCost)
	if err != nil {
		return "", err
	}
	return string(hash), nil
}

// CheckPassword verifies that a plaintext password matches a bcrypt hash.
// Returns nil if the password is correct, or an error if incorrect.
//
// Parameters:
//   - password: The plaintext password to check
//   - hash: The bcrypt hash to compare against
//
// Returns nil if passwords match, error if they don't match or there's an issue.
//
// Example:
//
//	err := bedrock.CheckPassword("user_password123", storedHash)
//	if err != nil {
//	    return bedrock.JSON(401, map[string]string{"error": "invalid password"})
//	}
//	// Password is correct, proceed with login
func CheckPassword(password, hash string) error {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
}
