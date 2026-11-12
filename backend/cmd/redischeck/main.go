// Command redischeck verifies the Redis-compatible server (Memurai on
// Windows): it answers PING and runs a Lua script that uses redis.call and
// TIME, the two features the queue depends on.
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/redis/go-redis/v9"

	"eventmesh/internal/config"
)

const luaProbe = `
local t = redis.call('TIME')
redis.call('SET', KEYS[1], 'ok', 'PX', 5000)
return {redis.call('GET', KEYS[1]), t[1] * 1000 + math.floor(t[2] / 1000)}
`

func main() {
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	opts, err := redis.ParseURL(cfg.RedisURL)
	if err != nil {
		fmt.Fprintln(os.Stderr, "invalid EM_REDIS_URL:", err)
		os.Exit(1)
	}
	rdb := redis.NewClient(opts)
	defer func() { _ = rdb.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := rdb.Ping(ctx).Err(); err != nil {
		fmt.Fprintln(os.Stderr, "PING failed:", err)
		os.Exit(1)
	}
	res, err := rdb.Eval(ctx, luaProbe, []string{cfg.RedisKeyPrefix + ":probe"}).Slice()
	if err != nil || len(res) != 2 {
		fmt.Fprintln(os.Stderr, "Lua EVAL (redis.call + TIME) failed:", err)
		os.Exit(1)
	}
	info, _ := rdb.Info(ctx, "server").Result()
	fmt.Printf("PONG; Lua EVAL with redis.call and TIME ok (server time %v ms)\n%s\n", res[1], firstVersionLine(info))
}

func firstVersionLine(info string) string {
	for _, line := range splitLines(info) {
		if len(line) > 14 && line[:14] == "redis_version:" {
			return line
		}
	}
	return ""
}

func splitLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, trimCR(s[start:i]))
			start = i + 1
		}
	}
	return append(out, trimCR(s[start:]))
}

func trimCR(s string) string {
	if n := len(s); n > 0 && s[n-1] == '\r' {
		return s[:n-1]
	}
	return s
}
