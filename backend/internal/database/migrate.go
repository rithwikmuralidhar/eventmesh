package database

import (
	"errors"
	"fmt"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5" // registers the pgx5:// driver
	"github.com/golang-migrate/migrate/v4/source/iofs"

	"eventmesh/migrations"
)

// newMigrator builds a migrator over the embedded files. golang-migrate's pgx
// driver is selected by the "pgx5" URL scheme.
func newMigrator(databaseURL string) (*migrate.Migrate, error) {
	src, err := iofs.New(migrations.FS, ".")
	if err != nil {
		return nil, fmt.Errorf("open embedded migrations: %w", err)
	}
	m, err := migrate.NewWithSourceInstance("iofs", src, toPgx5(databaseURL))
	if err != nil {
		return nil, fmt.Errorf("open migrator: %w", err)
	}
	return m, nil
}

func toPgx5(url string) string {
	for _, p := range []string{"postgresql://", "postgres://"} {
		if len(url) > len(p) && url[:len(p)] == p {
			return "pgx5://" + url[len(p):]
		}
	}
	return url
}

// MigrateUp applies every pending migration. It is a no-op when up to date.
func MigrateUp(databaseURL string) error {
	m, err := newMigrator(databaseURL)
	if err != nil {
		return err
	}
	defer closeMigrator(m)
	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("migrate up: %w", err)
	}
	return nil
}

// MigrateDown rolls back exactly one migration.
func MigrateDown(databaseURL string) error {
	m, err := newMigrator(databaseURL)
	if err != nil {
		return err
	}
	defer closeMigrator(m)
	if err := m.Steps(-1); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("migrate down: %w", err)
	}
	return nil
}

// MigrateDownAll rolls back every migration (used by the up/down/up test).
func MigrateDownAll(databaseURL string) error {
	m, err := newMigrator(databaseURL)
	if err != nil {
		return err
	}
	defer closeMigrator(m)
	if err := m.Down(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("migrate down all: %w", err)
	}
	return nil
}

func closeMigrator(m *migrate.Migrate) { _, _ = m.Close() }
