// Package users holds the user identity rules shared by registration, login
// and the seed: how an email is normalized and what counts as valid.
package users

import (
	"errors"
	"net/mail"
	"strings"
	"time"

	"github.com/google/uuid"
)

// MaxEmailLen is the longest accepted email address.
const MaxEmailLen = 254

// User is the public view of an account. It never carries the password hash.
type User struct {
	ID        uuid.UUID `json:"id"`
	Email     string    `json:"email"`
	CreatedAt time.Time `json:"created_at"`
}

// NormalizeEmail trims and lowercases an address. Every email is normalized
// before it is stored or looked up, which is what makes the plain unique
// index on users.email behave case-insensitively.
func NormalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// ValidateEmail checks a normalized address.
func ValidateEmail(email string) error {
	if email == "" {
		return errors.New("email is required")
	}
	if len(email) > MaxEmailLen {
		return errors.New("email is too long")
	}
	addr, err := mail.ParseAddress(email)
	// ParseAddress also accepts "Name <a@b.c>"; requiring the parsed address
	// to equal the input rejects that form.
	if err != nil || addr.Address != email || !strings.Contains(email[strings.LastIndex(email, "@")+1:], ".") {
		return errors.New("email is not a valid address")
	}
	return nil
}
