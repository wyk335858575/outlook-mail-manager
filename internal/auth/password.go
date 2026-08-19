package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

const (
	passwordAlgorithm = "argon2id-v19"
	argonMemoryKiB    = 64 * 1024
	argonIterations   = 3
	argonParallelism  = 2
	argonSaltBytes    = 16
	argonHashBytes    = 32
)

var (
	ErrPasswordPolicy = errors.New("password must contain at least 12 characters and at most 1024 bytes")
	ErrUsernamePolicy = errors.New("username must contain 3 to 64 letters, numbers, or . _ - @")
)

func ValidateUsername(username string) error {
	length := utf8.RuneCountInString(username)
	if !utf8.ValidString(username) || length < 3 || length > 64 {
		return ErrUsernamePolicy
	}
	for _, value := range username {
		if unicode.IsLetter(value) || unicode.IsDigit(value) || strings.ContainsRune("._-@", value) {
			continue
		}
		return ErrUsernamePolicy
	}
	return nil
}

func ValidatePassword(password string) error {
	if !utf8.ValidString(password) || utf8.RuneCountInString(password) < 12 || len(password) > 1024 {
		return ErrPasswordPolicy
	}
	return nil
}

func HashPassword(password string, random io.Reader) (string, error) {
	if err := ValidatePassword(password); err != nil {
		return "", err
	}
	if random == nil {
		random = rand.Reader
	}
	salt := make([]byte, argonSaltBytes)
	if _, err := io.ReadFull(random, salt); err != nil {
		return "", fmt.Errorf("generate password salt: %w", err)
	}
	hash := argon2.IDKey([]byte(password), salt, argonIterations, argonMemoryKiB, argonParallelism, argonHashBytes)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version,
		argonMemoryKiB,
		argonIterations,
		argonParallelism,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(hash),
	), nil
}

func VerifyPassword(encoded, password string) (bool, error) {
	if len(password) > 1024 {
		return false, nil
	}
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, errors.New("password hash has an unsupported format")
	}

	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false, errors.New("password hash has an unsupported version")
	}

	var memory uint32
	var iterations uint32
	var parallelism uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &iterations, &parallelism); err != nil {
		return false, errors.New("password hash has invalid parameters")
	}
	if memory < 8*1024 || memory > 256*1024 || iterations < 1 || iterations > 10 || parallelism < 1 || parallelism > 8 {
		return false, errors.New("password hash parameters are outside safe limits")
	}

	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(salt) < 8 || len(salt) > 64 {
		return false, errors.New("password hash has an invalid salt")
	}
	expected, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(expected) < 16 || len(expected) > 64 {
		return false, errors.New("password hash has an invalid digest")
	}

	actual := argon2.IDKey([]byte(password), salt, iterations, memory, parallelism, uint32(len(expected)))
	return subtle.ConstantTimeCompare(actual, expected) == 1, nil
}
