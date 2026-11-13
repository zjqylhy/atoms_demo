// Package auth owns password validation; session and ownership records live in Store.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

var usernamePattern = regexp.MustCompile(`^[a-z0-9_]{3,32}$`)
var hashSlot = make(chan struct{}, 1)

func Username(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if !usernamePattern.MatchString(value) {
		return "", errors.New("用户名需为 3～32 位字母、数字或下划线")
	}
	return value, nil
}

func ValidatePassword(password string) error {
	if !utf8.ValidString(password) || utf8.RuneCountInString(password) < 12 || utf8.RuneCountInString(password) > 128 || len(password) > 512 {
		return errors.New("密码需为 12～128 个字符")
	}
	return nil
}

// Bound the expensive calculation before allocating Argon2's working memory.
func derive(ctx context.Context, password string, salt []byte) ([]byte, error) {
	select {
	case hashSlot <- struct{}{}:
		defer func() { <-hashSlot }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return argon2.IDKey([]byte(password), salt, 2, 19*1024, 1, 32), nil
}

func Hash(ctx context.Context, password string) (string, error) {
	if err := ValidatePassword(password); err != nil {
		return "", err
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key, err := derive(ctx, password, salt)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("$argon2id$v=19$m=19456,t=2,p=1$%s$%s", base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

func Verify(ctx context.Context, encoded, password string) bool {
	if len(password) > 512 {
		return false
	}
	parts := strings.Split(encoded, "$")
	valid := len(parts) == 6 && parts[1] == "argon2id" && parts[2] == "v=19" && parts[3] == "m=19456,t=2,p=1"
	salt, expected := make([]byte, 16), make([]byte, 32)
	if valid {
		var err error
		salt, err = base64.RawStdEncoding.DecodeString(parts[4])
		valid = err == nil && len(salt) == 16
		expected, err = base64.RawStdEncoding.DecodeString(parts[5])
		valid = valid && err == nil && len(expected) == 32
	}
	// Unknown accounts take the same expensive path without accepting a dummy hash.
	if !valid {
		salt, expected = make([]byte, 16), make([]byte, 32)
	}
	actual, err := derive(ctx, password, salt)
	return err == nil && subtle.ConstantTimeCompare(actual, expected) == 1 && valid
}
