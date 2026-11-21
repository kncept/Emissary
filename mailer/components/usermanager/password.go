package usermanager

import (
	"crypto/pbkdf2"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/bcrypt"
)

const (
	// Password hash scheme constants (legacy, kept for migration documentation)
	passwordHashScheme     = "pbkdf2-sha256"
	passwordHashIterations = 210_000
	passwordSaltBytes      = 16
	passwordKeyBytes       = 32

	// bcrypt settings (default for new passwords)
	bcryptCost = 12

	// argon2 settings
	argon2Time    = 1
	argon2Memory  = 64 * 1024
	argon2Threads = 4
	argon2KeyLen  = 32
)

// bcryptHashBytes is the bcrypt primitive shared with the built-in
// administrator verifier. It is deliberately not tied to a user record salt
// prefix: callers decide how the hash is labelled.
func bcryptHashBytes(password string) ([]byte, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcryptCost)
	if err != nil {
		return nil, fmt.Errorf("bcrypt hash: %w", err)
	}
	return hash, nil
}

// verifyBcryptHash compares a password with a bcrypt hash produced by
// bcryptHashBytes (or stored by an older version).
func verifyBcryptHash(hash []byte, password string) bool {
	return bcrypt.CompareHashAndPassword(hash, []byte(password)) == nil
}

// adminPasswordDigest pre-hashes a configured or supplied password before
// bcrypt so a long configuration secret is not silently truncated at bcrypt's
// 72-byte input limit.
func adminPasswordDigest(password string) string {
	digest := sha256.Sum256([]byte(password))
	return base64.RawStdEncoding.EncodeToString(digest[:])
}

// hashPassword generates a new password hash using bcrypt (default).
// Returns the salt (with b- prefix) and the hash, or an error.
func hashPassword(password string) (salt, hash string, err error) {
	hashBytes, err := bcryptHashBytes(password)
	if err != nil {
		return "", "", err
	}
	return "b-", string(hashBytes), nil
}

// checkPassword verifies a password against the stored hash using the
// salt prefix to determine the algorithm. Accounts created without a
// password have an empty verifier and can never authenticate, including
// with an empty password.
func checkPassword(salt, stored, password string) bool {
	if password == "" || stored == "" {
		return false
	}
	switch {
	case salt == "":
		// Plaintext comparison (insecure, for migration/testing only)
		return subtle.ConstantTimeCompare([]byte(stored), []byte(password)) == 1

	case strings.HasPrefix(salt, "b-"):
		// bcrypt
		return verifyBcryptHash([]byte(stored), password)

	case strings.HasPrefix(salt, "a2-"):
		// argon2id
		return verifyArgon2(salt, stored, password)

	case strings.HasPrefix(salt, "p2-"):
		// PBKDF2-SHA256 (legacy format)
		return checkPBKDF2(salt, stored, password)

	default:
		// Unknown prefix - treat as invalid
		return false
	}
}

// verifyArgon2 verifies an argon2id hash.
// The salt format is: a2-$time$memory$threads$keyLen$saltBase64
// The stored hash is the base64-encoded argon2 output.
func verifyArgon2(salt, stored, password string) bool {
	if !strings.HasPrefix(salt, "a2-") {
		return false
	}
	parts := strings.Split(salt[3:], "$")
	if len(parts) != 5 {
		return false
	}
	time, err := strconv.ParseUint(parts[0], 10, 32)
	if err != nil {
		return false
	}
	memory, err := strconv.ParseUint(parts[1], 10, 32)
	if err != nil {
		return false
	}
	threads, err := strconv.ParseUint(parts[2], 10, 8)
	if err != nil {
		return false
	}
	keyLen, err := strconv.ParseUint(parts[3], 10, 32)
	if err != nil {
		return false
	}
	saltBytes, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false
	}

	expected, err := base64.RawStdEncoding.DecodeString(stored)
	if err != nil {
		return false
	}

	actual := argon2.IDKey([]byte(password), saltBytes, uint32(time), uint32(memory), uint8(threads), uint32(keyLen))
	return subtle.ConstantTimeCompare(actual, expected) == 1
}

// checkPBKDF2 verifies a legacy PBKDF2-SHA256 hash.
// The salt format is: p2-$iterations$saltBase64
// The stored hash is the base64-encoded PBKDF2 output.
func checkPBKDF2(salt, stored, password string) bool {
	if !strings.HasPrefix(salt, "p2-") {
		return false
	}
	parts := strings.Split(salt[3:], "$")
	if len(parts) != 2 {
		return false
	}
	iterations, err := strconv.Atoi(parts[0])
	if err != nil || iterations <= 0 {
		return false
	}
	saltBytes, err := base64.RawStdEncoding.DecodeString(parts[1])
	if err != nil {
		return false
	}

	expected, err := base64.RawStdEncoding.DecodeString(stored)
	if err != nil {
		return false
	}

	actual, err := pbkdf2.Key(sha256.New, password, saltBytes, iterations, len(expected))
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(actual, expected) == 1
}
