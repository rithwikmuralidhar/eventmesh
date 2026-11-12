// Command dbcheck verifies that PostgreSQL accepts a connection on EM_DATABASE_URL.
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/jackc/pgx/v5"

	"eventmesh/internal/config"
)

func main() {
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot connect to EM_DATABASE_URL:", err)
		os.Exit(1)
	}
	defer func() { _ = conn.Close(ctx) }()
	var version string
	if err := conn.QueryRow(ctx, "select version()").Scan(&version); err != nil {
		fmt.Fprintln(os.Stderr, "query failed:", err)
		os.Exit(1)
	}
	fmt.Println(version)
}
