package config

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[strings.TrimPrefix(k, "EM_")] }
}

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load(env(nil))
	require.NoError(t, err)

	assert.Equal(t, EnvDevelopment, cfg.Env)
	assert.Equal(t, 10, cfg.WorkerCount)
	assert.Equal(t, 5, cfg.MaxRetries)
	assert.Equal(t, 2*time.Second, cfg.InitialRetryDelay)
	assert.Equal(t, 5*time.Minute, cfg.MaxRetryDelay)
	assert.Equal(t, 15*time.Second, cfg.DeliveryTimeout)
	assert.Equal(t, 60*time.Second, cfg.LeaseDuration)
	assert.Equal(t, 7*24*time.Hour, cfg.SessionTTL)
	assert.Empty(t, cfg.SSRFAllowedHosts, "SSRF allowlist must be empty by default")
	assert.Equal(t, []string{"http://127.0.0.1:3000", "http://localhost:3000"}, cfg.CORSAllowedOrigins)
	assert.Len(t, cfg.SecretEncryptionKey, 32)
	assert.True(t, cfg.AllowRegistration, "registration is open unless closed explicitly")

	closed, err := Load(env(map[string]string{"ALLOW_REGISTRATION": "false"}))
	require.NoError(t, err)
	assert.False(t, closed.AllowRegistration)
}

func TestLoadReportsEveryProblemAtOnce(t *testing.T) {
	_, err := Load(env(map[string]string{
		"WORKER_COUNT":          "zero",
		"MAX_RETRIES":           "-1",
		"DELIVERY_TIMEOUT":      "fast",
		"SECRET_ENCRYPTION_KEY": "too-short",
		"SERVE_FRONTEND":        "maybe",
	}))
	require.Error(t, err)
	for _, key := range []string{"WORKER_COUNT", "MAX_RETRIES", "DELIVERY_TIMEOUT", "SECRET_ENCRYPTION_KEY", "SERVE_FRONTEND"} {
		assert.Contains(t, err.Error(), key)
	}
}

func TestLoadRejectsLeaseShorterThanTwiceTheTimeout(t *testing.T) {
	_, err := Load(env(map[string]string{"DELIVERY_TIMEOUT": "40s", "LEASE_DURATION": "60s"}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "LEASE_DURATION")
}

func TestProductionRefusesDevelopmentDefaults(t *testing.T) {
	realKey := base64.StdEncoding.EncodeToString(make([]byte, 32))

	_, err := Load(env(map[string]string{"ENV": "production"}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "SECRET_ENCRYPTION_KEY")
	assert.Contains(t, err.Error(), "DATABASE_URL")

	_, err = Load(env(map[string]string{
		"ENV":                   "production",
		"SECRET_ENCRYPTION_KEY": realKey,
		"DATABASE_URL":          "postgres://app:s3cret@db:5432/eventmesh",
		"SSRF_ALLOWED_HOSTS":    "receiver",
	}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "SSRF_ALLOWED_HOSTS")

	cfg, err := Load(env(map[string]string{
		"ENV":                   "production",
		"SECRET_ENCRYPTION_KEY": realKey,
		"DATABASE_URL":          "postgres://app:s3cret@db:5432/eventmesh",
	}))
	require.NoError(t, err)
	assert.True(t, cfg.CookieSecure)
}

func TestListParsing(t *testing.T) {
	cfg, err := Load(env(map[string]string{
		"SSRF_ALLOWED_HOSTS":   " receiver , ,other ",
		"CORS_ALLOWED_ORIGINS": "http://a.test,http://b.test",
	}))
	require.NoError(t, err)
	assert.Equal(t, []string{"receiver", "other"}, cfg.SSRFAllowedHosts)
	assert.Equal(t, []string{"http://a.test", "http://b.test"}, cfg.CORSAllowedOrigins)
}

func TestPoolSize(t *testing.T) {
	cfg, err := Load(env(map[string]string{"WORKER_COUNT": "25"}))
	require.NoError(t, err)
	assert.Equal(t, int32(29), cfg.PoolSize(true))
	assert.Equal(t, int32(20), cfg.PoolSize(false))

	cfg, err = Load(env(map[string]string{"DB_MAX_CONNS": "7"}))
	require.NoError(t, err)
	assert.Equal(t, int32(7), cfg.PoolSize(true))
}
