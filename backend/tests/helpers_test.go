// Package tests holds the integration tests. They need a real PostgreSQL and a
// real Redis-compatible server (Memurai on Windows), configured through
// EM_TEST_DATABASE_URL and EM_TEST_REDIS_URL. A missing variable fails the
// test with a message naming it; these tests never skip silently.
package tests

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func requireEnv(t *testing.T, name string) string {
	t.Helper()
	v := os.Getenv(name)
	if v == "" {
		t.Fatalf("%s is not set. Set it in .env (see .env.example) and run scripts\test-integration.ps1", name)
	}
	return v
}

// newRedis connects to the test Redis and returns the client plus a key
// prefix unique to this test, so runs never collide with each other or with
// the development stack. Keys under the prefix are deleted when the test ends.
func newRedis(t *testing.T) (*redis.Client, string) {
	t.Helper()
	opts, err := redis.ParseURL(requireEnv(t, "EM_TEST_REDIS_URL"))
	require.NoError(t, err)
	rdb := redis.NewClient(opts)
	ctx := context.Background()
	require.NoError(t, rdb.Ping(ctx).Err(), "cannot reach the Redis at EM_TEST_REDIS_URL")

	prefix := fmt.Sprintf("emtest-%s", uuid.NewString()[:8])
	t.Cleanup(func() {
		cctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		keys, _ := rdb.Keys(cctx, prefix+":*").Result()
		if len(keys) > 0 {
			_ = rdb.Del(cctx, keys...).Err()
		}
		_ = rdb.Close()
	})
	return rdb, prefix
}
