// Package database owns the PostgreSQL connection pool and the transaction
// helper. The sqlc-generated queries live in the db subpackage.
package database

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"eventmesh/internal/database/db"
)

// Connect opens a pool and verifies the database is reachable.
func Connect(ctx context.Context, url string, maxConns int32) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		// The URL contains the password, so it is deliberately not included.
		return nil, errors.New("parse DATABASE_URL: invalid connection string")
	}
	cfg.MaxConns = maxConns
	cfg.MaxConnIdleTime = 5 * time.Minute
	cfg.HealthCheckPeriod = 30 * time.Second

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("create connection pool: %w", err)
	}
	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	return pool, nil
}

// InTx runs fn inside one transaction. It commits when fn returns nil and
// rolls back otherwise, so writes that must succeed together either all
// happen or none do.
func InTx(ctx context.Context, pool *pgxpool.Pool, fn func(q *db.Queries) error) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	// Rollback after a successful Commit is a no-op that returns ErrTxClosed.
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	if err := fn(db.New(tx)); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}
	return nil
}

// IsNotFound reports whether err is the "no rows" result of a :one query.
func IsNotFound(err error) bool {
	return errors.Is(err, pgx.ErrNoRows)
}
