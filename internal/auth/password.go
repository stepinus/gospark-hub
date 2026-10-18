package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/crypto/pbkdf2"
)

var sha256New = sha256.New

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func hexEncode(b []byte) string { return hex.EncodeToString(b) }

const (
	pbkdf2Iterations = 260_000
	hashPrefix       = "pbkdf2_sha256"
	minPasswordLen   = 8
)

var emailRE = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)

// HashPassword returns the upstream pbkdf2_sha256$... format.
func HashPassword(password string) (string, error) {
	salt, err := randomHex(16)
	if err != nil {
		return "", err
	}
	dk := pbkdf2.Key([]byte(password), []byte(salt), pbkdf2Iterations, 32, sha256New)
	return hashPrefix + "$" + strconv.Itoa(pbkdf2Iterations) + "$" + salt + "$" + hexEncode(dk), nil
}

// VerifyPassword checks a password against the stored pbkdf2_sha256 hash.
func VerifyPassword(password, stored string) bool {
	parts := strings.Split(stored, "$")
	if len(parts) != 4 {
		return false
	}
	algo, itersStr, salt, digest := parts[0], parts[1], parts[2], parts[3]
	if algo != hashPrefix {
		return false
	}
	iters, err := strconv.Atoi(itersStr)
	if err != nil {
		return false
	}
	dk := pbkdf2.Key([]byte(password), []byte(salt), iters, 32, sha256New)
	return hmac.Equal([]byte(hexEncode(dk)), []byte(digest))
}

// ValidateEmail returns the trimmed email or an error with the upstream message.
func ValidateEmail(email string) (string, error) {
	email = strings.TrimSpace(email)
	if !emailRE.MatchString(email) {
		return "", errors.New("Enter a valid email address.")
	}
	return email, nil
}

// ValidatePassword returns the password or an error if it is too short.
func ValidatePassword(password string) (string, error) {
	if len(password) < minPasswordLen {
		return "", errors.New("Password must be at least 8 characters.")
	}
	return password, nil
}
