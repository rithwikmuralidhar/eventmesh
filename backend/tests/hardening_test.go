package tests

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"eventmesh/internal/httpapi"
)

// Settings a public (self-hosted) deployment relies on: closed registration,
// browser hardening headers and client IPs taken from a trusted tunnel/proxy.

func TestClosedRegistrationRefusesNewAccountsButKeepsLogin(t *testing.T) {
	open := newStack(t, nil)
	owner := open.newClient()
	owner.register("owner@example.com")

	// Same database and Redis, registration now closed (as after a restart with EM_ALLOW_REGISTRATION=false).
	closed := newStack(t, overrides{"EM_ALLOW_REGISTRATION": "false"}, func(d *httpapi.Deps) { d.Pool = open.pool })
	c := closed.newClient()
	r := c.do("POST", "/auth/register", map[string]string{"email": "stranger@example.com", "password": "correct-horse-1"})
	assert.Equal(t, http.StatusForbidden, r.Status, string(r.Raw))
	assert.Contains(t, string(r.Raw), "registration_closed")

	login := c.do("POST", "/auth/login", map[string]string{"email": "owner@example.com", "password": "correct-horse-1"})
	assert.Equal(t, http.StatusOK, login.Status, string(login.Raw))
}

func TestSecurityHeaders(t *testing.T) {
	s := newStack(t, nil)
	r := s.newClient().do("GET", "/auth/me", nil)
	assert.Equal(t, "nosniff", r.Header.Get("X-Content-Type-Options"))
	assert.Equal(t, "DENY", r.Header.Get("X-Frame-Options"))
	assert.Equal(t, "frame-ancestors 'none'", r.Header.Get("Content-Security-Policy"))
	assert.Empty(t, r.Header.Get("Strict-Transport-Security"), "HSTS only in production (HTTPS)")
}

// Behind a tunnel every request arrives from the tunnel's address. With that
// address trusted, the visitor's IP from X-Forwarded-For is what gets rate
// limited, so one abusive visitor cannot lock everyone else out of login.
func TestTrustedProxyRateLimitsPerVisitor(t *testing.T) {
	s := newStack(t, overrides{"EM_TRUSTED_PROXIES": "127.0.0.1", "EM_AUTH_RATE_LIMIT_PER_MINUTE": "2"})
	c := s.newClient()
	login := func(email, ip string) int {
		return c.do("POST", "/auth/login", map[string]string{"email": email, "password": "wrong-password-1"},
			"X-Forwarded-For", ip).Status
	}
	require.Equal(t, http.StatusUnauthorized, login("a@example.com", "203.0.113.1"))
	require.Equal(t, http.StatusUnauthorized, login("b@example.com", "203.0.113.1"))
	assert.Equal(t, http.StatusTooManyRequests, login("c@example.com", "203.0.113.1"), "third attempt from the same visitor")
	assert.Equal(t, http.StatusUnauthorized, login("d@example.com", "203.0.113.2"), "another visitor is not affected")
}
