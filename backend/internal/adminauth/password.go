package adminauth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

const (
	passwordPrefix     = "argon2id-v1"
	passwordMemory     = 64 * 1024
	passwordIterations = 3
	passwordThreads    = 2
	passwordKeyLength  = 32
)

var ErrWeakPassword = errors.New("password must be 12 to 1024 bytes")
var ErrWeakEmployeePassword = errors.New("employee password must be 6 to 1024 characters")

func HashPassword(password string) (string, error) {
	if len(password) < 12 || len(password) > 1024 {
		return "", ErrWeakPassword
	}
	return hashPassword(password)
}

// HashEmployeePassword allows employee-selected passwords of six or more
// Unicode characters. Administrator password policy remains separate.
func HashEmployeePassword(password string) (string, error) {
	if utf8.RuneCountInString(password) < 6 || len(password) > 1024 {
		return "", ErrWeakEmployeePassword
	}
	return hashPassword(password)
}

func hashPassword(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, passwordIterations, passwordMemory, passwordThreads, passwordKeyLength)
	return passwordPrefix + "$" + base64.RawStdEncoding.EncodeToString(salt) + "$" + base64.RawStdEncoding.EncodeToString(key), nil
}

func VerifyPassword(password, encoded string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 3 || parts[0] != passwordPrefix {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[1])
	if err != nil || len(salt) != 16 {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[2])
	if err != nil || len(want) != passwordKeyLength {
		return false
	}
	got := argon2.IDKey([]byte(password), salt, passwordIterations, passwordMemory, passwordThreads, passwordKeyLength)
	return subtle.ConstantTimeCompare(got, want) == 1
}

func SpendPasswordCheck(password string) {
	var salt [16]byte
	argon2.IDKey([]byte(password), salt[:], passwordIterations, passwordMemory, passwordThreads, passwordKeyLength)
}
